package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The trends endpoints must reject malformed requests before any BigQuery
// call is issued — every accepted request costs a partition scan, so the
// validation layer is the cost/injection guard the spec mandates.
func TestTrendsDashboardValidation(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"missing refresh_date", "country_code=JP"},
		{"malformed refresh_date", "refresh_date=07/19/2026&country_code=JP"},
		{"missing country_code", "refresh_date=2026-07-19"},
	}

	// bq is nil: reaching a query would panic, proving validation short-circuits.
	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/trends/dashboard?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.TrendsDashboard(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestTrendsTermValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing refresh_date", "term=ai", "refresh_date"},
		{"neither term nor terms", "refresh_date=2026-07-19", "term or terms"},
		{"too many compare terms", "refresh_date=2026-07-19&country_code=JP&terms=a,b,c,d,e,f", "at most 5"},
		{"terms without country", "refresh_date=2026-07-19&terms=ai,crypto", "country_code"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/trends/term?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.TrendsTerm(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

// In the US view the Trends geo chart lists DMAs: names arrive in
// country_name with plain numeric scores (the chart's contract, unaffected by
// the SEM W2 geo query's NULL-preserving rows).
func TestTrendsTermUSGeoListsDMAs(t *testing.T) {
	fake := &fakeBigQuery{
		columns: []fakeBQColumn{{"geo", "STRING"}, {"score", "INTEGER"}, {"rising_rank", "INTEGER"}, {"percent_gain", "INTEGER"}},
		rows:    [][]any{{"New York NY", "28", "0", "0"}, {"Glendive MT", "0", "0", "0"}},
	}
	h := newFakeBQHandler(t, fake)
	w := httptest.NewRecorder()
	h.TrendsTerm(w, httptest.NewRequest(http.MethodGet,
		"/api/opendata/trends/term?refresh_date=2026-10-04&country_code=US&term=usury", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d (%s), want 200", w.Code, w.Body.String())
	}

	var got TrendsTermData
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	want := []TrendsGeoPoint{{CountryName: "New York NY", Score: 28}, {CountryName: "Glendive MT", Score: 0}}
	if len(got.Geo) != len(want) {
		t.Fatalf("got %d geo points, want %d: %s", len(got.Geo), len(want), w.Body.String())
	}
	for i := range want {
		if got.Geo[i] != want[i] {
			t.Errorf("geo[%d] = %+v, want %+v", i, got.Geo[i], want[i])
		}
	}
}
