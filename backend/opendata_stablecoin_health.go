package main

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// Sampler and provider health for /meta (stablecoin.md §8.7). Counters live
// in memory and reset on restart.

// stableProviderInterval is how often each provider is called in this build.
// A provider whose last success is older than 3 intervals is stale (S13);
// keys without an entry have no adapter yet (Curve, Blockscout and
// Chainlink: v1.1).
var stableProviderInterval = map[string]time.Duration{
	"defillama":    stableSupplyTick,
	"circle":       stableSupplyTick,
	"tether":       stableTetherEvery,
	"rpc_supply":   stableRPCEvery,
	"rpc_controls": stableControlsTick,
	"ecb":          stableRatesEvery,
	"us_treasury":  stableRatesEvery,
	"coinbase":     stablePegTick,
	"kraken":       stablePegTick,
	"bitstamp":     stablePegTick,
	"okx":          stablePegTick,
	"binance":      stablePegTick,
	"uniswap_rpc":  stablePegTick,
}

// stableSamplerDefs lists the samplers in /meta order.
var stableSamplerDefs = []struct {
	Name     string
	Interval time.Duration
}{{"peg", stablePegTick}, {"peg_history", stableDepegEvery}, {"supply", stableSupplyTick}, {"supply_history", stableChartsEvery},
	{"rates", stableRatesEvery}, {"controls", stableControlsTick}}

// stableStallIntervals: a sampler that missed this many intervals in a row
// is stalled (logged once; S13 watch in M4).
const stableStallIntervals = 3

type StableProviderHealth struct {
	Key           string   `json:"key"`
	Label         string   `json:"label"`
	Attribution   string   `json:"attribution"`
	Enabled       bool     `json:"enabled"`
	Status        string   `json:"status"` // ok | stale | error | pending | disabled | not_in_use
	LastOK        string   `json:"last_ok,omitempty"`
	LastError     string   `json:"last_error,omitempty"`
	Success24hPct *float64 `json:"success_24h_pct"`
	P50LatencyMS  *int64   `json:"p50_latency_ms"`
}

type StableSamplerHealth struct {
	Name                string `json:"name"`
	IntervalSeconds     int64  `json:"interval_seconds"`
	LastRun             string `json:"last_run,omitempty"`
	LastOK              string `json:"last_ok,omitempty"`
	NextDue             string `json:"next_due,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	Stalled             bool   `json:"stalled"`
}

type stableCall struct {
	at      time.Time
	ok      bool
	latency time.Duration
}

type stableProviderState struct {
	calls    []stableCall
	lastAt   time.Time
	lastOK   time.Time
	lastCode string
}

type stableSamplerState struct {
	interval time.Duration
	lastRun  time.Time
	lastOK   time.Time
	nextDue  time.Time
	failures int
	stalled  bool
}

type stableHealth struct {
	mu        sync.Mutex
	settings  stableSettings
	now       func() time.Time
	onStall   func(name string) // test hook; the warning is logged regardless
	providerS map[string]*stableProviderState
	samplerS  map[string]*stableSamplerState
}

func newStableHealth(settings stableSettings, now func() time.Time) *stableHealth {
	h := &stableHealth{settings: settings, now: now, providerS: map[string]*stableProviderState{}, samplerS: map[string]*stableSamplerState{}}
	for _, d := range stableSamplerDefs {
		h.samplerS[d.Name] = &stableSamplerState{interval: d.Interval}
	}
	return h
}

// recordFetch is the sampler's onFetch hook: one upstream call.
func (h *stableHealth) recordFetch(provider string, took time.Duration, code string) {
	h.recordFetchAt(provider, h.now(), took, code)
}

func (h *stableHealth) recordFetchAt(provider string, at time.Time, took time.Duration, code string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.providerS[provider]
	if p == nil {
		p = &stableProviderState{}
		h.providerS[provider] = p
	}
	cutoff := h.now().Add(-24 * time.Hour)
	kept := p.calls[:0]
	for _, c := range p.calls {
		if !c.at.Before(cutoff) {
			kept = append(kept, c)
		}
	}
	p.calls = append(kept, stableCall{at: at, ok: code == "", latency: took})
	if !at.Before(p.lastAt) {
		p.lastAt, p.lastCode = at, code
	}
	if code == "" && at.After(p.lastOK) {
		p.lastOK = at
	}
}

func (h *stableHealth) provider(key string) StableProviderHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.providerLocked(key)
}

func (h *stableHealth) providerLocked(key string) StableProviderHealth {
	meta := stableProviderMeta(key)
	out := StableProviderHealth{Key: key, Label: meta.Label, Attribution: meta.License, Enabled: h.settings.Providers[key]}
	interval, inUse := stableProviderInterval[key]
	p := h.providerS[key]
	if p != nil {
		out.LastOK = stableFmt(p.lastOK)
		out.LastError = p.lastCode
		now := h.now()
		var n, okN int
		var lat []time.Duration
		for _, c := range p.calls {
			if c.at.Before(now.Add(-24 * time.Hour)) {
				continue
			}
			n++
			if c.ok {
				okN++
			}
			lat = append(lat, c.latency)
		}
		if n > 0 {
			out.Success24hPct = stableF(math.Round(float64(okN)/float64(n)*1000) / 10)
			sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
			ms := lat[(len(lat)+1)/2-1].Milliseconds() // nearest-rank median
			out.P50LatencyMS = &ms
		}
	}
	switch {
	case !out.Enabled:
		out.Status = "disabled"
	case !inUse:
		out.Status = "not_in_use"
	case p == nil || p.lastAt.IsZero():
		out.Status = "pending"
	case p.lastCode != "":
		out.Status = "error"
	case h.now().Sub(p.lastOK) > stableStallIntervals*interval:
		out.Status = "stale"
	default:
		out.Status = "ok"
	}
	return out
}

// providers lists every provider key in registry order.
func (h *stableHealth) providers() []StableProviderHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]StableProviderHealth, 0, len(stableProviderRegistry))
	for _, p := range stableProviderRegistry {
		out = append(out, h.providerLocked(p.ID))
	}
	return out
}

// recordRun records one sampler run that started at start.
func (h *stableHealth) recordRun(name string, interval time.Duration, start, nextDue time.Time, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.samplerS[name]
	if s == nil {
		s = &stableSamplerState{interval: interval}
		h.samplerS[name] = s
	}
	s.interval, s.lastRun, s.nextDue, s.stalled = interval, start, nextDue, false
	if ok {
		s.lastOK, s.failures = start, 0
	} else {
		s.failures++
	}
}

// checkStalls logs a warning once per stall: a sampler that has not run for
// 3 intervals.
func (h *stableHealth) checkStalls() {
	h.mu.Lock()
	var stalled []string
	now := h.now()
	for _, d := range stableSamplerDefs {
		s := h.samplerS[d.Name]
		if s == nil || s.lastRun.IsZero() || s.stalled {
			continue
		}
		if now.Sub(s.lastRun) > stableStallIntervals*s.interval {
			s.stalled = true
			stalled = append(stalled, d.Name)
		}
	}
	hook := h.onStall
	h.mu.Unlock()
	for _, name := range stalled {
		slog.Warn("stablecoin sampler stalled", "sampler", name, "missed_intervals", stableStallIntervals)
		if hook != nil {
			hook(name)
		}
	}
}

func (h *stableHealth) sampler(name string) StableSamplerHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.samplerLocked(name)
}

func (h *stableHealth) samplerLocked(name string) StableSamplerHealth {
	out := StableSamplerHealth{Name: name}
	s := h.samplerS[name]
	if s == nil {
		return out
	}
	out.IntervalSeconds = int64(s.interval / time.Second)
	out.LastRun, out.LastOK, out.NextDue = stableFmt(s.lastRun), stableFmt(s.lastOK), stableFmt(s.nextDue)
	out.ConsecutiveFailures = s.failures
	out.Stalled = s.stalled || (!s.lastRun.IsZero() && h.now().Sub(s.lastRun) > stableStallIntervals*s.interval)
	return out
}

func (h *stableHealth) samplers() []StableSamplerHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]StableSamplerHealth, 0, len(stableSamplerDefs))
	for _, d := range stableSamplerDefs {
		out = append(out, h.samplerLocked(d.Name))
	}
	return out
}

// runStableOnce is one runner iteration: the supply tick, then the history
// and rates jobs and the retention prune when due, each run recorded in h.
// It wires the sampler's fetch hook to h on first use.
func runStableOnce(ctx context.Context, s *stableSupplySampler, h *stableHealth) {
	if s.onFetch == nil {
		s.onFetch = h.recordFetch
	}
	start := s.now().UTC()
	ok := s.tick(ctx)
	h.recordRun("supply", stableSupplyTick, start, start.Add(stableSupplyTick), ok)
	if ran, ok := s.syncHistory(ctx); ran {
		h.recordRun("supply_history", stableChartsEvery, start, s.historyNextDue(), ok)
	}
	if ran, ok := s.syncRates(ctx); ran {
		h.recordRun("rates", stableRatesEvery, start, s.ratesNextDue(), ok)
	}
	s.prune(ctx)
	h.checkStalls()
}

// runStableSamplers runs runStableOnce at boot and every supply tick until
// ctx ends, plus a watchdog that notices a stuck iteration.
func runStableSamplers(ctx context.Context, s *stableSupplySampler, h *stableHealth) {
	go func() {
		t := time.NewTicker(stableSupplyTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.checkStalls()
			}
		}
	}()
	t := time.NewTicker(stableSupplyTick)
	defer t.Stop()
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("stablecoin sampler panic", "panic", r)
				}
			}()
			runStableOnce(ctx, s, h)
		}()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
