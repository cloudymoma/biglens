package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestBinanceAutoGeoBlocked(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	raw451, err := os.ReadFile("testdata/bitcoin/binance_451.json")
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnavailableForLegalReasons) // 451
		_, _ = w.Write(raw451)
	}))
	defer srv.Close()

	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	adapter := newBinanceAdapter(store, func() time.Time { return now })
	adapter.fapiBaseURL = srv.URL
	adapter.dapiBaseURL = srv.URL

	// First call hits HTTP 451 -> geo_blocked and records binance_geo_blocked_until = now + 24h.
	_, code := adapter.Fetch(ctx)
	if code != "geo_blocked" {
		t.Fatalf("first Fetch code = %q, want geo_blocked", code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP calls after first Fetch = %d, want 1", got)
	}
	val, ok, err := store.getBtcKV(ctx, btcBinanceGeoBlockedKey)
	if err != nil || !ok {
		t.Fatalf("binance_geo_blocked_until not stored in btc_kv: ok=%v err=%v", ok, err)
	}
	wantUntil := now.Add(24 * time.Hour).Format(time.RFC3339)
	if val != wantUntil {
		t.Errorf("binance_geo_blocked_until = %q, want %q", val, wantUntil)
	}

	// Second call within 24h makes zero HTTP requests, even on a fresh adapter instance sharing the store.
	now = now.Add(12 * time.Hour)
	adapter2 := newBinanceAdapter(store, func() time.Time { return now })
	adapter2.fapiBaseURL = srv.URL
	adapter2.dapiBaseURL = srv.URL
	_, code2 := adapter2.Fetch(ctx)
	if code2 != "geo_blocked" {
		t.Fatalf("second Fetch within 24h code = %q, want geo_blocked", code2)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP calls after second Fetch within 24h = %d, want 1 (no new HTTP call)", got)
	}
}

func TestBinanceUSDMAndCoinM(t *testing.T) {
	ctx := context.Background()
	fTicker, err := os.ReadFile("testdata/bitcoin/binance_fapi_ticker24h.json")
	if err != nil {
		t.Fatal(err)
	}
	fOI, err := os.ReadFile("testdata/bitcoin/binance_fapi_oi.json")
	if err != nil {
		t.Fatal(err)
	}
	fPrem, err := os.ReadFile("testdata/bitcoin/binance_fapi_premium.json")
	if err != nil {
		t.Fatal(err)
	}
	dTicker, err := os.ReadFile("testdata/bitcoin/binance_dapi_ticker24h.json")
	if err != nil {
		t.Fatal(err)
	}
	dOI, err := os.ReadFile("testdata/bitcoin/binance_dapi_oi.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/ticker/24hr":
			_, _ = w.Write(fTicker)
		case "/fapi/v1/openInterest":
			_, _ = w.Write(fOI)
		case "/fapi/v1/premiumIndex":
			_, _ = w.Write(fPrem)
		case "/dapi/v1/ticker/24hr":
			_, _ = w.Write(dTicker)
		case "/dapi/v1/openInterest":
			_, _ = w.Write(dOI)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	adapter := newBinanceAdapter(nil, time.Now)
	adapter.fapiBaseURL = srv.URL
	adapter.dapiBaseURL = srv.URL

	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	if d.USDMVol24hUSD == nil || math.Abs(*d.USDMVol24hUSD-10966095175.28) > 1e-2 {
		t.Errorf("USDMVol24hUSD = %v, want 10966095175.28 (~$10.97 B)", d.USDMVol24hUSD)
	}
	if d.CoinMOIUSD == nil || math.Abs(*d.CoinMOIUSD-1343862400.0) > 1e-2 {
		t.Errorf("CoinMOIUSD = %v, want 1343862400 ($1.344 B)", d.CoinMOIUSD)
	}
	if d.FundingAPRPct == nil || math.Abs(*d.FundingAPRPct-5.964465) > 0.01 {
		t.Errorf("FundingAPRPct = %v, want ~5.96%%", d.FundingAPRPct)
	}
}

func TestOKXSwap(t *testing.T) {
	ctx := context.Background()
	tRaw, err := os.ReadFile("testdata/bitcoin/okx_swap_ticker.json")
	if err != nil {
		t.Fatal(err)
	}
	oiRaw, err := os.ReadFile("testdata/bitcoin/okx_swap_oi.json")
	if err != nil {
		t.Fatal(err)
	}
	fRaw, err := os.ReadFile("testdata/bitcoin/okx_swap_funding.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/market/ticker":
			_, _ = w.Write(tRaw)
		case "/api/v5/public/open-interest":
			_, _ = w.Write(oiRaw)
		case "/api/v5/public/funding-rate":
			_, _ = w.Write(fRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	adapter := newOKXAdapter()
	adapter.baseURL = srv.URL
	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	wantVol := 69758.85 * 81484.0
	if d.Deriv24hUSD == nil || math.Abs(*d.Deriv24hUSD-wantVol) > 1.0 {
		t.Errorf("Deriv24hUSD = %v, want %f (69,758.85 BTC * 81,484)", d.Deriv24hUSD, wantVol)
	}
	if d.OIUSD == nil || math.Abs(*d.OIUSD-3463070000.0) > 1.0 {
		t.Errorf("OIUSD = %v, want 3463070000.0", d.OIUSD)
	}
	if d.IntervalHours != 8.0 {
		t.Errorf("IntervalHours = %v, want 8.0", d.IntervalHours)
	}
}

func TestCoinbaseINTXAndCDE(t *testing.T) {
	ctx := context.Background()
	intxRaw, err := os.ReadFile("testdata/bitcoin/coinbase_intx_btc_perp.json")
	if err != nil {
		t.Fatal(err)
	}
	cdeRaw, err := os.ReadFile("testdata/bitcoin/coinbase_cde_bip.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/intx":
			_, _ = w.Write(intxRaw)
		case "/cde":
			_, _ = w.Write(cdeRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	adapter := newCoinbaseAdapter()
	adapter.intxURL = srv.URL + "/intx"
	adapter.cdeURL = srv.URL + "/cde"

	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	if d.CDEOIBTC == nil || math.Abs(*d.CDEOIBTC-1737.43) > 1e-6 {
		t.Errorf("CDEOIBTC = %v, want 1737.43 (173,743 * 0.01)", d.CDEOIBTC)
	}
	if d.FundingAPRPct == nil || math.Abs(*d.FundingAPRPct-8.76) > 1e-6 {
		t.Errorf("FundingAPRPct = %v, want 8.76%% (hourly 0.00001 * 8760 * 100)", d.FundingAPRPct)
	}
	if d.INTXNotional24hUSD != nil {
		t.Errorf("INTXNotional24hUSD = %v, want nil when notional_24hr is 0 (never an average)", *d.INTXNotional24hUSD)
	}
	if d.Deriv24hUSD != nil {
		t.Errorf("Deriv24hUSD = %v, want nil when both notional_24hr and CDE volume_24h are 0", *d.Deriv24hUSD)
	}
}

func TestKrakenPerp(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/bitcoin/kraken_futures_tickers.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	adapter := newKrakenAdapter(func() time.Time { return now })
	adapter.tickersURL = srv.URL

	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	if d.FundingAPRPct == nil || math.Abs(*d.FundingAPRPct-11.22) > 0.01 {
		t.Errorf("FundingAPRPct = %v, want 11.22%%", d.FundingAPRPct)
	}
	if len(d.BasisPoints) != 1 || d.BasisPoints[0].Instrument != "FF_XBTUSD_261225" {
		t.Errorf("BasisPoints = %+v, want 1 point for FF_XBTUSD_261225", d.BasisPoints)
	}
}

func TestHyperliquid(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/bitcoin/hyperliquid_meta_and_ctx.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	adapter := newHyperliquidAdapter()
	adapter.infoURL = srv.URL

	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	wantOIUSD := 41064.00828 * 81484.0
	if d.OIUSD == nil || math.Abs(*d.OIUSD-wantOIUSD) > 1.0 {
		t.Errorf("OIUSD = %v, want %f (~$3.346 B)", d.OIUSD, wantOIUSD)
	}
	if d.Deriv24hUSD == nil || math.Abs(*d.Deriv24hUSD-2840125000.0) > 1.0 {
		t.Errorf("Deriv24hUSD = %v, want 2840125000.0 ($2.84 B)", d.Deriv24hUSD)
	}
	if d.FundingAPRPct == nil || math.Abs(*d.FundingAPRPct-10.95) > 1e-6 {
		t.Errorf("FundingAPRPct = %v, want 10.95%% (1.25e-5/h * 8760 * 100)", d.FundingAPRPct)
	}
}

func TestDeribitPerpAndExpiry(t *testing.T) {
	exp, ok := parseDeribitExpiry("BTC-25DEC26")
	if !ok {
		t.Fatal("parseDeribitExpiry(BTC-25DEC26) returned ok=false")
	}
	wantExp := time.Date(2026, 12, 25, 8, 0, 0, 0, time.UTC)
	if !exp.Equal(wantExp) {
		t.Errorf("parseDeribitExpiry(BTC-25DEC26) = %s, want %s", exp.Format(time.RFC3339), wantExp.Format(time.RFC3339))
	}

	ctx := context.Background()
	perpRaw, err := os.ReadFile("testdata/bitcoin/deribit_perp_ticker.json")
	if err != nil {
		t.Fatal(err)
	}
	futRaw, err := os.ReadFile("testdata/bitcoin/deribit_futures_summary.json")
	if err != nil {
		t.Fatal(err)
	}
	dvolRaw, err := os.ReadFile("testdata/bitcoin/deribit_dvol.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/public/ticker":
			_, _ = w.Write(perpRaw)
		case "/public/get_book_summary_by_currency":
			_, _ = w.Write(futRaw)
		case "/public/get_volatility_index_data":
			_, _ = w.Write(dvolRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	adapter := newDeribitAdapter(func() time.Time { return now })
	adapter.baseURL = srv.URL

	d, code := adapter.Fetch(ctx)
	if code != "" {
		t.Fatalf("Fetch code = %q, want empty", code)
	}
	if len(d.BasisPoints) != 3 {
		t.Fatalf("BasisPoints len = %d, want 3", len(d.BasisPoints))
	}
	if d.BasisPoints[2].Instrument != "BTC-25DEC26" || d.BasisPoints[2].Expiry != "2026-12-25T08:00:00Z" {
		t.Errorf("BasisPoints[2] = %+v, want BTC-25DEC26 at 2026-12-25T08:00:00Z", d.BasisPoints[2])
	}

	dvol, dCode := fetchDeribitDVOL(ctx, srv.URL, now, 30)
	if dCode != "" || len(dvol) != 3 || math.Abs(dvol[2].Close-53.6) > 1e-6 {
		t.Errorf("fetchDeribitDVOL = %+v, code=%q", dvol, dCode)
	}
}
