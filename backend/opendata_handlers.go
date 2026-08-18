package main

// HTTP handlers for the BigQuery Open Data section. Each public dataset gets
// its own /api/opendata/<dataset>/* namespace; Google Trends is the first.

import (
	"fmt"
	"net/http"
	"strings"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
)

// maxTrendsCompareTerms caps the history comparison per google_trends.md
// (Widget 4.1 supports 2-5 terms).
const maxTrendsCompareTerms = 5

type TrendsMeta struct {
	LatestRefreshDate string          `json:"latest_refresh_date"`
	RefreshDates      []string        `json:"refresh_dates"`
	Countries         []TrendsCountry `json:"countries"`
	Dmas              []SemDMA        `json:"dmas"`
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

	dates, err := h.bq.GetTrendsRefreshDates(r.Context())
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(dates) == 0 {
		writeJSON(w, &TrendsMeta{RefreshDates: []string{}, Countries: []TrendsCountry{}})
		return
	}

	latest, err := civil.ParseDate(dates[0])
	if err != nil {
		writeError(w, fmt.Sprintf("unexpected refresh_date %q: %v", dates[0], err), http.StatusInternalServerError)
		return
	}

	// The US market lives in the DMA-grained US tables (partition dates
	// verified aligned with the international ones), so one meta payload
	// carries both filter lists: countries for Global, DMAs for US.
	var countries []TrendsCountry
	var dmas []SemDMA
	g, gctx := errgroup.WithContext(r.Context())
	g.Go(func() error {
		var err error
		countries, err = h.bq.GetTrendsCountries(gctx, latest)
		return err
	})
	g.Go(func() error {
		var err error
		dmas, err = h.bq.GetSemDMAs(gctx, latest)
		return err
	})
	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := &TrendsMeta{
		LatestRefreshDate: dates[0],
		RefreshDates:      dates,
		Countries:         countries,
		Dmas:              dmas,
	}
	h.cache.Set(key, data)
	writeJSON(w, data)
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
	countryCode := r.URL.Query().Get("country_code")
	if countryCode == "" {
		writeError(w, "country_code is required", http.StatusBadRequest)
		return
	}
	// dma narrows the US market to one metro ('' = national); it is only
	// meaningful with country_code=US.
	dma := r.URL.Query().Get("dma")

	key := fmt.Sprintf("opendata:trends_dashboard:%s:%s:%s", refreshDate, countryCode, dma)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data TrendsDashboardData
	g, ctx := errgroup.WithContext(r.Context())

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
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

type TrendsTermData struct {
	Geo     []TrendsGeoPoint     `json:"geo"`
	History []TrendsHistoryPoint `json:"history"`
}

// TrendsTerm serves the term-focused widgets: cross-country distribution for
// `term` (Widget 3.x) and the 5-year weekly history for `terms` (Widget 4.1).
func (h *APIHandler) TrendsTerm(w http.ResponseWriter, r *http.Request) {
	refreshDate, err := parseTrendsDate(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	term := q.Get("term")
	countryCode := q.Get("country_code")
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

	dma := q.Get("dma")
	key := fmt.Sprintf("opendata:trends_term:%s:%s:%s:%s:%s", refreshDate, countryCode, dma, term, strings.Join(terms, ","))
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	data := TrendsTermData{Geo: []TrendsGeoPoint{}, History: []TrendsHistoryPoint{}}
	g, ctx := errgroup.WithContext(r.Context())

	if term != "" {
		g.Go(func() error {
			// In the US view the cross-country chart becomes a DMA breakdown:
			// the US never appears in the international tables, and metro
			// grain is the useful spread there. Reuses the SEM geo query
			// (top-25 ∪ rising across all 210 DMAs).
			if countryCode == "US" {
				rows, err := h.bq.GetSemGeoUS(ctx, refreshDate, term)
				if err != nil {
					return err
				}
				for _, row := range rows {
					data.Geo = append(data.Geo, TrendsGeoPoint{CountryName: row.Geo, Score: row.Score})
				}
				return nil
			}
			geo, err := h.bq.GetTrendsGeo(ctx, refreshDate, term)
			if err != nil {
				return err
			}
			if geo != nil {
				data.Geo = geo
			}
			return nil
		})
	}

	if len(terms) > 0 {
		g.Go(func() error {
			var history []TrendsHistoryPoint
			var err error
			if countryCode == "US" {
				history, err = h.bq.GetTrendsHistoryUS(ctx, refreshDate, dma, terms)
			} else {
				history, err = h.bq.GetTrendsHistory(ctx, refreshDate, countryCode, terms)
			}
			if err != nil {
				return err
			}
			if history != nil {
				data.History = history
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
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
