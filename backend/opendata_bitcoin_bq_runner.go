package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/civil"
)

var (
	errBtcCoinAgeDisabled = errors.New("btc_coinage is disabled: set bitcoin.bigquery.coin_age.enabled: true in conf.yaml")
	errBtcGapTooLarge     = errors.New("gap_too_large")
)

const (
	btcManualCooldown     = 5 * time.Minute
	btcManualNoOpCooldown = time.Minute
	btcScheduleCadence    = time.Hour
	// btcSettleDelay is how long after 00:00 UTC the previous UTC day becomes
	// fetchable: Job A and Job B wait until BigQuery has ingested past
	// window_end + 6h (MTP-11 timestamp slack), plus the shared finality margin.
	btcSettleDelay = btcInversionSlack + stablecoinFinality

	btcBQActionHeader = "X-BigLens-Action"
	btcBQActionValue  = "bitcoin-bq"

	btcPricingNote = "Estimated at $6.25/TiB; BigQuery bills actual bytes scanned."
)

type btcJobLastRun struct {
	At          string `json:"at"`
	Trigger     string `json:"trigger"`
	Result      string `json:"result"`
	BytesBilled int64  `json:"bytes_billed"`
}

// BtcBQEstimateResponse is the free dry-run preview of the next window a
// Bitcoin BigQuery job would fetch.
type BtcBQEstimateResponse struct {
	Job            string  `json:"job"`
	WindowFrom     string  `json:"window_from"`
	WindowTo       string  `json:"window_to"`
	EstimatedBytes int64   `json:"estimated_bytes"`
	EstimatedUSD   float64 `json:"estimated_usd"`
	// ReserveBytes is the budget reservation (and MaximumBytesBilled cap) the
	// run holds while in flight: 1.25× the estimate, at least 1 GiB.
	ReserveBytes     int64  `json:"reserve_bytes"`
	PricingNote      string `json:"pricing_note"`
	BudgetUsedBytes  int64  `json:"budget_used_bytes"`
	BudgetLimitBytes int64  `json:"budget_limit_bytes"`
	FitsBudget       bool   `json:"fits_budget"`
	FitsHardCap      bool   `json:"fits_hard_cap"`
	NothingToDo      bool   `json:"nothing_to_do"`
}

func btcJobScheduleEnabled(settings btcSettings, job string) bool {
	return settings.Jobs && (job != btcCoinageJobID || settings.CoinAgeEnabled)
}

// btcSettledEnd returns the exclusive end day of the newest window Job A and
// Job B can fetch at now: today once the 6h slack and finality margin have
// passed (≈ 06:30 UTC), otherwise yesterday. It mirrors safeEndForJob without
// spending a Ready query.
func btcSettledEnd(now time.Time) civil.Date {
	now = now.UTC()
	today := civil.DateOf(now)
	if now.Before(dayStart(today).Add(btcSettleDelay)) {
		return today.AddDays(-1)
	}
	return today
}

// btcNextNewDayAt is the next time a UTC day settles (≈ 06:30 UTC).
func btcNextNewDayAt(now time.Time) string {
	now = now.UTC()
	t := dayStart(civil.DateOf(now)).Add(btcSettleDelay)
	if !now.Before(t) {
		t = t.Add(24 * time.Hour)
	}
	return t.Format(time.RFC3339)
}

// btcNextCheckAt is the next hourly schedule tick after lastTick (or now when
// the loop has not ticked yet). btc_coinage only fetches once cadence_days new
// days have settled, so its next check is the first tick after that window is due.
func btcNextCheckAt(job string, settings btcSettings, cursor string, now, lastTick time.Time) time.Time {
	base := now
	if !lastTick.IsZero() {
		base = lastTick
	}
	next := base.Add(btcScheduleCadence)
	if job != btcCoinageJobID || cursor == "" || settings.CadenceDays <= 1 {
		return next.UTC()
	}
	cur, err := civil.ParseDate(cursor)
	if err != nil {
		return next.UTC()
	}
	due := dayStart(cur.AddDays(1 + settings.CadenceDays)).Add(btcSettleDelay)
	if due.After(next) {
		ticks := math.Ceil(float64(due.Sub(base)) / float64(btcScheduleCadence))
		next = base.Add(time.Duration(ticks) * btcScheduleCadence)
	}
	return next.UTC()
}

func loadBtcLastRun(ctx context.Context, store *riskStore, job string) (btcJobLastRun, bool) {
	if store == nil {
		return btcJobLastRun{}, false
	}
	raw, ok, err := store.getBtcKV(ctx, "last_run:"+job)
	if err != nil || !ok || raw == "" {
		return btcJobLastRun{}, false
	}
	var lr btcJobLastRun
	if err := json.Unmarshal([]byte(raw), &lr); err != nil || lr.At == "" {
		return btcJobLastRun{}, false
	}
	return lr, true
}

func applyBtcLastRun(st *BtcBQStatus, lr btcJobLastRun) {
	st.LastRunAt = lr.At
	st.LastRunTrigger = lr.Trigger
	st.LastRunResult = lr.Result
	b := lr.BytesBilled
	st.LastRunBytesBilled = &b
}

// btcBQRunner owns the btc_onchain and btc_coinage syncers for both scheduled
// hourly ticks and manual one-off runs. Syncers are built even when the
// schedule is off (bitcoin.bigquery.jobs: false). The UTXO baseline keeps its
// own CLI / allow_ui flow (btcBaselineRunner).
type btcBQRunner struct {
	mu           sync.Mutex
	settings     btcSettings
	store        *riskStore
	bq           *BQClient
	budget       *btcBudget
	flags        *btcStopFlags
	maint        *btcHolderMaintenance
	now          func() time.Time
	syncers      map[string]*dailyBQSyncer
	running      map[string]bool
	lastManualAt map[string]time.Time
	lastLoopTick time.Time
	lastRun      map[string]btcJobLastRun
	jobLookup    func(jobID string) (int64, bool, error)
	runCtx       context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func newBtcBQRunner(settings btcSettings, store *riskStore, rollupSrc btcRollupQuerySource, coinAgeSrc btcCoinAgeQuerySource, prices *btcPriceSyncer, now func() time.Time) *btcBQRunner {
	if store == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	budget := newBtcBudget(store, settings, now)
	flags := newBtcStopFlags(store)
	maint := newBtcHolderMaintenance(store, prices, now)

	rollupSyncer := &dailyBQSyncer{
		store: store,
		job: &btcOnchainJob{
			store:     store,
			src:       rollupSrc,
			minTxs:    btcRollupMinDailyTxs,
			minInputs: btcRollupMinDailyInputs,
		},
		now:            now,
		initialDays:    settings.InitialDays,
		name:           btcOnchainJobID,
		hardCapBytes:   256 << 30,
		dryRunSizedCap: true,
		maxWindowDays:  31,
		chunkCatchUp:   true,
		timeout:        10 * time.Minute,
		prune:          func(context.Context, civil.Date) error { return nil },
		budget:         budget,
		failurePolicy:  btcJobFailurePolicy(btcOnchainJobID),
		stopFlags:      flags,
	}
	retentionDays := settings.CoinAgeInitialDays + 30
	stateAsOf := func() string {
		v, _ := store.getBtcUTXOStateAsOf(context.Background())
		return v
	}
	coinAgeSyncer := &dailyBQSyncer{
		store: store,
		job: &btcCoinAgeJob{
			store:        store,
			src:          coinAgeSrc,
			minInputs:    btcCoinAgeMinDailyInputs,
			whaleMinSats: settings.WhaleMinSats,
			whalesPerDay: settings.WhalesPerDay,
			onChange:     maint.runOnce,
		},
		now:            now,
		initialDays:    settings.CoinAgeInitialDays,
		name:           btcCoinageJobID,
		hardCapBytes:   256 << 30,
		dryRunSizedCap: true,
		maxWindowDays:  731,
		minWindowDays:  settings.CadenceDays,
		timeout:        30 * time.Minute,
		prune:          pruneBtcCoinAge(store, retentionDays, stateAsOf),
		budget:         budget,
		failurePolicy:  btcJobFailurePolicy(btcCoinageJobID),
		stopFlags:      flags,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	return &btcBQRunner{
		settings:     settings,
		store:        store,
		budget:       budget,
		flags:        flags,
		maint:        maint,
		now:          now,
		syncers:      map[string]*dailyBQSyncer{btcOnchainJobID: rollupSyncer, btcCoinageJobID: coinAgeSyncer},
		running:      map[string]bool{},
		lastManualAt: map[string]time.Time{},
		lastRun:      map[string]btcJobLastRun{},
		runCtx:       runCtx,
		cancel:       cancel,
	}
}

// newBtcBQRunnerFromClient wires the runner to BigQuery: production query
// sources, and stale-reservation recovery through jobs.get (0 bytes billed).
func newBtcBQRunnerFromClient(settings btcSettings, store *riskStore, client *BQClient, prices *btcPriceSyncer) *btcBQRunner {
	if store == nil || client == nil {
		return nil
	}
	r := newBtcBQRunner(
		settings,
		store,
		bqRollupSource{bq: client},
		bqCoinAgeSource{bq: client, whaleMinSats: settings.WhaleMinSats, whalesPerDay: settings.WhalesPerDay},
		prices,
		time.Now,
	)
	r.bq = client
	return r
}

func (r *btcBQRunner) Wait() {
	if r == nil {
		return
	}
	r.wg.Wait()
}

func (r *btcBQRunner) Cancel() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *btcBQRunner) Shutdown() {
	if r == nil {
		return
	}
	r.Cancel()
	r.wg.Wait()
}

func (r *btcBQRunner) manualCtx() context.Context {
	if r == nil {
		return context.Background()
	}
	r.mu.Lock()
	ctx := r.runCtx
	r.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (r *btcBQRunner) isRunning(job string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[job]
}

func (r *btcBQRunner) loopTickAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLoopTick
}

func (r *btcBQRunner) recordLoopTick(t time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastLoopTick = t.UTC()
	r.mu.Unlock()
}

func (r *btcBQRunner) getLastRun(job string) (btcJobLastRun, bool) {
	if r == nil {
		return btcJobLastRun{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.lastRun[job]
	return lr, ok
}

func (r *btcBQRunner) recordLastRun(ctx context.Context, job string, lr btcJobLastRun) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastRun[job] = lr
	r.mu.Unlock()
	if r.store != nil {
		if b, err := json.Marshal(lr); err == nil {
			_ = r.store.setBtcKV(ctx, "last_run:"+job, string(b))
		}
	}
}

// decorateStatus overlays the runner's in-memory state (running flag, last
// loop tick and last run) on a job status read from SQLite.
func (r *btcBQRunner) decorateStatus(st *BtcBQStatus) {
	if r == nil || st == nil {
		return
	}
	st.Running = r.isRunning(st.Job)
	if lr, ok := r.getLastRun(st.Job); ok {
		applyBtcLastRun(st, lr)
	}
	if st.ScheduleEnabled {
		if lt := r.loopTickAt(); !lt.IsZero() {
			s := btcNextCheckAt(st.Job, r.settings, st.SyncedThrough, r.now(), lt).Format(time.RFC3339)
			st.NextCheckAt = &s
		}
	}
}

// recoverStaleOnce settles expired reservations: reservations with a BigQuery
// job ID settle the job's actual billed bytes (jobs.get), others settle their
// reserved bytes conservatively.
func (r *btcBQRunner) recoverStaleOnce(ctx context.Context) {
	if r == nil || r.budget == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	lookup := r.jobLookup
	if lookup == nil && r.bq != nil {
		lookup = bqLookupJobBytes(rctx, r.bq.client)
	}
	if err := r.budget.recoverStale(rctx, r.now().UTC(), lookup); err != nil {
		slog.Warn("bitcoin bigquery recover stale reservations", "error", err)
	}
}

func (r *btcBQRunner) executeSync(ctx context.Context, job, trigger string) {
	if r == nil {
		return
	}
	syncer, ok := r.syncers[job]
	if !ok || syncer == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error(job+" sync panic", "source", job, "panic", rec)
			_ = r.store.setSyncError(context.Background(), job, "panic")
			r.recordLastRun(context.Background(), job, btcJobLastRun{
				At:      r.now().UTC().Format(time.RFC3339),
				Trigger: trigger,
				Result:  "error",
			})
		}
	}()
	if _, stopped, _ := r.flags.stopped(ctx, job); stopped {
		if trigger != "schedule" {
			r.recordLastRun(ctx, job, btcJobLastRun{
				At:      r.now().UTC().Format(time.RFC3339),
				Trigger: trigger,
				Result:  "stopped",
			})
		}
		return
	}
	var opts dailySyncOpts
	if trigger == "manual" {
		// Run now bypasses backoff and fetches every settled day not yet
		// synced; cadence_days only paces the schedule.
		opts = dailySyncOpts{ignoreBackoff: true, ignoreMinWindow: true}
	}
	t0 := r.now()
	timeout := syncer.timeout
	if timeout <= 0 {
		timeout = stablecoinSyncTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run, didWork := syncer.syncWithOpts(tctx, opts)
	bctx, bCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer bCancel()

	// "source not caught up" means BigQuery has not ingested past window_end + 6h
	// yet: an unbilled wait that must not overwrite sync_state.last_error.
	if didWork && run.errCode != "" && run.errCode != "source not caught up" {
		if err := r.store.setSyncError(bctx, job, run.errCode); err != nil {
			slog.Error(job+" sync state", "source", job, "error", err)
		}
		if run.errCode == "over_budget" {
			_ = r.store.setBtcKV(bctx, "over_budget_month:"+job, r.now().UTC().Format("2006-01"))
		}
	} else if didWork && run.errCode == "" {
		_ = r.store.deleteBtcKV(bctx, "over_budget_month:"+job)
	}
	_, stoppedNow, _ := r.flags.stopped(bctx, job)
	var result string
	switch {
	case stoppedNow || strings.HasPrefix(run.errCode, "stopped"):
		result = "stopped"
	case run.errCode == "over_budget":
		result = "budget_paused"
	case run.errCode == "source not caught up":
		result = "no_new_day"
	case run.errCode != "":
		result = "error"
	case !didWork:
		result = "no_new_day"
	default:
		result = "ok"
	}
	if trigger == "manual" && (!didWork || run.errCode == "source not caught up") {
		// Shorten the 5-minute manual cooldown to 1 minute when no billed
		// query ran (e.g. "source not caught up"), so a late public dataset
		// doesn't lock the button for 5 minutes while still rate-limiting
		// repeated Ready and dry-run probes.
		r.mu.Lock()
		r.lastManualAt[job] = r.now().UTC().Add(btcManualNoOpCooldown - btcManualCooldown)
		r.mu.Unlock()
	}
	if didWork || trigger != "schedule" {
		r.recordLastRun(bctx, job, btcJobLastRun{
			At:          r.now().UTC().Format(time.RFC3339),
			Trigger:     trigger,
			Result:      result,
			BytesBilled: run.billed,
		})
	}
	if trigger == "manual" && result == "ok" && job == btcOnchainJobID {
		// btc_coinage refreshes holder metrics in Store (onChange). After a
		// manual btc_onchain run, roll the holder state forward now instead
		// of waiting for an hourly tick that never comes while the schedule
		// is off.
		r.maint.runOnce(bctx)
	}
	if didWork {
		slog.Info(job+" sync",
			"source", job,
			"trigger", trigger,
			"window_start", run.start.String(),
			"window_end", run.end.String(),
			"rows", run.rows,
			"bytes_billed", run.billed,
			"duration", r.now().Sub(t0).String(),
			"error", run.errCode,
		)
	}
}

func (r *btcBQRunner) runScheduled(ctx context.Context, job string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.running[job] {
		r.mu.Unlock()
		return
	}
	r.running[job] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running[job] = false
		r.mu.Unlock()
	}()
	r.executeSync(ctx, job, "schedule")
}

// scheduledJobs returns the hourly loop: loop tick and stale-reservation
// recovery, Job A, Job B (when coin_age.enabled) and holder maintenance. It
// returns nil when the schedule is off; manual runs still work.
func (r *btcBQRunner) scheduledJobs() []func(context.Context) {
	if r == nil || !r.settings.Enabled || !r.settings.Jobs || r.store == nil {
		return nil
	}
	expGiB, warn := btcFeasibility(r.settings, btcEstRollup1dBytes, btcEstCoinAgeRunBytes)
	if warn != "" {
		slog.Warn("bitcoin bigquery feasibility", "warning", warn, "expected_gib", expGiB)
	} else {
		slog.Info("bitcoin bigquery jobs enabled",
			"initial_days", r.settings.InitialDays,
			"cadence_days", r.settings.CadenceDays,
			"expected_month_gib", expGiB,
			"budget_gib", float64(r.settings.BudgetBytes)/float64(1<<30),
		)
	}
	tickFn := func(ctx context.Context) {
		r.recordLoopTick(r.now().UTC())
		r.recoverStaleOnce(ctx)
	}
	out := []func(context.Context){
		tickFn,
		func(ctx context.Context) { r.runScheduled(ctx, btcOnchainJobID) },
	}
	if r.settings.CoinAgeEnabled {
		out = append(out, func(ctx context.Context) { r.runScheduled(ctx, btcCoinageJobID) })
	}
	return append(out, r.maint.runOnce)
}

// estimate dry-runs the exact window that job would fetch next on a manual
// run without starting any billed query (no Ready and no Run calls).
func (r *btcBQRunner) estimate(ctx context.Context, job string) (BtcBQEstimateResponse, error) {
	if r == nil || r.store == nil {
		return BtcBQEstimateResponse{}, fmt.Errorf("runner unavailable")
	}
	if job == btcCoinageJobID && !r.settings.CoinAgeEnabled {
		return BtcBQEstimateResponse{}, errBtcCoinAgeDisabled
	}
	syncer, ok := r.syncers[job]
	if !ok || syncer == nil || syncer.job == nil {
		return BtcBQEstimateResponse{}, fmt.Errorf("unknown job %q", job)
	}
	now := r.now().UTC()
	today := civil.DateOf(now)
	candEnd := btcSettledEnd(now)
	st, err := r.store.getSyncState(ctx, job)
	if err != nil {
		return BtcBQEstimateResponse{}, err
	}
	start, newFrom, err := syncer.startDate(st, today)
	if err != nil {
		return BtcBQEstimateResponse{}, fmt.Errorf("bad cursor %q", st.Cursor)
	}
	r.recoverStaleOnce(ctx)
	bSt, err := r.budget.status(ctx, now)
	if err != nil {
		return BtcBQEstimateResponse{}, err
	}

	out := BtcBQEstimateResponse{
		Job:              job,
		PricingNote:      btcPricingNote,
		BudgetUsedBytes:  bSt.SettledBytes + bSt.ReservedBytes,
		BudgetLimitBytes: bSt.BudgetBytes,
	}
	end, nothingToDo, errCode := syncer.resolveWindow(start, candEnd, newFrom, dailySyncOpts{
		ignoreBackoff:   true,
		ignoreMinWindow: true,
	})
	if errCode == "gap too large" {
		return BtcBQEstimateResponse{}, fmt.Errorf("%w: window %s..%s exceeds max %d days", errBtcGapTooLarge, start, end.AddDays(-1), syncer.maxWindowDays)
	}
	if nothingToDo {
		out.FitsBudget = true
		out.FitsHardCap = true
		out.NothingToDo = true
		return out, nil
	}
	estBytes, err := syncer.job.DryRun(ctx, start, end)
	if err != nil {
		return BtcBQEstimateResponse{}, err
	}
	fitsHardCap := syncer.hardCapBytes <= 0 || estBytes <= syncer.hardCapBytes
	out.WindowFrom = start.String()
	out.WindowTo = end.AddDays(-1).String()
	out.EstimatedBytes = estBytes
	out.EstimatedUSD = bytesToUSD(estBytes)
	out.ReserveBytes = dryRunSizedReservation(estBytes, syncer.hardCapBytes)
	out.FitsHardCap = fitsHardCap
	out.FitsBudget = fitsHardCap && out.ReserveBytes <= bSt.RemainingBytes
	return out, nil
}

// jobStatus reads a job's status from SQLite and overlays the runner's live
// state (running, last loop tick, last run).
func (s *bitcoinService) jobStatus(ctx context.Context, job string) (BtcBQStatus, error) {
	st, err := s.read.jobStatus(ctx, job)
	if err == nil {
		s.bqRunner.decorateStatus(&st)
	}
	return st, err
}

// --- /bigquery/estimate and /bigquery/run handlers ---

func checkBtcBQPostGuards(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	// Loopback only (RemoteAddr + Host/Origin); ignore X-Forwarded-For / X-Real-IP.
	if !isLoopbackRequest(r) {
		writeError(w, "forbidden: BigQuery actions are restricted to loopback requests (127.0.0.1 / ::1)", http.StatusForbidden)
		return "", false
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if ct != "application/json" || r.Header.Get(btcBQActionHeader) != btcBQActionValue {
		writeError(w, "missing required Content-Type: application/json or X-BigLens-Action: bitcoin-bq header", http.StatusBadRequest)
		return "", false
	}
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	if job != btcOnchainJobID && job != btcCoinageJobID {
		writeError(w, "invalid job (expected btc_onchain or btc_coinage)", http.StatusBadRequest)
		return "", false
	}
	return job, true
}

func writeBtcEstimateError(w http.ResponseWriter, job string, err error) {
	switch {
	case errors.Is(err, errBtcCoinAgeDisabled):
		writeError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, errBtcGapTooLarge):
		writeError(w, err.Error(), http.StatusConflict)
	default:
		slog.Warn("bitcoin bigquery estimate failed", "job", job, "error", err)
		writeError(w, "dry-run estimate failed", http.StatusBadGateway)
	}
}

func (h *APIHandler) BitcoinBQEstimate(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	job, ok := checkBtcBQPostGuards(w, r)
	if !ok {
		return
	}
	runner := h.btc.bqRunner
	if runner == nil {
		writeError(w, "BigQuery runner unavailable", http.StatusServiceUnavailable)
		return
	}
	if runner.manualCtx().Err() != nil {
		writeError(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	est, err := runner.estimate(ctx, job)
	if err != nil {
		writeBtcEstimateError(w, job, err)
		return
	}
	writeJSON(w, est)
}

func (h *APIHandler) BitcoinBQRun(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	job, ok := checkBtcBQPostGuards(w, r)
	if !ok {
		return
	}
	runner := h.btc.bqRunner
	if runner == nil {
		writeError(w, "BigQuery runner unavailable", http.StatusServiceUnavailable)
		return
	}
	if runner.manualCtx().Err() != nil {
		writeError(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	if job == btcCoinageJobID && !runner.settings.CoinAgeEnabled {
		writeError(w, errBtcCoinAgeDisabled.Error(), http.StatusConflict)
		return
	}
	var req struct {
		ConfirmedEstimateBytes *int64 `json:"confirmed_estimate_bytes"`
	}
	if r.Body == nil {
		writeError(w, "missing request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.ConfirmedEstimateBytes == nil || *req.ConfirmedEstimateBytes < 0 {
		writeError(w, "invalid or missing confirmed_estimate_bytes", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if reason, stopped, _ := runner.flags.stopped(ctx, job); stopped {
		if reason == "" {
			reason = fmt.Sprintf("job %s is stopped — resume with ./biglens-server --bitcoin-bq-resume %s", job, job)
		}
		writeError(w, reason, http.StatusConflict)
		return
	}

	now := runner.now().UTC()
	runner.mu.Lock()
	if runner.running[job] {
		runner.mu.Unlock()
		writeError(w, "job is already running", http.StatusConflict)
		return
	}
	if lastAt, exists := runner.lastManualAt[job]; exists && now.Sub(lastAt) < btcManualCooldown {
		waitSec := int(math.Ceil((btcManualCooldown - now.Sub(lastAt)).Seconds()))
		runner.mu.Unlock()
		writeError(w, fmt.Sprintf("rate limited: wait %ds before starting another manual run of %s", waitSec, job), http.StatusTooManyRequests)
		return
	}
	runner.running[job] = true
	runner.mu.Unlock()

	abortRunning := func() {
		runner.mu.Lock()
		runner.running[job] = false
		runner.mu.Unlock()
	}

	est, err := runner.estimate(ctx, job)
	if err != nil {
		abortRunning()
		writeBtcEstimateError(w, job, err)
		return
	}
	if est.NothingToDo {
		abortRunning()
		writeError(w, "nothing to do: already synced through the latest settled UTC day", http.StatusConflict)
		return
	}
	maxAllowed := int64(math.Floor(float64(*req.ConfirmedEstimateBytes) * 1.10))
	if est.EstimatedBytes > maxAllowed {
		abortRunning()
		writeError(w, "estimate changed by more than 10%; please run estimate again", http.StatusConflict)
		return
	}
	if !est.FitsHardCap {
		abortRunning()
		writeError(w, "hard_cap_exceeded: estimated scan exceeds the 256 GiB per-run safety cap", http.StatusConflict)
		return
	}
	if !est.FitsBudget {
		abortRunning()
		writeError(w, "over_budget: estimated run exceeds remaining monthly budget", http.StatusConflict)
		return
	}

	runner.mu.Lock()
	if runner.runCtx != nil && runner.runCtx.Err() != nil {
		runner.running[job] = false
		runner.mu.Unlock()
		writeError(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	runner.lastManualAt[job] = now
	runner.wg.Add(1)
	runner.mu.Unlock()

	go func() {
		defer runner.wg.Done()
		defer func() {
			runner.mu.Lock()
			runner.running[job] = false
			runner.mu.Unlock()
		}()
		runner.executeSync(runner.manualCtx(), job, "manual")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "started",
		"job":    job,
	})
}
