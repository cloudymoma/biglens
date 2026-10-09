package main

// Pure parsing and decision logic for the snapshot lists (OFAC via 0xB10C,
// MEW darklist). No I/O here so every edge case is table-testable.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var listAddressRe = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// normalizeEVMListAddress trims, lowercases and validates one EVM address.
func normalizeEVMListAddress(s string) (string, bool) {
	a, _, err := parseChainAddress("eth", s)
	return a, err == nil
}

// normalizeListAddress trims, normalizes, and checksum-validates one upstream
// value across EVM (lowercase hex), TRON (exact-case base58), and Bitcoin
// (lowercase bech32/bech32m or exact-case base58).
func normalizeListAddress(s string) (string, bool) {
	for _, chain := range []string{"eth", "tron", "btc", "sol"} {
		if a, _, err := parseChainAddress(chain, s); err == nil {
			return a, true
		}
	}
	return "", false
}

// parseOFACText parses a 0xB10C sanctioned_addresses_*.txt file: one address
// per line (EVM, TRON, or Bitcoin).
func parseOFACText(body []byte, label string) ([]listEntry, int) {
	var out []listEntry
	skipped := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		a, ok := normalizeListAddress(line)
		if !ok {
			skipped++
			continue
		}
		out = append(out, listEntry{Address: a, Label: label})
	}
	return out, skipped
}

// parseMEWDarklist parses addresses-darklist.json ([{address, comment, date}]).
func parseMEWDarklist(body []byte) ([]listEntry, int, error) {
	var rows []struct {
		Address string `json:"address"`
		Comment string `json:"comment"`
		Date    string `json:"date"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, 0, fmt.Errorf("decode mew darklist: %w", err)
	}
	var out []listEntry
	skipped := 0
	for _, r := range rows {
		a, ok := normalizeEVMListAddress(r.Address)
		if !ok {
			skipped++
			continue
		}
		out = append(out, listEntry{Address: a, Label: strings.TrimSpace(r.Comment), ListedAt: normalizeListDate(r.Date)})
	}
	return out, skipped, nil
}

// normalizeListDate accepts the two formats seen in the MEW file.
func normalizeListDate(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "1/2/06"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// mergeListEntries dedupes by address, joining distinct non-empty labels with
// "; " (first-seen order) and keeping the earliest non-empty date.
func mergeListEntries(in []listEntry) []listEntry {
	byAddr := map[string]*listEntry{}
	seenLabel := map[string]map[string]bool{}
	var order []string
	for _, e := range in {
		m, ok := byAddr[e.Address]
		if !ok {
			m = &listEntry{Address: e.Address}
			byAddr[e.Address] = m
			seenLabel[e.Address] = map[string]bool{}
			order = append(order, e.Address)
		}
		if e.Label != "" && !seenLabel[e.Address][e.Label] {
			seenLabel[e.Address][e.Label] = true
			if m.Label == "" {
				m.Label = e.Label
			} else {
				m.Label += "; " + e.Label
			}
		}
		if e.ListedAt != "" && (m.ListedAt == "" || e.ListedAt < m.ListedAt) {
			m.ListedAt = e.ListedAt
		}
	}
	sort.Strings(order)
	out := make([]listEntry, 0, len(order))
	for _, a := range order {
		out = append(out, *byAddr[a])
	}
	return out
}

// decideListUpdate is the anti-wipe guard (spec §7.4): reject empty payloads
// and shrinks below half of a list with >= 20 rows, unless the exact same
// payload was already rejected once (then it is a legitimate delisting).
func decideListUpdate(prev syncState, newCount int, newHash string) (bool, string) {
	if newCount == 0 {
		return false, "upstream list empty"
	}
	if prev.RowCount >= 20 && newCount*2 < prev.RowCount {
		if prev.PendingHash != "" && prev.PendingHash == newHash {
			return true, ""
		}
		return false, fmt.Sprintf("upstream shrank %d→%d; kept previous snapshot", prev.RowCount, newCount)
	}
	return true, ""
}
