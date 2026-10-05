package main

// HTTP handlers for the GDELT Open Data dashboard. Two endpoints so the fast
// event panels render without waiting for the heavier GKG scans:
//
//	GET /api/opendata/gdelt/events?start_date=...&end_date=...   (Overview panels 1+2)
//	GET /api/opendata/gdelt/gkg?start_date=...&end_date=...      (Overview panel 3)
//	GET /api/opendata/gdelt/dyads?start_date=...&end_date=...    (Country & Relations board)
//	GET /api/opendata/gdelt/country?...&country=USA              (Country & Relations drill-down)
//	GET /api/opendata/gdelt/industry?...&industry=finance        (Industry Pulse)

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/singleflight"
)

// Span caps keep worst-case scans bounded (GKG scans ~10x the events table
// per day); requests beyond them are rejected before any BigQuery call.
const (
	maxGdeltEventsDays = 90
	maxGdeltGkgDays    = 30
	// Story velocity is a short-window concept; the mentions stream is also
	// ~3x the events table per day.
	maxGdeltStoriesDays = 14

	gdeltQueryTimeout  = 2 * time.Minute
	gdeltHistoricalTTL = 24 * time.Hour
	gdeltLiveTTL       = 10 * time.Minute
)

// gdeltFlight collapses concurrent identical queries the instant a cache
// entry expires, so one BigQuery round-trip serves all waiters.
var gdeltFlight singleflight.Group

// gdeltCacheTTL caches completed historical UTC windows for 24h (past
// _PARTITIONDATE partitions are immutable) while keeping windows ending today
// at 10m so 15-minute GDELT updates stay fresh.
func gdeltCacheTTL(end civil.Date) time.Duration {
	if end.Before(civil.DateOf(time.Now().UTC())) {
		return gdeltHistoricalTTL
	}
	return gdeltLiveTTL
}

// gdeltDetachedCtx detaches the singleflight work from the first caller's
// request cancellation so a single aborted tab switch does not cancel a paid
// BigQuery scan for other waiters or skip populating the cache.
func gdeltDetachedCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), gdeltQueryTimeout)
}

// parseGdeltRange validates start_date/end_date. _PARTITIONDATE is a UTC
// date, so "today" is evaluated in UTC.
func parseGdeltRange(r *http.Request, maxDays int) (start, end civil.Date, err error) {
	q := r.URL.Query()
	start, err = civil.ParseDate(q.Get("start_date"))
	if err != nil {
		return start, end, fmt.Errorf("invalid start_date %q: expected YYYY-MM-DD", q.Get("start_date"))
	}
	end, err = civil.ParseDate(q.Get("end_date"))
	if err != nil {
		return start, end, fmt.Errorf("invalid end_date %q: expected YYYY-MM-DD", q.Get("end_date"))
	}
	if start.After(end) {
		return start, end, fmt.Errorf("start_date must be on or before end_date")
	}
	if today := civil.DateOf(time.Now().UTC()); end.After(today) {
		return start, end, fmt.Errorf("end_date must not be in the future (UTC)")
	}
	if span := end.DaysSince(start) + 1; span > maxDays {
		return start, end, fmt.Errorf("date range spans %d days; at most %d days allowed", span, maxDays)
	}
	return start, end, nil
}

// --- /events ---

type GdeltOverall struct {
	EventCount   int64   `json:"event_count"`
	AvgTone      float64 `json:"avg_tone"`
	AvgGoldstein float64 `json:"avg_goldstein"`
}

type GdeltDaily struct {
	IngestDate string  `json:"ingest_date"`
	EventCount int64   `json:"event_count"`
	AvgTone    float64 `json:"avg_tone"`
}

type GdeltQuadClass struct {
	QuadClass  int64 `json:"quad_class"`
	EventCount int64 `json:"event_count"`
}

type GdeltEventType struct {
	EventRootCode string  `json:"event_root_code"`
	EventCount    int64   `json:"event_count"`
	AvgGoldstein  float64 `json:"avg_goldstein"`
	AvgTone       float64 `json:"avg_tone"`
}

type GdeltEventsData struct {
	Overall      GdeltOverall     `json:"overall"`
	Daily        []GdeltDaily     `json:"daily"`
	QuadClass    []GdeltQuadClass `json:"quad_class"`
	EventTypes   []GdeltEventType `json:"event_types"`
	Hotspots     []GdeltHotspot   `json:"hotspots"`
	ConflictNews []GdeltNews      `json:"conflict_news"`
}

func (h *APIHandler) GdeltEvents(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltEventsDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:events:%s:%s", start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		summary, hotspots, news, err := h.bq.GetGdeltEventsConsolidated(ctx, start, end)
		if err != nil {
			return nil, err
		}
		data := GdeltEventsData{
			Hotspots:     hotspots,
			ConflictNews: news,
		}
		if data.Hotspots == nil {
			data.Hotspots = []GdeltHotspot{}
		}
		if data.ConflictNews == nil {
			data.ConflictNews = []GdeltNews{}
		}

		data.Overall, data.Daily, data.QuadClass, data.EventTypes = rollupGdeltSummary(summary)
		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// weightedAvg combines per-group averages exactly: Σ(avg_i×n_i)/Σ(n_i).
// A simple mean of group averages would let tiny groups distort the result.
type weightedAvg struct {
	sum float64
	n   int64
}

func (a *weightedAvg) add(avg float64, n int64) {
	a.sum += avg * float64(n)
	a.n += n
}

func (a *weightedAvg) value() float64 {
	if a.n == 0 {
		return 0
	}
	return math.Round(a.sum/float64(a.n)*100) / 100
}

func rollupGdeltSummary(rows []GdeltSummaryRow) (GdeltOverall, []GdeltDaily, []GdeltQuadClass, []GdeltEventType) {
	var overallTone, overallGoldstein weightedAvg
	dailyCount := map[string]int64{}
	dailyTone := map[string]*weightedAvg{}
	quadCount := map[int64]int64{}
	typeCount := map[string]int64{}
	typeTone := map[string]*weightedAvg{}
	typeGoldstein := map[string]*weightedAvg{}

	for _, r := range rows {
		overallTone.add(r.AvgTone, r.EventCount)
		overallGoldstein.add(r.AvgGoldstein, r.EventCount)

		dailyCount[r.IngestDate] += r.EventCount
		if dailyTone[r.IngestDate] == nil {
			dailyTone[r.IngestDate] = &weightedAvg{}
		}
		dailyTone[r.IngestDate].add(r.AvgTone, r.EventCount)

		quadCount[r.QuadClass] += r.EventCount

		typeCount[r.EventRootCode] += r.EventCount
		if typeTone[r.EventRootCode] == nil {
			typeTone[r.EventRootCode] = &weightedAvg{}
			typeGoldstein[r.EventRootCode] = &weightedAvg{}
		}
		typeTone[r.EventRootCode].add(r.AvgTone, r.EventCount)
		typeGoldstein[r.EventRootCode].add(r.AvgGoldstein, r.EventCount)
	}

	overall := GdeltOverall{
		EventCount:   overallTone.n,
		AvgTone:      overallTone.value(),
		AvgGoldstein: overallGoldstein.value(),
	}

	daily := make([]GdeltDaily, 0, len(dailyCount))
	for d, n := range dailyCount {
		daily = append(daily, GdeltDaily{IngestDate: d, EventCount: n, AvgTone: dailyTone[d].value()})
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].IngestDate < daily[j].IngestDate })

	quads := make([]GdeltQuadClass, 0, len(quadCount))
	for qc, n := range quadCount {
		quads = append(quads, GdeltQuadClass{QuadClass: qc, EventCount: n})
	}
	sort.Slice(quads, func(i, j int) bool { return quads[i].QuadClass < quads[j].QuadClass })

	types := make([]GdeltEventType, 0, len(typeCount))
	for code, n := range typeCount {
		types = append(types, GdeltEventType{
			EventRootCode: code,
			EventCount:    n,
			AvgGoldstein:  typeGoldstein[code].value(),
			AvgTone:       typeTone[code].value(),
		})
	}
	sort.Slice(types, func(i, j int) bool { return types[i].EventCount > types[j].EventCount })

	return overall, daily, quads, types
}

// --- /dyads ---

type GdeltDyadsData struct {
	Dyads     []GdeltDyadRow      `json:"dyads"`
	Countries []GdeltCountryCount `json:"countries"`
}

func (h *APIHandler) GdeltDyads(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltEventsDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:dyads:%s:%s", start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		dyads, countries, err := h.bq.GetGdeltDyadsConsolidated(ctx, start, end)
		if err != nil {
			return nil, err
		}
		data := GdeltDyadsData{
			Dyads:     dyads,
			Countries: countries,
		}
		if data.Dyads == nil {
			data.Dyads = []GdeltDyadRow{}
		}
		if data.Countries == nil {
			data.Countries = []GdeltCountryCount{}
		}

		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /country ---

// CAMEO actor country codes are exactly three uppercase letters. The value
// is also bound as a query parameter, so this is defense in depth plus a
// fast 400 for typos.
var gdeltCountryRe = regexp.MustCompile(`^[A-Z]{3}$`)

type GdeltCountryData struct {
	Country    string                  `json:"country"`
	Daily      []GdeltCountryDaily     `json:"daily"`
	EventTypes []GdeltCountryEventType `json:"event_types"`
	Partners   []GdeltPartnerRow       `json:"partners"`
	TopEvents  []GdeltCountryEvent     `json:"top_events"`
}

func (h *APIHandler) GdeltCountry(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltEventsDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	country := r.URL.Query().Get("country")
	if !gdeltCountryRe.MatchString(country) {
		writeError(w, fmt.Sprintf("invalid country %q: expected a 3-letter CAMEO code like USA", country), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:country:%s:%s:%s", country, start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		daily, types, partners, events, err := h.bq.GetGdeltCountryConsolidated(ctx, start, end, country)
		if err != nil {
			return nil, err
		}
		data := GdeltCountryData{
			Country:    country,
			Daily:      daily,
			EventTypes: types,
			Partners:   partners,
			TopEvents:  events,
		}
		if data.Daily == nil {
			data.Daily = []GdeltCountryDaily{}
		}
		if data.EventTypes == nil {
			data.EventTypes = []GdeltCountryEventType{}
		}
		if data.Partners == nil {
			data.Partners = []GdeltPartnerRow{}
		}
		if data.TopEvents == nil {
			data.TopEvents = []GdeltCountryEvent{}
		}

		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /impact ---

type GdeltImpactData struct {
	Daily     []GdeltImpactDaily    `json:"daily"`
	Countries []GdeltImpactCountry  `json:"countries"`
	Incidents []GdeltImpactIncident `json:"incidents"`
}

func (h *APIHandler) GdeltImpact(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltGkgDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:impact:%s:%s", start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		daily, countries, incidents, err := h.bq.GetGdeltImpactConsolidated(ctx, start, end)
		if err != nil {
			return nil, err
		}
		data := GdeltImpactData{
			Daily:     daily,
			Countries: countries,
			Incidents: incidents,
		}
		if data.Daily == nil {
			data.Daily = []GdeltImpactDaily{}
		}
		if data.Countries == nil {
			data.Countries = []GdeltImpactCountry{}
		}
		if data.Incidents == nil {
			data.Incidents = []GdeltImpactIncident{}
		}

		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /stories ---

type GdeltStoriesData struct {
	Stories []GdeltStoryRow `json:"stories"`
}

func (h *APIHandler) GdeltStories(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltStoriesDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:stories:%s:%s", start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		data := GdeltStoriesData{Stories: []GdeltStoryRow{}}
		stories, err := h.bq.GetGdeltStories(ctx, start, end)
		if err != nil {
			return nil, err
		}
		if stories != nil {
			data.Stories = stories
		}
		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /gkg ---

type GdeltGkgData struct {
	Themes  []GdeltNamedCount  `json:"themes"`
	Persons []GdeltNamedCount  `json:"persons"`
	Sources []GdeltMediaSource `json:"sources"`
}

func (h *APIHandler) GdeltGkg(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltGkgDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:gdelt:gkg:%s:%s", start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		themes, persons, sources, err := h.bq.GetGdeltGkgConsolidated(ctx, start, end)
		if err != nil {
			return nil, err
		}
		data := GdeltGkgData{
			Themes:  themes,
			Persons: persons,
			Sources: sources,
		}
		if data.Themes == nil {
			data.Themes = []GdeltNamedCount{}
		}
		if data.Persons == nil {
			data.Persons = []GdeltNamedCount{}
		}
		if data.Sources == nil {
			data.Sources = []GdeltMediaSource{}
		}

		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /industry ---

type GdeltIndustryData struct {
	Daily     []GdeltIndustryDaily   `json:"daily"`
	Orgs      []GdeltIndustryOrg     `json:"orgs"`
	Subtopics []GdeltNamedCount      `json:"subtopics"`
	Outlets   []GdeltMediaSource     `json:"outlets"`
	Articles  []GdeltIndustryArticle `json:"articles"`
}

func (h *APIHandler) GdeltIndustry(w http.ResponseWriter, r *http.Request) {
	start, end, err := parseGdeltRange(r, maxGdeltGkgDays)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	industry := r.URL.Query().Get("industry")
	themes, ok := industryThemes[industry]
	if !ok {
		writeError(w, fmt.Sprintf("invalid industry %q: valid values are %s",
			industry, strings.Join(industryKeys(), ", ")), http.StatusBadRequest)
		return
	}
	themeRe := industryThemeRegex(themes)

	key := fmt.Sprintf("opendata:gdelt:industry:%s:%s:%s", industry, start, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := gdeltFlight.Do(key, func() (any, error) {
		ctx, cancel := gdeltDetachedCtx(r)
		defer cancel()

		daily, orgs, subtopics, outlets, articles, err := h.bq.GetGdeltIndustryConsolidated(ctx, start, end, themeRe)
		if err != nil {
			return nil, err
		}
		data := GdeltIndustryData{
			Daily:     daily,
			Orgs:      orgs,
			Subtopics: subtopics,
			Outlets:   outlets,
			Articles:  articles,
		}
		if data.Daily == nil {
			data.Daily = []GdeltIndustryDaily{}
		}
		if data.Orgs == nil {
			data.Orgs = []GdeltIndustryOrg{}
		}
		if data.Subtopics == nil {
			data.Subtopics = []GdeltNamedCount{}
		}
		if data.Outlets == nil {
			data.Outlets = []GdeltMediaSource{}
		}
		if data.Articles == nil {
			data.Articles = []GdeltIndustryArticle{}
		}

		h.cache.SetWithTTL(key, &data, gdeltCacheTTL(end))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

