package main

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

func countRows(t *testing.T, s *riskStore, table, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The daily rollup counts 5 minutes per sample at |dev| ≥ 30 / ≥ 100 bps.
// A price of exactly 1.003 is 30.0 bps off peg and counts, although
// 1.003 − 1 is 29.99999999999989 bps in float64.
func TestStablePegDailyRollup(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var rows []stablePeg5mRow
	for i, c := range []float64{0.9990, 1.003, 0.99650, 0.9880, 0.9995} {
		cv := c
		dev := (c - 1) * 1e4
		rows = append(rows, stablePeg5mRow{Bucket: day.Add(time.Duration(i) * 5 * time.Minute), Asset: "usdt", Numeraire: "USD", State: "ok",
			Consensus: &cv, DevBps: &dev, InputsUsed: 3, VenuesUsed: 3})
	}
	// A sample without a consensus is not part of the day.
	rows = append(rows, stablePeg5mRow{Bucket: day.Add(30 * time.Minute), Asset: "usdt", Numeraire: "USD", State: "unknown"})
	// Another day and another asset stay out.
	one := 1.0
	zero := 0.0
	rows = append(rows, stablePeg5mRow{Bucket: day.Add(24 * time.Hour), Asset: "usdt", Numeraire: "USD", State: "ok", Consensus: &one, DevBps: &zero, InputsUsed: 2, VenuesUsed: 2},
		stablePeg5mRow{Bucket: day, Asset: "usdc", Numeraire: "USD", State: "ok", Consensus: &one, DevBps: &zero, InputsUsed: 2, VenuesUsed: 2})
	if err := s.insertStablePeg5m(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.rollupStablePegDaily(ctx, "usdt", "2026-10-19", day.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, err := s.stablePegDailySeries(ctx, "usdt", "consensus", "2026-10-01")
	if err != nil || len(d) != 1 {
		t.Fatalf("daily = %+v, %v", d, err)
	}
	r := d[0]
	if r.Day != "2026-10-19" || r.Samples != 5 || r.MinutesGE30 == nil || *r.MinutesGE30 != 15 || r.MinutesGE100 == nil || *r.MinutesGE100 != 5 {
		t.Errorf("rollup = %+v (want 5 samples, 15 min ≥ 30 bps, 5 min ≥ 100 bps)", r)
	}
	for label, got := range map[string]struct {
		v    *float64
		want float64
	}{"open": {r.Open, 0.9990}, "close": {r.Close, 0.9995}, "high": {r.High, 1.003}, "low": {r.Low, 0.9880},
		"min_dev": {r.MinDevBps, (0.9880 - 1) * 1e4}, "max_dev": {r.MaxDevBps, (1.003 - 1) * 1e4}} {
		approxPtr(t, label, got.v, got.want, 1e-9)
	}
	// Re-running the rollup replaces the row.
	if err := s.rollupStablePegDaily(ctx, "usdt", "2026-10-19", day.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "stable_peg_daily", `WHERE asset = 'usdt' AND day = '2026-10-19'`); n != 1 {
		t.Errorf("daily rows after a rerun = %d", n)
	}
	// 5-min series, oldest first, nulls kept.
	series, err := s.stablePeg5mSeries(ctx, "usdt", day)
	if err != nil || len(series) != 7 || series[5].Consensus != nil || series[0].Bucket != day {
		t.Errorf("5m series = %+v, %v", series, err)
	}
}

func TestStablePegStoreRetention(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	price := 1.0
	q := func(at time.Time) StableQuote {
		return StableQuote{Venue: "kraken", Pair: "USDCUSD", Mid: &price, Bid: &price, Ask: &price, Included: true}
	}
	if err := s.insertStableQuotes5m(ctx, now.Add(-stableQuotesRetention-time.Hour), []StableQuote{q(now)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.insertStableQuotes5m(ctx, now.Add(-time.Hour), []StableQuote{q(now)}, nil); err != nil {
		t.Fatal(err)
	}
	dev := 0.0
	if err := s.insertStablePeg5m(ctx, []stablePeg5mRow{
		{Bucket: now.Add(-stablePegRetention - time.Hour), Asset: "usdc", Numeraire: "USD", State: "ok", Consensus: &price, DevBps: &dev},
		{Bucket: now.Add(-time.Hour), Asset: "usdc", Numeraire: "USD", State: "ok", Consensus: &price, DevBps: &dev},
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-stablePegRetention - time.Hour), now.Add(-time.Hour)} {
		if err := s.insertStableDex10m(ctx, at, stableUniswapReading{Price: 1, SqrtPriceX96: "1", ReadAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.rollupStablePegDaily(ctx, "usdc", now.Add(-stablePegRetention-time.Hour).Format(time.DateOnly), now); err != nil {
		t.Fatal(err)
	}
	// The heatmap inputs follow the venue quotes' 30 days.
	inputs := stablePegResult{Assets: map[string]StablePeg{"usdc": {Asset: "usdc", Quotes: []StableQuote{q(now)}}}}
	for _, at := range []time.Time{now.Add(-stableQuotesRetention - time.Hour), now.Add(-time.Hour)} {
		if err := s.insertStableInputs5m(ctx, at, inputs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pruneStable(ctx, now); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"stable_quotes_5m": 1, "stable_peg_5m": 1, "stable_dex_10m": 1, "stable_peg_daily": 1, "stable_inputs_5m": 1} {
		if n := countRows(t, s, table, ``); n != want {
			t.Errorf("%s rows after prune = %d, want %d", table, n, want)
		}
	}
	// The DEX table keeps the first reading of each 10-minute bucket.
	at := now.Truncate(10 * time.Minute)
	_ = s.insertStableDex10m(ctx, at.Add(time.Minute), stableUniswapReading{Price: 1.0001, SqrtPriceX96: "2", ReadAt: at.Add(time.Minute)})
	_ = s.insertStableDex10m(ctx, at.Add(6*time.Minute), stableUniswapReading{Price: 1.0002, SqrtPriceX96: "3", ReadAt: at.Add(6 * time.Minute)})
	var p float64
	if err := s.db.QueryRow(`SELECT price FROM stable_dex_10m WHERE bucket = ?`, stableTS(at)).Scan(&p); err != nil || p != 1.0001 {
		t.Errorf("dex bucket price = %v, %v (want the first reading)", p, err)
	}
}

// The 5-minute peg sampler: venues and the Uniswap pool are read, the
// consensus computed, and quotes, peg samples, daily rollups and the DEX
// price stored; the latest result is kept in memory for /live and /peg.
func TestStablePegSamplerTick(t *testing.T) {
	venues := newStableVenueStub(t)
	rpc, rpcCalls := newUniswapRPCStub(t, nil)
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	cfg := StablecoinConfig{Providers: map[string]bool{"binance": false}}
	p := newStablePegSampler(cfg.settings(), store, venues.endpoints(), func(string) []string { return []string{rpc} }, func() time.Time { return clock })
	var (
		fetchMu sync.Mutex
		fetches []string
	)
	// Venue calls run concurrently, as with the health hook in production.
	p.onFetch = func(provider string, _ time.Duration, code string) {
		fetchMu.Lock()
		fetches = append(fetches, provider+":"+code)
		fetchMu.Unlock()
	}
	ctx := context.Background()

	if p.latest() != nil {
		t.Fatal("result before the first tick")
	}
	if ok := p.tick(ctx); !ok {
		t.Error("first tick reported a failure")
	}
	bucket := stableTS(time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC))
	if n := countRows(t, store, "stable_quotes_5m", `WHERE bucket = ?`, bucket); n != len(stableVenuePairs)+1 {
		t.Errorf("quotes stored = %d, want %d", n, len(stableVenuePairs)+1)
	}
	var reason string
	if err := store.db.QueryRow(`SELECT exclude_reason FROM stable_quotes_5m WHERE bucket = ? AND venue = 'binance'`, bucket).Scan(&reason); err != nil || reason != "disabled" {
		t.Errorf("binance (disabled) row reason = %q, %v", reason, err)
	}
	if venues.count("/binance/api/v3/ticker/24hr") != 0 {
		t.Error("disabled Binance was called")
	}
	if n := countRows(t, store, "stable_peg_5m", `WHERE bucket = ?`, bucket); n != 4 {
		t.Errorf("peg samples = %d, want usdt, usdc, eurc and the relative peg", n)
	}
	res := p.latest()
	if res == nil || res.Assets["usdt"].Consensus == nil || res.Relative.Median == nil {
		t.Fatalf("latest = %+v", res)
	}
	var stored float64
	if err := store.db.QueryRow(`SELECT consensus FROM stable_peg_5m WHERE bucket = ? AND asset = 'usdt'`, bucket).Scan(&stored); err != nil ||
		math.Abs(stored-*res.Assets["usdt"].Consensus) > 1e-15 {
		t.Errorf("stored usdt consensus = %v, %v", stored, err)
	}
	if err := store.db.QueryRow(`SELECT consensus FROM stable_peg_5m WHERE bucket = ? AND asset = ?`, bucket, stableRelativeAsset).Scan(&stored); err != nil ||
		math.Abs(stored-*res.Relative.Median) > 1e-15 {
		t.Errorf("stored relative peg = %v, %v", stored, err)
	}
	if d, _ := store.stablePegDailySeries(ctx, "eurc", "consensus", "2026-10-01"); len(d) != 1 || d[0].Samples != 1 {
		t.Errorf("eurc daily = %+v", d)
	}
	if n := countRows(t, store, "stable_dex_10m", ``); n != 1 {
		t.Errorf("dex rows = %d", n)
	}
	// The pool identity is checked once (3 calls), then slot0 is 1 call per tick.
	if *rpcCalls != 4 {
		t.Errorf("RPC calls after the first tick = %d, want 4", *rpcCalls)
	}

	// Four minutes later (the fixtures' quote times stay within 300 s): a new
	// 5-minute bucket; the 10-minute DEX bucket is unchanged.
	clock = time.Date(2026, 10, 19, 13, 25, 0, 0, time.UTC)
	p.tick(ctx)
	if n := countRows(t, store, "stable_peg_5m", ``); n != 8 {
		t.Errorf("peg samples after two ticks = %d", n)
	}
	if d, _ := store.stablePegDailySeries(ctx, "usdt", "consensus", "2026-10-01"); len(d) != 1 || d[0].Samples != 2 {
		t.Errorf("usdt daily after two ticks = %+v", d)
	}
	if n := countRows(t, store, "stable_dex_10m", ``); n != 1 || *rpcCalls != 5 {
		t.Errorf("dex rows %d, RPC calls %d after the second tick", n, *rpcCalls)
	}

	// A venue failure in the same bucket: served as excluded, the tick
	// reports not ok, the consensus continues with the remaining venues, and
	// the bucket's rows are replaced.
	venues.setStatus("/kraken/0/public/Ticker", 503)
	clock = clock.Add(10 * time.Second)
	if ok := p.tick(ctx); ok {
		t.Error("tick with Kraken down reported ok")
	}
	if u := p.latest().Assets["usdt"]; u.VenuesUsed != 2 || u.Consensus == nil {
		t.Errorf("usdt with Kraken down = %+v", u)
	}
	if n := countRows(t, store, "stable_peg_5m", ``); n != 8 {
		t.Errorf("peg samples after a rerun in the same bucket = %d", n)
	}
	reason = ""
	if err := store.db.QueryRow(`SELECT exclude_reason FROM stable_quotes_5m WHERE bucket = ? AND venue = 'kraken' AND pair = 'USDTZUSD'`,
		stableTS(time.Date(2026, 10, 19, 13, 25, 0, 0, time.UTC))).Scan(&reason); err != nil || reason != "5xx" {
		t.Errorf("kraken row after the failure = %q, %v", reason, err)
	}
	venues.setStatus("/kraken/0/public/Ticker", 0)
	// A new 10-minute bucket stores a second DEX row.
	clock = time.Date(2026, 10, 19, 13, 30, 0, 0, time.UTC)
	p.tick(ctx)
	if n := countRows(t, store, "stable_dex_10m", ``); n != 2 {
		t.Errorf("dex rows after a new 10-minute bucket = %d", n)
	}

	// Venue providers report through onFetch (provider health).
	fetchMu.Lock()
	defer fetchMu.Unlock()
	for _, want := range []string{"coinbase:", "kraken:", "bitstamp:", "okx:", "uniswap_rpc:", "kraken:5xx"} {
		found := false
		for _, f := range fetches {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no %q fetch recorded in %v", want, fetches)
		}
	}
}

func TestRunStablePegOnceRecordsHealth(t *testing.T) {
	venues := newStableVenueStub(t)
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	settings := StablecoinConfig{Providers: map[string]bool{"uniswap_rpc": false}}.settings()
	h := newStableHealth(settings, func() time.Time { return clock })
	p := newStablePegSampler(settings, newTestRiskStore(t), venues.endpoints(), nil, func() time.Time { return clock })
	runStablePegOnce(context.Background(), p, h)
	var peg *StableSamplerHealth
	for _, s := range h.samplers() {
		if s.Name == "peg" {
			s := s
			peg = &s
		}
	}
	if peg == nil || peg.LastRun == "" || peg.LastOK == "" || peg.ConsecutiveFailures != 0 {
		t.Errorf("peg sampler health = %+v", peg)
	}
	if k := h.provider("kraken"); k.Status != "ok" {
		t.Errorf("kraken provider after a tick = %+v", k)
	}
	if u := h.provider("uniswap_rpc"); u.Status != "disabled" {
		t.Errorf("uniswap_rpc = %+v, want disabled", u)
	}
}
