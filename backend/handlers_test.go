package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The 30/90-day buckets must be exact subsets of the >=7-day query result so
// that replacing three identical 180-day scans with one doesn't change what
// each dashboard table shows.
func TestBucketInactiveEmails(t *testing.T) {
	inactive := []InactiveEmail{
		{Email: "ancient@x.com", DaysIdle: 200},
		{Email: "old@x.com", DaysIdle: 90},
		{Email: "stale@x.com", DaysIdle: 30},
		{Email: "recent@x.com", DaysIdle: 8},
	}

	i30, i90 := bucketInactiveEmails(inactive)

	wantI30 := []string{"ancient@x.com", "old@x.com", "stale@x.com"}
	if len(i30) != len(wantI30) {
		t.Fatalf("i30 = %d entries, want %d", len(i30), len(wantI30))
	}
	for i, e := range i30 {
		if e.Email != wantI30[i] {
			t.Errorf("i30[%d] = %q, want %q", i, e.Email, wantI30[i])
		}
	}

	wantI90 := []string{"ancient@x.com", "old@x.com"}
	if len(i90) != len(wantI90) {
		t.Fatalf("i90 = %d entries, want %d", len(i90), len(wantI90))
	}
	for i, e := range i90 {
		if e.Email != wantI90[i] {
			t.Errorf("i90[%d] = %q, want %q", i, e.Email, wantI90[i])
		}
	}
}

func TestSplitTrimmed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain list", "a,b", []string{"a", "b"}},
		{"trims spaces and tabs", " a ,\tb\t", []string{"a", "b"}},
		{"trims newlines and carriage returns", "a\r\n,\nb", []string{"a", "b"}},
		{"drops empty parts", ",a,, ,b,", []string{"a", "b"}},
		{"empty input", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitTrimmed(tt.in, ",")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitTrimmed(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDegradedWidgetsJSONMarshaling(t *testing.T) {
	data := StorageDashboardData{
		DegradedWidgets: []string{"cold_tables"},
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if !strings.Contains(string(b), `"degraded_widgets":["cold_tables"]`) {
		t.Errorf("json output = %s, missing degraded_widgets", string(b))
	}
}

func TestStorageDashboardRejectsInvalidDataset(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboards/storage?dataset=foo%60bar", nil)
	rec := httptest.NewRecorder()
	h.StorageDashboard(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSearchIndexesCacheKeyIgnoresTimeRangeAndUser(t *testing.T) {
	k1 := searchIndexesCacheKey(QueryFilters{Region: "region-us", Dataset: "ds1", Table: "t1", TimeRange: "7d", UserEmail: "a@x.com"})
	k2 := searchIndexesCacheKey(QueryFilters{Region: "region-us", Dataset: "ds1", Table: "t1", TimeRange: "90d", UserEmail: "b@x.com"})
	if k1 != k2 {
		t.Errorf("cache keys differ across time_range/user_email: %q vs %q", k1, k2)
	}
}

func TestParseEmailsNormalizesAndSorts(t *testing.T) {
	r1 := httptest.NewRequest(http.MethodGet, "/api/iam?emails=Bob@Example.com,alice@example.com,bob@example.com", nil)
	r2 := httptest.NewRequest(http.MethodGet, "/api/iam?emails=alice@example.com,bob@example.com", nil)
	e1 := parseEmails(r1)
	e2 := parseEmails(r2)
	want := []string{"alice@example.com", "bob@example.com"}
	if !reflect.DeepEqual(e1, want) || !reflect.DeepEqual(e2, want) {
		t.Errorf("parseEmails = %v / %v, want %v", e1, e2, want)
	}
	if iamCacheKey("us", "7d", "UTC", e1) != iamCacheKey("us", "7d", "UTC", e2) {
		t.Errorf("iamCacheKey differs for reordered/duplicate emails")
	}
	if iamCacheKey("us", "7d", "UTC", e1) == iamCacheKey("us", "7d", "Asia/Shanghai", e1) {
		t.Errorf("iamCacheKey should distinguish timezones")
	}
}

