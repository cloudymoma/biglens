package main

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

type trustLevel int

const (
	trustNone trustLevel = iota
	trustLow             // sender of a genuine non-dust incoming transfer
	trustHigh            // recipient of a genuine non-dust outgoing transfer (signed by user)
)

var dustBelow = map[string]string{
	"USDT": "1",
	"USDC": "1",
	"ETH":  "0.0001",
	"TRX":  "1",
}

var mimicHomoglyphs = map[rune]rune{
	'₮': 'T',
	// Cyrillic lookalikes
	'А': 'A', 'а': 'A',
	'В': 'B', 'в': 'B',
	'С': 'C', 'с': 'C',
	'Е': 'E', 'е': 'E',
	'Н': 'H', 'н': 'H',
	'І': 'I', 'і': 'I',
	'К': 'K', 'к': 'K',
	'М': 'M', 'м': 'M',
	'О': 'O', 'о': 'O',
	'Р': 'P', 'р': 'P',
	'Ѕ': 'S', 'ѕ': 'S',
	'Т': 'T', 'т': 'T',
	'У': 'Y', 'у': 'Y',
	'Х': 'X', 'х': 'X',
	'Ү': 'Y', 'ү': 'Y',
	// Greek lookalikes
	'Α': 'A', 'Β': 'B', 'Ε': 'E', 'Ζ': 'Z', 'Η': 'H',
	'Ι': 'I', 'Κ': 'K', 'Μ': 'M', 'Ν': 'N', 'Ο': 'O',
	'Ρ': 'P', 'Τ': 'T', 'Υ': 'Y', 'Χ': 'X',
	// Superscript / subscript zero
	'⁰': '0', '₀': '0',
}

// normalizeMimicSymbol normalizes an upstream token symbol to detect
// counterfeit tokens mimicking USDT / USDC (spec §5.4, review-2 #6).
func normalizeMimicSymbol(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0 // fullwidth ASCII to standard ASCII
		}
		if mapped, ok := mimicHomoglyphs[r]; ok {
			r = mapped
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	out := b.String()
	out = strings.TrimSuffix(out, ".E")
	out = strings.TrimSuffix(out, "0")
	return out
}

// lookalike reports whether addresses a and b are distinct addresses that share
// the same first 4 and last 4 characters after the chain prefix (0x for EVM,
// case-insensitive; T for TRON, case-sensitive; spec §5.5).
func lookalike(a, b string, fam chainFamily) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch fam {
	case familyEVM:
		a = strings.TrimPrefix(strings.ToLower(a), "0x")
		b = strings.TrimPrefix(strings.ToLower(b), "0x")
	case familyTron:
		a = strings.TrimPrefix(a, "T")
		b = strings.TrimPrefix(b, "T")
	default:
		return false
	}
	if a == b || len(a) < 8 || len(b) < 8 {
		return false
	}
	return a[:4] == b[:4] && a[len(a)-4:] == b[len(b)-4:]
}

func canonFlagAddr(addr string, fam chainFamily) string {
	s := strings.TrimSpace(addr)
	if fam == familyEVM {
		return strings.ToLower(s)
	}
	return s
}

func parseAmountRat(amt string) (*big.Rat, bool) {
	s := strings.TrimSpace(amt)
	if s == "" {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

func isZeroAmount(amt string) bool {
	r, ok := parseAmountRat(amt)
	return ok && r.Sign() == 0
}

func isPositiveAmount(amt string) bool {
	r, ok := parseAmountRat(amt)
	return ok && r.Sign() > 0
}

func isDustAmount(amt, asset string) bool {
	threshStr, ok := dustBelow[asset]
	if !ok {
		return false
	}
	rAmt, okAmt := parseAmountRat(amt)
	rThresh, okThresh := parseAmountRat(threshStr)
	return okAmt && okThresh && rAmt.Sign() > 0 && rAmt.Cmp(rThresh) < 0
}

func hasLookalikeInTrust(cp string, trust map[string]trustLevel, fam chainFamily) bool {
	for trustedAddr, lvl := range trust {
		if lvl > trustNone && lookalike(cp, trustedAddr, fam) {
			return true
		}
	}
	return false
}

func hasHighTrustLookalike(cp string, trust map[string]trustLevel, fam chainFamily) bool {
	for trustedAddr, lvl := range trust {
		if lvl == trustHigh && lookalike(cp, trustedAddr, fam) {
			return true
		}
	}
	return false
}

// applyFlags scans txs in chronological order (oldest -> newest; tie-breaking
// by block and tx_hash) to populate txs[i].Flags in place without mutating the
// order of txs (spec §5.5, Task 9).
func applyFlags(txs []payTx, asset string, fam chainFamily) {
	order := make([]int, len(txs))
	parsedTime := make([]int64, len(txs))
	for i := range txs {
		order[i] = i
		if t, err := time.Parse(time.RFC3339, txs[i].Timestamp); err == nil {
			parsedTime[i] = t.UnixNano()
		}
	}

	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if fam == familyEVM {
			if txs[ia].Block != txs[ib].Block {
				return txs[ia].Block < txs[ib].Block
			}
			if parsedTime[ia] != parsedTime[ib] {
				return parsedTime[ia] < parsedTime[ib]
			}
			return txs[ia].TxHash < txs[ib].TxHash
		}
		if parsedTime[ia] != parsedTime[ib] {
			return parsedTime[ia] < parsedTime[ib]
		}
		if txs[ia].Block != txs[ib].Block {
			return txs[ia].Block < txs[ib].Block
		}
		return txs[ia].TxHash < txs[ib].TxHash
	})

	trust := make(map[string]trustLevel)
	tainted := make(map[string]bool)
	knownPoisoners := make(map[string]bool)
	for i := range txs {
		cp := canonFlagAddr(txs[i].Counterparty, fam)
		if cp == "" {
			continue
		}
		if slices.Contains(txs[i].CounterpartyHits, "scam_lookalikes") || slices.Contains(txs[i].Flags, "lookalike_known") {
			tainted[cp] = true
			knownPoisoners[cp] = true
		}
	}

	for _, idx := range order {
		tx := &txs[idx]
		hadListed := slices.Contains(tx.Flags, "counterparty_listed")
		flags := []string{}

		isOfficial := tx.TokenTier == tierNative || tx.TokenTier == tierBridged
		isCounterfeit := tx.TokenTier == tierCounterfeit
		zeroVal := isOfficial && !tx.Failed && isZeroAmount(tx.Amount)
		posVal := isOfficial && !tx.Failed && isPositiveAmount(tx.Amount)
		dustVal := isOfficial && !tx.Failed && isDustAmount(tx.Amount, asset)
		cp := canonFlagAddr(tx.Counterparty, fam)

		if tx.Direction == "out" && tx.Failed {
			flags = append(flags, "failed")
		}
		if isCounterfeit {
			flags = append(flags, "counterfeit_token")
		}
		if zeroVal {
			flags = append(flags, "zero_value")
		}
		if tx.Direction == "in" && dustVal {
			flags = append(flags, "dust")
		}

		if cp != "" && trust[cp] != trustHigh {
			switch {
			case tx.Direction == "in" || (tx.Direction == "out" && (zeroVal || isCounterfeit)):
				if hasLookalikeInTrust(cp, trust, fam) {
					flags = append(flags, "lookalike")
					tainted[cp] = true
				}
			case tx.Direction == "out" && posVal:
				if hasLookalikeInTrust(cp, trust, fam) {
					flags = append(flags, "sent_to_lookalike")
					tainted[cp] = true
				}
			}
		}

		if cp != "" && knownPoisoners[cp] {
			if !slices.Contains(flags, "lookalike_known") {
				flags = append(flags, "lookalike_known")
			}
			if tx.Direction == "out" && posVal && !slices.Contains(flags, "sent_to_lookalike") {
				flags = append(flags, "sent_to_lookalike")
			}
		}
		if hadListed && !slices.Contains(flags, "counterparty_listed") {
			flags = append(flags, "counterparty_listed")
		}

		tx.Flags = flags

		// Update trust set after evaluating flags for this row.
		if isOfficial && !tx.Failed && posVal && !dustVal && cp != "" && !knownPoisoners[cp] {
			switch tx.Direction {
			case "in":
				if !tainted[cp] && trust[cp] < trustLow {
					trust[cp] = trustLow
				}
			case "out":
				if !hasHighTrustLookalike(cp, trust, fam) {
					trust[cp] = trustHigh
					delete(tainted, cp)
				}
			}
		}
	}
}

func hasListedCounterpartyHit(hits []string) bool {
	for _, h := range hits {
		switch h {
		case "ofac", "mew_darklist", "stablecoin", "tron_stablecoin":
			return true
		}
	}
	return false
}

// applyFakeTokenSet re-tiers ETH token transfers whose contract is in the local
// scam_fake_tokens corpus from tierOther to tierCounterfeit (spec P2.2 #2).
func applyFakeTokenSet(ctx context.Context, store *riskStore, network string, txs []payTx) error {
	if network != "eth" {
		return nil
	}
	hasOther := false
	for i := range txs {
		if txs[i].TokenTier == tierOther && strings.TrimSpace(txs[i].TokenContract) != "" {
			hasOther = true
			break
		}
	}
	if !hasOther {
		return nil
	}
	if store == nil {
		return errors.New("unavailable")
	}
	fakes, err := store.fakeTokenSet(ctx, "eth")
	if err != nil {
		return errors.New("unavailable")
	}
	for i := range txs {
		if txs[i].TokenTier != tierOther {
			continue
		}
		c := strings.ToLower(strings.TrimSpace(txs[i].TokenContract))
		if c == "" || !fakes[c] {
			continue
		}
		if _, isReg := lookupRegistryToken(network, c); isReg {
			continue
		}
		txs[i].TokenTier = tierCounterfeit
		if txs[i].Flags != nil && !slices.Contains(txs[i].Flags, "counterfeit_token") {
			txs[i].Flags = append(txs[i].Flags, "counterfeit_token")
		}
	}
	return nil
}

// applyLocalHits queries local OFAC / MEW / stablecoin-freeze / scam-lookalike
// corpora in a single batch via store.riskPoolForAddresses, re-tiers ETH fake
// tokens via scam_fake_tokens, and tags matching rows with counterparty_listed,
// lookalike_known, and (for real outgoing to known poisoners) sent_to_lookalike.
func applyLocalHits(ctx context.Context, store *riskStore, network string, txs []payTx) error {
	if store == nil {
		return errors.New("unavailable")
	}
	if err := applyFakeTokenSet(ctx, store, network, txs); err != nil {
		return err
	}
	fam := chains[network].Family
	seen := make(map[string]bool, len(txs))
	var addrs []string
	for _, tx := range txs {
		cp := canonFlagAddr(tx.Counterparty, fam)
		if cp == "" || seen[cp] {
			continue
		}
		seen[cp] = true
		addrs = append(addrs, cp)
	}
	if len(addrs) == 0 {
		return nil
	}
	pool, err := store.riskPoolForAddresses(ctx, network, addrs)
	if err != nil {
		return errors.New("unavailable")
	}
	for i := range txs {
		cp := canonFlagAddr(txs[i].Counterparty, fam)
		hits := pool[cp]
		if len(hits) == 0 {
			continue
		}
		txs[i].CounterpartyHits = slices.Clone(hits)
		if hasListedCounterpartyHit(hits) && !slices.Contains(txs[i].Flags, "counterparty_listed") {
			txs[i].Flags = append(txs[i].Flags, "counterparty_listed")
		}
		if slices.Contains(hits, "scam_lookalikes") {
			if !slices.Contains(txs[i].Flags, "lookalike_known") {
				txs[i].Flags = append(txs[i].Flags, "lookalike_known")
			}
			isOfficial := txs[i].TokenTier == tierNative || txs[i].TokenTier == tierBridged
			if txs[i].Direction == "out" && isOfficial && !txs[i].Failed && isPositiveAmount(txs[i].Amount) && !slices.Contains(txs[i].Flags, "sent_to_lookalike") {
				txs[i].Flags = append(txs[i].Flags, "sent_to_lookalike")
			}
		}
	}
	return nil
}
