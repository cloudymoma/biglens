package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

type fakeDailyJob struct {
	mu           sync.Mutex
	id           string
	ready        bool
	readyErr     error
	dryBytes     int64
	runResult    dailyResult
	runBilled    int64
	runErr       error
	storeErr     error
	panicOnRun   bool
	readyCalls   []civil.Date
	dryRunCalls  []fetchCall
	runCalls     []fetchCall
	storeApplied int
}

func (f *fakeDailyJob) ID() string { return f.id }

func (f *fakeDailyJob) Ready(_ context.Context, end civil.Date) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCalls = append(f.readyCalls, end)
	if f.readyErr != nil {
		return false, f.readyErr
	}
	return f.ready, nil
}

func (f *fakeDailyJob) DryRun(_ context.Context, start, end civil.Date) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRunCalls = append(f.dryRunCalls, fetchCall{start: start, end: end})
	return f.dryBytes, nil
}

func (f *fakeDailyJob) Run(_ context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, fetchCall{start: start, end: end, maxBytes: maxBytes})
	if f.panicOnRun {
		panic("simulated job panic")
	}
	if f.runErr != nil {
		return dailyResult{}, f.runBilled, f.runErr
	}
	res := f.runResult
	if res.Rows == 0 {
		res.Rows = 10
	}
	billed := f.runBilled
	if billed == 0 {
		billed = 1 << 30
	}
	return res, billed, nil
}

func (f *fakeDailyJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	f.mu.Lock()
	f.storeApplied++
	storeErr := f.storeErr
	f.mu.Unlock()
	if storeErr != nil {
		return storeErr
	}
	return s.applyScamBatch(ctx, f.id, "eth", r.Days, newFrom, cursor, now)
}

func newTestDailySyncer(t *testing.T, id string, now time.Time, initialDays int) (*dailyBQSyncer, *fakeDailyJob, *riskStore) {
	t.Helper()
	store := newTestRiskStore(t)
	job := &fakeDailyJob{id: id, ready: true, dryBytes: 50 << 30}
	syncer := &dailyBQSyncer{
		store:         store,
		job:           job,
		now:           func() time.Time { return now },
		initialDays:   initialDays,
		retentionDays: 90,
	}
	return syncer, job, store
}

func TestDailySyncerColdStartUsesInitialDays(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	notified := 0
	s.onChange = func() { notified++ }

	s.syncOnce(context.Background())

	if len(job.dryRunCalls) != 1 {
		t.Fatalf("dryRunCalls = %d, want 1", len(job.dryRunCalls))
	}
	if len(job.runCalls) != 1 {
		t.Fatalf("runCalls = %d, want 1", len(job.runCalls))
	}
	rc := job.runCalls[0]
	if rc.start != syncToday.AddDays(-30) || rc.end != syncToday {
		t.Errorf("cold start window = [%v, %v), want [%v, %v)", rc.start, rc.end, syncToday.AddDays(-30), syncToday)
	}
	if rc.maxBytes != dailySyncColdStartMaxBytes {
		t.Errorf("cold start maxBytes = %d, want %d", rc.maxBytes, dailySyncColdStartMaxBytes)
	}
	st, err := store.getSyncState(context.Background(), scamEthSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if st.CoverageFrom != "2026-08-27" || st.Cursor != "2026-09-25" || st.LastError != "" {
		t.Errorf("sync_state after cold start = %+v", st)
	}
	if notified != 1 {
		t.Errorf("onChange called %d times, want 1", notified)
	}
}

func TestDailySyncerInitialDaysZeroDisablesColdStart(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 0)
	s.syncOnce(context.Background())

	if len(job.readyCalls) != 0 || len(job.runCalls) != 0 {
		t.Fatalf("initial_days=0 must not call Ready or Run: ready=%d run=%d", len(job.readyCalls), len(job.runCalls))
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "" {
		t.Errorf("cursor = %q, want empty", st.Cursor)
	}
}

func TestDailySyncerAdvancesCursor(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}

	s.syncOnce(context.Background())

	if len(job.runCalls) != 1 {
		t.Fatalf("runCalls = %d, want 1", len(job.runCalls))
	}
	rc := job.runCalls[0]
	if rc.start != syncToday.AddDays(-1) || rc.end != syncToday {
		t.Errorf("incremental window = [%v, %v), want [%v, %v)", rc.start, rc.end, syncToday.AddDays(-1), syncToday)
	}
	if rc.maxBytes != dailySyncIncrementalMaxBytes {
		t.Errorf("incremental maxBytes = %d, want %d", rc.maxBytes, dailySyncIncrementalMaxBytes)
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-25" || st.CoverageFrom != "2026-08-27" {
		t.Errorf("sync_state = %+v", st)
	}

	// Second tick when already caught up does not touch BigQuery.
	s.syncOnce(context.Background())
	if len(job.runCalls) != 1 || len(job.readyCalls) != 1 {
		t.Errorf("caught-up tick touched BQ: ready=%d run=%d", len(job.readyCalls), len(job.runCalls))
	}
}

func TestDailySyncerNotReadySkipsRun(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}
	job.ready = false

	s.syncOnce(context.Background())

	if len(job.runCalls) != 0 {
		t.Fatalf("Run called %d times when Ready=false, want 0", len(job.runCalls))
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != "source not caught up" {
		t.Errorf("sync_state when not ready = %+v", st)
	}
}

func TestDailySyncerRejectsWindowOver31Days(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-07-01", "2026-08-15", at(5, 0)); err != nil {
		t.Fatal(err)
	}

	s.syncOnce(context.Background())

	if len(job.runCalls) != 0 {
		t.Fatalf("Run called %d times for >31d gap, want 0", len(job.runCalls))
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-08-15" || !strings.HasPrefix(st.LastError, "gap too large") {
		t.Errorf("sync_state on >31d gap = %+v", st)
	}
}

func TestDailySyncerBilledFailureBacksOff6h(t *testing.T) {
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, scamEthSourceID, cur, 30)
	s.now = func() time.Time { return cur }
	job.runErr = errors.New("row decode failed")
	job.runBilled = 5 << 30

	s.syncOnce(context.Background())
	if len(job.runCalls) != 1 {
		t.Fatalf("runCalls = %d, want 1", len(job.runCalls))
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.LastError != "bigquery_error" {
		t.Errorf("last_error = %q, want bigquery_error", st.LastError)
	}

	// 1 hour later: still within 6h backoff.
	cur = cur.Add(time.Hour)
	s.syncOnce(context.Background())
	if len(job.runCalls) != 1 {
		t.Fatalf("retried billed failure after 1h: runCalls = %d", len(job.runCalls))
	}

	// 6 hours after failure: backoff expired, retries.
	cur = cur.Add(5 * time.Hour)
	job.runErr = nil
	s.syncOnce(context.Background())
	if len(job.runCalls) != 2 {
		t.Fatalf("did not retry after 6h backoff: runCalls = %d", len(job.runCalls))
	}
	st, _ = store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-25" || st.LastError != "" {
		t.Errorf("sync_state after retry = %+v", st)
	}
}

func TestDailySyncerSourceFailureIsolated(t *testing.T) {
	store := newTestRiskStore(t)
	now := at(6, 0)

	ethJob := &fakeDailyJob{id: scamEthSourceID, ready: true, runErr: errors.New("bq quota exceeded")}
	tronJob := &fakeDailyJob{id: scamTronSourceID, ready: true}

	ethSync := &dailyBQSyncer{store: store, job: ethJob, now: func() time.Time { return now }, initialDays: 30}
	tronSync := &dailyBQSyncer{store: store, job: tronJob, now: func() time.Time { return now }, initialDays: 30}

	ethSync.syncOnce(context.Background())
	tronSync.syncOnce(context.Background())

	ethSt, _ := store.getSyncState(context.Background(), scamEthSourceID)
	tronSt, _ := store.getSyncState(context.Background(), scamTronSourceID)

	if ethSt.Cursor != "" || ethSt.LastError != "bigquery_error" {
		t.Errorf("eth sync_state = %+v, want empty cursor and bigquery_error", ethSt)
	}
	if tronSt.Cursor != "2026-09-25" || tronSt.LastError != "" {
		t.Errorf("tron sync_state = %+v, want cursor 2026-09-25 and no error", tronSt)
	}
}

func TestDailySyncerRecoversPanic(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	job.panicOnRun = true

	s.syncOnce(context.Background())

	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.LastError != "panic" || strings.Contains(st.LastError, "simulated") {
		t.Errorf("last_error after panic = %q, want 'panic' only", st.LastError)
	}
}

func TestDailySyncerStoreFailureKeepsCursorAndBacksOff(t *testing.T) {
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, scamEthSourceID, cur, 30)
	s.now = func() time.Time { return cur }
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}
	job.storeErr = errors.New("sqlite disk full")

	s.syncOnce(context.Background())

	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != "unavailable" {
		t.Fatalf("sync_state after Store failure = %+v, want cursor 2026-09-24 and error unavailable", st)
	}

	// Must back off for 6h because BigQuery already billed the Run.
	cur = cur.Add(time.Hour)
	s.syncOnce(context.Background())
	if len(job.runCalls) != 1 {
		t.Errorf("Store failure re-ran billed query after 1h: runCalls = %d", len(job.runCalls))
	}
}
