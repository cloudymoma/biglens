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
	mu                sync.Mutex
	id                string
	ready             bool
	readyErr          error
	dryBytes          int64
	runResult         dailyResult
	runBilled         int64
	runErr            error
	storeErr          error
	panicOnRun        bool
	blockUntilCtxDone bool
	attachJobID       string
	onAttach          func()
	readyCalls        []civil.Date
	dryRunCalls       []fetchCall
	runCalls          []fetchCall
	storeApplied      int
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

func (f *fakeDailyJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	f.mu.Lock()
	attachID := f.attachJobID
	onAttach := f.onAttach
	f.runCalls = append(f.runCalls, fetchCall{start: start, end: end, maxBytes: maxBytes})
	panicOnRun := f.panicOnRun
	blockUntilDone := f.blockUntilCtxDone
	runErr := f.runErr
	runBilled := f.runBilled
	res := f.runResult
	f.mu.Unlock()

	if attachID != "" {
		syncAttachJob(ctx, attachID)
		if onAttach != nil {
			onAttach()
		}
	}
	if panicOnRun {
		panic("simulated job panic")
	}
	if blockUntilDone {
		<-ctx.Done()
		return dailyResult{}, runBilled, ctx.Err()
	}
	if runErr != nil {
		return dailyResult{}, runBilled, runErr
	}
	if res.Rows == 0 {
		res.Rows = 10
	}
	billed := runBilled
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

// initial_days: 0 disables Scam Radar entirely (spec P2.0 Q2, README, the
// panel's hint): a source that already has a cursor must stop syncing too,
// while the data stored so far stays readable.
func TestDailySyncerInitialDaysZeroDisablesExistingCursor(t *testing.T) {
	ctx := context.Background()
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 0)
	const lk = "0x111199999999999999999999999999999999aaaa"
	if err := store.applyScamBatch(ctx, scamEthSourceID, "eth", []scamDayBatch{{
		Day:        "2026-09-24",
		Lookalikes: []lookalikeRow{{Lookalike: lk, Imitated: "0x111100000000000000000000000000000000aaaa", Hits: 3, Victims: 2}},
	}}, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}

	s.syncOnce(ctx)

	if len(job.readyCalls) != 0 || len(job.runCalls) != 0 || len(job.dryRunCalls) != 0 {
		t.Fatalf("initial_days=0 with a cursor touched BigQuery: ready=%d dry=%d run=%d",
			len(job.readyCalls), len(job.dryRunCalls), len(job.runCalls))
	}
	st, _ := store.getSyncState(ctx, scamEthSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != "" {
		t.Errorf("sync_state = %+v, want cursor 2026-09-24 and no error", st)
	}
	hits, err := store.lookalikeHits(ctx, "eth", []string{lk})
	if err != nil || hits[lk].Hits != 3 {
		t.Errorf("stored lookalike after disabling = %+v, %v; want it still readable", hits, err)
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

// A run whose query succeeded but whose result is implausible must fail like
// a billed BigQuery error: nothing stored, cursor unchanged, the guard's
// message as last_error (not a generic code), and the 6 h billed back-off.
func TestDailySyncerSuspiciousEmptyFailsRun(t *testing.T) {
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, scamEthSourceID, cur, 30)
	s.now = func() time.Time { return cur }
	notified := 0
	s.onChange = func() { notified++ }
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}
	job.runErr = suspiciousEmpty("2026-09-25", "official USDT/USDC transfers scanned")
	job.runBilled = 3 << 30

	s.syncOnce(context.Background())

	const wantMsg = "suspicious empty result for 2026-09-25: 0 official USDT/USDC transfers scanned — upstream schema or format change?"
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != wantMsg {
		t.Fatalf("sync_state = %+v, want cursor 2026-09-24 and last_error %q", st, wantMsg)
	}
	if job.storeApplied != 0 || notified != 0 {
		t.Errorf("suspicious run stored %d batches / notified %d times, want 0", job.storeApplied, notified)
	}

	cur = cur.Add(time.Hour)
	s.syncOnce(context.Background())
	if len(job.runCalls) != 1 {
		t.Fatalf("suspicious billed run retried after 1h: runCalls = %d, want 1 (6h back-off)", len(job.runCalls))
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

func TestScamRadarConfigDefaultsAndZeroDisable(t *testing.T) {
	var cfg AddressRiskConfig
	if got := cfg.scamRadarInitialDays(); got != 30 {
		t.Errorf("default scamRadarInitialDays = %d, want 30", got)
	}
	if got := cfg.scamRadarRetentionDays(); got != 90 {
		t.Errorf("default scamRadarRetentionDays = %d, want 90", got)
	}

	zero := 0
	cfg.ScamRadar.InitialDays = &zero
	if got := cfg.scamRadarInitialDays(); got != 0 {
		t.Errorf("zero scamRadarInitialDays = %d, want 0", got)
	}

	forty := 40
	cfg.ScamRadar.InitialDays = &forty
	cfg.ScamRadar.RetentionDays = 120
	if got := cfg.scamRadarInitialDays(); got != 31 {
		t.Errorf("clamped scamRadarInitialDays = %d, want 31", got)
	}
	if got := cfg.scamRadarRetentionDays(); got != 120 {
		t.Errorf("custom scamRadarRetentionDays = %d, want 120", got)
	}
}

type fakeSyncBudget struct {
	mu           sync.Mutex
	refuseErr    error
	reservedID   string
	reservedJob  string
	reservedEst  int64
	attachedJob  string
	settledID    string
	settledBytes int64
	settledCalls int
}

func (b *fakeSyncBudget) reserve(_ context.Context, job string, bytes int64) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuseErr != nil {
		return "", b.refuseErr
	}
	b.reservedID = "res-1"
	b.reservedJob = job
	b.reservedEst = bytes
	return b.reservedID, nil
}

func (b *fakeSyncBudget) attachJob(_ context.Context, id, bqJobID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id == b.reservedID {
		b.attachedJob = bqJobID
	}
	return nil
}

func (b *fakeSyncBudget) settle(_ context.Context, id string, billed int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settledID = id
	b.settledBytes = billed
	b.settledCalls++
	return nil
}

type fakeSyncStopFlags struct {
	mu     sync.Mutex
	isStop map[string]string
	recs   map[string][]syncFailureRecord
}

func newFakeSyncStopFlags() *fakeSyncStopFlags {
	return &fakeSyncStopFlags{
		isStop: make(map[string]string),
		recs:   make(map[string][]syncFailureRecord),
	}
}

func (f *fakeSyncStopFlags) stopped(_ context.Context, job string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.isStop[job]
	return r, ok, nil
}

func (f *fakeSyncStopFlags) recordFailure(_ context.Context, job string, rec syncFailureRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[job] = append(f.recs[job], rec)
	return nil
}

func (f *fakeSyncStopFlags) failures(_ context.Context, job string) ([]syncFailureRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]syncFailureRecord, len(f.recs[job]))
	copy(out, f.recs[job])
	return out, nil
}

func (f *fakeSyncStopFlags) stop(_ context.Context, job, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isStop[job] = reason
	return nil
}

func (f *fakeSyncStopFlags) clear(_ context.Context, job string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.isStop, job)
	delete(f.recs, job)
	return nil
}

func TestDailyBQSyncerDefaultsMatchScamRadar(t *testing.T) {
	s, job, store := newTestDailySyncer(t, scamEthSourceID, at(6, 0), 30)
	s.syncOnce(context.Background())

	if len(job.runCalls) != 1 {
		t.Fatalf("runCalls = %d, want 1", len(job.runCalls))
	}
	if job.runCalls[0].maxBytes != dailySyncColdStartMaxBytes {
		t.Errorf("cold start maxBytes = %d, want %d", job.runCalls[0].maxBytes, dailySyncColdStartMaxBytes)
	}
	st, _ := store.getSyncState(context.Background(), scamEthSourceID)
	if st.Cursor != "2026-09-25" {
		t.Errorf("cursor = %q, want 2026-09-25", st.Cursor)
	}
}

func TestDailyBQSyncerChunkCatchUp(t *testing.T) {
	ctx := context.Background()
	s, job, store := newTestDailySyncer(t, "btc_onchain", at(6, 0), 90)
	s.maxWindowDays = 31
	s.chunkCatchUp = true

	// Tick 1: cold start 90 days ago -> clamped to 31 days.
	s.syncOnce(ctx)
	if len(job.runCalls) != 1 {
		t.Fatalf("tick 1 runCalls = %d, want 1", len(job.runCalls))
	}
	w1 := job.runCalls[0]
	if w1.end.DaysSince(w1.start) != 31 {
		t.Errorf("tick 1 window days = %d, want 31 ([%v, %v))", w1.end.DaysSince(w1.start), w1.start, w1.end)
	}

	// Tick 2: next 31 days.
	s.syncOnce(ctx)
	if len(job.runCalls) != 2 {
		t.Fatalf("tick 2 runCalls = %d, want 2", len(job.runCalls))
	}
	w2 := job.runCalls[1]
	if w2.start != w1.end || w2.end.DaysSince(w2.start) != 31 {
		t.Errorf("tick 2 window = [%v, %v), want start %v and 31 days", w2.start, w2.end, w1.end)
	}

	// Tick 3: remaining 28 days to syncToday.
	s.syncOnce(ctx)
	if len(job.runCalls) != 3 {
		t.Fatalf("tick 3 runCalls = %d, want 3", len(job.runCalls))
	}
	w3 := job.runCalls[2]
	if w3.end != syncToday {
		t.Errorf("tick 3 end = %v, want %v", w3.end, syncToday)
	}
	st, _ := store.getSyncState(ctx, "btc_onchain")
	if st.Cursor != "2026-09-25" {
		t.Errorf("final cursor = %q, want 2026-09-25", st.Cursor)
	}
}

func TestDailyBQSyncerMinWindowCadence(t *testing.T) {
	ctx := context.Background()
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, "btc_coinage", cur, 30)
	s.now = func() time.Time { return cur }
	s.minWindowDays = 7

	// Cold start runs immediately even when minWindowDays = 7.
	s.syncOnce(ctx)
	if len(job.runCalls) != 1 {
		t.Fatalf("cold start runCalls = %d, want 1", len(job.runCalls))
	}

	// 3 days later: incremental window is 3 days < 7 -> must not call Ready or Run.
	cur = cur.Add(3 * 24 * time.Hour)
	s.syncOnce(ctx)
	if len(job.runCalls) != 1 || len(job.readyCalls) != 1 {
		t.Fatalf("3d incremental ran early: readyCalls=%d runCalls=%d", len(job.readyCalls), len(job.runCalls))
	}

	// 7 days after cold start: window is 7 days -> runs!
	cur = cur.Add(4 * 24 * time.Hour)
	s.syncOnce(ctx)
	if len(job.runCalls) != 2 {
		t.Fatalf("7d incremental did not run: runCalls=%d, want 2", len(job.runCalls))
	}
	st, _ := store.getSyncState(ctx, "btc_coinage")
	if st.Cursor != "2026-10-02" {
		t.Errorf("cursor after 7d cadence = %q, want 2026-10-02", st.Cursor)
	}
}

func TestDailyBQSyncerReserveSkip(t *testing.T) {
	ctx := context.Background()
	s, job, store := newTestDailySyncer(t, "btc_onchain", at(6, 0), 30)
	s.budget = &fakeSyncBudget{refuseErr: errors.New("insufficient budget")}

	s.syncOnce(ctx)

	if len(job.runCalls) != 0 {
		t.Fatalf("Run called %d times when reservation refused, want 0", len(job.runCalls))
	}
	st, _ := store.getSyncState(ctx, "btc_onchain")
	if st.LastError != "over_budget" || st.Cursor != "" {
		t.Errorf("sync_state = %+v, want over_budget and empty cursor", st)
	}
}

func TestDailyBQSyncerDryRunSizedCap(t *testing.T) {
	ctx := context.Background()
	s, job, _ := newTestDailySyncer(t, "btc_onchain", at(6, 0), 30)
	s.dryRunSizedCap = true
	s.hardCapBytes = 256 << 30
	job.dryBytes = 80 << 30 // 80 GiB * 1.25 = 100 GiB

	s.syncOnce(ctx)

	if len(job.runCalls) != 1 {
		t.Fatalf("runCalls = %d, want 1", len(job.runCalls))
	}
	want := int64(100 << 30)
	if job.runCalls[0].maxBytes != want {
		t.Errorf("maxBytes = %d, want %d (1.25x dryRun)", job.runCalls[0].maxBytes, want)
	}

	// Small dryRun (< 1 GiB) clamps up to 1 GiB floor.
	s2, job2, _ := newTestDailySyncer(t, "btc_onchain", at(6, 0), 7)
	s2.dryRunSizedCap = true
	s2.hardCapBytes = 256 << 30
	job2.dryBytes = 200 << 20 // 200 MiB
	s2.syncOnce(ctx)
	if len(job2.runCalls) != 1 || job2.runCalls[0].maxBytes != 1<<30 {
		t.Errorf("small dryRun maxBytes = %d, want %d (1 GiB floor)", job2.runCalls[0].maxBytes, int64(1<<30))
	}
}

func TestDailyBQSyncerSettle(t *testing.T) {
	ctx := context.Background()
	s, job, _ := newTestDailySyncer(t, "btc_onchain", at(6, 0), 7)
	budget := &fakeSyncBudget{}
	s.budget = budget
	s.dryRunSizedCap = true
	job.dryBytes = 4 << 30
	job.runBilled = 3 << 30
	job.attachJobID = "bq-job-123"
	var attachedBeforeComplete bool
	job.onAttach = func() {
		budget.mu.Lock()
		attachedBeforeComplete = budget.attachedJob == "bq-job-123" && budget.settledCalls == 0
		budget.mu.Unlock()
	}

	s.syncOnce(ctx)

	if !attachedBeforeComplete {
		t.Errorf("expected job ID to be attached before Run completed and settled")
	}
	if budget.settledCalls != 1 || budget.settledBytes != 3<<30 {
		t.Errorf("settledCalls=%d settledBytes=%d, want 1 and %d", budget.settledCalls, budget.settledBytes, int64(3<<30))
	}

	// A failed Run that billed bytes still settles the reservation.
	sFail, jobFail, _ := newTestDailySyncer(t, "btc_onchain", at(6, 0), 7)
	budgetFail := &fakeSyncBudget{}
	sFail.budget = budgetFail
	jobFail.dryBytes = 4 << 30
	jobFail.runBilled = 2 << 30
	jobFail.runErr = suspiciousEmpty("2026-09-25", "txs")
	sFail.syncOnce(ctx)
	if budgetFail.settledCalls != 1 || budgetFail.settledBytes != 2<<30 {
		t.Errorf("failed billed run settledCalls=%d settledBytes=%d, want 1 and %d",
			budgetFail.settledCalls, budgetFail.settledBytes, int64(2<<30))
	}
}

func TestDailyBQSyncerFailurePolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("policy A stops on 4th billed failure within 24h", func(t *testing.T) {
		cur := at(6, 0)
		s, job, store := newTestDailySyncer(t, "btc_onchain", cur, 7)
		s.now = func() time.Time { return cur }
		flags := newFakeSyncStopFlags()
		s.stopFlags = flags
		s.failurePolicy = syncFailurePolicy{
			maxBilledFailures: 4,
			within:            24 * time.Hour,
			backoff:           []time.Duration{3 * time.Hour, 6 * time.Hour, 12 * time.Hour},
		}
		job.runErr = suspiciousEmpty("2026-09-25", "contiguity_hole at height 975012")
		job.runBilled = 40 << 20

		// Unbilled events ("source not caught up", over_budget, unbilled BQ error) must never count.
		job.ready = false
		s.syncOnce(ctx)
		job.ready = true
		if recs, _ := flags.failures(ctx, "btc_onchain"); len(recs) != 0 {
			t.Fatalf("readiness miss recorded a failure: %+v", recs)
		}

		// Failure 1 at 0h -> backoff 3h.
		s.syncOnce(ctx)
		if _, stopped, _ := flags.stopped(ctx, "btc_onchain"); stopped {
			t.Fatal("stopped after 1st failure")
		}
		// At 2h: still backing off.
		cur = cur.Add(2 * time.Hour)
		s.syncOnce(ctx)
		if len(job.runCalls) != 1 {
			t.Fatalf("ran during 3h backoff: runCalls=%d", len(job.runCalls))
		}
		// Failure 2 at 3h -> backoff 6h.
		cur = cur.Add(1 * time.Hour)
		s.syncOnce(ctx)
		if len(job.runCalls) != 2 {
			t.Fatalf("runCalls at 3h = %d, want 2", len(job.runCalls))
		}
		// Failure 3 at 9h -> backoff 12h.
		cur = cur.Add(6 * time.Hour)
		s.syncOnce(ctx)
		if len(job.runCalls) != 3 {
			t.Fatalf("runCalls at 9h = %d, want 3", len(job.runCalls))
		}
		if _, stopped, _ := flags.stopped(ctx, "btc_onchain"); stopped {
			t.Fatal("stopped after 3rd failure")
		}
		// Failure 4 at 21h -> stopped!
		cur = cur.Add(12 * time.Hour)
		s.syncOnce(ctx)
		reason, stopped, _ := flags.stopped(ctx, "btc_onchain")
		if !stopped || !strings.Contains(reason, "--bitcoin-bq-resume btc_onchain") {
			t.Fatalf("stopped=%v reason=%q, want stopped with resume hint", stopped, reason)
		}
		st, _ := store.getSyncState(ctx, "btc_onchain")
		if !strings.Contains(st.LastError, "stopped after 4 billed failures") {
			t.Errorf("last_error = %q, want stop message", st.LastError)
		}

		// Subsequent ticks make no Ready/DryRun/Run calls while stopped.
		readyBefore := len(job.readyCalls)
		cur = cur.Add(24 * time.Hour)
		s.syncOnce(ctx)
		if len(job.readyCalls) != readyBefore || len(job.runCalls) != 4 {
			t.Fatalf("stopped job made BQ calls: ready=%d (was %d) run=%d", len(job.readyCalls), readyBefore, len(job.runCalls))
		}

		// Clearing the flag resumes the job, and a success clears the failure list.
		_ = flags.clear(ctx, "btc_onchain")
		job.runErr = nil
		s.syncOnce(ctx)
		if len(job.runCalls) != 5 {
			t.Fatalf("resumed job did not run: runCalls=%d, want 5", len(job.runCalls))
		}
		if recs, _ := flags.failures(ctx, "btc_onchain"); len(recs) != 0 {
			t.Errorf("failures after success = %+v, want empty", recs)
		}
	})

	t.Run("policy A 4 failures spread over >24h never stop", func(t *testing.T) {
		cur := at(6, 0)
		s, job, _ := newTestDailySyncer(t, "btc_onchain", cur, 7)
		s.now = func() time.Time { return cur }
		flags := newFakeSyncStopFlags()
		s.stopFlags = flags
		s.failurePolicy = syncFailurePolicy{
			maxBilledFailures: 4,
			within:            24 * time.Hour,
			backoff:           []time.Duration{3 * time.Hour, 6 * time.Hour, 12 * time.Hour},
		}
		job.runErr = suspiciousEmpty("2026-09-25", "txs")
		job.runBilled = 1 << 20

		for i := 0; i < 4; i++ {
			s.syncOnce(ctx)
			cur = cur.Add(10 * time.Hour) // 0h, 10h, 20h, 30h -> at 30h, 0h is outside 24h window
		}
		if _, stopped, _ := flags.stopped(ctx, "btc_onchain"); stopped {
			t.Fatal("expected 4 failures over 30h NOT to stop a 24h-window policy")
		}
	})

	t.Run("policy B stops on 2nd consecutive billed failure including timeout", func(t *testing.T) {
		cur := at(6, 0)
		s, job, _ := newTestDailySyncer(t, "btc_coinage", cur, 7)
		s.now = func() time.Time { return cur }
		flags := newFakeSyncStopFlags()
		s.stopFlags = flags
		s.failurePolicy = syncFailurePolicy{
			maxBilledFailures: 2,
			backoff:           []time.Duration{6 * time.Hour},
		}
		// Timeout with 0 reported billed bytes still counts as billed.
		job.runErr = context.DeadlineExceeded
		job.runBilled = 0
		s.syncOnce(ctx)
		if _, stopped, _ := flags.stopped(ctx, "btc_coinage"); stopped {
			t.Fatal("stopped on 1st failure")
		}

		cur = cur.Add(6 * time.Hour)
		s.syncOnce(ctx)
		if _, stopped, _ := flags.stopped(ctx, "btc_coinage"); !stopped {
			t.Fatal("expected stop on 2nd consecutive failure")
		}
	})
}

func TestDailyBQSyncerRealTimeoutSettlesAndStops(t *testing.T) {
	ctx := context.Background()
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, btcCoinageJobID, cur, 7)
	s.now = func() time.Time { return cur }

	settings := (BitcoinConfig{}).settings()
	settings.BudgetBytes = 1000 << 30 // 1,000 GiB
	if err := store.ensureBtcBQSchema(ctx); err != nil {
		t.Fatal(err)
	}
	budget := newBtcBudget(store, settings, func() time.Time { return cur })
	flags := newBtcStopFlags(store)

	s.budget = budget
	s.stopFlags = flags
	s.dryRunSizedCap = true
	s.hardCapBytes = 256 << 30
	s.timeout = 150 * time.Millisecond
	s.failurePolicy = btcJobFailurePolicy(btcCoinageJobID)

	job.dryBytes = 100 << 30 // est = 100 GiB -> maxBytes = 125 GiB
	job.runBilled = 0
	job.blockUntilCtxDone = true
	job.attachJobID = "bq-job-timeout-1"

	var reservedWhileInFlight int64
	job.onAttach = func() {
		if st, err := budget.status(context.Background(), cur); err == nil {
			reservedWhileInFlight = st.ReservedBytes
		}
	}

	// First real timeout: Run blocks until tctx expires.
	s.syncOnce(ctx)

	if reservedWhileInFlight != 125<<30 {
		t.Errorf("reservedWhileInFlight = %d, want %d (1.25x dryRun maxBytes)", reservedWhileInFlight, int64(125<<30))
	}
	bSt1, err := budget.status(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if bSt1.ReservedBytes != 0 {
		t.Errorf("ReservedBytes after 1st timeout = %d, want 0", bSt1.ReservedBytes)
	}
	if bSt1.SettledBytes != 100<<30 {
		t.Errorf("SettledBytes after 1st timeout = %d, want %d (est settled on timeout with billed==0)", bSt1.SettledBytes, int64(100<<30))
	}
	recs1, err := flags.failures(ctx, btcCoinageJobID)
	if err != nil || len(recs1) != 1 || recs1[0].BilledBytes != 100<<30 {
		t.Fatalf("SQLite failures after 1st timeout = %+v, err=%v; want 1 record with 100 GiB", recs1, err)
	}
	if _, stopped, _ := flags.stopped(ctx, btcCoinageJobID); stopped {
		t.Fatal("stopped after 1st timeout, want retry after 6h")
	}

	// Second real timeout 6 hours later -> must settle another 100 GiB and stop in SQLite.
	cur = cur.Add(6 * time.Hour)
	job.attachJobID = "bq-job-timeout-2"
	s.syncOnce(ctx)

	bSt2, err := budget.status(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if bSt2.ReservedBytes != 0 || bSt2.SettledBytes != 200<<30 {
		t.Errorf("budget after 2nd timeout = %+v, want reserved=0 settled=200 GiB", bSt2)
	}
	reason, stopped, err := flags.stopped(ctx, btcCoinageJobID)
	if err != nil || !stopped || !strings.Contains(reason, "--bitcoin-bq-resume btc_coinage") {
		t.Fatalf("SQLite stopped after 2nd timeout: stopped=%v reason=%q err=%v", stopped, reason, err)
	}
	syncSt, _ := store.getSyncState(ctx, btcCoinageJobID)
	if !strings.Contains(syncSt.LastError, "stopped after 2 consecutive billed failures") {
		t.Errorf("sync_state.LastError after 2nd timeout = %q, want stop message", syncSt.LastError)
	}
}

func TestDailyBQSyncerReservesMaxBytesCap(t *testing.T) {
	ctx := context.Background()
	cur := at(6, 0)
	s, job, store := newTestDailySyncer(t, btcCoinageJobID, cur, 7)

	settings := (BitcoinConfig{}).settings()
	settings.BudgetBytes = 90 << 30 // 90 GiB budget
	s.budget = newBtcBudget(store, settings, func() time.Time { return cur })
	s.dryRunSizedCap = true
	s.hardCapBytes = 256 << 30
	// est = 80 GiB fits under 90 GiB, but MaxBytesBilled = 1.25 * 80 GiB = 100 GiB exceeds 90 GiB.
	job.dryBytes = 80 << 30

	s.syncOnce(ctx)

	if len(job.runCalls) != 0 {
		t.Fatalf("Run called %d times when MaxBytesBilled (100 GiB) exceeds 90 GiB budget, want 0", len(job.runCalls))
	}
	st, _ := store.getSyncState(ctx, btcCoinageJobID)
	if st.LastError != "over_budget" {
		t.Errorf("LastError = %q, want over_budget", st.LastError)
	}
}
