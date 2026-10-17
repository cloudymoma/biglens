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

	// Reservation 2: no job ID -> released with 0 billed bytes.
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
	if st.SettledBytes != 40<<30 {
		t.Errorf("SettledBytes after recovery = %d, want %d (40 GiB)", st.SettledBytes, int64(40<<30))
	}
}

func TestBitcoinSyncJobsRecoversStaleReservations(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	settings.BudgetBytes = 1000 << 30
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	jobs := newBitcoinJobs(settings, store, &BQClient{})
	if jobs == nil {
		t.Fatal("expected non-nil btcJobs when Jobs=true")
	}
	jobs.now = func() time.Time { return t0 }
	jobs.budget.now = jobs.now

	// 1. Reservation with an attached BigQuery job ID (process crashed mid-query).
	idWithJob, err := jobs.budget.reserve(ctx, btcCoinageJobID, 125<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.budget.attachJob(ctx, idWithJob, "bq-job-crashed-1"); err != nil {
		t.Fatal(err)
	}
	// 2. Reservation without a BigQuery job ID (process crashed before q.Run).
	if _, err := jobs.budget.reserve(ctx, btcOnchainJobID, 2<<30); err != nil {
		t.Fatal(err)
	}

	// Advance 2 hours past TTL expiry.
	later := t0.Add(2 * time.Hour)
	jobs.now = func() time.Time { return later }
	jobs.budget.now = jobs.now

	// Even before recoverStaleOnce runs, a reservation with an attached bq_job_id
	// must NOT vanish from ReservedBytes when its TTL expires.
	preSt, err := jobs.budget.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if preSt.ReservedBytes != 125<<30 {
		t.Errorf("ReservedBytes before recovery = %d, want %d (submitted job must stay accounted until settled)",
			preSt.ReservedBytes, int64(125<<30))
	}

	// Wire jobLookup and run the production recovery step exposed by bitcoinSyncJobs.
	jobs.jobLookup = func(jobID string) (int64, bool, error) {
		if jobID == "bq-job-crashed-1" {
			return 95 << 30, true, nil
		}
		return 0, false, nil
	}
	jobs.recoverStaleOnce(ctx)

	postSt, err := jobs.budget.status(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if postSt.ReservedBytes != 0 {
		t.Errorf("ReservedBytes after recoverStaleOnce = %d, want 0", postSt.ReservedBytes)
	}
	if postSt.SettledBytes != 95<<30 {
		t.Errorf("SettledBytes after recoverStaleOnce = %d, want %d (95 GiB)", postSt.SettledBytes, int64(95<<30))
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

func TestNewBitcoinJobsDisabled(t *testing.T) {
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings() // Jobs defaults to false
	if jobs := newBitcoinJobs(s, store, &BQClient{}); jobs != nil {
		t.Fatalf("newBitcoinJobs with Jobs=false returned non-nil: %+v", jobs)
	}
	s.Jobs = true
	if jobs := newBitcoinJobs(s, nil, &BQClient{}); jobs != nil {
		t.Fatalf("newBitcoinJobs with nil store returned non-nil: %+v", jobs)
	}
}

func TestBitcoinJobsNotWiredWhenDisabled(t *testing.T) {
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings() // Jobs defaults to false
	if fns := bitcoinSyncJobs(s, store, &BQClient{}); len(fns) != 0 {
		t.Fatalf("bitcoinSyncJobs with jobs:false returned %d funcs, want 0", len(fns))
	}
	s.Jobs = true
	if fns := bitcoinSyncJobs(s, nil, &BQClient{}); len(fns) != 0 {
		t.Fatalf("bitcoinSyncJobs with nil store returned %d funcs, want 0", len(fns))
	}
	if fns := bitcoinSyncJobs(s, store, &BQClient{}); len(fns) == 0 {
		t.Fatal("bitcoinSyncJobs with jobs:true and valid store/client returned 0 funcs")
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

	// 1. jobs: false -> "off"
	sOff := (BitcoinConfig{}).settings()
	rsOff := newBtcReadStore(store, sOff, func() time.Time { return now })
	st, err := rsOff.jobStatus(ctx, btcOnchainJobID)
	if err != nil || st.State != "off" {
		t.Fatalf("jobs=false state = %+v, %v; want off", st, err)
	}

	// 2. jobs: true, no cursor -> "cold_start"
	sOn := sOff
	sOn.Jobs = true
	rs := newBtcReadStore(store, sOn, func() time.Time { return now })
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	if st.State != "cold_start" {
		t.Errorf("no-cursor state = %q, want cold_start", st.State)
	}

	// 3. cursor set -> "ready"
	if err := store.applyScamBatch(ctx, btcOnchainJobID, "btc", nil, "2025-10-17", "2026-10-16", now); err != nil {
		t.Fatal(err)
	}
	st, _ = rs.jobStatus(ctx, btcOnchainJobID)
	if st.State != "ready" || st.SyncedThrough != "2026-10-16" {
		t.Errorf("ready state = %+v, want ready thru 2026-10-16", st)
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
