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
	h.btc.prices = newBtcPriceSyncer(openTestRiskStore(t), h.btc.settings, func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	})
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
	if len(meta.Providers) != 6 {
		t.Errorf("Providers len = %d, want 6 (M1–M3 scope)", len(meta.Providers))
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
	if live.TipHeight != 971691 {
		t.Errorf("TipHeight = %d, want 971691", live.TipHeight)
	}
	if live.Difficulty == nil || live.Difficulty.NextRetargetHeight <= 0 {
		t.Errorf("Difficulty = %+v", live.Difficulty)
	}
	if live.Halving.NextHeight != 1_050_000 || live.Halving.BlocksRemaining != 78_309 {
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

func TestBitcoinHandlersNeverQueryBigQuery(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failSrv.Close()

	origMempool := mempoolBaseURL
	origCBStats := btcCoinbaseStatsURL
	t.Cleanup(func() {
		applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: origMempool})
		btcCoinbaseStatsURL = origCBStats
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: failSrv.URL})
	btcCoinbaseStatsURL = failSrv.URL

	h := newTestBitcoinHandler(t)
	// h.bq is explicitly nil: any attempt to dereference h.bq panics and fails the test.
	h.bq = nil

	for _, rt := range bitcoinRoutes(h) {
		t.Run(rt.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, rt.Pattern, nil)
			rec := httptest.NewRecorder()
			rt.Handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want 200", rt.Pattern, rec.Code)
			}
		})
	}
}

func TestBitcoinHandlersCannotStartJobs(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failSrv.Close()

	origMempool := mempoolBaseURL
	origCBStats := btcCoinbaseStatsURL
	t.Cleanup(func() {
		applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: origMempool})
		btcCoinbaseStatsURL = origCBStats
	})
	applyCryptoGasConfig(CryptoGasConfig{MempoolBaseURL: failSrv.URL})
	btcCoinbaseStatsURL = failSrv.URL

	h := newTestBitcoinHandler(t)
	h.bq = nil
	h.btc.starter = panicBaselineStarter{t: t}

	for _, rt := range bitcoinRoutes(h) {
		t.Run("GET "+rt.Pattern, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, rt.Pattern, nil)
			rec := httptest.NewRecorder()
			rt.Handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s status = %d", rt.Pattern, rec.Code)
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
}
