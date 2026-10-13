package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	bqTarget     = "0x1111111111111111111111111111111111111111"
	bqFunder     = "0x5555555555555555555555555555555555555555"
	bqPeerMEW    = "0x2222222222222222222222222222222222222222"
	bqPeerLook   = "0x123400000000000000000000000000000000abcd"
	bqPeerPlain  = "0x3333333333333333333333333333333333333333"
	bqPeerDeploy = "0x4444444444444444444444444444444444444444" // deployer corpus, only ever paid by the target
	bqPeerDepEOA = "0x6666666666666666666666666666666666666666" // deployer corpus, sent the target transactions
)

// fakeBQQuerier stands in for BigQuery: it records every profile job and
// answers with row/billed/err after delay (or when ctx ends).
type fakeBQQuerier struct {
	mu     sync.Mutex
	addrs  []string
	maxes  []int64
	row    *bqProfileRow
	billed int64
	err    error
	delay  time.Duration
}

func (f *fakeBQQuerier) runProfile(ctx context.Context, addr string, maxBytes int64) (*bqProfileRow, int64, error) {
	f.mu.Lock()
	f.addrs = append(f.addrs, addr)
	f.maxes = append(f.maxes, maxBytes)
	row, billed, err, delay := f.row, f.billed, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	return row, billed, err
}

func (f *fakeBQQuerier) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.addrs)
}

func testBQRow() *bqProfileRow {
	return &bqProfileRow{
		OutTxs: 12, OutFirst: "2019-03-02T00:00:00Z", OutLast: "2026-10-01T12:00:00Z", OutPeers: 3, OutWei: "1500000000000000000",
		InTxs: 30, InFirst: "2019-03-01T08:00:00Z", InLast: "2026-09-20T12:00:00Z", InPeers: 4, InWei: "2750000000000000000",
		Peers:       6,
		FirstFunder: []bqProfileFunderRow{{Address: bqFunder, BlockNumber: 7300000, TS: "2019-03-01T08:00:00Z", Wei: "250000000000000000"}},
		TopPeers: []bqProfilePeerRow{
			{Address: bqPeerPlain, Sent: 10, Received: 5, FirstTS: "2019-03-02T00:00:00Z", LastTS: "2026-10-01T12:00:00Z", WeiSent: "1000000000000000000", WeiReceived: "0"},
			{Address: bqPeerMEW, Sent: 2, Received: 0, FirstTS: "2020-01-01T00:00:00Z", LastTS: "2020-02-01T00:00:00Z", WeiSent: "500000000000000000", WeiReceived: "0"},
			{Address: bqPeerLook, Sent: 0, Received: 3, FirstTS: "2026-09-01T00:00:00Z", LastTS: "2026-09-20T12:00:00Z", WeiSent: "0", WeiReceived: "1"},
			{Address: bqFunder, Sent: 0, Received: 1, FirstTS: "2019-03-01T08:00:00Z", LastTS: "2019-03-01T08:00:00Z", WeiSent: "0", WeiReceived: "250000000000000000"},
			{Address: bqPeerDeploy, Sent: 1, Received: 0, FirstTS: "2026-05-01T00:00:00Z", LastTS: "2026-05-01T00:00:00Z", WeiSent: "10000000000000000", WeiReceived: "0"},
			{Address: bqPeerDepEOA, Sent: 0, Received: 2, FirstTS: "2026-06-01T00:00:00Z", LastTS: "2026-06-02T00:00:00Z", WeiSent: "0", WeiReceived: "20000000000000000"},
		},
	}
}

func testBQSettings() bqProfileSettings {
	return bqProfileSettings{Enabled: true, MaxBytesBilled: 2 << 30, MaxPerHour: 60, Timeout: time.Second}
}

func TestBQProfileQueryShape(t *testing.T) {
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithoutAuthentication(), option.WithEndpoint("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	q := newBQProfileQuery(client, bqTarget, 2<<30)
	sql := strings.Join(strings.Fields(q.Q), " ")
	for _, want := range []string{
		"FROM `bigquery-public-data.goog_blockchain_ethereum_mainnet_us.transactions_by_from_address` WHERE from_address = @addr",
		"FROM `bigquery-public-data.goog_blockchain_ethereum_mainnet_us.transactions_by_to_address` WHERE to_address = @addr",
		"IF(value > 0,", // first funder: earliest incoming transaction that moved ETH
		"LIMIT 200",
		"CAST(IFNULL(SUM(wei_sent), 0) AS STRING)", // BIGNUMERIC wei leaves BigQuery as a string
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("profile SQL missing %q:\n%s", want, sql)
		}
	}
	// Bytes are the cost: only the needed columns, never SELECT * or the
	// (expensive) hash / input columns, and the address only as a parameter.
	for _, banned := range []string{"SELECT *", "transaction_hash", "input", "value_lossless", bqTarget} {
		if strings.Contains(sql, banned) {
			t.Errorf("profile SQL must not contain %q", banned)
		}
	}
	if strings.Count(sql, "transactions_by_from_address") != 1 || strings.Count(sql, "transactions_by_to_address") != 1 {
		t.Error("each view must be scanned exactly once (one job, no re-reads)")
	}
	if len(q.Parameters) != 1 || q.Parameters[0].Name != "addr" || q.Parameters[0].Value != bqTarget {
		t.Errorf("parameters = %+v, want only @addr", q.Parameters)
	}
	if q.MaxBytesBilled != 2<<30 {
		t.Errorf("MaxBytesBilled = %d", q.MaxBytesBilled)
	}
	if q.Labels["app"] != "biglens" || q.Labels["feature"] != "address_profile" || len(q.Labels) != 2 {
		t.Errorf("labels = %v", q.Labels)
	}
	if q.DryRun || q.UseLegacySQL {
		t.Error("profile must be a real Standard SQL job")
	}
}

func TestHourlyBucket(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	b := newHourlyBucket(2, t0)
	if !b.allow(t0) || !b.allow(t0) || b.allow(t0) {
		t.Fatal("a fresh bucket must allow exactly its hourly capacity")
	}
	if !b.allow(t0.Add(30*time.Minute)) || b.allow(t0.Add(30*time.Minute)) {
		t.Error("half an hour must refill half the capacity (one token)")
	}
	t2 := t0.Add(5 * time.Hour)
	if !b.allow(t2) || !b.allow(t2) || b.allow(t2) {
		t.Error("refill must stop at the capacity")
	}
}

func TestBQProfileErrStatus(t *testing.T) {
	over := "Query exceeded limit for bytes billed: 2147483648. 6853792024 or higher required."
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"job error reason", &bigquery.Error{Reason: "bytesBilledLimitExceeded", Message: over}, bqStatusOverBudget},
		{"wrapped job error", fmt.Errorf("profile: %w", &bigquery.Error{Reason: "bytesBilledLimitExceeded"}), bqStatusOverBudget},
		{"API error item", &googleapi.Error{Code: 400, Errors: []googleapi.ErrorItem{{Reason: "bytesBilledLimitExceeded"}}}, bqStatusOverBudget},
		{"message only", errors.New(over), bqStatusOverBudget},
		{"deadline", context.DeadlineExceeded, bqStatusTimeout},
		{"access denied", &googleapi.Error{Code: 403, Message: "Access Denied: Project p: User does not have bigquery.jobs.create"}, bqStatusError},
		{"other job error", &bigquery.Error{Reason: "invalidQuery", Message: "Syntax error"}, bqStatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bqProfileErrStatus(tt.err); got != tt.want {
				t.Errorf("bqProfileErrStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBQProfilerCachesAndSharesJobs(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow(), billed: 954204160, delay: 50 * time.Millisecond}
	p := newBQProfiler(testBQSettings(), f)
	var wg sync.WaitGroup
	runs := make([]bqProfileRun, 3)
	for i := range runs {
		wg.Add(1)
		go func() { defer wg.Done(); runs[i], _ = p.run(context.Background(), bqTarget) }()
	}
	wg.Wait()
	for _, r := range runs {
		if r.Status != bqStatusOK || r.Billed != 954204160 || r.Row == nil {
			t.Fatalf("run = %+v", r)
		}
	}
	if f.calls() != 1 {
		t.Fatalf("concurrent lookups ran %d jobs, want 1 (singleflight)", f.calls())
	}
	r, cached := p.run(context.Background(), bqTarget)
	if r.Status != bqStatusOK || !cached || f.calls() != 1 {
		t.Errorf("second lookup = %+v cached=%v, jobs=%d; want the cached result", r, cached, f.calls())
	}
	if f.maxes[0] != 2<<30 {
		t.Errorf("job cap = %d, want the configured max_bytes_billed", f.maxes[0])
	}
}

func TestBQProfilerRateLimit(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow()}
	s := testBQSettings()
	s.MaxPerHour = 1
	p := newBQProfiler(s, f)
	if r, _ := p.run(context.Background(), bqTarget); r.Status != bqStatusOK {
		t.Fatalf("first = %+v", r)
	}
	if r, _ := p.run(context.Background(), bqFunder); r.Status != bqStatusRateLimited {
		t.Errorf("second address = %+v, want rate_limited", r)
	}
	if f.calls() != 1 {
		t.Errorf("jobs = %d; a rate-limited lookup must not call BigQuery", f.calls())
	}
	// Cached results do not spend tokens.
	if r, cached := p.run(context.Background(), bqTarget); r.Status != bqStatusOK || !cached {
		t.Errorf("cached address while limited = %+v cached=%v", r, cached)
	}
}

func TestBQProfilerTimeoutKeepsJobForRetry(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow(), delay: 150 * time.Millisecond}
	s := testBQSettings()
	s.Timeout = 30 * time.Millisecond
	p := newBQProfiler(s, f)
	if r, _ := p.run(context.Background(), bqTarget); r.Status != bqStatusTimeout {
		t.Fatalf("slow job = %+v, want timeout", r)
	}
	time.Sleep(250 * time.Millisecond) // the job keeps running and lands in the cache
	r, cached := p.run(context.Background(), bqTarget)
	if r.Status != bqStatusOK || !cached || f.calls() != 1 {
		t.Errorf("retry = %+v cached=%v jobs=%d; want the finished job's result without re-billing", r, cached, f.calls())
	}
}

func TestBQProfilerOverBudgetCachedErrorsNot(t *testing.T) {
	f := &fakeBQQuerier{err: &bigquery.Error{Reason: "bytesBilledLimitExceeded", Message: "Query exceeded limit for bytes billed"}}
	p := newBQProfiler(testBQSettings(), f)
	if r, _ := p.run(context.Background(), bqTarget); r.Status != bqStatusOverBudget {
		t.Fatalf("over budget = %+v", r)
	}
	if r, cached := p.run(context.Background(), bqTarget); r.Status != bqStatusOverBudget || !cached || f.calls() != 1 {
		t.Errorf("over_budget is lasting and must be cached: %+v cached=%v jobs=%d", r, cached, f.calls())
	}

	g := &fakeBQQuerier{err: errors.New("googleapi: Error 403: Access Denied: secret-project-detail")}
	p2 := newBQProfiler(testBQSettings(), g)
	p2.run(context.Background(), bqTarget)
	if r, cached := p2.run(context.Background(), bqTarget); r.Status != bqStatusError || cached || g.calls() != 2 {
		t.Errorf("errors must not be cached: %+v cached=%v jobs=%d", r, cached, g.calls())
	}
}

func TestBQProfilerAvailability(t *testing.T) {
	var nilProfiler *bqProfiler
	if nilProfiler.available() {
		t.Error("nil profiler must be unavailable")
	}
	off := testBQSettings()
	off.Enabled = false
	if newBQProfiler(off, &fakeBQQuerier{}).available() {
		t.Error("enabled: false must be unavailable")
	}
	if newBQProfiler(testBQSettings(), nil).available() {
		t.Error("no BigQuery client must be unavailable")
	}
	if !newBQProfiler(testBQSettings(), &fakeBQQuerier{}).available() {
		t.Error("enabled with a client must be available")
	}
}

// bqTestService has a store with the first funder on OFAC, a counterparty on
// the MEW darklist, one in the lookalike corpus and three in the serial
// counterfeit-token deployer snapshot: bqPeerDepEOA (sent the target
// transactions), and bqPeerDeploy and bqPeerMEW (only ever paid by it).
func bqTestService(t *testing.T, f *fakeBQQuerier) *addressRiskService {
	t.Helper()
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: bqFunder}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: bqPeerMEW}}, "h", riskNow)
	deployer := func(addr string) fakeTokenDeployerRow {
		return fakeTokenDeployerRow{Address: addr, Contracts: 5, FirstSeen: "2026-05-01", LastSeen: "2026-09-01"}
	}
	// The Scam Radar day must land before markStablecoinSynced moves the cursor.
	if err := store.applyScamBatchWithDeployers(ctx, scamEthSourceID, "eth", []scamDayBatch{{Day: "2026-09-25",
		Lookalikes: []lookalikeRow{{Lookalike: bqPeerLook, Imitated: "0x123499999999999999999999999999999999abcd",
			Hits: 4, Victims: 2, FirstSeen: "2026-09-01", LastSeen: "2026-09-25"}}}},
		[]fakeTokenDeployerRow{deployer(bqPeerDeploy), deployer(bqPeerDepEOA), deployer(bqPeerMEW)},
		"2026-08-27", "2026-09-25", riskNow); err != nil {
		t.Fatal(err)
	}
	markStablecoinSynced(t, store)
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": ""}
	svc.bqProfile = newBQProfiler(testBQSettings(), f)
	return svc
}

func TestBQProfileResultAndClues(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow(), billed: 954204160}
	svc := bqTestService(t, f)
	src, clues, prof := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
	if src.ID != "bigquery_profile" || src.Status != bqStatusOK || src.SendsAddress || len(src.Hosts) != 0 {
		t.Errorf("source = %+v, want ok and sends_address=false (own GCP project, not a third party)", src)
	}
	if prof == nil || prof.Status != bqStatusOK || prof.BytesBilled != 954204160 || prof.Cached || !prof.ListsChecked {
		t.Fatalf("profile = %+v", prof)
	}
	if prof.Outgoing.Txs != 12 || prof.Incoming.Txs != 30 || prof.Outgoing.ETH != "1.5" || prof.Incoming.ETH != "2.75" ||
		prof.Counterparties != 6 || prof.FirstSeen != "2019-03-01T08:00:00Z" || prof.LastActive != "2026-10-01T12:00:00Z" {
		t.Errorf("summary = %+v out=%+v in=%+v", prof, prof.Outgoing, prof.Incoming)
	}
	if prof.AccountAgeDays != 2766 { // 2019-03-01T08:00 → 2026-09-26T12:00
		t.Errorf("account age = %d days", prof.AccountAgeDays)
	}
	ff := prof.FirstFunder
	if ff == nil || ff.Address != bqFunder || ff.BlockNumber != 7300000 || ff.ValueETH != "0.25" || strings.Join(ff.Lists, ",") != "ofac" {
		t.Errorf("first funder = %+v", ff)
	}
	if len(prof.TopPeers) != 6 || prof.PeersAnalyzed != 6 || prof.TopPeers[0].Address != bqPeerPlain {
		t.Errorf("top peers = %+v", prof.TopPeers)
	}
	var listed []string
	for _, p := range prof.ListedPeers {
		listed = append(listed, p.Address+"="+strings.Join(p.Lists, "+"))
	}
	// A deployer hit counts only for a peer that sent the target a top-level
	// transaction (an EOA): bqPeerDeploy and bqPeerMEW were only ever paid by
	// it and could be a token-launcher factory contract.
	if strings.Join(listed, ",") != bqPeerMEW+"=mew_darklist,"+bqPeerLook+"=scam_lookalikes,"+bqFunder+"=ofac,"+bqPeerDepEOA+"=fake_token_deployers" {
		t.Errorf("listed peers = %v", listed)
	}
	if p := prof.TopPeers[4]; p.Address != bqPeerDeploy || len(p.Lists) != 0 {
		t.Errorf("outgoing-only deployer-corpus peer = %+v, want no lists", p)
	}
	if prof.TopPeers[2].ETHReceived != "<0.000001" {
		t.Errorf("dust must not read as zero: %q", prof.TopPeers[2].ETHReceived)
	}
	// The UI maps over every lists array: never null in JSON.
	if js, _ := json.Marshal(prof); strings.Contains(string(js), `"lists":null`) || !strings.Contains(string(js), `"lists":[]`) {
		t.Errorf("unlisted peers must carry \"lists\":[]: %s", js)
	}

	byCode := map[string]riskClue{}
	for _, c := range clues {
		if c.Source != "bigquery_profile" {
			t.Errorf("clue from %q", c.Source)
		}
		byCode[c.Code] = c
	}
	fc := byCode["bq_first_funder_listed"]
	if fc.Severity != sevAssociation || fc.Title != "First funder "+bqFunder+" is OFAC-listed" ||
		fc.ObservedAt == nil || *fc.ObservedAt != "2019-03-01T08:00:00Z" || strings.Contains(fc.Detail, "factory") {
		t.Errorf("first funder clue = %+v (no deployer caveat without a deployer match)", fc)
	}
	cc := byCode["bq_counterparties_listed"]
	if cc.Severity != sevAssociation || cc.Title != "3 direct counterparties appear on local lists" ||
		!strings.Contains(cc.Detail, bqPeerMEW) || !strings.Contains(cc.Detail, "MEW darklist") ||
		!strings.Contains(cc.Detail, "address-poisoning lookalike") ||
		!strings.Contains(cc.Detail, bqPeerDepEOA+" (Serial counterfeit-token deployer;") ||
		strings.Contains(cc.Detail, bqPeerDeploy) || strings.Contains(cc.Detail, bqFunder) {
		t.Errorf("counterparties clue = %+v (the funder has its own clue)", cc)
	}
	if !strings.HasSuffix(cc.Detail, " "+bqDeployerCaveat) || !strings.Contains(bqDeployerCaveat, "token-launcher factory contract") {
		t.Errorf("counterparties clue detail = %q, want the factory caveat after a deployer match", cc.Detail)
	}
	// First seen, account age and activity are on the card; as clues they
	// were noise for every ordinary address (only risk-relevant Info stays).
	if len(clues) != 2 {
		t.Errorf("clues = %+v, want only the 2 association clues", clues)
	}
}

// Info clues stay only when they matter for risk: a never-used address (see
// TestBQProfileNoActivity) and an address whose first activity is < 30 days old.
func TestBQProfileNewAddressClue(t *testing.T) {
	tests := []struct {
		name  string
		age   time.Duration
		title string // "" = no new-address clue
	}{
		{"hours old", 2 * time.Hour, "New address: first activity less than a day ago"},
		{"six days", 6*24*time.Hour + time.Hour, "New address: first activity 6 days ago"},
		{"one day", 24*time.Hour + time.Hour, "New address: first activity 1 day ago"},
		{"29 days", 29*24*time.Hour + time.Hour, "New address: first activity 29 days ago"},
		{"30 days is not new", 30*24*time.Hour + time.Hour, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := riskNow.Add(-tt.age).UTC().Format(time.RFC3339)
			row := &bqProfileRow{InTxs: 1, InFirst: first, InLast: first, InPeers: 1, InWei: "50000000000000000", OutWei: "0", Peers: 1,
				FirstFunder: []bqProfileFunderRow{{Address: bqPeerPlain, BlockNumber: 23500000, TS: first, Wei: "50000000000000000"}},
				TopPeers:    []bqProfilePeerRow{{Address: bqPeerPlain, Received: 1, FirstTS: first, LastTS: first, WeiSent: "0", WeiReceived: "50000000000000000"}}}
			svc := bqTestService(t, &fakeBQQuerier{row: row})
			_, clues, _ := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
			if tt.title == "" {
				if len(clues) != 0 {
					t.Errorf("clues = %+v, want none for a 30-day-old address", clues)
				}
				return
			}
			if len(clues) != 1 || clues[0].Code != "bq_new_address" || clues[0].Severity != sevInfo || clues[0].Title != tt.title ||
				clues[0].ObservedAt == nil || *clues[0].ObservedAt != first {
				t.Fatalf("clues = %+v, want one info %q", clues, tt.title)
			}
			if !strings.Contains(clues[0].Detail, bqPeerPlain) || !strings.Contains(clues[0].Detail, "0.05 ETH") {
				t.Errorf("detail = %q, want the first funder and amount", clues[0].Detail)
			}
		})
	}
}

// riskPoolForAddresses also tags serial counterfeit-token deployers; their
// clue text must read as words, not as the internal source id.
func TestBQProfileDeployerLabels(t *testing.T) {
	if got := poolLabels([]string{"fake_token_deployers"}); len(got) != 1 || got[0] != "Serial counterfeit-token deployer" {
		t.Errorf("poolLabels = %v", got)
	}
	prof := &riskBQProfile{FirstSeen: "2019-03-01T08:00:00Z", AccountAgeDays: 2766,
		FirstFunder: &riskBQFunder{Address: bqPeerDeploy, BlockNumber: 1, Timestamp: "2019-03-01T08:00:00Z", ValueETH: "1", Lists: []string{"fake_token_deployers"}}}
	clues := bqProfileClues(prof, "2026-09-26T12:00:00Z")
	if len(clues) != 1 || clues[0].Title != "First funder "+bqPeerDeploy+" is a serial counterfeit-token deployer" {
		t.Fatalf("clues = %+v", clues)
	}
	if !strings.HasSuffix(clues[0].Detail, " "+bqDeployerCaveat) {
		t.Errorf("first funder clue detail = %q, want the factory caveat", clues[0].Detail)
	}
}

// The first funder sent the target ETH in a top-level transaction, so it is
// an EOA and its deployer match counts, even when it is not among the top
// counterparties.
func TestBQProfileDeployerFirstFunderCounts(t *testing.T) {
	row := testBQRow()
	row.FirstFunder = []bqProfileFunderRow{{Address: bqPeerDeploy, BlockNumber: 7300000, TS: "2019-03-01T08:00:00Z", Wei: "250000000000000000"}}
	row.TopPeers = row.TopPeers[:1] // bqPeerPlain only
	svc := bqTestService(t, &fakeBQQuerier{row: row})
	_, clues, prof := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
	if prof.FirstFunder == nil || strings.Join(prof.FirstFunder.Lists, ",") != "fake_token_deployers" {
		t.Fatalf("first funder = %+v, want the deployer match", prof.FirstFunder)
	}
	if len(clues) != 1 || clues[0].Code != "bq_first_funder_listed" ||
		clues[0].Title != "First funder "+bqPeerDeploy+" is a serial counterfeit-token deployer" {
		t.Errorf("clues = %+v", clues)
	}
}

func TestBQProfileNoActivity(t *testing.T) {
	f := &fakeBQQuerier{row: &bqProfileRow{OutWei: "0", InWei: "0"}, billed: 948376648}
	svc := bqTestService(t, f)
	_, clues, prof := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
	if prof.Status != bqStatusOK || prof.FirstFunder != nil || prof.FirstSeen != "" || len(prof.TopPeers) != 0 {
		t.Errorf("empty profile = %+v", prof)
	}
	if len(clues) != 1 || clues[0].Code != "bq_no_activity" || clues[0].Severity != sevInfo {
		t.Errorf("clues = %+v, want one info bq_no_activity", clues)
	}
}

func TestBQProfileFailuresAreGeneric(t *testing.T) {
	secret := "Access Denied: Table secret-project:private.table: user leak@example.com"
	for status, f := range map[string]*fakeBQQuerier{
		bqStatusError:      {err: errors.New(secret)},
		bqStatusOverBudget: {err: &bigquery.Error{Reason: "bytesBilledLimitExceeded", Message: "Query exceeded limit for bytes billed"}},
	} {
		t.Run(status, func(t *testing.T) {
			svc := bqTestService(t, f)
			src, clues, prof := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
			if src.Status != status || prof == nil || prof.Status != status || len(clues) != 0 {
				t.Fatalf("source %+v, profile %+v, clues %+v", src, prof, clues)
			}
			if strings.Contains(fmt.Sprintf("%+v %+v", src, *prof), "secret-project") || strings.Contains(prof.Message, "leak@") {
				t.Errorf("raw BigQuery error leaked: %+v / %+v", src, prof)
			}
			if prof.Message == "" {
				t.Error("non-ok profile needs a generic message")
			}
		})
	}
	svc := bqTestService(t, &fakeBQQuerier{row: testBQRow()})
	if src, _, _ := svc.checkBQProfile(context.Background(), "0xABC", riskNow); src.Status != bqStatusError {
		t.Errorf("invalid address source = %+v; the job must never run with an unvalidated parameter", src)
	}
}

func TestBQProfileWithoutStoreIsPartial(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow()}
	svc := bqTestService(t, f)
	svc.store = nil
	src, clues, prof := svc.checkBQProfile(context.Background(), bqTarget, riskNow)
	if src.Status != "partial" || prof.ListsChecked {
		t.Errorf("source = %+v lists_checked=%v; without local lists the profile is partial", src, prof.ListsChecked)
	}
	for _, c := range clues {
		if c.Severity == sevAssociation {
			t.Errorf("association clue without local lists: %+v", c)
		}
	}
}

func TestFormatWeiETH(t *testing.T) {
	for in, want := range map[string]string{
		"0":                         "0",
		"1":                         "<0.000001", // dust never reads as zero
		"250000000000000000":        "0.25",
		"1500000000000000000":       "1.5",
		"87564582537611094200799":   "87,564.582538",
		"1000000000000000000000000": "1,000,000",
		"not-a-number":              "",
	} {
		if got := formatWeiETH(in); got != want {
			t.Errorf("formatWeiETH(%q) = %q, want %q", in, got, want)
		}
	}
}
