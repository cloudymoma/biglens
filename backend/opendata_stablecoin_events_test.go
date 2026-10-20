package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStableEventsFeed(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stableEndpoints{}, nil, &clock)
	svc, store := h.stable, h.stable.store

	if err := store.saveStableControls(ctx, nil, []stableControlEvent{
		{DetectedAt: clock.Add(-48 * time.Hour), Asset: "usdc", Chain: "eth", Field: "implementation", Old: "0xa", New: "0xb",
			FirstSeen: clock.Add(-49 * time.Hour), Source: "rpc", ConfirmedBy: stableConfirmedTwoRPCs},
		{DetectedAt: clock.Add(-240 * time.Hour), Asset: "usdt", Chain: "tron", Field: "owner", Old: "Ta", New: "Tb",
			FirstSeen: clock.Add(-241 * time.Hour), Source: "rpc", ConfirmedBy: stableConfirmedTwoSamples},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.saveStableSignals(ctx, nil, []stableSignalEvent{
		{TS: clock.Add(-3 * time.Hour), Asset: "usdt", Signal: "S1", From: "normal", To: "watch", Inputs: map[string]any{"dev_bps": -31.5}},
		{TS: clock.Add(-2 * time.Hour), Asset: "usdt", Signal: "S1", From: "watch", To: "normal"},
		{TS: clock.Add(-1 * time.Hour), Asset: "usdt", Signal: "S1", From: "unknown", To: "normal"}, // warm-up: not an event
		{TS: clock.Add(-1 * time.Hour), Asset: "usdc", Signal: "S12", From: "normal", To: "watch"},  // not a peg signal
	}); err != nil {
		t.Fatal(err)
	}
	snap := func(at time.Time, asset string, v float64) stableSupplySnapshot {
		return stableSupplySnapshot{TS: at, Asset: asset, Chain: "all", Source: "issuer", CirculatingNative: stableF(v), FetchedAt: at}
	}
	if err := store.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{
		snap(clock.Add(-5*time.Hour), "usdc", 72_000e6),
		snap(clock.Add(-4*time.Hour), "usdc", 72_300e6), // +$300M in 60 min: event
		snap(clock.Add(-3*time.Hour), "usdc", 72_400e6), // +$100M: none
		snap(clock.Add(-6*time.Hour), "eurc", 440e6),
		snap(clock.Add(-5*time.Hour), "eurc", 414e6), // −€26M: event
		snap(clock.Add(-30*time.Hour), "usdt", 183_000e6),
		snap(clock.Add(-6*time.Hour), "usdt", 183_500e6), // 24 h apart: none
	}); err != nil {
		t.Fatal(err)
	}
	svc.facts = stableFacts{Assets: map[string]stableAssetFacts{
		"usdc": {CadenceDays: 30, Attestations: []stableAttestation{{PeriodEnd: "2026-08-31", Published: "2026-09-29", Firm: "Deloitte & Touche LLP",
			Type: "attestation", URL: "https://example.com/a.pdf"}},
			Events: []stableFactEvent{{Date: "2024-07-01", Kind: "regulatory", Title: "EU issuer authorized", SourceURL: "https://example.com/r"}}},
	}, Regimes: []stableRegimeFacts{{Regime: "US-GENIUS", KeyDates: []stableKeyDate{{Date: "2025-07-18", Label: "Enacted"},
		{Date: "2027-01-18", Label: "Takes effect at the latest"}}, AsOf: "2026-10-20", SourceURL: "https://example.com/g"}}}
	seedFreezeHistory(t, store)

	evs := svc.buildEvents(ctx, []string{"usdt", "usdc", "eurc"}, clock, 50)
	count := map[string]int{}
	var frozen, destroyed bool
	for i, e := range evs {
		count[e.Kind]++
		if i > 0 && evs[i-1].TS < e.TS {
			t.Errorf("events not newest first: %s before %s", evs[i-1].TS, e.TS)
		}
		frozen = frozen || (e.Kind == "freeze" && strings.Contains(e.Title, "address frozen") && e.URL != "")
		destroyed = destroyed || (e.Kind == "freeze" && strings.Contains(e.Title, "frozen funds destroyed"))
		switch {
		case e.Kind == "control_change" && e.Asset == "usdc" && e.Severity != stableStateWarning:
			t.Errorf("implementation change 2 d ago: severity %s, want warning", e.Severity)
		case e.Kind == "control_change" && e.Asset == "usdt" && e.Severity != stableStateInfo:
			t.Errorf("owner change 10 d ago: severity %s, want info", e.Severity)
		case e.Kind == "peg_state" && e.Detail["to"] == "watch" && e.Severity != stableStateWatch:
			t.Errorf("peg normal → watch: severity %s, want watch", e.Severity)
		case e.Kind == "supply_move" && e.Asset == "usdc" && !strings.Contains(e.Title, "+$300M in 60 min"):
			t.Errorf("usdc supply move title %q", e.Title)
		case e.Kind == "supply_move" && e.Asset == "eurc" && !strings.Contains(e.Title, "−€26M"):
			t.Errorf("eurc supply move title %q", e.Title)
		case e.Kind == "key_date" && e.TS != "2025-07-18T00:00:00Z":
			t.Errorf("key date %s %q: only past key dates belong in the feed", e.TS, e.Title)
		}
	}
	want := map[string]int{"control_change": 2, "peg_state": 2, "supply_move": 2, "attestation": 1, "regulatory": 1, "key_date": 1}
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%s events = %d, want %d (all: %+v)", k, count[k], n, evs)
		}
	}
	if !frozen || !destroyed {
		t.Errorf("freeze events from Address Risk: frozen %v, destroyed %v (%d freeze events)", frozen, destroyed, count["freeze"])
	}
	// The asset filter keeps that asset's events and the regime key dates.
	for _, e := range svc.buildEvents(ctx, []string{"eurc"}, clock, 50) {
		if e.Asset != "eurc" && e.Asset != "all" {
			t.Errorf("eurc feed has %+v", e)
		}
	}
	if got := svc.buildEvents(ctx, []string{"usdt", "usdc", "eurc"}, clock, 3); len(got) != 3 {
		t.Errorf("limit 3: %d events", len(got))
	}
}
