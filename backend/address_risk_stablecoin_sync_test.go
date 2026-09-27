package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

type fetchCall struct {
	start, end civil.Date
	maxBytes   int64
}

// fakeStableSource stands in for BigQuery. Blocks are "exported" up to
// exportedUntil; Fetch returns the configured events inside the window.
type fakeStableSource struct {
	mu            sync.Mutex
	exportedUntil time.Time
	events        []stablecoinEvent
	dryBytes      int64
	fetchErr      error
	fetchBilled   int64 // bytes BigQuery billed before fetchErr happened
	checkErr      error
	blockFetch    bool // Fetch hangs until ctx is done
	panicOnFetch  bool
	fetches       []fetchCall
	checks        []civil.Date
	dryRuns       []fetchCall
}

func (f *fakeStableSource) Fetch(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stablecoinEvent, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches = append(f.fetches, fetchCall{start, end, maxBytes})
	if f.panicOnFetch {
		panic("bigquery client bug")
	}
	if f.blockFetch {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, 0, ctx.Err()
	}
	if f.fetchErr != nil {
		return nil, f.fetchBilled, f.fetchErr
	}
	var out []stablecoinEvent
	for _, e := range f.events {
		d := e.BlockTime[:10]
		if d >= start.String() && d < end.String() {
			out = append(out, e)
		}
	}
	return out, 3 << 30, nil
}

func (f *fakeStableSource) DryRun(_ context.Context, start, end civil.Date) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dryRuns = append(f.dryRuns, fetchCall{start: start, end: end})
	return f.dryBytes, nil
}

func (f *fakeStableSource) MaxBlockTime(_ context.Context, end civil.Date) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, end)
	if f.checkErr != nil {
		return time.Time{}, f.checkErr
	}
	lo, hi := dayStart(end), dayStart(end).Add(2*time.Hour)
	if f.exportedUntil.Before(lo) {
		return time.Time{}, nil // no block in the window yet
	}
	if f.exportedUntil.After(hi) {
		return hi.Add(-12 * time.Second), nil
	}
	return f.exportedUntil, nil
}

func (f *fakeStableSource) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fetches)
}

// at builds a UTC time on 2026-09-26 (today in these tests).
func at(hh, mm int) time.Time { return time.Date(2026, 9, 26, hh, mm, 0, 0, time.UTC) }

var syncToday = civil.Date{Year: 2026, Month: time.September, Day: 26}

func newTestStableSyncer(t *testing.T, now time.Time, days int) (*stablecoinSyncer, *fakeStableSource, *riskStore) {
	t.Helper()
	src := &fakeStableSource{exportedUntil: now.Add(-12 * time.Second), dryBytes: 89e9}
	store := newTestRiskStore(t)
	return &stablecoinSyncer{store: store, src: src, now: func() time.Time { return now }, initialDays: days}, src, store
}

func setCursor(t *testing.T, store *riskStore, from, cursor civil.Date) {
	t.Helper()
	if err := store.insertStablecoinEvents(context.Background(), nil, from.String(), cursor.String(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestSafeEnd(t *testing.T) {
	tests := []struct {
		name       string
		now        time.Time
		exported   time.Time
		want       civil.Date
		wantOK     bool
		wantChecks []civil.Date
	}{
		{"01:00, today exported past 00:30", at(1, 0), at(0, 59), syncToday, true, []civil.Date{syncToday}},
		{"00:10 checks yesterday only", at(0, 10), at(0, 9), syncToday.AddDays(-1), true, []civil.Date{syncToday.AddDays(-1)}},
		{"01:00, export lagging at 00:20 → falls back to yesterday", at(1, 0), at(0, 20), syncToday.AddDays(-1), true, []civil.Date{syncToday, syncToday.AddDays(-1)}},
		{"export stuck the day before → unavailable", at(1, 0), time.Date(2026, 9, 25, 0, 10, 0, 0, time.UTC), civil.Date{}, false, []civil.Date{syncToday, syncToday.AddDays(-1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &fakeStableSource{exportedUntil: tt.exported}
			got, ok, err := safeEnd(context.Background(), src, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				got = civil.Date{}
			}
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("safeEnd = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
			if len(src.checks) != len(tt.wantChecks) {
				t.Fatalf("checks = %v, want %v", src.checks, tt.wantChecks)
			}
			for i := range tt.wantChecks {
				if src.checks[i] != tt.wantChecks[i] {
					t.Errorf("check %d = %v, want %v", i, src.checks[i], tt.wantChecks[i])
				}
			}
		})
	}
}

func TestStablecoinColdStart(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	s.syncOnce(context.Background())
	if len(src.fetches) != 1 {
		t.Fatalf("fetches = %d, want 1", len(src.fetches))
	}
	f := src.fetches[0]
	if f.start != syncToday.AddDays(-30) || f.end != syncToday {
		t.Errorf("window = [%v, %v), want [today-30, today)", f.start, f.end)
	}
	// The 150 GiB cap must admit the ~89 GB cold start (spec §7.1).
	if f.maxBytes != 150<<30 || f.maxBytes < 89e9 {
		t.Errorf("maxBytes = %d", f.maxBytes)
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.CoverageFrom != "2026-08-27" || st.Cursor != "2026-09-25" || st.LastError != "" {
		t.Errorf("state = %+v", st)
	}
}

// Started at 00:10: only yesterday is checked, the cold start ends at
// today-1, and the next tick after 00:30 picks up the last day.
func TestStablecoinColdStartJustAfterMidnight(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(0, 10), 30)
	s.syncOnce(context.Background())
	if len(src.checks) != 1 || src.checks[0] != syncToday.AddDays(-1) {
		t.Errorf("checks = %v, want only today-1", src.checks)
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.Cursor != "2026-09-24" {
		t.Errorf("cursor = %s, want today-2", st.Cursor)
	}
}

func TestStablecoinColdStartDisabled(t *testing.T) {
	s, src, _ := newTestStableSyncer(t, at(6, 0), 0)
	s.syncOnce(context.Background())
	if len(src.fetches) != 0 || len(src.checks) != 0 {
		t.Errorf("initial_sync_days=0 must not touch BigQuery: fetches %d checks %d", len(src.fetches), len(src.checks))
	}
}

func TestStablecoinIncremental(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		cursor      civil.Date
		wantFetches int
		wantChecks  int
		wantWindow  [2]civil.Date
		wantCursor  string
		wantErr     string
	}{
		{"up to date after 00:30", at(6, 0), syncToday.AddDays(-1), 0, 0, [2]civil.Date{}, "2026-09-25", ""},
		{"00:10 with cursor today-2: coarse skip, no check", at(0, 10), syncToday.AddDays(-2), 0, 0, [2]civil.Date{}, "2026-09-24", ""},
		{"00:10 with cursor today-3 fetches [today-2, today-1)", at(0, 10), syncToday.AddDays(-3), 1, 1, [2]civil.Date{syncToday.AddDays(-2), syncToday.AddDays(-1)}, "2026-09-24", ""},
		{"one day behind after 00:30", at(6, 0), syncToday.AddDays(-2), 1, 1, [2]civil.Date{syncToday.AddDays(-1), syncToday}, "2026-09-25", ""},
		{"gap of 40 days is left to the backfill CLI", at(6, 0), syncToday.AddDays(-41), 0, 1, [2]civil.Date{}, "2026-08-16", "gap too large, run --address-risk-backfill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, src, store := newTestStableSyncer(t, tt.now, 30)
			setCursor(t, store, tt.cursor.AddDays(-30), tt.cursor)
			s.syncOnce(context.Background())
			if len(src.fetches) != tt.wantFetches || len(src.checks) != tt.wantChecks {
				t.Fatalf("fetches %d checks %d, want %d and %d", len(src.fetches), len(src.checks), tt.wantFetches, tt.wantChecks)
			}
			if tt.wantFetches == 1 && (src.fetches[0].start != tt.wantWindow[0] || src.fetches[0].end != tt.wantWindow[1]) {
				t.Errorf("window = [%v, %v), want %v", src.fetches[0].start, src.fetches[0].end, tt.wantWindow)
			}
			st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
			if st.Cursor != tt.wantCursor || st.LastError != tt.wantErr {
				t.Errorf("cursor %s last_error %q, want %s %q", st.Cursor, st.LastError, tt.wantCursor, tt.wantErr)
			}
		})
	}
}

// If blocks are not exported past the window end, the logs query must not
// run at all: the cursor would otherwise skip events that arrive later.
func TestStablecoinIntegrityCheckFailureSkipsFetch(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	setCursor(t, store, syncToday.AddDays(-32), syncToday.AddDays(-2))
	src.exportedUntil = time.Date(2026, 9, 25, 0, 5, 0, 0, time.UTC)
	s.syncOnce(context.Background())
	if len(src.fetches) != 0 {
		t.Fatalf("fetches = %d, want 0", len(src.fetches))
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != "source not caught up" {
		t.Errorf("state = %+v", st)
	}
}

func TestStablecoinFetchErrorKeepsCursor(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	setCursor(t, store, syncToday.AddDays(-32), syncToday.AddDays(-2))
	src.fetchErr = errors.New("googleapi: Error 400: Query exceeded limit for bytes billed")
	s.syncOnce(context.Background())
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.Cursor != "2026-09-24" || st.LastError != "bigquery_error" {
		t.Errorf("state = %+v", st)
	}
	src.fetchErr = context.DeadlineExceeded
	s.syncOnce(context.Background())
	st, _ = store.getSyncState(context.Background(), stablecoinSourceID)
	if st.LastError != "timeout" {
		t.Errorf("last_error = %q, want timeout", st.LastError)
	}
}

func TestStablecoinSyncStoresEventsAndNotifies(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	a := "0xe07bd590e1198666230932bad5db3dbbe21e7d57"
	src.events = []stablecoinEvent{
		{TxHash: "0xf1", LogIndex: 1, Token: "USDC", Action: "freeze", Address: a, BlockNumber: 1, BlockTime: "2026-09-25T05:00:59Z"},
		{TxHash: "0xf2", LogIndex: 1, Token: "USDT", Action: "freeze", Address: a, BlockNumber: 2, BlockTime: "2026-09-25T12:19:23Z"},
		{TxHash: "0xf3", LogIndex: 1, Token: "USDT", Action: "freeze", Address: a, BlockNumber: 3, BlockTime: "2026-09-26T01:00:00Z"}, // today: outside the window
	}
	notified := 0
	s.onChange = func() { notified++ }
	s.syncOnce(context.Background())
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.RowCount != 2 || notified != 1 {
		t.Errorf("rows %d notified %d, want 2 and 1", st.RowCount, notified)
	}
	s.syncOnce(context.Background()) // up to date now
	if notified != 1 || src.fetchCount() != 1 {
		t.Errorf("an idle tick must not query or notify: notified %d fetches %d", notified, src.fetchCount())
	}
}

func TestStablecoinSyncRecoversFromPanic(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	src.panicOnFetch = true
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	runAddressRiskSync(ctx, 40*time.Millisecond, s.syncOnce)
	if src.fetchCount() < 2 {
		t.Errorf("fetches = %d; a panic must not stop later ticks", src.fetchCount())
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.LastError != "panic" || strings.Contains(st.LastError, "bigquery client bug") {
		t.Errorf("last_error = %q, want the class only", st.LastError)
	}
}

// A failure after BigQuery already billed the query (bad row, store write
// error) must not re-run that query every hour: a failing 30-day cold start
// would otherwise bill ~$0.5 per tick.
func TestStablecoinBilledFailureBacksOff(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	cur := at(6, 0)
	s.now = func() time.Time { return cur }
	src.fetchErr, src.fetchBilled = errors.New("row 0xabc:1: bad address"), 89e9
	s.syncOnce(context.Background())
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if src.fetchCount() != 1 || st.LastError != "bigquery_error" {
		t.Fatalf("fetches %d last_error %q", src.fetchCount(), st.LastError)
	}
	cur = cur.Add(time.Hour)
	s.syncOnce(context.Background())
	if src.fetchCount() != 1 {
		t.Errorf("billed failure retried after 1h: fetches = %d", src.fetchCount())
	}
	cur = cur.Add(6 * time.Hour)
	s.syncOnce(context.Background())
	if src.fetchCount() != 2 {
		t.Errorf("no retry after the back-off: fetches = %d", src.fetchCount())
	}
}

func TestStablecoinStoreFailureBacksOff(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	cur := at(6, 0)
	s.now = func() time.Time { return cur }
	if _, err := store.db.Exec(`DROP TABLE stablecoin_events`); err != nil {
		t.Fatal(err)
	}
	s.syncOnce(context.Background())
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.LastError != "unavailable" {
		t.Fatalf("last_error = %q", st.LastError)
	}
	cur = cur.Add(time.Hour)
	s.syncOnce(context.Background())
	if src.fetchCount() != 1 {
		t.Errorf("store failure re-ran the billed query after 1h: fetches = %d", src.fetchCount())
	}
}

// Permission, quota or billing errors on the completeness check must be
// reported as BigQuery errors, not as "not exported yet".
func TestStablecoinBlockCheckErrorIsReported(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	src.checkErr = errors.New("googleapi: Error 403: Access Denied")
	s.syncOnce(context.Background())
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.LastError != "bigquery_error" || src.fetchCount() != 0 {
		t.Errorf("last_error %q fetches %d, want bigquery_error and 0", st.LastError, src.fetchCount())
	}
	if _, ok, err := safeEnd(context.Background(), src, at(6, 0)); ok || err == nil {
		t.Errorf("safeEnd = ok %v err %v, want the check error", ok, err)
	}
}

// A hung BigQuery call must not block the scheduler goroutine (and with it
// the list sync) forever.
func TestStablecoinSyncTimesOut(t *testing.T) {
	orig := stablecoinSyncTimeout
	stablecoinSyncTimeout = 50 * time.Millisecond
	defer func() { stablecoinSyncTimeout = orig }()
	s, src, store := newTestStableSyncer(t, at(6, 0), 30)
	src.blockFetch = true
	done := make(chan struct{})
	go func() { s.syncOnce(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncOnce did not return after its timeout")
	}
	st, _ := store.getSyncState(context.Background(), stablecoinSourceID)
	if st.LastError != "timeout" {
		t.Errorf("last_error = %q, want timeout", st.LastError)
	}
}
