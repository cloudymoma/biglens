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
	// Depeg history (§7.2): a day counts by its close. USDC's DefiLlama
	// 2023-03-12 row (−389 bps) is a depeg day; 2023-03-11 (−24 bps) is not,
	// but its Bitstamp low 0.80526 is a marker, confirmed by the candle's own
	// close (−315 bps).
	byDay := map[string]StableDepegDay{}
	for _, d := range usdc.DepegDays {
		byDay[d.Day] = d
	}
	if d, ok := byDay["2023-03-12"]; !ok || d.Source != stablePriceSourceLlama || math.Abs(d.CloseDevBps-(0.961065-1)*1e4) > 1e-9 || d.ThresholdBps != 30 {
		t.Errorf("usdc 2023-03-12 = %+v (listed %v)", d, ok)
	}
	if d, ok := byDay["2023-03-11"]; ok {
		t.Errorf("2023-03-11 counted by its intraday low: %+v", d)
	}
	if usdc.DepegDaysTotal != 10 || len(usdc.DepegDays) != 10 {
		t.Errorf("usdc depeg days = %d (listed %d), want the 10 DefiLlama days at ≥ 30 bps", usdc.DepegDaysTotal, len(usdc.DepegDays))
	}
	var marks []string
	for _, m := range usdc.LowMarkers {
		marks = append(marks, m.Day+":"+strings.Join(m.ConfirmedBy, "+"))
	}
	if got := strings.Join(marks, " "); got != "2023-03-13:bitstamp_close+defillama 2023-03-12:bitstamp_close+defillama 2023-03-11:bitstamp_close" {
		t.Errorf("usdc low markers = %s", got)
	}
	if m := usdc.LowMarkers[2]; m.Low != 0.80526 || math.Abs(m.LowDevBps-(0.80526-1)*1e4) > 1e-9 {
		t.Errorf("usdc 2023-03-11 marker = %+v", m)
	}
	since := int(clock.Sub(time.Date(2023, 3, 13, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	if l := usdc.LastDeviation; l == nil || l.Day != "2023-03-13" || l.Source != stablePegSourceBitstamp || math.Abs(l.DevBps+500) > 1e-9 || l.DaysSince != since {
		t.Errorf("usdc last ≥ 100 bps = %+v, want the 2023-03-13 low, %d days ago", l, since)
	}
	if usdc.HistoryFrom != "2023-03-01" || resp.LowMarkerBps != 100 {
		t.Errorf("usdc history from %q, low marker threshold %v", usdc.HistoryFrom, resp.LowMarkerBps)
	}
	if len(usdc.Annotations) != 1 || usdc.Annotations[0].Date != "2023-03-11" {
		t.Errorf("usdc annotations = %+v", usdc.Annotations)
	}
	// Venue table, relative peg (exchange median and Uniswap), FX, all annotations.
	if len(resp.Markets) != len(stableVenuePairs)+1 || resp.Relative.Median == nil || resp.Relative.ExchangeMedian == nil ||
		resp.Relative.Uniswap == nil || len(resp.Relative.Series) != 1 || resp.FX == nil || resp.FX.EURUSD == nil {
		t.Errorf("peg venue table %d, relative %+v, fx %+v", len(resp.Markets), resp.Relative, resp.FX)
	}
	if resp.FXSingleVenue || resp.FX.VenuesUsed != 2 {
		t.Errorf("fx from Kraken and Bitstamp: single venue %v, venues used %d", resp.FXSingleVenue, resp.FX.VenuesUsed)
	}
	if len(resp.Annotations) != 7 || resp.AnnotationsOverridden || resp.DepegThresholdBps != 30 {
		t.Errorf("annotations %d (overridden %v), threshold %v", len(resp.Annotations), resp.AnnotationsOverridden, resp.DepegThresholdBps)
	}

	// EURC: euro terms, with the EUR-only flags. A stored eurceur candle
	// (written before M3) is never used: no markers for EURC.
	lo, cl := 0.95, 0.96
	if err := h.stable.store.upsertStablePegDaily(context.Background(), []stablePegDaily{{Day: "2026-10-18", Asset: "eurc", Source: stablePegSourceBitstamp,
		Low: &lo, Close: &cl, MinDevBps: stableF(-500), MaxDevBps: stableF(0), Samples: 1, UpdatedAt: clock}}); err != nil {
		t.Fatal(err)
	}
	h.cache = NewCache(time.Minute)
	eurc := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=eurc")).Assets[0]
	if eurc.Numeraire != "EUR" || eurc.SecondDirectEURVenue == nil || eurc.PartiallyDependent == nil {
		t.Errorf("eurc = %+v", eurc.StablePeg)
	}
	if len(eurc.LowMarkers) != 0 || eurc.LastDeviation != nil || len(eurc.DepegDays) != 0 {
		t.Errorf("eurc history: markers %+v, last deviation %+v, days %+v", eurc.LowMarkers, eurc.LastDeviation, eurc.DepegDays)
	}
	// USDT: the 2022-05-13 wick (low −110 bps, close −20, no DefiLlama row)
	// is not a marker; the newest ≥ 100 bps day is DefiLlama's 2023-03-21.
	usdt := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt")).Assets[0]
	marks = nil
	for _, m := range usdt.LowMarkers {
		marks = append(marks, m.Day+":"+strings.Join(m.ConfirmedBy, "+"))
	}
	if got := strings.Join(marks, " "); got != "2022-05-12:bitstamp_close 2022-05-11:bitstamp_close" {
		t.Errorf("usdt low markers = %s", got)
	}
	if l := usdt.LastDeviation; usdt.DepegDaysTotal != 15 || l == nil || l.Day != "2023-03-21" || l.Source != stablePriceSourceLlama {
		t.Errorf("usdt: %d depeg days, last ≥ 100 bps %+v", usdt.DepegDaysTotal, l)
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

	// Kraken down: the EUR/USD consensus comes from Bitstamp alone and /peg
	// says so (§5.2).
	venues.setStatus("/kraken/0/public/Ticker", http.StatusServiceUnavailable)
	clock = clock.Add(5 * time.Minute)
	runStablePegOnce(context.Background(), h.stable.peg, h.stable.health)
	h.cache = NewCache(time.Minute)
	if r := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=eurc")); !r.FXSingleVenue || r.FX == nil || r.FX.VenuesUsed != 1 || r.FX.EURUSD == nil {
		t.Errorf("fx with Kraken down: single venue %v, fx %+v", r.FXSingleVenue, r.FX)
	}
	venues.setStatus("/kraken/0/public/Ticker", 0)

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
