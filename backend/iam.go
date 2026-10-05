package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
)

// --- Email autocomplete ---

// escapeLikePattern escapes '\', '%', and '_' so they match literally in a
// SQL LIKE expression with ESCAPE '\\'.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func filterEmailsByPrefix(all []string, prefix string, limit int) []string {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(prefix))
	if q == "" {
		return []string{}
	}
	out := make([]string, 0, limit)
	for _, e := range all {
		if strings.HasPrefix(strings.ToLower(e), q) {
			out = append(out, e)
			if len(out) >= limit {
				return out
			}
		}
	}
	for _, e := range all {
		le := strings.ToLower(e)
		if !strings.HasPrefix(le, q) && strings.Contains(le, q) {
			out = append(out, e)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func (b *BQClient) GetDistinctEmails180d(ctx context.Context, region string) ([]string, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT DISTINCT user_email
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 180 DAY)
			AND user_email IS NOT NULL
			AND IFNULL(statement_type, '') != 'SCRIPT'
		ORDER BY user_email`,
		b.regionRef(region)))
	type row struct {
		UserEmail string `bigquery:"user_email"`
	}
	rows, err := collectRows[row](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("distinct emails 180d query failed: %w", err)
	}
	emails := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.UserEmail != "" {
			emails = append(emails, r.UserEmail)
		}
	}
	return emails, nil
}

func (b *BQClient) SearchEmails(ctx context.Context, region, prefix string, limit int) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var whereParts []string
	var params []bigquery.QueryParameter

	whereParts = append(whereParts,
		"creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 180 DAY)",
		"user_email IS NOT NULL",
		"IFNULL(statement_type, '') != 'SCRIPT'")

	if prefix != "" {
		whereParts = append(whereParts, `LOWER(user_email) LIKE CONCAT(LOWER(@prefix), '%') ESCAPE '\\'`)
		params = append(params, bigquery.QueryParameter{Name: "prefix", Value: escapeLikePattern(prefix)})
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

func usageTimelineSQL(regionRef, truncUnit, where string) string {
	return fmt.Sprintf(
		`SELECT bucket, IF(rk <= 10, email, '(others)') AS email, SUM(c) AS call_count
		FROM (
			SELECT bucket, email, c, DENSE_RANK() OVER (ORDER BY tot DESC, email) AS rk
			FROM (
				SELECT bucket, email, c, SUM(c) OVER (PARTITION BY email) AS tot
				FROM (
					SELECT
						FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", TIMESTAMP_TRUNC(creation_time, %s)) AS bucket,
						user_email AS email,
						COUNT(*) AS c
					FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
					WHERE %s
					GROUP BY bucket, email
				)
			)
		)
		GROUP BY bucket, email
		ORDER BY bucket ASC`,
		truncUnit, regionRef, where)
}

func (b *BQClient) GetUsageTimeline(ctx context.Context, region string, emails []string, timeRange string) ([]UsageTimepoint, error) {
	interval, truncUnit := timeRangeToBucket(timeRange)

	var clauses []string
	var params []bigquery.QueryParameter

	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"user_email IS NOT NULL",
		"IFNULL(statement_type, '') != 'SCRIPT'")

	if len(emails) > 0 {
		clauses = append(clauses, "LOWER(user_email) IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}

	q := b.client.Query(usageTimelineSQL(b.regionRef(region), truncUnit, strings.Join(clauses, " AND ")))
	q.Parameters = params

	return collectRows[UsageTimepoint](q, ctx)
}

// --- Top frequent callers & IAM Summary ---

type TopCaller struct {
	Email       string  `json:"email" bigquery:"email"`
	TotalCalls  int64   `json:"total_calls" bigquery:"total_calls"`
	TotalSlotMs int64   `json:"total_slot_ms" bigquery:"total_slot_ms"`
	TotalBytes  int64   `json:"total_bytes" bigquery:"total_bytes"`
	AvgDuration float64 `json:"avg_duration_sec" bigquery:"avg_duration_sec"`
	LastActive  string  `json:"last_active" bigquery:"last_active"`
}

type callerStatRow struct {
	Email       string  `bigquery:"email"`
	IsSA        bool    `bigquery:"is_sa"`
	TotalCalls  int64   `bigquery:"total_calls"`
	TotalSlotMs int64   `bigquery:"total_slot_ms"`
	TotalBytes  int64   `bigquery:"total_bytes"`
	AvgDuration float64 `bigquery:"avg_duration_sec"`
	LastActive  string  `bigquery:"last_active"`
}

func callerSummarySQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			user_email AS email,
			ENDS_WITH(user_email, '.gserviceaccount.com') AS is_sa,
			COUNT(*) AS total_calls,
			IFNULL(SUM(total_slot_ms), 0) AS total_slot_ms,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes,
			IFNULL(AVG(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND)) / 1000.0, 0.0) AS avg_duration_sec,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", MAX(creation_time)) AS last_active
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE %s
		GROUP BY email
		ORDER BY total_calls DESC`,
		regionRef, where)
}

func rollupIdentityStats(rows []callerStatRow, limit int) (*IAMSummary, []TopCaller) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	summary := &IAMSummary{TotalEmails: int64(len(rows))}
	top := make([]TopCaller, 0, min(len(rows), limit))
	for _, r := range rows {
		if r.IsSA {
			summary.ServiceAccounts++
		} else {
			summary.HumanUsers++
		}
		summary.TotalCalls += r.TotalCalls
		if len(top) < limit {
			top = append(top, TopCaller{
				Email:       r.Email,
				TotalCalls:  r.TotalCalls,
				TotalSlotMs: r.TotalSlotMs,
				TotalBytes:  r.TotalBytes,
				AvgDuration: r.AvgDuration,
				LastActive:  r.LastActive,
			})
		}
	}
	return summary, top
}

// GetIdentityStats computes both IAMSummary and TopCaller rows from a single
// JOBS_BY_PROJECT scan over the selected window and optional identity filter.
func (b *BQClient) GetIdentityStats(ctx context.Context, region string, emails []string, timeRange string, limit int) (*IAMSummary, []TopCaller, error) {
	interval := timeRangeToInterval(timeRange)
	clauses := []string{
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"user_email IS NOT NULL",
		"IFNULL(statement_type, '') != 'SCRIPT'",
	}
	var params []bigquery.QueryParameter
	if len(emails) > 0 {
		clauses = append(clauses, "LOWER(user_email) IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}
	q := b.client.Query(callerSummarySQL(b.regionRef(region), strings.Join(clauses, " AND ")))
	q.Parameters = params

	rows, err := collectRows[callerStatRow](q, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("identity stats query failed: %w", err)
	}
	s, top := rollupIdentityStats(rows, limit)
	return s, top, nil
}

func (b *BQClient) GetTopCallers(ctx context.Context, region string, emails []string, timeRange string, limit int) ([]TopCaller, error) {
	_, top, err := b.GetIdentityStats(ctx, region, emails, timeRange, limit)
	return top, err
}

// --- Inactive emails & New actors (180-day scan) ---

type InactiveEmail struct {
	Email      string `json:"email" bigquery:"email"`
	LastActive string `json:"last_active" bigquery:"last_active"`
	DaysIdle   int64  `json:"days_idle" bigquery:"days_idle"`
	TotalCalls int64  `json:"total_calls" bigquery:"total_calls"`
}

type longWindowIAMRow struct {
	Email        string `bigquery:"email"`
	IsSA         bool   `bigquery:"is_sa"`
	LastActive   string `bigquery:"last_active"`
	DaysIdle     int64  `bigquery:"days_idle"`
	TotalCalls   int64  `bigquery:"total_calls"`
	FirstSeen90d string `bigquery:"first_seen_90d"`
	PriorActive  string `bigquery:"prior_active"`
	Jobs90d      int64  `bigquery:"jobs_90d"`
	IsNewActor   bool   `bigquery:"is_new_actor"`
}

func longWindowIAMSQL(regionRef string) string {
	return fmt.Sprintf(
		`SELECT
			user_email AS email,
			ENDS_WITH(user_email, '.gserviceaccount.com') AS is_sa,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", MAX(creation_time)) AS last_active,
			TIMESTAMP_DIFF(CURRENT_TIMESTAMP(), MAX(creation_time), DAY) AS days_idle,
			COUNT(*) AS total_calls,
			IFNULL(FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ",
				MIN(IF(creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY), creation_time, NULL))), '') AS first_seen_90d,
			IFNULL(FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ",
				MAX(IF(creation_time < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY), creation_time, NULL))), '') AS prior_active,
			COUNTIF(creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)) AS jobs_90d,
			IFNULL(MIN(IF(creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY), creation_time, NULL)) >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY), FALSE) AS is_new_actor
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 180 DAY)
			AND user_email IS NOT NULL
			AND IFNULL(statement_type, '') != 'SCRIPT'
		GROUP BY email
		HAVING TIMESTAMP_DIFF(CURRENT_TIMESTAMP(), MAX(creation_time), DAY) >= 7
			OR MIN(IF(creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY), creation_time, NULL)) >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)`,
		regionRef)
}

func splitLongWindowIAM(rows []longWindowIAMRow, minIdleDays int64, filterEmails []string) ([]InactiveEmail, []NewActor) {
	var emailSet map[string]bool
	if len(filterEmails) > 0 {
		emailSet = make(map[string]bool, len(filterEmails))
		for _, e := range filterEmails {
			emailSet[strings.ToLower(e)] = true
		}
	}
	var inactive []InactiveEmail
	var newActors []NewActor
	for _, r := range rows {
		if emailSet != nil && !emailSet[strings.ToLower(r.Email)] {
			continue
		}
		if r.DaysIdle >= minIdleDays {
			inactive = append(inactive, InactiveEmail{
				Email:      r.Email,
				LastActive: r.LastActive,
				DaysIdle:   r.DaysIdle,
				TotalCalls: r.TotalCalls,
			})
		}
		if r.IsNewActor && r.FirstSeen90d != "" {
			newActors = append(newActors, NewActor{
				Email:       r.Email,
				FirstSeen:   r.FirstSeen90d,
				PriorActive: r.PriorActive,
				Jobs:        r.Jobs90d,
				IsSA:        r.IsSA,
			})
		}
	}
	sort.Slice(inactive, func(i, j int) bool {
		if inactive[i].DaysIdle != inactive[j].DaysIdle {
			return inactive[i].DaysIdle > inactive[j].DaysIdle
		}
		return inactive[i].Email < inactive[j].Email
	})
	sort.Slice(newActors, func(i, j int) bool {
		if newActors[i].FirstSeen != newActors[j].FirstSeen {
			return newActors[i].FirstSeen > newActors[j].FirstSeen
		}
		return newActors[i].Email < newActors[j].Email
	})
	if len(newActors) > 50 {
		newActors = newActors[:50]
	}
	return inactive, newActors
}

// GetLongWindowIAM scans 180 days of JOBS_BY_PROJECT once and returns raw
// identity rows for both the >=7d inactive buckets and the 90d new actors.
func (b *BQClient) GetLongWindowIAM(ctx context.Context, region string) ([]longWindowIAMRow, error) {
	q := b.client.Query(longWindowIAMSQL(b.regionRef(region)))
	rows, err := collectRows[longWindowIAMRow](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("long-window IAM query failed: %w", err)
	}
	return rows, nil
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
				AND user_email IS NOT NULL
				AND IFNULL(statement_type, '') != 'SCRIPT'
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

// iamSummarySQL counts identities, not jobs: JOBS_BY_PROJECT has one row per
// job, so the human and service-account cards use COUNT(DISTINCT ...) like
// total_emails and add up to it. Any '.iam.gserviceaccount.com' address also
// ends with '.gserviceaccount.com', so one suffix check covers both.
func iamSummarySQL(regionRef, interval string) string {
	return fmt.Sprintf(
		`SELECT
			COUNT(DISTINCT user_email) AS total_emails,
			COUNT(DISTINCT IF(ENDS_WITH(user_email, '.gserviceaccount.com'), user_email, NULL)) AS service_accounts,
			COUNT(DISTINCT IF(NOT ENDS_WITH(user_email, '.gserviceaccount.com'), user_email, NULL)) AS human_users,
			COUNT(*) AS total_calls
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)
			AND user_email IS NOT NULL
			AND IFNULL(statement_type, '') != 'SCRIPT'`,
		regionRef, interval)
}

func (b *BQClient) GetIAMSummary(ctx context.Context, region, timeRange string) (*IAMSummary, error) {
	q := b.client.Query(iamSummarySQL(b.regionRef(region), timeRangeToInterval(timeRange)))

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
	Email       string `json:"email" bigquery:"email"`
	FirstSeen   string `json:"first_seen" bigquery:"first_seen"`
	PriorActive string `json:"prior_active,omitempty" bigquery:"prior_active"`
	Jobs        int64  `json:"jobs" bigquery:"jobs"`
	IsSA        bool   `json:"is_sa" bigquery:"is_sa"`
}

func (b *BQClient) GetNewActors(ctx context.Context, region string) ([]NewActor, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT user_email AS email,
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", MIN(creation_time)) AS first_seen,
			COUNT(*) AS jobs,
			LOGICAL_OR(ENDS_WITH(user_email, '.gserviceaccount.com')) AS is_sa
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)
			AND user_email IS NOT NULL
			AND IFNULL(statement_type, '') != 'SCRIPT'
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

type offHoursGroupingRow struct {
	Dow          int64  `bigquery:"dow"`
	Hr           int64  `bigquery:"hr"`
	Email        string `bigquery:"email"`
	IsCell       int64  `bigquery:"is_cell"`
	Jobs         int64  `bigquery:"jobs"`
	OffHoursJobs int64  `bigquery:"off_hours_jobs"`
}

func offHoursSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			IFNULL(dow, 0) AS dow,
			IFNULL(hr, -1) AS hr,
			IFNULL(email, '') AS email,
			is_cell,
			jobs,
			off_hours_jobs
		FROM (
			SELECT
				dow,
				hr,
				email,
				GROUPING(email) AS is_cell,
				COUNT(*) AS jobs,
				COUNTIF(hr NOT BETWEEN 8 AND 19 OR dow IN (1, 7)) AS off_hours_jobs
			FROM (
				SELECT
					EXTRACT(DAYOFWEEK FROM creation_time) AS dow,
					EXTRACT(HOUR FROM creation_time) AS hr,
					user_email AS email
				FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
				WHERE %s
			)
			GROUP BY GROUPING SETS ((dow, hr), (email))
		)`,
		regionRef, where)
}

func splitOffHoursRows(rows []offHoursGroupingRow) ([]OffHoursCell, []OffHoursUser) {
	var cells []OffHoursCell
	var top []OffHoursUser
	for _, r := range rows {
		if r.IsCell == 1 {
			cells = append(cells, OffHoursCell{Dow: r.Dow, Hr: r.Hr, Jobs: r.Jobs})
			continue
		}
		if r.Email != "" && r.OffHoursJobs > 0 {
			top = append(top, OffHoursUser{Email: r.Email, Jobs: r.OffHoursJobs})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Jobs != top[j].Jobs {
			return top[i].Jobs > top[j].Jobs
		}
		return top[i].Email < top[j].Email
	})
	if len(top) > 10 {
		top = top[:10]
	}
	return cells, top
}

func (b *BQClient) GetOffHours(ctx context.Context, region string, emails []string, timeRange string) ([]OffHoursCell, []OffHoursUser, error) {
	interval := timeRangeToInterval(timeRange)
	var clauses []string
	var params []bigquery.QueryParameter
	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"user_email IS NOT NULL",
		"NOT ENDS_WITH(user_email, '.gserviceaccount.com')",
		"IFNULL(statement_type, '') != 'SCRIPT'")
	if len(emails) > 0 {
		clauses = append(clauses, "LOWER(user_email) IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}
	where := strings.Join(clauses, " AND ")

	q := b.client.Query(offHoursSQL(b.regionRef(region), where))
	q.Parameters = params
	rows, err := collectRows[offHoursGroupingRow](q, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("off-hours query failed: %w", err)
	}
	cells, top := splitOffHoursRows(rows)
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

func exfilSignalsSQL(regionRef, where string) string {
	return fmt.Sprintf(
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
		QUALIFY ROW_NUMBER() OVER (PARTITION BY signal ORDER BY IFNULL(total_bytes_processed, 0) DESC, creation_time DESC) <= 25
		ORDER BY CASE signal
			WHEN 'EXTRACT_TO_GCS' THEN 1
			WHEN 'EXPORT_DATA' THEN 2
			WHEN 'CROSS_PROJECT_WRITE' THEN 3
			ELSE 4
		END, bytes DESC`,
		regionRef, where)
}

func (b *BQClient) GetExfilSignals(ctx context.Context, region string, emails []string, timeRange string) ([]ExfilSignal, error) {
	interval := timeRangeToInterval(timeRange)
	var clauses []string
	params := []bigquery.QueryParameter{
		{Name: "project", Value: b.config.BigQuery.ProjectID},
	}
	clauses = append(clauses,
		fmt.Sprintf("creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", interval),
		"user_email IS NOT NULL",
		"IFNULL(statement_type, '') != 'SCRIPT'")
	if len(emails) > 0 {
		clauses = append(clauses, "LOWER(user_email) IN UNNEST(@emails)")
		params = append(params, bigquery.QueryParameter{Name: "emails", Value: emails})
	}

	q := b.client.Query(exfilSignalsSQL(b.regionRef(region), strings.Join(clauses, " AND ")))
	q.Parameters = params
	return collectRows[ExfilSignal](q, ctx)
}
