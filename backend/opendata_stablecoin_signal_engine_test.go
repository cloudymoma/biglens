package main

import (
	"context"
	"testing"
	"time"
)

// sigPegResult is one peg sample with every core asset at devBps and a
// healthy Uniswap price.
func sigPegResult(at time.Time, devBps float64) *stablePegResult {
	res := &stablePegResult{AsOf: at, Assets: map[string]StablePeg{}}
	second := true
	for _, def := range stableCoreAssets {
		p := StablePeg{Asset: def.ID, Numeraire: def.Numeraire, State: "ok", Consensus: stableF(1 + devBps/1e4), DevBps: stableF(devBps),
			DispBps: stableF(2), InputsUsed: 3, VenuesUsed: 3, AsOf: stableFmt(at)}
		if def.ID == "eurc" {
			p.SecondDirectEURVenue = &second
		}
		res.Assets[def.ID] = p
	}
	res.Relative = StableRelativePeg{Pair: "USDC/USDT", Quotes: []StableQuote{{Venue: "uniswap", Pair: stableUniswapPair, Kind: "dex",
		Mid: stableF(1.0001), Included: true}}}
	res.Markets = []StableQuote{{Venue: "kraken", Pair: "USDTZUSD", Included: true}}
	return res
}

func sigState(t *testing.T, e *stableSignalEngine, asset, id string) StableSignal {
	t.Helper()
	for _, s := range e.snapshot(asset) {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("%s %s missing from the snapshot", asset, id)
	return StableSignal{}
}

func TestStableSignalEngineColdStart(t *testing.T) {
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e := newStableSignalEngine(store, func() time.Time { return now })
	// Before any input exists every signal is unknown, never normal (§7.4).
	if err := e.apply(context.Background(), stableSignalInputs{Now: now, ControlsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	na := map[string]bool{"eurc|S3": true, "usdc|S5": true, "eurc|S5": true}
	for _, def := range stableCoreAssets {
		sigs := e.snapshot(def.ID)
		if len(sigs) != len(stableSignalDefs) {
			t.Fatalf("%s: %d signals, want %d", def.ID, len(sigs), len(stableSignalDefs))
		}
		for _, s := range sigs {
			want := stableStateUnknown
			if na[def.ID+"|"+s.ID] {
				want = stableStateNA
			}
			if s.State != want || s.Rule == "" || s.Dimension == "" {
				t.Errorf("cold start %s %s = %s (rule %q), want %s with a rule", def.ID, s.ID, s.State, s.Rule, want)
			}
		}
		if w := e.worst(def.ID); w != stableStateUnknown {
			t.Errorf("cold start worst %s = %s, want unknown", def.ID, w)
		}
	}
	if s := sigState(t, e, "usdt", "S1"); s.Reason != stableReasonWarmingUp {
		t.Errorf("cold S1 reason = %q, want warming up", s.Reason)
	}
}

func TestStableSignalEngineTransitionsAndReload(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	e := newStableSignalEngine(store, clock)
	feed := func(e *stableSignalEngine, dev float64) {
		t.Helper()
		now = now.Add(5 * time.Minute)
		if err := e.apply(ctx, stableSignalInputs{Now: now, Peg: sigPegResult(now, dev), ControlsEnabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		feed(e, -5)
		if s := sigState(t, e, "usdt", "S1"); s.State != stableStateUnknown {
			t.Fatalf("S1 after %d samples = %s, want unknown (warming up)", i+1, s.State)
		}
		if s13 := sigState(t, e, "usdt", "S13"); s13.State != stableStateUnknown || s13.Reason != stableReasonWarmingUp || s13.Summary != "" {
			t.Fatalf("S13 after %d samples = state %q reason %q summary %q, want unknown / warming up with empty summary",
				i+1, s13.State, s13.Reason, s13.Summary)
		}
	}
	// The same sample applied again (another sampler's trigger) is not a new sample.
	if err := e.apply(ctx, stableSignalInputs{Now: now, Peg: sigPegResult(now, -5), ControlsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if s := sigState(t, e, "usdt", "S1"); s.State != stableStateUnknown {
		t.Fatalf("S1 after a repeated sample = %s, want unknown (still 2 samples)", s.State)
	}
	feed(e, -5)
	s1 := sigState(t, e, "usdt", "S1")
	if s1.State != stableStateNormal || s1.Inputs["dev_bps"] == nil || s1.AsOf != stableFmt(now) {
		t.Fatalf("S1 after 3 samples = %+v, want normal with inputs and as_of", s1)
	}
	if s13 := sigState(t, e, "usdt", "S13"); s13.State != stableStateNormal || s13.Summary != "all core sources healthy" {
		t.Fatalf("S13 after 3 samples = %+v, want normal with 'all core sources healthy'", s13)
	}
	if s := sigState(t, e, "usdc", "S3"); s.State != stableStateNormal {
		t.Errorf("S3 usdc = %s, want normal", s.State)
	}
	var n int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stable_signal_state`).Scan(&n); err != nil || n != len(stableCoreAssets)*len(stableSignalDefs) {
		t.Fatalf("stable_signal_state rows = %d (%v), want %d", n, err, len(stableCoreAssets)*len(stableSignalDefs))
	}
	evs, err := store.stableSignalEvents(ctx, "usdt", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Signal == "S1" && ev.From == stableStateUnknown && ev.To == stableStateNormal {
			found = true
		}
	}
	if !found {
		t.Fatalf("events %+v: want usdt S1 unknown → normal", evs)
	}

	// A new engine (restart) restores the hysteresis: no second warm-up.
	e2 := newStableSignalEngine(store, clock)
	feed(e2, -5)
	if s := sigState(t, e2, "usdt", "S1"); s.State != stableStateNormal {
		t.Fatalf("S1 after a restart within 15 min = %s, want normal", s.State)
	}
	for i := 0; i < 3; i++ {
		feed(e2, -150)
	}
	if s := sigState(t, e2, "usdt", "S1"); s.State != stableStateWarning || s.Since != stableFmt(now) {
		t.Fatalf("S1 after 3 samples at −150 bps = %s since %s, want warning since %s", s.State, s.Since, stableFmt(now))
	}
	if w := e2.worst("usdt"); w != stableStateWarning {
		t.Errorf("worst usdt = %s, want warning", w)
	}
	evs, _ = store.stableSignalEvents(ctx, "usdt", 50)
	if len(evs) == 0 || evs[0].Signal != "S1" || evs[0].From != stableStateNormal || evs[0].To != stableStateWarning {
		t.Fatalf("newest event = %+v, want usdt S1 normal → warning", evs)
	}

	// A stalled peg sampler makes S1 unknown and S13 at least watch (§13 Operations).
	now = now.Add(20 * time.Minute)
	if err := e2.apply(ctx, stableSignalInputs{Now: now, Peg: sigPegResult(now.Add(-20*time.Minute), -150), PegStalled: true, ControlsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if s := sigState(t, e2, "usdt", "S1"); s.State != stableStateUnknown {
		t.Errorf("S1 with the peg sampler stalled = %s, want unknown", s.State)
	}
	if s := sigState(t, e2, "usdt", "S13"); s.State != stableStateWatch {
		t.Errorf("S13 with the peg sampler stalled = %s (%s), want watch", s.State, s.Reason)
	}
}

func TestStableSignalEngineLevelAndConfirmed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e := newStableSignalEngine(newTestRiskStore(t), func() time.Time { return now })
	supply := func(at time.Time, diff float64) *stableSupplyState {
		st := &stableSupplyState{AsOf: at, Assets: map[string]StableSupplyAsset{}}
		for _, def := range stableCoreAssets {
			a := StableSupplyAsset{Asset: def.ID}
			a.Reconcile.Aggregator = StableReconAggregator{DiffPct: stableF(diff), AboveWatch: diff > 0.5 || diff < -0.5}
			a.Reconcile.Onchain = []StableReconOnchain{{Chain: "eth"}}
			st.Assets[def.ID] = a
		}
		return st
	}
	facts := stableFacts{Assets: map[string]stableAssetFacts{
		"usdt": {Regulatory: []stableRegulatory{{Regime: "EU-MiCA", Status: "not_authorized", Entity: "Tether", AsOf: "2026-10-20", SourceURL: "https://example.com"}}},
	}}
	tether := &StableTetherPoint{Coverage: stableF(1.0302), AsOf: "2026-10-19T00:00:00Z"}
	apply := func(st *stableSupplyState) {
		t.Helper()
		now = now.Add(10 * time.Minute)
		if err := e.apply(ctx, stableSignalInputs{Now: now, Supply: st, Facts: facts, Tether: tether, ControlsEnabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	apply(supply(now, 0.1))
	if s := sigState(t, e, "usdt", "S12"); s.State != stableStateUnknown {
		t.Fatalf("S12 after one supply sample = %s, want unknown (confirmation needs 2)", s.State)
	}
	apply(supply(now, 0.1))
	if s := sigState(t, e, "usdt", "S12"); s.State != stableStateNormal {
		t.Fatalf("S12 after 2 samples = %s, want normal", s.State)
	}
	apply(supply(now, 0.8))
	if s := sigState(t, e, "usdt", "S12"); s.State != stableStateNormal {
		t.Fatalf("S12 after one sample at 0.8%% = %s, want normal (unconfirmed)", s.State)
	}
	apply(supply(now, 0.8))
	if s := sigState(t, e, "usdt", "S12"); s.State != stableStateWatch {
		t.Fatalf("S12 after 2 samples at 0.8%% = %s, want watch", s.State)
	}
	// Level-triggered signals follow their inputs at once.
	if s := sigState(t, e, "usdt", "S5"); s.State != stableStateNormal {
		t.Errorf("S5 coverage 1.0302 = %s, want normal", s.State)
	}
	if s := sigState(t, e, "usdt", "S11"); s.State != stableStateWatch {
		t.Errorf("S11 MiCA not_authorized = %s, want watch", s.State)
	}
	if s := sigState(t, e, "usdc", "S11"); s.State != stableStateUnknown {
		t.Errorf("S11 without curated facts = %s, want unknown", s.State)
	}
}

func TestStableHealthStale3x(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newStableHealth(StablecoinConfig{}.settings(), func() time.Time { return now })
	if h.stale3x("circle") {
		t.Error("never called: want not stale")
	}
	h.recordFetchAt("circle", now.Add(-29*time.Minute), time.Second, "timeout")
	if h.stale3x("circle") {
		t.Error("failing for 29 min (interval 10 min): want not stale yet")
	}
	h.recordFetchAt("circle", now.Add(-31*time.Minute), time.Second, "timeout")
	if !h.stale3x("circle") {
		t.Error("no success for 31 min (3 × 10 min): want stale")
	}
	h.recordFetchAt("circle", now.Add(-5*time.Minute), time.Second, "")
	if h.stale3x("circle") {
		t.Error("success 5 min ago: want not stale")
	}
	h.recordFetchAt("tether", now.Add(-2*time.Hour), time.Second, "")
	if !h.stale3x("tether") {
		t.Error("tether's last success 2 h ago (interval 30 min): want stale")
	}
	h2 := newStableHealth(StablecoinConfig{Providers: map[string]bool{"circle": false}}.settings(), func() time.Time { return now })
	h2.recordFetchAt("circle", now.Add(-5*time.Hour), time.Second, "timeout")
	if h2.stale3x("circle") {
		t.Error("a disabled provider is never stale")
	}
}

// The samplers run the engine after every sample (main.go wiring), and /live
// shows each asset's worst signal.
func TestStableSignalsWiredToSamplers(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	h, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)
	h.stable.wireSignals()
	if h.stable.signals == nil {
		t.Fatal("wireSignals: no engine")
	}
	for i := 0; i < 3; i++ {
		h.stable.peg.tick(ctx)
		clock = clock.Add(5 * time.Minute)
	}
	s1 := sigState(t, h.stable.signals, "usdt", "S1")
	if s1.State == stableStateUnknown || s1.Inputs["dev_bps"] == nil {
		t.Fatalf("usdt S1 after 3 peg ticks = %+v, want a known state with inputs", s1)
	}
	live := h.stable.buildLive()
	for _, a := range live.Assets {
		if a.WorstSignal == nil || *a.WorstSignal == "" {
			t.Errorf("/live %s worst_signal = nil, want the engine's summary", a.Asset)
		}
	}
	if live.Alerts == nil {
		t.Error("/live alerts = nil, want counts")
	}
	var n int
	if err := h.stable.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stable_signal_state`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("stable_signal_state rows = %d (%v)", n, err)
	}
}
