package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	"cloud.google.com/go/civil"
)

// Scam Radar counters for the Controls & Freezes tab (stablecoin.md §7.2 tab
// 6, §6.3 note 8): 30-day counts of counterfeit USDT/USDC token contracts
// and address-poisoning hits, read from Scam Radar's stores. Read-only: no
// upstream calls, and what counts as a counterfeit stays Scam Radar's rule
// (counterfeitSymbolPattern).

const stableScamWindowDays = 30

const stableScamNote = "Ethereum poisoning counts cover official USDT and USDC transfers together; TRON counts cover USDT. " +
	"Victims are daily counts summed over the window, so a victim hit on two days counts twice. Scam Radar does not track EURC lookalikes."

// Scam Radar's daily metrics per chain (scam_daily_stats).
var stableScamMetrics = map[string][]string{
	"eth":  {"eth_poison_hits", "eth_poison_victims", "eth_lookalikes", "eth_fake_transfers", "eth_fake_contracts"},
	"tron": {"tron_poison_hits", "tron_poison_victims", "tron_candidates", "tron_usdt_freezes"},
}

type StableScamCounters struct {
	WindowDays int                      `json:"window_days"`
	Note       string                   `json:"note"`
	ETH        *StableScamChainCounters `json:"eth"`  // USDT and USDC
	TRON       *StableScamChainCounters `json:"tron"` // USDT only
}

// StableScamChainCounters covers the 30 days From..To that end at the Scam
// Radar job's last synced day. Counts are null until the job has synced.
type StableScamChainCounters struct {
	Status        string `json:"status"` // ok | not_synced
	Source        string `json:"source"` // scam_eth | scam_tron
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	CoverageFrom  string `json:"coverage_from,omitempty"`
	Partial       bool   `json:"partial"` // the job's history starts inside the window
	DaysWithData  int    `json:"days_with_data"`
	PoisonHits    *int64 `json:"poison_hits"`
	PoisonVictims *int64 `json:"poison_victims"`
	// Ethereum only: counterfeit-symbol contracts active in the window, by
	// the symbol they imitate (usdt, usdc), and their transfers.
	CounterfeitContracts map[string]int `json:"counterfeit_contracts,omitempty"`
	CounterfeitTransfers *int64         `json:"counterfeit_transfers,omitempty"`
}

// stableScamCounters returns the counters for one asset ("all": both
// chains). TRON poisoning is USDT only; EURC has none.
func (s *riskStore) stableScamCounters(ctx context.Context, asset string) (StableScamCounters, error) {
	out := StableScamCounters{WindowDays: stableScamWindowDays, Note: stableScamNote}
	wantETH := asset == "all" || asset == "usdt" || asset == "usdc"
	wantTRON := asset == "all" || asset == "usdt"
	if !wantETH && !wantTRON {
		return out, nil
	}
	states, err := s.allSyncStates(ctx)
	if err != nil {
		return out, err
	}
	if wantETH {
		if out.ETH, err = s.stableScamChain(ctx, "eth", scamEthSourceID, states[scamEthSourceID]); err != nil {
			return out, err
		}
	}
	if wantTRON {
		if out.TRON, err = s.stableScamChain(ctx, "tron", scamTronSourceID, states[scamTronSourceID]); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *riskStore) stableScamChain(ctx context.Context, chain, source string, st syncState) (*StableScamChainCounters, error) {
	c := &StableScamChainCounters{Status: "not_synced", Source: source, CoverageFrom: st.CoverageFrom}
	to, err := civil.ParseDate(st.Cursor)
	if st.Cursor == "" || err != nil {
		return c, nil
	}
	from := to.AddDays(-(stableScamWindowDays - 1))
	c.Status, c.From, c.To = "ok", from.String(), to.String()
	c.Partial = st.CoverageFrom != "" && st.CoverageFrom > c.From
	metrics := stableScamMetrics[chain]
	args := []any{c.From, c.To}
	for _, m := range metrics {
		args = append(args, m)
	}
	rs, err := s.db.QueryContext(ctx, `SELECT day, metric, value FROM scam_daily_stats WHERE day >= ? AND day <= ? AND metric IN (?`+
		strings.Repeat(",?", len(metrics)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("scam daily stats: %w", err)
	}
	defer rs.Close()
	sums, days := map[string]float64{}, map[string]bool{}
	for rs.Next() {
		var day, metric string
		var v float64
		if err := rs.Scan(&day, &metric, &v); err != nil {
			return nil, err
		}
		sums[metric] += v
		days[day] = true
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	hits, victims := int64(math.Round(sums[chain+"_poison_hits"])), int64(math.Round(sums[chain+"_poison_victims"]))
	c.PoisonHits, c.PoisonVictims, c.DaysWithData = &hits, &victims, len(days)
	if chain != "eth" {
		return c, nil
	}
	transfers := int64(math.Round(sums["eth_fake_transfers"]))
	c.CounterfeitTransfers = &transfers
	c.CounterfeitContracts = map[string]int{"usdt": 0, "usdc": 0}
	syms, err := s.db.QueryContext(ctx, `SELECT symbol FROM scam_fake_tokens WHERE chain = 'eth' AND last_seen >= ? AND first_seen <= ?`, c.From, c.To)
	if err != nil {
		return nil, fmt.Errorf("scam fake tokens: %w", err)
	}
	defer syms.Close()
	for syms.Next() {
		var sym string
		if err := syms.Scan(&sym); err != nil {
			return nil, err
		}
		// counterfeitSymbolPattern admits USDT, USDC and USD₮ with one
		// trailing non-alphanumeric character.
		if strings.Contains(strings.ToUpper(sym), "USDC") {
			c.CounterfeitContracts["usdc"]++
		} else {
			c.CounterfeitContracts["usdt"]++
		}
	}
	return c, syms.Err()
}
