package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A failed upstream must be retried on the next request, not served from
// cache for an hour; a success must not re-run the (billed) query.
func TestCachedFetchDoesNotCacheErrors(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	calls := 0
	failing := func() (any, error) { calls++; return nil, errors.New("bq unavailable") }
	for i := 0; i < 2; i++ {
		if _, err := h.cachedFetch("k:fail", time.Hour, failing); err == nil {
			t.Fatal("expected the fetch error to propagate")
		}
	}
	if calls != 2 {
		t.Errorf("failing fetch ran %d times, want 2 (errors must not be cached)", calls)
	}
}

func TestCachedFetchCachesSuccess(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	calls := 0
	ok := func() (any, error) { calls++; return []GasHourRow{{HourUTC: "x"}}, nil }
	for i := 0; i < 2; i++ {
		v, err := h.cachedFetch("k:ok", time.Hour, ok)
		if err != nil || len(v.([]GasHourRow)) != 1 {
			t.Fatalf("got %v, %v", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("successful fetch ran %d times, want 1", calls)
	}
}

func gasTestWindow() (time.Time, time.Time) {
	end := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	return end.Add(-72 * time.Hour), end
}

func gasTestSeries() map[string]gasSeriesResult {
	m := map[string]gasSeriesResult{}
	for _, c := range gasChainOrder {
		m[c] = gasSeriesResult{rows: []GasHourRow{{HourUTC: "2026-10-04 09:00", Primary: nf(2)}}}
	}
	return m
}

// One chain failing (e.g. Solana's us-central1 dataset) must not blank the
// other six; the failed chain still gets 72 empty buckets and its error.
func TestBuildGasPulsePartialFailure(t *testing.T) {
	start, end := gasTestWindow()
	series := gasTestSeries()
	series[gasChainSol] = gasSeriesResult{err: errors.New("sol 72h query: access denied")}

	d := buildGasPulse(start, end, series, map[string]GasAllTime{}, nil, nil, errors.New("unused"))

	if d.WindowStart != "2026-10-01 10:00" || d.WindowEnd != "2026-10-04 10:00" {
		t.Errorf("window = %s .. %s", d.WindowStart, d.WindowEnd)
	}
	if len(d.Chains) != len(gasChainOrder) {
		t.Fatalf("got %d chains, want %d", len(d.Chains), len(gasChainOrder))
	}
	for i, c := range d.Chains {
		if c.Meta.ID != gasChainOrder[i] {
			t.Errorf("chain %d = %s, want %s (selector order)", i, c.Meta.ID, gasChainOrder[i])
		}
		if len(c.Hours) != 72 {
			t.Errorf("%s has %d buckets, want 72", c.Meta.ID, len(c.Hours))
		}
		if c.Meta.ID == gasChainSol {
			if c.Error == "" || c.Stats.Samples != 0 {
				t.Errorf("sol: error=%q samples=%d, want the error and no samples", c.Error, c.Stats.Samples)
			}
			continue
		}
		if c.Error != "" || c.Stats.Samples != 1 || c.Stats.Latest != 2 {
			t.Errorf("%s: error=%q stats=%+v, want a clean chain with one sample", c.Meta.ID, c.Error, c.Stats)
		}
	}
}

// Record cards: EVM chains share one BQ result, TRON has its own source, BTC
// and Solana have none — and a failure only marks the cards it feeds.
func TestBuildGasPulseAllTime(t *testing.T) {
	start, end := gasTestWindow()
	eth := GasAllTime{Unit: "gwei", AthValue: 8629.05}
	tron := &GasAllTime{Unit: "sun", AthValue: 420}

	d := buildGasPulse(start, end, gasTestSeries(), map[string]GasAllTime{"eth": eth}, nil, tron, nil)
	by := map[string]GasPulseChain{}
	for _, c := range d.Chains {
		by[c.Meta.ID] = c
	}
	if a := by[gasChainETH].AllTime; a == nil || a.AthValue != 8629.05 {
		t.Errorf("eth all_time = %+v", a)
	}
	if a := by[gasChainTron].AllTime; a == nil || a.AthValue != 420 {
		t.Errorf("tron all_time = %+v", a)
	}
	for _, id := range []string{gasChainBTC, gasChainSol} {
		if by[id].AllTime != nil || by[id].AllTimeError != "" {
			t.Errorf("%s must have no record card and no error, got %+v / %q", id, by[id].AllTime, by[id].AllTimeError)
		}
	}
	if by[gasChainArb].AllTimeError == "" {
		t.Error("arb missing from the BQ result must say its record is unavailable")
	}
}

func TestBuildGasPulseAllTimeQueryError(t *testing.T) {
	start, end := gasTestWindow()
	d := buildGasPulse(start, end, gasTestSeries(), nil, errors.New("quota exceeded"), &GasAllTime{Unit: "sun"}, nil)
	for _, c := range d.Chains {
		switch c.Meta.ID {
		case gasChainETH, gasChainArb, gasChainOP, gasChainPoly:
			if c.AllTime != nil || c.AllTimeError == "" {
				t.Errorf("%s: all_time=%+v err=%q, want nil and an error", c.Meta.ID, c.AllTime, c.AllTimeError)
			}
		case gasChainTron:
			if c.AllTime == nil || c.AllTimeError != "" {
				t.Errorf("tron must be unaffected by the BQ record failure, got %+v / %q", c.AllTime, c.AllTimeError)
			}
		}
	}
}

func TestBuildGasPulseTronError(t *testing.T) {
	start, end := gasTestWindow()
	d := buildGasPulse(start, end, gasTestSeries(), map[string]GasAllTime{"eth": {}}, nil, nil, errors.New("upstream status 429"))
	for _, c := range d.Chains {
		if c.Meta.ID == gasChainTron && (c.AllTime != nil || c.AllTimeError == "" || c.Error != "") {
			t.Errorf("tron: all_time=%+v all_time_error=%q error=%q; want only the record card to fail", c.AllTime, c.AllTimeError, c.Error)
		}
	}
}

// A client that disconnects during a cold load must not cancel the shared
// fetch: singleflight hands the result to every waiter, and a cancelled
// (already billed) BigQuery job would otherwise be re-run on the next request.
func TestGasFetchSurvivesClientDisconnect(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Write([]byte(`{"prices":"` + tronPricesFixture + `"}`))
	}))
	defer srv.Close()
	old := tronEnergyPricesURL
	tronEnergyPricesURL = srv.URL
	defer func() { tronEnergyPricesURL = old }()

	h := &APIHandler{cache: NewCache(time.Minute)}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/gas-pulse", nil).WithContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.gasTronAllTime(r)
		done <- err
	}()

	cancel() // the first client goes away while upstream is still working
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("shared fetch failed after the client disconnected: %v", err)
	}
	if _, ok := h.cache.Get(gasTronKey); !ok {
		t.Error("result was not cached after the client disconnected")
	}
}
