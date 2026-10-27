package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"cloud.google.com/go/civil"
)

const (
	dailySyncIncrementalMaxBytes = 50 << 30
	dailySyncColdStartMaxBytes   = 200 << 30
)

// Silent-zero guards. Every day of a window is pre-filled with zeros and the
// cursor advances on success with no resync path, so a filter that silently
// stops matching after an upstream schema/format change would otherwise store
// permanent 0-hit days. A run whose query succeeded but whose result falls
// below these floors fails instead (see suspiciousResultError).
const (
	// scamEthMinDailyTransfers: R1 normally scans hundreds of thousands of
	// official USDT/USDC transfers per UTC day.
	scamEthMinDailyTransfers = 1
	// scamEthMinDailyZeroValue: zero-value transfers on official USDT/USDC
	// happen thousands of times per UTC day (what poisoners send); 0 means
	// the q = '0' predicate stopped matching.
	scamEthMinDailyZeroValue = 1
	// scamTronMinDailyTransfers: R3 normally scans millions of official TRON
	// USDT Transfer logs per UTC day.
	scamTronMinDailyTransfers = 1
	// scamTronMinDailyCandidates: TRON dust poisoning yields dust candidates
	// every day; 0 means the dust or pairing predicate stopped matching.
	scamTronMinDailyCandidates = 1
	// scamBTCMinDailyTxs: R5 normally counts hundreds of thousands of
	// Bitcoin transactions per UTC day.
	scamBTCMinDailyTxs = 1
	// tronStablecoinMinDailyUSDTLogs: R4 normally scans millions of official
	// TRON USDT logs (any topic) per UTC day, even when no blacklist event
	// occurred that day.
	tronStablecoinMinDailyUSDTLogs = 1
	// scamMinFakeTokenDeployers: the tokens_latest snapshot normally yields
	// over a thousand serial counterfeit deployers (1,178 on 2026-10-13).
	scamMinFakeTokenDeployers = 1
)

// suspiciousResultError fails a run whose BigQuery query succeeded but whose
// result is implausible. sync treats it like any failed Run (nothing stored,
// cursor unchanged, billed back-off) and records the message as last_error.
type suspiciousResultError struct{ msg string }

func (e *suspiciousResultError) Error() string { return e.msg }

// suspiciousEmpty builds the guard error, e.g. "suspicious empty result for
// 2026-10-12: 0 official USDT/USDC transfers scanned — upstream schema or
// format change?".
func suspiciousEmpty(scope, what string) error {
	return &suspiciousResultError{msg: fmt.Sprintf("suspicious empty result for %s: 0 %s — upstream schema or format change?", scope, what)}
}

// minPerDayGuard fails when any day of the window has a count below floor
// (days absent from counts count as 0), naming the first such day.
func minPerDayGuard(order []string, counts map[string]int64, floor int64, what string) error {
	var low []string
	for _, d := range order {
		if counts[d] < floor {
			low = append(low, d)
		}
	}
	switch len(low) {
	case 0:
		return nil
	case 1:
		return suspiciousEmpty(low[0], what)
	default:
		return suspiciousEmpty(fmt.Sprintf("%s and %d more days", low[0], len(low)-1), what)
	}
}

// dailyRunErrCode is the last_error for a failed Run: the guard's own
// message (safe to show, no driver text) or a generic BigQuery code.
func dailyRunErrCode(err error) string {
	var se *suspiciousResultError
	if errors.As(err, &se) {
		return se.msg
	}
	return bqErrCode(err)
}

type dailyResult struct {
	Rows       int
	TronEvents []stablecoinEvent
	Days       []scamDayBatch
	Deployers  []fakeTokenDeployerRow // scam_eth: serial-deployer snapshot
	Payload    any                    // job-specific typed payload (e.g. Bitcoin rollup / coin-age)
}

type dailyJob interface {
	ID() string
	Ready(ctx context.Context, end civil.Date) (bool, error)
	DryRun(ctx context.Context, start, end civil.Date) (int64, error)
	Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error)
	Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error
}

// syncBudget reserves estimated bytes before a job runs, attaches the BigQuery
// job ID once submitted, and settles actual billed bytes on completion.
type syncBudget interface {
	reserve(ctx context.Context, job string, bytes int64) (string, error)
	attachJob(ctx context.Context, id, bqJobID string) error
	settle(ctx context.Context, id string, billed int64) error
}

// syncFailureRecord records one billed failure for cost-tiered stop policies.
type syncFailureRecord struct {
	At          time.Time `json:"at"`
	Guard       string    `json:"guard,omitempty"`
	DayOrHeight string    `json:"day_or_height,omitempty"`
	Message     string    `json:"message"`
	BilledBytes int64     `json:"billed_bytes"`
}

// syncStopFlags persists billed failure history and operator-resumable stop flags.
type syncStopFlags interface {
	stopped(ctx context.Context, job string) (string, bool, error)
	recordFailure(ctx context.Context, job string, rec syncFailureRecord) error
	failures(ctx context.Context, job string) ([]syncFailureRecord, error)
	stop(ctx context.Context, job, reason string) error
	clear(ctx context.Context, job string) error
}

// syncFailurePolicy configures when repeated billed failures stop a job.
// Zero value keeps Scam Radar's legacy 6h billed backoff with no stop.
type syncFailurePolicy struct {
	maxBilledFailures int
	within            time.Duration
	backoff           []time.Duration
}

type syncJobAttachKey struct{}

// withSyncJobAttach attaches a callback to ctx that jobs invoke with their
// BigQuery job ID before waiting for completion.
func withSyncJobAttach(ctx context.Context, fn func(bqJobID string)) context.Context {
	return context.WithValue(ctx, syncJobAttachKey{}, fn)
}

// syncAttachJob reports the submitted BigQuery job ID to the enclosing syncer's
// reservation, if any.
func syncAttachJob(ctx context.Context, bqJobID string) {
	if bqJobID == "" {
		return
	}
	if fn, ok := ctx.Value(syncJobAttachKey{}).(func(string)); ok && fn != nil {
		fn(bqJobID)
	}
}

func safeEndForJob(ctx context.Context, job dailyJob, now time.Time, start civil.Date) (civil.Date, bool, error) {
	now = now.UTC()
	today := civil.DateOf(now)
	cand := today
	if now.Before(dayStart(today).Add(stablecoinFinality)) {
		cand = today.AddDays(-1)
	}
	if ok, err := job.Ready(ctx, cand); err != nil || ok {
		return cand, ok, err
	}
	if cand == today && (!start.IsValid() || start.Before(today.AddDays(-1))) {
		if ok, err := job.Ready(ctx, today.AddDays(-1)); err != nil || ok {
			return today.AddDays(-1), ok, err
		}
	}
	return civil.Date{}, false, nil
}

type dailyBQSyncer struct {
	store            *riskStore
	job              dailyJob
	now              func() time.Time
	initialDays      int
	retentionDays    int
	onChange         func()
	retryAfter       time.Time
	name             string
	hardCapBytes     int64
	dryRunSizedCap   bool
	maxWindowDays    int
	chunkCatchUp     bool
	minWindowDays    int
	timeout          time.Duration
	prune            func(ctx context.Context, today civil.Date) error
	budget           syncBudget
	failurePolicy    syncFailurePolicy
	stopFlags        syncStopFlags
	unbilledFailures int
}

func (s *dailyBQSyncer) logName() string {
	if s.name != "" {
		return s.name
	}
	return "scam_radar"
}

func (s *dailyBQSyncer) syncOnce(ctx context.Context) {
	sourceID := s.job.ID()
	tag := s.logName()
	defer func() {
		if r := recover(); r != nil {
			slog.Error(tag+" sync panic", "source", sourceID, "panic", r)
			_ = s.store.setSyncError(context.Background(), sourceID, "panic")
		}
	}()
	t0 := time.Now()
	timeout := s.timeout
	if timeout <= 0 {
		timeout = stablecoinSyncTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run, ok := s.sync(tctx)
	if !ok {
		return
	}
	if run.errCode != "" {
		bctx, bCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if err := s.store.setSyncError(bctx, sourceID, run.errCode); err != nil {
			slog.Error(tag+" sync state", "source", sourceID, "error", err)
		}
		bCancel()
	}
	slog.Info(tag+" sync",
		"source", sourceID,
		"window_start", run.start.String(),
		"window_end", run.end.String(),
		"rows", run.rows,
		"bytes_billed", run.billed,
		"duration", time.Since(t0).String(),
		"error", run.errCode,
	)
}

func (s *dailyBQSyncer) sync(ctx context.Context) (stablecoinRun, bool) {
	// address_risk.scam_radar.initial_days: 0 disables Scam Radar entirely
	// (spec P2.0 Q2): no BigQuery even when a source already has a cursor;
	// the corpora stored so far stay readable.
	if s.initialDays <= 0 {
		return stablecoinRun{}, false
	}
	sourceID := s.job.ID()
	tag := s.logName()
	if s.stopFlags != nil {
		reason, stopped, err := s.stopFlags.stopped(ctx, sourceID)
		if err != nil {
			return stablecoinRun{errCode: "unavailable"}, true
		}
		if stopped {
			if reason == "" {
				reason = "stopped"
			}
			return stablecoinRun{errCode: reason}, false
		}
	}
	st, err := s.store.getSyncState(ctx, sourceID)
	if err != nil {
		return stablecoinRun{errCode: "unavailable"}, true
	}
	now := s.now().UTC()
	if now.Before(s.retryAfter) {
		return stablecoinRun{}, false
	}
	today := civil.DateOf(now)
	var start civil.Date
	newFrom := ""
	if st.Cursor == "" {
		start = today.AddDays(-s.initialDays)
		newFrom = start.String()
	} else {
		cur, err := civil.ParseDate(st.Cursor)
		if err != nil {
			return stablecoinRun{errCode: "bad_state"}, true
		}
		start = cur.AddDays(1)
		if !start.Before(today) || (!start.Before(today.AddDays(-1)) && now.Before(dayStart(today).Add(stablecoinFinality))) {
			return stablecoinRun{}, false
		}
		if s.minWindowDays > 1 && today.DaysSince(start) < s.minWindowDays {
			return stablecoinRun{}, false
		}
	}

	end, ok, err := safeEndForJob(ctx, s.job, now, start)
	if err != nil {
		slog.Warn(tag+" bigquery ready", "source", sourceID, "error", err)
		s.applyUnbilledBackoff(now)
		return stablecoinRun{start: start, errCode: bqErrCode(err)}, true
	}
	if !ok {
		return stablecoinRun{start: start, errCode: "source not caught up"}, true
	}
	if !start.Before(end) {
		return stablecoinRun{}, false
	}
	if s.minWindowDays > 1 && newFrom == "" && end.DaysSince(start) < s.minWindowDays {
		return stablecoinRun{}, false
	}
	maxWin := s.maxWindowDays
	if maxWin <= 0 {
		maxWin = stablecoinMaxWindowDays
	}
	if end.DaysSince(start) > maxWin {
		if s.chunkCatchUp {
			end = start.AddDays(maxWin)
		} else {
			return stablecoinRun{start: start, end: end, errCode: "gap too large"}, true
		}
	}

	maxBytes := int64(dailySyncIncrementalMaxBytes)
	if newFrom != "" {
		maxBytes = dailySyncColdStartMaxBytes
	}
	if s.hardCapBytes > 0 {
		maxBytes = s.hardCapBytes
	}

	var est int64
	needDryRun := newFrom != "" || s.dryRunSizedCap || s.budget != nil
	if needDryRun {
		dEst, dErr := s.job.DryRun(ctx, start, end)
		if dErr != nil {
			if s.dryRunSizedCap || s.budget != nil {
				slog.Warn(tag+" dry run", "source", sourceID, "error", dErr)
				s.applyUnbilledBackoff(now)
				return stablecoinRun{start: start, end: end, errCode: bqErrCode(dErr)}, true
			}
		} else {
			est = dEst
			if newFrom != "" {
				slog.Info(tag+" cold start",
					"source", sourceID,
					"window_start", start.String(),
					"window_end", end.String(),
					"estimated_bytes", est,
					"estimated_usd", bytesToUSD(est),
				)
			}
		}
	}

	if s.hardCapBytes > 0 && est > s.hardCapBytes {
		return stablecoinRun{start: start, end: end, errCode: "over_budget"}, true
	}
	if s.dryRunSizedCap {
		sized := int64(math.Ceil(float64(est) * 1.25))
		if sized < 1<<30 {
			sized = 1 << 30
		}
		if s.hardCapBytes > 0 && sized > s.hardCapBytes {
			sized = s.hardCapBytes
		}
		maxBytes = sized
	}

	var resID string
	if s.budget != nil {
		id, rErr := s.budget.reserve(ctx, sourceID, maxBytes)
		if rErr != nil {
			return stablecoinRun{start: start, end: end, errCode: "over_budget"}, true
		}
		resID = id
	}

	runCtx := ctx
	if s.budget != nil && resID != "" {
		runCtx = withSyncJobAttach(ctx, func(bqJobID string) {
			actx, aCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer aCancel()
			_ = s.budget.attachJob(actx, resID, bqJobID)
		})
	}

	res, billed, err := s.job.Run(runCtx, start, end, maxBytes)
	bctx, bCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer bCancel()
	if err != nil && billed == 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) && est > 0 {
		billed = est
	}
	if s.budget != nil && resID != "" {
		_ = s.budget.settle(bctx, resID, billed)
	}
	if err != nil {
		slog.Warn(tag+" bigquery", "source", sourceID, "error", err)
		errCode := dailyRunErrCode(err)
		if billed > 0 || errors.Is(err, context.DeadlineExceeded) {
			errCode = s.applyBilledFailure(bctx, sourceID, now, errCode, billed)
		} else {
			s.applyUnbilledBackoff(now)
		}
		return stablecoinRun{start: start, end: end, billed: billed, errCode: errCode}, true
	}

	cursor := end.AddDays(-1).String()
	if err := s.job.Store(ctx, s.store, res, newFrom, cursor, s.now()); err != nil {
		slog.Error(tag+" store", "source", sourceID, "error", err)
		errCode := s.applyBilledFailure(bctx, sourceID, now, "unavailable", billed)
		return stablecoinRun{start: start, end: end, billed: billed, errCode: errCode}, true
	}

	s.unbilledFailures = 0
	if s.stopFlags != nil {
		_ = s.stopFlags.clear(bctx, sourceID)
	}

	if s.prune != nil {
		if err := s.prune(ctx, today); err != nil {
			slog.Warn(tag+" prune", "source", sourceID, "error", err)
		}
	} else {
		if err := s.store.pruneScam(ctx, s.retentionDays, today); err != nil {
			slog.Warn(tag+" prune", "source", sourceID, "error", err)
		}
	}
	if s.onChange != nil {
		s.onChange()
	}
	return stablecoinRun{start: start, end: end, rows: res.Rows, billed: billed}, true
}

func (s *dailyBQSyncer) applyUnbilledBackoff(now time.Time) {
	if s.failurePolicy.maxBilledFailures <= 0 {
		return
	}
	s.unbilledFailures++
	shift := s.unbilledFailures - 1
	if shift > 5 {
		shift = 5
	}
	d := time.Hour << shift
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	s.retryAfter = now.Add(d)
}

func (s *dailyBQSyncer) applyBilledFailure(ctx context.Context, sourceID string, now time.Time, errCode string, billed int64) string {
	if s.failurePolicy.maxBilledFailures <= 0 || s.stopFlags == nil {
		s.retryAfter = now.Add(stablecoinBilledRetry)
		return errCode
	}
	rec := syncFailureRecord{
		At:          now,
		Message:     errCode,
		BilledBytes: billed,
	}
	_ = s.stopFlags.recordFailure(ctx, sourceID, rec)
	all, err := s.stopFlags.failures(ctx, sourceID)
	if err != nil {
		s.retryAfter = now.Add(stablecoinBilledRetry)
		return errCode
	}
	var active []syncFailureRecord
	if s.failurePolicy.within > 0 {
		cutoff := now.Add(-s.failurePolicy.within)
		for _, f := range all {
			if !f.At.Before(cutoff) {
				active = append(active, f)
			}
		}
	} else {
		active = all
	}
	if len(active) >= s.failurePolicy.maxBilledFailures {
		stopMsg := formatSyncStopMessage(sourceID, active, s.failurePolicy.within)
		_ = s.stopFlags.stop(ctx, sourceID, stopMsg)
		return stopMsg
	}
	backoff := stablecoinBilledRetry
	if len(s.failurePolicy.backoff) > 0 {
		idx := len(active) - 1
		if idx >= len(s.failurePolicy.backoff) {
			idx = len(s.failurePolicy.backoff) - 1
		}
		if idx >= 0 {
			backoff = s.failurePolicy.backoff[idx]
		}
	}
	s.retryAfter = now.Add(backoff)
	return errCode
}

func formatSyncStopMessage(sourceID string, recs []syncFailureRecord, within time.Duration) string {
	var msgs []string
	for _, r := range recs {
		msgs = append(msgs, r.Message)
	}
	summary := strings.Join(msgs, "; ")
	resumeFlag := "--bitcoin-bq-resume"
	if strings.HasPrefix(sourceID, "stable_") {
		resumeFlag = "--stablecoin-bq-resume"
	}
	if within > 0 && len(recs) >= 2 {
		span := recs[len(recs)-1].At.Sub(recs[0].At).Round(time.Minute)
		return fmt.Sprintf("stopped after %d billed failures in %s (%s) — investigate, then run %s %s",
			len(recs), span, summary, resumeFlag, sourceID)
	}
	return fmt.Sprintf("stopped after %d consecutive billed failures (%s) — investigate, then run %s %s",
		len(recs), summary, resumeFlag, sourceID)
}
