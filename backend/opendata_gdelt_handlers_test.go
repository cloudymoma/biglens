package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

// Every accepted GDELT request costs a partition scan, so malformed or
// oversized ranges must be rejected before any BigQuery call. bq is nil in
// these tests: reaching a query would panic, proving validation
// short-circuits.
func TestGdeltRangeValidation(t *testing.T) {
	today := civil.DateOf(time.Now().UTC())
	tomorrow := today.AddDays(1)
	eventsTooWide := today.AddDays(-maxGdeltEventsDays) // span = 91 days
	gkgTooWide := today.AddDays(-maxGdeltGkgDays)       // span = 31 days

	h := &APIHandler{cache: NewCache(time.Minute)}
	endpoints := map[string]http.HandlerFunc{
		"events":  h.GdeltEvents,
		"gkg":     h.GdeltGkg,
		"dyads":   h.GdeltDyads,
		"country": h.GdeltCountry, // range is validated before the country param
		"impact":  h.GdeltImpact,
		"stories": h.GdeltStories,
	}

	shared := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing start_date", fmt.Sprintf("end_date=%s", today), "start_date"},
		{"missing end_date", fmt.Sprintf("start_date=%s", today), "end_date"},
		{"malformed start_date", fmt.Sprintf("start_date=07/19/2026&end_date=%s", today), "start_date"},
		{"start after end", fmt.Sprintf("start_date=%s&end_date=%s", today, today.AddDays(-1)), "before end_date"},
		{"end in the future", fmt.Sprintf("start_date=%s&end_date=%s", today, tomorrow), "future"},
	}

	for name, handler := range endpoints {
		for _, tt := range shared {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/opendata/gdelt/"+name+"?"+tt.query, nil)
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

	// Span caps differ per endpoint: events 90 days, gkg 30 days.
	spanCases := []struct {
		endpoint string
		handler  http.HandlerFunc
		start    civil.Date
	}{
		{"events", h.GdeltEvents, eventsTooWide},
		{"gkg", h.GdeltGkg, gkgTooWide},
		{"dyads", h.GdeltDyads, eventsTooWide},
		{"impact", h.GdeltImpact, gkgTooWide},
		{"stories", h.GdeltStories, today.AddDays(-maxGdeltStoriesDays)},
	}
	for _, tt := range spanCases {
		t.Run(tt.endpoint+"/span too large", func(t *testing.T) {
			url := fmt.Sprintf("/api/opendata/gdelt/%s?start_date=%s&end_date=%s", tt.endpoint, tt.start, today)
			r := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			tt.handler(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), "days allowed") {
				t.Errorf("error %q does not mention the span cap", w.Body.String())
			}
		})
	}

	// A span exactly at the gkg cap passes validation for gkg. bq is nil, so
	// the handler panics only after validation succeeds — recover proves the
	// boundary is accepted rather than rejected with 400.
	t.Run("gkg/span at cap passes validation", func(t *testing.T) {
		start := today.AddDays(-(maxGdeltGkgDays - 1)) // span = 30 days
		r := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/opendata/gdelt/gkg?start_date=%s&end_date=%s", start, today), nil)
		if _, _, err := parseGdeltRange(r, maxGdeltGkgDays); err != nil {
			t.Errorf("span of exactly %d days rejected: %v", maxGdeltGkgDays, err)
		}
	})
}

// The country drill-down interpolates nothing into SQL — the code is bound
// as a query parameter — but malformed codes must still 400 before any
// BigQuery call (bq is nil: reaching a query would panic).
func TestGdeltCountryValidation(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	today := civil.DateOf(time.Now().UTC())

	for _, country := range []string{"", "US", "usa", "USAX", "U.S"} {
		t.Run(fmt.Sprintf("country=%q", country), func(t *testing.T) {
			url := fmt.Sprintf("/api/opendata/gdelt/country?start_date=%s&end_date=%s&country=%s", today, today, country)
			r := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			h.GdeltCountry(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
			if !strings.Contains(w.Body.String(), "country") {
				t.Errorf("error %q does not mention the country param", w.Body.String())
			}
		})
	}
}

// The dashboard's headline metrics must weight group averages by event
// count — a plain mean of group averages lets tiny groups distort global
// tone, which is the statistical bug this rollup exists to prevent.
func TestRollupGdeltSummaryWeighted(t *testing.T) {
	rows := []GdeltSummaryRow{
		{IngestDate: "2026-07-20", QuadClass: 1, EventRootCode: "01", EventCount: 300, AvgGoldstein: 2, AvgTone: 2},
		{IngestDate: "2026-07-20", QuadClass: 4, EventRootCode: "19", EventCount: 100, AvgGoldstein: -8, AvgTone: -2},
		{IngestDate: "2026-07-21", QuadClass: 4, EventRootCode: "19", EventCount: 100, AvgGoldstein: -8, AvgTone: -6},
	}

	overall, daily, quads, types := rollupGdeltSummary(rows)

	// Weighted tone: (2*300 - 2*100 - 6*100) / 500 = -0.4.
	// An unweighted mean of group averages would give -2.0.
	if overall.EventCount != 500 {
		t.Errorf("overall.EventCount = %d, want 500", overall.EventCount)
	}
	if overall.AvgTone != -0.4 {
		t.Errorf("overall.AvgTone = %v, want -0.4 (weighted)", overall.AvgTone)
	}
	if overall.AvgGoldstein != -2 { // (2*300 - 8*200) / 500
		t.Errorf("overall.AvgGoldstein = %v, want -2", overall.AvgGoldstein)
	}

	if len(daily) != 2 || daily[0].IngestDate != "2026-07-20" || daily[1].IngestDate != "2026-07-21" {
		t.Fatalf("daily = %+v, want 2 ascending dates", daily)
	}
	if daily[0].EventCount != 400 || daily[0].AvgTone != 1 { // (2*300 - 2*100)/400
		t.Errorf("daily[0] = %+v, want count 400 tone 1", daily[0])
	}

	if len(quads) != 2 || quads[0].QuadClass != 1 || quads[1].EventCount != 200 {
		t.Errorf("quads = %+v, want classes [1,4] with counts [300,200]", quads)
	}

	// event_types sorted by count desc; "19" tone = (-2*100 - 6*100)/200 = -4.
	if len(types) != 2 || types[0].EventRootCode != "01" {
		t.Fatalf("types = %+v, want '01' first by count", types)
	}
	if types[1].AvgTone != -4 || types[1].AvgGoldstein != -8 {
		t.Errorf("types[1] = %+v, want weighted tone -4, goldstein -8", types[1])
	}
}

// A cache hit must serve without touching BigQuery: bq is nil, so any query
// attempt would panic.
func TestGdeltEventsCacheHit(t *testing.T) {
	h := &APIHandler{cache: NewCache(time.Minute)}
	today := civil.DateOf(time.Now().UTC())
	key := fmt.Sprintf("opendata:gdelt:events:%s:%s", today, today)
	h.cache.Set(key, &GdeltEventsData{Overall: GdeltOverall{EventCount: 42}})

	url := fmt.Sprintf("/api/opendata/gdelt/events?start_date=%s&end_date=%s", today, today)
	r := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()

	h.GdeltEvents(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), `"event_count":42`) {
		t.Errorf("body %q does not contain cached payload", w.Body.String())
	}
}
