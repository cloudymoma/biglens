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
	TotalEmails    int64 `json:"total_emails" bigquery:"total_emails"`
	ServiceAccounts int64 `json:"service_accounts" bigquery:"service_accounts"`
	HumanUsers     int64 `json:"human_users" bigquery:"human_users"`
	TotalCalls     int64 `json:"total_calls" bigquery:"total_calls"`
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
