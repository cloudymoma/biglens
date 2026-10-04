package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gasLiveTestRaw() (*btcLiveRaw, *tronLiveRaw) {
	return &btcLiveRaw{
			Fees:    btcPreciseFees{Fastest: 1.5, HalfHour: 0.9, Economy: 0.2, Minimum: 0.1},
			Mempool: btcMempoolStats{Count: 10, Vsize: 2_000_000, TotalFee: 50_000, FeeHistogram: [][2]float64{{1.2, 2_000_000}}},
		},
		&tronLiveRaw{EnergySun: 100, BandwidthSun: 1000, EnergySince: "2025-08-29T12:00:00Z"}
}

// mempool.space failing must not take the TRON calculator down with it.
func TestBuildGasLivePartialFailure(t *testing.T) {
	_, tron := gasLiveTestRaw()
	trx := 0.3346
	d := buildGasLive(time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC), nil, errors.New("mempool.space fetch: upstream status 503"), tron, nil, testCalibration(), nil, nil, &trx)

	if d.AsOf != "2026-10-04T08:00:00Z" {
		t.Errorf("as_of = %q", d.AsOf)
	}
	if d.BTC != nil || d.BTCError == "" {
		t.Errorf("btc = %+v, btc_error = %q; want nil and the upstream error", d.BTC, d.BTCError)
	}
	if d.Tron == nil || d.TronError != "" || d.Tron.Costs[0].BurnUSD == nil {
		t.Errorf("tron = %+v, tron_error = %q; want a priced calculator", d.Tron, d.TronError)
	}
}

func TestBuildGasLiveTronFailure(t *testing.T) {
	btc, _ := gasLiveTestRaw()
	d := buildGasLive(time.Now(), btc, nil, nil, errors.New("tron prices fetch: upstream status 429"), testCalibration(), nil, nil, nil)
	if d.BTC == nil || d.BTCError != "" || d.Tron != nil || d.TronError == "" {
		t.Errorf("btc=%v btc_error=%q tron=%v tron_error=%q; want only TRON to fail", d.BTC, d.BTCError, d.Tron, d.TronError)
	}
}

// Coinbase down: every native value still renders, USD stays null.
func TestBuildGasLiveWithoutSpot(t *testing.T) {
	btc, tron := gasLiveTestRaw()
	d := buildGasLive(time.Now(), btc, nil, tron, nil, testCalibration(), nil, nil, nil)
	if d.BTC == nil || d.Tron == nil || d.BTCError != "" || d.TronError != "" {
		t.Fatalf("missing spot must not be an error: %+v", d)
	}
	for _, tier := range d.BTC.Tiers {
		if tier.USD != nil {
			t.Errorf("btc %s USD = %v, want nil", tier.Label, *tier.USD)
		}
	}
	for _, c := range d.Tron.Costs {
		if c.BurnUSD != nil || c.BurnTRX == 0 {
			t.Errorf("tron %s = %+v, want TRX cost without USD", c.Label, c)
		}
	}
}

// Each chain is cached on its own: a healthy chain is not re-queried within
// its TTL, while a failing one is retried on the next request.
func TestGasL2QuotesCachePerChain(t *testing.T) {
	var okHits, badHits atomic.Int32
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3b9aca00"}`))
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bad.Close()
	old := l2Chains
	l2Chains = []l2ChainConfig{
		{ID: "eth", Name: "Ethereum", Kind: l2KindL1, RPCs: []string{ok.URL}, ChainID: 1},
		{ID: "arb", Name: "Arbitrum One", Kind: l2KindL1, RPCs: []string{bad.URL}, ChainID: 1},
	}
	defer func() { l2Chains = old }()

	h := &APIHandler{cache: NewCache(time.Minute)}
	r := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/gas-live", nil)
	for i := 0; i < 2; i++ {
		quotes, errs := h.gasL2Quotes(r, l2ActionsFor(nil))
		if quotes["eth"] == nil || errs["arb"] == nil {
			t.Fatalf("round %d: quotes=%v errs=%v; want eth priced and arb failing", i, quotes, errs)
		}
	}
	if okHits.Load() != 1 {
		t.Errorf("healthy chain queried %d times, want 1 (cached)", okHits.Load())
	}
	if badHits.Load() != 2 {
		t.Errorf("failing chain queried %d times, want 2 (errors are not cached)", badHits.Load())
	}
}

// Calibration down: TRON says why it has no costs, BTC is untouched, and the
// error is surfaced — no fallback to stale constants.
func TestBuildGasLiveWithoutCalibration(t *testing.T) {
	btc, tron := gasLiveTestRaw()
	d := buildGasLive(time.Now(), btc, nil, tron, nil, nil, errors.New("usdt energy modes: quota"), nil, nil)
	if d.BTC == nil || d.BTCError != "" {
		t.Errorf("btc must be unaffected: %+v / %q", d.BTC, d.BTCError)
	}
	if d.Tron != nil || !strings.Contains(d.TronError, "transfer profile") {
		t.Errorf("tron = %+v, tron_error = %q; want an unavailable profile", d.Tron, d.TronError)
	}
	if d.Calibration != nil || d.CalibrationError == "" {
		t.Errorf("calibration = %+v, error = %q", d.Calibration, d.CalibrationError)
	}
}

// A new day's calibrated gas must not be served a quote built for the old one.
func TestGasL2QuotesKeyIncludesGas(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3b9aca00"}`))
	}))
	defer srv.Close()
	old := l2Chains
	l2Chains = []l2ChainConfig{{ID: "eth", Name: "Ethereum", Kind: l2KindL1, RPCs: []string{srv.URL}, ChainID: 1, USDC: ethUSDCAddress}}
	defer func() { l2Chains = old }()

	h := &APIHandler{cache: NewCache(time.Minute)}
	r := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/gas-live", nil)
	day1, day2 := testCalibration(), testCalibration()
	day2.USDCTransferGas = 50000
	h.gasL2Quotes(r, l2ActionsFor(day1))
	h.gasL2Quotes(r, l2ActionsFor(day1))
	h.gasL2Quotes(r, l2ActionsFor(day2))
	if hits.Load() != 2 {
		t.Errorf("upstream hit %d times, want 2 (same gas cached, new gas re-quoted)", hits.Load())
	}
}
