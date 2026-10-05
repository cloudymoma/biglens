package main

import (
	"reflect"
	"strings"
	"testing"
)

// The slot chart folds every job's timeline rows into one row per second and
// only then buckets them. Averages divide by the whole bucket, so idle seconds
// count as zero, and peaks stay the busiest single second, so "Peak
// Concurrent" keeps its per-second meaning instead of silently turning into
// the highest bucket average.
func TestSlotTimelineSQL(t *testing.T) {
	where, params := QueryFilters{TimeRange: "30d", UserEmail: "a@b.com"}.TimelineWhere("period_start")
	sql := slotTimelineSQL("`p`.`region-us`", where)

	for _, want := range []string{
		"FROM `p`.`region-us`.INFORMATION_SCHEMA.JOBS_TIMELINE_BY_PROJECT",
		"job_creation_time >= ",
		"user_email = @user_email",
		"AND state IN ('PENDING', 'RUNNING')",
		"AND (statement_type IS NULL OR statement_type != 'SCRIPT')",
		"COUNT(DISTINCT IF(state = 'PENDING', job_id, NULL)) AS pending_jobs",
		"GROUP BY period_start",
		"DIV(UNIX_SECONDS(period_start), @bucket_secs) * @bucket_secs",
		"SUM(running_ms) / (@bucket_secs * 1000) AS avg_running",
		"SUM(pending_jobs) / @bucket_secs AS avg_pending",
		"MAX(running_ms) / 1000 AS peak_total",
		"CAST(MAX(pending_jobs) AS FLOAT64) AS peak_pending",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in SQL:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "GROUP BY period_start, state") {
		t.Errorf("SQL still returns one row per second and state:\n%s", sql)
	}

	// Every column the struct loads must be produced by the query.
	rt := reflect.TypeOf(SlotBucket{})
	for i := 0; i < rt.NumField(); i++ {
		if col := rt.Field(i).Tag.Get("bigquery"); !strings.Contains(sql, " AS "+col+",") && !strings.Contains(sql, " AS "+col+"\n") {
			t.Errorf("SlotBucket.%s loads %q, which the SQL never selects:\n%s", rt.Field(i).Name, col, sql)
		}
	}

	// Every referenced parameter must be bound.
	bound := map[string]bool{}
	for _, p := range params {
		bound[p.Name] = true
	}
	for _, name := range []string{"bucket_secs", "user_email"} {
		if !strings.Contains(sql, "@"+name) || !bound[name] {
			t.Errorf("@%s referenced=%v bound=%v", name, strings.Contains(sql, "@"+name), bound[name])
		}
	}
}

// searchIndexesSQL interpolates dataset identifiers into backtick-quoted
// INFORMATION_SCHEMA paths, so every dataset name must match datasetNameRe
// and multiple datasets must be combined via UNION ALL to avoid N+1 jobs.
func TestSearchIndexesSQLValidatesAndBatches(t *testing.T) {
	for _, bad := range []string{"", "ds`; DROP TABLE x; --", "a-b", "a.b", "../ds"} {
		if _, err := searchIndexesSQL("proj", []string{bad}, false); err == nil {
			t.Errorf("searchIndexesSQL accepted invalid dataset %q", bad)
		}
	}

	sql, err := searchIndexesSQL("proj", []string{"ds_one", "ds_two"}, true)
	if err != nil {
		t.Fatalf("searchIndexesSQL valid datasets failed: %v", err)
	}
	for _, want := range []string{
		"FROM `proj.ds_one.INFORMATION_SCHEMA.SEARCH_INDEXES` WHERE table_name = @table_name",
		"UNION ALL",
		"FROM `proj.ds_two.INFORMATION_SCHEMA.SEARCH_INDEXES` WHERE table_name = @table_name",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("searchIndexesSQL missing %q in:\n%s", want, sql)
		}
	}
}

func TestStorageOverviewSQLAndRollup(t *testing.T) {
	where, _ := QueryFilters{Dataset: "ds1"}.StorageWhere()
	sql := storageOverviewSQL("`p`.`region-us`", where)

	for _, want := range []string{
		"GROUPING(table_schema) AS is_rollup",
		"SUM(IF(NOT deleted, active_logical_bytes, 0))",
		"SUM(IF(NOT deleted, long_term_logical_bytes, 0))",
		"SUM(active_physical_bytes)",
		"table_type = 'BASE TABLE'",
		"table_schema = @dataset",
		"GROUP BY ROLLUP(table_schema)",
		"ORDER BY is_rollup DESC",
		"LIMIT 51",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("storageOverviewSQL missing %q in:\n%s", want, sql)
		}
	}

	stats, bd, ds := rollupStorageOverview([]storageRollupRow{
		{
			IsRollup:         1,
			ActiveLogical:    1000,
			LongTermLogical:  500,
			ActivePhysical:   300,
			LongTermPhysical: 100,
			TimeTravel:       50,
			FailSafe:         20,
		},
		{
			Dataset:          "ds1",
			IsRollup:         0,
			ActiveLogical:    600,
			LongTermLogical:  400,
			ActivePhysical:   200,
			LongTermPhysical: 80,
			TimeTravel:       30,
			FailSafe:         10,
		},
	})
	if stats.LogicalBytes != 1500 || stats.PhysicalBytes != 420 || stats.ActiveLogical != 1000 || stats.FailSafe != 20 {
		t.Errorf("unexpected StorageStats: %+v", stats)
	}
	if bd.ActiveBytes != 1000 || bd.LongTermBytes != 500 {
		t.Errorf("unexpected StorageBreakdown: %+v", bd)
	}
	if len(ds) != 1 || ds[0].Dataset != "ds1" || ds[0].ActiveLogical != 600 {
		t.Errorf("unexpected DatasetStorage: %+v", ds)
	}
}


