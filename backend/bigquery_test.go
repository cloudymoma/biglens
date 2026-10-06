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
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("storageOverviewSQL missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "LIMIT 51") {
		t.Errorf("storageOverviewSQL should not truncate datasets with LIMIT 51:\n%s", sql)
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

func TestRecommendationsAndErrorSQL(t *testing.T) {
	recSQL := recommendationsSQL("`p`.`region-us`")
	for _, want := range []string{
		"JSON_VALUE(additional_details.overview, '$.bytesSavedMonthly')",
		"JSON_VALUE(additional_details.overview, '$.slotMsSavedMonthly')",
		"AS projected_savings_usd",
	} {
		if !strings.Contains(recSQL, want) {
			t.Errorf("recommendationsSQL missing %q in:\n%s", want, recSQL)
		}
	}

	where, _ := QueryFilters{TimeRange: "7d"}.JobsWhere("creation_time")
	for name, sql := range map[string]string{
		"errorOverviewSQL":   errorOverviewSQL("`p`.`region-us`", where),
		"errorStatsSQL":      errorStatsSQL("`p`.`region-us`", where),
		"topFailingUsersSQL": topFailingUsersSQL("`p`.`region-us`", where),
	} {
		if !strings.Contains(sql, "(statement_type IS NULL OR statement_type != 'SCRIPT')") {
			t.Errorf("%s missing SCRIPT exclusion in:\n%s", name, sql)
		}
	}

	errStats, failUsers := rollupErrorOverview([]errorOverviewRow{
		{Reason: "invalidQuery", GReason: 0, GUser: 1, JobCount: 8, SlotMs: 500},
		{UserEmail: "alice@example.com", GReason: 1, GUser: 0, JobCount: 5, SlotMs: 1024},
	})
	if len(errStats) != 1 || errStats[0].Reason != "invalidQuery" || errStats[0].JobCount != 8 {
		t.Errorf("unexpected ErrorStats: %+v", errStats)
	}
	if len(failUsers) != 1 || failUsers[0].UserEmail != "alice@example.com" || failUsers[0].JobCount != 5 {
		t.Errorf("unexpected FailingUsers: %+v", failUsers)
	}
}

func TestColdTablesSQLAndReservationTimelineSQL(t *testing.T) {
	where, _ := QueryFilters{Dataset: "ds1"}.StorageWhere()
	extra := strings.TrimPrefix(where, " WHERE ")
	if extra != "" {
		extra = " AND " + extra
	}
	coldSQL := coldTablesSQL("`p`.`region-us`", "90 DAY", extra)
	for _, want := range []string{
		"WITH refs AS (",
		"SELECT DISTINCT rt.dataset_id, rt.table_id",
		"WHERE j.creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)",
		"AND rt.project_id = @project",
		"AND ts.deleted = FALSE",
		"AND ts.table_type = 'BASE TABLE'",
		"AND ts.creation_time < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)",
		"LEFT JOIN refs r",
		"WHERE r.table_id IS NULL",
		"AS billing_model",
	} {
		if !strings.Contains(coldSQL, want) {
			t.Errorf("coldTablesSQL missing %q in:\n%s", want, coldSQL)
		}
	}

	resSQL := reservationTimelineSQL("`p`.`region-us`", "7 DAY")
	for _, want := range []string{
		"DIV(UNIX_SECONDS(period_start), @bucket_secs) * @bucket_secs",
		"SAFE_DIVIDE(period_autoscale_slot_seconds, 60)",
		"CAST(AVG(autoscale_slots) AS FLOAT64) AS autoscale",
	} {
		if !strings.Contains(resSQL, want) {
			t.Errorf("reservationTimelineSQL missing %q in:\n%s", want, resSQL)
		}
	}
}

func TestCostOverviewSQLAndRollup(t *testing.T) {
	where, _ := QueryFilters{TimeRange: "30d"}.JobsWhere("creation_time")
	sqlUser := costOverviewSQL("`p`.`region-us`", where, "IFNULL(user_email, '')", true)
	for _, want := range []string{
		"GROUP BY GROUPING SETS ((), (day), (spend_name))",
		"SUM(IF(reservation_id IS NULL, total_bytes_billed, 0))",
	} {
		if !strings.Contains(sqlUser, want) {
			t.Errorf("costOverviewSQL(user) missing %q in:\n%s", want, sqlUser)
		}
	}

	sqlDataset := costOverviewSQL("`p`.`region-us`", where, "IFNULL(user_email, '')", false)
	if !strings.Contains(sqlDataset, "GROUP BY GROUPING SETS ((), (day))") {
		t.Errorf("costOverviewSQL(dataset) unexpected grouping sets:\n%s", sqlDataset)
	}

	summary, daily, spend := rollupCostOverview([]costGroupingRow{
		{
			GDay:                1,
			GName:               1,
			BytesBilled:         1000,
			OnDemandBytesBilled: 600,
			BytesProcessed:      1200,
			TotalSlotMs:         5000,
		},
		{
			Day:                 "2026-04-01",
			GDay:                0,
			GName:               1,
			BytesBilled:         300,
			OnDemandBytesBilled: 200,
			TotalSlotMs:         2000,
		},
		{
			Day:                 "2026-04-02",
			GDay:                0,
			GName:               1,
			BytesBilled:         700,
			OnDemandBytesBilled: 400,
			TotalSlotMs:         3000,
		},
		{
			Name:        "alice@example.com",
			GDay:        1,
			GName:       0,
			BytesBilled: 800,
			TotalSlotMs: 4000,
		},
	})
	if summary.BytesBilled != 1000 || summary.OnDemandBytesBilled != 600 || summary.BytesProcessed != 1200 {
		t.Errorf("unexpected CostSummary: %+v", summary)
	}
	if len(daily) != 2 || daily[0].Day != "2026-04-01" || daily[1].Day != "2026-04-02" {
		t.Errorf("unexpected DailyCost order: %+v", daily)
	}
	if len(spend) != 1 || spend[0].Name != "alice@example.com" || spend[0].TotalBytes != 800 {
		t.Errorf("unexpected SpendEntry: %+v", spend)
	}
}

func TestListJobsSQLAndFilters(t *testing.T) {
	f := QueryFilters{
		TimeRange: "7d",
		Dataset:   "ds1",
		Table:     "tbl1",
		Status:    "failed",
		CacheHit:  "miss",
	}
	jobsWhere, _ := f.JobsWhere("creation_time")
	for _, want := range []string{
		"EXISTS (SELECT 1 FROM UNNEST(referenced_tables) rt WHERE rt.dataset_id = @dataset AND rt.table_id = @table_name)",
		"NOT EXISTS (SELECT 1 FROM UNNEST(labels) l WHERE l.key = 'app' AND l.value = 'biglens')",
		"error_result IS NOT NULL",
		"cache_hit = FALSE",
	} {
		if !strings.Contains(jobsWhere, want) {
			t.Errorf("JobsWhere missing %q in:\n%s", want, jobsWhere)
		}
	}

	tlWhere, _ := f.TimelineWhere("period_start")
	for _, want := range []string{
		"error_result IS NOT NULL",
		"cache_hit = FALSE",
	} {
		if !strings.Contains(tlWhere, want) {
			t.Errorf("TimelineWhere missing %q in:\n%s", want, tlWhere)
		}
	}

	sql := listJobsSQL("`p`.`region-us`", jobsWhere)
	if !strings.Contains(sql, "FROM (\n\t\t\tSELECT") || !strings.Contains(sql, "ORDER BY creation_time DESC\n\t\t\tLIMIT 100\n\t\t)") {
		t.Errorf("listJobsSQL missing inner LIMIT 100 subquery:\n%s", sql)
	}

	// Scoped cache keys should ignore irrelevant filters.
	f1 := QueryFilters{Region: "US", TimeRange: "7d", Dataset: "ds1", UserEmail: "u1@x.com"}
	f2 := QueryFilters{Region: "US", TimeRange: "30d", Dataset: "ds1", UserEmail: "u2@x.com"}
	if f1.StorageCoreCacheKey("storage") != f2.StorageCoreCacheKey("storage") {
		t.Errorf("StorageCoreCacheKey should ignore TimeRange and UserEmail")
	}
}




