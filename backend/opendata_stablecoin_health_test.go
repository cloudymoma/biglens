package main

import (
	"context"
	"testing"
	"time"
)

func TestStableProviderHealthStats(t *testing.T) {
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	h := newStableHealth(StablecoinConfig{}.settings(), func() time.Time { return clock })
	// An old call outside the 24 h window is ignored.
	h.recordFetchAt("defillama", clock.Add(-25*time.Hour), 10*time.Millisecond, "5xx")
	for _, ms := range []int{100, 200, 300} {
		h.recordFetchAt("defillama", clock.Add(-time.Hour), time.Duration(ms)*time.Millisecond, "")
	}
	h.recordFetchAt("defillama", clock.Add(-5*time.Minute), 50*time.Millisecond, "5xx")

	p := h.provider("defillama")
	if p.Success24hPct == nil || *p.Success24hPct != 75 {
		t.Errorf("success_24h_pct = %v, want 75", p.Success24hPct)
	}
	if p.P50LatencyMS == nil || *p.P50LatencyMS != 100 {
		t.Errorf("p50_latency_ms = %v, want 100 (nearest rank of 50,100,200,300)", p.P50LatencyMS)
	}
	if p.Status != "error" || p.LastError != "5xx" || p.LastOK != "2026-10-19T11:00:00Z" || !p.Enabled {
		t.Errorf("provider = %+v, want error/5xx with last_ok 11:00", p)
	}

	// A last success older than 3 call intervals is stale.
	h.recordFetchAt("circle", clock.Add(-31*time.Minute), 20*time.Millisecond, "")
	if c := h.provider("circle"); c.Status != "stale" {
		t.Errorf("circle 31 min after its last success = %q, want stale (3 × 10 min)", c.Status)
	}
	h.recordFetchAt("circle", clock.Add(-29*time.Minute), 20*time.Millisecond, "")
	if c := h.provider("circle"); c.Status != "ok" {
		t.Errorf("circle 29 min after its last success = %q, want ok", c.Status)
	}
	if tz := h.provider("tether"); tz.Status != "pending" || tz.Success24hPct != nil || tz.P50LatencyMS != nil {
		t.Errorf("never-called provider = %+v, want pending with null stats", tz)
	}
}

func TestStableHealthProviderList(t *testing.T) {
	cfg := StablecoinConfig{Providers: map[string]bool{"binance": false}}
	h := newStableHealth(cfg.settings(), time.Now)
	list := h.providers()
	if len(list) != len(stableProviderRegistry) {
		t.Fatalf("providers = %d, want %d", len(list), len(stableProviderRegistry))
	}
	byKey := map[string]StableProviderHealth{}
	for i, p := range list {
		if p.Key != stableProviderRegistry[i].ID {
			t.Errorf("provider %d = %s, want registry order (%s)", i, p.Key, stableProviderRegistry[i].ID)
		}
		byKey[p.Key] = p
	}
	if b := byKey["binance"]; b.Enabled || b.Status != "disabled" {
		t.Errorf("binance = %+v, want disabled", b)
	}
	// Keys without an adapter yet are reported as not in use, not as failing;
	// a provider with an adapter that has not been called yet is pending.
	if c := byKey["curve"]; !c.Enabled || c.Status != "not_in_use" {
		t.Errorf("curve = %+v, want enabled but not_in_use", c)
	}
	if k := byKey["kraken"]; !k.Enabled || k.Status != "pending" {
		t.Errorf("kraken = %+v, want enabled and pending", k)
	}
	if c := byKey["rpc_controls"]; !c.Enabled || c.Status != "pending" {
		t.Errorf("rpc_controls = %+v, want enabled and pending (M3 adapter)", c)
	}
	if e := byKey["ecb"]; e.Attribution == "" || e.Label == "" {
		t.Errorf("ecb = %+v, want label and attribution", e)
	}
}

func TestStableSamplerHealth(t *testing.T) {
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	h := newStableHealth(StablecoinConfig{}.settings(), func() time.Time { return clock })
	var stalls []string
	h.onStall = func(name string) { stalls = append(stalls, name) }

	h.recordRun("supply", stableSupplyTick, clock, clock.Add(stableSupplyTick), true)
	clock = clock.Add(stableSupplyTick)
	h.recordRun("supply", stableSupplyTick, clock, clock.Add(stableSupplyTick), false)
	clock = clock.Add(stableSupplyTick)
	h.recordRun("supply", stableSupplyTick, clock, clock.Add(stableSupplyTick), false)
	s := h.sampler("supply")
	if s.ConsecutiveFailures != 2 || s.LastRun != "2026-10-19T12:20:00Z" || s.LastOK != "2026-10-19T12:00:00Z" ||
		s.NextDue != "2026-10-19T12:30:00Z" || s.Stalled {
		t.Errorf("supply sampler = %+v", s)
	}
	h.recordRun("supply", stableSupplyTick, clock, clock.Add(stableSupplyTick), true)
	if s := h.sampler("supply"); s.ConsecutiveFailures != 0 || s.LastOK != "2026-10-19T12:20:00Z" {
		t.Errorf("after a success = %+v", s)
	}

	// Three missed intervals: stalled, and the warning fires once.
	clock = clock.Add(3*stableSupplyTick + time.Second)
	h.checkStalls()
	h.checkStalls()
	if s := h.sampler("supply"); !s.Stalled {
		t.Errorf("sampler 3 intervals late = %+v, want stalled", s)
	}
	if len(stalls) != 1 || stalls[0] != "supply" {
		t.Errorf("stall warnings = %v, want one for supply", stalls)
	}
	// A run clears the stall; a later stall warns again.
	h.recordRun("supply", stableSupplyTick, clock, clock.Add(stableSupplyTick), true)
	if h.sampler("supply").Stalled {
		t.Error("stall not cleared by a run")
	}
	clock = clock.Add(4 * stableSupplyTick)
	h.checkStalls()
	if len(stalls) != 2 {
		t.Errorf("stall warnings = %v, want a second one", stalls)
	}
	if never := h.sampler("rates"); never.LastRun != "" || never.Stalled || never.ConsecutiveFailures != 0 {
		t.Errorf("never-run sampler = %+v", never)
	}
	if names := h.samplers(); len(names) != 6 || names[0].Name != "peg" || names[1].Name != "peg_history" || names[2].Name != "supply" ||
		names[3].Name != "supply_history" || names[4].Name != "rates" || names[5].Name != "controls" {
		t.Errorf("samplers = %+v, want peg, peg_history, supply, supply_history, rates, controls", names)
	}
}

// One runner iteration feeds the health tracker from the sampler.
func TestStableRunOnceRecordsHealth(t *testing.T) {
	stub := newStableStub(t)
	rpcs, _, _ := stableRPCStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	settings := StablecoinConfig{}.settings()
	s := newStableSupplySampler(settings, newTestRiskStore(t), stub.endpoints(), rpcs, now)
	h := newStableHealth(settings, now)
	runStableOnce(context.Background(), s, h)
	for _, name := range []string{"supply", "supply_history", "rates"} {
		if smp := h.sampler(name); smp.LastRun == "" || smp.LastOK == "" || smp.ConsecutiveFailures != 0 {
			t.Errorf("sampler %s = %+v, want a successful run", name, smp)
		}
	}
	for _, key := range []string{"defillama", "circle", "tether", "rpc_supply", "ecb", "us_treasury"} {
		if p := h.provider(key); p.Status != "ok" || p.Success24hPct == nil || *p.Success24hPct != 100 {
			t.Errorf("provider %s = %+v, want ok at 100%%", key, p)
		}
	}
	// A failing source makes the supply run fail and is counted per provider.
	stub.setFail("/circle", 503)
	clock = clock.Add(stableSupplyTick)
	runStableOnce(context.Background(), s, h)
	if smp := h.sampler("supply"); smp.ConsecutiveFailures != 1 {
		t.Errorf("supply after a Circle failure = %+v", smp)
	}
	if p := h.provider("circle"); p.Status != "error" || p.LastError != "5xx" || *p.Success24hPct != 50 {
		t.Errorf("circle after a failure = %+v", p)
	}
}
