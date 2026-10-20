package main

import (
	"context"
	"testing"
	"time"
)

func TestEnsureStableSchemaTables(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // idempotent
		if err := s.ensureStableSchema(ctx); err != nil {
			t.Fatalf("ensureStableSchema #%d: %v", i+1, err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'stable\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	want := []string{"stable_chain_daily", "stable_control_events", "stable_control_state", "stable_dex_10m", "stable_peg_5m", "stable_peg_daily",
		"stable_price_daily", "stable_quotes_5m", "stable_rates_daily", "stable_supply_daily", "stable_supply_snapshots", "stable_tether_snapshots"}
	if len(got) != len(want) {
		t.Fatalf("stable_ tables = %v, want exactly %v (M1 supply, M2 peg and M3 control tables)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stable_ tables = %v, want %v", got, want)
		}
	}
}

func TestStableSupplySnapshotsRoundTrip(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	obs := time.Date(2026, 10, 19, 13, 19, 1, 0, time.UTC)
	fetched := obs.Add(40 * time.Second)
	rows := []stableSupplySnapshot{
		{TS: obs, Asset: "eurc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(fxCircleEURCEth), FetchedAt: fetched},
		{TS: obs, Asset: "eurc", Chain: "all", Source: "issuer", CirculatingNative: btcFloatPtr(fxCircleEURCTotal), FetchedAt: fetched},
		// Unknown values stay NULL, never 0.
		{TS: obs, Asset: "usdt", Chain: "eth", Source: "issuer", CirculatingNative: nil, UnissuedNative: btcFloatPtr(fxTetherEthNotIssued), QuarantinedNative: btcFloatPtr(0), FetchedAt: fetched},
	}
	if err := s.insertStableSupplySnapshots(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Re-inserting the same observation is a no-op (same key).
	if err := s.insertStableSupplySnapshots(ctx, rows[:1]); err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	got, err := s.stableSupplySnapshots(ctx, "eurc", "issuer", obs.Add(-time.Hour), obs.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("eurc issuer rows = %d, want 2: %+v", len(got), got)
	}
	if got[0].Chain != "all" || got[1].Chain != "eth" {
		t.Errorf("rows not ordered by ts, chain: %+v", got)
	}
	if !got[1].TS.Equal(obs) || !got[1].FetchedAt.Equal(fetched) {
		t.Errorf("times = %v / %v, want %v / %v", got[1].TS, got[1].FetchedAt, obs, fetched)
	}
	if got[1].CirculatingNative == nil || *got[1].CirculatingNative != fxCircleEURCEth {
		t.Errorf("circulating = %v", got[1].CirculatingNative)
	}
	if got[1].OnchainTotalNative != nil {
		t.Errorf("onchain_total = %v, want NULL", *got[1].OnchainTotalNative)
	}
	usdt, err := s.stableSupplySnapshots(ctx, "usdt", "issuer", obs.Add(-time.Hour), obs.Add(time.Hour))
	if err != nil || len(usdt) != 1 {
		t.Fatalf("usdt rows = %v, %v", usdt, err)
	}
	if usdt[0].CirculatingNative != nil {
		t.Errorf("unknown circulating read back as %v, want nil", *usdt[0].CirculatingNative)
	}
	if usdt[0].QuarantinedNative == nil || *usdt[0].QuarantinedNative != 0 {
		t.Errorf("a known zero must stay 0, got %v", usdt[0].QuarantinedNative)
	}
}

func TestStableDailyUpsertKeepsNewest(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	if err := s.upsertStableSupplyDaily(ctx, []stableSupplyDaily{{Day: "2026-10-19", Asset: "usdc", Source: "issuer", CirculatingNative: btcFloatPtr(2), UpdatedAt: t2}}); err != nil {
		t.Fatal(err)
	}
	// An older observation of the same day must not overwrite a newer one.
	if err := s.upsertStableSupplyDaily(ctx, []stableSupplyDaily{{Day: "2026-10-19", Asset: "usdc", Source: "issuer", CirculatingNative: btcFloatPtr(1), UpdatedAt: t1}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.stableSupplyDailySeries(ctx, "usdc", "issuer", "2026-10-01")
	if err != nil || len(got) != 1 {
		t.Fatalf("daily = %v, %v", got, err)
	}
	if got[0].CirculatingNative == nil || *got[0].CirculatingNative != 2 || got[0].CirculatingUSD != nil {
		t.Errorf("daily row = %+v, want newest value 2 and NULL usd", got[0])
	}

	if err := s.upsertStableChainDaily(ctx, []stableChainDaily{
		{Day: "2026-10-19", Asset: "usdc", Chain: "eth", Source: "onchain", CirculatingNative: btcFloatPtr(5), UpdatedAt: t2},
		{Day: "2026-10-19", Asset: "usdc", Chain: "eth", Source: "onchain", CirculatingNative: btcFloatPtr(4), UpdatedAt: t1},
	}); err != nil {
		t.Fatal(err)
	}
	chains, err := s.stableChainDailySeries(ctx, "usdc", "onchain", "2026-10-01")
	if err != nil || len(chains) != 1 || chains[0].CirculatingNative == nil || *chains[0].CirculatingNative != 5 {
		t.Fatalf("chain daily = %+v, %v; want one row with the newest value 5", chains, err)
	}
}

func TestStableTetherSnapshotsAndRates(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	fetched := time.Date(2026, 10, 19, 13, 30, 0, 0, time.UTC)
	snap := stableTetherSnapshot{UpdateID: fxTetherUpdateID, TotalAssetsUSD: btcFloatPtr(fxTetherAssetsUSD),
		TotalLiabilitiesUSD: btcFloatPtr(fxTetherLiabilitiesUSD), ShareholderEquityUSD: btcFloatPtr(fxTetherEquityUSD),
		ChainsJSON: `[{"chain":"Ethereum"}]`, FetchedAt: fetched}
	for i := 0; i < 2; i++ { // one row per Tether update id
		if err := s.insertStableTetherSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
	snaps, err := s.stableTetherSnapshots(ctx, 10)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("tether snapshots = %+v, %v", snaps, err)
	}
	if snaps[0].UpdateID != fxTetherUpdateID || *snaps[0].TotalLiabilitiesUSD != fxTetherLiabilitiesUSD || snaps[0].ChainsJSON != snap.ChainsJSON {
		t.Errorf("tether snapshot = %+v", snaps[0])
	}

	if err := s.upsertStableRatesDaily(ctx, []stableRateDaily{
		{Day: "2026-10-16", Series: stableSeriesECBEURUSD, Value: fxECBEURUSD2, FetchedAt: fetched},
		{Day: "2026-10-16", Series: stableSeriesECBESTRPct, Value: fxECBESTR2, FetchedAt: fetched},
	}); err != nil {
		t.Fatal(err)
	}
	// A revised value for the same day replaces the old one.
	if err := s.upsertStableRatesDaily(ctx, []stableRateDaily{{Day: "2026-10-16", Series: stableSeriesECBEURUSD, Value: fxECBEURUSD2 + 0.0001, FetchedAt: fetched.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	fx, err := s.stableRatesSeries(ctx, stableSeriesECBEURUSD, "2026-10-01")
	if err != nil || len(fx) != 1 || fx[0].Value != fxECBEURUSD2+0.0001 {
		t.Fatalf("eurusd series = %+v, %v", fx, err)
	}
}

func TestPruneStableSnapshotsKeepsDaily(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-stableSnapshotRetention - time.Hour)
	recent := now.Add(-stableSnapshotRetention + time.Hour)
	if err := s.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{
		{TS: old, Asset: "usdc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(1), FetchedAt: old},
		{TS: recent, Asset: "usdc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(2), FetchedAt: recent},
	}); err != nil {
		t.Fatal(err)
	}
	oldDay := old.Format("2006-01-02")
	if err := s.upsertStableSupplyDaily(ctx, []stableSupplyDaily{{Day: oldDay, Asset: "usdc", Source: "issuer", CirculatingNative: btcFloatPtr(1), UpdatedAt: old}}); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertStableChainDaily(ctx, []stableChainDaily{{Day: oldDay, Asset: "usdc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(1), UpdatedAt: old}}); err != nil {
		t.Fatal(err)
	}
	if err := s.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: old.Unix(), ChainsJSON: "[]", FetchedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertStableRatesDaily(ctx, []stableRateDaily{{Day: oldDay, Series: stableSeriesUST13WPct, Value: 4.18, FetchedAt: old}}); err != nil {
		t.Fatal(err)
	}

	n, err := s.pruneStable(ctx, now)
	if err != nil {
		t.Fatalf("pruneStable: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1 (only the snapshot older than 180 d)", n)
	}
	snaps, _ := s.stableSupplySnapshots(ctx, "usdc", "issuer", now.AddDate(-1, 0, 0), now)
	if len(snaps) != 1 || !snaps[0].TS.Equal(recent) {
		t.Errorf("snapshots after prune = %+v, want only the recent one", snaps)
	}
	if d, _ := s.stableSupplyDailySeries(ctx, "usdc", "issuer", "2000-01-01"); len(d) != 1 {
		t.Errorf("supply daily rows = %d, want 1 (kept forever)", len(d))
	}
	if d, _ := s.stableChainDailySeries(ctx, "usdc", "issuer", "2000-01-01"); len(d) != 1 {
		t.Errorf("chain daily rows = %d, want 1 (kept forever)", len(d))
	}
	if ts, _ := s.stableTetherSnapshots(ctx, 10); len(ts) != 1 {
		t.Errorf("tether snapshots = %d, want 1 (kept forever)", len(ts))
	}
	if r, _ := s.stableRatesSeries(ctx, stableSeriesUST13WPct, "2000-01-01"); len(r) != 1 {
		t.Errorf("rates rows = %d, want 1 (kept forever)", len(r))
	}
}
