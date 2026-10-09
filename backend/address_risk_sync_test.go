package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpstreamErrCode(t *testing.T) {
	timeoutErr := &url.Error{Op: "Get", URL: "https://api.etherscan.io/v2/api?apikey=SECRET", Err: &net.DNSError{IsTimeout: true}}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"wrapped deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), "timeout"},
		{"url.Error timeout", timeoutErr, "timeout"},
		{"other network error", &url.Error{Op: "Get", URL: "https://x?apikey=SECRET", Err: errors.New("connection refused")}, "network_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamErrCode(tt.err)
			if got != tt.want {
				t.Errorf("upstreamErrCode = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "SECRET") || strings.Contains(got, "http") {
				t.Errorf("error code leaks URL: %q", got)
			}
		})
	}
}

func TestReadCapped(t *testing.T) {
	if b, err := readCapped(strings.NewReader("hello"), 5); err != nil || string(b) != "hello" {
		t.Errorf("at limit: %q %v", b, err)
	}
	if _, err := readCapped(strings.NewReader("hello!"), 5); err == nil {
		t.Error("over limit must fail — a truncated OFAC txt still parses partially")
	}
	if _, err := readCapped(errReader{}, 5); err == nil {
		t.Error("read error must propagate")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// fakeFiles is the upstream content; guarded because the httptest server
// reads it from another goroutine while tests change it between syncs.
type fakeFiles struct {
	mu sync.Mutex
	m  map[string]string
}

func (f *fakeFiles) set(path, body string) { f.mu.Lock(); f.m[path] = body; f.mu.Unlock() }

func (f *fakeFiles) get(path string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.m[path]
	return b, ok
}

// testSyncer serves each file path from files; a missing path returns 500.
func testSyncer(t *testing.T, files *fakeFiles) (*riskListSyncer, *riskStore, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files.get(r.URL.Path)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	store := newTestRiskStore(t)
	now := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	s := newRiskListSyncer(store)
	s.now = func() time.Time { return now }
	s.sources = []riskListSource{
		{ID: "ofac", Parse: parseOFACBody, Files: []riskListFile{
			{URL: srv.URL + "/eth.txt"}, {URL: srv.URL + "/usdt.txt", Label: "tagged USDT"},
		}},
		{ID: "mew_darklist", Parse: parseMEWBody, Files: []riskListFile{{URL: srv.URL + "/mew.json"}}},
	}
	return s, store, &now
}

func files(m map[string]string) *fakeFiles { return &fakeFiles{m: m} }

// addrN returns a distinct valid lowercase address for i >= 0.
func addrN(i int) string { return fmt.Sprintf("0x%040x", i+1) }

func TestSyncSourceHappyPathAndDedupeAcrossFiles(t *testing.T) {
	eth := "0x098B716B8Aaf21512996dC57EB0615e2383E2f96\n0x1111111111111111111111111111111111111111\n"
	usdt := "0x1111111111111111111111111111111111111111\nTXYZtron\n" // same EVM addr in both files
	s, store, _ := testSyncer(t, files(map[string]string{"/eth.txt": eth, "/usdt.txt": usdt}))
	if err := s.syncSource(context.Background(), s.sources[0]); err != nil {
		t.Fatal(err)
	}
	st, _ := store.getSyncState(context.Background(), "ofac")
	if st.RowCount != 2 || st.LastError != "" {
		t.Fatalf("state = %+v, want 2 rows (deduped across files)", st)
	}
	hits, _ := store.listHits(context.Background(), "0x1111111111111111111111111111111111111111")
	if len(hits) != 1 || hits[0].Label != "tagged USDT" {
		t.Errorf("merged label = %+v", hits)
	}
}

func TestSyncSourceAnyFileFailureKeepsOldRows(t *testing.T) {
	s, store, _ := testSyncer(t, files(map[string]string{"/eth.txt": "0x098b716b8aaf21512996dc57eb0615e2383e2f96\n", "/usdt.txt": ""}))
	ctx := context.Background()
	if err := s.syncSource(ctx, s.sources[0]); err != nil {
		t.Fatal(err)
	}
	s.sources[0].Files[1].URL += "-gone" // now 500s
	if err := s.syncSource(ctx, s.sources[0]); err == nil {
		t.Fatal("expected error when one OFAC file fails")
	}
	st, _ := store.getSyncState(ctx, "ofac")
	if st.RowCount != 1 || st.LastError != "upstream_http_500" {
		t.Errorf("state = %+v", st)
	}
	if hits, _ := store.listHits(ctx, "0x098b716b8aaf21512996dc57eb0615e2383e2f96"); len(hits) != 1 {
		t.Error("old rows lost after failed sync")
	}
}

func TestSyncSourceShrinkGuardThenAccept(t *testing.T) {
	ff := files(map[string]string{})
	s, store, now := testSyncer(t, ff)
	ctx := context.Background()
	big := "["
	for i := 0; i < 40; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"address":"` + addrN(i) + `","comment":"c","date":""}`
	}
	ff.set("/mew.json", big+"]")
	if err := s.syncSource(ctx, s.sources[1]); err != nil {
		t.Fatal(err)
	}
	ff.set("/mew.json", `[{"address":"`+addrN(0)+`","comment":"c","date":""}]`)
	*now = now.Add(6 * time.Hour)
	if err := s.syncSource(ctx, s.sources[1]); err == nil {
		t.Fatal("first big shrink must be rejected")
	}
	st, _ := store.getSyncState(ctx, "mew_darklist")
	if st.RowCount != 40 || st.PendingCount != 1 || st.LastOKAt != "2026-09-26T06:00:00Z" {
		t.Fatalf("after reject: %+v (last_ok_at must not move)", st)
	}
	*now = now.Add(6 * time.Hour)
	if err := s.syncSource(ctx, s.sources[1]); err != nil {
		t.Fatalf("identical shrink seen twice must be accepted: %v", err)
	}
	st, _ = store.getSyncState(ctx, "mew_darklist")
	if st.RowCount != 1 || st.PendingCount != 0 {
		t.Errorf("after accept: %+v", st)
	}
}

func TestSyncDueOnlySyncsStaleSources(t *testing.T) {
	s, store, now := testSyncer(t, files(map[string]string{
		"/eth.txt": "0x098b716b8aaf21512996dc57eb0615e2383e2f96\n", "/usdt.txt": "", "/mew.json": `[]`,
	}))
	ctx := context.Background()
	s.syncDue(ctx) // ofac ok; mew rejected as empty
	first, _ := store.getSyncState(ctx, "ofac")
	*now = now.Add(time.Hour)
	s.syncDue(ctx)
	again, _ := store.getSyncState(ctx, "ofac")
	if again.LastOKAt != first.LastOKAt {
		t.Error("ofac re-synced before 6h")
	}
	*now = now.Add(6 * time.Hour)
	s.syncDue(ctx)
	later, _ := store.getSyncState(ctx, "ofac")
	if later.LastOKAt == first.LastOKAt {
		t.Error("ofac not re-synced after 6h")
	}
	mew, _ := store.getSyncState(ctx, "mew_darklist")
	if mew.LastError != "upstream list empty" {
		t.Errorf("mew state = %+v", mew)
	}
}

func TestRunAddressRiskSyncRecoversFromPanic(t *testing.T) {
	s, store, _ := testSyncer(t, files(map[string]string{}))
	calls := 0
	s.sources = []riskListSource{{ID: "ofac", Files: []riskListFile{{URL: "http://unused"}},
		Parse: func([]byte, string) ([]listEntry, int, error) { calls++; panic("parser bug") }}}
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	runAddressRiskSync(ctx, 40*time.Millisecond, s.syncDue) // returns when ctx is done
	if calls < 2 {
		t.Errorf("parser called %d times; a panic must not stop later ticks", calls)
	}
	st, _ := store.getSyncState(context.Background(), "ofac")
	if !strings.HasPrefix(st.LastError, "panic: ") {
		t.Errorf("last_error = %q, want panic recorded", st.LastError)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The overview caches list counts; only a real replace should invalidate it.
func TestSyncSourceNotifiesOnlyOnReplace(t *testing.T) {
	s, _, _ := testSyncer(t, files(map[string]string{"/eth.txt": "0x098b716b8aaf21512996dc57eb0615e2383e2f96\n", "/usdt.txt": ""}))
	notified := 0
	s.onChange = func() { notified++ }
	ctx := context.Background()
	for i := 0; i < 2; i++ { // second run: same content → markListUnchanged
		if err := s.syncSource(ctx, s.sources[0]); err != nil {
			t.Fatal(err)
		}
	}
	if notified != 1 {
		t.Errorf("onChange called %d times, want 1", notified)
	}
}

func TestOFACSourceFiles(t *testing.T) {
	var ofac *riskListSource
	for i := range riskListSources {
		if riskListSources[i].ID == "ofac" {
			ofac = &riskListSources[i]
			break
		}
	}
	if ofac == nil {
		t.Fatal("missing ofac source")
	}
	if len(ofac.Files) != 6 {
		t.Fatalf("len(ofac.Files) = %d, want 6: %+v", len(ofac.Files), ofac.Files)
	}
	const prefix = "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_"
	wantLabels := map[string]string{
		prefix + "ETH.txt":  "",
		prefix + "TRX.txt":  "tagged TRX",
		prefix + "XBT.txt":  "tagged XBT",
		prefix + "USDT.txt": "tagged USDT",
		prefix + "USDC.txt": "tagged USDC",
		prefix + "SOL.txt":  "tagged SOL",
	}
	for _, f := range ofac.Files {
		wantLabel, ok := wantLabels[f.URL]
		if !ok {
			t.Errorf("unexpected OFAC file URL %q", f.URL)
			continue
		}
		if f.Label != wantLabel {
			t.Errorf("file %q label = %q, want %q", f.URL, f.Label, wantLabel)
		}
		delete(wantLabels, f.URL)
	}
	if len(wantLabels) != 0 {
		t.Errorf("missing OFAC files: %v", wantLabels)
	}
}
