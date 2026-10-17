package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

type fakeBaselineSource struct {
	dryBytes   int64
	rows       []btcUTXOBaselineRow
	billed     int64
	dryCalls   int
	fetchCalls int
	onFetch    func()
}

func (f *fakeBaselineSource) DryRunBaseline(ctx context.Context, end civil.Date) (int64, error) {
	f.dryCalls++
	return f.dryBytes, nil
}

func (f *fakeBaselineSource) FetchBaseline(ctx context.Context, end civil.Date, maxBytes int64) ([]btcUTXOBaselineRow, int64, error) {
	f.fetchCalls++
	if f.onFetch != nil {
		f.onFetch()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return f.rows, f.billed, nil
}

func TestBtcUTXOBaselineSQL(t *testing.T) {
	sqlText := btcUTXOBaselineSQL()
	for _, want := range []string{
		"CAST(value AS INT64) AS sats",
		"TIMESTAMP(@genesis_time)",
		"TIMESTAMP(@end_date)",
		"type != 'nulldata'",
		"IN UNNEST(@bip30_txids)",
		"IN UNNEST(@bip30_overwritten_times)",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("btcUTXOBaselineSQL missing %q", want)
		}
	}
}

func TestBtcBaselineCLIDryRun(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	src := &fakeBaselineSource{
		dryBytes: 687 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   680 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })
	plan, st, err := runner.runCLI(ctx, false, false)
	if err != nil {
		t.Fatalf("runCLI dry run: %v", err)
	}
	if plan.EstimatedBytes != 687<<30 {
		t.Fatalf("EstimatedBytes = %d, want %d", plan.EstimatedBytes, int64(687<<30))
	}
	if src.fetchCalls != 0 {
		t.Fatalf("fetchCalls = %d, want 0 on dry run", src.fetchCalls)
	}
	if st.Available {
		t.Fatalf("expected no baseline written on dry run, got %+v", st)
	}
	if _, ok, _ := store.getBtcPendingBaseline(ctx); ok {
		t.Fatalf("expected no baseline_pending flag on dry run")
	}
}

func TestBtcBaselineIntervalLock(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	s.BaselineAllowUI = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	src := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   100 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })

	// First run succeeds.
	if _, _, err := runner.runCLI(ctx, true, false); err != nil {
		t.Fatalf("first runCLI: %v", err)
	}

	// Second CLI run 5 days later (< 30d interval) without --force is refused.
	now = now.Add(5 * 24 * time.Hour)
	if _, _, err := runner.runCLI(ctx, true, false); !errors.Is(err, errBtcBaselineInterval) {
		t.Fatalf("expected errBtcBaselineInterval on CLI without --force, got %v", err)
	}

	// UI dryRun within interval is also refused.
	if _, err := runner.dryRun(ctx); !errors.Is(err, errBtcBaselineInterval) {
		t.Fatalf("expected errBtcBaselineInterval on UI dryRun, got %v", err)
	}

	// CLI --force bypasses the interval lock and creates generation 2.
	_, st, err := runner.runCLI(ctx, true, true)
	if err != nil {
		t.Fatalf("runCLI with --force: %v", err)
	}
	if st.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", st.Generation)
	}
}

func TestBtcBaselineBudget(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	s.BudgetBytes = 500 << 30 // 500 GiB budget
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	// Baseline needs 687 GiB > 500 GiB budget -> refused even with --force.
	src := &fakeBaselineSource{
		dryBytes: 687 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   687 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })
	if _, _, err := runner.runCLI(ctx, true, true); !errors.Is(err, errBtcOverBudget) {
		t.Fatalf("expected errBtcOverBudget even with --force, got %v", err)
	}
	if src.fetchCalls != 0 {
		t.Fatalf("expected 0 FetchBaseline calls when over budget, got %d", src.fetchCalls)
	}
}

func TestBtcBaselineConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "concurrent.db")
	store1, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer store1.db.Close()
	store2, err := openRiskStore(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.db.Close()

	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	var secondErr error
	src1 := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   100 << 30,
	}
	src2 := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   100 << 30,
	}
	runner1 := newBtcBaselineRunner(store1, s, src1, func() time.Time { return now })
	runner2 := newBtcBaselineRunner(store2, s, src2, func() time.Time { return now })

	// While runner1 is mid-FetchBaseline (holding an active reservation in SQLite),
	// runner2 on store2 attempts to run and must be refused with errBtcBaselineInFlight.
	src1.onFetch = func() {
		_, _, secondErr = runner2.runCLI(ctx, true, true)
	}
	if _, _, err := runner1.runCLI(ctx, true, false); err != nil {
		t.Fatalf("runner1: %v", err)
	}
	if !errors.Is(secondErr, errBtcBaselineInFlight) {
		t.Fatalf("runner2 error while runner1 active = %v, want errBtcBaselineInFlight", secondErr)
	}
	if src2.fetchCalls != 0 {
		t.Fatalf("runner2 fetchCalls = %d, want 0", src2.fetchCalls)
	}
}

func TestBtcBaselineHandoffAtomic(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	src := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 500_000_000, UTXOs: 3}},
		billed:   100 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })
	runner.beforeCommit = func() error {
		return errors.New("simulated crash before commit")
	}

	if _, _, err := runner.runCLI(ctx, true, false); err == nil {
		t.Fatalf("expected error from simulated crash before commit")
	}
	if gen, ok, _ := store.latestBtcBaselineGeneration(ctx); ok || gen != 0 {
		t.Fatalf("expected no baseline generation after rollback, got gen=%d ok=%v", gen, ok)
	}
	if _, ok, _ := store.getBtcPendingBaseline(ctx); ok {
		t.Fatalf("expected no baseline_pending flag after rollback")
	}
	if v, ok, _ := store.getBtcKV(ctx, btcLastBaselineAtKey); ok || v != "" {
		t.Fatalf("expected no last_baseline_at after rollback, got %q", v)
	}
	bSt, err := runner.budget.status(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if bSt.ReservedBytes != 0 || bSt.SettledBytes != 100<<30 {
		t.Fatalf("budget after commit failure = %+v, want reserved=0 and settled=%d (100 GiB)", bSt, int64(100<<30))
	}
}

func TestBtcBaselineRequiresJobs(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings() // Jobs: false
	src := &fakeBaselineSource{dryBytes: 100 << 30}
	runner := newBtcBaselineRunner(store, s, src, time.Now)

	if _, _, err := runner.runCLI(ctx, false, false); !errors.Is(err, errBtcJobsDisabled) {
		t.Fatalf("runCLI with Jobs=false = %v, want errBtcJobsDisabled", err)
	}
	if _, err := runner.dryRun(ctx); !errors.Is(err, errBtcJobsDisabled) {
		t.Fatalf("dryRun with Jobs=false = %v, want errBtcJobsDisabled", err)
	}
	if src.dryCalls != 0 {
		t.Fatalf("expected 0 dryRun calls when Jobs=false, got %d", src.dryCalls)
	}
}

func TestBtcBaselineTokenSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	s.BaselineAllowUI = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	src := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   100 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })

	plan, err := runner.dryRun(ctx)
	if err != nil || plan.Token == "" {
		t.Fatalf("dryRun: %v, plan=%+v", err, plan)
	}

	// First confirm succeeds.
	if err := runner.confirm(ctx, plan.Token); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	// Second confirm with the same token is rejected (single-use).
	if err := runner.confirm(ctx, plan.Token); !errors.Is(err, errBtcBaselineBadToken) {
		t.Fatalf("second confirm = %v, want errBtcBaselineBadToken", err)
	}

	// Advance past the 30-day interval lock, mint a new token, then let it expire (> 15 min).
	now = now.Add(31 * 24 * time.Hour)
	plan2, err := runner.dryRun(ctx)
	if err != nil {
		t.Fatalf("dryRun 2: %v", err)
	}
	now = now.Add(16 * time.Minute)
	if err := runner.confirm(ctx, plan2.Token); !errors.Is(err, errBtcBaselineBadToken) {
		t.Fatalf("expired confirm = %v, want errBtcBaselineBadToken", err)
	}
}

func TestBtcBaselineReconciliation(t *testing.T) {
	// At height 100: scheduled supply = 101 * 50 BTC = 505,000,000,000 sats.
	// Minus genesis (5,000,000,000), unclaimed (10,000), opret (5,000) -> expected = 499,999,985,000 sats.
	expected, delta, suspect := reconcileBtcBaseline(499_999_985_000, 100, 10_000, 5_000)
	if expected != 499_999_985_000 {
		t.Fatalf("expected = %d, want 499,999,985,000", expected)
	}
	if delta == nil || *delta != 0 || suspect {
		t.Fatalf("exact match: delta=%v suspect=%v", delta, suspect)
	}

	// Deviation > 0.01% -> suspect = true.
	_, delta2, suspect2 := reconcileBtcBaseline(499_800_000_000, 100, 10_000, 5_000)
	if delta2 == nil || !suspect2 {
		t.Fatalf("expected suspect=true for >0.01%% deviation, got delta=%v suspect=%v", delta2, suspect2)
	}
}

func TestBtcBaselineConfirmSurvivesCallerCancel(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	s.BaselineAllowUI = true
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	callerCtx, callerCancel := context.WithCancel(ctx)
	defer callerCancel()

	src := &fakeBaselineSource{
		dryBytes: 100 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   98 << 30,
		onFetch: func() {
			// Simulate browser disconnect / HTTP write timeout while FetchBaseline is running.
			callerCancel()
		},
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })
	plan, err := runner.dryRun(ctx)
	if err != nil {
		t.Fatalf("dryRun: %v", err)
	}

	if err := runner.confirm(callerCtx, plan.Token); err != nil {
		t.Fatalf("confirm failed when caller context canceled mid-query: %v", err)
	}
	if gen, ok, _ := store.latestBtcBaselineGeneration(ctx); !ok || gen != 1 {
		t.Fatalf("latestBtcBaselineGeneration = %d, %v; want 1, true", gen, ok)
	}
	bSt, err := runner.budget.status(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if bSt.ReservedBytes != 0 || bSt.SettledBytes != 98<<30 {
		t.Fatalf("budget after confirm = %+v, want reserved=0 settled=98 GiB", bSt)
	}
}

func TestBtcBaselineReservesSizedMaxCap(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	s := (BitcoinConfig{}).settings()
	s.Jobs = true
	s.BaselineAllowUI = true
	// Budget is 800 GiB: dryBytes (687 GiB) < 800 GiB, but MaxBytesBilled (ceil(1.25 * 687 GiB) = 858.75 GiB) > 800 GiB.
	s.BudgetBytes = 800 << 30
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	src := &fakeBaselineSource{
		dryBytes: 687 << 30,
		rows:     []btcUTXOBaselineRow{{CreateDay: "2020-01-01", Sats: 100_000_000, UTXOs: 1}},
		billed:   687 << 30,
	}
	runner := newBtcBaselineRunner(store, s, src, func() time.Time { return now })

	if _, err := runner.dryRun(ctx); !errors.Is(err, errBtcOverBudget) {
		t.Fatalf("dryRun when maxCap (858.75 GiB) > budget (800 GiB) = %v, want errBtcOverBudget", err)
	}
	if _, _, err := runner.runCLI(ctx, true, true); !errors.Is(err, errBtcOverBudget) {
		t.Fatalf("runCLI when maxCap (858.75 GiB) > budget (800 GiB) = %v, want errBtcOverBudget", err)
	}
}
