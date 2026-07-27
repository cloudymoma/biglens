package main

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/bigquery"
)

// --- Email autocomplete ---

func (b *BQClient) SearchEmails(ctx context.Context, region, prefix string, limit int) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var whereParts []string
	var params []bigquery.QueryParameter

	whereParts = append(whereParts,
		"creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)")

	if prefix != "" {
		whereParts = append(whereParts, "LOWER(user_email) LIKE CONCAT(LOWER(@prefix), '%')")
		params = append(params, bigquery.QueryParameter{Name: "prefix", Value: prefix})
	}

	q := b.client.Query(fmt.Sprintf(
		`SELECT DISTINCT user_email
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s
		ORDER BY user_email
		LIMIT @limit`,
		b.regionRef(region), strings.Join(whereParts, " AND ")))

	params = append(params, bigquery.QueryParameter{Name: "limit", Value: limit})
	q.Parameters = params

	type row struct {
		UserEmail string `bigquery:"user_email"`
	}

	rows, err := collectRows[row](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("search emails query failed: %w", err)
	}

	emails := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.UserEmail != "" {
			emails = append(emails, r.UserEmail)
		}
	}
	return emails, nil
}

// --- Usage timeline: jobs per interval bucketed by hour/day ---

type UsageTimepoint struct {
	Bucket    string `json:"bucket" bigquery:"bucket"`
	Email     string `json:"email" bigquery:"email"`
	CallCount int64  `json:"call_count" bigquery:"call_count"`
}

func (b *BQClient) GetUsageTimeline(ctx context.Context, region string, emails []string, timeRange string) ([]UsageTimepoint, error) {
	interval, truncUnit := timeRangeToBucket(timeRange)

	var clauses []string
	var params []bigquery.QueryParameter

	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval))

	if len(emails) > 0 {
		clauses = append(clauses, "user_email IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}

	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", TIMESTAMP_TRUNC(creation_time, %s)) AS bucket,
			user_email AS email,
			COUNT(*) AS call_count
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s
		GROUP BY bucket, email
		ORDER BY bucket ASC`,
		truncUnit, b.regionRef(region), strings.Join(clauses, " AND ")))
	q.Parameters = params

	return collectRows[UsageTimepoint](q, ctx)
}

// --- Top frequent callers ---

type TopCaller struct {
	Email       string  `json:"email" bigquery:"email"`
	TotalCalls  int64   `json:"total_calls" bigquery:"total_calls"`
	TotalSlotMs int64   `json:"total_slot_ms" bigquery:"total_slot_ms"`
	TotalBytes  int64   `json:"total_bytes" bigquery:"total_bytes"`
	AvgDuration float64 `json:"avg_duration_sec" bigquery:"avg_duration_sec"`
	LastActive  string  `json:"last_active" bigquery:"last_active"`
}

func (b *BQClient) GetTopCallers(ctx context.Context, region string, emails []string, timeRange string, limit int) ([]TopCaller, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	interval := timeRangeToInterval(timeRange)
	var clauses []string
	var params []bigquery.QueryParameter

	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval))

	if len(emails) > 0 {
		clauses = append(clauses, "user_email IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}

	params = append(params, bigquery.QueryParameter{Name: "result_limit", Value: limit})

	q := b.client.Query(fmt.Sprintf(
		`SELECT
			user_email AS email,
			COUNT(*) AS total_calls,
			IFNULL(SUM(total_slot_ms), 0) AS total_slot_ms,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes,
			AVG(TIMESTAMP_DIFF(end_time, start_time, SECOND)) AS avg_duration_sec,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", MAX(creation_time)) AS last_active
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s
		GROUP BY email
		ORDER BY total_calls DESC
		LIMIT @result_limit`,
		b.regionRef(region), strings.Join(clauses, " AND ")))
	q.Parameters = params

	return collectRows[TopCaller](q, ctx)
}

// --- Inactive emails ---

type InactiveEmail struct {
	Email      string `json:"email" bigquery:"email"`
	LastActive string `json:"last_active" bigquery:"last_active"`
	DaysIdle   int64  `json:"days_idle" bigquery:"days_idle"`
	TotalCalls int64  `json:"total_calls" bigquery:"total_calls"`
}

func (b *BQClient) GetInactiveEmails(ctx context.Context, region string, inactiveDays int) ([]InactiveEmail, error) {
	if inactiveDays <= 0 {
		inactiveDays = 30
	}

	params := []bigquery.QueryParameter{
		{Name: "inactive_days", Value: inactiveDays},
	}

	q := b.client.Query(fmt.Sprintf(
		`WITH recent AS (
			SELECT
				user_email AS email,
				MAX(creation_time) AS last_active,
				COUNT(*) AS total_calls
			FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
			WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 180 DAY)
			GROUP BY email
		)
		SELECT
			email,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", last_active) AS last_active,
			TIMESTAMP_DIFF(CURRENT_TIMESTAMP(), last_active, DAY) AS days_idle,
			total_calls
		FROM recent
		WHERE TIMESTAMP_DIFF(CURRENT_TIMESTAMP(), last_active, DAY) >= @inactive_days
		ORDER BY days_idle DESC`,
		b.regionRef(region)))
	q.Parameters = params

	return collectRows[InactiveEmail](q, ctx)
}

// --- IAM Summary stats ---

type IAMSummary struct {
	TotalEmails     int64 `json:"total_emails" bigquery:"total_emails"`
	ServiceAccounts int64 `json:"service_accounts" bigquery:"service_accounts"`
	HumanUsers      int64 `json:"human_users" bigquery:"human_users"`
	TotalCalls      int64 `json:"total_calls" bigquery:"total_calls"`
}

func (b *BQClient) GetIAMSummary(ctx context.Context, region, timeRange string) (*IAMSummary, error) {
	interval := timeRangeToInterval(timeRange)

	q := b.client.Query(fmt.Sprintf(
		`SELECT
			COUNT(DISTINCT user_email) AS total_emails,
			COUNTIF(ENDS_WITH(user_email, '.gserviceaccount.com') OR ENDS_WITH(user_email, '.iam.gserviceaccount.com')) AS service_accounts,
			COUNTIF(NOT ENDS_WITH(user_email, '.gserviceaccount.com') AND NOT ENDS_WITH(user_email, '.iam.gserviceaccount.com')) AS human_users,
			COUNT(*) AS total_calls
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)`,
		b.regionRef(region), interval))

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("iam summary query failed: %w", err)
	}

	var s IAMSummary
	if err := it.Next(&s); err != nil {
		return &IAMSummary{}, nil
	}
	return &s, nil
}

func timeRangeToInterval(tr string) string {
	switch tr {
	case "1d":
		return "1 DAY"
	case "30d":
		return "30 DAY"
	case "90d":
		return "90 DAY"
	default:
		return "7 DAY"
	}
}

func timeRangeToBucket(tr string) (interval, truncUnit string) {
	switch tr {
	case "1d":
		return "1 DAY", "HOUR"
	case "7d":
		return "7 DAY", "HOUR"
	case "30d":
		return "30 DAY", "DAY"
	case "90d":
		return "90 DAY", "DAY"
	default:
		return "7 DAY", "HOUR"
	}
}

// --- New actors: first job ever (90d baseline) within the last 7 days ---

type NewActor struct {
	Email     string `json:"email" bigquery:"email"`
	FirstSeen string `json:"first_seen" bigquery:"first_seen"`
	Jobs      int64  `json:"jobs" bigquery:"jobs"`
	IsSA      bool   `json:"is_sa" bigquery:"is_sa"`
}

func (b *BQClient) GetNewActors(ctx context.Context, region string) ([]NewActor, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT user_email AS email,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", MIN(creation_time)) AS first_seen,
			COUNT(*) AS jobs,
			LOGICAL_OR(ENDS_WITH(user_email, 'gserviceaccount.com')) AS is_sa
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)
		GROUP BY email
		HAVING MIN(creation_time) >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)
		ORDER BY first_seen DESC LIMIT 50`, b.regionRef(region)))
	return collectRows[NewActor](q, ctx)
}

// --- Off-hours activity: human jobs by weekday x hour (UTC) ---

type OffHoursCell struct {
	Dow  int64 `json:"dow" bigquery:"dow"`
	Hr   int64 `json:"hr" bigquery:"hr"`
	Jobs int64 `json:"jobs" bigquery:"jobs"`
}

type OffHoursUser struct {
	Email string `json:"email" bigquery:"email"`
	Jobs  int64  `json:"jobs" bigquery:"jobs"`
}

func (b *BQClient) GetOffHours(ctx context.Context, region string, emails []string, timeRange string) ([]OffHoursCell, []OffHoursUser, error) {
	interval := timeRangeToInterval(timeRange)
	var clauses []string
	var params []bigquery.QueryParameter
	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"NOT ENDS_WITH(user_email, 'gserviceaccount.com')")
	if len(emails) > 0 {
		clauses = append(clauses, "user_email IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}
	where := strings.Join(clauses, " AND ")

	hq := b.client.Query(fmt.Sprintf(
		`SELECT EXTRACT(DAYOFWEEK FROM creation_time) AS dow,
			EXTRACT(HOUR FROM creation_time) AS hr, COUNT(*) AS jobs
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s GROUP BY dow, hr`, b.regionRef(region), where))
	hq.Parameters = params
	cells, err := collectRows[OffHoursCell](hq, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("off-hours heatmap query failed: %w", err)
	}

	tq := b.client.Query(fmt.Sprintf(
		`SELECT user_email AS email, COUNT(*) AS jobs
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s AND EXTRACT(HOUR FROM creation_time) < 6
		GROUP BY email ORDER BY jobs DESC LIMIT 10`, b.regionRef(region), where))
	tq.Parameters = params
	top, err := collectRows[OffHoursUser](tq, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("off-hours top query failed: %w", err)
	}
	return cells, top, nil
}

// --- Exfiltration signals ---

type ExfilSignal struct {
	Email       string `json:"email" bigquery:"email"`
	JobID       string `json:"job_id" bigquery:"job_id"`
	Signal      string `json:"signal" bigquery:"signal"`
	Bytes       int64  `json:"bytes" bigquery:"bytes"`
	DestProject string `json:"dest_project" bigquery:"dest_project"`
	Created     string `json:"created" bigquery:"created"`
}

func (b *BQClient) GetExfilSignals(ctx context.Context, region string, emails []string, timeRange string) ([]ExfilSignal, error) {
	interval := timeRangeToInterval(timeRange)
	var clauses []string
	params := []bigquery.QueryParameter{
		{Name: "project", Value: b.config.BigQuery.ProjectID},
	}
	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"IFNULL(statement_type, '') != 'SCRIPT'")
	if len(emails) > 0 {
		clauses = append(clauses, "user_email IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}

	q := b.client.Query(fmt.Sprintf(
		`SELECT user_email AS email, IFNULL(job_id, '') AS job_id,
			CASE
				WHEN job_type = 'EXTRACT' THEN 'EXTRACT_TO_GCS'
				WHEN statement_type = 'EXPORT_DATA' THEN 'EXPORT_DATA'
				WHEN destination_table.project_id IS NOT NULL
					AND destination_table.project_id != @project THEN 'CROSS_PROJECT_WRITE'
				ELSE 'LARGE_SCAN'
			END AS signal,
			IFNULL(total_bytes_processed, 0) AS bytes,
			IFNULL(destination_table.project_id, '') AS dest_project,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", creation_time) AS created
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s AND (
			job_type = 'EXTRACT'
			OR statement_type = 'EXPORT_DATA'
			OR (destination_table.project_id IS NOT NULL AND destination_table.project_id != @project)
			OR total_bytes_processed > 1099511627776)
		ORDER BY bytes DESC LIMIT 50`,
		b.regionRef(region), strings.Join(clauses, " AND ")))
	q.Parameters = params
	return collectRows[ExfilSignal](q, ctx)
}
