package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// scamTronMinDailyTransfers: R3 normally scans millions of official TRON
	// USDT Transfer logs per UTC day.
	scamTronMinDailyTransfers = 1
	// scamBTCMinDailyTxs: R5 normally counts hundreds of thousands of
	// Bitcoin transactions per UTC day.
	scamBTCMinDailyTxs = 1
	// tronStablecoinEmptyGuardDays: a day without TRON USDT blacklist events
	// is normal; a window this long without a single one is not.
	tronStablecoinEmptyGuardDays = 7
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

// minPerDayGuard fails when any day of the window has a count below min
// (days absent from counts count as 0), naming the first such day.
func minPerDayGuard(order []string, counts map[string]int64, min int64, what string) error {
	var low []string
	for _, d := range order {
		if counts[d] < min {
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
}

type dailyJob interface {
	ID() string
	Ready(ctx context.Context, end civil.Date) (bool, error)
	DryRun(ctx context.Context, start, end civil.Date) (int64, error)
	Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error)
	Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error
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
	store         *riskStore
	job           dailyJob
	now           func() time.Time
	initialDays   int
	retentionDays int
	onChange      func()
	retryAfter    time.Time
}

func (s *dailyBQSyncer) syncOnce(ctx context.Context) {
	sourceID := s.job.ID()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("scam_radar sync panic", "source", sourceID, "panic", r)
			_ = s.store.setSyncError(context.Background(), sourceID, "panic")
		}
	}()
	t0 := time.Now()
	tctx, cancel := context.WithTimeout(ctx, stablecoinSyncTimeout)
	defer cancel()

	run, ok := s.sync(tctx)
	if !ok {
		return
	}
	if run.errCode != "" {
		if err := s.store.setSyncError(ctx, sourceID, run.errCode); err != nil {
			slog.Error("scam_radar sync state", "source", sourceID, "error", err)
		}
	}
	slog.Info("scam_radar sync",
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
	}

	end, ok, err := safeEndForJob(ctx, s.job, now, start)
	if err != nil {
		slog.Warn("scam_radar bigquery ready", "source", sourceID, "error", err)
		return stablecoinRun{start: start, errCode: bqErrCode(err)}, true
	}
	if !ok {
		return stablecoinRun{start: start, errCode: "source not caught up"}, true
	}
	if !start.Before(end) {
		return stablecoinRun{}, false
	}
	if end.DaysSince(start) > stablecoinMaxWindowDays {
		return stablecoinRun{start: start, end: end, errCode: "gap too large"}, true
	}

	maxBytes := int64(dailySyncIncrementalMaxBytes)
	if newFrom != "" {
		maxBytes = dailySyncColdStartMaxBytes
		if est, err := s.job.DryRun(ctx, start, end); err == nil {
			slog.Info("scam_radar cold start",
				"source", sourceID,
				"window_start", start.String(),
				"window_end", end.String(),
				"estimated_bytes", est,
				"estimated_usd", bytesToUSD(est),
			)
		}
	}

	res, billed, err := s.job.Run(ctx, start, end, maxBytes)
	if err != nil {
		slog.Warn("scam_radar bigquery", "source", sourceID, "error", err)
		if billed > 0 || errors.Is(err, context.DeadlineExceeded) {
			s.retryAfter = now.Add(stablecoinBilledRetry)
		}
		return stablecoinRun{start: start, end: end, billed: billed, errCode: dailyRunErrCode(err)}, true
	}

	cursor := end.AddDays(-1).String()
	if err := s.job.Store(ctx, s.store, res, newFrom, cursor, s.now()); err != nil {
		slog.Error("scam_radar store", "source", sourceID, "error", err)
		s.retryAfter = now.Add(stablecoinBilledRetry)
		return stablecoinRun{start: start, end: end, billed: billed, errCode: "unavailable"}, true
	}

	if err := s.store.pruneScam(ctx, s.retentionDays, today); err != nil {
		slog.Warn("scam_radar prune", "source", sourceID, "error", err)
	}
	if s.onChange != nil {
		s.onChange()
	}
	return stablecoinRun{start: start, end: end, rows: res.Rows, billed: billed}, true
}
