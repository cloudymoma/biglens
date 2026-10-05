package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	bqv2 "google.golang.org/api/bigquery/v2"
)

// Row-level security has no INFORMATION_SCHEMA view: the documented ways to
// list row access policies are the console, `bq ls --row_access_policies
// dataset.table` and the RowAccessPolicies.List API, one table at a time
// (https://cloud.google.com/bigquery/docs/managing-row-level-security).
// BigLens enumerates tables with tables.list and calls RowAccessPolicies.List
// on each table that can carry policies. Both are free metadata reads; no
// query job is created.

type RLSPolicy struct {
	Dataset   string `json:"dataset"`
	Table     string `json:"table"`
	Policy    string `json:"policy"`
	Predicate string `json:"predicate"`
	Modified  string `json:"modified"` // last modified, RFC 3339 UTC; "" if unknown
}

const (
	rlsComplete     = "complete"      // every RLS-capable table was checked
	rlsPartial      = "partial"       // some tables checked; cap, errors or timeout left others out
	rlsNotEvaluated = "not_evaluated" // no table could be checked: no conclusion either way

	// rlsWidget is the degraded-widget name reported when policies could not
	// be read (permission, API error or timeout).
	rlsWidget = "row_level_security"
)

// RLSScan says how much of the project the policy list covers, so an empty
// list is never mistaken for "no policies" when they could not be read.
type RLSScan struct {
	Status         string `json:"status"`
	TablesChecked  int    `json:"tables_checked"`  // tables whose policies were read
	TablesTotal    int    `json:"tables_total"`    // RLS-capable tables selected for checking
	DatasetsFailed int    `json:"datasets_failed"` // datasets whose tables could not be listed
	Truncated      bool   `json:"truncated"`       // more RLS-capable tables exist past MaxTables
	TimedOut       bool   `json:"timed_out"`
	MaxTables      int    `json:"max_tables"`
}

// failed reports whether part of the scan could not be read. Hitting the
// table cap alone is a coverage limit, reported through Truncated instead.
func (s RLSScan) failed() bool {
	return s.DatasetsFailed > 0 || s.TablesChecked < s.TablesTotal
}

type rlsTable struct {
	Dataset, Table, Type string // Type as returned by tables.list
}

// rlsAPI is the part of the BigQuery REST API the scan uses; tests swap in a
// fake.
type rlsAPI interface {
	// listTables calls visit for each table of the dataset in API order and
	// stops paging as soon as visit returns false.
	listTables(ctx context.Context, dataset string, visit func(rlsTable) bool) error
	listPolicies(ctx context.Context, dataset, table string) ([]*bqv2.RowAccessPolicy, error)
}

type rlsLimits struct {
	Concurrency int           // API calls in flight
	MaxTables   int           // tables checked per scan
	Timeout     time.Duration // for the whole scan
}

// 8 calls in flight stay well under BigQuery's 100 API requests per second
// per user per method; 500 tables at that rate fit the 20s budget, which
// keeps the dashboard far from the server's 60s write timeout.
var defaultRLSLimits = rlsLimits{Concurrency: 8, MaxTables: 500, Timeout: 20 * time.Second}

// rlsCapableType reports whether a table of this tables.list type can carry
// row access policies, per the BigQuery docs:
//   - TABLE: standard tables; table clones are listed as TABLE too.
//   - SNAPSHOT: "Table snapshots support row-level security"
//     (using-row-level-security-with-features#table_snapshots).
//   - EXTERNAL: BigLake tables, object tables included, support row-level
//     security (biglake-intro, object-table-introduction). tables.list does
//     not say whether an external table is BigLake, so all of them are
//     asked; one without policies just returns an empty list.
//
// VIEW and MATERIALIZED_VIEW are skipped: rows read through either are
// filtered by the base table's policies
// (using-row-level-security-with-features#logical_materialized_and_authorized_views),
// and base tables in the scanned datasets are checked themselves.
func rlsCapableType(t string) bool {
	switch t {
	case "TABLE", "SNAPSHOT", "EXTERNAL":
		return true
	}
	return false
}

// scanRowAccessPolicies lists the row access policies of every RLS-capable
// table in datasets, at most lim.MaxTables tables in dataset order. The
// returned scan records what was left out (cap, errors, timeout); errors
// themselves are only logged.
func scanRowAccessPolicies(ctx context.Context, api rlsAPI, datasets []string, lim rlsLimits) ([]RLSPolicy, RLSScan) {
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	scan := RLSScan{MaxTables: lim.MaxTables}
	var (
		mu       sync.Mutex
		failures int
		firstErr error
	)
	fail := func(err error, args ...any) {
		slog.Debug("row access policy scan: call failed", append(args, "error", err)...)
		mu.Lock()
		failures++
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	// 1. Find RLS-capable tables. Listing a dataset stops once it alone
	// exceeds the cap, so date-sharded datasets aren't paged through.
	perDataset := make([][]rlsTable, len(datasets))
	listed := make([]bool, len(datasets))
	forEachLimited(ctx, len(datasets), lim.Concurrency, func(i int) {
		ds := datasets[i]
		if !datasetNameRe.MatchString(ds) {
			fail(errors.New("invalid dataset name"), "dataset", ds)
			return
		}
		err := api.listTables(ctx, ds, func(t rlsTable) bool {
			if rlsCapableType(t.Type) {
				perDataset[i] = append(perDataset[i], t)
			}
			return len(perDataset[i]) <= lim.MaxTables
		})
		if err != nil {
			fail(err, "dataset", ds)
			return
		}
		listed[i] = true
	})
	var tables []rlsTable
	for i := range datasets {
		if !listed[i] {
			// Rejected, failed part-way or never reached before the
			// timeout: its tables are not evaluated.
			scan.DatasetsFailed++
			continue
		}
		tables = append(tables, perDataset[i]...)
	}
	if len(tables) > lim.MaxTables {
		tables = tables[:lim.MaxTables]
		scan.Truncated = true
	}
	scan.TablesTotal = len(tables)

	// 2. List each table's policies.
	found := make([][]RLSPolicy, len(tables))
	read := make([]bool, len(tables))
	forEachLimited(ctx, len(tables), lim.Concurrency, func(i int) {
		t := tables[i]
		ps, err := api.listPolicies(ctx, t.Dataset, t.Table)
		if err != nil {
			fail(err, "dataset", t.Dataset, "table", t.Table)
			return
		}
		for _, p := range ps {
			if p != nil {
				found[i] = append(found[i], toRLSPolicy(t, p))
			}
		}
		read[i] = true
	})

	// 3. Assemble.
	var policies []RLSPolicy
	for i := range tables {
		if read[i] {
			scan.TablesChecked++
			policies = append(policies, found[i]...)
		}
	}
	sort.Slice(policies, func(i, j int) bool {
		a, b := policies[i], policies[j]
		if a.Dataset != b.Dataset {
			return a.Dataset < b.Dataset
		}
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		return a.Policy < b.Policy
	})

	switch {
	case !scan.failed() && !scan.Truncated:
		scan.Status = rlsComplete
	case scan.TablesChecked > 0:
		scan.Status = rlsPartial
	default:
		scan.Status = rlsNotEvaluated
	}
	if scan.failed() {
		scan.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		if firstErr == nil {
			firstErr = ctx.Err() // work left undone, not failed
		}
		slog.Warn("row access policy scan incomplete",
			"status", scan.Status, "tables_checked", scan.TablesChecked, "tables_total", scan.TablesTotal,
			"datasets_failed", scan.DatasetsFailed, "failed_calls", failures, "timed_out", scan.TimedOut,
			"first_error", firstErr)
	}
	return policies, scan
}

// forEachLimited runs fn(0..n-1) on at most limit goroutines and stops
// handing out work once ctx is done.
func forEachLimited(ctx context.Context, n, limit int, fn func(i int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(limit, n)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
feed:
	for i := range n {
		if ctx.Err() != nil {
			break
		}
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
}

func toRLSPolicy(t rlsTable, p *bqv2.RowAccessPolicy) RLSPolicy {
	out := RLSPolicy{
		Dataset:   t.Dataset,
		Table:     t.Table,
		Predicate: p.FilterPredicate,
		Modified:  rlsTimestamp(p.LastModifiedTime),
	}
	if p.RowAccessPolicyReference != nil {
		out.Policy = p.RowAccessPolicyReference.PolicyId
	}
	return out
}

// rlsTimestamp normalizes RowAccessPolicy.lastModifiedTime to RFC 3339 UTC.
// The discovery document types the field as google-datetime (RFC 3339) but
// describes it as milliseconds since the epoch, so both forms are accepted.
func rlsTimestamp(v string) string {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	return ""
}

// bqRLSAPI implements rlsAPI with the BigQuery v2 REST service.
type bqRLSAPI struct {
	svc     *bqv2.Service
	project string
}

var errStopListing = errors.New("stop listing tables")

func (a bqRLSAPI) listTables(ctx context.Context, dataset string, visit func(rlsTable) bool) error {
	// The cloud.google.com/go/bigquery table iterator drops the table type,
	// so tables.list is called directly.
	err := a.svc.Tables.List(a.project, dataset).MaxResults(1000).
		Pages(ctx, func(page *bqv2.TableList) error {
			for _, t := range page.Tables {
				if t.TableReference == nil {
					continue
				}
				if !visit(rlsTable{Dataset: dataset, Table: t.TableReference.TableId, Type: t.Type}) {
					return errStopListing
				}
			}
			return nil
		})
	if errors.Is(err, errStopListing) {
		return nil
	}
	return err
}

func (a bqRLSAPI) listPolicies(ctx context.Context, dataset, table string) ([]*bqv2.RowAccessPolicy, error) {
	var out []*bqv2.RowAccessPolicy
	err := a.svc.RowAccessPolicies.List(a.project, dataset, table).
		Pages(ctx, func(page *bqv2.ListRowAccessPoliciesResponse) error {
			out = append(out, page.RowAccessPolicies...)
			return nil
		})
	return out, err
}

// GetRowAccessPolicies lists the row access policies in the given datasets.
func (b *BQClient) GetRowAccessPolicies(ctx context.Context, datasets []string) ([]RLSPolicy, RLSScan) {
	return scanRowAccessPolicies(ctx, b.rls, datasets, defaultRLSLimits)
}

// --- Caching (APIHandler) ---

const (
	rlsCacheTTL = time.Hour        // policies change rarely; a scan is up to ~550 API calls
	rlsRetryTTL = 10 * time.Minute // scans with failures are retried sooner
)

type rlsResult struct {
	Policies []RLSPolicy
	Scan     RLSScan
}

func rlsCacheKey(region string, datasets []string) string {
	return "security_rls:" + region + ":" + strings.Join(datasets, ",")
}

// rowAccessPolicies returns the scan for these datasets, cached apart from
// the dashboard so changing the time range does not repeat it.
func (h *APIHandler) rowAccessPolicies(ctx context.Context, region string, datasets []string) rlsResult {
	key := rlsCacheKey(region, datasets)
	if cached, ok := h.cache.Get(key); ok {
		return cached.(rlsResult)
	}
	policies, scan := h.bq.GetRowAccessPolicies(ctx, datasets)
	res := rlsResult{Policies: policies, Scan: scan}
	switch {
	case ctx.Err() != nil:
		// The request was canceled, not timed out by the scan: don't keep it.
	case scan.failed():
		h.cache.SetWithTTL(key, res, rlsRetryTTL)
	default:
		h.cache.SetWithTTL(key, res, rlsCacheTTL)
	}
	return res
}

// fillRLS stores the scan in data and marks the widget degraded when
// policies could not be read, so the UI shows "not evaluated" or "partially
// evaluated" rather than "no policies".
func (h *APIHandler) fillRLS(ctx context.Context, region string, datasets []string, data *SecurityDashboardData, addDegraded func(string)) {
	res := h.rowAccessPolicies(ctx, region, datasets)
	data.RLSPolicies = res.Policies
	data.RLSScan = res.Scan
	if res.Scan.failed() {
		addDegraded(rlsWidget)
	}
}
