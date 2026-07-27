package main

// HTTP handlers for the GDELT Open Data dashboard. Two endpoints so the fast
// event panels render without waiting for the heavier GKG scans:
//
//	GET /api/opendata/gdelt/events?start_date=...&end_date=...   (Overview panels 1+2)
//	GET /api/opendata/gdelt/gkg?start_date=...&end_date=...      (Overview panel 3)
//	GET /api/opendata/gdelt/dyads?start_date=...&end_date=...    (Country & Relations board)
//	GET /api/opendata/gdelt/country?...&country=USA              (Country & Relations drill-down)

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

// Span caps keep worst-case scans bounded (GKG scans ~10x the events table
// per day); requests beyond them are rejected before any BigQuery call.
const (
	maxGdeltEventsDays = 90
	maxGdeltGkgDays    = 30
)

// gdeltFlight collapses concurrent identical queries the instant a cache
// entry expires, so one BigQuery round-trip serves all waiters.
var gdeltFlight singleflight.Group

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
		data := GdeltEventsData{
			Hotspots:     []GdeltHotspot{},
			ConflictNews: []GdeltNews{},
		}
		var summary []GdeltSummaryRow

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetGdeltSummary(ctx, start, end)
			if err != nil {
				return err
			}
			summary = rows
			return nil
		})
		g.Go(func() error {
			hotspots, err := h.bq.GetGdeltHotspots(ctx, start, end)
			if err != nil {
				return err
			}
			if hotspots != nil {
				data.Hotspots = hotspots
			}
			return nil
		})
		g.Go(func() error {
			news, err := h.bq.GetGdeltConflictNews(ctx, start, end)
			if err != nil {
				return err
			}
			if news != nil {
				data.ConflictNews = news
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.Overall, data.Daily, data.QuadClass, data.EventTypes = rollupGdeltSummary(summary)
		h.cache.Set(key, &data)
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
		data := GdeltDyadsData{
			Dyads:     []GdeltDyadRow{},
			Countries: []GdeltCountryCount{},
		}

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			dyads, err := h.bq.GetGdeltDyads(ctx, start, end)
			if err != nil {
				return err
			}
			if dyads != nil {
				data.Dyads = dyads
			}
			return nil
		})
		g.Go(func() error {
			countries, err := h.bq.GetGdeltActorCountries(ctx, start, end)
			if err != nil {
				return err
			}
			if countries != nil {
				data.Countries = countries
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		h.cache.Set(key, &data)
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
		data := GdeltCountryData{
			Country:    country,
			Daily:      []GdeltCountryDaily{},
			EventTypes: []GdeltCountryEventType{},
			Partners:   []GdeltPartnerRow{},
			TopEvents:  []GdeltCountryEvent{},
		}

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			daily, err := h.bq.GetGdeltCountryDaily(ctx, start, end, country)
			if err != nil {
				return err
			}
			if daily != nil {
				data.Daily = daily
			}
			return nil
		})
		g.Go(func() error {
			types, err := h.bq.GetGdeltCountryEventTypes(ctx, start, end, country)
			if err != nil {
				return err
			}
			if types != nil {
				data.EventTypes = types
			}
			return nil
		})
		g.Go(func() error {
			partners, err := h.bq.GetGdeltCountryPartners(ctx, start, end, country)
			if err != nil {
				return err
			}
			if partners != nil {
				data.Partners = partners
			}
			return nil
		})
		g.Go(func() error {
			events, err := h.bq.GetGdeltCountryEvents(ctx, start, end, country)
			if err != nil {
				return err
			}
			if events != nil {
				data.TopEvents = events
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		h.cache.Set(key, &data)
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
		data := GdeltGkgData{
			Themes:  []GdeltNamedCount{},
			Persons: []GdeltNamedCount{},
			Sources: []GdeltMediaSource{},
		}

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			themes, err := h.bq.GetGdeltThemes(ctx, start, end)
			if err != nil {
				return err
			}
			if themes != nil {
				data.Themes = themes
			}
			return nil
		})
		g.Go(func() error {
			persons, err := h.bq.GetGdeltPersons(ctx, start, end)
			if err != nil {
				return err
			}
			if persons != nil {
				data.Persons = persons
			}
			return nil
		})
		g.Go(func() error {
			sources, err := h.bq.GetGdeltMediaSources(ctx, start, end)
			if err != nil {
				return err
			}
			if sources != nil {
				data.Sources = sources
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}
