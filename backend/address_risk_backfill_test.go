package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func TestParseBackfillOpts(t *testing.T) {
	now := at(6, 0)
	tests := []struct {
		name      string
		since     string
		sinceDays int
		want      string
		wantErr   bool
	}{
		{"default is full history", "", 0, "2017-11-28", false},
		{"explicit since", "2024-01-01", 0, "2024-01-01", false},
		{"since-days", "", 90, "2026-06-28", false},
		{"before the first freeze clamps", "2015-01-01", 0, "2017-11-28", false},
		{"both flags", "2024-01-01", 30, "", true},
		{"bad date", "2024/01/01", 0, "", true},
		{"negative days", "", -5, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parseBackfillOpts(tt.since, tt.sinceDays, false, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && o.Since.String() != tt.want {
				t.Errorf("since = %s, want %s", o.Since, tt.want)
			}
		})
	}
}

func batchesString(bs []backfillBatch) string {
	var parts []string
	for _, b := range bs {
		parts = append(parts, b.Label+":"+b.Start.String()+".."+b.End.String()+" from="+b.NewFrom+" cursor="+b.NewCursor)
	}
	return strings.Join(parts, "\n")
}

func TestPlanBackfill(t *testing.T) {
	d := func(s string) civil.Date { x, _ := civil.ParseDate(s); return x }
	tests := []struct {
		name  string
		st    syncState
		since string
		want  string
	}{
		{"after a 30-day cold start: whole years, newest first",
			syncState{CoverageFrom: "2026-08-27", Cursor: "2026-09-25"}, "2024-06-01",
			"2026:2026-01-01..2026-08-27 from=2026-01-01 cursor=\n" +
				"2025:2025-01-01..2026-01-01 from=2025-01-01 cursor=\n" +
				"2024:2024-06-01..2025-01-01 from=2024-06-01 cursor="},
		// Cursor today-40 (spec §15 says "31 + 9"; [today-39, today) is 39 days = 31 + 8).
		{"forward gap after a long outage comes first, in 31-day chunks",
			syncState{CoverageFrom: "2026-08-01", Cursor: "2026-08-17"}, "2026-08-01",
			"forward gap:2026-08-18..2026-09-18 from= cursor=2026-09-17\n" +
				"forward gap:2026-09-18..2026-09-26 from= cursor=2026-09-25"},
		{"empty database: first batch sets both watermarks",
			syncState{}, "2025-03-01",
			"2026:2026-01-01..2026-09-26 from=2026-01-01 cursor=2026-09-25\n" +
				"2025:2025-03-01..2026-01-01 from=2025-03-01 cursor="},
		{"already covered", syncState{CoverageFrom: "2017-11-28", Cursor: "2026-09-25"}, "2017-11-28", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planBackfill(tt.st, d(tt.since), syncToday)
			if err != nil {
				t.Fatal(err)
			}
			if s := batchesString(got); s != tt.want {
				t.Errorf("plan =\n%s\nwant\n%s", s, tt.want)
			}
		})
	}
}

func TestBackfillDryRunBillsNothing(t *testing.T) {
	src := &fakeStableSource{exportedUntil: at(5, 0), dryBytes: 730e9}
	store := newTestRiskStore(t)
	setCursor(t, store, civil.Date{Year: 2026, Month: 8, Day: 27}, syncToday.AddDays(-1))
	var out bytes.Buffer
	err := runAddressRiskBackfill(context.Background(), src, store, backfillOpts{Since: civil.Date{Year: 2025, Month: 1, Day: 1}}, at(6, 0), &out)
	if err != nil {
		t.Fatal(err)
	}
	if src.fetchCount() != 0 || len(src.dryRuns) != 2 {
		t.Errorf("fetches %d dry runs %d, want 0 and 2", src.fetchCount(), len(src.dryRuns))
	}
	if !strings.Contains(out.String(), "~$4.15") || !strings.Contains(out.String(), "Re-run with --yes") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestBackfillRunsBatchesWithDryRunCap(t *testing.T) {
	src := &fakeStableSource{exportedUntil: at(5, 0), dryBytes: 100e9}
	a := "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	src.events = []stablecoinEvent{{TxHash: "0xold", LogIndex: 1, Token: "USDT", Action: "freeze", Address: a, BlockNumber: 14e6, BlockTime: "2025-04-14T10:00:00Z"}}
	store := newTestRiskStore(t)
	setCursor(t, store, civil.Date{Year: 2026, Month: 8, Day: 27}, syncToday.AddDays(-1))
	var out bytes.Buffer
	err := runAddressRiskBackfill(context.Background(), src, store, backfillOpts{Since: civil.Date{Year: 2025, Month: 1, Day: 1}, Yes: true}, at(6, 0), &out)
	if err != nil {
		t.Fatal(err)
	}
	if src.fetchCount() != 2 {
		t.Fatalf("fetches = %d", src.fetchCount())
	}
	for _, f := range src.fetches {
		if f.maxBytes != 110e9 {
			t.Errorf("maxBytes = %d, want dry-run × 1.1", f.maxBytes)
		}
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.CoverageFrom != "2025-01-01" || st.Cursor != "2026-09-25" || st.RowCount != 1 {
		t.Errorf("state = %+v", st)
	}
}

// A failing year leaves coverage_from at the last completed year, so a
// re-run resumes instead of starting over.
func TestBackfillResumesAfterFailure(t *testing.T) {
	src := &fakeStableSource{exportedUntil: at(5, 0), dryBytes: 1e9}
	store := newTestRiskStore(t)
	setCursor(t, store, civil.Date{Year: 2026, Month: 8, Day: 27}, syncToday.AddDays(-1))
	failing := &failAfterSource{fakeStableSource: src, okFetches: 1}
	var out bytes.Buffer
	if err := runAddressRiskBackfill(context.Background(), failing, store, backfillOpts{Since: civil.Date{Year: 2024, Month: 1, Day: 1}, Yes: true}, at(6, 0), &out); err == nil {
		t.Fatal("expected an error from the second batch")
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.CoverageFrom != "2026-01-01" {
		t.Fatalf("coverage_from = %s, want 2026-01-01 after one good batch", st.CoverageFrom)
	}
	src.fetches = nil
	if err := runAddressRiskBackfill(context.Background(), src, store, backfillOpts{Since: civil.Date{Year: 2024, Month: 1, Day: 1}, Yes: true}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}
	if len(src.fetches) != 2 || src.fetches[0].start.Year != 2025 {
		t.Errorf("resume fetched %+v, want 2025 then 2024", src.fetches)
	}
}

func TestBackfillSourceNotCaughtUp(t *testing.T) {
	src := &fakeStableSource{exportedUntil: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}
	var out bytes.Buffer
	err := runAddressRiskBackfill(context.Background(), src, newTestRiskStore(t), backfillOpts{Since: stablecoinFirstDate, Yes: true}, at(6, 0), &out)
	if err == nil || src.fetchCount() != 0 {
		t.Errorf("err = %v, fetches = %d", err, src.fetchCount())
	}
}

type failAfterSource struct {
	*fakeStableSource
	okFetches int
}

func (f *failAfterSource) Fetch(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stablecoinEvent, int64, error) {
	if f.okFetches == 0 {
		return nil, 0, context.DeadlineExceeded
	}
	f.okFetches--
	return f.fakeStableSource.Fetch(ctx, start, end, maxBytes)
}

func TestBackfillShowsBlockCheckError(t *testing.T) {
	src := &fakeStableSource{checkErr: errors.New("googleapi: Error 403: Access Denied")}
	var out bytes.Buffer
	err := runAddressRiskBackfill(context.Background(), src, newTestRiskStore(t), backfillOpts{Since: stablecoinFirstDate}, at(6, 0), &out)
	if err == nil || !strings.Contains(err.Error(), "Access Denied") {
		t.Errorf("err = %v, want the BigQuery error shown to the operator", err)
	}
}

func TestBackfillMetaPersistenceAndCorruptionDetection(t *testing.T) {
	ctx := context.Background()
	src := &fakeStableSource{
		exportedUntil: at(5, 0),
		dryBytes:      1e9,
		events: []stablecoinEvent{
			{TxHash: "0x1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", BlockNumber: 1, BlockTime: "2020-01-01T00:00:00Z"},
			{TxHash: "0x2", LogIndex: 2, Token: "USDC", Action: "freeze", Address: "0x2222222222222222222222222222222222222222", BlockNumber: 2, BlockTime: "2021-01-01T00:00:00Z"},
		},
	}
	store := newTestRiskStore(t)
	setCursor(t, store, civil.Date{Year: 2026, Month: 1, Day: 1}, syncToday.AddDays(-1))

	var out bytes.Buffer
	if err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}

	st, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Completed || st.Corrupted || st.CanRun || st.EventsStored != 2 || st.USDTEvents != 1 || st.USDCEvents != 1 || st.SinceDate != "2017-11-28" {
		t.Fatalf("healthy status = %+v", st)
	}

	// Simulate data corruption by deleting one row from stablecoin_events.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM stablecoin_events WHERE tx_hash = '0x1'`); err != nil {
		t.Fatal(err)
	}
	stCorrupt, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if stCorrupt.Status != "corrupted" || !stCorrupt.Corrupted || !stCorrupt.CanRun || stCorrupt.LiveEvents != 1 {
		t.Fatalf("corrupted status = %+v", stCorrupt)
	}

	// A failed forced re-sync (e.g. before 00:30 UTC when blocks aren't exported yet) must NOT
	// regress sync_state.coverage_from in SQLite.
	_ = runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true, Force: true}, at(0, 10), &out)
	syncSt, err := store.getSyncState(ctx, stablecoinSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if syncSt.CoverageFrom != "2017-11-28" {
		t.Fatalf("failed Force run regressed coverage_from to %q, want 2017-11-28 preserved", syncSt.CoverageFrom)
	}

	// Repair with Force: true re-scans back to 2017-11-28 in memory and restores the missing event.
	if err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true, Force: true}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}
	stRepaired, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !stRepaired.Completed || stRepaired.Corrupted || stRepaired.LiveEvents != 2 {
		t.Fatalf("repaired status = %+v", stRepaired)
	}
}

func TestBackfillPartialVsFullStatus(t *testing.T) {
	ctx := context.Background()
	src := &fakeStableSource{
		exportedUntil: at(5, 0),
		dryBytes:      1e9,
		events: []stablecoinEvent{
			{TxHash: "0x1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", BlockNumber: 1, BlockTime: "2020-01-01T00:00:00Z"},
		},
	}
	store := newTestRiskStore(t)

	// Partial backfill (--since 2026-01-01 --yes) must be marked as "partial", Completed=false, CanRun=true.
	var out bytes.Buffer
	if err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: civil.Date{Year: 2026, Month: 1, Day: 1}, Yes: true}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}
	stPartial, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if stPartial.Status != "partial" || stPartial.Completed || !stPartial.CanRun || stPartial.SinceDate != "2026-01-01" {
		t.Fatalf("partial status = %+v, want status=partial completed=false can_run=true since_date=2026-01-01", stPartial)
	}

	// Completing the remaining history down to 2017-11-28 upgrades status to "completed", Completed=true, CanRun=false.
	if err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}
	stFull, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if stFull.Status != "completed" || !stFull.Completed || stFull.CanRun || stFull.SinceDate != "2017-11-28" {
		t.Fatalf("full status = %+v, want status=completed completed=true can_run=false since_date=2017-11-28", stFull)
	}
}

func TestBackfillGetIsReadOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	if err := store.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0x1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", BlockNumber: 1, BlockTime: "2020-01-01T00:00:00Z"},
	}, stablecoinFirstDay, syncToday.AddDays(-1).String(), time.Now()); err != nil {
		t.Fatal(err)
	}

	st, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "completed" || !st.Completed || st.CanRun {
		t.Fatalf("adopted status = %+v, want status=completed completed=true can_run=false", st)
	}
	// Verify that backfillStatus did NOT write a backfill_meta row to the database.
	metaSt, err := store.getBackfillMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metaSt.Status != "idle" {
		t.Fatalf("backfillStatus wrote to backfill_meta: %+v", metaSt)
	}
}

// The server, not the UI, enforces estimate → confirm: confirm needs the
// single-use token of the latest estimate, unexpired and for the same mode,
// and the run stops unbilled if its re-plan grew more than 10%.
func TestAddressRiskBackfillHTTPRequiresDryRunThenConfirm(t *testing.T) {
	src := &fakeStableSource{
		exportedUntil: at(5, 0),
		dryBytes:      2e9, // × 10 yearly batches (2026…2017) = 20 GB
		events: []stablecoinEvent{
			{TxHash: "0x1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", BlockNumber: 1, BlockTime: "2020-01-01T00:00:00Z"},
		},
	}
	store := newTestRiskStore(t)
	svc := newAddressRiskService(store, nil)
	svc.bqSrc = src
	clock := at(6, 0)
	svc.now = func() time.Time { return clock }
	h := &APIHandler{risk: svc}

	const path = "/api/opendata/crypto/address-risk/backfill"
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.AddressRiskBackfill(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return w
	}
	expect := func(body string, code int, msg string) {
		t.Helper()
		if w := post(body); w.Code != code || !strings.Contains(w.Body.String(), msg) {
			t.Fatalf("POST %s = %d %q, want %d containing %q", body, w.Code, w.Body.String(), code, msg)
		}
	}
	dryRun := func() string {
		t.Helper()
		w := post(`{"dry_run":true}`)
		var st riskBackfillStatus
		if w.Code != http.StatusOK {
			t.Fatalf("dry run = %d %q", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if !st.DryRunReady || st.DryRunToken == "" || st.DryRunBytes != 20e9 || st.DryRunBatches != 10 {
			t.Fatalf("dry run status = %+v", st)
		}
		return st.DryRunToken
	}
	waitIdle := func() {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); svc.isBackfillRunning(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("backfill still running")
			}
		}
	}
	confirm := func(tok string) string { return `{"confirm":true,"dry_run_token":"` + tok + `"}` }
	const noPlan = "no matching estimate"

	expect(`{}`, http.StatusBadRequest, "dry_run=true")
	expect(`{"dry_run":true,"confirm":true}`, http.StatusBadRequest, "dry_run=true")
	expect(`{"confirm":true}`, http.StatusConflict, noPlan) // the old one-click path

	tok1 := dryRun()
	gw := httptest.NewRecorder()
	h.AddressRiskBackfill(gw, httptest.NewRequest(http.MethodGet, path, nil))
	if strings.Contains(gw.Body.String(), "dry_run_token") {
		t.Fatalf("GET leaked the token: %s", gw.Body.String())
	}
	expect(`{"confirm":true}`, http.StatusConflict, noPlan)
	expect(confirm("wrong"), http.StatusConflict, noPlan)
	expect(`{"confirm":true,"force":true,"dry_run_token":"`+tok1+`"}`, http.StatusConflict, "different mode")
	clock = at(6, 16) // past backfillPlanTTL
	expect(confirm(tok1), http.StatusConflict, noPlan)
	clock = at(6, 0)
	if src.fetchCount() != 0 {
		t.Fatalf("fetches = %d before any valid confirm", src.fetchCount())
	}

	// The failed attempts above did not use tok1 up. The re-plan now costs
	// 30 GB against 20 GB confirmed, so the run stops before billing.
	src.mu.Lock()
	src.dryBytes = 3e9
	src.mu.Unlock()
	expect(confirm(tok1), http.StatusAccepted, `"running":true`)
	waitIdle()
	st, err := store.backfillStatus(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "failed" || !strings.Contains(st.Error, "estimate grew to 30.0 GB") || src.fetchCount() != 0 {
		t.Fatalf("grown estimate: status = %+v, fetches = %d", st, src.fetchCount())
	}

	src.mu.Lock()
	src.dryBytes = 2e9
	src.mu.Unlock()
	stale := dryRun()
	tok2 := dryRun() // replaces the previous estimate
	if stale == tok2 || tok1 == tok2 {
		t.Fatal("tokens must be unique")
	}
	expect(confirm(stale), http.StatusConflict, noPlan)
	expect(confirm(tok2), http.StatusAccepted, `"running":true`)
	waitIdle()
	st, err = store.backfillStatus(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Completed || st.CanRun || src.fetchCount() != 10 {
		t.Fatalf("final status = %+v, fetches = %d", st, src.fetchCount())
	}
	// Single use: a forced re-sync cannot reuse tok2.
	expect(`{"confirm":true,"force":true,"dry_run_token":"`+tok2+`"}`, http.StatusConflict, noPlan)
	svc.backfillMu.Lock()
	defer svc.backfillMu.Unlock()
	if svc.pendingPlan != nil {
		t.Errorf("pendingPlan = %+v after a started run, want nil", svc.pendingPlan)
	}
}

func TestBackfillStopsWhenEstimateGrowsPastConfirmed(t *testing.T) {
	ctx := context.Background()
	src := &fakeStableSource{exportedUntil: at(5, 0), dryBytes: 3e9}
	store := newTestRiskStore(t)
	var out bytes.Buffer
	err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true, MaxTotalBytes: 22e9}, at(6, 0), &out)
	var pe publicError
	if !errors.As(err, &pe) || !strings.Contains(pe.msg, "30.0 GB") || !strings.Contains(pe.msg, "limit 22.0 GB") {
		t.Fatalf("err = %v, want the estimate-grew error", err)
	}
	if src.fetchCount() != 0 {
		t.Fatalf("fetches = %d, want 0: nothing may be billed", src.fetchCount())
	}
	st, err := store.backfillStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "failed" || st.Error != pe.msg {
		t.Fatalf("status = %+v, want failed with %q", st, pe.msg)
	}

	// Exactly at the cap still runs.
	src.mu.Lock()
	src.dryBytes = 2.2e9
	src.mu.Unlock()
	if err := runAddressRiskBackfill(ctx, src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true, MaxTotalBytes: 22e9}, at(6, 0), &out); err != nil {
		t.Fatal(err)
	}
	if src.fetchCount() != 10 {
		t.Fatalf("fetches = %d, want 10", src.fetchCount())
	}
}

// Dry-run failures return a fixed message; BigQuery's text (project and job
// IDs) goes only to the server log, and no token is issued.
func TestAddressRiskBackfillDryRunErrorsArePublic(t *testing.T) {
	for _, tt := range []struct {
		name     string
		src      *fakeStableSource
		wantCode int
		wantBody string
	}{
		{"BigQuery error", &fakeStableSource{checkErr: errors.New("googleapi: Error 403: Access Denied: Project secret-proj-123: job secret-proj-123:US.bqjob_r1")},
			http.StatusBadGateway, "BigQuery or database error"},
		{"blocks not exported yet", &fakeStableSource{exportedUntil: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)},
			http.StatusServiceUnavailable, errBlocksNotExported.msg},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAddressRiskService(newTestRiskStore(t), nil)
			svc.bqSrc = tt.src
			svc.now = func() time.Time { return at(6, 0) }
			h := &APIHandler{risk: svc}
			w := httptest.NewRecorder()
			h.AddressRiskBackfill(w, httptest.NewRequest(http.MethodPost, "/api/opendata/crypto/address-risk/backfill", strings.NewReader(`{"dry_run":true}`)))
			if w.Code != tt.wantCode || !strings.Contains(w.Body.String(), tt.wantBody) || strings.Contains(w.Body.String(), "secret-proj") {
				t.Errorf("POST dry_run = %d %q, want %d containing %q", w.Code, w.Body.String(), tt.wantCode, tt.wantBody)
			}
			svc.backfillMu.Lock()
			defer svc.backfillMu.Unlock()
			if svc.pendingPlan != nil {
				t.Error("a failed estimate must not issue a token")
			}
		})
	}
}

// GET /backfill serves the stored error, so a failed run stores the fixed
// message; the CLI still returns (and prints) the raw error.
func TestBackfillFailureStoredWithoutRawBigQueryText(t *testing.T) {
	src := &fakeStableSource{exportedUntil: at(5, 0), dryBytes: 1e9, fetchErr: errors.New("googleapi: Error 403: Access Denied: Project secret-proj-123")}
	store := newTestRiskStore(t)
	var out bytes.Buffer
	err := runAddressRiskBackfill(context.Background(), src, store, backfillOpts{Since: stablecoinFirstDate, Yes: true}, at(6, 0), &out)
	if err == nil || !strings.Contains(err.Error(), "secret-proj-123") {
		t.Fatalf("err = %v, want the raw error for the CLI", err)
	}
	st, err := store.backfillStatus(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "failed" || strings.Contains(st.Error, "secret-proj") || !strings.Contains(st.Error, "server log") {
		t.Fatalf("stored status = %+v, want the fixed message", st)
	}
}
