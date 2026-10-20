package main

import (
	"context"
	"encoding/json"
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
	for name, body := range map[string]string{
		"not json":       `<html>`,
		"an object":      `{"prices": []}`,
		"empty":          `[]`,
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
	ohlc := map[string][]byte{
		"usdtusd": readStableFixture(t, "replay/bitstamp_ohlc_usdtusd_2022-05.json"),
		"usdcusd": readStableFixture(t, "replay/bitstamp_ohlc_usdcusd_2023-03.json"),
	}
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
			pair := strings.Trim(strings.TrimPrefix(r.URL.Path, "/bitstamp/api/v2/ohlc/"), "/")
			q := r.URL.Query()
			if q.Get("step") != "86400" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
			limit, _ := strconv.Atoi(q.Get("limit"))
			label := map[string]string{"usdtusd": "USDT/USD", "usdcusd": "USDC/USD", "eurceur": "EURC/EUR"}[pair]
			var doc struct {
				Data struct {
					Pair string            `json:"pair"`
					OHLC []json.RawMessage `json:"ohlc"`
				} `json:"data"`
			}
			if b, ok := ohlc[pair]; ok {
				_ = json.Unmarshal(b, &doc)
			}
			out := []json.RawMessage{}
			for _, c := range doc.Data.OHLC {
				var ts struct {
					Timestamp string `json:"timestamp"`
				}
				_ = json.Unmarshal(c, &ts)
				if v, _ := strconv.ParseInt(ts.Timestamp, 10, 64); v >= start && len(out) < limit {
					out = append(out, c)
				}
			}
			b, _ := json.Marshal(map[string]any{"data": map[string]any{"pair": label, "ohlc": out}})
			w.Write(b)
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
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	p := newStablePegSampler(StablecoinConfig{}.settings(), store, stub.endpoints(), nil, func() time.Time { return clock })
	p.ohlcLimit = 4
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
	// Bitstamp daily candles, paged 4 at a time: the intraday lows.
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
	if usdt, _ := store.stablePegDailySeries(ctx, "usdt", stablePegSourceBitstamp, "2000-01-01"); len(usdt) != 8 {
		t.Errorf("usdt candles = %d, want 8", len(usdt))
	}
	qs := stub.queries("/bitstamp/api/v2/ohlc/usdcusd/")
	if len(qs) != 3 || !strings.Contains(qs[0], "start="+strconv.FormatInt(stableOHLCFrom.Unix(), 10)) || !strings.Contains(qs[0], "limit=4") {
		t.Errorf("usdcusd requests = %v (want 3 pages from %v)", qs, stableOHLCFrom)
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
	if len(qs) < 4 || !strings.Contains(qs[3], "start="+strconv.FormatInt(resume, 10)) {
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
