package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Peg sampler (stablecoin.md §8.4): every 5 minutes it reads the venue
// tickers and the Uniswap pool, computes the consensus (§5.2–5.3), stores the
// quotes, the peg samples, the daily rollups and the DEX price, and keeps the
// latest result in memory, so /live and /peg never call upstreams.

// stablePegVenueKeys are the provider keys of the exchange adapters.
var stablePegVenueKeys = []string{"coinbase", "kraken", "bitstamp", "okx", "binance"}

// stablePegECBWindow: the ECB rate used as the EUR volume fallback must be
// at most this old (the series skips weekends and holidays).
const stablePegECBWindow = 10 * 24 * time.Hour

type stablePegSampler struct {
	settings stableSettings
	store    *riskStore
	ep       stableEndpoints
	venues   *stableVenueFetcher
	rpcs     func(network string) []string
	now      func() time.Time
	// onFetch reports every upstream call (provider health); may be nil.
	onFetch func(provider string, took time.Duration, code string)
	// ohlcLimit overrides Bitstamp's page size (tests); 0 = stableOHLCPage.
	ohlcLimit int

	mu         sync.Mutex
	poolOK     bool // the pool identity check passed once
	lastResult *stablePegResult
	depegDue   time.Time
	ecbLoaded  bool // the ECB EUR/USD history was loaded in this process
}

func newStablePegSampler(settings stableSettings, store *riskStore, ep stableEndpoints, rpcs func(string) []string, now func() time.Time) *stablePegSampler {
	p := &stablePegSampler{settings: settings, store: store, ep: ep, rpcs: rpcs, now: now}
	p.venues = newStableVenueFetcher(ep, now)
	p.venues.onFetch = func(provider string, took time.Duration, code string) {
		if p.onFetch != nil {
			p.onFetch(provider, took, code)
		}
	}
	return p
}

// latest returns the newest result (nil before the first tick).
func (p *stablePegSampler) latest() *stablePegResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastResult
}

// stableFetchFailed reports whether a quote code is an upstream failure (not
// a provider that is off, a product that is not listed, or a pause).
func stableFetchFailed(code string) bool {
	return code != "" && code != "disabled" && code != "paused"
}

// tick runs one peg sample and reports whether every enabled upstream answered.
func (p *stablePegSampler) tick(ctx context.Context) bool {
	now := p.now().UTC()
	enabled := map[string]bool{}
	for _, k := range stablePegVenueKeys {
		enabled[k] = p.settings.Providers[k]
	}
	quotes := p.venues.fetchAll(ctx, enabled)
	ok := true
	for _, q := range quotes {
		if stableFetchFailed(q.Code) {
			ok = false
		}
	}
	var uni *stableUniswapReading
	if p.settings.Providers["uniswap_rpc"] && p.rpcs != nil {
		r := p.readPool(ctx, now)
		if stableFetchFailed(r.Code) {
			ok = false
		}
		uni = &r
	}
	in := stablePegInputs{Quotes: quotes, Uniswap: uni, Now: now, ECBEURUSD: p.ecbRate(ctx, now)}
	res := stableComputePeg(in)
	p.persist(ctx, now, quotes, uni, res)
	p.mu.Lock()
	p.lastResult = &res
	p.mu.Unlock()
	return ok
}

// readPool checks the pool identity once (until it passes), then reads slot0.
func (p *stablePegSampler) readPool(ctx context.Context, now time.Time) stableUniswapReading {
	p.mu.Lock()
	checked := p.poolOK
	p.mu.Unlock()
	t0 := time.Now()
	if !checked {
		if code := checkStableUniswapPool(ctx, p.rpcs); code != "" {
			p.observe("uniswap_rpc", t0, code)
			return stableUniswapReading{ReadAt: now, Code: code}
		}
		p.mu.Lock()
		p.poolOK = true
		p.mu.Unlock()
	}
	r := readStableUniswap(ctx, p.rpcs, now)
	p.observe("uniswap_rpc", t0, r.Code)
	return r
}

func (p *stablePegSampler) observe(provider string, start time.Time, code string) {
	if p.onFetch != nil {
		p.onFetch(provider, time.Since(start), code)
	}
}

// ecbRate is the latest stored ECB EUR/USD reference rate within the window.
func (p *stablePegSampler) ecbRate(ctx context.Context, now time.Time) *float64 {
	if p.store == nil {
		return nil
	}
	rows, err := p.store.stableRatesSeries(ctx, stableSeriesECBEURUSD, now.Add(-stablePegECBWindow).Format(time.DateOnly))
	if err != nil || len(rows) == 0 {
		return nil
	}
	return stableF(rows[len(rows)-1].Value)
}

func (p *stablePegSampler) persist(ctx context.Context, now time.Time, quotes []stableVenueQuote, uni *stableUniswapReading, res stablePegResult) {
	if p.store == nil {
		return
	}
	last := map[string]*float64{}
	for _, q := range quotes {
		last[q.Venue+"|"+q.Pair] = q.Last
	}
	if err := p.store.insertStableQuotes5m(ctx, now, res.Markets, last); err != nil {
		slog.Warn("stablecoin peg quotes store", "error", err)
	}
	var rows []stablePeg5mRow
	for _, def := range stableCoreAssets {
		a := res.Assets[def.ID]
		rows = append(rows, stablePeg5mRow{Bucket: now, Asset: def.ID, Numeraire: a.Numeraire, State: a.State, Consensus: a.Consensus,
			DevBps: a.DevBps, DispBps: a.DispBps, SingleVenuePrice: a.SingleVenuePrice, InputsUsed: a.InputsUsed, VenuesUsed: a.VenuesUsed})
	}
	rel := res.Relative
	relRow := stablePeg5mRow{Bucket: now, Asset: stableRelativeAsset, Numeraire: "USDT", State: "unknown", Consensus: rel.Median,
		DevBps: rel.DevBps, InputsUsed: rel.InputsUsed}
	venues := map[string]bool{}
	var lo, hi *float64
	for _, q := range rel.Quotes {
		if !q.Included {
			continue
		}
		venues[q.Venue] = true
		if lo == nil || *q.Mid < *lo {
			lo = stableF(*q.Mid)
		}
		if hi == nil || *q.Mid > *hi {
			hi = stableF(*q.Mid)
		}
	}
	relRow.VenuesUsed = len(venues)
	if rel.Median != nil {
		relRow.State = "ok"
	}
	if rel.InputsUsed >= 2 {
		relRow.DispBps = stableF((*hi - *lo) * 1e4)
	}
	rows = append(rows, relRow)
	if err := p.store.insertStablePeg5m(ctx, rows); err != nil {
		slog.Warn("stablecoin peg samples store", "error", err)
		return
	}
	day := now.Format(time.DateOnly)
	for _, r := range rows {
		if err := p.store.rollupStablePegDaily(ctx, r.Asset, day, now); err != nil {
			slog.Warn("stablecoin peg daily rollup", "asset", r.Asset, "error", err)
		}
	}
	if uni != nil && uni.Code == "" {
		if err := p.store.insertStableDex10m(ctx, now, *uni); err != nil {
			slog.Warn("stablecoin dex store", "error", err)
		}
	}
}

// runStablePegOnce runs one peg tick, then the daily depeg-history job when
// due, and records both samplers' health.
func runStablePegOnce(ctx context.Context, p *stablePegSampler, h *stableHealth) {
	if p.onFetch == nil && h != nil {
		p.onFetch = h.recordFetch
	}
	start := p.now().UTC()
	ok := p.tick(ctx)
	if h != nil {
		h.recordRun("peg", stablePegTick, start, start.Add(stablePegTick), ok)
	}
	if ran, ok := p.syncDepegHistory(ctx); ran && h != nil {
		h.recordRun("peg_history", stableDepegEvery, start, p.depegNextDue(), ok)
	}
}

// runStablePegSampler runs the peg sampler at boot and every 5 minutes until
// ctx ends; a panic in one iteration is logged and the loop continues.
func runStablePegSampler(ctx context.Context, p *stablePegSampler, h *stableHealth) {
	t := time.NewTicker(stablePegTick)
	defer t.Stop()
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("stablecoin peg sampler panic", "panic", r)
				}
			}()
			runStablePegOnce(ctx, p, h)
		}()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
