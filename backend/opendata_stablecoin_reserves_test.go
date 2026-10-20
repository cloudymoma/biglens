package main

import (
	"context"
	"math"
	"testing"
	"time"
)

// Tether reserves (§5.5, §7.2 tab 4): one snapshot per Tether update, the
// coverage ratio (total assets ÷ total liabilities) and the history.
func TestStableTetherReserves(t *testing.T) {
	store := newTestRiskStore(t)
	ctx := context.Background()
	if r, err := store.stableTetherReserves(ctx, 10); err != nil || r.Latest != nil || len(r.History) != 0 {
		t.Fatalf("empty = %+v %v", r, err)
	}
	f := func(v float64) *float64 { return &v }
	for _, s := range []stableTetherSnapshot{
		{UpdateID: 1792020600, TotalAssetsUSD: f(188_000e6), TotalLiabilitiesUSD: f(183_000e6), ShareholderEquityUSD: f(5_000e6)},
		// Appendix B worked numbers: 189,132.0M / 183,581.2M = 1.0302.
		{UpdateID: 1792193400, TotalAssetsUSD: f(189_132.0e6), TotalLiabilitiesUSD: f(183_581.2e6), ShareholderEquityUSD: f(5_466.9e6)},
		{UpdateID: 1792107000, TotalLiabilitiesUSD: f(183_300e6)}, // no assets published: coverage unknown
	} {
		s.ChainsJSON, s.FetchedAt = "[]", time.Unix(s.UpdateID, 0)
		if err := store.insertStableTetherSnapshot(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// The same update id is stored once.
	if err := store.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: 1792193400, TotalAssetsUSD: f(1), ChainsJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	r, err := store.stableTetherReserves(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.History) != 3 || r.History[0].UpdateID != 1792020600 || r.History[2].UpdateID != 1792193400 {
		t.Fatalf("history = %+v, want 3 updates oldest first", r.History)
	}
	l := r.Latest
	if l == nil || l.UpdateID != 1792193400 || l.AsOf != "2026-10-16T23:30:00Z" || l.Coverage == nil || math.Abs(*l.Coverage-189_132.0/183_581.2) > 1e-12 ||
		math.Round(*l.Coverage*1e4)/1e4 != 1.0302 || *l.TotalAssetsUSD != 189_132.0e6 || *l.ShareholderEquityUSD != 5_466.9e6 {
		t.Errorf("latest = %+v", l)
	}
	if r.History[1].Coverage != nil {
		t.Errorf("coverage without assets = %v, want null", *r.History[1].Coverage)
	}
	// Only the newest updates up to the limit, still oldest first.
	if r, _ := store.stableTetherReserves(ctx, 2); len(r.History) != 2 || r.History[0].UpdateID != 1792107000 || r.Latest.UpdateID != 1792193400 {
		t.Errorf("limit 2 = %+v", r.History)
	}
	if c := stableTetherCoverage(f(1), f(0)); c != nil {
		t.Errorf("coverage with zero liabilities = %v", *c)
	}
}
