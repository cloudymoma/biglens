package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// useFreshGasAllTimeState swaps in a cold-start tracker for one test.
func useFreshGasAllTimeState(t *testing.T) {
	t.Helper()
	old := gasAllTimeState
	gasAllTimeState = newGasAllTimeTracker()
	t.Cleanup(func() { gasAllTimeState = old })
}

// The freshness contract of the BigQuery-derived record cards: every EVM card
// says what its records are verified through and whether the last refresh
// failed; TRON's TronGrid card carries none of it.
func TestBuildGasPulseAllTimeFreshnessJSON(t *testing.T) {
	start, end := gasTestWindow()
	tr := newGasAllTimeTracker()
	tr.markFailed()
	allTime := tr.current(time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC))
	d := buildGasPulse(start, end, gasTestSeries(), allTime, nil, &GasAllTime{Unit: "sun", AthValue: 420}, nil)

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Chains []struct {
			Meta struct {
				ID string `json:"id"`
			} `json:"meta"`
			AllTime map[string]any `json:"all_time"`
		} `json:"chains"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	freshKeys := []string{"records_through", "records_stale", "refresh_failed", "refresh_note"}
	for _, c := range out.Chains {
		switch c.Meta.ID {
		case gasChainETH, gasChainArb, gasChainOP, gasChainPoly:
			a := c.AllTime
			if a["records_through"] != "2026-01-01T00:00:00Z" || a["records_stale"] != true ||
				a["refresh_failed"] != true || a["refresh_note"] != gasAllTimeNoteFailed {
				t.Errorf("%s all_time freshness = %v, want through 2026-01-01T00:00:00Z, stale, failed, note %q",
					c.Meta.ID, a, gasAllTimeNoteFailed)
			}
		case gasChainTron:
			for _, k := range freshKeys {
				if _, ok := c.AllTime[k]; ok {
					t.Errorf("tron all_time must not carry %q (not a BigQuery record): %v", k, c.AllTime)
				}
			}
		}
	}
}

// A failed background refresh must reach the client as metadata while the
// cards keep serving the last verified records (here: the seeds); the raw
// BigQuery error stays in the server log, and the failure back-off keeps the
// next request from re-running the billed scan.
func TestGasAllTimeRefreshFailureIsSurfaced(t *testing.T) {
	useFreshGasAllTimeState(t)
	const rawErr = "Syntax error: Unexpected keyword SELECT at [3:5] in `bigquery-public-data.crypto_ethereum.blocks`"
	fake := &fakeBigQuery{status: http.StatusBadRequest, errMsg: rawErr}
	h := newFakeBQHandler(t, fake)
	r := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/gas-pulse", nil)

	first, err := h.gasAllTime(r)
	if err != nil {
		t.Fatalf("gasAllTime must never fail the response: %v", err)
	}
	if eth := first[gasChainETH]; eth.RefreshFailed || !eth.RecordsStale || eth.RefreshNote != gasAllTimeNoteRefreshing {
		t.Errorf("first response = %+v, want the stale baseline with a pending refresh", eth)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := h.cache.Get(gasAllTimeKey + ":failed"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background refresh never recorded its failure")
		}
		time.Sleep(10 * time.Millisecond)
	}

	m, err := h.gasAllTime(r)
	if err != nil {
		t.Fatalf("gasAllTime must never fail the response: %v", err)
	}
	seedThrough := gasAllTimeSeedTime.UTC().Format(time.RFC3339)
	for _, id := range []string{gasChainETH, gasChainArb, gasChainOP, gasChainPoly} {
		at, ok := m[id]
		if !ok {
			t.Fatalf("%s card missing after a failed refresh", id)
		}
		if !at.RefreshFailed || at.RefreshNote != gasAllTimeNoteFailed || at.RecordsThrough != seedThrough {
			t.Errorf("%s = %+v, want refresh_failed, note %q, through %s", id, at, gasAllTimeNoteFailed, seedThrough)
		}
		if want := float64(gasAllTimeSeeds[id].Ath.Fee) / 1e9; at.AthValue != want {
			t.Errorf("%s ath = %v, want the last verified value %v", id, at.AthValue, want)
		}
	}
	body, _ := json.Marshal(m)
	for _, leak := range []string{"Syntax error", "bigquery-public-data", "SELECT"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("response leaks the raw BigQuery error (%q): %s", leak, body)
		}
	}
	if n := len(fake.sent()); n != 1 {
		t.Errorf("all-time scan ran %d times, want 1 (the failure back-off must hold)", n)
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

// An expensive fetch that fails (calibration: ~1.3 GB of BigQuery) must not
// re-run on every 30s poll; the failure is remembered for retryAfter.
func TestCachedFetchOrBackoffThrottlesFailures(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	calls := 0
	failing := func() (any, error) { calls++; return nil, errors.New("tron prices fetch: upstream status 429") }
	for i := 0; i < 3; i++ {
		if _, err := h.cachedFetchOrBackoff("k:cal", time.Hour, 30*time.Millisecond, failing); err == nil {
			t.Fatal("expected the failure to propagate")
		}
	}
	if calls != 1 {
		t.Errorf("failing fetch ran %d times within the back-off window, want 1", calls)
	}
	time.Sleep(50 * time.Millisecond)
	h.cachedFetchOrBackoff("k:cal", time.Hour, 30*time.Millisecond, failing)
	if calls != 2 {
		t.Errorf("failing fetch ran %d times after the window, want 2 (retried)", calls)
	}
}

func TestCachedFetchOrBackoffCachesSuccess(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	calls := 0
	ok := func() (any, error) { calls++; return "v", nil }
	for i := 0; i < 2; i++ {
		if v, err := h.cachedFetchOrBackoff("k:ok", time.Hour, time.Minute, ok); err != nil || v != "v" {
			t.Fatalf("got %v, %v", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("successful fetch ran %d times, want 1", calls)
	}
}

func TestMergeRollingGasHours(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	existing := map[string]GasHourRow{
		"2026-10-01 09:00": {HourUTC: "2026-10-01 09:00", Primary: nf(1)}, // outside window: pruned
		"2026-10-01 10:00": {HourUTC: "2026-10-01 10:00", Primary: nf(2)},
		"2026-10-04 08:00": {HourUTC: "2026-10-04 08:00", Primary: nf(3)}, // overlapped hour: overwritten
	}
	delta := []GasHourRow{
		{HourUTC: "2026-10-04 08:00", Primary: nf(30)},
		{HourUTC: "2026-10-04 09:00", Primary: nf(40)},
	}
	gotMap, gotRows := mergeRollingGasHours(existing, delta, start, end)
	if len(gotMap) != 3 || len(gotRows) != 3 {
		t.Fatalf("len = %d/%d, want 3: %+v", len(gotMap), len(gotRows), gotMap)
	}
	if _, pruned := gotMap["2026-10-01 09:00"]; pruned {
		t.Error("expected 2026-10-01 09:00 to be pruned")
	}
	if gotMap["2026-10-01 10:00"].Primary.Float64 != 2 || gotMap["2026-10-04 08:00"].Primary.Float64 != 30 || gotMap["2026-10-04 09:00"].Primary.Float64 != 40 {
		t.Errorf("unexpected merged hours: %+v", gotMap)
	}

	effEnd, complete := rollingEffectiveEnd(gotMap, start, end)
	if !complete || !effEnd.Equal(end) {
		t.Errorf("rollingEffectiveEnd(complete) = (%v, %v), want (%v, true)", effEnd, complete, end)
	}

	delete(gotMap, "2026-10-04 09:00")
	effEnd, complete = rollingEffectiveEnd(gotMap, start, end)
	wantEff := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	if complete || !effEnd.Equal(wantEff) {
		t.Errorf("rollingEffectiveEnd(missing latest hour) = (%v, %v), want (%v, false)", effEnd, complete, wantEff)
	}
}
