package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Each job-oriented filter must translate into exactly one WHERE clause with
// parameterized values, so dashboards stay injection-safe and cacheable.
func TestJobsWhereNewFilters(t *testing.T) {
	tests := []struct {
		name       string
		f          QueryFilters
		wantClause string
		wantParams int
	}{
		{"job type", QueryFilters{TimeRange: "7d", JobType: "QUERY"}, "job_type = @job_type", 1},
		{"failed status", QueryFilters{TimeRange: "7d", Status: "failed"}, "error_result IS NOT NULL", 0},
		{"success status", QueryFilters{TimeRange: "7d", Status: "success"}, "error_result IS NULL", 0},
		{"cache hit", QueryFilters{TimeRange: "7d", CacheHit: "hit"}, "cache_hit = TRUE", 0},
		{"cache miss", QueryFilters{TimeRange: "7d", CacheHit: "miss"}, "(cache_hit IS NULL OR cache_hit = FALSE)", 0},
		{"on-demand billing", QueryFilters{TimeRange: "7d", Billing: "ondemand"}, "reservation_id IS NULL", 0},
		{"reservation billing", QueryFilters{TimeRange: "7d", Billing: "reservation"}, "reservation_id IS NOT NULL", 0},
		{"service accounts", QueryFilters{TimeRange: "7d", Principal: "sa"}, "user_email LIKE '%gserviceaccount%'", 0},
		{"humans", QueryFilters{TimeRange: "7d", Principal: "human"}, "user_email NOT LIKE '%gserviceaccount%'", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, params := tt.f.JobsWhere("creation_time")
			if !strings.Contains(where, tt.wantClause) {
				t.Errorf("JobsWhere() = %q, want clause %q", where, tt.wantClause)
			}
			if len(params) != tt.wantParams {
				t.Errorf("JobsWhere() params = %d, want %d", len(params), tt.wantParams)
			}
		})
	}
}

// The timeline view (JOBS_TIMELINE_BY_PROJECT) has no cache_hit or
// error_result columns, so TimelineWhere must silently drop those filters
// instead of generating SQL that fails.
func TestTimelineWhereSubset(t *testing.T) {
	f := QueryFilters{
		TimeRange: "7d", JobType: "QUERY", Status: "failed",
		CacheHit: "hit", Billing: "reservation", Principal: "sa", UserEmail: "a@b.com",
	}
	where, _ := f.TimelineWhere("period_start")

	for _, want := range []string{"job_type = @job_type", "reservation_id IS NOT NULL", "user_email LIKE '%gserviceaccount%'", "user_email = @user_email", "period_start >="} {
		if !strings.Contains(where, want) {
			t.Errorf("TimelineWhere() = %q, missing %q", where, want)
		}
	}
	for _, reject := range []string{"error_result", "cache_hit"} {
		if strings.Contains(where, reject) {
			t.Errorf("TimelineWhere() = %q, must not contain %q", where, reject)
		}
	}
}

func TestParseFiltersNewParams(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/x?job_type=LOAD&status=failed&cache_hit=miss&billing=ondemand&principal=human&group_by=dataset", nil)
	f := ParseFilters(r)

	if f.JobType != "LOAD" || f.Status != "failed" || f.CacheHit != "miss" ||
		f.Billing != "ondemand" || f.Principal != "human" || f.GroupBy != "dataset" {
		t.Errorf("ParseFilters() = %+v, want new fields populated", f)
	}
}

func TestParseFiltersGroupByDefault(t *testing.T) {
	f := ParseFilters(httptest.NewRequest("GET", "/x", nil))
	if f.GroupBy != "user" {
		t.Errorf("GroupBy default = %q, want %q", f.GroupBy, "user")
	}
}

// Two filter sets differing only in a new field must produce distinct cache
// keys, or dashboards would serve stale cross-filter results.
func TestCacheKeyIncludesNewFilters(t *testing.T) {
	base := QueryFilters{Region: "us", TimeRange: "7d"}
	variants := []QueryFilters{
		{Region: "us", TimeRange: "7d", JobType: "QUERY"},
		{Region: "us", TimeRange: "7d", Status: "failed"},
		{Region: "us", TimeRange: "7d", CacheHit: "hit"},
		{Region: "us", TimeRange: "7d", Billing: "ondemand"},
		{Region: "us", TimeRange: "7d", Principal: "sa"},
		{Region: "us", TimeRange: "7d", GroupBy: "dataset"},
	}
	seen := map[string]bool{base.CacheKey("p"): true}
	for i, v := range variants {
		k := v.CacheKey("p")
		if seen[k] {
			t.Errorf("variant %d cache key %q collides", i, k)
		}
		seen[k] = true
	}
}
