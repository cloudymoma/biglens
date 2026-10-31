package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCGCuratedSpot(t *testing.T) {
	resetBtcCGRateLimitForTest()
	raw, err := os.ReadFile("testdata/bitcoin/coingecko_tickers_curated.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	prev := btcCGTickersBaseURL
	btcCGTickersBaseURL = srv.URL
	defer func() { btcCGTickersBaseURL = prev }()

	got, code := fetchCGCuratedSpot(context.Background(), nil)
	if code != "" {
		t.Fatalf("fetchCGCuratedSpot code = %q, want empty", code)
	}
	bn, ok := got.ByVenue["binance"]
	if !ok {
		t.Fatal("missing binance in ByVenue")
	}
	// 10 valid pairs summed; anomaly ($500M) and stale ($400M) excluded.
	if bn.Pairs != 10 {
		t.Errorf("binance Pairs = %d, want 10 (anomaly and stale excluded)", bn.Pairs)
	}
	const wantBinanceSpot = 1620000000.0
	if math.Abs(bn.Spot24hUSD-wantBinanceSpot) > 1.0 {
		t.Errorf("binance Spot24hUSD = %f, want %f", bn.Spot24hUSD, wantBinanceSpot)
	}
	// Depth = (Σ up + Σ down) / 2 = (35,400,000 + 33,400,000) / 2 = 34,400,000.
	const wantBinanceDepth = (35400000.0 + 33400000.0) / 2.0
	if bn.Depth2pctUSD == nil || math.Abs(*bn.Depth2pctUSD-wantBinanceDepth) > 1.0 {
		t.Errorf("binance Depth2pctUSD = %v, want %f", bn.Depth2pctUSD, wantBinanceDepth)
	}
	// Total curated spot across Binance ($1.62B) + Coinbase ($0.85B) + OKX ($0.458B) + Kraken ($0.21B) + Bybit ($0.462B) = $3.60B.
	if math.Abs(got.TotalSpot24hUSD-3600000000.0) > 1.0 {
		t.Errorf("TotalSpot24hUSD = %f, want 3600000000.0 ($3.6 B)", got.TotalSpot24hUSD)
	}
}

func TestCGDerivativesBTC(t *testing.T) {
	resetBtcCGRateLimitForTest()
	raw, err := os.ReadFile("testdata/bitcoin/coingecko_derivatives.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	prev := btcCGDerivativesURL
	btcCGDerivativesURL = srv.URL
	defer func() { btcCGDerivativesURL = prev }()

	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, code := fetchCGDerivativesBTC(context.Background(), now)
	if code != "" {
		t.Fatalf("fetchCGDerivativesBTC code = %q, want empty", code)
	}
	// 10 unexpired BTC rows across 9 venues; expired BTC-27SEP24 and ETHUSDT excluded.
	if got.BTCIndexRows != 10 {
		t.Errorf("BTCIndexRows = %d, want 10 (expired and ETH excluded)", got.BTCIndexRows)
	}
	if got.VenuesCount != 9 {
		t.Errorf("VenuesCount = %d, want 9", got.VenuesCount)
	}
	// Long-tail venues (WEEX, Tapbit) appear only in AllReported totals, never in FallbackVenues.
	if _, exists := got.FallbackVenues["weex"]; exists {
		t.Error("long-tail venue WEEX must not appear in FallbackVenues")
	}
	if got.AllReportedVolume24hUSD <= 40_000_000_000 {
		t.Errorf("AllReportedVolume24hUSD = %f, expected long-tail volume included", got.AllReportedVolume24hUSD)
	}
}

func TestCGFallbackRows(t *testing.T) {
	resetBtcCGRateLimitForTest()
	raw, err := os.ReadFile("testdata/bitcoin/coingecko_derivatives.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	prev := btcCGDerivativesURL
	btcCGDerivativesURL = srv.URL
	defer func() { btcCGDerivativesURL = prev }()

	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, code := fetchCGDerivativesBTC(context.Background(), now)
	if code != "" {
		t.Fatalf("fetchCGDerivativesBTC code = %q", code)
	}

	bn, ok := got.FallbackVenues["binance"]
	if !ok {
		t.Fatal("missing binance fallback row")
	}
	if len(bn.Sources) == 0 || bn.Sources[0].Via != "coingecko" {
		t.Errorf("binance fallback Sources = %+v, want Via=coingecko", bn.Sources)
	}
	wantVol := 10966095175.28 + 1482500000.0
	if bn.Deriv24hUSD == nil || math.Abs(*bn.Deriv24hUSD-wantVol) > 1.0 {
		t.Errorf("binance Deriv24hUSD = %v, want %f", bn.Deriv24hUSD, wantVol)
	}
	wantOI := 7656980000.0 + 1343862400.0
	if bn.OIUSD == nil || math.Abs(*bn.OIUSD-wantOI) > 1.0 {
		t.Errorf("binance OIUSD = %v, want %f", bn.OIUSD, wantOI)
	}
	// Verified 8h venue: funding_rate 0.005447% -> 5.964465% APR.
	if bn.FundingAPRPct == nil || math.Abs(*bn.FundingAPRPct-5.964465) > 0.01 {
		t.Errorf("binance fallback FundingAPRPct = %v, want ~5.96%%", bn.FundingAPRPct)
	}

	// Unverified fallback venue funding (e.g. kraken, hyperliquid) must be nil.
	if kr, ok := got.FallbackVenues["kraken"]; !ok || kr.FundingAPRPct != nil {
		t.Errorf("kraken fallback FundingAPRPct = %v, want nil (unverified unit in CoinGecko)", kr.FundingAPRPct)
	}
}

type lazyDerivativesStream struct {
	total     int
	idx       int
	malformed bool
	oversize  bool
	buf       []byte
}

func (s *lazyDerivativesStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.oversize {
			// Emit > 32 MiB of valid JSON whitespace/elements so byteCountReader
			// trips without spending 20+ seconds decoding 195k structs under -race.
			if s.idx == 0 {
				s.buf = append(s.buf, `[`...)
				s.buf = append(s.buf, `{"market":"Binance (Futures)","symbol":"ETHUSDT","index_id":"ETH","price":"2640.0","contract_type":"perpetual","open_interest":1000,"converted_volume":{"usd":"1000"}},`...)
			}
			s.idx++
			for i := 0; i < 64*1024; i++ {
				s.buf = append(s.buf, ' ')
			}
			continue
		}
		if s.idx == 0 {
			s.buf = append(s.buf, '[')
		}
		if s.idx >= s.total {
			if s.idx == s.total {
				s.buf = append(s.buf, ']')
				s.idx++
				break
			}
			return 0, io.EOF
		}
		if s.malformed && s.idx == s.total/2 {
			s.buf = append(s.buf, []byte(`{"market": BROKEN_JSON`)...)
			s.idx = s.total + 1
			break
		}
		if s.idx > 0 {
			s.buf = append(s.buf, ',')
		}
		indexID := "ETH"
		market := "Other"
		if s.idx%100 == 0 {
			indexID = "BTC"
			market = fmt.Sprintf("Venue-%d", s.idx/100)
			if s.idx == 0 {
				market = "Binance (Futures)"
			}
		}
		elem := fmt.Sprintf(
			`{"market":%q,"symbol":"SYM-%d","index_id":%q,"price":"81484.00","price_percentage_change_24h":1.13,"contract_type":"perpetual","index":81450.0,"basis":-0.04,"spread":0.01,"funding_rate":0.005447,"open_interest":1000000.0,"volume_24h":8148400.0,"last_traded_at":1760659200,"expired_at":null}`,
			market, s.idx, indexID,
		)
		s.buf = append(s.buf, elem...)
		s.idx++
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func TestCGDerivativesStreaming(t *testing.T) {
	resetBtcCGRateLimitForTest()
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

	// 1. Lazily generated ~10 MB stream of 30,000 tickers (300 BTC rows).
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, &lazyDerivativesStream{total: 30_000})
	}))
	defer srvOK.Close()

	prev := btcCGDerivativesURL
	defer func() { btcCGDerivativesURL = prev }()

	btcCGDerivativesURL = srvOK.URL
	got, code := fetchCGDerivativesBTC(context.Background(), now)
	if code != "" {
		t.Fatalf("30,000-ticker stream code = %q, want empty", code)
	}
	if got.BTCIndexRows != 300 {
		t.Errorf("BTCIndexRows = %d, want 300", got.BTCIndexRows)
	}

	// 2. Malformed input mid-stream -> bad_response.
	srvBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, &lazyDerivativesStream{total: 1000, malformed: true})
	}))
	defer srvBad.Close()
	btcCGDerivativesURL = srvBad.URL
	if _, code := fetchCGDerivativesBTC(context.Background(), now); code != "bad_response" {
		t.Errorf("malformed stream code = %q, want bad_response", code)
	}

	// 3. Body over 32 MiB -> bad_response.
	srvHuge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, &lazyDerivativesStream{oversize: true})
	}))
	defer srvHuge.Close()
	btcCGDerivativesURL = srvHuge.URL
	if _, code := fetchCGDerivativesBTC(context.Background(), now); code != "bad_response" {
		t.Errorf("oversize (>32 MiB) stream code = %q, want bad_response", code)
	}
}

func BenchmarkCGDerivativesDecode(b *testing.B) {
	resetBtcCGRateLimitForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, &lazyDerivativesStream{total: 30_000})
	}))
	defer srv.Close()

	prev := btcCGDerivativesURL
	btcCGDerivativesURL = srv.URL
	defer func() { btcCGDerivativesURL = prev }()

	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, code := fetchCGDerivativesBTC(context.Background(), now); code != "" {
			b.Fatalf("code = %q", code)
		}
	}
}

func TestCFTCBitcoin(t *testing.T) {
	raw, err := os.ReadFile("testdata/bitcoin/cftc_tff_btc.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	prev := btcCFTCTFFBaseURL
	btcCFTCTFFBaseURL = srv.URL
	defer func() { btcCFTCTFFBaseURL = prev }()

	got, code := fetchCFTCBitcoin(context.Background(), 52)
	if code != "" {
		t.Fatalf("fetchCFTCBitcoin code = %q, want empty", code)
	}
	// CME 20,752 contracts * 5 BTC = 103,760 BTC.
	if math.Abs(got.CMEOIBTC-103760.0) > 1e-6 {
		t.Errorf("CMEOIBTC = %f, want 103760.0 (20,752 * 5 BTC)", got.CMEOIBTC)
	}
	if len(got.CMEHistory) != 3 {
		t.Fatalf("len(CMEHistory) = %d, want 3", len(got.CMEHistory))
	}
	latestCME := got.CMEHistory[len(got.CMEHistory)-1]
	// Asset managers: (8,142 - 4,500) = +3,642 contracts * 5 BTC = +18,210 BTC.
	if math.Abs(latestCME.AssetManagersNetBTC-18210.0) > 1e-6 {
		t.Errorf("AssetManagersNetBTC = %f, want +18210.0 (+3,642 * 5 BTC)", latestCME.AssetManagersNetBTC)
	}
	// Leveraged funds: (3,812 - 9,656) = -5,844 contracts * 5 BTC = -29,220 BTC.
	if math.Abs(latestCME.LeveragedFundsNetBTC-(-29220.0)) > 1e-6 {
		t.Errorf("LeveragedFundsNetBTC = %f, want -29220.0 (-5,844 * 5 BTC)", latestCME.LeveragedFundsNetBTC)
	}
	// Micro (28,400 * 0.1 = 2,840 BTC) and CDE (173,743 * 0.01 = 1,737.43 BTC).
	if math.Abs(got.CMEMicroOIBTC-2840.0) > 1e-6 {
		t.Errorf("CMEMicroOIBTC = %f, want 2840.0", got.CMEMicroOIBTC)
	}
	if math.Abs(got.CDEOIBTC-1737.43) > 1e-6 {
		t.Errorf("CDEOIBTC = %f, want 1737.43", got.CDEOIBTC)
	}
	if math.Abs(got.TotalCMERegBTC-(103760.0+2840.0+1737.43)) > 1e-6 {
		t.Errorf("TotalCMERegBTC = %f, want %f (CME + Micro + CDE)", got.TotalCMERegBTC, 103760.0+2840.0+1737.43)
	}
}

func TestCFTCBitcoinBugFixes(t *testing.T) {
	// 1. Substring "5 BITCOIN" must not match "0.5 BITCOIN" or "0.05 BITCOIN",
	// and Coinbase institutional 1 BTC contract ("(1 BITCOIN)") must resolve to 1.0 BTC.
	if sz, ok := resolveCFTCContractSizeBTC("HALF BITCOIN - SOME EXCHANGE", "999001", "(CONTRACTS OF 0.5 BITCOIN)"); !ok || math.Abs(sz-0.5) > 1e-9 {
		t.Errorf("resolveCFTCContractSizeBTC(0.5 BITCOIN) = (%v, %v), want (0.5, true)", sz, ok)
	}
	if sz, ok := resolveCFTCContractSizeBTC("TWENTIETH BITCOIN - SOME EXCHANGE", "999002", "(0.05 BITCOIN)"); !ok || math.Abs(sz-0.05) > 1e-9 {
		t.Errorf("resolveCFTCContractSizeBTC(0.05 BITCOIN) = (%v, %v), want (0.05, true)", sz, ok)
	}
	if sz, ok := resolveCFTCContractSizeBTC("BITCOIN - COINBASE DERIVATIVES, LLC", "133002", "(1 BITCOIN)"); !ok || math.Abs(sz-1.0) > 1e-9 {
		t.Errorf("resolveCFTCContractSizeBTC(Coinbase BTI 1 BTC) = (%v, %v), want (1.0, true)", sz, ok)
	}

	// 2. Stale discontinued contracts from older report dates must not pollute current OI totals,
	// and 1 BTC Coinbase contracts must be included in CDEOIBTC and TotalCMERegBTC.
	payload := `[
		{"market_and_exchange_names":"BITCOIN - CHICAGO MERCANTILE EXCHANGE","cftc_contract_market_code":"133741","report_date_as_yyyy_mm_dd":"2026-10-13T00:00:00.000","contract_units":"(CONTRACTS OF 5 BITCOIN)","open_interest_all":"100"},
		{"market_and_exchange_names":"BITCOIN - COINBASE DERIVATIVES, LLC","cftc_contract_market_code":"133002","report_date_as_yyyy_mm_dd":"2026-10-13T00:00:00.000","contract_units":"(1 BITCOIN)","open_interest_all":"50"},
		{"market_and_exchange_names":"BITCOIN NANO - COINBASE DERIVATIVES, LLC","cftc_contract_market_code":"133001","report_date_as_yyyy_mm_dd":"2026-10-13T00:00:00.000","contract_units":"(0.01 BITCOIN)","open_interest_all":"1000"},
		{"market_and_exchange_names":"OLD DELISTED BITCOIN - CHICAGO MERCANTILE EXCHANGE","cftc_contract_market_code":"999741","report_date_as_yyyy_mm_dd":"2025-12-01T00:00:00.000","contract_units":"(CONTRACTS OF 5 BITCOIN)","open_interest_all":"9000"}
	]`
	returnEmpty := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if returnEmpty {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	prev := btcCFTCTFFBaseURL
	btcCFTCTFFBaseURL = srv.URL
	defer func() { btcCFTCTFFBaseURL = prev }()

	// 3. Empty [] Socrata response must return a non-empty error code so btcCachedSource
	// does NOT cache an empty summary for 12 hours.
	h := newTestBitcoinHandler(t)
	val1, src1 := h.btcCachedSource(context.Background(), btcMarketsCFTCKey, btcMarketsCFTCTTL, "cftc", func(c context.Context) (any, string) {
		return fetchCFTCBitcoin(c, 52)
	})
	if val1 != nil || src1.Status == "ok" {
		t.Fatalf("empty CFTC response returned (%+v, %+v), want nil and error status (not cached 12h)", val1, src1)
	}

	// Next call after Socrata recovers must immediately fetch fresh data (not stuck on cached empty).
	returnEmpty = false
	val2, src2 := h.btcCachedSource(context.Background(), btcMarketsCFTCKey, btcMarketsCFTCTTL, "cftc", func(c context.Context) (any, string) {
		return fetchCFTCBitcoin(c, 52)
	})
	if src2.Status != "ok" || val2 == nil {
		t.Fatalf("recovered CFTC response returned (%+v, %+v), want ok", val2, src2)
	}
	got := val2.(*BtcCFTCSummary)
	// Current 2026-10-13 contracts only: CME 100*5=500 BTC, CDE 50*1 + 1000*0.01 = 60 BTC.
	// Stale 2025-12-01 delisted contract (9000*5=45,000 BTC) must be excluded.
	if math.Abs(got.CMEOIBTC-500.0) > 1e-6 {
		t.Errorf("CMEOIBTC = %f, want 500.0 (excluding stale 2025-12-01 contract)", got.CMEOIBTC)
	}
	if math.Abs(got.CDEOIBTC-60.0) > 1e-6 {
		t.Errorf("CDEOIBTC = %f, want 60.0 (50*1 + 1000*0.01)", got.CDEOIBTC)
	}
	if math.Abs(got.TotalCMERegBTC-560.0) > 1e-6 {
		t.Errorf("TotalCMERegBTC = %f, want 560.0 (500 + 60)", got.TotalCMERegBTC)
	}
	if len(got.LatestByMarket) != 3 {
		t.Errorf("len(LatestByMarket) = %d, want 3 active contracts on %s", len(got.LatestByMarket), got.AsOfReportDate)
	}
}

func TestCFTCBitcoinLive2026Payload(t *testing.T) {
	// CFTC's live TFF dataset mislabels Coinbase's Bitcoin Cash perp ("133LM5") with contract_units "(1 Bitcoin)".
	// It must be rejected as non-BTC, and fractional units like "(1/100 Bitcoin)" must never match "100".
	if sz, ok := resolveCFTCContractSizeBTC("BITCOIN CASH PERP STYLE - COINBASE DERIVATIVES, LLC", "133LM5", "(1 Bitcoin)"); ok || sz != 0 {
		t.Errorf("resolveCFTCContractSizeBTC(BITCOIN CASH) = (%v, %v), want (0, false)", sz, ok)
	}
	if sz, ok := resolveCFTCContractSizeBTC("Nano Bitcoin  - COINBASE DERIVATIVES, LLC", "133LM1", "(1/100 Bitcoin)"); !ok || math.Abs(sz-0.01) > 1e-9 {
		t.Errorf("resolveCFTCContractSizeBTC(1/100 Bitcoin without X) = (%v, %v), want (0.01, true)", sz, ok)
	}

	// Exact 2026-10-27 Socrata rows returned by upper(market_and_exchange_names) like '%BITCOIN%'.
	livePayload := `[
		{"market_and_exchange_names":"BITCOIN - CHICAGO MERCANTILE EXCHANGE","cftc_contract_market_code":"133741","report_date_as_yyyy_mm_dd":"2026-10-27T00:00:00.000","contract_units":"(5 Bitcoins)","open_interest_all":"20791"},
		{"market_and_exchange_names":"MICRO BITCOIN - CHICAGO MERCANTILE EXCHANGE","cftc_contract_market_code":"133742","report_date_as_yyyy_mm_dd":"2026-10-27T00:00:00.000","contract_units":"(Bitcoin X $0.10)","open_interest_all":"30883"},
		{"market_and_exchange_names":"Nano Bitcoin  - COINBASE DERIVATIVES, LLC","cftc_contract_market_code":"133LM1","report_date_as_yyyy_mm_dd":"2026-10-27T00:00:00.000","contract_units":"(1/100 X Bitcoin = $300)","open_interest_all":"121384"},
		{"market_and_exchange_names":"NANO BITCOIN PERP STYLE - COINBASE DERIVATIVES, LLC","cftc_contract_market_code":"133LM4","report_date_as_yyyy_mm_dd":"2026-10-27T00:00:00.000","contract_units":"(1/100 X Bitcoin)","open_interest_all":"186914"},
		{"market_and_exchange_names":"BITCOIN CASH PERP STYLE - COINBASE DERIVATIVES, LLC","cftc_contract_market_code":"133LM5","report_date_as_yyyy_mm_dd":"2026-10-27T00:00:00.000","contract_units":"(1 Bitcoin)","open_interest_all":"2522"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(livePayload))
	}))
	defer srv.Close()

	prev := btcCFTCTFFBaseURL
	btcCFTCTFFBaseURL = srv.URL
	defer func() { btcCFTCTFFBaseURL = prev }()

	got, code := fetchCFTCBitcoin(context.Background(), 52)
	if code != "" {
		t.Fatalf("fetchCFTCBitcoin code = %q, want empty", code)
	}
	const wantCME = 20791 * 5.0
	const wantMicro = 30883 * 0.1
	const wantCDE = 121384*0.01 + 186914*0.01 // 3082.98 BTC (excluding 2522 BCH contracts)
	if math.Abs(got.CMEOIBTC-wantCME) > 1e-6 {
		t.Errorf("CMEOIBTC = %f, want %f", got.CMEOIBTC, wantCME)
	}
	if math.Abs(got.CMEMicroOIBTC-wantMicro) > 1e-6 {
		t.Errorf("CMEMicroOIBTC = %f, want %f", got.CMEMicroOIBTC, wantMicro)
	}
	if math.Abs(got.CDEOIBTC-wantCDE) > 1e-6 {
		t.Errorf("CDEOIBTC = %f, want %f (Bitcoin Cash 133LM5 must be excluded)", got.CDEOIBTC, wantCDE)
	}
	if math.Abs(got.TotalCMERegBTC-(wantCME+wantMicro+wantCDE)) > 1e-6 {
		t.Errorf("TotalCMERegBTC = %f, want %f", got.TotalCMERegBTC, wantCME+wantMicro+wantCDE)
	}
	if len(got.LatestByMarket) != 4 {
		t.Errorf("len(LatestByMarket) = %d, want 4 BTC contracts (got %+v)", len(got.LatestByMarket), got.LatestByMarket)
	}
}
