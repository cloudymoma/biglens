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
	return QueryFilters{
		Region:    region,
		Dataset:   q.Get("dataset"),
		Table:     q.Get("table"),
		UserEmail: q.Get("user_email"),
		TimeRange: tr,
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
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s", prefix, f.Region, f.Dataset, f.Table, f.UserEmail, f.TimeRange)
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
	clauses := []string{
		fmt.Sprintf("%s >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)", timeCol, f.TimeInterval()),
	}
	var params []bigquery.QueryParameter

	if f.UserEmail != "" {
		clauses = append(clauses, "user_email = @user_email")
		params = append(params, bigquery.QueryParameter{Name: "user_email", Value: f.UserEmail})
	}

	return " WHERE " + strings.Join(clauses, " AND "), params
}
