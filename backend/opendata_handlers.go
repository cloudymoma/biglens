package main

// HTTP handlers for the BigQuery Open Data section. Each public dataset gets
// its own /api/opendata/<dataset>/* namespace; Google Trends is the first.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

// maxTrendsCompareTerms caps the history comparison per google_trends.md
// (Widget 4.1 supports 2-5 terms).
const (
	maxTrendsCompareTerms = 5
	trendsSnapshotTTL     = 24 * time.Hour
	trendsMetaTTL         = 1 * time.Hour
)

// trendsFlight coalesces concurrent identical Trends and SEM requests so
// simultaneous page loads or StrictMode double-invocations share one BigQuery job.
var trendsFlight singleflight.Group

type TrendsMeta struct {
	LatestRefreshDate   string          `json:"latest_refresh_date"`
	RefreshDates        []string        `json:"refresh_dates"`
	Countries           []TrendsCountry `json:"countries"`
	UsLatestRefreshDate string          `json:"us_latest_refresh_date"`
	UsRefreshDates      []string        `json:"us_refresh_dates"`
	Dmas                []SemDMA        `json:"dmas"`
}

type trendsGlobalMetaSlice struct {
	LatestRefreshDate string
	RefreshDates      []string
	Countries         []TrendsCountry
}

type trendsUSMetaSlice struct {
	LatestRefreshDate string
	RefreshDates      []string
	DMAs              []SemDMA
}

func (h *APIHandler) getTrendsGlobalMeta(ctx context.Context) (*trendsGlobalMetaSlice, error) {
	const key = "opendata:trends_meta:global"
	if cached, ok := h.cache.Get(key); ok {
		return cached.(*trendsGlobalMetaSlice), nil
	}
	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached.(*trendsGlobalMetaSlice), nil
		}
		qctx := context.WithoutCancel(ctx)
		dates, err := h.bq.GetTrendsRefreshDates(qctx)
		if err != nil {
			return nil, err
		}
		res := &trendsGlobalMetaSlice{
			RefreshDates: []string{},
			Countries:    []TrendsCountry{},
		}
		if len(dates) == 0 {
			h.cache.SetWithTTL(key, res, trendsMetaTTL)
			return res, nil
		}
		latest, err := civil.ParseDate(dates[0])
		if err != nil {
			return nil, fmt.Errorf("unexpected refresh_date %q: %w", dates[0], err)
		}
		cKey := "opendata:trends_countries:" + dates[0]
		var countries []TrendsCountry
		if cCached, ok := h.cache.Get(cKey); ok {
			countries = cCached.([]TrendsCountry)
		} else {
			countries, err = h.bq.GetTrendsCountries(qctx, latest)
			if err != nil {
				return nil, err
			}
			if countries == nil {
				countries = []TrendsCountry{}
			}
			h.cache.SetWithTTL(cKey, countries, trendsSnapshotTTL)
		}
		res.LatestRefreshDate = dates[0]
		res.RefreshDates = dates
		res.Countries = countries
		h.cache.SetWithTTL(key, res, trendsMetaTTL)
		return res, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*trendsGlobalMetaSlice), nil
}

func (h *APIHandler) getTrendsUSMeta(ctx context.Context) (*trendsUSMetaSlice, error) {
	const key = "opendata:trends_meta:us"
	if cached, ok := h.cache.Get(key); ok {
		return cached.(*trendsUSMetaSlice), nil
	}
	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached.(*trendsUSMetaSlice), nil
		}
		qctx := context.WithoutCancel(ctx)
		dates, err := h.bq.GetSemUSRefreshDates(qctx)
		if err != nil {
			return nil, err
		}
		res := &trendsUSMetaSlice{
			RefreshDates: []string{},
			DMAs:         []SemDMA{},
		}
		if len(dates) == 0 {
			h.cache.SetWithTTL(key, res, trendsMetaTTL)
			return res, nil
		}
		latest, err := civil.ParseDate(dates[0])
		if err != nil {
			return nil, fmt.Errorf("unexpected US refresh_date %q: %w", dates[0], err)
		}
		dKey := "opendata:trends_dmas:" + dates[0]
		var dmas []SemDMA
		if dCached, ok := h.cache.Get(dKey); ok {
			dmas = dCached.([]SemDMA)
		} else {
			dmas, err = h.bq.GetSemDMAs(qctx, latest)
			if err != nil {
				return nil, err
			}
			if dmas == nil {
				dmas = []SemDMA{}
			}
			h.cache.SetWithTTL(dKey, dmas, trendsSnapshotTTL)
		}
		res.LatestRefreshDate = dates[0]
		res.RefreshDates = dates
		res.DMAs = dmas
		h.cache.SetWithTTL(key, res, trendsMetaTTL)
		return res, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*trendsUSMetaSlice), nil
}

// TrendsMetaHandler serves available partition dates and countries so the
// frontend can populate its filters without ever issuing MAX(refresh_date)
// per interaction.
func (h *APIHandler) TrendsMetaHandler(w http.ResponseWriter, r *http.Request) {
	const key = "opendata:trends_meta"
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		data := &TrendsMeta{
			RefreshDates:   []string{},
			Countries:      []TrendsCountry{},
			UsRefreshDates: []string{},
			Dmas:           []SemDMA{},
		}
		g, gctx := errgroup.WithContext(context.WithoutCancel(r.Context()))
		g.Go(func() error {
			gl, err := h.getTrendsGlobalMeta(gctx)
			if err != nil {
				return err
			}
			data.LatestRefreshDate = gl.LatestRefreshDate
			data.RefreshDates = gl.RefreshDates
			data.Countries = gl.Countries
			return nil
		})
		g.Go(func() error {
			us, err := h.getTrendsUSMeta(gctx)
			if err != nil {
				return err
			}
			data.UsLatestRefreshDate = us.LatestRefreshDate
			data.UsRefreshDates = us.RefreshDates
			data.Dmas = us.DMAs
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		h.cache.SetWithTTL(key, data, trendsMetaTTL)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

type TrendsDashboardData struct {
	TopTerms    []TrendsTopTerm    `json:"top_terms"`
	RisingTerms []TrendsRisingTerm `json:"rising_terms"`
}

func (h *APIHandler) TrendsDashboard(w http.ResponseWriter, r *http.Request) {
	refreshDate, err := parseTrendsDate(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	countryCode := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country_code")))
	if countryCode == "" {
		writeError(w, "country_code is required", http.StatusBadRequest)
		return
	}
	// dma narrows the US market to one metro ('' = national); it is only
	// meaningful with country_code=US.
	dma := ""
	if countryCode == "US" {
		dma = strings.TrimSpace(r.URL.Query().Get("dma"))
	}

	key := fmt.Sprintf("opendata:trends_dashboard:%s:%s:%s", refreshDate, countryCode, dma)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		var data TrendsDashboardData
		g, ctx := errgroup.WithContext(context.WithoutCancel(r.Context()))

		g.Go(func() error {
			var top []TrendsTopTerm
			var err error
			if countryCode == "US" {
				top, err = h.bq.GetTrendsTopTermsUS(ctx, refreshDate, dma)
			} else {
				top, err = h.bq.GetTrendsTopTerms(ctx, refreshDate, countryCode)
			}
			if err != nil {
				return err
			}
			data.TopTerms = top
			return nil
		})

		g.Go(func() error {
			var rising []TrendsRisingTerm
			var err error
			if countryCode == "US" {
				rising, err = h.bq.GetTrendsRisingTermsUS(ctx, refreshDate, dma)
			} else {
				rising, err = h.bq.GetTrendsRisingTerms(ctx, refreshDate, countryCode)
			}
			if err != nil {
				return err
			}
			data.RisingTerms = rising
			return nil
		})

		if err := g.Wait(); err != nil {
			return nil, err
		}

		h.cache.SetWithTTL(key, &data, trendsSnapshotTTL)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

type TrendsTermData struct {
	Geo     []TrendsGeoPoint     `json:"geo"`
	History []TrendsHistoryPoint `json:"history"`
}

// TrendsTerm serves the term-focused widgets: cross-country distribution for
// `term` (Widget 3.x) and the 5-year weekly history for `terms` (Widget 4.1).
// Geo and per-term history are cached independently under normalized keys so
// changing the focus term never re-runs history, and adding a compare term
// only queries the newly added term.
func (h *APIHandler) TrendsTerm(w http.ResponseWriter, r *http.Request) {
	refreshDate, err := parseTrendsDate(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	term := strings.TrimSpace(q.Get("term"))
	countryCode := strings.ToUpper(strings.TrimSpace(q.Get("country_code")))
	terms := splitTrimmed(q.Get("terms"), ",")
	if len(terms) > maxTrendsCompareTerms {
		writeError(w, fmt.Sprintf("at most %d terms can be compared", maxTrendsCompareTerms), http.StatusBadRequest)
		return
	}
	if term == "" && len(terms) == 0 {
		writeError(w, "term or terms is required", http.StatusBadRequest)
		return
	}
	if len(terms) > 0 && countryCode == "" {
		writeError(w, "country_code is required for term history", http.StatusBadRequest)
		return
	}

	dma := ""
	if countryCode == "US" {
		dma = strings.TrimSpace(q.Get("dma"))
	}

	data := TrendsTermData{Geo: []TrendsGeoPoint{}, History: []TrendsHistoryPoint{}}
	g, ctx := errgroup.WithContext(context.WithoutCancel(r.Context()))

	if term != "" {
		g.Go(func() error {
			geoScope := "global"
			if countryCode == "US" {
				geoScope = "us"
			}
			geoKey := fmt.Sprintf("opendata:trends_geo:%s:%s:%s", geoScope, refreshDate, strings.ToLower(term))
			if cached, ok := h.cache.Get(geoKey); ok {
				data.Geo = cached.([]TrendsGeoPoint)
				return nil
			}
			v, err, _ := trendsFlight.Do(geoKey, func() (any, error) {
				if cached, ok := h.cache.Get(geoKey); ok {
					return cached.([]TrendsGeoPoint), nil
				}
				var geo []TrendsGeoPoint
				var err error
				if countryCode == "US" {
					geo, err = h.bq.GetTrendsGeoUS(ctx, refreshDate, term)
				} else {
					geo, err = h.bq.GetTrendsGeo(ctx, refreshDate, term)
				}
				if err != nil {
					return nil, err
				}
				if geo == nil {
					geo = []TrendsGeoPoint{}
				}
				h.cache.SetWithTTL(geoKey, geo, trendsSnapshotTTL)
				return geo, nil
			})
			if err != nil {
				return err
			}
			data.Geo = v.([]TrendsGeoPoint)
			return nil
		})
	}

	if len(terms) > 0 {
		g.Go(func() error {
			seen := make(map[string]bool, len(terms))
			orderedTerms := make([]string, 0, len(terms))
			var missing []string
			cachedByTerm := make(map[string][]TrendsHistoryPoint, len(terms))

			for _, t := range terms {
				lt := strings.ToLower(t)
				if seen[lt] {
					continue
				}
				seen[lt] = true
				orderedTerms = append(orderedTerms, lt)
				tKey := fmt.Sprintf("opendata:trends_hist:%s:%s:%s:%s", refreshDate, countryCode, dma, lt)
				if c, ok := h.cache.Get(tKey); ok {
					cachedByTerm[lt] = c.([]TrendsHistoryPoint)
				} else {
					missing = append(missing, lt)
				}
			}

			if len(missing) > 0 {
				sortedMissing := append([]string(nil), missing...)
				sort.Strings(sortedMissing)
				batchKey := fmt.Sprintf("opendata:trends_hist_batch:%s:%s:%s:%s",
					refreshDate, countryCode, dma, strings.Join(sortedMissing, ","))
				v, err, _ := trendsFlight.Do(batchKey, func() (any, error) {
					var rows []TrendsHistoryPoint
					var err error
					if countryCode == "US" {
						rows, err = h.bq.GetTrendsHistoryUS(ctx, refreshDate, dma, sortedMissing)
					} else {
						rows, err = h.bq.GetTrendsHistory(ctx, refreshDate, countryCode, sortedMissing)
					}
					if err != nil {
						return nil, err
					}
					byTerm := make(map[string][]TrendsHistoryPoint, len(sortedMissing))
					for _, pt := range rows {
						lt := strings.ToLower(pt.Term)
						byTerm[lt] = append(byTerm[lt], pt)
					}
					for _, lt := range sortedMissing {
						pts := byTerm[lt]
						if pts == nil {
							pts = []TrendsHistoryPoint{}
						}
						tKey := fmt.Sprintf("opendata:trends_hist:%s:%s:%s:%s", refreshDate, countryCode, dma, lt)
						h.cache.SetWithTTL(tKey, pts, trendsSnapshotTTL)
					}
					return byTerm, nil
				})
				if err != nil {
					return err
				}
				fetchedByTerm := v.(map[string][]TrendsHistoryPoint)
				for _, lt := range missing {
					cachedByTerm[lt] = fetchedByTerm[lt]
				}
			}

			var combined []TrendsHistoryPoint
			for _, lt := range orderedTerms {
				combined = append(combined, cachedByTerm[lt]...)
			}
			if combined != nil {
				data.History = combined
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, &data)
}

func parseTrendsDate(r *http.Request) (civil.Date, error) {
	raw := r.URL.Query().Get("refresh_date")
	if raw == "" {
		return civil.Date{}, fmt.Errorf("refresh_date is required (YYYY-MM-DD)")
	}
	d, err := civil.ParseDate(raw)
	if err != nil {
		return civil.Date{}, fmt.Errorf("invalid refresh_date %q: expected YYYY-MM-DD", raw)
	}
	return d, nil
}
