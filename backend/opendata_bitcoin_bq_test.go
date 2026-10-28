package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBtcBudgetReservationAcrossHandles(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "security.db")
	h1, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.db.Close()
	h2, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.db.Close()

	settings := (BitcoinConfig{}).settings()
	settings.BudgetBytes = 100 << 30 // 100 GiB
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	b1 := newBtcBudget(h1, settings, func() time.Time { return now })
	b2 := newBtcBudget(h2, settings, func() time.Time { return now })
	if err := h1.ensureBtcBQSchema(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		wg         sync.WaitGroup
		id1, id2   string
		err1, err2 error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		id1, err1 = b1.reserve(ctx, btcCoinageJobID, 60<<30)
	}()
	go func() {
		defer wg.Done()
		id2, err2 = b2.reserve(ctx, btcBaselineJobID, 60<<30)
	}()
	wg.Wait()

	ok1 := err1 == nil && id1 != ""
	ok2 := err2 == nil && id2 != ""
	if ok1 == ok2 {
		t.Fatalf("concurrent 60 GiB reservations on 100 GiB budget: ok1=%v (%v) ok2=%v (%v); want exactly one to succeed",
			ok1, err1, ok2, err2)
	}
	st, err := b1.status(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReservedBytes != 60<<30 || st.RemainingBytes != 40<<30 {
		t.Errorf("budget status after reservation = %+v, want reserved 60 GiB and remaining 40 GiB", st)
	}
}

func TestBtcReservationStaleRecovery(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	settings := (BitcoinConfig{}).settings()
	settings.BudgetBytes = 1000 << 30
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	b := newBtcBudget(store, settings, func() time.Time { return t0 })

	// Reservation 1: has a BigQuery job ID -> settles 40 GiB from lookup.
	idWithJob, err := b.reserve(ctx, btcCoinageJobID, 100<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.attachJob(ctx, idWithJob, "bq-job-settled"); err != nil {
		t.Fatal(err)
	}

	// Reservation 2: no job ID -> settles its reserved 10 GiB conservatively.
	if _, err := b.reserve(ctx, btcOnchainJobID, 10<<30); err != nil {
		t.Fatal(err)
	}

	// Advance past expiry (2 hours later).
	later := t0.Add(2 * time.Hour)
	err = b.recoverStale(ctx, later, func(jobID string) (int64, bool, error) {
		if jobID == "bq-job-settled" {
			return 40 << 30, true, nil
		}
		return 0, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	st, err := b.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReservedBytes != 0 {
		t.Errorf("ReservedBytes after recovery = %d, want 0", st.ReservedBytes)
	}
	if st.SettledBytes != 50<<30 {
		t.Errorf("SettledBytes after recovery = %d, want %d (40 GiB + 10 GiB)", st.SettledBytes, int64(50<<30))
	}
}

func TestStableBudgetSuite(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "security.db")
	h1, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.db.Close()
	h2, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.db.Close()

	cfg := bqBudgetConfig{TablePrefix: bqPrefixStable, BudgetBytes: 100 << 30}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	b1 := newBqBudget(h1, cfg, func() time.Time { return now })
	b2 := newBqBudget(h2, cfg, func() time.Time { return now })
	if err := h1.ensureStableSchema(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		wg         sync.WaitGroup
		id1, id2   string
		err1, err2 error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		id1, err1 = b1.reserve(ctx, "stable_eth", 60<<30)
	}()
	go func() {
		defer wg.Done()
		id2, err2 = b2.reserve(ctx, "stable_tron", 60<<30)
	}()
	wg.Wait()

	ok1 := err1 == nil && id1 != ""
	ok2 := err2 == nil && id2 != ""
	if ok1 == ok2 {
		t.Fatalf("concurrent 60 GiB reservations on 100 GiB stable_ budget: ok1=%v (%v) ok2=%v (%v); want exactly one to succeed",
			ok1, err1, ok2, err2)
	}
	st, err := b1.status(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReservedBytes != 60<<30 || st.RemainingBytes != 40<<30 {
		t.Errorf("stable_ budget status after reservation = %+v, want reserved 60 GiB and remaining 40 GiB", st)
	}

	// Verify isolation from btc_budget / btc_reservations and stale recovery.
	winID := id1
	if !ok1 {
		winID = id2
	}
	if err := b1.attachJob(ctx, winID, "bq-stable-settled"); err != nil {
		t.Fatal(err)
	}
	if _, err := b1.reserve(ctx, "stable_tron", 10<<30); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Hour)
	if err := b1.recoverStale(ctx, later, func(jobID string) (int64, bool, error) {
		if jobID == "bq-stable-settled" {
			return 25 << 30, true, nil
		}
		return 0, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err = b1.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReservedBytes != 0 || st.SettledBytes != 35<<30 {
		t.Errorf("stable_ budget after stale recovery = %+v, want reserved 0 and settled 35 GiB", st)
	}
	var btcCount int
	if err := h1.ensureBtcBQSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h1.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM btc_budget`).Scan(&btcCount); err != nil || btcCount != 0 {
		t.Errorf("btc_budget rows = %d (%v), want 0 (stable_ budget must use stable_budget table)", btcCount, err)
	}
}

func TestBitcoinSyncJobsRecoversStaleReservations(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	settings.BudgetBytes = 1000 << 30
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := t0

	runner := newBtcBQRunner(settings, store, &fakeBtcRunnerRollupSource{}, &fakeBtcRunnerCoinAgeSource{}, nil, func() time.Time { return clock })
	if runner == nil {
		t.Fatal("expected non-nil runner when Jobs=true")
	}

	// 1. Reservation with an attached BigQuery job ID (process crashed mid-query).
	idWithJob, err := runner.budget.reserve(ctx, btcCoinageJobID, 125<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.budget.attachJob(ctx, idWithJob, "bq-job-crashed-1"); err != nil {
		t.Fatal(err)
	}
	// 2. Reservation without a BigQuery job ID (process crashed before attachJob).
	if _, err := runner.budget.reserve(ctx, btcOnchainJobID, 2<<30); err != nil {
		t.Fatal(err)
	}

	// Advance 2 hours past TTL expiry.
	later := t0.Add(2 * time.Hour)
	clock = later

	// Even before recovery runs, a reservation with an attached bq_job_id
	// must NOT vanish from ReservedBytes when its TTL expires.
	preSt, err := runner.budget.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if preSt.ReservedBytes != 125<<30 {
		t.Errorf("ReservedBytes before recovery = %d, want %d (submitted job must stay accounted until settled)",
			preSt.ReservedBytes, int64(125<<30))
	}

	// Wire jobLookup and run the production recovery step: the first function
	// of the hourly loop records the tick and recovers stale reservations.
	runner.jobLookup = func(jobID string) (int64, bool, error) {
		if jobID == "bq-job-crashed-1" {
			return 95 << 30, true, nil
		}
		return 0, false, nil
	}
	runner.scheduledJobs()[0](ctx)

	postSt, err := runner.budget.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if postSt.ReservedBytes != 0 {
		t.Errorf("ReservedBytes after recovery = %d, want 0", postSt.ReservedBytes)
	}
	if postSt.SettledBytes != 97<<30 {
		t.Errorf("SettledBytes after recovery = %d, want %d (95 GiB + 2 GiB)", postSt.SettledBytes, int64(97<<30))
	}
	if !runner.loopTickAt().Equal(later) {
		t.Errorf("loop tick = %v, want %v (next_check_at is derived from it)", runner.loopTickAt(), later)
	}
}

func TestBtcFeasibilityWarning(t *testing.T) {
	var cfg BitcoinConfig
	cfg.BigQuery.MonthlyBudgetGiB = 256
	cfg.BigQuery.CoinAge.CadenceDays = 7
	s := cfg.settings()

	expGiB, warn := btcFeasibility(s, 0, 0)
	if int(expGiB+0.5) != 531 {
		t.Errorf("expectedGiB = %.2f, want ~531", expGiB)
	}
	const want = "budget 256 GiB < expected 531 GiB/month: coin age will pause around day 21"
	if warn != want {
		t.Errorf("warning = %q, want %q", warn, want)
	}
}

func TestBtcFeasibilityDefaultsOK(t *testing.T) {
	s := (BitcoinConfig{}).settings()
	expGiB, warn := btcFeasibility(s, 0, 0)
	if warn != "" {
		t.Errorf("unexpected warning on default 2048 GiB budget: %q (expectedGiB=%.2f)", warn, expGiB)
	}
}

func TestBtcBQRunnerBuiltWithScheduleOff(t *testing.T) {
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings() // Jobs defaults to false
	runner := newBtcBQRunner(s, store, &fakeBtcRunnerRollupSource{}, &fakeBtcRunnerCoinAgeSource{}, nil, nil)
	// Run now works with the schedule off, so both syncers must exist...
	if runner == nil || runner.syncers[btcOnchainJobID] == nil || runner.syncers[btcCoinageJobID] == nil {
		t.Fatalf("schedule off: runner = %+v, want btc_onchain and btc_coinage syncers for manual runs", runner)
	}
	// ...but nothing may be scheduled.
	if fns := runner.scheduledJobs(); len(fns) != 0 {
		t.Fatalf("scheduledJobs with jobs=false returned %d funcs, want 0", len(fns))
	}
	if r := newBtcBQRunner(s, nil, &fakeBtcRunnerRollupSource{}, &fakeBtcRunnerCoinAgeSource{}, nil, nil); r != nil {
		t.Fatalf("newBtcBQRunner with nil store returned non-nil: %+v", r)
	}
	if r := newBtcBQRunnerFromClient(s, store, nil, nil); r != nil {
		t.Fatalf("newBtcBQRunnerFromClient with nil client returned non-nil: %+v", r)
	}
}

func TestBitcoinJobsNotWiredWhenDisabled(t *testing.T) {
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings() // Jobs defaults to false
	if fns := newBtcBQRunnerFromClient(s, store, &BQClient{}, nil).scheduledJobs(); len(fns) != 0 {
		t.Fatalf("scheduledJobs with jobs:false returned %d funcs, want 0", len(fns))
	}
	s.Jobs = true
	if fns := newBtcBQRunnerFromClient(s, nil, &BQClient{}, nil).scheduledJobs(); len(fns) != 0 {
		t.Fatalf("scheduledJobs with nil store returned %d funcs, want 0", len(fns))
	}
	if fns := newBtcBQRunnerFromClient(s, store, &BQClient{}, nil).scheduledJobs(); len(fns) == 0 {
		t.Fatal("scheduledJobs with jobs:true and valid store/client returned 0 funcs")
	}
}

func TestBtcResumeCLI(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	flags := newBtcStopFlags(store)

	if err := flags.recordFailure(ctx, btcOnchainJobID, syncFailureRecord{
		At:          time.Now().UTC(),
		Message:     "contiguity_hole",
		BilledBytes: 40 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	if err := flags.stop(ctx, btcOnchainJobID, "stopped after 4 billed failures"); err != nil {
		t.Fatal(err)
	}
	_ = store.setSyncError(ctx, btcOnchainJobID, "stopped after 4 billed failures")

	if err := btcResumeJob(ctx, store, btcOnchainJobID); err != nil {
		t.Fatal(err)
	}
	if _, stopped, _ := flags.stopped(ctx, btcOnchainJobID); stopped {
		t.Error("expected stopped flag to be cleared")
	}
	if recs, _ := flags.failures(ctx, btcOnchainJobID); len(recs) != 0 {
		t.Errorf("expected failures list to be cleared, got %+v", recs)
	}
	st, _ := store.getSyncState(ctx, btcOnchainJobID)
	if st.LastError != "" {
		t.Errorf("LastError after resume = %q, want empty", st.LastError)
	}
}

func TestBtcJobStatusStates(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	// 1. jobs: false -> "off"; no schedule, so no next check.
	sOff := (BitcoinConfig{}).settings()
	rsOff := newBtcReadStore(store, sOff, func() time.Time { return now })
	st, err := rsOff.jobStatus(ctx, btcOnchainJobID)
	if err != nil || st.State != "off" {
		t.Fatalf("jobs=false state = %+v, %v; want off", st, err)
	}
	if st.ScheduleEnabled || st.NextCheckAt != nil {
		t.Errorf("jobs=false schedule_enabled=%v next_check_at=%v, want false and nil", st.ScheduleEnabled, st.NextCheckAt)
	}
	// A UTC day settles at 06:30 UTC (6h block-timestamp slack + finality): at
	// 12:00 today's settle has passed, so the next one is tomorrow.
	if st.NextNewDayAt != "2026-10-18T06:30:00Z" {
		t.Errorf("next_new_day_at = %q, want 2026-10-18T06:30:00Z", st.NextNewDayAt)
	}

	// 2. jobs: true, no cursor -> "cold_start", next check one hour out.
	sOn := sOff
	sOn.Jobs = true
	rs := newBtcReadStore(store, sOn, func() time.Time { return now })
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	if st.State != "cold_start" {
		t.Errorf("no-cursor state = %q, want cold_start", st.State)
	}
	if !st.ScheduleEnabled || st.NextCheckAt == nil || *st.NextCheckAt != "2026-10-17T13:00:00Z" {
		t.Errorf("jobs=true schedule_enabled=%v next_check_at=%v, want true and 2026-10-17T13:00:00Z", st.ScheduleEnabled, st.NextCheckAt)
	}

	// 3. cursor set -> "ready"
	if err := store.applyScamBatch(ctx, btcOnchainJobID, "btc", nil, "2025-10-17", "2026-10-16", now); err != nil {
		t.Fatal(err)
	}
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	if st.State != "ready" || st.SyncedThrough != "2026-10-16" {
		t.Errorf("ready state = %+v, want ready thru 2026-10-16", st)
	}
	// Data from a manual run with the schedule off also reads "ready".
	if st, _ = rsOff.jobStatus(ctx, btcOnchainJobID); st.State != "ready" || st.ScheduleEnabled {
		t.Errorf("jobs=false with cursor = %+v, want ready with schedule_enabled=false", st)
	}

	// 4. billed failure recorded -> "retrying" with Failures=1 and NextRunAfter=+3h
	flags := newBtcStopFlags(store)
	_ = flags.recordFailure(ctx, btcOnchainJobID, syncFailureRecord{
		At:          now,
		Message:     "contiguity_hole",
		BilledBytes: 40 << 20,
	})
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	wantNext := now.Add(3 * time.Hour).Format(time.RFC3339)
	if st.State != "retrying" || st.Failures != 1 || st.NextRunAfter != wantNext {
		t.Errorf("retrying state = %+v, want retrying failures=1 next=%s", st, wantNext)
	}

	// 5. stopped -> "stopped" with StopReason
	_ = flags.stop(ctx, btcOnchainJobID, "stopped after 4 billed failures — run --bitcoin-bq-resume btc_onchain")
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	if st.State != "stopped" || !strings.Contains(st.StopReason, "--bitcoin-bq-resume") {
		t.Errorf("stopped state = %+v", st)
	}
}

func TestCryptoLatestRowTimeAtSQL(t *testing.T) {
	sqlAt, err := cryptoReadyAtSQL("btc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlAt, "block_timestamp >= @at") || !strings.Contains(sqlAt, "TIMESTAMP_ADD(@at, INTERVAL 2 HOUR)") {
		t.Errorf("cryptoReadyAtSQL = %s, want @at and 2 HOUR interval", sqlAt)
	}
	// Verify original Crypto Pulse probe is unchanged.
	sqlOrig, err := cryptoReadySQL("btc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlOrig, "TIMESTAMP(@end_date)") {
		t.Errorf("cryptoReadySQL changed unexpectedly: %s", sqlOrig)
	}
}
