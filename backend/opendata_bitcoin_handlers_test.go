package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

type panicBaselineStarter struct {
	t *testing.T
}

func (p panicBaselineStarter) dryRun(ctx context.Context) (btcBaselinePlan, error) {
	p.t.Fatalf("unexpected baseline dryRun call from HTTP handler")
	return btcBaselinePlan{}, errors.New("forbidden")
}

func (p panicBaselineStarter) confirm(ctx context.Context, token string) error {
	p.t.Fatalf("unexpected baseline confirm call from HTTP handler")
	return errors.New("forbidden")
}

func newTestBitcoinHandler(t *testing.T) *APIHandler {
	t.Helper()
	cfg := &Config{}
	cfg.BigQuery.ProjectID = "test-proj"
	h := NewAPIHandler(&BQClient{config: cfg})
	nowFn := func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	}
	h.btc.prices = newBtcPriceSyncer(openTestRiskStore(t), h.btc.settings, nowFn)
	h.btc.prices.lastSync = time.Now()
	h.btc.nodes = newBtcNodesSyncer(nil, h.btc.settings, t.TempDir(), nowFn)
	h.btc.nodes.lastSync = time.Now()
	return h
}

func TestBitcoinMeta(t *testing.T) {
	h := newTestBitcoinHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/meta", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMeta(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var meta BtcMetaData
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.BigQuery.Jobs {
		t.Errorf("BigQuery.Jobs = true, want false")
	}
	if meta.BigQuery.DataAvailable {
		t.Errorf("BigQuery.DataAvailable = true, want false")
	}
	if meta.BigQuery.BudgetGiB != 2048 {
		t.Errorf("BudgetGiB = %v, want 2048", meta.BigQuery.BudgetGiB)
	}
	if meta.BigQuery.CadenceDays != 7 {
		t.Errorf("CadenceDays = %d, want 7", meta.BigQuery.CadenceDays)
	}
	if math.Abs(meta.BigQuery.Costs.FirstDayUSD-1.49) > 1e-6 ||
		math.Abs(meta.BigQuery.Costs.SteadyMonthUSD-2.88) > 1e-6 ||
		math.Abs(meta.BigQuery.Costs.UTXOBaselineUSD-4.20) > 1e-6 {
		t.Errorf("Costs = %+v", meta.BigQuery.Costs)
	}
	if len(meta.Providers) != len(btcProviderRegistry) {
		t.Errorf("Providers len = %d, want %d", len(meta.Providers), len(btcProviderRegistry))
	}
}

func TestBitcoinLive(t *testing.T) {
	diffRaw, err := os.ReadFile("testdata/bitcoin/mempool_difficulty_adjustment.json")
	if err != nil {
		t.Fatal(err)
	}
	poolsRaw, err := os.ReadFile("testdata/bitcoin/mempool_pools_1w.json")
	if err != nil {
		t.Fatal(err)
	}
	cbStatsRaw, err := os.ReadFile("testdata/bitcoin/coinbase_stats.json")
	if err != nil {
		t.Fatal(err)
	}

	memSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/fees/precise":
			w.Write([]byte(`{"fastestFee":2.5,"halfHourFee":2.0,"hourFee":1.5,"economyFee":1.1,"minimumFee":1.0}`))
		case "/api/mempool":
			w.Write([]byte(`{"count":42000,"vsize":39000000,"total_fee":50000000,"fee_histogram":[[2,1000000]]}`))
		case "/api/v1/fees/mempool-blocks":
			w.Write([]byte(`[{"blockVSize":1000000,"nTx":2500,"totalFees":10000000,"medianFee":2.5,"feeRange":[1,10]}]`))
		case "/api/v1/blocks":
			w.Write([]byte(`[{"height":971691,"timestamp":1760616000,"tx_count":3100,"size":1600000,"weight":3990000,"extras":{"medianFee":2.2,"totalFees":12000000}}]`))
		case "/api/v1/difficulty-adjustment":
			w.Write(diffRaw)
		case "/api/v1/mining/pools/1w":
			w.Write(poolsRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer memSrv.Close()

	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(cbStatsRaw)
	}))
	defer cbSrv.Close()

	origMempool := mempoolBaseURL
	origFees := mempoolFeesURL
	origStats := mempoolStatsURL
	origProj := mempoolProjectedURL
	origRecent := mempoolRecentURL
	origCBStats := btcCoinbaseStatsURL
	t.Cleanup(func() {
		mempoolBaseURL = origMempool
		mempoolFeesURL = origFees
		mempoolStatsURL = origStats
		mempoolProjectedURL = origProj
		mempoolRecentURL = origRecent
		btcCoinbaseStatsURL = origCBStats
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: memSrv.URL})
	btcCoinbaseStatsURL = cbSrv.URL + "/products/BTC-USD/stats"

	h := newTestBitcoinHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/live", nil)
	rec := httptest.NewRecorder()
	h.BitcoinLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var live BtcLiveData
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.PriceUSD == nil || *live.PriceUSD <= 0 {
		t.Errorf("PriceUSD = %v, want > 0", live.PriceUSD)
	}
	if live.TipHeight == nil || *live.TipHeight != 971691 {
		t.Errorf("TipHeight = %v, want 971691", live.TipHeight)
	}
	if live.Difficulty == nil || live.Difficulty.NextRetargetHeight <= 0 {
		t.Errorf("Difficulty = %+v", live.Difficulty)
	}
	if live.Halving.NextHeight == nil || *live.Halving.NextHeight != 1_050_000 || live.Halving.BlocksRemaining == nil || *live.Halving.BlocksRemaining != 78_309 {
		t.Errorf("Halving = %+v", live.Halving)
	}
	if live.Hashrate7dEHs == nil || *live.Hashrate7dEHs <= 0 {
		t.Errorf("Hashrate7dEHs = %v, want > 0", live.Hashrate7dEHs)
	}
}

func TestBitcoinLivePartial(t *testing.T) {
	pricesRaw, err := os.ReadFile("testdata/bitcoin/mempool_prices.json")
	if err != nil {
		t.Fatal(err)
	}
	memSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/prices":
			w.Write(pricesRaw)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer memSrv.Close()

	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer cbSrv.Close()

	origMempool := mempoolBaseURL
	origCBStats := btcCoinbaseStatsURL
	t.Cleanup(func() {
		applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: origMempool})
		btcCoinbaseStatsURL = origCBStats
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: memSrv.URL})
	btcCoinbaseStatsURL = cbSrv.URL

	h := newTestBitcoinHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/live", nil)
	rec := httptest.NewRecorder()
	h.BitcoinLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on partial failure, got %d", rec.Code)
	}
	var live BtcLiveData
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.PriceUSD == nil || *live.PriceUSD <= 0 {
		t.Errorf("expected mempool price fallback when coinbase is down, got %v", live.PriceUSD)
	}
	if live.Difficulty != nil {
		t.Errorf("expected nil Difficulty when mempool difficulty endpoint is 502")
	}
}

func stubAllBtcUpstreams(t *testing.T, url string) {
	t.Helper()
	origMempool := mempoolBaseURL
	origCBStats := btcCoinbaseStatsURL
	origCM := btcCoinMetricsBaseURL
	origCGTreas := btcCGTreasuriesURL
	origCGGlobal := btcCGGlobalURL
	origCGBtc := btcCGBitcoinURL
	origCGTick := btcCGTickersBaseURL
	origCGDeriv := btcCGDerivativesURL
	origFapi := btcBinanceFapiBaseURL
	origDapi := btcBinanceDapiBaseURL
	origOKX := btcOKXBaseURL
	origINTX := btcCoinbaseINTXPerpURL
	origCDE := btcCoinbaseCDEBIPURL
	origKraken := btcKrakenFuturesTickURL
	origHL := btcHyperliquidInfoURL
	origDeribit := btcDeribitBaseURL
	origCFTC := btcCFTCTFFBaseURL
	origFnG := btcAlternativeMeFnGURL
	origBitnodes := btcBitnodesLatestURL
	origDBIP := btcDBIPBaseURL
	t.Cleanup(func() {
		applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: origMempool})
		btcCoinbaseStatsURL = origCBStats
		btcCoinMetricsBaseURL = origCM
		btcCGTreasuriesURL = origCGTreas
		btcCGGlobalURL = origCGGlobal
		btcCGBitcoinURL = origCGBtc
		btcCGTickersBaseURL = origCGTick
		btcCGDerivativesURL = origCGDeriv
		btcBinanceFapiBaseURL = origFapi
		btcBinanceDapiBaseURL = origDapi
		btcOKXBaseURL = origOKX
		btcCoinbaseINTXPerpURL = origINTX
		btcCoinbaseCDEBIPURL = origCDE
		btcKrakenFuturesTickURL = origKraken
		btcHyperliquidInfoURL = origHL
		btcDeribitBaseURL = origDeribit
		btcCFTCTFFBaseURL = origCFTC
		btcAlternativeMeFnGURL = origFnG
		btcBitnodesLatestURL = origBitnodes
		btcDBIPBaseURL = origDBIP
		resetBtcCGRateLimitForTest()
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: url})
	btcCoinbaseStatsURL = url
	btcCoinMetricsBaseURL = url
	btcCGTreasuriesURL = url
	btcCGGlobalURL = url
	btcCGBitcoinURL = url
	btcCGTickersBaseURL = url
	btcCGDerivativesURL = url
	btcBinanceFapiBaseURL = url
	btcBinanceDapiBaseURL = url
	btcOKXBaseURL = url
	btcCoinbaseINTXPerpURL = url
	btcCoinbaseCDEBIPURL = url
	btcKrakenFuturesTickURL = url
	btcHyperliquidInfoURL = url
	btcDeribitBaseURL = url
	btcCFTCTFFBaseURL = url
	btcAlternativeMeFnGURL = url
	btcBitnodesLatestURL = url
	btcDBIPBaseURL = url
}

// btcBQActionRoute reports the POST-only BigQuery action routes (the loopback-
// and header-guarded estimate/run pair); every other Bitcoin route serves GET.
func btcBQActionRoute(pattern string) bool {
	return strings.HasPrefix(pattern, "/api/opendata/bitcoin/bigquery/")
}

// panicBtcQuerySource fails the test on any BigQuery call (Job A and Job B).
type panicBtcQuerySource struct {
	t *testing.T
}

func (p panicBtcQuerySource) fail(call string) {
	p.t.Fatalf("unexpected BigQuery %s call from HTTP handler", call)
}

func (p panicBtcQuerySource) ReadyAt(context.Context, time.Time) (bool, error) {
	p.fail("ReadyAt")
	return false, errors.New("forbidden")
}

func (p panicBtcQuerySource) DryRunBlocks(context.Context, civil.Date, civil.Date) (int64, error) {
	p.fail("DryRunBlocks")
	return 0, errors.New("forbidden")
}

func (p panicBtcQuerySource) DryRunRollup(context.Context, civil.Date, civil.Date) (int64, error) {
	p.fail("DryRunRollup")
	return 0, errors.New("forbidden")
}

func (p panicBtcQuerySource) FetchBlocks(context.Context, civil.Date, civil.Date, int64) ([]btcBlockRow, int64, error) {
	p.fail("FetchBlocks")
	return nil, 0, errors.New("forbidden")
}

func (p panicBtcQuerySource) FetchRollup(context.Context, civil.Date, civil.Date, int64) ([]btcRollupRawRow, int64, error) {
	p.fail("FetchRollup")
	return nil, 0, errors.New("forbidden")
}

func (p panicBtcQuerySource) DryRunCoinAge(context.Context, civil.Date, civil.Date) (int64, error) {
	p.fail("DryRunCoinAge")
	return 0, errors.New("forbidden")
}

func (p panicBtcQuerySource) FetchCoinAge(context.Context, civil.Date, civil.Date, int64) ([]btcCoinAgeRawRow, int64, error) {
	p.fail("FetchCoinAge")
	return nil, 0, errors.New("forbidden")
}

func TestBitcoinHandlersNeverQueryBigQuery(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failSrv.Close()
	stubAllBtcUpstreams(t, failSrv.URL)

	h := newTestBitcoinHandler(t)
	// h.bq is explicitly nil: any attempt to dereference h.bq panics and fails the test.
	h.bq = nil

	for _, rt := range bitcoinRoutes(h) {
		t.Run(rt.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, rt.Pattern, nil)
			rec := httptest.NewRecorder()
			rt.Handler(rec, req)
			want := http.StatusOK
			if btcBQActionRoute(rt.Pattern) {
				want = http.StatusMethodNotAllowed
			}
			if rec.Code != want {
				t.Errorf("GET %s status = %d, want %d", rt.Pattern, rec.Code, want)
			}
		})
	}
}

func TestBitcoinHandlersCannotStartJobs(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failSrv.Close()
	stubAllBtcUpstreams(t, failSrv.URL)

	h := newTestBitcoinHandler(t)
	h.bq = nil
	h.btc.starter = panicBaselineStarter{t: t}
	src := panicBtcQuerySource{t: t}
	h.btc.bqRunner = newBtcBQRunner(h.btc.settings, openTestRiskStore(t), src, src, nil, h.btc.now)

	// GET can never start a job: read routes answer 200, and the BigQuery
	// action routes reject the method before touching the runner.
	for _, rt := range bitcoinRoutes(h) {
		t.Run("GET "+rt.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, rt.Pattern, nil)
			rec := httptest.NewRecorder()
			rt.Handler(rec, req)
			want := http.StatusOK
			if btcBQActionRoute(rt.Pattern) {
				want = http.StatusMethodNotAllowed
			}
			if rec.Code != want {
				t.Errorf("GET %s status = %d, want %d", rt.Pattern, rec.Code, want)
			}
		})
	}

	// Baseline POST with default allow_ui: false must return 403 without touching starter.
	postReq := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(`{"action":"dry_run"}`))
	postRec := httptest.NewRecorder()
	h.BitcoinBaseline(postRec, postReq)
	if postRec.Code != http.StatusForbidden {
		t.Errorf("POST /onchain/baseline with allow_ui:false = %d, want 403", postRec.Code)
	}

	// BigQuery actions from another device (even claiming loopback through
	// X-Forwarded-For), or without the custom header, never reach a query source.
	for _, tc := range []struct {
		target string
		fn     http.HandlerFunc
	}{
		{"/api/opendata/bitcoin/bigquery/estimate?job=btc_onchain", h.BitcoinBQEstimate},
		{"/api/opendata/bitcoin/bigquery/run?job=btc_coinage", h.BitcoinBQRun},
	} {
		lan := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(`{"confirmed_estimate_bytes": 1}`))
		lan.RemoteAddr = "192.168.1.25:54321"
		lan.Header.Set("X-Forwarded-For", "127.0.0.1")
		lan.Header.Set("Content-Type", "application/json")
		lan.Header.Set("X-BigLens-Action", "bitcoin-bq")
		lanRec := httptest.NewRecorder()
		tc.fn(lanRec, lan)
		if lanRec.Code != http.StatusForbidden {
			t.Errorf("POST %s from LAN = %d, want 403", tc.target, lanRec.Code)
		}
		bare := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(`{"confirmed_estimate_bytes": 1}`))
		bare.Host = "localhost:1983"
		bare.RemoteAddr = "127.0.0.1:1234"
		bare.Header.Set("Content-Type", "application/json")
		bareRec := httptest.NewRecorder()
		tc.fn(bareRec, bare)
		if bareRec.Code != http.StatusBadRequest {
			t.Errorf("POST %s without X-BigLens-Action = %d, want 400", tc.target, bareRec.Code)
		}
	}
}

func TestFoldMempoolStats(t *testing.T) {
	vsizes := make([]float64, 39)
	vsizes[0] = 39_158_473 // 0 sat/vB -> band_0_1_vmb = 39.158473
	vsizes[1] = 884_625    // 1 sat/vB -> band_1_2_vmb = 0.884625
	vsizes[2] = 500_000    // 2 sat/vB -> band_2_5_vmb
	vsizes[4] = 300_000    // 4 sat/vB -> band_2_5_vmb (total 0.8)
	vsizes[5] = 200_000    // 5 sat/vB -> band_5_10_vmb
	vsizes[8] = 400_000    // 10 sat/vB -> band_10_50_vmb
	vsizes[14] = 100_000   // 50 sat/vB -> band_50_plus_vmb

	pt, err := foldMempoolStats("2026-10-16T00:00:00Z", vsizes)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pt.Band0To1VMB-39.158473) > 1e-6 {
		t.Errorf("Band0To1VMB = %f, want 39.158473", pt.Band0To1VMB)
	}
	if math.Abs(pt.Band1To2VMB-0.884625) > 1e-6 {
		t.Errorf("Band1To2VMB = %f, want 0.884625", pt.Band1To2VMB)
	}
	if math.Abs(pt.Band2To5VMB-0.8) > 1e-6 {
		t.Errorf("Band2To5VMB = %f, want 0.8", pt.Band2To5VMB)
	}
	if math.Abs(pt.Band5To10VMB-0.2) > 1e-6 {
		t.Errorf("Band5To10VMB = %f, want 0.2", pt.Band5To10VMB)
	}
	if math.Abs(pt.Band10To50VMB-0.4) > 1e-6 {
		t.Errorf("Band10To50VMB = %f, want 0.4", pt.Band10To50VMB)
	}
	if math.Abs(pt.Band50PlusVMB-0.1) > 1e-6 {
		t.Errorf("Band50PlusVMB = %f, want 0.1", pt.Band50PlusVMB)
	}
}

func TestFoldMempoolStatsLen(t *testing.T) {
	if _, err := foldMempoolStats("2026-10-16T00:00:00Z", make([]float64, 38)); err == nil {
		t.Fatal("expected error for 38-element vsizes slice")
	}
}

func TestTxCosts(t *testing.T) {
	price := 80_000.0
	costs := computeBtcTxTemplateCosts(10, 5, 2, &price)
	if len(costs) != 6 {
		t.Fatalf("len(costs) = %d, want 6", len(costs))
	}
	// Standard 140 vB at 10 sat/vB = 1,400 sats = $1.12; consolidation 3,442 vB at 2 sat/vB = 6,884 sats = $5.5072
	std := costs[0]
	if std.Vbytes != 140 || std.NextBlockSats != 1400 || std.NextBlockUSD == nil || math.Abs(*std.NextBlockUSD-1.12) > 1e-4 {
		t.Errorf("standard template = %+v", std)
	}
	cons := costs[5]
	if cons.Vbytes != 3442 || cons.EconomySats != 6884 || cons.EconomyUSD == nil || math.Abs(*cons.EconomyUSD-5.5072) > 1e-4 {
		t.Errorf("consolidation template = %+v", cons)
	}
}

func TestConsolidationSignal(t *testing.T) {
	openSig := consolidationSignal(2.0, 1.5)
	if !openSig.Open {
		t.Errorf("expected open signal for economy=2.0, blocks=1.5: %+v", openSig)
	}
	closedFee := consolidationSignal(8.0, 1.5)
	if closedFee.Open {
		t.Errorf("expected closed signal when economy=8.0 > 5.0: %+v", closedFee)
	}
	closedBacklog := consolidationSignal(2.0, 5.0)
	if closedBacklog.Open {
		t.Errorf("expected closed signal when backlog=5.0 > 3.0: %+v", closedBacklog)
	}
}

func TestBitcoinFeesPeriods(t *testing.T) {
	statsRaw, err := os.ReadFile("testdata/bitcoin/mempool_statistics_24h.json")
	if err != nil {
		t.Fatal(err)
	}
	ratesRaw, err := os.ReadFile("testdata/bitcoin/mempool_block_fee_rates_1w.json")
	if err != nil {
		t.Fatal(err)
	}
	cbStatsRaw, err := os.ReadFile("testdata/bitcoin/coinbase_stats.json")
	if err != nil {
		t.Fatal(err)
	}

	memSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/fees/precise":
			w.Write([]byte(`{"fastestFee":2.5,"halfHourFee":2.0,"hourFee":1.5,"economyFee":1.1,"minimumFee":1.0}`))
		case r.URL.Path == "/api/mempool":
			w.Write([]byte(`{"count":42000,"vsize":2000000,"total_fee":50000000,"fee_histogram":[[2,1000000]]}`))
		case r.URL.Path == "/api/v1/fees/mempool-blocks":
			w.Write([]byte(`[{"blockVSize":1000000,"nTx":2500,"totalFees":10000000,"medianFee":2.5,"feeRange":[1,10]}]`))
		case r.URL.Path == "/api/v1/blocks":
			w.Write([]byte(`[{"height":971691,"timestamp":1760616000,"tx_count":3100,"size":1600000,"weight":3990000,"extras":{"medianFee":2.2,"totalFees":12000000}}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/statistics/"):
			w.Write(statsRaw)
		case strings.HasPrefix(r.URL.Path, "/api/v1/mining/blocks/fee-rates/"):
			w.Write(ratesRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer memSrv.Close()

	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(cbStatsRaw)
	}))
	defer cbSrv.Close()

	origMempool := mempoolBaseURL
	origCBStats := btcCoinbaseStatsURL
	t.Cleanup(func() {
		applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: origMempool})
		btcCoinbaseStatsURL = origCBStats
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: memSrv.URL})
	btcCoinbaseStatsURL = cbSrv.URL

	h := newTestBitcoinHandler(t)
	for _, p := range []string{"24h", "1w", "1m"} {
		req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/fees?period="+p, nil)
		rec := httptest.NewRecorder()
		h.BitcoinFees(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("period %s status = %d", p, rec.Code)
		}
		var out BtcFeesData
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Period != p || len(out.Templates) != 6 || len(out.MempoolHistory) == 0 || len(out.BlockFeeRates) == 0 {
			t.Errorf("period %s response incomplete: period=%s templates=%d hist=%d rates=%d",
				p, out.Period, len(out.Templates), len(out.MempoolHistory), len(out.BlockFeeRates))
		}
	}
}

func TestBitcoinOnchainReadOnly(t *testing.T) {
	ctx := context.Background()
	h := newTestBitcoinHandler(t)
	h.bq = nil // must never touch BigQuery
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	h.btc.now = func() time.Time { return now }
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)

	payload := btcRollupPayload{
		CoverageStart: 971548,
		Blocks: []btcBlockRow{
			{Height: 971548, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
		},
		Days: []BtcOnchainDay{
			{Date: "2026-10-14", Blocks: 144, Txs: 450000, FeesSats: 303128842},
		},
	}
	if err := store.applyBtcRollupBatch(ctx, payload, "2026-10-14", "2026-10-14", now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/onchain?days=30", nil)
	rec := httptest.NewRecorder()
	h.BitcoinOnchain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out BtcOnchainData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Date != "2026-10-14" || out.Status.SyncedThrough != "2026-10-14" {
		t.Errorf("onchain response = %+v", out)
	}
}

func TestBitcoinOnchainJobsOff(t *testing.T) {
	h := newTestBitcoinHandler(t)
	h.bq = nil
	store := openTestRiskStore(t)
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/onchain?days=90", nil)
	rec := httptest.NewRecorder()
	h.BitcoinOnchain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out BtcOnchainData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status.State != "off" || out.Status.Jobs || len(out.Rows) != 0 {
		t.Errorf("jobs off response = %+v, want state=off jobs=false rows=0", out)
	}
}

func TestBitcoinOnchainHoldersReadOnly(t *testing.T) {
	ctx := context.Background()
	h := newTestBitcoinHandler(t)
	h.bq = nil // must never touch BigQuery
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	h.btc.now = func() time.Time { return now }
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)

	seedMaintenanceChain(t, store, now)
	settings := h.btc.settings
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	if _, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil); err != nil {
		t.Fatal(err)
	}
	maint := newBtcHolderMaintenance(store, h.btc.prices, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/onchain/holders?days=30", nil)
	rec := httptest.NewRecorder()
	h.BitcoinOnchainHolders(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out BtcOnchainHoldersData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Coinage) != 2 || len(out.Holders) != 3 || !out.Baseline.Available || out.Baseline.Pending {
		t.Fatalf("onchain holders response = %+v", out)
	}
}

func TestBitcoinBaselineEndpoint(t *testing.T) {
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	h := newTestBitcoinHandler(t)
	h.bq = nil
	h.btc.now = func() time.Time { return now }
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)

	// 1. Default allow_ui: false -> POST returns 403.
	req403 := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(`{"action":"dry_run"}`))
	rec403 := httptest.NewRecorder()
	h.BitcoinBaseline(rec403, req403)
	if rec403.Code != http.StatusForbidden {
		t.Fatalf("POST with allow_ui:false = %d, want 403", rec403.Code)
	}

	// 2. Enable allow_ui + jobs and wire a fake starter.
	h.btc.settings.Jobs = true
	h.btc.settings.BaselineAllowUI = true
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)
	fakeSrc := &fakeBaselineSource{
		dryBytes: 687 * (1 << 30),
		billed:   687 * (1 << 30),
		rows:     []btcUTXOBaselineRow{{CreateDay: "2026-10-14", Sats: 2_000_000_000_000_000, UTXOs: 100}},
	}
	h.btc.starter = newBtcBaselineRunner(store, h.btc.settings, fakeSrc, func() time.Time { return now })

	// Dry run mints a token.
	dryReq := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(`{"action":"dry_run"}`))
	dryRec := httptest.NewRecorder()
	h.BitcoinBaseline(dryRec, dryReq)
	if dryRec.Code != http.StatusOK {
		t.Fatalf("POST dry_run = %d, body = %s", dryRec.Code, dryRec.Body.String())
	}
	var plan btcBaselinePlan
	if err := json.Unmarshal(dryRec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Token == "" {
		t.Fatalf("expected non-empty token from dry_run")
	}

	// Confirm with the token builds the baseline and returns status.
	confBody := `{"action":"confirm","token":"` + plan.Token + `"}`
	confReq := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(confBody))
	confRec := httptest.NewRecorder()
	h.BitcoinBaseline(confRec, confReq)
	if confRec.Code != http.StatusOK {
		t.Fatalf("POST confirm = %d, body = %s", confRec.Code, confRec.Body.String())
	}
	var st BtcBaselineStatus
	if err := json.Unmarshal(confRec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Available || st.Generation != 1 || !st.Pending {
		t.Fatalf("POST confirm status = %+v", st)
	}

	// Reusing the same token returns 400 Bad Request.
	reuseReq := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(confBody))
	reuseRec := httptest.NewRecorder()
	h.BitcoinBaseline(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusBadRequest {
		t.Fatalf("POST confirm reuse = %d, want 400", reuseRec.Code)
	}
}

func TestWriteBtcBaselineErrorHidesInternal500Details(t *testing.T) {
	rec := httptest.NewRecorder()
	secretErr := errors.New("sqlite open /var/lib/biglens/secret/risk.db: permission denied (project=internal-prod-123)")
	writeBtcBaselineError(rec, secretErr)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/var/lib/biglens/secret/risk.db") || strings.Contains(body, "internal-prod-123") {
		t.Fatalf("500 response leaked internal error details: %s", strings.TrimSpace(body))
	}
}
