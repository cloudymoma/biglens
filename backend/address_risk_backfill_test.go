package main

import (
	"bytes"
	"context"
	"errors"
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
