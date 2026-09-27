package main

// One-hop association analysis (spec §8.4): the target's newest Etherscan
// transactions are filtered against address poisoning, then matched against
// the local Critical/Warning pool (OFAC, currently frozen USDT/USDC, MEW).
// One clue per counterparty. Etherscan data is never written to SQLite.

import (
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	riskZeroAddress     = "0x0000000000000000000000000000000000000000"
	riskMaxAssociations = 200
	riskPoisoningNote   = "Inbound transfers can be unsolicited (e.g. dusting / address poisoning)."
)

// riskTokenAllowlist: symbol and decimals come from here, never from
// Etherscan's attacker-controlled tokenSymbol/tokenDecimal (verified on-chain
// 2026-09-26).
var riskTokenAllowlist = map[string]struct {
	Symbol   string
	Decimals int
}{
	"0xdac17f958d2ee523a2206206994597c13d831ec7": {"USDT", 6},
	"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": {"USDC", 6},
	"0x6b175474e89094c44da98b954eedeac495271d0f": {"DAI", 18},
	"0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": {"WETH", 18},
	"0x2260fac5e5542a773aa44fbcfedf7c193bc2c599": {"WBTC", 8},
}

var riskTokenAllowlistSymbols = []string{"USDT", "USDC", "DAI", "WETH", "WBTC"}

// riskPool maps a lowercase address to the local lists it is on:
// "ofac", "stablecoin" (currently frozen), "mew_darklist".
type riskPool map[string][]string

type riskAssociation struct {
	Counterparty        string   `json:"counterparty"`
	CounterpartySources []string `json:"counterparty_sources"`
	TxCount             int      `json:"tx_count"`
	Channels            []string `json:"channels"`  // txlist | tokentx | internal
	Direction           string   `json:"direction"` // in | out | both
	TxHash              string   `json:"tx_hash"`   // newest matching transaction
	Amount              string   `json:"amount"`    // of that transaction, with unit
}

type riskListScope struct {
	N        int    `json:"n"`
	OldestAt string `json:"oldest_at"`
}

type riskAssociationScope struct {
	TxList         riskListScope `json:"txlist"`
	TokenTx        riskListScope `json:"tokentx"`
	TxListInternal riskListScope `json:"txlistinternal"`
	Hops           int           `json:"hops"`
	TokenAllowlist []string      `json:"token_allowlist"`
	Truncated      bool          `json:"truncated"`
}

// formatUnits renders an integer amount exactly, trimming trailing zeros:
// dust amounts (the poisoning signal) must not round to 0.
func formatUnits(n *big.Int, decimals int) string {
	s := new(big.Int).Abs(n).String()
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	whole, frac := s[:len(s)-decimals], strings.TrimRight(s[len(s)-decimals:], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}

func unixToRFC3339(ts string) string {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ""
	}
	return time.Unix(n, 0).UTC().Format(time.RFC3339)
}

func listScope(rows []etherscanRow) riskListScope {
	sc := riskListScope{N: len(rows)}
	var oldest int64
	for _, r := range rows {
		if n, err := strconv.ParseInt(r.TimeStamp, 10, 64); err == nil && (oldest == 0 || n < oldest) {
			oldest = n
		}
	}
	if oldest > 0 {
		sc.OldestAt = time.Unix(oldest, 0).UTC().Format(time.RFC3339)
	}
	return sc
}

// counterpartyOf applies spec §8.4 rule 5 (also used for txlist and tokentx):
// from == target → to (contractAddress for creations), out; to == target or
// contractAddress == target → from, in. Rows matching neither are dropped.
func counterpartyOf(r etherscanRow, target string) (string, string) {
	from, to, created := strings.ToLower(r.From), strings.ToLower(r.To), strings.ToLower(r.ContractAddress)
	switch {
	case from == target:
		if to == "" {
			return created, "out"
		}
		return to, "out"
	case to == target, to == "" && created == target:
		return from, "in"
	}
	return "", ""
}

type assocAgg struct {
	hashes     map[string]bool
	channels   map[string]bool
	directions map[string]bool
	newestTS   int64
	newestHash string
	newestAmt  string
}

// associationClues matches the three lists against the pool. asOf is the
// query time.
func associationClues(target string, txlist, tokentx, internal []etherscanRow, pool riskPool, asOf string) ([]riskClue, *riskAssociationScope) {
	aggs := map[string]*assocAgg{}
	unmatched := 0
	add := func(channel string, r etherscanRow, amountUnit string, decimals int) {
		if channel != "tokentx" && r.IsError != "0" {
			return // failed calls moved no value
		}
		v, ok := new(big.Int).SetString(r.Value, 10)
		if !ok || v.Sign() == 0 {
			return // zero-value transferFrom is the classic poisoning vector
		}
		cp, dir := counterpartyOf(r, target)
		if dir == "" {
			unmatched++ // should not happen per Etherscan docs; counted, never logged with addresses
			return
		}
		if cp == "" || cp == target || cp == riskZeroAddress || len(pool[cp]) == 0 {
			return
		}
		a := aggs[cp]
		if a == nil {
			a = &assocAgg{hashes: map[string]bool{}, channels: map[string]bool{}, directions: map[string]bool{}}
			aggs[cp] = a
		}
		a.hashes[r.Hash] = true
		a.channels[channel] = true
		a.directions[dir] = true
		// Lists are newest first and txlist is read first, so on a timestamp
		// tie the first row seen (the outer transaction) stays the newest.
		ts, err := strconv.ParseInt(r.TimeStamp, 10, 64)
		if err != nil {
			ts = 0
		}
		if a.newestHash == "" || ts > a.newestTS {
			a.newestTS, a.newestHash, a.newestAmt = ts, r.Hash, formatUnits(v, decimals)+" "+amountUnit
		}
	}
	for _, r := range txlist {
		add("txlist", r, "ETH", 18)
	}
	for _, r := range tokentx {
		tok, ok := riskTokenAllowlist[strings.ToLower(r.ContractAddress)]
		if !ok {
			continue // counterfeit or unknown token contract
		}
		r.IsError = "0"
		add("tokentx", r, tok.Symbol, tok.Decimals)
	}
	for _, r := range internal {
		add("internal", r, "ETH", 18)
	}

	if unmatched > 0 {
		slog.Warn("address_risk association rows matched neither side", "count", unmatched)
	}
	var clues []riskClue
	for cp, a := range aggs {
		dir := "out"
		switch {
		case a.directions["in"] && a.directions["out"]:
			dir = "both"
		case a.directions["in"]:
			dir = "in"
		}
		var channels []string
		for _, c := range []string{"txlist", "tokentx", "internal"} {
			if a.channels[c] {
				channels = append(channels, c)
			}
		}
		title := map[string]string{"out": "Sent to %s", "in": "Received from %s", "both": "Sent to and received from %s"}[dir]
		detail := "Counterparty is on: " + strings.Join(poolLabels(pool[cp]), ", ")
		if dir != "out" {
			detail += ". " + riskPoisoningNote
		}
		var observed *string // null when no timestamp parsed (spec §8.3)
		if a.newestTS > 0 {
			t := time.Unix(a.newestTS, 0).UTC().Format(time.RFC3339)
			observed = &t
		}
		clues = append(clues, riskClue{
			Severity: sevAssociation, Source: "etherscan", Code: "association",
			Title: fmt.Sprintf(title, cp), Detail: detail, ObservedAt: observed, AsOf: asOf,
			RefURL: "https://etherscan.io/tx/" + a.newestHash,
			Association: &riskAssociation{Counterparty: cp, CounterpartySources: pool[cp], TxCount: len(a.hashes),
				Channels: channels, Direction: dir, TxHash: a.newestHash, Amount: a.newestAmt},
		})
	}
	// Counterparty severity (OFAC/frozen before MEW-only), then tx count.
	sort.Slice(clues, func(i, j int) bool {
		ri, rj := poolRank(clues[i].Association.CounterpartySources), poolRank(clues[j].Association.CounterpartySources)
		if ri != rj {
			return ri < rj
		}
		if clues[i].Association.TxCount != clues[j].Association.TxCount {
			return clues[i].Association.TxCount > clues[j].Association.TxCount
		}
		return clues[i].Association.Counterparty < clues[j].Association.Counterparty
	})
	scope := &riskAssociationScope{TxList: listScope(txlist), TokenTx: listScope(tokentx), TxListInternal: listScope(internal),
		Hops: 1, TokenAllowlist: riskTokenAllowlistSymbols}
	if len(clues) > riskMaxAssociations {
		clues, scope.Truncated = clues[:riskMaxAssociations], true
	}
	return clues, scope
}

// poolRank: 0 when the counterparty is Critical locally (OFAC or frozen), 1 for MEW only.
func poolRank(sources []string) int {
	for _, s := range sources {
		if s == "ofac" || s == "stablecoin" {
			return 0
		}
	}
	return 1
}

func poolLabels(sources []string) []string {
	names := map[string]string{"ofac": "OFAC SDN list", "stablecoin": "USDT/USDC frozen", "mew_darklist": "MEW darklist"}
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, names[s])
	}
	return out
}
