package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stableVenueStub serves the venue fixtures under the paths the adapters use.
type stableVenueStub struct {
	mu       sync.Mutex
	hits     map[string]int
	override map[string]int  // path → HTTP status to answer instead
	listFor  map[string]bool // Bitstamp pairs answered with the full ticker list
	srv      *httptest.Server
}

func (s *stableVenueStub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *stableVenueStub) setStatus(path string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override[path] = code
}

func (s *stableVenueStub) serveList(pair string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listFor[pair] = true
}

func (s *stableVenueStub) endpoints() stableEndpoints {
	return stableEndpoints{CoinbaseBase: s.srv.URL + "/coinbase", KrakenBase: s.srv.URL + "/kraken",
		BitstampBase: s.srv.URL + "/bitstamp", OKXBase: s.srv.URL + "/okx", BinanceBase: s.srv.URL + "/binance"}
}

// recorded is one fixture entry: {"http_status": n, "body": ...}.
type recorded struct {
	Status int             `json:"http_status"`
	Body   json.RawMessage `json:"body"`
}

func newStableVenueStub(t *testing.T) *stableVenueStub {
	t.Helper()
	var coinbase, bitstamp map[string]recorded
	var okx, binance recorded
	for name, dst := range map[string]any{"coinbase_tickers.json": &coinbase, "bitstamp_tickers.json": &bitstamp,
		"okx_ticker.json": &okx, "binance_ticker24h.json": &binance} {
		if err := json.Unmarshal(readStableFixture(t, name), dst); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	kraken := readStableFixture(t, "kraken_ticker.json")
	st := &stableVenueStub{hits: map[string]int{}, override: map[string]int{}, listFor: map[string]bool{}}
	write := func(w http.ResponseWriter, r recorded) {
		if r.Status != http.StatusOK {
			w.WriteHeader(r.Status)
		}
		w.Write(r.Body)
	}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.hits[r.URL.Path]++
		code := st.override[r.URL.Path]
		lists := st.listFor
		st.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/coinbase/products/") && strings.HasSuffix(p, "/ticker"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/coinbase/products/"), "/ticker")
			if rec, ok := coinbase[id]; ok {
				write(w, rec)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case p == "/kraken/0/public/Ticker":
			if r.URL.Query().Get("pair") != "USDTUSD,USDCUSD,EURCEUR,EURCUSD,EURUSD" {
				w.Write([]byte(`{"error":["EQuery:Unknown asset pair"]}`))
				return
			}
			w.Write(kraken)
		case strings.HasPrefix(p, "/bitstamp/api/v2/ticker/"):
			pair := strings.Trim(strings.TrimPrefix(p, "/bitstamp/api/v2/ticker/"), "/")
			st.mu.Lock()
			asList := lists[pair]
			st.mu.Unlock()
			if rec, ok := bitstamp[pair]; ok && pair != "eurcusd_unknown_pair" && !asList {
				write(w, rec)
				return
			}
			write(w, bitstamp["eurcusd_unknown_pair"]) // an unknown pair answers 200 with the full list
		case p == "/okx/api/v5/market/ticker" && r.URL.Query().Get("instId") == "USDC-USDT":
			write(w, okx)
		case p == "/binance/api/v3/ticker/24hr" && r.URL.Query().Get("symbol") == "USDCUSDT":
			write(w, binance)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func fixtureBody(t *testing.T, file, key string) []byte {
	t.Helper()
	var m map[string]recorded
	if err := json.Unmarshal(readStableFixture(t, file), &m); err != nil {
		t.Fatal(err)
	}
	return m[key].Body
}

func TestParseCoinbaseTicker(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	q, err := parseCoinbaseTicker(fixtureBody(t, "coinbase_tickers.json", "USDT-USD"), "USDT-USD", at)
	if err != nil {
		t.Fatal(err)
	}
	if q.Venue != "coinbase" || q.Pair != "USDT-USD" || q.Base != "USDT" || q.Quote != "USD" || q.Bid != fxCoinbaseUSDTUSDBid ||
		q.Ask != fxCoinbaseUSDTUSDAsk || q.VolBase == nil || *q.VolBase != fxCoinbaseUSDTUSDVol || q.Last == nil {
		t.Errorf("USDT-USD = %+v", q)
	}
	if want := time.Date(2026, 10, 19, 13, 20, 30, 596866968, time.UTC); !q.TS.Equal(want) || !q.FetchedAt.Equal(at) {
		t.Errorf("times = %v / %v", q.TS, q.FetchedAt)
	}
	good := string(fixtureBody(t, "coinbase_tickers.json", "EURC-USDC"))
	for name, body := range map[string]string{
		"not json":      `<html>`,
		"a list":        `[]`,
		"missing bid":   strings.Replace(good, `"bid"`, `"bid_x"`, 1),
		"bad number":    strings.Replace(good, `"bid": "1.0851"`, `"bid": "n/a"`, 1),
		"crossed book":  strings.Replace(good, `"bid": "1.0851"`, `"bid": "1.0900"`, 1),
		"zero ask":      strings.Replace(good, `"ask": "1.0852"`, `"ask": "0"`, 1),
		"bad time":      strings.Replace(good, `"time": "2026-10-19T13:20:32.596866968Z"`, `"time": "soon"`, 1),
		"error message": `{"message": "NotFound"}`,
	} {
		if _, err := parseCoinbaseTicker([]byte(body), "EURC-USDC", at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseCoinbaseTicker([]byte(good), "XYZ-ABC", at); err == nil {
		t.Error("unknown product accepted")
	}
}

func TestParseKrakenTicker(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	qs, err := parseKrakenTicker(readStableFixture(t, "kraken_ticker.json"), at)
	if err != nil {
		t.Fatal(err)
	}
	// Mapped by the returned (renamed) keys, not the requested pairs.
	want := map[string][2]float64{"USDTZUSD": {fxKrakenUSDTZUSDBid, fxKrakenUSDTZUSDAsk}, "USDCUSD": {fxKrakenUSDCUSDBid, fxKrakenUSDCUSDAsk},
		"EURCEUR": {fxKrakenEURCEURBid, fxKrakenEURCEURAsk}, "EURCUSD": {fxKrakenEURCUSDBid, fxKrakenEURCUSDAsk},
		"ZEURZUSD": {fxKrakenZEURZUSDBid, fxKrakenZEURZUSDAsk}}
	if len(qs) != len(want) {
		t.Fatalf("quotes = %+v", qs)
	}
	for _, q := range qs {
		w, ok := want[q.Pair]
		if !ok || q.Code != "" || q.Bid != w[0] || q.Ask != w[1] || q.Venue != "kraken" || q.VolBase == nil || !q.TS.IsZero() || !q.FetchedAt.Equal(at) {
			t.Errorf("%s = %+v", q.Pair, q)
		}
	}
	if q := krakenByPair(qs, "ZEURZUSD"); q.Base != "EUR" || q.Quote != "USD" || *q.VolBase != fxKrakenZEURZUSDVol {
		t.Errorf("ZEURZUSD = %+v", q)
	}
	// A pair missing from the result is reported, not dropped.
	body := strings.Replace(string(readStableFixture(t, "kraken_ticker.json")), `"EURCUSD"`, `"EURCUSDX"`, 1)
	qs, err = parseKrakenTicker([]byte(body), at)
	if err != nil {
		t.Fatal(err)
	}
	if q := krakenByPair(qs, "EURCUSD"); q.Code != "bad_response" {
		t.Errorf("missing EURCUSD = %+v", q)
	}
	for name, body := range map[string]string{
		"not json":     `<html>`,
		"error":        `{"error": ["EQuery:Unknown asset pair"], "result": {}}`,
		"no result":    `{"error": []}`,
		"empty result": `{"error": [], "result": {}}`,
	} {
		if _, err := parseKrakenTicker([]byte(body), at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A malformed pair entry is that pair's bad_response.
	fixture := string(readStableFixture(t, "kraken_ticker.json"))
	bad := strings.Replace(fixture, `"0.99987000"`, `"oops"`, 1) // USDCUSD's bid
	if bad == fixture {
		t.Fatal("fixture edit did not apply (test bug)")
	}
	qs, _ = parseKrakenTicker([]byte(bad), at)
	if q := krakenByPair(qs, "USDCUSD"); q.Code != "bad_response" {
		t.Errorf("malformed USDCUSD = %+v", q)
	}
}

func krakenByPair(qs []stableVenueQuote, pair string) stableVenueQuote {
	for _, q := range qs {
		if q.Pair == pair {
			return q
		}
	}
	return stableVenueQuote{}
}

func TestParseBitstampTicker(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	q, err := parseBitstampTicker(fixtureBody(t, "bitstamp_tickers.json", "eurceur"), "eurceur", at)
	if err != nil {
		t.Fatal(err)
	}
	if q.Base != "EURC" || q.Quote != "EUR" || q.Bid != fxBitstampEURCEURBid || q.Ask != fxBitstampEURCEURAsk || *q.VolBase != fxBitstampEURCEURVol ||
		!q.TS.Equal(time.Unix(1792416049, 0)) {
		t.Errorf("eurceur = %+v", q)
	}
	// An unknown pair answers HTTP 200 with the full ticker list: bad_response.
	if _, err := parseBitstampTicker(fixtureBody(t, "bitstamp_tickers.json", "eurcusd_unknown_pair"), "eurceur", at); err == nil {
		t.Error("ticker list accepted as a pair ticker")
	}
	good := string(fixtureBody(t, "bitstamp_tickers.json", "usdtusd"))
	for name, body := range map[string]string{
		"missing ask":   strings.Replace(good, `"ask"`, `"ask_x"`, 1),
		"bad timestamp": strings.Replace(good, `"timestamp": "1792416049"`, `"timestamp": "x"`, 1),
		"empty object":  `{}`,
	} {
		if _, err := parseBitstampTicker([]byte(body), "usdtusd", at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseOKXAndBinanceTickers(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	var okx, bin recorded
	_ = json.Unmarshal(readStableFixture(t, "okx_ticker.json"), &okx)
	_ = json.Unmarshal(readStableFixture(t, "binance_ticker24h.json"), &bin)
	q, err := parseOKXTicker(okx.Body, "USDC-USDT", at)
	if err != nil {
		t.Fatal(err)
	}
	if q.Base != "USDC" || q.Quote != "USDT" || q.Bid != fxOKXUSDCUSDTBid || q.Ask != fxOKXUSDCUSDTAsk || q.VolQuote == nil ||
		*q.VolQuote != fxOKXUSDCUSDTVolQuote || !q.TS.Equal(time.UnixMilli(1792416051080)) {
		t.Errorf("okx = %+v", q)
	}
	for name, body := range map[string]string{
		"error code":    strings.Replace(string(okx.Body), `"code": "0"`, `"code": "51001"`, 1),
		"other inst":    strings.Replace(string(okx.Body), `"instId": "USDC-USDT"`, `"instId": "BTC-USDT"`, 1),
		"no data":       `{"code": "0", "data": [], "msg": ""}`,
		"crossed":       strings.Replace(string(okx.Body), `"bidPx": "1.00062"`, `"bidPx": "1.1"`, 1),
		"missing askPx": strings.Replace(string(okx.Body), `"askPx"`, `"askPx_x"`, 1),
	} {
		if _, err := parseOKXTicker([]byte(body), "USDC-USDT", at); err == nil {
			t.Errorf("okx %s: accepted", name)
		}
	}
	q, err = parseBinanceTicker(bin.Body, "USDCUSDT", at)
	if err != nil {
		t.Fatal(err)
	}
	if q.Base != "USDC" || q.Quote != "USDT" || q.Bid != fxBinanceUSDCUSDTBid || q.Ask != fxBinanceUSDCUSDTAsk || q.VolQuote == nil ||
		*q.VolQuote != fxBinanceUSDCUSDTVolQuote || !q.TS.Equal(time.UnixMilli(1792416052284)) {
		t.Errorf("binance = %+v", q)
	}
	for name, body := range map[string]string{
		"other symbol": strings.Replace(string(bin.Body), `"symbol": "USDCUSDT"`, `"symbol": "BTCUSDT"`, 1),
		"error":        `{"code": -1121, "msg": "Invalid symbol."}`,
		"zero bid":     strings.Replace(string(bin.Body), `"bidPrice": "1.00062000"`, `"bidPrice": "0.00000000"`, 1),
	} {
		if _, err := parseBinanceTicker([]byte(body), "USDCUSDT", at); err == nil {
			t.Errorf("binance %s: accepted", name)
		}
	}
}

func TestFetchStableVenuesFromStub(t *testing.T) {
	stub := newStableVenueStub(t)
	ep := stub.endpoints()
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	vf := newStableVenueFetcher(ep, func() time.Time { return now })
	qs := vf.fetchAll(ctx, map[string]bool{"coinbase": true, "kraken": true, "bitstamp": true, "okx": true, "binance": true})
	byKey := map[string]stableVenueQuote{}
	for _, q := range qs {
		byKey[q.Venue+"|"+q.Pair] = q
	}
	for _, p := range stableVenuePairs {
		q, ok := byKey[p.Venue+"|"+p.Pair]
		if !ok || q.Code != "" || q.Bid <= 0 || q.Base != p.Base || q.Quote != p.Quote {
			t.Errorf("%s %s = %+v", p.Venue, p.Pair, q)
		}
	}
	if len(qs) != len(stableVenuePairs) {
		t.Errorf("%d quotes, want %d", len(qs), len(stableVenuePairs))
	}
	if stub.count("/kraken/0/public/Ticker") != 1 {
		t.Errorf("kraken calls = %d, want one batched call", stub.count("/kraken/0/public/Ticker"))
	}
	// Disabled providers are never called and report disabled quotes.
	stub2 := newStableVenueStub(t)
	vf2 := newStableVenueFetcher(stub2.endpoints(), func() time.Time { return now })
	for _, q := range vf2.fetchAll(ctx, map[string]bool{"kraken": true}) {
		if q.Venue != "kraken" && q.Code != "disabled" {
			t.Errorf("%s %s with the provider off = %+v", q.Venue, q.Pair, q)
		}
	}
	if stub2.count("/okx/api/v5/market/ticker") != 0 || stub2.count("/coinbase/products/USDT-USD/ticker") != 0 {
		t.Error("a disabled provider was called")
	}

	// Coinbase: a 404 on a product means it is not listed (disabled), not an outage.
	if q := vf.fetchCoinbase(ctx, "USDC-USD"); q.Code != "disabled" {
		t.Errorf("coinbase USDC-USD = %+v, want disabled", q)
	}
	// Bitstamp: a pair answered with the full ticker list (what an unknown
	// pair gets) is bad_response.
	stub.serveList("eurceur")
	if q := vf.fetchBitstamp(ctx, "eurceur"); q.Code != "bad_response" {
		t.Errorf("bitstamp list answer = %+v, want bad_response", q)
	}
	// Binance: HTTP 451 or 403 pauses the provider for 24 h.
	for _, status := range []int{http.StatusUnavailableForLegalReasons, http.StatusForbidden} {
		stub3 := newStableVenueStub(t)
		clock := now
		vf3 := newStableVenueFetcher(stub3.endpoints(), func() time.Time { return clock })
		stub3.setStatus("/binance/api/v3/ticker/24hr", status)
		if q := vf3.fetchBinance(ctx, "USDCUSDT"); q.Code != stableMapStatus(status) {
			t.Errorf("binance %d = %+v", status, q)
		}
		stub3.setStatus("/binance/api/v3/ticker/24hr", 0)
		clock = clock.Add(23 * time.Hour)
		if q := vf3.fetchBinance(ctx, "USDCUSDT"); q.Code != "paused" || stub3.count("/binance/api/v3/ticker/24hr") != 1 {
			t.Errorf("binance within 24 h of %d = %+v (calls %d), want paused without a call", status, q, stub3.count("/binance/api/v3/ticker/24hr"))
		}
		if until := vf3.binancePausedUntil(); !until.Equal(now.Add(24 * time.Hour)) {
			t.Errorf("paused until %v, want %v", until, now.Add(24*time.Hour))
		}
		clock = now.Add(24*time.Hour + time.Second)
		if q := vf3.fetchBinance(ctx, "USDCUSDT"); q.Code != "" || stub3.count("/binance/api/v3/ticker/24hr") != 2 {
			t.Errorf("binance after the pause = %+v", q)
		}
	}
	// Other failures map to the shared fetch codes.
	stub.setStatus("/okx/api/v5/market/ticker", http.StatusTooManyRequests)
	if q := vf.fetchOKX(ctx, "USDC-USDT"); q.Code != "rate_limited" {
		t.Errorf("okx 429 = %+v", q)
	}
	stub.setStatus("/coinbase/products/USDT-USD/ticker", http.StatusBadGateway)
	if q := vf.fetchCoinbase(ctx, "USDT-USD"); q.Code != "5xx" {
		t.Errorf("coinbase 502 = %+v", q)
	}
}
