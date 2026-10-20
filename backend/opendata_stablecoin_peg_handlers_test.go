package main

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newStablePegTestHandler wires the supply and peg samplers to the stubs
// (venues, Uniswap pool, DefiLlama prices, ECB, Bitstamp candles).
func newStablePegTestHandler(t *testing.T, cfg StablecoinConfig, clock *time.Time) (*APIHandler, *stableStub, *stableVenueStub) {
	t.Helper()
	stub, venues, ep := stableVerifyEndpoints(t)
	rpc, _ := newUniswapRPCStub(t, nil)
	rpcs := func(string) []string { return []string{rpc} }
	h := newStableTestHandler(t, cfg, ep, nil, clock)
	h.stable.peg = newStablePegSampler(h.stable.settings, h.stable.store, ep, rpcs, func() time.Time { return *clock })
	h.stable.peg.onFetch = h.stable.health.recordFetch
	return h, stub, venues
}

func TestStablecoinPegEndpoint(t *testing.T) {
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	h, _, venues := newStablePegTestHandler(t, StablecoinConfig{}, &clock)

	// Cold: no peg sample yet, everything unknown, still 200.
	rec := stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt")
	if rec.Code != http.StatusOK {
		t.Fatalf("cold status %d", rec.Code)
	}
	cold := decodeStable[StablePegResponse](t, rec)
	if !cold.WarmingUp || len(cold.Assets) != 1 || cold.Assets[0].State != "unknown" || cold.Assets[0].Consensus != nil || len(cold.Markets) != 0 {
		t.Errorf("cold peg = %+v", cold)
	}
	if venues.count("/kraken/0/public/Ticker") != 0 {
		t.Fatal("/peg called an upstream")
	}

	runStablePegOnce(context.Background(), h.stable.peg, h.stable.health)
	h.cache = NewCache(time.Minute)
	before := venues.count("/kraken/0/public/Ticker")
	resp := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdc&range=24h"))
	if venues.count("/kraken/0/public/Ticker") != before {
		t.Error("/peg fetched upstream data")
	}
	latest := h.stable.peg.latest()
	if resp.WarmingUp || resp.Stale || resp.Range != "24h" || len(resp.Assets) != 1 {
		t.Fatalf("peg = %+v", resp)
	}
	usdc := resp.Assets[0]
	if usdc.Asset != "usdc" || usdc.Consensus == nil || *usdc.Consensus != *latest.Assets["usdc"].Consensus || usdc.VenuesUsed != 3 || len(usdc.Quotes) != 3 {
		t.Errorf("usdc latest = %+v", usdc.StablePeg)
	}
	// The consensus and dispersion series (5-minute samples for 24h).
	if usdc.SeriesResolution != "5m" || len(usdc.Series) != 1 || usdc.Series[0].Consensus == nil || usdc.Series[0].DispBps == nil {
		t.Errorf("usdc series = %s %+v", usdc.SeriesResolution, usdc.Series)
	}
	// Depeg history: the Bitstamp intraday low of 2023-03-11 and the DefiLlama
	// 2023-03-12 row are days at ≥ 30 bps; annotations for the asset.
	var low, llama bool
	for _, d := range usdc.DepegDays {
		if d.Day == "2023-03-11" && d.Source == stablePegSourceBitstamp && d.MinDevBps != nil && math.Abs(*d.MinDevBps-(0.80526-1)*1e4) < 1e-9 {
			low = true
		}
		if d.Day == "2023-03-12" && d.Source == stablePriceSourceLlama && d.MinDevBps != nil && math.Abs(*d.MinDevBps-(0.961065-1)*1e4) < 1e-9 {
			llama = true
		}
	}
	if !low || !llama {
		t.Errorf("depeg days lack the 2023-03 lows (bitstamp %v, defillama %v): %+v", low, llama, usdc.DepegDays)
	}
	if len(usdc.Annotations) != 1 || usdc.Annotations[0].Date != "2023-03-11" {
		t.Errorf("usdc annotations = %+v", usdc.Annotations)
	}
	// Venue table, relative peg (exchange median and Uniswap), FX, all annotations.
	if len(resp.Markets) != len(stableVenuePairs)+1 || resp.Relative.Median == nil || resp.Relative.ExchangeMedian == nil ||
		resp.Relative.Uniswap == nil || len(resp.Relative.Series) != 1 || resp.FX == nil || resp.FX.EURUSD == nil {
		t.Errorf("peg venue table %d, relative %+v, fx %+v", len(resp.Markets), resp.Relative, resp.FX)
	}
	if len(resp.Annotations) != 7 || resp.AnnotationsOverridden || resp.DepegThresholdBps != 30 {
		t.Errorf("annotations %d (overridden %v), threshold %v", len(resp.Annotations), resp.AnnotationsOverridden, resp.DepegThresholdBps)
	}

	// EURC: euro terms, with the EUR-only flags.
	eurc := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=eurc")).Assets[0]
	if eurc.Numeraire != "EUR" || eurc.SecondDirectEURVenue == nil || eurc.PartiallyDependent == nil {
		t.Errorf("eurc = %+v", eurc.StablePeg)
	}
	// All assets; long ranges use the daily rollup.
	all := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?range=all"))
	if len(all.Assets) != 3 || all.Range != "all" || all.Assets[0].SeriesResolution != "1d" || len(all.Assets[0].Series) != 1 ||
		all.Assets[0].Series[0].MinutesGE30 == nil {
		t.Errorf("range=all: %d assets, %+v", len(all.Assets), all.Assets)
	}
	// Whitelists: a bad asset is a generic 400; an unknown range falls back.
	for _, bad := range []string{"dai", "USDT", "usdt'--"} {
		rec := stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset="+bad)
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), bad) {
			t.Errorf("asset=%s = %d %q", bad, rec.Code, rec.Body.String())
		}
	}
	if r := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt&range=10y")); r.Range != "7d" {
		t.Errorf("range=10y -> %q, want 7d", r.Range)
	}

	// A sample older than 3 intervals is served marked stale, never refreshed.
	clock = clock.Add(16 * time.Minute)
	h.cache = NewCache(time.Minute)
	if r := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt")); !r.Stale || r.Assets[0].Consensus == nil {
		t.Errorf("16 min old sample: stale %v, consensus %v", r.Stale, r.Assets[0].Consensus)
	}
}

func TestStablecoinLivePegFields(t *testing.T) {
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	h, _, venues := newStablePegTestHandler(t, StablecoinConfig{}, &clock)
	cold := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	if !cold.PegWarmingUp || cold.Assets[0].Consensus != nil || cold.Assets[0].VenuesUsed != nil {
		t.Errorf("cold live = %+v", cold)
	}
	runStablePegOnce(context.Background(), h.stable.peg, h.stable.health)
	clock = clock.Add(4 * time.Minute) // second sample, still fresh, in the next bucket
	runStablePegOnce(context.Background(), h.stable.peg, h.stable.health)
	h.cache = NewCache(time.Minute)
	before := venues.count("/coinbase/products/USDT-USD/ticker")
	live := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	if venues.count("/coinbase/products/USDT-USD/ticker") != before {
		t.Error("/live fetched upstream data")
	}
	latest := h.stable.peg.latest()
	if live.PegWarmingUp {
		t.Error("peg still warming up after two samples")
	}
	for _, a := range live.Assets {
		p := latest.Assets[a.Asset]
		if a.Consensus == nil || *a.Consensus != *p.Consensus || a.DevBps == nil || a.VenuesUsed == nil || *a.VenuesUsed != p.VenuesUsed ||
			a.PegState != "ok" || a.PriceAsOf != stableFmt(latest.AsOf) || a.PriceStale {
			t.Errorf("live %s = %+v", a.Asset, a)
		}
		if a.DevMin24hBps == nil || a.DevMax24hBps == nil || *a.DevMin24hBps > *a.DevMax24hBps {
			t.Errorf("live %s 24h range = %v..%v", a.Asset, a.DevMin24hBps, a.DevMax24hBps)
		}
	}
	// Stale after 3 missed intervals; the last value stays visible, flagged.
	clock = clock.Add(16 * time.Minute)
	h.cache = NewCache(time.Minute)
	if a := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live")).Assets[0]; !a.PriceStale || a.Consensus == nil {
		t.Errorf("stale live = %+v", a)
	}
}
