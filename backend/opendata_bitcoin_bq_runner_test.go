package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

// fakeBtcRunnerRollupSource is a goroutine-safe Job A source for runner tests:
// it counts calls, records the MaximumBytesBilled of the first billed query
// and can hold FetchBlocks open (blockFetch) after signalling fetchHit.
type fakeBtcRunnerRollupSource struct {
	mu           sync.Mutex
	ready        bool
	dryBlocks    int64
	dryRollup    int64
	blocks       []btcBlockRow
	rollup       []btcRollupRawRow
	billedBlocks int64
	billedRollup int64
	readyCalls   int
	dryRunCalls  int
	fetchCalls   int
	lastMaxBytes int64
	fetchHit     chan struct{}
	blockFetch   chan struct{}
}

func (f *fakeBtcRunnerRollupSource) ReadyAt(context.Context, time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCalls++
	return f.ready, nil
}

func (f *fakeBtcRunnerRollupSource) DryRunBlocks(context.Context, civil.Date, civil.Date) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRunCalls++
	return f.dryBlocks, nil
}

func (f *fakeBtcRunnerRollupSource) DryRunRollup(context.Context, civil.Date, civil.Date) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRunCalls++
	return f.dryRollup, nil
}

func (f *fakeBtcRunnerRollupSource) FetchBlocks(_ context.Context, _, _ civil.Date, maxBytes int64) ([]btcBlockRow, int64, error) {
	f.mu.Lock()
	f.fetchCalls++
	f.lastMaxBytes = maxBytes
	hit, blk := f.fetchHit, f.blockFetch
	f.fetchHit = nil
	f.mu.Unlock()
	if hit != nil {
		close(hit)
	}
	if blk != nil {
		<-blk
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocks, f.billedBlocks, nil
}

func (f *fakeBtcRunnerRollupSource) FetchRollup(context.Context, civil.Date, civil.Date, int64) ([]btcRollupRawRow, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchCalls++
	return f.rollup, f.billedRollup, nil
}

func (f *fakeBtcRunnerRollupSource) setDryRun(blocks, rollup int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryBlocks, f.dryRollup = blocks, rollup
}

// gate makes the next FetchBlocks signal hit and wait for blk to close.
func (f *fakeBtcRunnerRollupSource) gate() (hit, blk chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchHit, f.blockFetch = make(chan struct{}), make(chan struct{})
	return f.fetchHit, f.blockFetch
}

func (f *fakeBtcRunnerRollupSource) calls() (ready, dryRun, fetch int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readyCalls, f.dryRunCalls, f.fetchCalls
}

// fakeBtcRunnerCoinAgeSource is a goroutine-safe Job B source for runner tests.
type fakeBtcRunnerCoinAgeSource struct {
	mu          sync.Mutex
	ready       bool
	dry         int64
	rows        []btcCoinAgeRawRow
	billed      int64
	readyCalls  int
	dryRunCalls int
	fetchCalls  int
	lastStart   civil.Date
	lastEnd     civil.Date
}

func (f *fakeBtcRunnerCoinAgeSource) ReadyAt(context.Context, time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCalls++
	return f.ready, nil
}

func (f *fakeBtcRunnerCoinAgeSource) DryRunCoinAge(context.Context, civil.Date, civil.Date) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRunCalls++
	return f.dry, nil
}

func (f *fakeBtcRunnerCoinAgeSource) FetchCoinAge(_ context.Context, start, end civil.Date, _ int64) ([]btcCoinAgeRawRow, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchCalls++
	f.lastStart, f.lastEnd = start, end
	return f.rows, f.billed, nil
}

// btcRunnerDayBlocks returns two contiguous blocks mined on day.
func btcRunnerDayBlocks(day string) []btcBlockRow {
	return []btcBlockRow{
		{Height: 1000, TS: day + "T10:00:00Z", Date: day, Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
		{Height: 1001, TS: day + "T10:10:00Z", Date: day, Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
	}
}

// btcRunnerCoinAgeRows returns guard-passing coin-age rows for each spend day.
func btcRunnerCoinAgeRows(days ...string) []btcCoinAgeRawRow {
	var rows []btcCoinAgeRawRow
	for _, d := range days {
		rows = append(rows,
			btcCoinAgeRawRow{Kind: "totals", SpendDay: d, Sats: 500_000_000_000, N: 100_000},
			btcCoinAgeRawRow{Kind: "matrix", SpendDay: d, CreateDay: "2026-10-10", Sats: 500_000_000_000, N: 100_000},
		)
	}
	return rows
}

func seedBtcCursor(t *testing.T, store *riskStore, job, from, cursor string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := ensureSyncRow(ctx, store.db, job); err != nil {
		t.Fatal(err)
	}
	if err := updateSyncWatermarksTx(ctx, store.db, job, from, cursor, 10, now); err != nil {
		t.Fatal(err)
	}
}

func TestBtcBQEstimateRunAndAbuseGuards(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	nowFn := func() time.Time { return clock }
	h := newTestBitcoinHandler(t)
	h.bq = nil
	h.btc.now = nowFn
	store := openTestRiskStore(t)
	h.btc.read = newBtcReadStore(store, h.btc.settings, nowFn)

	// Cursor 2026-10-17: at 13:21 UTC the next settled window is the single day 2026-10-18.
	seedBtcCursor(t, store, btcOnchainJobID, "2025-10-19", "2026-10-17", clock)
	rollup := &fakeBtcRunnerRollupSource{
		ready:        true,
		dryBlocks:    40 << 20,
		dryRollup:    600 << 20,
		blocks:       btcRunnerDayBlocks("2026-10-18"),
		rollup:       []btcRollupRawRow{validTestRollupRow("2026-10-18", 2)},
		billedBlocks: 30 << 20,
		billedRollup: 500 << 20,
	}
	h.btc.bqRunner = newBtcBQRunner(h.btc.settings, store, rollup, &fakeBtcRunnerCoinAgeSource{ready: true}, nil, nowFn)

	const (
		estimateURL = "/api/opendata/bitcoin/bigquery/estimate?job=btc_onchain"
		runURL      = "/api/opendata/bitcoin/bigquery/run?job=btc_onchain"
	)
	post := func(fn http.HandlerFunc, target, body string) *httptest.ResponseRecorder {
		return stableBQPost(t, fn, target, "127.0.0.1:1234", "application/json", "bitcoin-bq", body)
	}
	meta := func() BtcMetaBigQuery {
		return decodeStable[BtcMetaData](t, stableGet(t, h.BitcoinMeta, "/api/opendata/bitcoin/meta")).BigQuery
	}
	const wantEstBytes = int64((40 + 600) << 20)
	confirmed := fmt.Sprintf(`{"confirmed_estimate_bytes": %d}`, wantEstBytes)

	// 1. Abuse guards: non-loopback -> 403 (even with X-Forwarded-For); a missing
	// or foreign action header, the wrong content type or the baseline job -> 400.
	reqNonLoop := httptest.NewRequest(http.MethodPost, estimateURL, nil)
	reqNonLoop.RemoteAddr = "192.168.1.25:54321"
	reqNonLoop.Header.Set("X-Forwarded-For", "127.0.0.1")
	reqNonLoop.Header.Set("Content-Type", "application/json")
	reqNonLoop.Header.Set("X-BigLens-Action", "bitcoin-bq")
	recNonLoop := httptest.NewRecorder()
	h.BitcoinBQEstimate(recNonLoop, reqNonLoop)
	if recNonLoop.Code != http.StatusForbidden {
		t.Fatalf("non-loopback estimate = %d, want 403", recNonLoop.Code)
	}
	for _, tc := range []struct {
		name, target, remote, contentType, action string
	}{
		{"missing action header", estimateURL, "127.0.0.1:1234", "application/json", ""},
		{"stablecoin action header", estimateURL, "127.0.0.1:1234", "application/json", "stablecoin-bq"},
		{"wrong content type", estimateURL, "[::1]:1234", "text/plain", "bitcoin-bq"},
		{"baseline job keeps its own CLI / allow_ui flow", "/api/opendata/bitcoin/bigquery/estimate?job=btc_utxo_baseline", "127.0.0.1:1234", "application/json", "bitcoin-bq"},
	} {
		if rec := stableBQPost(t, h.BitcoinBQEstimate, tc.target, tc.remote, tc.contentType, tc.action, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", tc.name, rec.Code)
		}
	}

	// 2. Schedule off: next_check_at is null; the next UTC day settles at 06:30 UTC.
	st0 := meta().Statuses[btcOnchainJobID]
	if st0.ScheduleEnabled || st0.NextCheckAt != nil {
		t.Errorf("schedule off: schedule_enabled=%v next_check_at=%v, want false and nil", st0.ScheduleEnabled, st0.NextCheckAt)
	}
	if st0.NextNewDayAt != "2026-10-20T06:30:00Z" {
		t.Errorf("next_new_day_at = %q, want 2026-10-20T06:30:00Z", st0.NextNewDayAt)
	}

	// 3. The estimate path makes ONLY dry-run calls (zero Ready and zero Fetch calls).
	recEst := post(h.BitcoinBQEstimate, estimateURL, "{}")
	if recEst.Code != http.StatusOK {
		t.Fatalf("estimate status = %d (%s), want 200", recEst.Code, recEst.Body.String())
	}
	est := decodeStable[BtcBQEstimateResponse](t, recEst)
	if est.WindowFrom != "2026-10-18" || est.WindowTo != "2026-10-18" || est.EstimatedBytes != wantEstBytes || !est.FitsBudget || est.NothingToDo {
		t.Fatalf("estimate = %+v, want window 2026-10-18..2026-10-18, bytes=%d, fits_budget=true", est, wantEstBytes)
	}
	if est.ReserveBytes != 1<<30 || est.BudgetLimitBytes != 2048<<30 {
		t.Errorf("reserve_bytes=%d budget_limit_bytes=%d, want the 1 GiB reservation floor and 2048 GiB", est.ReserveBytes, est.BudgetLimitBytes)
	}
	if est.PricingNote != "Estimated at $6.25/TiB; BigQuery bills actual bytes scanned." {
		t.Errorf("PricingNote = %q", est.PricingNote)
	}
	if ready, dry, fetch := rollup.calls(); ready != 0 || fetch != 0 || dry != 2 {
		t.Fatalf("estimate source calls: ready=%d dryRun=%d fetch=%d, want 0, 2, 0", ready, dry, fetch)
	}

	// 4. "Estimate changed" (> +10%) gets 409 and runs nothing.
	recChanged := post(h.BitcoinBQRun, runURL, `{"confirmed_estimate_bytes": 500000000}`)
	if recChanged.Code != http.StatusConflict || !strings.Contains(recChanged.Body.String(), "estimate changed") {
		t.Fatalf("estimate changed run = %d (%s), want 409 estimate changed", recChanged.Code, recChanged.Body.String())
	}

	// 5. A stopped job gets 409 with its stop reason and runs nothing.
	flags := newBtcStopFlags(store)
	if err := flags.stop(ctx, btcOnchainJobID, "stopped after 4 billed failures"); err != nil {
		t.Fatal(err)
	}
	recStopped := post(h.BitcoinBQRun, runURL, confirmed)
	if recStopped.Code != http.StatusConflict || !strings.Contains(recStopped.Body.String(), "stopped after 4 billed failures") {
		t.Fatalf("stopped job run = %d (%s), want 409 with stop reason", recStopped.Code, recStopped.Body.String())
	}
	if err := btcResumeJob(ctx, store, btcOnchainJobID); err != nil {
		t.Fatal(err)
	}

	// 6. An estimate over the 256 GiB per-run hard cap is refused (409,
	// budget_paused) before any billed query.
	overBytes := int64(40<<20 + 300<<30)
	rollup.setDryRun(40<<20, 300<<30)
	recOver := post(h.BitcoinBQRun, runURL, fmt.Sprintf(`{"confirmed_estimate_bytes": %d}`, overBytes))
	if recOver.Code != http.StatusConflict || !strings.Contains(recOver.Body.String(), "over_budget") {
		t.Fatalf("over-cap run = %d (%s), want 409 over_budget", recOver.Code, recOver.Body.String())
	}
	if stOver := meta().Statuses[btcOnchainJobID]; stOver.State != "budget_paused" || stOver.LastRunResult != "budget_paused" || stOver.LastRunTrigger != "manual" {
		t.Errorf("status after refusal = %+v, want budget_paused / manual", stOver)
	}
	if _, _, fetch := rollup.calls(); fetch != 0 {
		t.Fatalf("fetch calls before any accepted run = %d, want 0", fetch)
	}
	rollup.setDryRun(40<<20, 600<<20)
	_ = store.setSyncError(ctx, btcOnchainJobID, "")

	// 7. A manual run works with the schedule off; a concurrent second run gets
	// 409; another run within 5 minutes gets 429.
	hit, blk := rollup.gate()
	recRun := post(h.BitcoinBQRun, runURL, confirmed)
	if recRun.Code != http.StatusAccepted {
		t.Fatalf("manual run status = %d (%s), want 202", recRun.Code, recRun.Body.String())
	}
	<-hit
	if !meta().Statuses[btcOnchainJobID].Running {
		t.Errorf("/meta while in flight: running = false, want true")
	}
	if rec := post(h.BitcoinBQRun, runURL, confirmed); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already running") {
		t.Fatalf("concurrent run = %d (%s), want 409 already running", rec.Code, rec.Body.String())
	}
	close(blk)
	h.btc.bqRunner.Wait()

	clock = clock.Add(2 * time.Minute)
	if rec := post(h.BitcoinBQRun, runURL, confirmed); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("run within 5m = %d, want 429", rec.Code)
	}

	// Status after the manual run: ready (schedule still off), with last-run
	// details and the settled bytes on the budget bar.
	done := meta()
	stDone := done.Statuses[btcOnchainJobID]
	wantBilled := int64((30 + 500) << 20)
	if stDone.Running || stDone.State != "ready" || stDone.SyncedThrough != "2026-10-18" || stDone.LastRunTrigger != "manual" || stDone.LastRunResult != "ok" {
		t.Fatalf("status after manual run = %+v, want ready through 2026-10-18, manual, ok", stDone)
	}
	if stDone.LastRunBytesBilled == nil || *stDone.LastRunBytesBilled != wantBilled {
		t.Fatalf("last_run_bytes_billed = %v, want %d", stDone.LastRunBytesBilled, wantBilled)
	}
	if !done.DataAvailable || done.BudgetUsedBytes != wantBilled || done.BudgetLimitBytes != 2048<<30 {
		t.Fatalf("data_available=%v budget_used_bytes=%d budget_limit_bytes=%d, want true, %d, %d",
			done.DataAvailable, done.BudgetUsedBytes, done.BudgetLimitBytes, wantBilled, int64(2048<<30))
	}

	// Synced through the latest settled day: nothing to do until 06:30 UTC tomorrow.
	if estDone := decodeStable[BtcBQEstimateResponse](t, post(h.BitcoinBQEstimate, estimateURL, "{}")); !estDone.NothingToDo {
		t.Errorf("estimate after catching up: nothing_to_do = false, want true")
	}
	clock = clock.Add(btcManualCooldown)
	if rec := post(h.BitcoinBQRun, runURL, confirmed); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "nothing to do") {
		t.Fatalf("run when caught up = %d (%s), want 409 nothing to do", rec.Code, rec.Body.String())
	}

	// 8. Schedule on: next_check_at is the last loop tick + 1h.
	h.btc.settings.Jobs = true
	h.btc.read = newBtcReadStore(store, h.btc.settings, nowFn)
	h.btc.bqRunner.settings.Jobs = true
	tick := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	clock = tick
	h.btc.bqRunner.recordLoopTick(tick)
	stSched := meta().Statuses[btcOnchainJobID]
	if !stSched.ScheduleEnabled || stSched.NextCheckAt == nil || *stSched.NextCheckAt != "2026-10-19T15:00:00Z" {
		t.Fatalf("schedule on status = %+v, want schedule_enabled and next_check_at 2026-10-19T15:00:00Z", stSched)
	}
}

func TestBtcBQEstimateWaitsForSixHourSlack(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
	rollup := &fakeBtcRunnerRollupSource{ready: true, dryBlocks: 40 << 20, dryRollup: 600 << 20}
	runner := newBtcBQRunner((BitcoinConfig{}).settings(), store, rollup, &fakeBtcRunnerCoinAgeSource{}, nil, func() time.Time { return clock })
	seedBtcCursor(t, store, btcOnchainJobID, "2025-10-19", "2026-10-17", clock)

	// 03:00 UTC: blocks of 2026-10-18 may still be back-dated (MTP-11), so the
	// day has not settled and nothing is dry-run.
	est, err := runner.estimate(ctx, btcOnchainJobID)
	if err != nil {
		t.Fatal(err)
	}
	if !est.NothingToDo {
		t.Fatalf("estimate at 03:00 UTC = %+v, want nothing_to_do (2026-10-18 settles at 06:30 UTC)", est)
	}
	if _, dry, _ := rollup.calls(); dry != 0 {
		t.Errorf("dry-run calls with nothing to do = %d, want 0", dry)
	}
	if got := btcNextNewDayAt(clock); got != "2026-10-19T06:30:00Z" {
		t.Errorf("btcNextNewDayAt(03:00) = %q, want 2026-10-19T06:30:00Z", got)
	}

	// 06:30 UTC: the day settles and becomes the next window.
	clock = time.Date(2026, 10, 19, 6, 30, 0, 0, time.UTC)
	est, err = runner.estimate(ctx, btcOnchainJobID)
	if err != nil {
		t.Fatal(err)
	}
	if est.NothingToDo || est.WindowFrom != "2026-10-18" || est.WindowTo != "2026-10-18" {
		t.Fatalf("estimate at 06:30 UTC = %+v, want window 2026-10-18..2026-10-18", est)
	}
}

func TestBtcBQCoinageManualRunBypassesCadence(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	settings := (BitcoinConfig{}).settings() // cadence_days: 7
	coinAge := &fakeBtcRunnerCoinAgeSource{
		ready:  true,
		dry:    100 << 30,
		billed: 98 << 30,
		rows:   btcRunnerCoinAgeRows("2026-10-16", "2026-10-17", "2026-10-18"),
	}
	runner := newBtcBQRunner(settings, store, &fakeBtcRunnerRollupSource{}, coinAge, nil, func() time.Time { return clock })
	// Cursor 2026-10-15: only 3 of the 7 cadence days have settled.
	seedBtcCursor(t, store, btcCoinageJobID, "2025-10-19", "2026-10-15", clock)

	// The schedule keeps the weekly cadence (each run scans ~100 GiB however
	// short the window): not due, so not even a Ready probe.
	runner.runScheduled(ctx, btcCoinageJobID)
	if coinAge.readyCalls != 0 || coinAge.dryRunCalls != 0 || coinAge.fetchCalls != 0 {
		t.Fatalf("scheduled run before cadence: ready=%d dry=%d fetch=%d, want 0, 0, 0", coinAge.readyCalls, coinAge.dryRunCalls, coinAge.fetchCalls)
	}
	if lr, _ := runner.getLastRun(btcCoinageJobID); lr.Result != "no_new_day" || lr.Trigger != "schedule" {
		t.Errorf("scheduled last run = %+v, want no_new_day / schedule", lr)
	}

	// Run now previews and fetches every settled day not yet synced.
	est, err := runner.estimate(ctx, btcCoinageJobID)
	if err != nil {
		t.Fatal(err)
	}
	if est.NothingToDo || est.WindowFrom != "2026-10-16" || est.WindowTo != "2026-10-18" || est.EstimatedBytes != 100<<30 || est.ReserveBytes != 125<<30 || !est.FitsBudget {
		t.Fatalf("coinage estimate = %+v, want window 2026-10-16..2026-10-18, 100 GiB, 125 GiB reservation, fits", est)
	}
	runner.executeSync(ctx, btcCoinageJobID, "manual")
	if coinAge.fetchCalls != 1 || coinAge.lastStart.String() != "2026-10-16" || coinAge.lastEnd.String() != "2026-10-19" {
		t.Fatalf("manual coinage fetch calls=%d window=[%s, %s), want 1 call over [2026-10-16, 2026-10-19)",
			coinAge.fetchCalls, coinAge.lastStart, coinAge.lastEnd)
	}
	if st, err := store.getSyncState(ctx, btcCoinageJobID); err != nil || st.Cursor != "2026-10-18" {
		t.Fatalf("coinage cursor after manual run = %q (%v), want 2026-10-18", st.Cursor, err)
	}
	if lr, _ := runner.getLastRun(btcCoinageJobID); lr.Result != "ok" || lr.Trigger != "manual" || lr.BytesBilled != 98<<30 {
		t.Errorf("manual last run = %+v, want ok / manual / 98 GiB", lr)
	}
	if got := runner.syncers[btcCoinageJobID].minWindowDays; got != 7 {
		t.Errorf("coinage minWindowDays after manual run = %d, want 7 (cadence restored for the schedule)", got)
	}

	// With the schedule on, the next coin-age check is the first hourly tick
	// after the next 7-day window settles (2026-10-26 06:30 UTC).
	tick := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	if got := btcNextCheckAt(btcCoinageJobID, settings, "2026-10-18", clock, tick).Format(time.RFC3339); got != "2026-10-26T07:00:00Z" {
		t.Errorf("coinage next check = %s, want 2026-10-26T07:00:00Z", got)
	}
	if got := btcNextCheckAt(btcOnchainJobID, settings, "2026-10-18", clock, tick).Format(time.RFC3339); got != "2026-10-19T15:00:00Z" {
		t.Errorf("onchain next check = %s, want 2026-10-19T15:00:00Z", got)
	}
}

func TestBtcBQSourceNotCaughtUpIsNoNewDay(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	rollup := &fakeBtcRunnerRollupSource{ready: false}
	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	runner := newBtcBQRunner(settings, store, rollup, &fakeBtcRunnerCoinAgeSource{}, nil, func() time.Time { return clock })
	seedBtcCursor(t, store, btcOnchainJobID, "2025-10-19", "2026-10-17", clock)

	// BigQuery has not ingested past 2026-10-19 06:00 UTC yet: an unbilled,
	// expected daily wait that must not read as an error or count as a failure.
	runner.runScheduled(ctx, btcOnchainJobID)
	lr, ok := runner.getLastRun(btcOnchainJobID)
	if !ok || lr.Result != "no_new_day" || lr.BytesBilled != 0 {
		t.Fatalf("last run = %+v, want no_new_day with 0 bytes billed", lr)
	}
	if recs, _ := runner.flags.failures(ctx, btcOnchainJobID); len(recs) != 0 {
		t.Errorf("failures = %+v, want none for an unbilled readiness check", recs)
	}
	if _, dry, fetch := rollup.calls(); dry != 0 || fetch != 0 {
		t.Errorf("dry=%d fetch=%d, want 0 and 0 before the source catches up", dry, fetch)
	}
}

func TestBtcBQEstimateUsesSyncerReservation(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	nowFn := func() time.Time { return clock }
	settings := (BitcoinConfig{}).settings()

	// The estimate's reserve_bytes is exactly the MaximumBytesBilled cap the
	// syncer then reserves and passes to the first billed query.
	store := newTestRiskStore(t)
	seedBtcCursor(t, store, btcOnchainJobID, "2025-10-19", "2026-10-17", clock)
	rollup := &fakeBtcRunnerRollupSource{
		ready:        true,
		dryBlocks:    456_789_012,
		dryRollup:    3_000_000_000,
		blocks:       btcRunnerDayBlocks("2026-10-18"),
		rollup:       []btcRollupRawRow{validTestRollupRow("2026-10-18", 2)},
		billedBlocks: 400_000_000,
		billedRollup: 2_800_000_000,
	}
	runner := newBtcBQRunner(settings, store, rollup, &fakeBtcRunnerCoinAgeSource{}, nil, nowFn)
	est, err := runner.estimate(ctx, btcOnchainJobID)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(math.Ceil(float64(est.EstimatedBytes) * 1.25)); est.ReserveBytes != want {
		t.Fatalf("reserve_bytes = %d, want est*1.25 = %d", est.ReserveBytes, want)
	}
	runner.executeSync(ctx, btcOnchainJobID, "manual")
	if lr, _ := runner.getLastRun(btcOnchainJobID); lr.Result != "ok" {
		t.Fatalf("manual run = %+v, want ok", lr)
	}
	if rollup.lastMaxBytes != est.ReserveBytes {
		t.Errorf("first query maxBytes = %d, want the estimate's reserve_bytes %d", rollup.lastMaxBytes, est.ReserveBytes)
	}

	// Near the monthly cap: the raw 900 MiB estimate fits the 1 GiB left, but
	// its 1,125 MiB reservation does not, so the preview already refuses it.
	store2 := newTestRiskStore(t)
	seedBtcCursor(t, store2, btcOnchainJobID, "2025-10-19", "2026-10-17", clock)
	runner2 := newBtcBQRunner(settings, store2, &fakeBtcRunnerRollupSource{dryBlocks: 100 << 20, dryRollup: 800 << 20}, &fakeBtcRunnerCoinAgeSource{}, nil, nowFn)
	id, err := runner2.budget.reserve(ctx, btcCoinageJobID, 2047<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner2.budget.settle(ctx, id, 2047<<30); err != nil {
		t.Fatal(err)
	}
	est2, err := runner2.estimate(ctx, btcOnchainJobID)
	if err != nil {
		t.Fatal(err)
	}
	if est2.EstimatedBytes != 900<<20 || est2.ReserveBytes != 1125<<20 || est2.FitsBudget {
		t.Fatalf("near-cap estimate = %+v, want 900 MiB, 1125 MiB reservation, fits_budget=false", est2)
	}
}

func TestBtcBQRoutesCrossOriginProtection(t *testing.T) {
	h := newTestBitcoinHandler(t)
	src := panicBtcQuerySource{t: t}
	h.btc.bqRunner = newBtcBQRunner(h.btc.settings, openTestRiskStore(t), src, src, nil, h.btc.now)
	mux := http.NewServeMux()
	registerBitcoinRoutes(mux, h,
		func(fn http.HandlerFunc) http.Handler { return fn },
		func(next http.Handler) http.Handler { return next },
	)

	// Even a loopback request with every required header is refused when a
	// browser marks it cross-site: another website open on this computer
	// cannot drive the estimate/run endpoints.
	for _, target := range []string{
		"/api/opendata/bitcoin/bigquery/estimate?job=btc_onchain",
		"/api/opendata/bitcoin/bigquery/run?job=btc_onchain",
	} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"confirmed_estimate_bytes": 1}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-BigLens-Action", "bitcoin-bq")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d, want 403", target, rec.Code)
		}
	}
}
