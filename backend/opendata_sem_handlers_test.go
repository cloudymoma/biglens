package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The SEM endpoints must reject malformed requests before any BigQuery call
// is issued — every accepted request costs a partition scan, so validation is
// the cost guard. bq is nil: reaching a query would panic, proving the
// validation short-circuits.
func TestSemMetaValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "", "market"},
		{"unknown market", "market=emea", "market"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/meta?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemMeta(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

func TestSemDashboardValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "refresh_date=2026-08-16&geo=US", "market"},
		{"unknown market", "market=apac&refresh_date=2026-08-16&geo=US", "market"},
		{"missing refresh_date", "market=us", "refresh_date"},
		{"malformed refresh_date", "market=us&refresh_date=08/16/2026", "refresh_date"},
		{"global without geo", "market=global&refresh_date=2026-08-16", "geo"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/dashboard?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemDashboard(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}

// The geo and term endpoints share parseSemTermSelection; both must reject a
// request missing any of market / refresh_date / geo-for-global / term.
func TestSemTermSelectionValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "refresh_date=2026-08-16&geo=US&term=x", "market"},
		{"unknown market", "market=apac&refresh_date=2026-08-16&geo=US&term=x", "market"},
		{"missing refresh_date", "market=us&term=x", "refresh_date"},
		{"malformed refresh_date", "market=us&refresh_date=08/16/2026&term=x", "refresh_date"},
		{"global without geo", "market=global&refresh_date=2026-08-16&term=x", "geo"},
		{"missing term", "market=us&refresh_date=2026-08-16", "term"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}
	handlers := map[string]http.HandlerFunc{
		"geo":  h.SemGeo,
		"term": h.SemTerm,
	}

	for endpoint, handler := range handlers {
		for _, tt := range tests {
			t.Run(endpoint+"/"+tt.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/"+endpoint+"?"+tt.query, nil)
				w := httptest.NewRecorder()

				handler(w, r)

				if w.Code != http.StatusBadRequest {
					t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
				}
				if !strings.Contains(w.Body.String(), tt.wantMsg) {
					t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
				}
			})
		}
	}
}

func TestSemSafetyValidation(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing market", "", "market"},
		{"unknown market", "market=emea", "market"},
		{"global without geo", "market=global", "geo"},
		// Trends countries without a GDELT actor-code mapping must be
		// rejected up front, not passed through as a silent empty result.
		{"unmapped country", "market=global&geo=ZZ", "mapping"},
	}

	h := &APIHandler{cache: NewCache(time.Minute)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/opendata/sem/safety?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.SemSafety(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", w.Body.String(), tt.wantMsg)
			}
		})
	}
}
