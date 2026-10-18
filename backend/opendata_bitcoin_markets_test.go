package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type marketsTestUpstream struct {
	server         *httptest.Server
	binanceStatus  atomic.Int32
	okxStatus      atomic.Int32
	coinbaseStatus atomic.Int32
	krakenStatus   atomic.Int32
	hlStatus       atomic.Int32
	deribitStatus  atomic.Int32
	cgStatus       atomic.Int32
	cftcStatus     atomic.Int32
	marketCalls    atomic.Int32
	prevFapi       string
	prevDapi       string
	prevOKX        string
	prevINTX       string
	prevCDE        string
	prevKraken     string
	prevHL         string
	prevDeribit    string
	prevCGBitcoin  string
	prevCGTickers  string
	prevCGDeriv    string
	prevCFTC       string
}

func startMarketsTestUpstream(t *testing.T) *marketsTestUpstream {
	t.Helper()
	resetBtcCGRateLimitForTest()

	readFix := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "bitcoin", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return b
	}

	bnFapiTicker := readFix("binance_fapi_ticker24h.json")
	bnFapiOI := readFix("binance_fapi_oi.json")
	bnFapiPrem := readFix("binance_fapi_premium.json")
	bnDapiTicker := readFix("binance_dapi_ticker24h.json")
	bnDapiOI := readFix("binance_dapi_oi.json")
	okxTicker := readFix("okx_swap_ticker.json")
	okxOI := readFix("okx_swap_oi.json")
	okxFund := readFix("okx_swap_funding.json")
	okxRubik := []byte(`{"code":"0","data":[["1760572800000","3463070000","5684230000"],["1760659200000","3510000000","5820000000"]]}`)
	cbINTX := readFix("coinbase_intx_btc_perp.json")
	cbCDE := readFix("coinbase_cde_bip.json")
	krTickers := readFix("kraken_futures_tickers.json")
	hlMeta := readFix("hyperliquid_meta_and_ctx.json")
	dbPerp := readFix("deribit_perp_ticker.json")
	dbFut := readFix("deribit_futures_summary.json")
	dbDVOL := readFix("deribit_dvol.json")
	cgBtc := readFix("coingecko_bitcoin.json")
	cgSpot := readFix("coingecko_tickers_curated.json")
	cgDeriv := readFix("coingecko_derivatives.json")
	cftcRows := readFix("cftc_tff_btc.json")

	up := &marketsTestUpstream{}
	up.binanceStatus.Store(http.StatusOK)
	up.okxStatus.Store(http.StatusOK)
	up.coinbaseStatus.Store(http.StatusOK)
	up.krakenStatus.Store(http.StatusOK)
	up.hlStatus.Store(http.StatusOK)
	up.deribitStatus.Store(http.StatusOK)
	up.cgStatus.Store(http.StatusOK)
	up.cftcStatus.Store(http.StatusOK)

	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.marketCalls.Add(1)
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/fapi/"):
			if st := int(up.binanceStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			switch {
			case strings.HasSuffix(p, "/ticker/24hr"):
				_, _ = w.Write(bnFapiTicker)
			case strings.HasSuffix(p, "/openInterest"):
				_, _ = w.Write(bnFapiOI)
			case strings.HasSuffix(p, "/premiumIndex"):
				_, _ = w.Write(bnFapiPrem)
			}
		case strings.HasPrefix(p, "/dapi/"):
			if st := int(up.binanceStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			switch {
			case strings.HasSuffix(p, "/ticker/24hr"):
				_, _ = w.Write(bnDapiTicker)
			case strings.HasSuffix(p, "/openInterest"):
				_, _ = w.Write(bnDapiOI)
			}
		case strings.HasPrefix(p, "/api/v5/"):
			if st := int(up.okxStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			switch {
			case strings.Contains(p, "/market/ticker"):
				_, _ = w.Write(okxTicker)
			case strings.Contains(p, "/public/open-interest"):
				_, _ = w.Write(okxOI)
			case strings.Contains(p, "/public/funding-rate"):
				_, _ = w.Write(okxFund)
			case strings.Contains(p, "/rubik/"):
				_, _ = w.Write(okxRubik)
			}
		case strings.HasSuffix(p, "/instruments/BTC-PERP"):
			if st := int(up.coinbaseStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write(cbINTX)
		case strings.Contains(p, "/market/products/BIP-20DEC30-CDE"):
			if st := int(up.coinbaseStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write(cbCDE)
		case strings.Contains(p, "/derivatives/api/v3/tickers"):
			if st := int(up.krakenStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write(krTickers)
		case strings.HasSuffix(p, "/hl/info"):
			if st := int(up.hlStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write(hlMeta)
		case strings.HasPrefix(p, "/public/"):
			if st := int(up.deribitStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			switch {
			case strings.Contains(p, "ticker"):
				_, _ = w.Write(dbPerp)
			case strings.Contains(p, "get_book_summary_by_currency"):
				_, _ = w.Write(dbFut)
			case strings.Contains(p, "get_volatility_index_data"):
				_, _ = w.Write(dbDVOL)
			}
		case strings.HasPrefix(p, "/cg/"):
			if st := int(up.cgStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			switch {
			case strings.HasSuffix(p, "/coins/bitcoin"):
				_, _ = w.Write(cgBtc)
			case strings.HasSuffix(p, "/coins/bitcoin/tickers"):
				_, _ = w.Write(cgSpot)
			case strings.HasSuffix(p, "/derivatives"):
				_, _ = w.Write(cgDeriv)
			}
		case strings.HasSuffix(p, "/cftc"):
			if st := int(up.cftcStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write(cftcRows)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	up.prevFapi = btcBinanceFapiBaseURL
	up.prevDapi = btcBinanceDapiBaseURL
	up.prevOKX = btcOKXBaseURL
	up.prevINTX = btcCoinbaseINTXPerpURL
	up.prevCDE = btcCoinbaseCDEBIPURL
	up.prevKraken = btcKrakenFuturesTickURL
	up.prevHL = btcHyperliquidInfoURL
	up.prevDeribit = btcDeribitBaseURL
	up.prevCGBitcoin = btcCGBitcoinURL
	up.prevCGTickers = btcCGTickersBaseURL
	up.prevCGDeriv = btcCGDerivativesURL
	up.prevCFTC = btcCFTCTFFBaseURL

	u := up.server.URL
	btcBinanceFapiBaseURL = u
	btcBinanceDapiBaseURL = u
	btcOKXBaseURL = u
	btcCoinbaseINTXPerpURL = u + "/api/v1/instruments/BTC-PERP"
	btcCoinbaseCDEBIPURL = u + "/api/v3/brokerage/market/products/BIP-20DEC30-CDE"
	btcKrakenFuturesTickURL = u + "/derivatives/api/v3/tickers"
	btcHyperliquidInfoURL = u + "/hl/info"
	btcDeribitBaseURL = u
	btcCGBitcoinURL = u + "/cg/coins/bitcoin"
	btcCGTickersBaseURL = u + "/cg/coins/bitcoin/tickers"
	btcCGDerivativesURL = u + "/cg/derivatives"
	btcCFTCTFFBaseURL = u + "/cftc"

	t.Cleanup(func() {
		up.server.Close()
		btcBinanceFapiBaseURL = up.prevFapi
		btcBinanceDapiBaseURL = up.prevDapi
		btcOKXBaseURL = up.prevOKX
		btcCoinbaseINTXPerpURL = up.prevINTX
		btcCoinbaseCDEBIPURL = up.prevCDE
		btcKrakenFuturesTickURL = up.prevKraken
		btcHyperliquidInfoURL = up.prevHL
		btcDeribitBaseURL = up.prevDeribit
		btcCGBitcoinURL = up.prevCGBitcoin
		btcCGTickersBaseURL = up.prevCGTickers
		btcCGDerivativesURL = up.prevCGDeriv
		btcCFTCTFFBaseURL = up.prevCFTC
		resetBtcCGRateLimitForTest()
	})
	return up
}

func TestMarketsRatios(t *testing.T) {
	// Curated spot $3.6 B / derivatives $23.4 B -> 6.5x; all-reported 121.2 / 27.39 -> 4.42x.
	curatedSpot := btcFloatPtr(3_600_000_000.0)
	curatedDeriv := btcFloatPtr(23_400_000_000.0)
	allSpot := btcFloatPtr(27_390_000_000.0)
	allDeriv := btcFloatPtr(121_200_000_000.0)

	got := computeMarketsRatios(curatedSpot, curatedDeriv, allSpot, allDeriv, []BtcVenueRatio{
		{Venue: "binance", Spot24hUSD: 1_620_000_000, Deriv24hUSD: 10_966_095_175, Ratio: btcFloatPtr(10_966_095_175.0 / 1_620_000_000.0)},
	}, nil)

	if got.MajorDerivSpot == nil || math.Abs(*got.MajorDerivSpot-6.5) > 1e-6 {
		t.Errorf("MajorDerivSpot = %v, want 6.5", got.MajorDerivSpot)
	}
	if got.AllReportedDerivSpot == nil || math.Abs(*got.AllReportedDerivSpot-4.4249726) > 0.01 {
		t.Errorf("AllReportedDerivSpot = %v, want ~4.42", got.AllReportedDerivSpot)
	}
	if !got.AllReportedUnverified {
		t.Error("AllReportedUnverified = false, want true")
	}
	if len(got.PerVenue) != 1 || got.PerVenue[0].Venue != "binance" {
		t.Errorf("PerVenue = %+v, want binance entry", got.PerVenue)
	}
}

func TestMarketsBinanceViaCoinGecko(t *testing.T) {
	up := startMarketsTestUpstream(t)
	// Simulate US deployment region: Binance fapi/dapi return HTTP 451 (D13).
	up.binanceStatus.Store(http.StatusUnavailableForLegalReasons)

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/markets", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMarkets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var data BtcMarketsData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal BtcMarketsData: %v", err)
	}

	var bnRow *BtcVenueRow
	for i := range data.Venues {
		if data.Venues[i].Venue == "binance" {
			bnRow = &data.Venues[i]
			break
		}
	}
	if bnRow == nil {
		t.Fatal("missing binance row in Venues")
	}
	if len(bnRow.Sources) == 0 || bnRow.Sources[0].Via != "coingecko" || bnRow.Sources[0].Status != "ok" {
		t.Errorf("binance row Sources = %+v, want Via=coingecko Status=ok", bnRow.Sources)
	}
	if bnRow.Deriv24hUSD == nil || *bnRow.Deriv24hUSD <= 0 {
		t.Errorf("binance Deriv24hUSD = %v, want > 0 from CoinGecko fallback", bnRow.Deriv24hUSD)
	}
	if bnRow.OIUSD == nil || *bnRow.OIUSD <= 0 {
		t.Errorf("binance OIUSD = %v, want > 0 from CoinGecko fallback", bnRow.OIUSD)
	}
	if data.Ratios.MajorDerivSpot == nil || *data.Ratios.MajorDerivSpot <= 0 {
		t.Errorf("MajorDerivSpot = %v, want > 0", data.Ratios.MajorDerivSpot)
	}
	if len(data.OIShares) == 0 {
		t.Error("expected non-empty OIShares")
	}
	var foundTopBadge bool
	for _, src := range data.Sources {
		if src.ID == "binance" {
			foundTopBadge = true
			if src.Via != "coingecko" {
				t.Errorf("top-level binance source badge Via = %q, want coingecko", src.Via)
			}
		}
	}
	if !foundTopBadge {
		t.Error("missing binance in top-level Sources")
	}
}

func TestMarketsCoinGeckoDown(t *testing.T) {
	up := startMarketsTestUpstream(t)
	up.binanceStatus.Store(http.StatusUnavailableForLegalReasons)

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) }

	// 1. Warm lastGood while CoinGecko is up.
	rec1 := httptest.NewRecorder()
	h.BitcoinMarkets(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/markets", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("warm status = %d", rec1.Code)
	}

	// 2. Clear TTL cache so all sources re-fetch, then make CoinGecko return 429.
	h.cache = NewCache(5 * time.Minute)
	resetBtcCGRateLimitForTest()
	up.cgStatus.Store(http.StatusTooManyRequests)

	rec2 := httptest.NewRecorder()
	h.BitcoinMarkets(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/markets", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("degraded status = %d", rec2.Code)
	}

	var data BtcMarketsData
	if err := json.Unmarshal(rec2.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Spot, fallback rows (binance, bybit) and ratios are served from last-good with stale: true;
	// direct-venue rows (okx, coinbase, kraken, hyperliquid, deribit) stay live ("ok").
	if data.Ratios.MajorDerivSpot == nil || data.Ratios.AllReportedDerivSpot == nil {
		t.Fatalf("expected ratios from last-good, got %+v", data.Ratios)
	}
	var cgStale bool
	for _, s := range data.Sources {
		if s.ID == "coingecko" && s.Status == "stale" {
			cgStale = true
		}
	}
	if !cgStale {
		t.Errorf("expected coingecko source status = stale, got %+v", data.Sources)
	}

	for _, r := range data.Venues {
		switch r.Venue {
		case "binance", "bybit":
			if r.Deriv24hUSD == nil || *r.Deriv24hUSD <= 0 {
				t.Errorf("%s fallback Deriv24hUSD = %v, want last-good", r.Venue, r.Deriv24hUSD)
			}
			if len(r.Sources) == 0 || r.Sources[0].Status != "stale" {
				t.Errorf("%s fallback Sources[0] = %+v, want Status=stale", r.Venue, r.Sources)
			}
		case "okx", "coinbase", "kraken", "hyperliquid", "deribit":
			if len(r.Sources) == 0 || r.Sources[0].Status != "ok" {
				t.Errorf("%s direct Sources[0] = %+v, want live Status=ok", r.Venue, r.Sources)
			}
		}
		if r.Venue == "binance" || r.Venue == "okx" || r.Venue == "coinbase" || r.Venue == "kraken" || r.Venue == "bybit" {
			if r.Spot24hUSD == nil || *r.Spot24hUSD <= 0 {
				t.Errorf("%s Spot24hUSD = %v, want last-good spot", r.Venue, r.Spot24hUSD)
			}
		}
	}
}

func TestMarketsShares(t *testing.T) {
	_ = startMarketsTestUpstream(t)
	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) }

	rec := httptest.NewRecorder()
	h.BitcoinMarkets(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/markets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var data BtcMarketsData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.DEXSharePct == nil || *data.DEXSharePct <= 0 || *data.DEXSharePct >= 100 {
		t.Fatalf("DEXSharePct = %v, want in (0, 100)", data.DEXSharePct)
	}
	if data.USRegulatedSharePct == nil || *data.USRegulatedSharePct <= 0 || *data.USRegulatedSharePct >= 100 {
		t.Fatalf("USRegulatedSharePct = %v, want in (0, 100)", data.USRegulatedSharePct)
	}
	if data.OIToMcapPct == nil || *data.OIToMcapPct <= 0 {
		t.Fatalf("OIToMcapPct = %v, want > 0", data.OIToMcapPct)
	}

	// Verify DEX share = Hyperliquid OI / perp OI excl CME.
	var hlOI, perpExclCME, cmeOI float64
	for _, r := range data.Venues {
		if r.OIUSD == nil {
			continue
		}
		if r.Venue == "cme" {
			if !r.Weekly {
				t.Error("CME row must have Weekly = true")
			}
			cmeOI = *r.OIUSD
		} else {
			perpExclCME += *r.OIUSD
		}
		if r.Venue == "hyperliquid" {
			hlOI = *r.OIUSD
		}
	}
	wantDEX := (hlOI / perpExclCME) * 100.0
	if math.Abs(*data.DEXSharePct-wantDEX) > 1e-4 {
		t.Errorf("DEXSharePct = %f, want %f", *data.DEXSharePct, wantDEX)
	}
	if cmeOI <= 0 {
		t.Errorf("cmeOI = %f, want > 0 from CFTC weekly report", cmeOI)
	}
	// US-regulated share includes CME + Coinbase CDE OI.
	totalOI := perpExclCME + cmeOI
	minUSReg := (cmeOI / totalOI) * 100.0
	if *data.USRegulatedSharePct <= minUSReg {
		t.Errorf("USRegulatedSharePct = %f, want > CME-only %f (includes Coinbase CDE)", *data.USRegulatedSharePct, minUSReg)
	}
}

func TestMarketsPartial(t *testing.T) {
	up := startMarketsTestUpstream(t)
	// Make Kraken Futures fail with 500.
	up.krakenStatus.Store(http.StatusInternalServerError)

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) }

	rec := httptest.NewRecorder()
	h.BitcoinMarkets(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/markets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var data BtcMarketsData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var krRow *BtcVenueRow
	for i := range data.Venues {
		if data.Venues[i].Venue == "kraken" {
			krRow = &data.Venues[i]
			break
		}
	}
	if krRow == nil {
		t.Fatal("missing kraken row")
	}
	if len(krRow.Sources) == 0 || krRow.Sources[0].Status != "error" {
		t.Errorf("kraken Sources[0] = %+v, want Status=error", krRow.Sources)
	}
	if krRow.Deriv24hUSD != nil {
		t.Errorf("kraken Deriv24hUSD = %v, want nil when direct fails", krRow.Deriv24hUSD)
	}
	if data.Ratios.MajorDerivSpot == nil || *data.Ratios.MajorDerivSpot <= 0 {
		t.Errorf("MajorDerivSpot = %v, want computed over remaining venues", data.Ratios.MajorDerivSpot)
	}
	if !strings.Contains(data.Ratios.Note, "kraken") {
		t.Errorf("Ratios.Note = %q, want note mentioning kraken", data.Ratios.Note)
	}
}

func TestBtcMarketDailySnapshot(t *testing.T) {
	up := startMarketsTestUpstream(t)
	store, err := openRiskStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	h := newTestBitcoinHandler(t)
	h.btc.store = store

	// 1. Before 00:30 UTC -> no snapshot written.
	curNow := time.Date(2026, 10, 17, 0, 15, 0, 0, time.UTC)
	h.btc.now = func() time.Time { return curNow }
	snap := newBtcMarketSnapshotter(h, store, func() time.Time { return curNow })
	snap.syncOnce(context.Background())

	rows, err := store.loadBtcMarketDaily(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows before 00:30 UTC = %d, want 0", len(rows))
	}

	// 2. At 00:35 UTC -> writes one row for 2026-10-17.
	curNow = time.Date(2026, 10, 17, 0, 35, 0, 0, time.UTC)
	snap.syncOnce(context.Background())
	rows, err = store.loadBtcMarketDaily(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Date != "2026-10-17" {
		t.Fatalf("rows after 00:35 UTC = %+v, want 1 row for 2026-10-17", rows)
	}
	if rows[0].MajorDerivSpot == nil || *rows[0].MajorDerivSpot <= 0 {
		t.Errorf("snapshot MajorDerivSpot = %v, want > 0", rows[0].MajorDerivSpot)
	}

	// 3. Re-run on the same day is a no-op (no upstream calls, still 1 row).
	callsBefore := up.marketCalls.Load()
	snap.syncOnce(context.Background())
	callsAfter := up.marketCalls.Load()
	if callsAfter != callsBefore {
		t.Errorf("upstream calls on re-run = %d, want %d (no-op)", callsAfter, callsBefore)
	}
	rows, err = store.loadBtcMarketDaily(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("rows after re-run = %d, want 1", len(rows))
	}
}
