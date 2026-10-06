package main

// Incremental + cold-start sync of USDT/USDC freeze events (spec §7.1, §7.2).
// Only complete UTC days are fetched, and only once BigQuery has exported
// blocks at least 30 minutes past the window end; a cursor that moved past
// missing data would lose those events forever.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"cloud.google.com/go/civil"
)

const (
	// stablecoinFinality: the logs view includes unfinalized blocks (~15-18 min).
	stablecoinFinality = 30 * time.Minute
	// stablecoinMaxWindowDays bounds one incremental query; bigger gaps are
	// left to the backfill CLI, which chunks them.
	stablecoinMaxWindowDays = 31
	// stablecoinSyncMaxBytes caps incremental and cold-start queries
	// (31 days × ≤4.5 GB ≈ 140 GB); BigQuery rejects anything larger.
	stablecoinSyncMaxBytes = 150 << 30
)

// stablecoinSyncTimeout bounds one tick's BigQuery work; a hung call would
// otherwise block the shared scheduler goroutine and the list sync with it.
var stablecoinSyncTimeout = 10 * time.Minute

// stablecoinBilledRetry delays the retry after a failure that BigQuery had
// already billed (bad row, store write error): retrying hourly would re-bill
// the same query every tick (~$0.5 per tick for a failing cold start).
const stablecoinBilledRetry = 6 * time.Hour

func dayStart(d civil.Date) time.Time { return d.In(time.UTC) }

func blocksComplete(ctx context.Context, src stablecoinSource, end civil.Date) (bool, error) {
	t, err := src.MaxBlockTime(ctx, end)
	if err != nil {
		return false, err
	}
	return !t.Before(dayStart(end).Add(stablecoinFinality)), nil
}

// safeEnd returns the exclusive end day for any batch that moves the cursor.
// Before 00:30 UTC the candidate starts at yesterday, so a process started
// just after midnight still syncs up to yesterday. ok=false with a nil error
// means "not exported yet"; a non-nil error is a BigQuery failure (auth,
// quota) that the caller must report as such.
func safeEnd(ctx context.Context, src stablecoinSource, now time.Time) (civil.Date, bool, error) {
	return safeEndFrom(ctx, src, now, civil.Date{})
}

func safeEndFrom(ctx context.Context, src stablecoinSource, now time.Time, start civil.Date) (civil.Date, bool, error) {
	now = now.UTC()
	today := civil.DateOf(now)
	cand := today
	if now.Before(dayStart(today).Add(stablecoinFinality)) {
		cand = today.AddDays(-1)
	}
	if ok, err := blocksComplete(ctx, src, cand); err != nil || ok {
		return cand, ok, err
	}
	// Only probe yesterday as a fallback when the caller's start precedes
	// yesterday; in steady-state daily sync (start == yesterday), falling back
	// to end == yesterday produces an empty [start, start) window anyway.
	if cand == today && (!start.IsValid() || start.Before(today.AddDays(-1))) {
		if ok, err := blocksComplete(ctx, src, today.AddDays(-1)); err != nil || ok {
			return today.AddDays(-1), ok, err
		}
	}
	return civil.Date{}, false, nil
}

// bqErrCode classifies a BigQuery error for last_error (shown in the UI).
func bqErrCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "bigquery_error"
}

// bytesToUSD uses on-demand pricing, $6.25 per TiB.
func bytesToUSD(b int64) float64 { return float64(b) / (1 << 40) * 6.25 }

type stablecoinSyncer struct {
	store       *riskStore
	src         stablecoinSource
	now         func() time.Time
	initialDays int    // 0 disables the cold start
	onChange    func() // invalidates the overview cache; may be nil
	retryAfter  time.Time
}

type stablecoinRun struct {
	start, end civil.Date
	rows       int
	billed     int64
	errCode    string
}

// syncOnce is one scheduler tick: at most one BigQuery logs query, one
// summary log line, and a recover so a panic becomes last_error.
func (s *stablecoinSyncer) syncOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("address_risk sync panic", "source", stablecoinSourceID, "panic", r)
			s.store.setSyncError(context.Background(), stablecoinSourceID, "panic")
		}
	}()
	t0 := time.Now()
	tctx, cancel := context.WithTimeout(ctx, stablecoinSyncTimeout)
	defer cancel()
	run, ok := s.sync(tctx) // ctx, not tctx, below: the error must be recorded after a timeout
	if !ok {
		return
	}
	if run.errCode != "" {
		if err := s.store.setSyncError(ctx, stablecoinSourceID, run.errCode); err != nil {
			slog.Error("address_risk sync state", "source", stablecoinSourceID, "error", err)
		}
	}
	slog.Info("address_risk sync", "source", stablecoinSourceID, "window_start", run.start.String(),
		"window_end", run.end.String(), "rows", run.rows, "bytes_billed", run.billed,
		"duration", time.Since(t0).String(), "error", run.errCode)
}

// sync returns ok=false when this tick has nothing to do (no log line).
func (s *stablecoinSyncer) sync(ctx context.Context) (stablecoinRun, bool) {
	st, err := s.store.getSyncState(ctx, stablecoinSourceID)
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
		if s.initialDays == 0 {
			return stablecoinRun{}, false
		}
		start = today.AddDays(-s.initialDays)
		newFrom = start.String()
	} else {
		cur, err := civil.ParseDate(st.Cursor)
		if err != nil {
			return stablecoinRun{errCode: "bad_state"}, true
		}
		start = cur.AddDays(1)
		// Cheap skip without touching BigQuery: nothing complete to fetch yet.
		if !start.Before(today) || (!start.Before(today.AddDays(-1)) && now.Before(dayStart(today).Add(stablecoinFinality))) {
			return stablecoinRun{}, false
		}
	}
	end, ok, err := safeEndFrom(ctx, s.src, now, start)
	if err != nil {
		slog.Warn("address_risk bigquery", "source", stablecoinSourceID, "error", err)
		return stablecoinRun{start: start, errCode: bqErrCode(err)}, true
	}
	if !ok {
		return stablecoinRun{start: start, errCode: "source not caught up"}, true
	}
	if !start.Before(end) {
		return stablecoinRun{}, false
	}
	if end.DaysSince(start) > stablecoinMaxWindowDays {
		return stablecoinRun{start: start, end: end, errCode: "gap too large, run --address-risk-backfill"}, true
	}
	if newFrom != "" {
		if est, err := s.src.DryRun(ctx, start, end); err == nil {
			slog.Info("address_risk cold start", "window_start", start.String(), "window_end", end.String(),
				"estimated_bytes", est, "estimated_usd", bytesToUSD(est))
		}
	}
	events, billed, err := s.src.Fetch(ctx, start, end, stablecoinSyncMaxBytes)
	if err != nil {
		// BigQuery errors carry no API key or looked-up address (the service
		// account authenticates out of band), so the detail is safe to log.
		slog.Warn("address_risk bigquery", "source", stablecoinSourceID, "error", err)
		if billed > 0 || errors.Is(err, context.DeadlineExceeded) {
			s.retryAfter = now.Add(stablecoinBilledRetry)
		}
		return stablecoinRun{start: start, end: end, billed: billed, errCode: bqErrCode(err)}, true
	}
	if err := s.store.insertStablecoinEvents(ctx, events, newFrom, end.AddDays(-1).String(), s.now()); err != nil {
		slog.Error("address_risk store", "source", stablecoinSourceID, "error", err)
		s.retryAfter = now.Add(stablecoinBilledRetry)
		return stablecoinRun{start: start, end: end, billed: billed, errCode: "unavailable"}, true
	}
	if s.onChange != nil {
		s.onChange()
	}
	return stablecoinRun{start: start, end: end, rows: len(events), billed: billed}, true
}
