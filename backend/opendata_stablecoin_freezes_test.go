package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

// seedFreezeHistory writes Address Risk freeze history: Ethereum (USDT and
// USDC from 2026-06-01, EURC joining the query on 2026-09-01) synced through
// 2026-10-19, TRON from 2026-07-01 through 2026-10-18.
func seedFreezeHistory(t *testing.T, store *riskStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	ev := func(n int64, token, action, addr, amount, day string) stablecoinEvent {
		return stablecoinEvent{TxHash: "0x" + strings.Repeat("0", 63) + string(rune('a'+n)), LogIndex: n, Token: token, Action: action,
			Address: addr, Amount: amount, BlockNumber: 20_000_000 + n, BlockTime: day + "T12:00:00Z"}
	}
	// Before EURC was in the query: USDT/USDC only.
	early := []stablecoinEvent{
		ev(1, "USDT", "freeze", "0xc0", "", "2026-06-15"), // outside 90 days
		ev(2, "USDC", "freeze", "0xe0", "", "2026-07-25"),
		ev(3, "USDT", "freeze", "0xb0", "", "2026-08-15"),
	}
	if err := store.insertStablecoinEvents(ctx, early, "2026-06-01", "2026-08-31", now); err != nil {
		t.Fatal(err)
	}
	late := []stablecoinEvent{
		ev(4, "USDC", "freeze", "0xd0", "", "2026-09-25"),
		ev(5, "USDT", "freeze", "0xa0", "", "2026-10-01"),
		ev(6, "USDT", "destroy", "0xa0", "1234567890", "2026-10-02"), // 1,234.57 USDT
		ev(7, "EURC", "freeze", "0xf0", "", "2026-10-05"),
		ev(8, "USDT", "unfreeze", "0xb0", "", "2026-10-10"),
	}
	w := stablecoinWindow{Start: civil.Date{Year: 2026, Month: 9, Day: 1}, End: civil.Date{Year: 2026, Month: 10, Day: 20}, NewCursor: "2026-10-19"}
	if err := store.insertStablecoinWindow(ctx, late, w, now); err != nil {
		t.Fatal(err)
	}
	tron := []stablecoinEvent{
		ev(9, "USDT", "freeze", "TH", "", "2026-07-10"), // outside 90 days
		ev(10, "USDT", "freeze", "TG", "", "2026-10-17"),
		ev(11, "USDT", "destroy", "TG", "500000000", "2026-10-18"), // 500.00 USDT
	}
	if err := store.insertTronStablecoinEvents(ctx, tron, "2026-07-01", "2026-10-18", now); err != nil {
		t.Fatal(err)
	}
}

func freezeStatsFor(stats []StableFreezeStats, asset, chain string) (StableFreezeStats, bool) {
	for _, s := range stats {
		if s.Asset == asset && s.Chain == chain {
			return s, true
		}
	}
	return StableFreezeStats{}, false
}

func TestStableFreezeStats(t *testing.T) {
	store := newTestRiskStore(t)
	ctx := context.Background()

	// Nothing synced: every token says so, with no counts (never zeros).
	cold, err := store.stableFreezeStats(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(cold) != 4 {
		t.Fatalf("cold stats = %+v, want usdt eth/tron, usdc eth, eurc eth", cold)
	}
	for _, s := range cold {
		if s.Status != "not_synced" || len(s.Windows) != 0 || s.FrozenNow != nil {
			t.Errorf("cold %s %s = %+v", s.Asset, s.Chain, s)
		}
	}
	if e, _ := freezeStatsFor(cold, "eurc", "eth"); e.CoverageNote != "EURC freeze history not synced yet." {
		t.Errorf("cold eurc note = %q", e.CoverageNote)
	}

	seedFreezeHistory(t, store)
	all, err := store.stableFreezeStats(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	type win struct {
		freezes, unfreezes, destroys int
		destroyed                    string // "" = null
		partial                      bool
	}
	want := map[string][2]win{
		"usdt|eth":  {{1, 1, 1, "1234.57", false}, {2, 1, 1, "1234.57", false}},
		"usdc|eth":  {{1, 0, 0, "", false}, {2, 0, 0, "", false}},
		"eurc|eth":  {{1, 0, 0, "", false}, {1, 0, 0, "", true}}, // EURC coverage starts inside the 90-day window
		"usdt|tron": {{1, 0, 1, "500.00", false}, {1, 0, 1, "500.00", false}},
	}
	frozen := map[string]int{"usdt|eth": 2, "usdc|eth": 2, "eurc|eth": 1, "usdt|tron": 2}
	for key, w := range want {
		parts := strings.Split(key, "|")
		s, ok := freezeStatsFor(all, parts[0], parts[1])
		if !ok || s.Status != "ok" || len(s.Windows) != 2 {
			t.Errorf("%s = %+v", key, s)
			continue
		}
		for i, days := range []int{30, 90} {
			got := s.Windows[i]
			destroyed := ""
			if got.DestroyedNative != nil {
				destroyed = *got.DestroyedNative
			}
			if got.Days != days || got.Freezes != w[i].freezes || got.Unfreezes != w[i].unfreezes || got.Destroys != w[i].destroys ||
				destroyed != w[i].destroyed || got.Partial != w[i].partial {
				t.Errorf("%s %d-day window = %+v (destroyed %q), want %+v", key, days, got, destroyed, w[i])
			}
		}
		if s.FrozenNow == nil || *s.FrozenNow != frozen[key] {
			t.Errorf("%s frozen now = %v, want %d", key, s.FrozenNow, frozen[key])
		}
	}
	usdt, _ := freezeStatsFor(all, "usdt", "eth")
	if w := usdt.Windows[0]; w.From != "2026-09-20" || w.To != "2026-10-19" || usdt.Windows[1].From != "2026-07-22" || usdt.Cursor != "2026-10-19" ||
		usdt.CoverageFrom != "2026-06-01" || usdt.Token != "USDT" || usdt.Source != stablecoinSourceID {
		t.Errorf("usdt eth coverage = %+v", usdt)
	}
	if tr, _ := freezeStatsFor(all, "usdt", "tron"); tr.Windows[0].To != "2026-10-18" || tr.Source != tronStablecoinSourceID {
		t.Errorf("tron windows end at the TRON cursor: %+v", tr)
	}
	eurc, _ := freezeStatsFor(all, "eurc", "eth")
	if eurc.CoverageFrom != "2026-09-01" || eurc.CoverageNote != "EURC freeze history from 2026-09-01." {
		t.Errorf("eurc coverage = %q, note %q", eurc.CoverageFrom, eurc.CoverageNote)
	}
	// Recent events: newest first, destroy amounts in whole tokens, links.
	if len(usdt.Recent) != 5 || usdt.Recent[0].Action != "unfreeze" || usdt.Recent[1].Action != "destroy" || usdt.Recent[1].Amount != "1234.57" ||
		!strings.HasPrefix(usdt.Recent[0].TxURL, "https://etherscan.io/tx/0x") || usdt.Recent[0].BlockTime != "2026-10-10T12:00:00Z" {
		t.Errorf("usdt recent = %+v", usdt.Recent)
	}
	if tr, _ := freezeStatsFor(all, "usdt", "tron"); len(tr.Recent) != 3 || !strings.HasPrefix(tr.Recent[0].TxURL, "https://tronscan.org/#/transaction/") {
		t.Errorf("tron recent = %+v", tr.Recent)
	}
	// The asset filter.
	if one, _ := store.stableFreezeStats(ctx, "usdt"); len(one) != 2 || one[0].Asset != "usdt" || one[1].Asset != "usdt" {
		t.Errorf("usdt only = %+v", one)
	}
	if one, _ := store.stableFreezeStats(ctx, "eurc"); len(one) != 1 || one[0].Chain != "eth" {
		t.Errorf("eurc only = %+v", one)
	}
}

// Tether's quarantined amounts per chain come from the latest stored Tether
// update (stable_tether_snapshots), mapped to the canonical chain keys.
func TestStableTetherQuarantined(t *testing.T) {
	store := newTestRiskStore(t)
	ctx := context.Background()
	if q, err := store.stableTetherQuarantined(ctx); err != nil || q != nil {
		t.Fatalf("no snapshot: %+v %v, want nil (unknown)", q, err)
	}
	put := func(id int64, chains string) {
		t.Helper()
		if err := store.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: id, ChainsJSON: chains, FetchedAt: time.Unix(id, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	put(1792107000, `[{"name":"Tron","quarantined_native":1}]`)
	put(1792193400, `[{"name":"Ethereum","total_authorized_native":10,"not_issued_native":1,"quarantined_native":0.5,"circulating_native":8.5},`+
		`{"name":"Tron","total_authorized_native":20,"not_issued_native":0,"quarantined_native":12.25,"circulating_native":7.75},`+
		`{"name":"Kava EVM","quarantined_native":0}]`)
	q, err := store.stableTetherQuarantined(ctx)
	if err != nil || q == nil {
		t.Fatalf("quarantined = %+v %v", q, err)
	}
	if q.AsOf != "2026-10-16T23:30:00Z" || q.UpdateID != 1792193400 || q.TotalNative != 12.75 || len(q.Chains) != 3 {
		t.Fatalf("quarantined = %+v", q)
	}
	if c := q.Chains[0]; c.Chain != "tron" || c.ChainLabel != "TRON" || c.QuarantinedNative != 12.25 {
		t.Errorf("largest = %+v", c)
	}
	if c := q.Chains[1]; c.Chain != "eth" || c.QuarantinedNative != 0.5 {
		t.Errorf("second = %+v", c)
	}
	if c := q.Chains[2]; c.QuarantinedNative != 0 || c.ChainLabel == "" {
		t.Errorf("third = %+v", c)
	}
}
