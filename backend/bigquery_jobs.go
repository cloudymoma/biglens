package main

import (
	"context"
	"fmt"
)

// Job-level queries for the Insights widgets and the Jobs explorer tab, all
// backed by INFORMATION_SCHEMA.JOBS_BY_PROJECT.

// --- Widget 4.2: Error Analysis ---

type ErrorStat struct {
	Reason   string `json:"reason" bigquery:"reason"`
	JobCount int64  `json:"job_count" bigquery:"job_count"`
	SlotMs   int64  `json:"slot_ms" bigquery:"slot_ms"`
}

func errorStatsSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			IFNULL(error_result.reason, 'unknown') AS reason,
			COUNT(*) AS job_count,
			IFNULL(SUM(total_slot_ms), 0) AS slot_ms
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND error_result IS NOT NULL
			AND (statement_type IS NULL OR statement_type != 'SCRIPT')
		GROUP BY reason
		ORDER BY slot_ms DESC`,
		regionRef, where)
}

// GetErrorStats ranks failure reasons by wasted slot time: a handful of
// resource-exceeded errors typically outweigh thousands of syntax errors.
func (b *BQClient) GetErrorStats(ctx context.Context, filters QueryFilters) ([]ErrorStat, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(errorStatsSQL(b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[ErrorStat](q, ctx)
}

type FailingUser struct {
	UserEmail string `json:"user_email" bigquery:"user_email"`
	JobCount  int64  `json:"job_count" bigquery:"job_count"`
	SlotMs    int64  `json:"slot_ms" bigquery:"slot_ms"`
}

func topFailingUsersSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			user_email,
			COUNT(*) AS job_count,
			IFNULL(SUM(total_slot_ms), 0) AS slot_ms
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND error_result IS NOT NULL
			AND (statement_type IS NULL OR statement_type != 'SCRIPT')
		GROUP BY user_email
		ORDER BY slot_ms DESC
		LIMIT 10`,
		regionRef, where)
}

func (b *BQClient) GetTopFailingUsers(ctx context.Context, filters QueryFilters) ([]FailingUser, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(topFailingUsersSQL(b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[FailingUser](q, ctx)
}

type errorOverviewRow struct {
	GReason   int64  `bigquery:"g_reason"`
	GUser     int64  `bigquery:"g_user"`
	Reason    string `bigquery:"reason"`
	UserEmail string `bigquery:"user_email"`
	JobCount  int64  `bigquery:"job_count"`
	SlotMs    int64  `bigquery:"slot_ms"`
}

func errorOverviewSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			g_reason,
			g_user,
			IFNULL(reason, '') AS reason,
			IFNULL(user_email, '') AS user_email,
			job_count,
			slot_ms
		FROM (
			SELECT
				GROUPING(reason) AS g_reason,
				GROUPING(user_email) AS g_user,
				reason,
				user_email,
				COUNT(*) AS job_count,
				IFNULL(SUM(total_slot_ms), 0) AS slot_ms
			FROM (
				SELECT
					IFNULL(error_result.reason, 'unknown') AS reason,
					IFNULL(user_email, '') AS user_email,
					total_slot_ms
				FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
				%s AND error_result IS NOT NULL
					AND (statement_type IS NULL OR statement_type != 'SCRIPT')
			)
			GROUP BY GROUPING SETS ((reason), (user_email))
		)
		ORDER BY g_reason ASC, slot_ms DESC`,
		regionRef, where)
}

func rollupErrorOverview(rows []errorOverviewRow) ([]ErrorStat, []FailingUser) {
	var stats []ErrorStat
	var users []FailingUser
	for _, r := range rows {
		switch {
		case r.GReason == 0 && r.GUser == 1:
			stats = append(stats, ErrorStat{
				Reason:   r.Reason,
				JobCount: r.JobCount,
				SlotMs:   r.SlotMs,
			})
		case r.GReason == 1 && r.GUser == 0:
			if len(users) < 10 {
				users = append(users, FailingUser{
					UserEmail: r.UserEmail,
					JobCount:  r.JobCount,
					SlotMs:    r.SlotMs,
				})
			}
		}
	}
	return stats, users
}

// GetErrorOverview scans JOBS_BY_PROJECT once with GROUPING SETS ((reason), (user_email))
// to return both ErrorStat and top 10 FailingUser rows.
func (b *BQClient) GetErrorOverview(ctx context.Context, filters QueryFilters) ([]ErrorStat, []FailingUser, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(errorOverviewSQL(b.regionRef(filters.Region), where))
	q.Parameters = params
	rows, err := collectRows[errorOverviewRow](q, ctx)
	if err != nil {
		return nil, nil, err
	}
	es, fu := rollupErrorOverview(rows)
	return es, fu, nil
}


// --- Widget 4.3: Performance Insights ---

// perfInsightFlags is the shared SELECT fragment deriving one boolean per
// BigQuery performance-insight type from the stage-level insight array.
const perfInsightFlags = `
	EXISTS(SELECT 1 FROM UNNEST(query_info.performance_insights.stage_performance_standalone_insights) s WHERE s.slot_contention) AS slot_contention,
	EXISTS(SELECT 1 FROM UNNEST(query_info.performance_insights.stage_performance_standalone_insights) s WHERE s.insufficient_shuffle_quota) AS shuffle_quota,
	EXISTS(SELECT 1 FROM UNNEST(query_info.performance_insights.stage_performance_standalone_insights) s WHERE ARRAY_LENGTH(s.high_cardinality_joins) > 0) AS high_card_join,
	EXISTS(SELECT 1 FROM UNNEST(query_info.performance_insights.stage_performance_standalone_insights) s WHERE s.partition_skew IS NOT NULL) AS partition_skew`

type PerfInsightJob struct {
	JobID          string `json:"job_id" bigquery:"job_id"`
	UserEmail      string `json:"user_email" bigquery:"user_email"`
	SlotMs         int64  `json:"slot_ms" bigquery:"slot_ms"`
	SlotContention bool   `json:"slot_contention" bigquery:"slot_contention"`
	ShuffleQuota   bool   `json:"shuffle_quota" bigquery:"shuffle_quota"`
	HighCardJoin   bool   `json:"high_card_join" bigquery:"high_card_join"`
	PartitionSkew  bool   `json:"partition_skew" bigquery:"partition_skew"`
}

func (b *BQClient) GetPerfInsightJobs(ctx context.Context, filters QueryFilters) ([]PerfInsightJob, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT * FROM (
			SELECT
				job_id,
				user_email,
				IFNULL(total_slot_ms, 0) AS slot_ms,%s
			FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
			%s AND query_info.performance_insights IS NOT NULL
		)
		WHERE slot_contention OR shuffle_quota OR high_card_join OR partition_skew
		ORDER BY slot_ms DESC
		LIMIT 100`,
		perfInsightFlags, b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[PerfInsightJob](q, ctx)
}

// --- Widget 4.4: Most-Repeated Queries ---

type RepeatedQuery struct {
	QueryHash   string `json:"query_hash" bigquery:"query_hash"`
	SampleQuery string `json:"sample_query" bigquery:"sample_query"`
	Runs        int64  `json:"runs" bigquery:"runs"`
	TotalBytes  int64  `json:"total_bytes" bigquery:"total_bytes"`
	UserCount   int64  `json:"user_count" bigquery:"user_count"`
}

// GetRepeatedQueries groups by the literal-normalized query hash, so the
// same statement run with different literals (loops, schedules) collapses
// into one row.
func (b *BQClient) GetRepeatedQueries(ctx context.Context, filters QueryFilters) ([]RepeatedQuery, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			query_info.query_hashes.normalized_literals AS query_hash,
			ANY_VALUE(LEFT(query, 200)) AS sample_query,
			COUNT(*) AS runs,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes,
			COUNT(DISTINCT user_email) AS user_count
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND job_type = 'QUERY' AND statement_type != 'SCRIPT'
			AND query_info.query_hashes.normalized_literals IS NOT NULL
		GROUP BY query_hash
		HAVING runs >= 2
		ORDER BY total_bytes DESC
		LIMIT 15`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[RepeatedQuery](q, ctx)
}

// --- Jobs Explorer ---

type JobRow struct {
	JobID          string   `json:"job_id" bigquery:"job_id"`
	UserEmail      string   `json:"user_email" bigquery:"user_email"`
	JobType        string   `json:"job_type" bigquery:"job_type"`
	StatementType  string   `json:"statement_type" bigquery:"statement_type"`
	State          string   `json:"state" bigquery:"state"`
	ErrorReason    string   `json:"error_reason" bigquery:"error_reason"`
	CreationTime   string   `json:"creation_time" bigquery:"creation_time"`
	Reservation    string   `json:"reservation" bigquery:"reservation"`
	QueueMs        int64    `json:"queue_ms" bigquery:"queue_ms"`
	DurationMs     int64    `json:"duration_ms" bigquery:"duration_ms"`
	SlotMs         int64    `json:"slot_ms" bigquery:"slot_ms"`
	BytesBilled    int64    `json:"bytes_billed" bigquery:"bytes_billed"`
	CacheHit       bool     `json:"cache_hit" bigquery:"cache_hit"`
	RefTables      []string `json:"ref_tables" bigquery:"ref_tables"`
	Query          string   `json:"query" bigquery:"query"`
	SlotContention bool     `json:"slot_contention" bigquery:"slot_contention"`
	ShuffleQuota   bool     `json:"shuffle_quota" bigquery:"shuffle_quota"`
	HighCardJoin   bool     `json:"high_card_join" bigquery:"high_card_join"`
	PartitionSkew  bool     `json:"partition_skew" bigquery:"partition_skew"`
}

func listJobsSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			job_id,
			user_email,
			IFNULL(job_type, '') AS job_type,
			IFNULL(statement_type, '') AS statement_type,
			state,
			IFNULL(error_result.reason, '') AS error_reason,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", creation_time) AS creation_time,
			IFNULL(reservation_id, '') AS reservation,
			IFNULL(TIMESTAMP_DIFF(start_time, creation_time, MILLISECOND), 0) AS queue_ms,
			IFNULL(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND), 0) AS duration_ms,
			IFNULL(total_slot_ms, 0) AS slot_ms,
			IFNULL(total_bytes_billed, 0) AS bytes_billed,
			IFNULL(cache_hit, FALSE) AS cache_hit,
			ARRAY(SELECT CONCAT(rt.dataset_id, '.', rt.table_id) FROM UNNEST(referenced_tables) rt) AS ref_tables,
			IFNULL(LEFT(query, 1024), '') AS query,%s
		FROM (
			SELECT
				job_id,
				user_email,
				job_type,
				statement_type,
				state,
				error_result,
				creation_time,
				reservation_id,
				start_time,
				end_time,
				total_slot_ms,
				total_bytes_billed,
				cache_hit,
				referenced_tables,
				query,
				query_info
			FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
			%s
			ORDER BY creation_time DESC
			LIMIT 100
		)
		ORDER BY creation_time DESC`,
		perfInsightFlags, regionRef, where)
}

func (b *BQClient) ListJobs(ctx context.Context, filters QueryFilters) ([]JobRow, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(listJobsSQL(b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[JobRow](q, ctx)
}
