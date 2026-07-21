package main

import (
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
