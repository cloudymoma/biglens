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
	if r := rows[3]; r.Day != "2023-03-11" || r.Low != 0.80526 || r.Open != 0.99729 || r.High != 0.99975 || r.Close != 0.96853 || r.Volume == nil || *r.Volume != 1750000.5 {
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

func TestParseKrakenOHLC(t *testing.T) {
	body := `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0001","1.0005","0.9997","1.0002","1.0000","25000.0",40],[1758240000,"1.0002","1.0004","0.9998","0.9999","1.0001","30000.0",50]],"last":1758240000}}`
	rows, err := parseKrakenOHLC([]byte(body), "EURCEUR")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Day != "2025-09-18" || rows[0].Close != 1.0002 || rows[0].VWAP != 1.0000 ||
		rows[1].Day != "2025-09-19" || rows[1].Close != 0.9999 || rows[1].VWAP != 1.0001 {
		t.Fatalf("rows = %+v", rows)
	}
	for name, bad := range map[string]string{
		"not json":   `<html>`,
		"kraken err": `{"error":["EGeneral:Invalid arguments"],"result":{}}`,
		"other pair": `{"error":[],"result":{"USDCUSD":[[1758153600,"1","1","1","1","1","1",1]]}}`,
		"short row":  `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0","1.0","1.0","1.0"]]}}`,
		"low > high": `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0","0.99","1.01","1.0","1.0","1",1]]}}`,
		"zero close": `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0","1.0","1.0","0","1.0","1",1]]}}`,
		"zero vwap":  `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0","1.0","1.0","1.0","0","1",1]]}}`,
		"bad number": `{"error":[],"result":{"EURCEUR":[[1758153600,"1.0","1.0","1.0","n/a","1.0","1",1]]}}`,
	} {
		if _, err := parseKrakenOHLC([]byte(bad), "EURCEUR"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Appendix C: a top-level annotations list (UST, BUSD, DAI, FDUSD and USDe
// are not core assets). Since the M4 curation every entry is "verified"
// with an https source; "unknown" entries carry no checked claim.
func TestStableAnnotations(t *testing.T) {
	f := loadStableFacts("", nil)
	if len(f.Annotations) != 7 {
		t.Fatalf("annotations = %+v, want the 7 Appendix C entries", f.Annotations)
	}
	assets := map[string]bool{}
	for _, a := range f.Annotations {
		assets[a.Asset] = true
		if a.Status != "verified" || !strings.HasPrefix(a.SourceURL, "https://") {
			t.Errorf("annotation %+v must be verified with an https source", a)
		}
		wantKind := "depeg"
		if a.Asset == "BUSD" {
			wantKind = "minting_halt"
		}
		if a.Kind != wantKind {
			t.Errorf("annotation %s kind = %q, want %q", a.Asset, a.Kind, wantKind)
		}
		if a.Asset == "DAI" && a.Extreme != "Coinbase DAI-USD daily low 0.8582 on 2023-03-11" {
			t.Errorf("DAI annotation extreme = %q", a.Extreme)
		}
		if a.Asset == "USDe" && strings.Contains(a.Extreme, "283M") {
			t.Errorf("USDe annotation extreme still cites $283M without source: %q", a.Extreme)
		}
	}
	for _, want := range []string{"USDT", "USDC", "UST", "BUSD", "DAI", "FDUSD", "USDe"} {
		if !assets[want] {
			t.Errorf("no annotation for %s", want)
		}
	}
	base := `{"version": "t", "assets": {"usdt": {"status": "unknown"}}, "annotations": [%s]}`
	ok := []string{
		`{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "SVB", "status": "unknown"}`,
		`{"date": "2022-05", "kind": "depeg", "asset": "UST", "title": "collapse", "status": "unknown"}`,
		`{"date": "2023-02-13", "kind": "minting_halt", "asset": "BUSD", "title": "halt", "status": "unknown"}`,
		`{"date": "2025-10-10", "end_date": "2025-10-11", "kind": "depeg", "asset": "USDe", "title": "dislocation", "low_price": 0.65, "status": "unknown"}`,
		`{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "SVB", "low_price": 0.80526, "source_url": "https://example.com/svb"}`,
		`{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "SVB", "low_price": 0.86, "status": "verified", "source_url": "https://example.com/svb"}`,
	}
	for _, a := range ok {
		if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", a, 1))); err != nil {
			t.Errorf("%s rejected: %v", a, err)
		}
	}
	bad := map[string]string{
		"no title":           `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "status": "unknown"}`,
		"no asset":           `{"date": "2023-03-11", "kind": "depeg", "title": "x", "status": "unknown"}`,
		"no kind":            `{"date": "2023-03-11", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"bad kind":           `{"date": "2023-03-11", "kind": "hack", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"bad date":           `{"date": "March 2023", "kind": "depeg", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"end before start":   `{"date": "2023-03-11", "end_date": "2023-03-10", "kind": "depeg", "asset": "USDC", "title": "x", "status": "unknown"}`,
		"checked no source":  `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x"}`,
		"verified no source": `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x", "status": "verified"}`,
		"http source":        `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x", "source_url": "http://example.com"}`,
		"other status":       `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x", "status": "likely", "source_url": "https://example.com"}`,
		"zero low":           `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x", "low_price": 0, "status": "unknown"}`,
		"unknown field":      `{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "x", "status": "unknown", "color": "red"}`,
	}
	for name, a := range bad {
		if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", a, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An operator override replaces the list as a whole and is badged.
	override := `{"version": "op-1", "annotations": [{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "SVB", "source_url": "https://example.com/svb"}]}`
	got := loadStableFacts("/override.json", func(string) ([]byte, error) { return []byte(override), nil })
	if got.OverrideStatus != "applied" || !got.AnnotationsOverridden || len(got.Annotations) != 1 || got.Annotations[0].SourceURL == "" {
		t.Errorf("override = status %s, overridden %v, %+v", got.OverrideStatus, got.AnnotationsOverridden, got.Annotations)
	}
	invalid := `{"version": "op-2", "annotations": [{"date": "2023-03-11", "kind": "depeg", "asset": "USDC", "title": "SVB"}]}`
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
		case r.URL.Path == "/kraken/0/public/OHLC":
			serveKrakenOHLC(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *depegStub) endpoints() stableEndpoints {
	return stableEndpoints{LlamaBase: d.srv.URL + "/llama", ECBBase: d.srv.URL + "/ecb", BitstampBase: d.srv.URL + "/bitstamp", KrakenBase: d.srv.URL + "/kraken"}
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
		t.Errorf("eurc bitstamp candles = %d, want none (no fixture)", len(eurc))
	}
	// Bitstamp's eurceur daily candles are synced for EURC's pre-Kraken history.
	if q := stub.queries("/bitstamp/api/v2/ohlc/eurceur/"); len(q) != 4 {
		t.Errorf("eurceur candle requests = %v, want 4 windows", q)
	}
	// Kraken EURCEUR daily OHLC is synced for EURC's direct-EUR history,
	// storing VWAP next to Close.
	if eurcK, _ := store.stablePegDailySeries(ctx, "eurc", stablePegSourceKraken, "2000-01-01"); len(eurcK) != 3 || eurcK[0].VWAP == nil || *eurcK[0].VWAP != 1.0 {
		t.Errorf("eurc kraken candles = %+v, want 3 with VWAP stored", eurcK)
	}
	if q := stub.queries("/kraken/0/public/OHLC"); len(q) != 1 || !strings.Contains(q[0], "pair=EURCEUR") || !strings.Contains(q[0], "interval=1440") {
		t.Errorf("kraken ohlc requests = %v", q)
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
	cfg := StablecoinConfig{Providers: map[string]bool{"defillama": false, "bitstamp": false, "ecb": false, "kraken": false}}
	p2 := newStablePegSampler(cfg.settings(), newTestRiskStore(t), stub2.endpoints(), nil, func() time.Time { return clock })
	p2.syncDepegHistory(ctx)
	for _, path := range []string{"/llama/stablecoinprices", "/ecb/EXR/D.USD.EUR.SP00.A", "/bitstamp/api/v2/ohlc/usdcusd/", "/kraken/0/public/OHLC"} {
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

	// EURC: euro terms. The consensus close counts at 30 bps; Kraken EURCEUR
	// daily candles (same-sign min(|close_dev|, |vwap_dev|) at 30 bps) count
	// from 2025-09-18 onward; before 2025-09-18 Bitstamp eurceur closes count
	// at 30 bps only when volume ≥ stableBitstampEURCMinVolume (200k EURC).
	// DefiLlama ÷ ECB is not used for EURC.
	eur := stableDepegInputs{Numeraire: "EUR", Today: "2025-09-25",
		Rollup: []stablePegDaily{roll("2025-09-20", 1.0035, 0, 0)},
		Kraken: []stablePegDaily{
			{Day: "2025-09-18", Source: stablePegSourceKraken, Close: f(1.0002), VWAP: f(1.0001), Low: f(0.9998), High: f(1.0004), Samples: 1},
		},
		Candles: []stablePegDaily{
			{Day: "2023-03-11", Source: stablePegSourceBitstamp, Close: f(0.9630), Low: f(0.9000), High: f(1.0000), Volume: f(2_600_000), Samples: 1},
			{Day: "2024-02-19", Source: stablePegSourceBitstamp, Close: f(0.9900), Low: f(0.9805), High: f(1.0181), Volume: f(175_000), Samples: 1},
		},
		Prices: []stablePriceDaily{
			price("2023-03-13", 1.0980), // ignored for EURC
			price("2024-01-15", 1.1424), // ignored for EURC
		},
		ECB: map[string]float64{"2023-03-13": 1.0706, "2024-01-15": 1.12},
	}
	h = stableClassifyDepeg(eur)
	if len(h.Days) != 2 || h.Days[0].Day != "2025-09-20" || h.Days[0].Source != "consensus" || h.Days[0].ThresholdBps != 30 ||
		h.Days[1].Day != "2023-03-11" || h.Days[1].Source != stablePegSourceBitstamp || h.Days[1].SourceLabel != "Bitstamp close (≥ 200k vol)" ||
		h.Days[1].ThresholdBps != 30 || math.Abs(h.Days[1].CloseDevBps+370) > 1e-6 || math.Abs(h.Days[1].Close-0.9630) > 1e-12 {
		t.Fatalf("eurc days = %+v", h.Days)
	}
	if len(h.Markers) != 0 || h.LastGE100 == nil || h.LastGE100.Day != "2025-09-20" || h.LastGE100.ThresholdBps != 30 || h.LastGE100.DaysSince != 5 || h.HistoryFrom != "2023-03-11" {
		t.Errorf("eurc markers %+v, last %+v, history from %q", h.Markers, h.LastGE100, h.HistoryFrom)
	}
	// Empty inputs: nothing, never a substitute.
	if h := stableClassifyDepeg(stableDepegInputs{Numeraire: "USD", Today: "2024-01-20"}); len(h.Days) != 0 || h.Days == nil ||
		len(h.Markers) != 0 || h.Markers == nil || h.LastGE100 != nil || h.HistoryFrom != "" {
		t.Errorf("empty history = %+v", h)
	}
}

// TestStableEURC2024To2026Quiet pins the EURC per-source rules using
// synthetic fixtures (no verbatim Kraken or Bitstamp rows):
//   - Pre-Kraken Bitstamp segment (before 2025-09-18): classified on the close
//     at 30 bps only when volume ≥ stableBitstampEURCMinVolume (200,000 EURC);
//     all other days have no value (a gap, not a depeg day):
//   - −370 bps at 2.6M volume -> depeg day;
//   - −136 bps at 54k volume -> no value;
//   - −100 bps at 175k volume -> no value;
//   - −40 bps at 250k volume -> depeg day;
//   - −25 bps at 500k volume -> classified close, not a depeg day.
//   - Kraken segment (2025-09-18 onward): classified at 30 bps on the
//     same-sign min(|close_dev|, |vwap_dev|) (0 when signs differ).
func TestStableEURC2024To2026Quiet(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	in := stableDepegInputs{
		Numeraire: "EUR",
		Today:     "2026-10-21",
		Candles: []stablePegDaily{
			// −370 bps at 2.6M volume -> depeg day.
			{Day: "2023-03-11", Source: stablePegSourceBitstamp, Close: f(0.9630), Low: f(0.9000), High: f(1.0000), Volume: f(2_600_000), Samples: 1},
			// −25 bps at 500k volume -> classified close, not a depeg day (even if DefiLlama ÷ ECB had +256 bps).
			{Day: "2023-03-13", Source: stablePegSourceBitstamp, Close: f(0.9975), Low: f(0.9950), High: f(0.9999), Volume: f(500_000), Samples: 1},
			// −100 bps at 175k volume -> no value (below 200k floor).
			{Day: "2024-02-19", Source: stablePegSourceBitstamp, Close: f(0.9900), Low: f(0.9805), High: f(1.0181), Volume: f(175_000), Samples: 1},
			// −136 bps at 54k volume -> no value (below 200k floor).
			{Day: "2024-12-11", Source: stablePegSourceBitstamp, Close: f(0.9864), Low: f(0.9864), High: f(1.0010), Volume: f(54_000), Samples: 1},
		},
		Prices: []stablePriceDaily{
			// DefiLlama ÷ ECB rows are ignored for EURC.
			{Day: "2023-03-13", GeckoID: "euro-coin", PriceUSD: 1.0980, Source: stablePriceSourceLlama},
			{Day: "2025-08-01", GeckoID: "euro-coin", PriceUSD: 1.1600, Source: stablePriceSourceLlama},
		},
		ECB: map[string]float64{
			"2023-03-13": 1.0706,
			"2025-08-01": 1.1404,
		},
		Kraken: []stablePegDaily{
			// Normal quiet day (+2 bps close, +1 bps VWAP -> +1 bps).
			{Day: "2025-09-18", Source: stablePegSourceKraken, Close: f(1.0002), VWAP: f(1.0001), Low: f(0.9998), High: f(1.0003), Samples: 1},
			// Intraday wick: close +3 bps, VWAP +140 bps -> +3 bps (not a depeg day).
			{Day: "2025-11-28", Source: stablePegSourceKraken, Close: f(1.0003), VWAP: f(1.0140), Low: f(0.9960), High: f(2.0000), Samples: 1},
			// Single-trade close print: close +101 bps, VWAP +8 bps -> +8 bps (not a depeg day).
			{Day: "2026-02-03", Source: stablePegSourceKraken, Close: f(1.0101), VWAP: f(1.0008), Low: f(0.9996), High: f(1.0101), Samples: 1},
			// Opposite signs: close +40 bps, VWAP -40 bps -> 0 bps (not a depeg day).
			{Day: "2026-04-10", Source: stablePegSourceKraken, Close: f(1.0040), VWAP: f(0.9960), Low: f(0.9950), High: f(1.0045), Samples: 1},
			// Kraken day on 2026-06-05.
			{Day: "2026-06-05", Source: stablePegSourceKraken, Close: f(1.0001), VWAP: f(1.0001), Low: f(0.9998), High: f(1.0003), Samples: 1},
		},
	}
	h := stableClassifyDepeg(in)
	var days2024To2026 []StableDepegDay
	for _, d := range h.Days {
		if d.Day >= "2024-01-01" {
			days2024To2026 = append(days2024To2026, d)
		}
	}
	if len(days2024To2026) != 0 {
		t.Fatalf("EURC 2024-2026 depeg days = %+v, want 0 (quiet)", days2024To2026)
	}
	// Low-volume Bitstamp days (54k and 175k) and DefiLlama days have no value at all.
	for _, c := range h.Closes {
		if c.Day == "2024-02-19" || c.Day == "2024-12-11" || c.Day == "2025-08-01" {
			t.Errorf("unclassified day %s present in Closes: %+v", c.Day, c)
		}
	}
	// Pin Kraken-period count at >= 100 bps as 0.
	krakenGE100 := 0
	for _, c := range h.Closes {
		if c.Source == stablePegSourceKraken && stableBpsAtLeast(c.DevBps, 100) {
			krakenGE100++
		}
	}
	if krakenGE100 != 0 {
		t.Errorf("Kraken days at >= 100 bps = %d, want 0", krakenGE100)
	}
	if len(h.Days) != 1 || h.Days[0].Day != "2023-03-11" || h.Days[0].Source != stablePegSourceBitstamp ||
		h.Days[0].SourceLabel != "Bitstamp close (≥ 200k vol)" || h.Days[0].ThresholdBps != 30 || math.Abs(h.Days[0].DevBps+370) > 1e-6 {
		t.Errorf("EURC all depeg days = %+v, want only 2023-03-11 (−370 bps) on Bitstamp close (≥ 200k vol)", h.Days)
	}
	if h.LastGE100 == nil || h.LastGE100.Day != "2023-03-11" || h.LastGE100.ThresholdBps != 30 || h.LastGE100.SourceLabel != "Bitstamp close (≥ 200k vol)" {
		t.Errorf("EURC last deviation = %+v, want 2023-03-11 at >=30 bps (Bitstamp close (≥ 200k vol))", h.LastGE100)
	}
	for _, c := range h.Closes {
		if c.Day == "2026-06-05" && (c.Source != stablePegSourceKraken || c.DevBps != 1 || c.ThresholdBps != 30) {
			t.Errorf("2026-06-05 close = %+v, want kraken_ohlc +1 bps with 30 bps threshold", c)
		}
		if c.Day == "2026-04-10" && c.DevBps != 0 {
			t.Errorf("2026-04-10 opposite-sign close = %+v, want dev 0", c)
		}
	}

	// −40 bps at 250k volume on Bitstamp -> depeg day.
	withBitstamp40 := in
	withBitstamp40.Candles = append(append([]stablePegDaily{}, in.Candles...), stablePegDaily{
		Day: "2023-05-10", Source: stablePegSourceBitstamp, Close: f(0.9960), Low: f(0.9950), High: f(1.0000), Volume: f(250_000), Samples: 1,
	})
	hb := stableClassifyDepeg(withBitstamp40)
	if len(hb.Days) != 2 || hb.Days[0].Day != "2023-05-10" || hb.Days[0].Source != stablePegSourceBitstamp ||
		hb.Days[0].SourceLabel != "Bitstamp close (≥ 200k vol)" || math.Abs(hb.Days[0].DevBps+40) > 1e-6 {
		t.Fatalf("with −40 bps at 250k Bitstamp candle: days = %+v, want 2023-05-10 (−40 bps) and 2023-03-11 (−370 bps)", hb.Days)
	}

	// A close at +35 bps with a VWAP at +35 bps on Kraken IS a depeg day at 30 bps, and
	// becomes the more recent last deviation ("last >=30 bps (Kraken close & VWAP)").
	withMove := in
	withMove.Kraken = append(append([]stablePegDaily{}, in.Kraken...), stablePegDaily{
		Day: "2026-07-15", Source: stablePegSourceKraken, Close: f(1.0035), VWAP: f(1.0035), Low: f(1.0020), High: f(1.0045), Samples: 1,
	})
	hm := stableClassifyDepeg(withMove)
	if len(hm.Days) != 2 || hm.Days[0].Day != "2026-07-15" || hm.Days[0].Source != stablePegSourceKraken || hm.Days[0].ThresholdBps != 30 {
		t.Fatalf("with +35/+35 Kraken move: days = %+v, want 2026-07-15 (30 bps) and 2023-03-11 (30 bps)", hm.Days)
	}
	d0 := hm.Days[0]
	if math.Abs(d0.CloseDevBps-35) > 1e-6 || d0.VWAPDevBps == nil || math.Abs(*d0.VWAPDevBps-35) > 1e-6 || math.Abs(d0.DevBps-35) > 1e-6 {
		t.Errorf("Kraken depeg day fields = %+v, want close_dev=+35, vwap_dev=+35, dev=+35", d0)
	}
	if hm.LastGE100 == nil || hm.LastGE100.Day != "2026-07-15" || hm.LastGE100.ThresholdBps != 30 || hm.LastGE100.SourceLabel != "Kraken close & VWAP" {
		t.Errorf("last deviation with recent Kraken depeg = %+v, want 2026-07-15 at >=30 bps (Kraken close & VWAP)", hm.LastGE100)
	}
}
