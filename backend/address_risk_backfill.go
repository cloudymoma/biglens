package main

// Manual backfill of USDT/USDC freeze history (spec §7.3):
//
//	cd /opt/biglens/backend && sudo -u biglens ./biglens-server --address-risk-backfill [--since 2017-11-28 | --since-days N] [--yes]
//
// Without --yes it checks the exported block watermark (~25 MB partitioned logs
// check) and prints the dry-run estimate for the backfill batches without
// running them. It first closes a
// forward gap after the cursor (31-day chunks), then fills whole years from
// coverage_from back to --since, newest first; every batch commits its events
// and watermark together, so a failed run resumes where it stopped.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/civil"
)

// backfillMinMaxBytes floors MaxBytesBilled: 0 would mean "no limit".
const backfillMinMaxBytes = 64 << 20

type backfillOpts struct {
	Since civil.Date
	Yes   bool
	Force bool
	// MaxTotalBytes > 0 stops the run before any billed query when its
	// re-planned dry-run total exceeds it (HTTP: the confirmed estimate + 10%).
	// 0 means no total cap (CLI: --yes bills the estimate it just printed).
	MaxTotalBytes int64
}

// publicError marks a backfill error whose text was written here and is safe
// to serve over HTTP. Other errors (BigQuery, SQLite) can name projects, job
// IDs or file paths, so they are logged and replaced by a fixed message.
type publicError struct{ msg string }

func (e publicError) Error() string { return e.msg }

var errBlocksNotExported = publicError{"BigQuery has not exported recent blocks yet; retry in an hour"}

// publicBackfillError is the text POST and GET /backfill may show for err
// (from the estimate or the run).
func publicBackfillError(err error) string {
	var pe publicError
	switch {
	case errors.As(err, &pe):
		return pe.msg
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out; run it again (a backfill resumes from its last completed batch)"
	case errors.Is(err, context.Canceled):
		return "cancelled; run it again (a backfill resumes from its last completed batch)"
	}
	return "BigQuery or database error; see the server log (or CLI output) for details"
}

// backfillPlanTTL bounds how long a dry-run estimate can be confirmed.
const backfillPlanTTL = 15 * time.Minute

// backfillPlan is the dry-run estimate last shown to the operator. Confirm
// must present its single-use token, so the server enforces the
// estimate-then-confirm order, and the run is capped at bytes + 10%.
type backfillPlan struct {
	token    string
	force    bool
	bytes    int64
	issuedAt time.Time
}

type backfillBatch struct {
	Label              string
	Start, End         civil.Date // [Start, End)
	NewFrom, NewCursor string
}

// parseBackfillOpts validates the flags; --since and --since-days exclude
// each other and nothing earlier than the first USDT freeze is queried.
func parseBackfillOpts(since string, sinceDays int, yes bool, now time.Time) (backfillOpts, error) {
	if since != "" && sinceDays != 0 {
		return backfillOpts{}, errors.New("--since and --since-days are mutually exclusive")
	}
	if sinceDays < 0 {
		return backfillOpts{}, errors.New("--since-days must be positive")
	}
	d := stablecoinFirstDate
	if since != "" {
		p, err := civil.ParseDate(since)
		if err != nil {
			return backfillOpts{}, fmt.Errorf("--since must be YYYY-MM-DD: %q", since)
		}
		d = p
	}
	if sinceDays > 0 {
		d = civil.DateOf(now.UTC()).AddDays(-sinceDays)
	}
	if d.Before(stablecoinFirstDate) {
		d = stablecoinFirstDate
	}
	return backfillOpts{Since: d, Yes: yes}, nil
}

func laterDate(a, b civil.Date) civil.Date {
	if a.After(b) {
		return a
	}
	return b
}

func earlierDate(a, b civil.Date) civil.Date {
	if a.Before(b) {
		return a
	}
	return b
}

// planBackfill lists batches newest first. end is safeEnd (exclusive).
func planBackfill(st syncState, since, end civil.Date) ([]backfillBatch, error) {
	var out []backfillBatch
	var from civil.Date
	if st.Cursor == "" {
		first := laterDate(since, civil.Date{Year: end.AddDays(-1).Year, Month: time.January, Day: 1})
		if !first.Before(end) {
			return nil, nil
		}
		out = append(out, backfillBatch{Label: strconv.Itoa(first.Year), Start: first, End: end,
			NewFrom: first.String(), NewCursor: end.AddDays(-1).String()})
		from = first
	} else {
		cur, err := civil.ParseDate(st.Cursor)
		if err != nil {
			return nil, fmt.Errorf("bad cursor %q", st.Cursor)
		}
		from = cur.AddDays(1)
		if st.CoverageFrom != "" {
			if from, err = civil.ParseDate(st.CoverageFrom); err != nil {
				return nil, fmt.Errorf("bad coverage_from %q", st.CoverageFrom)
			}
		}
		for s := cur.AddDays(1); s.Before(end); {
			e := earlierDate(s.AddDays(stablecoinMaxWindowDays), end)
			out = append(out, backfillBatch{Label: "forward gap", Start: s, End: e, NewCursor: e.AddDays(-1).String()})
			s = e
		}
	}
	for from.After(since) {
		start := laterDate(since, civil.Date{Year: from.AddDays(-1).Year, Month: time.January, Day: 1})
		out = append(out, backfillBatch{Label: strconv.Itoa(start.Year), Start: start, End: from, NewFrom: start.String()})
		from = start
	}
	return out, nil
}

func saveMetaLogged(ctx context.Context, store *riskStore, m backfillMeta) {
	if err := store.saveBackfillMeta(ctx, m); err != nil {
		slog.Warn("save backfill_meta failed", "error", err)
	}
}

func saveBackfillFailure(ctx context.Context, store *riskStore, base backfillMeta, err error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	total, usdt, usdc, _ := store.stablecoinEventCounts(saveCtx)
	base.Status = "failed"
	base.EventsStored = total
	base.USDTEvents = usdt
	base.USDCEvents = usdc
	base.Err = publicBackfillError(err) // served by GET /backfill; callers show or log the raw err
	saveMetaLogged(saveCtx, store, base)
}

func finalizeBackfillMeta(ctx context.Context, store *riskStore, base backfillMeta, now time.Time) error {
	st, err := store.getSyncState(ctx, stablecoinSourceID)
	if err != nil {
		return err
	}
	total, usdt, usdc, err := store.stablecoinEventCounts(ctx)
	if err != nil {
		return err
	}
	if st.CoverageFrom != "" {
		base.SinceDate = st.CoverageFrom
	}
	if st.Cursor != "" {
		base.ThroughDate = st.Cursor
	}
	if st.CoverageFrom != "" && st.CoverageFrom <= stablecoinFirstDate.String() {
		base.Status = "completed"
	} else {
		base.Status = "partial"
	}
	base.EventsStored = total
	base.USDTEvents = usdt
	base.USDCEvents = usdc
	base.CompletedAt = fmtTime(now)
	base.ProgressLabel = ""
	base.Err = ""
	return store.saveBackfillMeta(ctx, base)
}

// estimateBackfill checks the exported block watermark (~25 MB partitioned logs
// check), plans the batches, and runs free BigQuery dry-runs without mutating
// SQLite watermarks.
func estimateBackfill(ctx context.Context, src stablecoinSource, store *riskStore, since civil.Date, force bool, now time.Time) (batches []backfillBatch, sizes []int64, totalBytes int64, end civil.Date, st syncState, err error) {
	end, ok, err := safeEnd(ctx, src, now)
	if err != nil {
		return nil, nil, 0, civil.Date{}, syncState{}, fmt.Errorf("BigQuery block check: %w", err)
	}
	if !ok {
		return nil, nil, 0, civil.Date{}, syncState{}, errBlocksNotExported
	}
	st, err = store.getSyncState(ctx, stablecoinSourceID)
	if err != nil {
		return nil, nil, 0, civil.Date{}, syncState{}, err
	}
	planState := st
	if force && planState.Cursor != "" {
		if c, err := civil.ParseDate(planState.Cursor); err == nil {
			planState.CoverageFrom = c.AddDays(1).String()
		} else {
			planState.CoverageFrom = planState.Cursor
		}
	}
	batches, err = planBackfill(planState, since, end)
	if err != nil {
		return nil, nil, 0, civil.Date{}, syncState{}, err
	}
	sizes = make([]int64, len(batches))
	for i, b := range batches {
		n, err := src.DryRun(ctx, b.Start, b.End)
		if err != nil {
			return nil, nil, 0, civil.Date{}, syncState{}, fmt.Errorf("dry run %s: %w", b.Label, err)
		}
		sizes[i] = n
		totalBytes += n
	}
	return batches, sizes, totalBytes, end, st, nil
}

// runAddressRiskBackfill prints progress to out (stdout: slog goes to a file
// the operator does not see) and persists completion metadata in backfill_meta.
func runAddressRiskBackfill(ctx context.Context, src stablecoinSource, store *riskStore, opts backfillOpts, now time.Time, out io.Writer) error {
	prevMeta, _ := store.getBackfillMeta(ctx)
	startedAt := prevMeta.StartedAt
	if startedAt == "" || prevMeta.Status != "running" {
		startedAt = fmtTime(now)
	}
	meta := backfillMeta{
		Status:      "running",
		SinceDate:   opts.Since.String(),
		ThroughDate: prevMeta.ThroughDate,
		BytesBilled: prevMeta.BytesBilled,
		StartedAt:   startedAt,
	}

	batches, sizes, total, end, st, err := estimateBackfill(ctx, src, store, opts.Since, opts.Force, now)
	if err != nil {
		if opts.Yes {
			saveBackfillFailure(ctx, store, meta, err)
		}
		return err
	}
	meta.ThroughDate = end.AddDays(-1).String()
	if opts.MaxTotalBytes > 0 && total > opts.MaxTotalBytes {
		err := publicError{fmt.Sprintf("estimate grew to %.1f GB since it was confirmed (limit %.1f GB); nothing was billed — run the estimate again",
			float64(total)/1e9, float64(opts.MaxTotalBytes)/1e9)}
		if opts.Yes {
			saveBackfillFailure(ctx, store, meta, err)
		}
		return err
	}

	if len(batches) == 0 {
		fmt.Fprintf(out, "Nothing to backfill: coverage is %s to %s.\n", st.CoverageFrom, st.Cursor)
		if opts.Yes {
			if err := finalizeBackfillMeta(ctx, store, meta, now); err != nil {
				slog.Warn("save backfill_meta failed", "error", err)
			}
		}
		return nil
	}
	for i, b := range batches {
		fmt.Fprintf(out, "%-12s %s to %s  %8.1f GB  ~$%.2f\n", b.Label, b.Start, b.End.AddDays(-1), float64(sizes[i])/1e9, bytesToUSD(sizes[i]))
	}
	fmt.Fprintf(out, "Total: %.1f GB, ~$%.2f (dry-run estimate; actual billing is usually lower)\n", float64(total)/1e9, bytesToUSD(total))
	if !opts.Yes {
		fmt.Fprintln(out, "Dry run only — no backfill batches were run (only the ~25 MB block-watermark check ran). Re-run with --yes to backfill.")
		return nil
	}

	meta.ProgressLabel = fmt.Sprintf("0/%d batches", len(batches))
	saveMetaLogged(ctx, store, meta)

	for i, b := range batches {
		meta.ProgressLabel = fmt.Sprintf("%s (%d/%d)", b.Label, i+1, len(batches))
		saveMetaLogged(ctx, store, meta)

		maxBytes := max(sizes[i]+sizes[i]/10, backfillMinMaxBytes) // dry-run × 1.1
		events, billed, err := src.Fetch(ctx, b.Start, b.End, maxBytes)
		meta.BytesBilled += billed
		if err != nil {
			werr := fmt.Errorf("%s: %w (re-run to resume)", b.Label, err)
			saveBackfillFailure(ctx, store, meta, werr)
			return werr
		}
		if err := store.insertStablecoinEvents(ctx, events, b.NewFrom, b.NewCursor, now); err != nil {
			werr := fmt.Errorf("%s: store: %w", b.Label, err)
			saveBackfillFailure(ctx, store, meta, werr)
			return werr
		}
		totalCnt, usdtCnt, usdcCnt, _ := store.stablecoinEventCounts(ctx)
		meta.EventsStored = totalCnt
		meta.USDTEvents = usdtCnt
		meta.USDCEvents = usdcCnt
		saveMetaLogged(ctx, store, meta)
		fmt.Fprintf(out, "%-12s done: %d events, %.1f GB billed\n", b.Label, len(events), float64(billed)/1e9)
	}
	if err := finalizeBackfillMeta(ctx, store, meta, now); err != nil {
		return fmt.Errorf("save backfill meta: %w", err)
	}
	fmt.Fprintln(out, "Backfill complete.")
	return nil
}

// riskBackfillStatus describes the persisted historical backfill state and
// whether the local SQLite data matches the recorded post-backfill metadata.
type riskBackfillStatus struct {
	Status        string  `json:"status"` // "idle" | "partial" | "running" | "completed" | "failed" | "corrupted"
	Completed     bool    `json:"completed"`
	Running       bool    `json:"running"`
	Corrupted     bool    `json:"corrupted"`
	CorruptReason string  `json:"corrupt_reason,omitempty"`
	CanRun        bool    `json:"can_run"`
	SinceDate     string  `json:"since_date,omitempty"`
	ThroughDate   string  `json:"through_date,omitempty"`
	CoverageFrom  string  `json:"coverage_from,omitempty"`
	CoverageTo    string  `json:"coverage_to,omitempty"`
	EventsStored  int     `json:"events_stored"`
	LiveEvents    int     `json:"live_events"`
	USDTEvents    int     `json:"usdt_events"`
	USDCEvents    int     `json:"usdc_events"`
	BytesBilled   int64   `json:"bytes_billed"`
	EstimatedUSD  float64 `json:"estimated_usd"`
	DryRunReady   bool    `json:"dry_run_ready,omitempty"`
	DryRunBytes   int64   `json:"dry_run_bytes,omitempty"`
	DryRunUSD     float64 `json:"dry_run_usd,omitempty"`
	DryRunBatches int     `json:"dry_run_batches,omitempty"`
	DryRunToken   string  `json:"dry_run_token,omitempty"` // confirm must send it back within backfillPlanTTL
	StartedAt     string  `json:"started_at,omitempty"`
	CompletedAt   string  `json:"completed_at,omitempty"`
	ProgressLabel string  `json:"progress_label,omitempty"`
	Error         string  `json:"error,omitempty"`
}

// backfillStatus reads backfill_meta alongside live stablecoin_events counts
// and sync_state watermarks (strictly read-only).
func (s *riskStore) backfillStatus(ctx context.Context, activeInProcess bool) (riskBackfillStatus, error) {
	m, err := s.getBackfillMeta(ctx)
	if err != nil {
		return riskBackfillStatus{}, err
	}
	st, err := s.getSyncState(ctx, stablecoinSourceID)
	if err != nil {
		return riskBackfillStatus{}, err
	}
	total, usdt, usdc, err := s.stablecoinEventCounts(ctx)
	if err != nil {
		return riskBackfillStatus{}, err
	}

	fullCoverage := st.CoverageFrom != "" && st.CoverageFrom <= stablecoinFirstDate.String()

	// Recognize an already-completed CLI backfill that predated backfill_meta
	// without writing to SQLite during GET.
	if m.Status == "idle" && fullCoverage && total > 0 {
		m = backfillMeta{
			Status:       "completed",
			SinceDate:    st.CoverageFrom,
			ThroughDate:  st.Cursor,
			EventsStored: total,
			USDTEvents:   usdt,
			USDCEvents:   usdc,
			CompletedAt:  st.LastOKAt,
		}
	}

	// If a previous server process died mid-backfill, report it as failed so
	// the operator can resume.
	if m.Status == "running" && !activeInProcess {
		m.Status = "failed"
		if m.Err == "" {
			m.Err = "previous backfill was interrupted; click Resume Backfill to continue"
		}
	}

	var corrupted bool
	var corruptReason string
	if m.Status == "completed" {
		expectedSince := m.SinceDate
		if expectedSince == "" {
			expectedSince = stablecoinFirstDate.String()
		}
		switch {
		case st.CoverageFrom == "" || st.CoverageFrom > expectedSince:
			corrupted = true
			corruptReason = fmt.Sprintf("coverage_from is %q (expected ≤ %s)", st.CoverageFrom, expectedSince)
		case total == 0 || total < m.EventsStored:
			corrupted = true
			corruptReason = fmt.Sprintf("live event count (%d) is below backfilled baseline (%d)", total, m.EventsStored)
		case usdt < m.USDTEvents || usdc < m.USDCEvents:
			corrupted = true
			corruptReason = fmt.Sprintf("token event count dropped (USDT %d/%d, USDC %d/%d)", usdt, m.USDTEvents, usdc, m.USDCEvents)
		}
	}

	status := m.Status
	if activeInProcess {
		status = "running"
	} else if corrupted {
		status = "corrupted"
	} else if status == "completed" && !fullCoverage {
		status = "partial"
	}
	completed := status == "completed" && !corrupted && fullCoverage
	running := status == "running"

	return riskBackfillStatus{
		Status:        status,
		Completed:     completed,
		Running:       running,
		Corrupted:     corrupted,
		CorruptReason: corruptReason,
		CanRun:        !running && (!completed || corrupted),
		SinceDate:     m.SinceDate,
		ThroughDate:   m.ThroughDate,
		CoverageFrom:  st.CoverageFrom,
		CoverageTo:    st.Cursor,
		EventsStored:  m.EventsStored,
		LiveEvents:    total,
		USDTEvents:    usdt,
		USDCEvents:    usdc,
		BytesBilled:   m.BytesBilled,
		EstimatedUSD:  bytesToUSD(m.BytesBilled),
		StartedAt:     m.StartedAt,
		CompletedAt:   m.CompletedAt,
		ProgressLabel: m.ProgressLabel,
		Error:         m.Err,
	}, nil
}

func (r *addressRiskService) isBackfillRunning() bool {
	r.backfillMu.Lock()
	defer r.backfillMu.Unlock()
	return r.backfillRunning
}

// addressRiskBackfillHandler wraps AddressRiskBackfill with Go 1.25's
// Sec-Fetch-Site / Origin CSRF check for state-changing POST requests.
func addressRiskBackfillHandler(h *APIHandler) http.Handler {
	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(h.AddressRiskBackfill))
}

// AddressRiskBackfill handles GET/POST /api/opendata/crypto/address-risk/backfill.
//
//	POST {"dry_run":true[,"force":true]}   free BigQuery dry-run estimate plus a
//	                                       single-use dry_run_token
//	POST {"confirm":true,"dry_run_token":t[,"force":true]}
//	                                       starts the billed backfill
//
// The server enforces the order: confirm needs the token of the last estimate,
// within backfillPlanTTL and for the same mode (a corrupted store implies
// force), and the run stops before billing if its re-planned estimate exceeds
// the confirmed one by more than 10%.
func (h *APIHandler) AddressRiskBackfill(w http.ResponseWriter, r *http.Request) {
	if h.risk == nil || h.risk.store == nil {
		writeError(w, "address risk store is unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		st, err := h.risk.store.backfillStatus(r.Context(), h.risk.isBackfillRunning())
		if err != nil {
			slog.Error("address risk backfill status failed", "error", err)
			writeError(w, "failed to read backfill status", http.StatusInternalServerError)
			return
		}
		writeJSON(w, st)

	case http.MethodPost:
		if h.risk.bqSrc == nil {
			writeError(w, "BigQuery source is not configured", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			DryRun      bool   `json:"dry_run"`
			Confirm     bool   `json:"confirm"`
			Force       bool   `json:"force"`
			DryRunToken string `json:"dry_run_token"`
		}
		if r.ContentLength > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, 1024)
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
		}
		if body.DryRun == body.Confirm {
			writeError(w, "send dry_run=true for a free estimate, then confirm=true with the dry_run_token it returned", http.StatusBadRequest)
			return
		}

		h.risk.backfillMu.Lock()
		if h.risk.backfillRunning {
			h.risk.backfillMu.Unlock()
			writeError(w, "historical backfill is already running", http.StatusConflict)
			return
		}
		curStatus, err := h.risk.store.backfillStatus(r.Context(), false)
		if err != nil {
			h.risk.backfillMu.Unlock()
			slog.Error("address risk backfill precheck failed", "error", err)
			writeError(w, "failed to inspect backfill status", http.StatusInternalServerError)
			return
		}
		// Already completed and healthy: do not re-run unless explicitly forced.
		if curStatus.Completed && !curStatus.Corrupted && !body.Force {
			h.risk.backfillMu.Unlock()
			writeJSON(w, curStatus)
			return
		}

		forceRepair := body.Force || curStatus.Corrupted
		if body.DryRun {
			h.risk.backfillMu.Unlock()
			h.backfillDryRun(w, r, curStatus, forceRepair)
			return
		}

		// A failed check leaves the plan in place; only a started run uses it up.
		now := h.risk.now()
		plan := h.risk.pendingPlan
		if plan == nil || body.DryRunToken == "" ||
			subtle.ConstantTimeCompare([]byte(body.DryRunToken), []byte(plan.token)) != 1 ||
			now.Sub(plan.issuedAt) > backfillPlanTTL {
			h.risk.backfillMu.Unlock()
			writeError(w, "no matching estimate (missing, expired or already used); run the estimate again", http.StatusConflict)
			return
		}
		if plan.force != forceRepair {
			h.risk.backfillMu.Unlock()
			writeError(w, "the estimate was made for a different mode; run the estimate again", http.StatusConflict)
			return
		}
		h.risk.pendingPlan = nil
		maxTotal := max(plan.bytes+plan.bytes/10, 1) // confirmed estimate × 1.1 (1 byte: an empty plan must stay empty)

		saveMetaLogged(r.Context(), h.risk.store, backfillMeta{
			Status:        "running",
			SinceDate:     stablecoinFirstDate.String(),
			ThroughDate:   curStatus.ThroughDate,
			EventsStored:  curStatus.LiveEvents,
			USDTEvents:    curStatus.USDTEvents,
			USDCEvents:    curStatus.USDCEvents,
			BytesBilled:   curStatus.BytesBilled,
			StartedAt:     fmtTime(now),
			ProgressLabel: "planning batches",
		})
		h.risk.backfillRunning = true
		h.risk.backfillMu.Unlock()

		go func() {
			defer func() {
				h.risk.backfillMu.Lock()
				h.risk.backfillRunning = false
				h.risk.backfillMu.Unlock()
				if h.risk.invalidateCache != nil {
					h.risk.invalidateCache()
				}
			}()
			bgCtx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()
			opts := backfillOpts{Since: stablecoinFirstDate, Yes: true, Force: forceRepair, MaxTotalBytes: maxTotal}
			if err := runAddressRiskBackfill(bgCtx, h.risk.bqSrc, h.risk.store, opts, h.risk.now(), io.Discard); err != nil {
				slog.Warn("address risk historical backfill failed", "error", err)
			}
		}()

		st, err := h.risk.store.backfillStatus(r.Context(), true)
		if err != nil {
			writeError(w, "failed to read backfill status", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, st)

	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// backfillDryRun answers POST {"dry_run":true}: the free estimate plus the
// token that confirm must send back. A new estimate replaces the previous one.
func (h *APIHandler) backfillDryRun(w http.ResponseWriter, r *http.Request, st riskBackfillStatus, force bool) {
	batches, _, totalBytes, _, _, err := estimateBackfill(r.Context(), h.risk.bqSrc, h.risk.store, stablecoinFirstDate, force, h.risk.now())
	if err != nil {
		slog.Warn("address risk backfill estimate failed", "error", err)
		code := http.StatusBadGateway
		if errors.Is(err, errBlocksNotExported) {
			code = http.StatusServiceUnavailable
		}
		writeError(w, publicBackfillError(err), code)
		return
	}
	token := rand.Text()
	h.risk.backfillMu.Lock()
	if h.risk.backfillRunning { // started by another request while this estimate ran
		h.risk.backfillMu.Unlock()
		writeError(w, "historical backfill is already running", http.StatusConflict)
		return
	}
	h.risk.pendingPlan = &backfillPlan{token: token, force: force, bytes: totalBytes, issuedAt: h.risk.now()}
	h.risk.backfillMu.Unlock()

	st.DryRunReady = true
	st.DryRunBytes = totalBytes
	st.DryRunUSD = bytesToUSD(totalBytes)
	st.DryRunBatches = len(batches)
	st.DryRunToken = token
	writeJSON(w, st)
}

// runBackfillCLI is the --address-risk-backfill entry point; it returns the
// process exit code. Only the config, BigQuery client and store are created.
func runBackfillCLI(ctx context.Context, cfg *Config, src stablecoinSource, since string, sinceDays int, yes bool) int {
	opts, err := parseBackfillOpts(since, sinceDays, yes, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	store, err := openRiskStore(cfg.AddressRisk.dbPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: open", cfg.AddressRisk.dbPath()+":", err)
		return 1
	}
	defer store.Close()
	if err := runAddressRiskBackfill(ctx, src, store, opts, time.Now(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
