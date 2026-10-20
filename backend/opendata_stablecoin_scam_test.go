package main

import (
	"context"
	"testing"
)

// seedScamRadar writes Scam Radar data: the Ethereum job synced through
// 2026-10-18 (from 2026-08-01), the TRON job through 2026-10-17 (from
// 2026-10-01), daily stats inside and outside the 30-day windows, and
// counterfeit-symbol contracts.
func seedScamRadar(t *testing.T, store *riskStore) {
	t.Helper()
	ctx := context.Background()
	for day, m := range map[string]map[string]float64{
		"2026-09-17": {"tron_poison_hits": 7},                                                       // before the TRON window
		"2026-09-18": {"eth_poison_hits": 100, "eth_poison_victims": 50, "eth_fake_transfers": 100}, // before the ETH window
		"2026-09-19": {"eth_poison_hits": 5, "eth_poison_victims": 2, "eth_fake_transfers": 1},      // the ETH window's first day
		"2026-10-17": {"tron_poison_hits": 3, "tron_poison_victims": 3},
		"2026-10-18": {"eth_poison_hits": 10, "eth_poison_victims": 4, "eth_fake_transfers": 7, "eth_fake_contracts": 2, "tron_poison_hits": 50}, // after the TRON cursor
	} {
		if err := store.putDailyStats(ctx, day, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.upsertFakeTokens(ctx, "eth", []fakeTokenRow{
		{Contract: "0x00000000000000000000000000000000000000f1", Symbol: "USDT", Transfers: 5, Recipients: 3, FirstSeen: "2026-10-01", LastSeen: "2026-10-18"},
		{Contract: "0x00000000000000000000000000000000000000f2", Symbol: "USD₮", Transfers: 1, Recipients: 1, FirstSeen: "2026-09-20", LastSeen: "2026-09-20"},
		{Contract: "0x00000000000000000000000000000000000000f3", Symbol: "USDC.", Transfers: 2, Recipients: 2, FirstSeen: "2026-10-10", LastSeen: "2026-10-10"},
		{Contract: "0x00000000000000000000000000000000000000f4", Symbol: "usdc", Transfers: 9, Recipients: 9, FirstSeen: "2026-09-01", LastSeen: "2026-09-01"}, // before the window
	}); err != nil {
		t.Fatal(err)
	}
	// The cursors last: Scam Radar refuses rows for days it has already synced.
	for _, st := range []struct{ source, from, cursor string }{
		{scamEthSourceID, "2026-08-01", "2026-10-18"},
		{scamTronSourceID, "2026-10-01", "2026-10-17"},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO sync_state(source, coverage_from, cursor) VALUES (?, ?, ?)
ON CONFLICT(source) DO UPDATE SET coverage_from = excluded.coverage_from, cursor = excluded.cursor`, st.source, st.from, st.cursor); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStableScamCounters(t *testing.T) {
	store := newTestRiskStore(t)
	ctx := context.Background()

	// Not synced: counters are null, never zeros.
	cold, err := store.stableScamCounters(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	if cold.WindowDays != 30 || cold.ETH == nil || cold.ETH.Status != "not_synced" || cold.ETH.PoisonHits != nil ||
		cold.TRON == nil || cold.TRON.PoisonHits != nil {
		t.Fatalf("cold counters = %+v", cold)
	}

	seedScamRadar(t, store)
	all, err := store.stableScamCounters(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	eth := all.ETH
	if eth == nil || eth.Status != "ok" || eth.From != "2026-09-19" || eth.To != "2026-10-18" || eth.DaysWithData != 2 || eth.Partial {
		t.Fatalf("eth = %+v", eth)
	}
	if eth.PoisonHits == nil || *eth.PoisonHits != 15 || eth.PoisonVictims == nil || *eth.PoisonVictims != 6 ||
		eth.CounterfeitTransfers == nil || *eth.CounterfeitTransfers != 8 {
		t.Errorf("eth counts: hits %v victims %v transfers %v", eth.PoisonHits, eth.PoisonVictims, eth.CounterfeitTransfers)
	}
	// Counterfeit contracts active in the window, by the symbol they imitate.
	if eth.CounterfeitContracts["usdt"] != 2 || eth.CounterfeitContracts["usdc"] != 1 || len(eth.CounterfeitContracts) != 2 {
		t.Errorf("counterfeit contracts = %v, want usdt 2, usdc 1", eth.CounterfeitContracts)
	}
	tron := all.TRON
	if tron == nil || tron.From != "2026-09-18" || tron.To != "2026-10-17" || tron.PoisonHits == nil || *tron.PoisonHits != 3 ||
		*tron.PoisonVictims != 3 || tron.DaysWithData != 1 || !tron.Partial || tron.CounterfeitContracts != nil {
		t.Errorf("tron = %+v (coverage starts 2026-10-01, inside the window)", tron)
	}
	// Asset scope: TRON poisoning is USDT only; EURC has no lookalike tracking.
	if usdc, _ := store.stableScamCounters(ctx, "usdc"); usdc.ETH == nil || usdc.TRON != nil {
		t.Errorf("usdc scope = %+v", usdc)
	}
	if usdt, _ := store.stableScamCounters(ctx, "usdt"); usdt.ETH == nil || usdt.TRON == nil {
		t.Errorf("usdt scope = %+v", usdt)
	}
	if eurc, _ := store.stableScamCounters(ctx, "eurc"); eurc.ETH != nil || eurc.TRON != nil || eurc.Note == "" {
		t.Errorf("eurc scope = %+v", eurc)
	}
}
