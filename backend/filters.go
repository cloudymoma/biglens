package main

import (
	"fmt"
	"net/http"
	"strings"

	"cloud.google.com/go/bigquery"
)

// validRegions is populated from bqRegions in init() to prevent injection.
var validRegions = map[string]bool{}

func init() {
	for _, r := range bqRegions {
		validRegions[r] = true
	}
}

// validateRegion returns the region if it's in the whitelist, else "us".
func validateRegion(region string) string {
	if validRegions[region] {
		return region
	}
	return "us"
}

type QueryFilters struct {
	Region    string
	Dataset   string
	Table     string
	UserEmail string
	TimeRange string
	JobType   string // QUERY | LOAD | EXTRACT | COPY
	Status    string // success | failed
	CacheHit  string // hit | miss
	Billing   string // ondemand | reservation
	Principal string // human | sa
	GroupBy   string // user | dataset | table | reservation
}

func ParseFilters(r *http.Request) QueryFilters {
	q := r.URL.Query()
	tr := q.Get("time_range")
	if tr == "" {
		tr = "7d"
	}
	region := q.Get("region")
	if region == "" {
		region = "us"
	}
	region = validateRegion(region)
	groupBy := q.Get("group_by")
	switch groupBy {
	case "dataset", "table", "reservation":
	default:
		groupBy = "user"
	}
	return QueryFilters{
		Region:    region,
		Dataset:   q.Get("dataset"),
		Table:     q.Get("table"),
		UserEmail: q.Get("user_email"),
		TimeRange: tr,
		JobType:   strings.ToUpper(q.Get("job_type")),
		Status:    q.Get("status"),
		CacheHit:  q.Get("cache_hit"),
		Billing:   q.Get("billing"),
		Principal: q.Get("principal"),
		GroupBy:   groupBy,
	}
}

func (f QueryFilters) TimeInterval() string {
	switch f.TimeRange {
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

// TimelineBucketSeconds is the slot-timeline bucket width for the time range.
// It comes only from this whitelist, never from request input, and keeps
// every range under ~1,500 points: 1d/1 min = 1,440, 7d/10 min = 1,008,
// 30d/1 h = 720, 90d/2 h = 1,080. The default mirrors TimeInterval's.
func (f QueryFilters) TimelineBucketSeconds() int64 {
	switch f.TimeRange {
	case "1d":
		return 60
	case "30d":
		return 3600
	case "90d":
		return 7200
	default:
		return 600
	}
}

func (f QueryFilters) CacheKey(prefix string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s:%s:%s:%s:%s:%s:%s", prefix, f.Region, f.Dataset, f.Table,
		f.UserEmail, f.TimeRange, f.JobType, f.Status, f.CacheHit, f.Billing, f.Principal, f.GroupBy)
}

func (f QueryFilters) StorageWhere() (string, []bigquery.QueryParameter) {
	var clauses []string
	var params []bigquery.QueryParameter

	if f.Dataset != "" {
		clauses = append(clauses, "table_schema = @dataset")
		params = append(params, bigquery.QueryParameter{Name: "dataset", Value: f.Dataset})
	}
	if f.Table != "" {
		clauses = append(clauses, "table_name = @table_name")
		params = append(params, bigquery.QueryParameter{Name: "table_name", Value: f.Table})
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), params
}

func (f QueryFilters) JobsWhere(timeCol string) (string, []bigquery.QueryParameter) {
	clauses, params := f.jobsClauses(
		fmt.Sprintf("%s >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", timeCol, f.TimeInterval()))

	switch f.Status {
	case "success":
		clauses = append(clauses, "error_result IS NULL")
	case "failed":
		clauses = append(clauses, "error_result IS NOT NULL")
	}
	switch f.CacheHit {
	case "hit":
		clauses = append(clauses, "cache_hit = TRUE")
	case "miss":
		clauses = append(clauses, "(cache_hit IS NULL OR cache_hit = FALSE)")
	}

	return " WHERE " + strings.Join(clauses, " AND "), params
}

// timelineWindowEnd is where the current, still-filling slot bucket starts:
// now rounded down to a whole multiple of @bucket_secs.
const timelineWindowEnd = "TIMESTAMP_SECONDS(DIV(UNIX_SECONDS(CURRENT_TIMESTAMP()), @bucket_secs) * @bucket_secs)"

// timelineCreationMarginHours is how long before the window start a job may
// have been created and still be PENDING or RUNNING inside the window.
// JOBS_TIMELINE_BY_PROJECT "is partitioned by the job_creation_time column"
// (https://cloud.google.com/bigquery/docs/information-schema-jobs-timeline),
// so only a bound on that column lets BigQuery skip partitions instead of
// scanning the view's whole 180-day retention; period_start stays the exact
// window filter.
//
// 24h covers the default worst case for a query job: up to 6h waiting in the
// interactive queue (https://cloud.google.com/bigquery/docs/query-queues) and
// then up to three 6h execution attempts, "a total runtime of ... up to 18
// hours" (https://cloud.google.com/bigquery/quotas). That is also the 24h
// cumulative limit of a multi-statement query, well above the 6h load-job
// limit, and wider than the 1200-minute margin of Google's "match
// administrative resource charts" JOBS_TIMELINE example. Jobs that can live
// longer (batch queries queued for hours, CREATE MODEL at 24-48h, continuous
// queries running for days) drop out of the chart when they were created
// more than 24h before the window start.
const timelineCreationMarginHours = 24

// TimelineWhere builds the WHERE for JOBS_TIMELINE_BY_PROJECT, which lacks
// the cache_hit and error_result columns, so those filters are dropped.
//
// timeCol is bounded to whole buckets of TimelineBucketSeconds (bound as
// @bucket_secs in the returned params): the window ends where the current,
// still-filling bucket starts and begins one time range earlier, so every
// bucket average covers a complete bucket. job_creation_time, the view's
// partitioning column, is bounded timelineCreationMarginHours before that.
func (f QueryFilters) TimelineWhere(timeCol string) (string, []bigquery.QueryParameter) {
	start := fmt.Sprintf("TIMESTAMP_SUB(%s, INTERVAL %s)", timelineWindowEnd, f.TimeInterval())
	clauses, params := f.jobsClauses(
		fmt.Sprintf("%s >= %s", timeCol, start),
		fmt.Sprintf("%s < %s", timeCol, timelineWindowEnd),
		fmt.Sprintf("job_creation_time >= TIMESTAMP_SUB(%s, INTERVAL %d HOUR)", start, timelineCreationMarginHours),
	)
	params = append(params, bigquery.QueryParameter{Name: "bucket_secs", Value: f.TimelineBucketSeconds()})
	return " WHERE " + strings.Join(clauses, " AND "), params
}

// jobsClauses appends the filters shared by JOBS_BY_PROJECT and
// JOBS_TIMELINE_BY_PROJECT to the caller's time-window clauses.
func (f QueryFilters) jobsClauses(window ...string) ([]string, []bigquery.QueryParameter) {
	clauses := window
	var params []bigquery.QueryParameter

	if f.UserEmail != "" {
		clauses = append(clauses, "user_email = @user_email")
		params = append(params, bigquery.QueryParameter{Name: "user_email", Value: f.UserEmail})
	}
	if f.JobType != "" {
		clauses = append(clauses, "job_type = @job_type")
		params = append(params, bigquery.QueryParameter{Name: "job_type", Value: f.JobType})
	}
	switch f.Billing {
	case "ondemand":
		clauses = append(clauses, "reservation_id IS NULL")
	case "reservation":
		clauses = append(clauses, "reservation_id IS NOT NULL")
	}
	switch f.Principal {
	case "sa":
		clauses = append(clauses, "user_email LIKE '%gserviceaccount%'")
	case "human":
		clauses = append(clauses, "user_email NOT LIKE '%gserviceaccount%'")
	}
	return clauses, params
}
