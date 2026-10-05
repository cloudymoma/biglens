package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	bqv2 "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/googleapi"
)

// fakeRLSAPI stands in for the BigQuery REST API: tables per dataset in API
// order, policies per "dataset.table", and injectable errors.
type fakeRLSAPI struct {
	tables    map[string][]rlsTable
	policies  map[string][]*bqv2.RowAccessPolicy
	tablesErr map[string]error
	policyErr map[string]error
	blockOn   string        // listPolicies on this table waits for ctx to end
	blockDS   string        // listTables on this dataset waits for ctx to end
	delay     time.Duration // per listPolicies call

	mu          sync.Mutex
	listedDS    []string       // datasets passed to listTables
	visited     map[string]int // tables handed to visit, per dataset
	listed      []string       // tables passed to listPolicies
	inflight    int
	maxInflight int
}

func (f *fakeRLSAPI) listTables(ctx context.Context, dataset string, visit func(rlsTable) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.listedDS = append(f.listedDS, dataset)
	f.mu.Unlock()
	if dataset == f.blockDS {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := f.tablesErr[dataset]; err != nil {
		return err
	}
	for _, t := range f.tables[dataset] {
		f.mu.Lock()
		if f.visited == nil {
			f.visited = map[string]int{}
		}
		f.visited[dataset]++
		f.mu.Unlock()
		if !visit(t) {
			return nil
		}
	}
	return nil
}

func (f *fakeRLSAPI) listPolicies(ctx context.Context, dataset, table string) ([]*bqv2.RowAccessPolicy, error) {
	key := dataset + "." + table
	f.mu.Lock()
	f.listed = append(f.listed, key)
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if key == f.blockOn {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.policyErr[key]; err != nil {
		return nil, err
	}
	return f.policies[key], nil
}

func (f *fakeRLSAPI) listedTables() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.listed...)
}

func tbl(dataset, table, typ string) rlsTable {
	return rlsTable{Dataset: dataset, Table: table, Type: typ}
}

func policy(id, predicate, modified string) *bqv2.RowAccessPolicy {
	return &bqv2.RowAccessPolicy{
		RowAccessPolicyReference: &bqv2.RowAccessPolicyReference{PolicyId: id},
		FilterPredicate:          predicate,
		LastModifiedTime:         modified,
	}
}

var testRLSLimits = rlsLimits{Concurrency: 4, MaxTables: 100, Timeout: 5 * time.Second}

func errAccessDenied() error {
	return &googleapi.Error{Code: 403, Message: "Access Denied: Table p:sales.orders: Permission bigquery.rowAccessPolicies.list denied"}
}

func TestRLSCapableType(t *testing.T) {
	for typ, want := range map[string]bool{
		"TABLE":             true,
		"SNAPSHOT":          true,
		"EXTERNAL":          true,
		"VIEW":              false,
		"MATERIALIZED_VIEW": false,
		"":                  false,
	} {
		if got := rlsCapableType(typ); got != want {
			t.Errorf("rlsCapableType(%q) = %v, want %v", typ, got, want)
		}
	}
}

// Views and materialized views are filtered by their base tables' policies,
// so only tables, snapshots and external (BigLake) tables are asked.
func TestScanRowAccessPoliciesListsOnlyRLSCapableTables(t *testing.T) {
	api := &fakeRLSAPI{tables: map[string][]rlsTable{"sales": {
		tbl("sales", "orders", "TABLE"),
		tbl("sales", "orders_v", "VIEW"),
		tbl("sales", "orders_mv", "MATERIALIZED_VIEW"),
		tbl("sales", "orders_snap", "SNAPSHOT"),
		tbl("sales", "lake", "EXTERNAL"),
	}}}

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"sales"}, testRLSLimits)

	want := []string{"sales.lake", "sales.orders", "sales.orders_snap"}
	got := api.listedTables()
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listPolicies called for %v, want %v", got, want)
	}
	if scan.Status != rlsComplete || scan.TablesTotal != 3 || scan.TablesChecked != 3 {
		t.Errorf("scan = %+v, want complete with 3 of 3 tables checked", scan)
	}
	if scan.failed() {
		t.Error("a scan without errors must not be flagged as failed")
	}
}

func TestScanRowAccessPoliciesAssemblesPolicies(t *testing.T) {
	api := &fakeRLSAPI{
		tables: map[string][]rlsTable{
			"sales": {tbl("sales", "orders", "TABLE"), tbl("sales", "accounts", "TABLE")},
			"hr":    {tbl("hr", "salaries", "SNAPSHOT")},
		},
		policies: map[string][]*bqv2.RowAccessPolicy{
			"sales.orders": {
				policy("us_only", `region = "US"`, "2024-05-01T10:20:30.123Z"),
				policy("apac_only", `region = "<APAC>"`, "1714558830123"),
			},
			"hr.salaries": {policy("self", "email = SESSION_USER()", "")},
		},
	}

	policies, scan := scanRowAccessPolicies(context.Background(), api, []string{"hr", "sales"}, testRLSLimits)

	want := []RLSPolicy{
		{Dataset: "hr", Table: "salaries", Policy: "self", Predicate: "email = SESSION_USER()", Modified: ""},
		{Dataset: "sales", Table: "orders", Policy: "apac_only", Predicate: `region = "<APAC>"`, Modified: "2024-05-01T10:20:30Z"},
		{Dataset: "sales", Table: "orders", Policy: "us_only", Predicate: `region = "US"`, Modified: "2024-05-01T10:20:30Z"},
	}
	if !reflect.DeepEqual(policies, want) {
		t.Errorf("policies =\n%+v\nwant\n%+v", policies, want)
	}
	if scan.Status != rlsComplete || scan.TablesChecked != 3 || scan.TablesTotal != 3 || scan.DatasetsFailed != 0 {
		t.Errorf("scan = %+v, want complete with 3 of 3 tables", scan)
	}
}

func TestRLSTimestamp(t *testing.T) {
	for in, want := range map[string]string{
		"2024-05-01T10:20:30.123456Z":   "2024-05-01T10:20:30Z",
		"2024-05-01T18:20:30+08:00":     "2024-05-01T10:20:30Z",
		"1714558830123":                 "2024-05-01T10:20:30Z",
		"":                              "",
		"not a timestamp":               "",
		"2024-05-01 10:20:30 +0000 UTC": "",
	} {
		if got := rlsTimestamp(in); got != want {
			t.Errorf("rlsTimestamp(%q) = %q, want %q", in, got, want)
		}
	}
}

// Past the table cap the scan stops and says so instead of silently
// dropping tables; the cap is applied in dataset order, so it is stable.
func TestScanRowAccessPoliciesCapsTablesAndMarksTruncated(t *testing.T) {
	api := &fakeRLSAPI{tables: map[string][]rlsTable{
		"a": {tbl("a", "t1", "TABLE"), tbl("a", "v1", "VIEW"), tbl("a", "t2", "TABLE"), tbl("a", "t3", "TABLE")},
		"b": {tbl("b", "t1", "TABLE"), tbl("b", "t2", "TABLE")},
	}}
	lim := testRLSLimits
	lim.MaxTables = 4

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"a", "b"}, lim)

	got := api.listedTables()
	sort.Strings(got)
	if want := []string{"a.t1", "a.t2", "a.t3", "b.t1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listPolicies called for %v, want the first 4 tables in dataset order %v", got, want)
	}
	if !scan.Truncated || scan.Status != rlsPartial || scan.TablesTotal != 4 || scan.TablesChecked != 4 || scan.MaxTables != 4 {
		t.Errorf("scan = %+v, want partial+truncated with 4 of 4 tables checked", scan)
	}
	if scan.failed() {
		t.Error("hitting the cap is a coverage limit, not a failure")
	}
}

// A dataset with thousands of (sharded) tables must not be paged through in
// full once it alone exceeds the cap.
func TestScanRowAccessPoliciesStopsListingPastTheCap(t *testing.T) {
	var shards []rlsTable
	for i := range 50 {
		shards = append(shards, tbl("events", fmt.Sprintf("events_%03d", i), "TABLE"))
	}
	api := &fakeRLSAPI{tables: map[string][]rlsTable{"events": shards}}
	lim := testRLSLimits
	lim.MaxTables = 5

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"events"}, lim)

	if n := api.visited["events"]; n > lim.MaxTables+1 {
		t.Errorf("listed %d tables of a 50-table dataset, want at most %d", n, lim.MaxTables+1)
	}
	if !scan.Truncated || scan.TablesTotal != 5 {
		t.Errorf("scan = %+v, want truncated at 5 tables", scan)
	}
}

func TestScanRowAccessPoliciesBoundsConcurrency(t *testing.T) {
	var tables []rlsTable
	for i := range 30 {
		tables = append(tables, tbl("big", fmt.Sprintf("t%02d", i), "TABLE"))
	}
	api := &fakeRLSAPI{tables: map[string][]rlsTable{"big": tables}, delay: 5 * time.Millisecond}
	lim := testRLSLimits
	lim.Concurrency = 3

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"big"}, lim)

	if api.maxInflight > 3 {
		t.Errorf("%d listPolicies calls in flight, want at most 3", api.maxInflight)
	}
	if api.maxInflight < 2 {
		t.Errorf("max %d call in flight: tables are not listed concurrently", api.maxInflight)
	}
	if scan.TablesChecked != 30 || scan.Status != rlsComplete {
		t.Errorf("scan = %+v, want all 30 tables checked", scan)
	}
}

// One unreadable table makes the result partial: the policies that were read
// are kept, but the scan is flagged so the UI can't claim full coverage.
func TestScanRowAccessPoliciesTableErrorMarksPartial(t *testing.T) {
	api := &fakeRLSAPI{
		tables: map[string][]rlsTable{"sales": {
			tbl("sales", "accounts", "TABLE"), tbl("sales", "orders", "TABLE"), tbl("sales", "refunds", "TABLE"),
		}},
		policies:  map[string][]*bqv2.RowAccessPolicy{"sales.accounts": {policy("p", "TRUE", "")}},
		policyErr: map[string]error{"sales.orders": errAccessDenied()},
	}

	policies, scan := scanRowAccessPolicies(context.Background(), api, []string{"sales"}, testRLSLimits)

	if scan.Status != rlsPartial || scan.TablesChecked != 2 || scan.TablesTotal != 3 {
		t.Errorf("scan = %+v, want partial with 2 of 3 tables checked", scan)
	}
	if !scan.failed() {
		t.Error("a table that could not be read must flag the scan as failed")
	}
	if len(policies) != 1 || policies[0].Table != "accounts" {
		t.Errorf("policies = %+v, want the one policy read from sales.accounts", policies)
	}
}

// Without bigquery.rowAccessPolicies.list every call fails: that is "not
// evaluated", never "no policies".
func TestScanRowAccessPoliciesAllTablesFailedIsNotEvaluated(t *testing.T) {
	api := &fakeRLSAPI{
		tables: map[string][]rlsTable{"sales": {tbl("sales", "orders", "TABLE"), tbl("sales", "refunds", "TABLE")}},
		policyErr: map[string]error{
			"sales.orders":  errAccessDenied(),
			"sales.refunds": errAccessDenied(),
		},
	}

	policies, scan := scanRowAccessPolicies(context.Background(), api, []string{"sales"}, testRLSLimits)

	if scan.Status != rlsNotEvaluated || scan.TablesChecked != 0 || !scan.failed() {
		t.Errorf("scan = %+v, want not_evaluated and failed", scan)
	}
	if len(policies) != 0 {
		t.Errorf("policies = %+v, want none", policies)
	}
}

func TestScanRowAccessPoliciesDatasetListErrorIsCounted(t *testing.T) {
	denied := &googleapi.Error{Code: 403, Message: "Permission bigquery.tables.list denied"}
	api := &fakeRLSAPI{
		tables:    map[string][]rlsTable{"ok": {tbl("ok", "t", "TABLE")}},
		tablesErr: map[string]error{"locked": denied},
	}

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"locked", "ok"}, testRLSLimits)
	if scan.Status != rlsPartial || scan.DatasetsFailed != 1 || scan.TablesChecked != 1 || !scan.failed() {
		t.Errorf("scan = %+v, want partial with 1 failed dataset", scan)
	}

	_, scan = scanRowAccessPolicies(context.Background(), api, []string{"locked"}, testRLSLimits)
	if scan.Status != rlsNotEvaluated || scan.DatasetsFailed != 1 || !scan.failed() {
		t.Errorf("scan = %+v, want not_evaluated when the only dataset can't be listed", scan)
	}
}

// Dataset names go into the request path, so they keep the datasetNameRe
// check; a rejected name counts as a dataset that was not evaluated.
func TestScanRowAccessPoliciesRejectsInvalidDatasetNames(t *testing.T) {
	api := &fakeRLSAPI{tables: map[string][]rlsTable{"ok": {tbl("ok", "t", "TABLE")}}}

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"ok", "../other/datasets/x"}, testRLSLimits)

	if !reflect.DeepEqual(api.listedDS, []string{"ok"}) {
		t.Errorf("listTables called for %v, want only the valid dataset", api.listedDS)
	}
	if scan.DatasetsFailed != 1 || scan.Status != rlsPartial {
		t.Errorf("scan = %+v, want the invalid dataset counted as failed", scan)
	}
}

func TestScanRowAccessPoliciesTimeoutMarksPartial(t *testing.T) {
	api := &fakeRLSAPI{
		tables:  map[string][]rlsTable{"sales": {tbl("sales", "a_fast", "TABLE"), tbl("sales", "b_slow", "TABLE")}},
		blockOn: "sales.b_slow",
	}
	lim := testRLSLimits
	lim.Concurrency = 1
	lim.Timeout = 50 * time.Millisecond

	start := time.Now()
	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"sales"}, lim)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("scan took %v, want it to stop at the 50ms timeout", elapsed)
	}
	if !scan.TimedOut || scan.Status != rlsPartial || scan.TablesChecked != 1 || scan.TablesTotal != 2 || !scan.failed() {
		t.Errorf("scan = %+v, want partial+timed_out with 1 of 2 tables checked", scan)
	}
}

// Datasets the scan never got to before its timeout are not evaluated; they
// must not pass for datasets without tables.
func TestScanRowAccessPoliciesTimeoutWhileListingTables(t *testing.T) {
	api := &fakeRLSAPI{
		tables:  map[string][]rlsTable{"b": {tbl("b", "t", "TABLE")}},
		blockDS: "a",
	}
	lim := testRLSLimits
	lim.Concurrency = 1
	lim.Timeout = 50 * time.Millisecond

	_, scan := scanRowAccessPolicies(context.Background(), api, []string{"a", "b"}, lim)

	if scan.DatasetsFailed != 2 || scan.Status != rlsNotEvaluated || !scan.TimedOut || !scan.failed() {
		t.Errorf("scan = %+v, want both datasets failed, not_evaluated, timed_out", scan)
	}
}

// A dataset holding only views has nothing that can carry a policy, so an
// empty result there is a complete answer.
func TestScanRowAccessPoliciesNoCapableTablesIsComplete(t *testing.T) {
	api := &fakeRLSAPI{tables: map[string][]rlsTable{"reports": {tbl("reports", "daily", "VIEW")}}}

	policies, scan := scanRowAccessPolicies(context.Background(), api, []string{"reports"}, testRLSLimits)

	if scan.Status != rlsComplete || scan.TablesTotal != 0 || scan.failed() || len(policies) != 0 {
		t.Errorf("scan = %+v policies = %v, want complete with nothing to check", scan, policies)
	}
}

func newRLSTestHandler(api rlsAPI) *APIHandler {
	return &APIHandler{bq: &BQClient{config: &Config{}, rls: api}, cache: NewCache(time.Minute)}
}

func TestFillRLSFlagsDegradedWhenPoliciesUnreadable(t *testing.T) {
	api := &fakeRLSAPI{
		tables:    map[string][]rlsTable{"sales": {tbl("sales", "orders", "TABLE")}},
		policyErr: map[string]error{"sales.orders": errAccessDenied()},
	}
	h := newRLSTestHandler(api)
	var data SecurityDashboardData
	var degraded []string

	h.fillRLS(context.Background(), "us", []string{"sales"}, &data, func(w string) { degraded = append(degraded, w) })

	if data.RLSScan.Status != rlsNotEvaluated {
		t.Errorf("rls_scan = %+v, want not_evaluated", data.RLSScan)
	}
	if !reflect.DeepEqual(degraded, []string{"row_level_security"}) {
		t.Errorf("degraded widgets = %v, want [row_level_security]", degraded)
	}
}

func TestFillRLSCompleteScanIsNotDegraded(t *testing.T) {
	api := &fakeRLSAPI{
		tables:   map[string][]rlsTable{"sales": {tbl("sales", "orders", "TABLE")}},
		policies: map[string][]*bqv2.RowAccessPolicy{"sales.orders": {policy("us_only", `region = "US"`, "")}},
	}
	h := newRLSTestHandler(api)
	var data SecurityDashboardData
	var degraded []string

	h.fillRLS(context.Background(), "us", []string{"sales"}, &data, func(w string) { degraded = append(degraded, w) })

	if data.RLSScan.Status != rlsComplete || len(data.RLSPolicies) != 1 || len(degraded) != 0 {
		t.Errorf("rls_scan = %+v policies = %v degraded = %v, want complete, 1 policy, not degraded",
			data.RLSScan, data.RLSPolicies, degraded)
	}
}

func cachedFor(t *testing.T, c *Cache, key string) (time.Duration, bool) {
	t.Helper()
	v, ok := c.store.Load(key)
	if !ok {
		return 0, false
	}
	return time.Until(v.(*cacheEntry).expiresAt), true
}

// Complete scans are reused for an hour, incomplete ones are retried after
// ten minutes, and scans cut short by the caller are not remembered.
func TestRowAccessPoliciesCaching(t *testing.T) {
	datasets := []string{"sales"}
	key := rlsCacheKey("us", datasets)

	ok := &fakeRLSAPI{tables: map[string][]rlsTable{"sales": {tbl("sales", "orders", "TABLE")}}}
	h := newRLSTestHandler(ok)
	h.rowAccessPolicies(context.Background(), "us", datasets)
	if ttl, hit := cachedFor(t, h.cache, key); !hit || ttl < 59*time.Minute || ttl > time.Hour {
		t.Errorf("complete scan cached for %v (hit=%v), want 1h", ttl, hit)
	}
	h.rowAccessPolicies(context.Background(), "us", datasets)
	if n := len(ok.listedTables()); n != 1 {
		t.Errorf("listPolicies called %d times over two loads, want 1 (second load from cache)", n)
	}

	failing := &fakeRLSAPI{
		tables:    map[string][]rlsTable{"sales": {tbl("sales", "orders", "TABLE")}},
		policyErr: map[string]error{"sales.orders": errAccessDenied()},
	}
	h = newRLSTestHandler(failing)
	h.rowAccessPolicies(context.Background(), "us", datasets)
	if ttl, hit := cachedFor(t, h.cache, key); !hit || ttl < 9*time.Minute || ttl > 10*time.Minute {
		t.Errorf("failed scan cached for %v (hit=%v), want 10m", ttl, hit)
	}

	h = newRLSTestHandler(ok)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := h.rowAccessPolicies(ctx, "us", datasets)
	if _, hit := cachedFor(t, h.cache, key); hit {
		t.Error("a scan cut short by a canceled request must not be cached")
	}
	if res.Scan.Status == rlsComplete {
		t.Errorf("canceled scan reported %q, want it incomplete", res.Scan.Status)
	}
}

func TestRLSCacheKeyDependsOnRegionAndDatasets(t *testing.T) {
	a := rlsCacheKey("us", []string{"a", "b"})
	if a == rlsCacheKey("eu", []string{"a", "b"}) || a == rlsCacheKey("us", []string{"a"}) {
		t.Error("cache key must change with the region and the dataset list")
	}
}
