package main

import (
	"fmt"
	"net/http"
	"strings"

	"cloud.google.com/go/bigquery"
)

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
	clauses, params := f.jobsClauses(timeCol)

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

// TimelineWhere builds the WHERE for JOBS_TIMELINE_BY_PROJECT, which lacks
// the cache_hit and error_result columns, so those filters are dropped.
func (f QueryFilters) TimelineWhere(timeCol string) (string, []bigquery.QueryParameter) {
	clauses, params := f.jobsClauses(timeCol)
	return " WHERE " + strings.Join(clauses, " AND "), params
}

// jobsClauses holds the filters shared by JOBS_BY_PROJECT and
// JOBS_TIMELINE_BY_PROJECT.
func (f QueryFilters) jobsClauses(timeCol string) ([]string, []bigquery.QueryParameter) {
	clauses := []string{
		fmt.Sprintf("%s >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", timeCol, f.TimeInterval()),
	}
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
