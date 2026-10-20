package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func readStableFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/stablecoin/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func approxPtr(t *testing.T, label string, got *float64, want, tol float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = null, want %.6f", label, want)
		return
	}
	if math.Abs(*got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f (±%g)", label, *got, want, tol)
	}
}

// §5.1 worked example (Appendix E.1): 493.04M ÷ 1.121427 = 439.66M EURC.
func TestLlamaNativeFromUSD(t *testing.T) {
	got := llamaNativeFromUSD(btcFloatPtr(493041373), btcFloatPtr(1.121427))
	approxPtr(t, "worked example", got, 439655343.59, 0.01)
	if got != nil && math.Round(*got/1e4)/1e2 != 439.66 {
		t.Errorf("worked example = %.2fM, want 439.66M", *got/1e6)
	}
	if v := llamaNativeFromUSD(btcFloatPtr(493041373), nil); v != nil {
		t.Errorf("missing price -> %v, want null", *v)
	}
	if v := llamaNativeFromUSD(btcFloatPtr(493041373), btcFloatPtr(0)); v != nil {
		t.Errorf("zero price -> %v, want null", *v)
	}
	if v := llamaNativeFromUSD(nil, btcFloatPtr(1.1)); v != nil {
		t.Errorf("missing value -> %v, want null", *v)
	}
}

func TestParseLlamaStablecoinsFixture(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 6, 0, 0, time.UTC)
	snap, err := parseLlamaStablecoins(readStableFixture(t, "defillama_stablecoins.json"), at)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !snap.FetchedAt.Equal(at) {
		t.Errorf("FetchedAt = %v", snap.FetchedAt)
	}
	usdt, ok := snap.core("usdt")
	if !ok || usdt.Symbol != "USDT" || usdt.PegType != "peggedUSD" {
		t.Fatalf("usdt = %+v, %v", usdt, ok)
	}
	approxPtr(t, "usdt price", usdt.PriceUSD, fxLlamaUSDTPrice, 1e-12)
	approxPtr(t, "usdt circulating_usd", usdt.CirculatingUSD, fxLlamaUSDTCirculatingUSD, 0.01)
	approxPtr(t, "usdt circulating_native", usdt.CirculatingNative, fxLlamaUSDTCirculatingNative, 0.5)
	// List values are native × the same-response price for USD pegs too: the
	// normalized prevDay equals the charts' native total for 2026-10-18.
	chart, err := parseLlamaChart(readStableFixture(t, "defillama_charts_usdt.json"), "peggedUSD")
	if err != nil {
		t.Fatal(err)
	}
	var chart1018 *float64
	for _, r := range chart {
		if r.Day == "2026-10-18" {
			chart1018 = r.CirculatingNative
		}
	}
	if chart1018 == nil {
		t.Fatal("charts fixture lacks 2026-10-18")
	}
	approxPtr(t, "usdt prev_day_native vs chart native 2026-10-18", usdt.PrevDayNative, *chart1018, 1)
	approxPtr(t, "usdt prev_week_native", usdt.PrevWeekNative, fxLlamaUSDTPrevWeekNative, 0.5)
	approxPtr(t, "usdt prev_month_native", usdt.PrevMonthNative, fxLlamaUSDTPrevMonthNative, 0.5)

	eurc, ok := snap.core("eurc")
	if !ok || eurc.Symbol != "EURC" || eurc.PegType != "peggedEUR" || eurc.LlamaID != "50" {
		t.Fatalf("eurc = %+v, %v", eurc, ok)
	}
	approxPtr(t, "eurc circulating_usd", eurc.CirculatingUSD, fxLlamaEURCCirculatingUSD, 0.01)
	approxPtr(t, "eurc circulating_native", eurc.CirculatingNative, fxLlamaEURCCirculatingNative, 0.5)
	approxPtr(t, "eurc prev_day_native", eurc.PrevDayNative, fxLlamaEURCPrevDayNative, 0.5)
	var eth *llamaChain
	for i := range eurc.Chains {
		if eurc.Chains[i].Chain == "Ethereum" {
			eth = &eurc.Chains[i]
		}
	}
	if eth == nil {
		t.Fatal("eurc has no Ethereum chain row")
	}
	approxPtr(t, "eurc Ethereum current_usd", eth.CurrentUSD, fxLlamaEURCEthereumUSD, 0.01)
	approxPtr(t, "eurc Ethereum current_native", eth.CurrentNative, fxLlamaEURCEthereumNative, 0.01)
	if eurc.Chains[0].Chain != "Ethereum" {
		t.Errorf("chains not sorted by current supply: first = %s", eurc.Chains[0].Chain)
	}

	usdc, ok := snap.core("usdc")
	if !ok || usdc.LlamaID != "2" {
		t.Fatalf("usdc = %+v, %v", usdc, ok)
	}
	// A null price (EURT) keeps the USD value and makes native amounts unknown.
	eurt, ok := snap.byID("49")
	if !ok || eurt.PriceUSD != nil || eurt.CirculatingNative != nil || eurt.CirculatingUSD == nil {
		t.Errorf("eurt (null price) = %+v", eurt)
	}
	// A non-USD/EUR peg is parsed under its own key; the watchlist filter is applied later.
	volt, ok := snap.byID("39")
	if !ok || volt.PegType != "peggedVAR" || volt.CirculatingUSD == nil {
		t.Errorf("volt = %+v", volt)
	}
}

func TestParseLlamaStablecoinsShape(t *testing.T) {
	at := time.Now()
	for name, body := range map[string]string{
		"not json":         `<html>`,
		"no peggedAssets":  `{}`,
		"empty list":       `{"peggedAssets": []}`,
		"core id drifted":  `{"peggedAssets": [{"id": "1", "symbol": "USDX", "pegType": "peggedUSD", "price": 1, "circulating": {"peggedUSD": 1}}, {"id": "2", "symbol": "USDC", "pegType": "peggedUSD"}, {"id": "50", "symbol": "EURC", "pegType": "peggedEUR"}]}`,
		"core id missing":  `{"peggedAssets": [{"id": "1", "symbol": "USDT", "pegType": "peggedUSD"}, {"id": "2", "symbol": "USDC", "pegType": "peggedUSD"}]}`,
		"core peg drifted": `{"peggedAssets": [{"id": "1", "symbol": "USDT", "pegType": "peggedUSD"}, {"id": "2", "symbol": "USDC", "pegType": "peggedUSD"}, {"id": "50", "symbol": "EURC", "pegType": "peggedUSD"}]}`,
	} {
		if _, err := parseLlamaStablecoins([]byte(body), at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseLlamaChartFixtures(t *testing.T) {
	rows, err := parseLlamaChart(readStableFixture(t, "defillama_charts_eurc.json"), "peggedEUR")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].Day != "2026-10-15" || rows[4].Day != "2026-10-19" {
		t.Fatalf("rows = %+v", rows)
	}
	// Chart totalCirculating is native already; totalCirculatingUSD is USD.
	approxPtr(t, "eurc 2026-10-19 native", rows[4].CirculatingNative, fxChartEURCLastNative, 1e-6)
	approxPtr(t, "eurc 2026-10-19 usd", rows[4].CirculatingUSD, fxChartEURCLastUSD, 1e-6)
	approxPtr(t, "eurc 2026-10-19 unreleased", rows[4].UnreleasedNative, fxChartEURCLastUnreleased, 1e-6)

	// Numeric dates are accepted as well as strings.
	num := `[{"date": 1792368000, "totalCirculating": {"peggedUSD": 5}, "totalCirculatingUSD": {"peggedUSD": 4.9}}]`
	if r, err := parseLlamaChart([]byte(num), "peggedUSD"); err != nil || len(r) != 1 || r[0].Day != "2026-10-19" {
		t.Errorf("numeric date: %+v, %v", r, err)
	}
	for name, body := range map[string]string{
		"not an array": `{"date": "1"}`,
		"empty":        `[]`,
		"wrong peg":    string(readStableFixture(t, "defillama_charts_usdt.json")),
		"bad date":     `[{"date": "yesterday", "totalCirculating": {"peggedEUR": 1}}]`,
	} {
		if _, err := parseLlamaChart([]byte(body), "peggedEUR"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStableFetchErrorCodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/429", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	mux.HandleFunc("/503", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/451", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnavailableForLegalReasons) })
	mux.HandleFunc("/403", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat("x", 2048))) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	mux.HandleFunc("/shape/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx := context.Background()
	cases := map[string]string{
		"/429": "rate_limited", "/503": "5xx", "/451": "geo_blocked", "/403": "blocked",
		"/404": "not_found", "/redirect": "bad_response", "/big": "bad_response", "/ok": "",
	}
	for path, want := range cases {
		if _, code := stableFetch(ctx, srv.URL+path, time.Second, 1024); code != want {
			t.Errorf("%s: code %q, want %q", path, code, want)
		}
	}
	if _, code := stableFetch(ctx, srv.URL+"/slow", 50*time.Millisecond, 1024); code != "timeout" {
		t.Errorf("slow: code %q, want timeout", code)
	}

	// Fetch wrappers map a parse failure to bad_response.
	ep := stableEndpoints{LlamaBase: srv.URL + "/shape"}
	if _, code := fetchLlamaStablecoins(ctx, ep, time.Now()); code != "bad_response" {
		t.Errorf("fetchLlamaStablecoins on a wrong shape: code %q, want bad_response", code)
	}
}

func TestFetchLlamaFromStub(t *testing.T) {
	list := readStableFixture(t, "defillama_stablecoins.json")
	chart := readStableFixture(t, "defillama_charts_eurc.json")
	var (
		mu    sync.Mutex
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/stablecoins":
			w.Write(list)
		case "/stablecoincharts/all":
			w.Write(chart)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ep := stableEndpoints{LlamaBase: srv.URL}
	snap, code := fetchLlamaStablecoins(context.Background(), ep, time.Now())
	if code != "" || len(snap.Assets) == 0 {
		t.Fatalf("list: code %q, %d assets", code, len(snap.Assets))
	}
	rows, code := fetchLlamaChart(context.Background(), ep, "50", "peggedEUR")
	if code != "" || len(rows) != 5 {
		t.Fatalf("chart: code %q, %d rows", code, len(rows))
	}
	want := []string{"/stablecoins?includePrices=true", "/stablecoincharts/all?stablecoin=50"}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("requested %v, want %v", paths, want)
	}
}

func TestStableLastGood(t *testing.T) {
	var lg stableLastGood[int]
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	if r := lg.resolve(0, "timeout", t0); r.Has || r.Stale || r.Code != "timeout" {
		t.Errorf("failure before any success = %+v, want no value", r)
	}
	if r := lg.resolve(7, "", t0); !r.Has || r.Stale || r.Value != 7 || r.Code != "" || !r.AsOf.Equal(t0) {
		t.Errorf("success = %+v", r)
	}
	t1 := t0.Add(10 * time.Minute)
	if r := lg.resolve(0, "5xx", t1); !r.Has || !r.Stale || r.Value != 7 || r.Code != "5xx" || !r.AsOf.Equal(t0) {
		t.Errorf("failure after success = %+v, want stale last-good 7 as of t0", r)
	}
}
