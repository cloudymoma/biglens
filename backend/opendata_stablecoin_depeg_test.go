package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseStablecoinPrices(t *testing.T) {
	want := map[string]bool{"tether": true, "usd-coin": true, "euro-coin": true}
	rows, err := parseStablecoinPrices(readStableFixture(t, "replay/prices_2023-03.json"), want)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 31*3 {
		t.Fatalf("rows = %d, want 93 (31 days × 3 coins)", len(rows))
	}
	got := map[string]float64{}
	for _, r := range rows {
		got[r.Day+"|"+r.GeckoID] = r.PriceUSD
	}
	// The integer 1 is a price like any other.
	if got["2023-03-12|usd-coin"] != 0.961065 || got["2023-03-13|tether"] != 1.01 {
		t.Errorf("2023-03-12 usd-coin = %v, 2023-03-13 tether = %v", got["2023-03-12|usd-coin"], got["2023-03-13|tether"])
	}
	// Coins outside the wanted set are skipped; a null price is absent, not 0.
	rows, err = parseStablecoinPrices([]byte(`[{"date": 1678665600, "prices": {"tether": 1, "dai": 0.9, "usd-coin": null}}]`), want)
	if err != nil || len(rows) != 1 || rows[0].GeckoID != "tether" || rows[0].Day != "2023-03-13" {
		t.Errorf("filtered = %+v, %v", rows, err)
	}
	// The live response starts with an undated row ("date": 0) and carries
	// string prices for some other coins; neither may reject the response,
	// and a malformed value of a wanted coin only drops that value.
	live := `[{"date": 0, "prices": {"tether": 1.0002, "frankencoin": "abc"}},
	  {"date": 1678665600, "prices": {"tether": 1, "frankencoin": "1.1017182", "usd-coin": "n/a"}},
	  {"date": 1678752000, "prices": {"tether": 0.9999, "usd-coin": 0.9991, "euro-coin": -1}}]`
	rows, err = parseStablecoinPrices([]byte(live), want)
	if err != nil || len(rows) != 3 {
		t.Fatalf("live-shaped rows = %+v, %v (want tether ×2 and usd-coin on 2023-03-14)", rows, err)
	}
	for _, r := range rows {
		if r.Day < "2023-03-13" || (r.GeckoID != "tether" && r.GeckoID != "usd-coin") {
			t.Errorf("unexpected row %+v", r)
		}
	}
	for name, body := range map[string]string{
		"not json":       `<html>`,
		"an object":      `{"prices": []}`,
		"empty":          `[]`,
		"only undated":   `[{"date": 0, "prices": {"tether": 1}}]`,
		"bad date":       `[{"date": "soon", "prices": {"tether": 1}}]`,
		"bad price":      `[{"date": 1678665600, "prices": {"tether": "one"}}]`,
		"negative price": `[{"date": 1678665600, "prices": {"tether": -1}}]`,
	} {
		if _, err := parseStablecoinPrices([]byte(body), want); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseBitstampOHLC(t *testing.T) {
	rows, err := parseBitstampOHLC(readStableFixture(t, "replay/bitstamp_ohlc_usdcusd_2023-03.json"), "USDC/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 || rows[0].Day != "2023-03-08" || rows[7].Day != "2023-03-15" {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[3]; r.Day != "2023-03-11" || r.Low != 0.80526 || r.Open != 0.99729 || r.High != 0.99975 || r.Close != 0.96853 {
		t.Errorf("2023-03-11 = %+v", r)
	}
	good := string(readStableFixture(t, "replay/bitstamp_ohlc_usdcusd_2023-03.json"))
	if rows, err := parseBitstampOHLC([]byte(`{"data": {"pair": "USDC/USD", "ohlc": []}}`), "USDC/USD"); err != nil || len(rows) != 0 {
		t.Errorf("empty page = %+v, %v", rows, err)
	}
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"other pair": strings.Replace(good, `"USDC/USD"`, `"USDT/USD"`, 1),
		"bad low":    strings.Replace(good, `"0.80526"`, `"low"`, 1),
		"low > high": strings.Replace(good, `"0.80526"`, `"1.5"`, 1),
		"no data":    `{"code": "API0001"}`,
	} {
		if _, err := parseBitstampOHLC([]byte(body), "USDC/USD"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Appendix C seeds: a top-level annotations list (UST, BUSD, DAI, FDUSD and
// USDe are not core assets), every entry "status": "unknown" until M4.
func TestStableAnnotations(t *testing.T) {
	f := loadStableFacts("", nil)
	if len(f.Annotations) != 7 {
		t.Fatalf("annotations = %+v, want the 7 Appendix C entries", f.Annotations)
	}
	assets := map[string]bool{}
	for _, a := range f.Annotations {
		assets[a.Asset] = true
		if a.Status != "unknown" || a.SourceURL != "" {
			t.Errorf("seed %+v must be status unknown without a source", a)
		}
	}
	for _, want := range []string{"USDT", "USDC", "UST", "BUSD", "DAI", "FDUSD", "USDe"} {
		if !assets[want] {
			t.Errorf("no annotation for %s", want)
		}
	}
	base := `{"version": "t", "assets": {"usdt": {"status": "unknown"}}, "annotations": [%s]}`
	ok := []string{
		`{"date": "2023-03-11", "asset": "USDC", "title": "SVB", "status": "unknown"}`,
		`{"date": "2022-05", "asset": "UST", "title": "collapse", "status": "unknown"}`,
		`{"date": "2025-10-10", "end_date": "2025-10-11", "asset": "USDe", "title": "dislocation", "low_price": 0.65, "status": "unknown"}`,
		`{"date": "2023-03-11", "asset": "USDC", "title": "SVB", "low_price": 0.80526, "source_url": "https://example.com/svb"}`,
	}
	for _, a := range ok {
		if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", a, 1))); err != nil {
			t.Errorf("%s rejected: %v", a, err)
		}
	}
	bad := map[string]string{
		"no title":          `{"date": "2023-03-11", "asset": "USDC", "status": "unknown"}`,
		"no asset":          `{"date": "2023-03-11", "title": "x", "status": "unknown"}`,
		"bad date":          `{"date": "March 2023", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"end before start":  `{"date": "2023-03-11", "end_date": "2023-03-10", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"checked no source": `{"date": "2023-03-11", "asset": "USDC", "title": "x"}`,
		"http source":       `{"date": "2023-03-11", "asset": "USDC", "title": "x", "source_url": "http://example.com"}`,
		"other status":      `{"date": "2023-03-11", "asset": "USDC", "title": "x", "status": "verified"}`,
		"zero low":          `{"date": "2023-03-11", "asset": "USDC", "title": "x", "low_price": 0, "status": "unknown"}`,
		"unknown field":     `{"date": "2023-03-11", "asset": "USDC", "title": "x", "status": "unknown", "color": "red"}`,
	}
	for name, a := range bad {
		if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", a, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An operator override replaces the list as a whole and is badged.
	override := `{"version": "op-1", "annotations": [{"date": "2023-03-11", "asset": "USDC", "title": "SVB", "source_url": "https://example.com/svb"}]}`
	got := loadStableFacts("/override.json", func(string) ([]byte, error) { return []byte(override), nil })
	if got.OverrideStatus != "applied" || !got.AnnotationsOverridden || len(got.Annotations) != 1 || got.Annotations[0].SourceURL == "" {
		t.Errorf("override = status %s, overridden %v, %+v", got.OverrideStatus, got.AnnotationsOverridden, got.Annotations)
	}
	invalid := `{"version": "op-2", "annotations": [{"date": "2023-03-11", "asset": "USDC", "title": "SVB"}]}`
	got = loadStableFacts("/override.json", func(string) ([]byte, error) { return []byte(invalid), nil })
	if got.OverrideStatus != "invalid" || got.AnnotationsOverridden || len(got.Annotations) != 7 {
		t.Errorf("invalid override = status %s, %d annotations", got.OverrideStatus, len(got.Annotations))
	}
}

// depegStub serves DefiLlama prices, the ECB EUR/USD history and Bitstamp
// daily candles (paged by start/limit like the real endpoint).
type depegStub struct {
	mu   sync.Mutex
	hits map[string][]string // path → raw queries
	srv  *httptest.Server
}

func (d *depegStub) queries(path string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.hits[path]...)
}

func newDepegStub(t *testing.T) *depegStub {
	t.Helper()
	prices := readStableFixture(t, "replay/prices_2023-03.json")
	exr := readStableFixture(t, "ecb_exr_usd.json")
	d := &depegStub{hits: map[string][]string{}}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.hits[r.URL.Path] = append(d.hits[r.URL.Path], r.URL.RawQuery)
		d.mu.Unlock()
		switch {
		case r.URL.Path == "/llama/stablecoinprices":
			w.Write(prices)
		case r.URL.Path == "/ecb/EXR/D.USD.EUR.SP00.A":
			w.Write(exr)
		case strings.HasPrefix(r.URL.Path, "/bitstamp/api/v2/ohlc/"):
			serveStableOHLC(t, w, r, strings.Trim(strings.TrimPrefix(r.URL.Path, "/bitstamp/api/v2/ohlc/"), "/"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *depegStub) endpoints() stableEndpoints {
	return stableEndpoints{LlamaBase: d.srv.URL + "/llama", ECBBase: d.srv.URL + "/ecb", BitstampBase: d.srv.URL + "/bitstamp"}
}

func TestSyncDepegHistory(t *testing.T) {
	stub := newDepegStub(t)
	store := newTestRiskStore(t)
	ctx := context.Background()
	// A clock just after the replay window keeps the 4-day candle windows few.
	clock := time.Date(2023, 3, 19, 12, 0, 0, 0, time.UTC)
	p := newStablePegSampler(StablecoinConfig{}.settings(), store, stub.endpoints(), nil, func() time.Time { return clock })
	// Start two days before the first candle: the first window is partly
	// before the data, as a window before a pair's listing is (live: USDT's
	// first 1000-day window held only 109 candles), and paging must go on.
	p.ohlcLimit = 4
	p.ohlcFrom = time.Date(2023, 3, 6, 0, 0, 0, 0, time.UTC)
	ran, ok := p.syncDepegHistory(ctx)
	if !ran || !ok {
		t.Fatalf("first sync: ran %v ok %v", ran, ok)
	}
	// DefiLlama daily prices for the core gecko ids.
	if n := countRows(t, store, "stable_price_daily", `WHERE source = 'defillama'`); n != 93 {
		t.Errorf("price rows = %d, want 93", n)
	}
	if pr, _ := store.stablePriceDailySeries(ctx, "usd-coin", "2023-03-12"); len(pr) == 0 || pr[0].Day != "2023-03-12" || pr[0].PriceUSD != 0.961065 {
		t.Errorf("usd-coin from 2023-03-12 = %+v", pr)
	}
	// Bitstamp daily candles in 4-day windows (Bitstamp answers start..start
	// + limit days, not limit candles): the intraday lows.
	usdc, _ := store.stablePegDailySeries(ctx, "usdc", stablePegSourceBitstamp, "2000-01-01")
	if len(usdc) != 8 {
		t.Fatalf("usdc candles = %d, want 8", len(usdc))
	}
	for _, r := range usdc {
		if r.Day == "2023-03-11" {
			approxPtr(t, "usdc 2023-03-11 low", r.Low, 0.80526, 1e-12)
			approxPtr(t, "usdc 2023-03-11 min_dev_bps", r.MinDevBps, (0.80526-1)*1e4, 1e-9)
			if r.MinutesGE30 != nil || r.Samples != 1 {
				t.Errorf("candle rollup fields = %+v (minutes unknown from OHLC)", r)
			}
		}
	}
	if eurc, _ := store.stablePegDailySeries(ctx, "eurc", stablePegSourceBitstamp, "2000-01-01"); len(eurc) != 0 {
		t.Errorf("eurc candles = %d, want none (no fixture)", len(eurc))
	}
	// Bitstamp's eurceur book is too thin for the history (§7.2): never read.
	if q := stub.queries("/bitstamp/api/v2/ohlc/eurceur/"); len(q) != 0 {
		t.Errorf("eurceur candles requested: %v", q)
	}
	qs := stub.queries("/bitstamp/api/v2/ohlc/usdcusd/")
	// 03-06 (2 candles), 03-10 (4), 03-14 (2), 03-18 (none; the next window starts after the clock).
	windows := []time.Time{p.ohlcFrom, p.ohlcFrom.AddDate(0, 0, 4), p.ohlcFrom.AddDate(0, 0, 8), p.ohlcFrom.AddDate(0, 0, 12)}
	if len(qs) != len(windows) {
		t.Fatalf("usdcusd requests = %v, want %d windows", qs, len(windows))
	}
	for i, w := range windows {
		if !strings.Contains(qs[i], "start="+strconv.FormatInt(w.Unix(), 10)) || !strings.Contains(qs[i], "limit=4") {
			t.Errorf("usdcusd request %d = %q, want start %v", i, qs[i], w)
		}
	}
	// The ECB EUR/USD history (for EURC's DefiLlama prices) is loaded once.
	if q := stub.queries("/ecb/EXR/D.USD.EUR.SP00.A"); len(q) != 1 || !strings.Contains(q[0], "lastNObservations=1500") {
		t.Errorf("ecb history requests = %v", q)
	}
	if fx, _ := store.stableRatesSeries(ctx, stableSeriesECBEURUSD, "2026-10-01"); len(fx) != 3 {
		t.Errorf("ecb rows = %d", len(fx))
	}

	// Not due again within 24 h.
	clock = clock.Add(time.Hour)
	if ran, _ := p.syncDepegHistory(ctx); ran {
		t.Error("history re-synced within 24 h")
	}
	// A day later: candles resume 3 days before the newest stored day.
	clock = clock.Add(24 * time.Hour)
	if ran, ok := p.syncDepegHistory(ctx); !ran || !ok {
		t.Fatalf("second sync: ran %v ok %v", ran, ok)
	}
	qs = stub.queries("/bitstamp/api/v2/ohlc/usdcusd/")
	resume := time.Date(2023, 3, 12, 0, 0, 0, 0, time.UTC).Unix() // newest 2023-03-15 − 3 days
	if len(qs) < 5 || !strings.Contains(qs[4], "start="+strconv.FormatInt(resume, 10)) {
		t.Errorf("incremental requests = %v, want start=%d", qs, resume)
	}
	if fx := stub.queries("/ecb/EXR/D.USD.EUR.SP00.A"); len(fx) != 1 {
		t.Errorf("ecb history refetched: %v", fx)
	}

	// Disabled providers are never called.
	stub2 := newDepegStub(t)
	cfg := StablecoinConfig{Providers: map[string]bool{"defillama": false, "bitstamp": false, "ecb": false}}
	p2 := newStablePegSampler(cfg.settings(), newTestRiskStore(t), stub2.endpoints(), nil, func() time.Time { return clock })
	p2.syncDepegHistory(ctx)
	for _, path := range []string{"/llama/stablecoinprices", "/ecb/EXR/D.USD.EUR.SP00.A", "/bitstamp/api/v2/ohlc/usdcusd/"} {
		if q := stub2.queries(path); len(q) != 0 {
			t.Errorf("disabled provider path %s called: %v", path, q)
		}
	}
}

// The history job runs with the peg sampler and reports its own health.
func TestRunStablePegOnceRunsHistory(t *testing.T) {
	stub := newDepegStub(t)
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	settings := StablecoinConfig{Providers: map[string]bool{"coinbase": false, "kraken": false, "bitstamp": true, "okx": false,
		"binance": false, "uniswap_rpc": false}}.settings()
	h := newStableHealth(settings, func() time.Time { return clock })
	p := newStablePegSampler(settings, newTestRiskStore(t), stub.endpoints(), nil, func() time.Time { return clock })
	runStablePegOnce(context.Background(), p, h)
	var hist *StableSamplerHealth
	for _, s := range h.samplers() {
		if s.Name == "peg_history" {
			s := s
			hist = &s
		}
	}
	if hist == nil || hist.LastOK == "" || hist.NextDue == "" {
		t.Errorf("peg_history health = %+v", hist)
	}
}

// Depeg-day classification (§7.2): a day counts by its daily close (the
// BigLens consensus rollup, else DefiLlama), a Bitstamp low is a marker only
// when ≥ 100 bps off and confirmed by a close ≥ 30 bps off that day, and
// EURC's DefiLlama days use the same-day ECB rate at 100 bps.
func TestStableClassifyDepeg(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	roll := func(day string, close, minDev float64, ge100 int) stablePegDaily {
		return stablePegDaily{Day: day, Source: stablePegSourceConsensus, Close: f(close), MinDevBps: f(minDev), MaxDevBps: f((close - 1) * 1e4),
			MinutesGE30: n(ge100 + 30), MinutesGE100: n(ge100), Samples: 288}
	}
	candle := func(day string, low, close float64) stablePegDaily {
		return stablePegDaily{Day: day, Source: stablePegSourceBitstamp, Low: f(low), Close: f(close), High: f(1.0001),
			MinDevBps: f((low - 1) * 1e4), MaxDevBps: f(1), Samples: 1}
	}
	price := func(day string, p float64) stablePriceDaily {
		return stablePriceDaily{Day: day, GeckoID: "usd-coin", PriceUSD: p, Source: stablePriceSourceLlama}
	}
	usd := stableDepegInputs{Numeraire: "USD", Today: "2024-01-20",
		Rollup: []stablePegDaily{
			roll("2024-01-10", 0.9968, -40, 0),   // close −32 bps: a depeg day, although DefiLlama is at par
			roll("2024-01-12", 0.9990, -120, 20), // close −10 bps outranks DefiLlama's −50; 20 min at ≥ 100 bps
		},
		Prices: []stablePriceDaily{
			price("2024-01-10", 0.9999),
			price("2024-01-11", 0.9965), // no rollup: DefiLlama −35 bps
			price("2024-01-12", 0.9950),
			price("2024-01-13", 0.9980),
			price("2024-01-14", 0.99756),
			price("2024-01-16", 1.003), // exactly +30 bps (29.99999999999989 in float64)
		},
		Candles: []stablePegDaily{
			candle("2024-01-13", 0.9850, 0.9990),   // a −150 bps wick; close −10, DefiLlama −20: no marker
			candle("2024-01-14", 0.80526, 0.96853), // confirmed by the candle's own close (−315 bps)
			candle("2024-01-15", 0.9905, 0.9960),   // low −95 bps: under the marker threshold
			candle("2024-01-16", 0.99, 1.0),        // low exactly −100 bps, confirmed by DefiLlama at exactly 30
		},
	}
	h := stableClassifyDepeg(usd)
	type day struct {
		day, source string
		dev, thr    float64
	}
	want := []day{{"2024-01-16", "defillama", 30, 30}, {"2024-01-11", "defillama", -35, 30}, {"2024-01-10", "consensus", -32, 30}}
	if len(h.Days) != len(want) || h.Total != len(want) {
		t.Fatalf("usd days = %+v (total %d), want %v", h.Days, h.Total, want)
	}
	for i, w := range want {
		d := h.Days[i]
		if d.Day != w.day || d.Source != w.source || math.Abs(d.CloseDevBps-w.dev) > 1e-6 || d.ThresholdBps != w.thr {
			t.Errorf("usd day %d = %+v, want %+v", i, d, w)
		}
	}
	if d := h.Days[2]; d.MinutesGE30 == nil || *d.MinutesGE30 != 30 || d.Close != 0.9968 {
		t.Errorf("consensus day = %+v, want its close and minutes", d)
	}
	if len(h.Markers) != 2 || h.Markers[0].Day != "2024-01-16" || h.Markers[1].Day != "2024-01-14" {
		t.Fatalf("usd markers = %+v, want 2024-01-16 and 2024-01-14", h.Markers)
	}
	if m := h.Markers[0]; strings.Join(m.ConfirmedBy, ",") != "defillama" || m.Low != 0.99 || m.Source != stablePegSourceBitstamp {
		t.Errorf("marker 2024-01-16 = %+v", m)
	}
	if m := h.Markers[1]; strings.Join(m.ConfirmedBy, ",") != "bitstamp_close" || m.Close == nil || *m.Close != 0.96853 ||
		math.Abs(m.LowDevBps-(0.80526-1)*1e4) > 1e-9 {
		t.Errorf("marker 2024-01-14 = %+v", m)
	}
	if l := h.LastGE100; l == nil || l.Day != "2024-01-16" || l.Source != stablePegSourceBitstamp || l.DaysSince != 4 || math.Abs(l.DevBps+100) > 1e-6 {
		t.Errorf("last ≥ 100 bps = %+v, want the 2024-01-16 marker, 4 days ago", h.LastGE100)
	}
	if h.HistoryFrom != "2024-01-10" {
		t.Errorf("history from %q", h.HistoryFrom)
	}
	// Without the candles, the newest ≥ 100 bps deviation is the consensus
	// on 2024-01-12: 20 minutes at ≥ 100 bps (S1's 15-minute window).
	noCandles := usd
	noCandles.Candles = nil
	if l := stableClassifyDepeg(noCandles).LastGE100; l == nil || l.Day != "2024-01-12" || l.Source != "consensus_intraday" || l.DevBps != -120 || l.DaysSince != 8 {
		t.Errorf("last ≥ 100 bps without candles = %+v", l)
	}
	// 10 minutes is not sustained: nothing at ≥ 100 bps in the history.
	short := noCandles
	short.Rollup = []stablePegDaily{roll("2024-01-12", 0.9990, -120, 10)}
	if h := stableClassifyDepeg(short); h.LastGE100 != nil || h.HistoryFrom != "2024-01-10" {
		t.Errorf("10 minutes at ≥ 100 bps: last %+v, history from %q", h.LastGE100, h.HistoryFrom)
	}

	// EURC: euro terms. The consensus close counts at 30 bps; DefiLlama's
	// USD price ÷ the same-day ECB rate at 100 bps; no same-day rate (a
	// Saturday) leaves the day unclassified.
	eur := stableDepegInputs{Numeraire: "EUR", Today: "2024-01-20",
		Rollup: []stablePegDaily{roll("2024-01-18", 1.0035, 0, 0)},
		Prices: []stablePriceDaily{
			price("2024-01-13", 1.1500), // Saturday: Friday's rate would make it +267.9 bps
			price("2024-01-15", 1.1350), // ÷ 1.12 = +133.9 bps
			price("2024-01-16", 1.1260), // ÷ 1.12 = +53.6 bps: under 100
		},
		ECB: map[string]float64{"2024-01-12": 1.12, "2024-01-15": 1.12, "2024-01-16": 1.12},
	}
	h = stableClassifyDepeg(eur)
	if len(h.Days) != 2 || h.Days[0].Day != "2024-01-18" || h.Days[0].Source != "consensus" || h.Days[0].ThresholdBps != 30 ||
		h.Days[1].Day != "2024-01-15" || h.Days[1].Source != "defillama" || h.Days[1].ThresholdBps != 100 ||
		math.Abs(h.Days[1].CloseDevBps-(1.1350/1.12-1)*1e4) > 1e-9 || math.Abs(h.Days[1].Close-1.1350/1.12) > 1e-12 {
		t.Fatalf("eurc days = %+v", h.Days)
	}
	if len(h.Markers) != 0 || h.LastGE100 == nil || h.LastGE100.Day != "2024-01-15" || h.LastGE100.DaysSince != 5 || h.HistoryFrom != "2024-01-15" {
		t.Errorf("eurc markers %+v, last ≥ 100 %+v, history from %q", h.Markers, h.LastGE100, h.HistoryFrom)
	}
	// The same-day rule.
	if v := stableDevFromUSD(1.15, "2024-01-13", eur.ECB); v != nil {
		t.Errorf("Saturday deviation = %v, want none without a same-day rate", *v)
	}
	// Empty inputs: nothing, never a substitute.
	if h := stableClassifyDepeg(stableDepegInputs{Numeraire: "USD", Today: "2024-01-20"}); len(h.Days) != 0 || h.Days == nil ||
		len(h.Markers) != 0 || h.Markers == nil || h.LastGE100 != nil || h.HistoryFrom != "" {
		t.Errorf("empty history = %+v", h)
	}
}
