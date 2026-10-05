package main

// HTTP handlers for the SEM Insights open-data dashboard:
// /api/opendata/sem/{meta,dashboard,geo,pulse,term}.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/civil"
)

const (
	semMarketGlobal = "global"
	semMarketUS     = "us"
)

type SemMeta struct {
	LatestRefreshDate string          `json:"latest_refresh_date"`
	RefreshDates      []string        `json:"refresh_dates"`
	Countries         []TrendsCountry `json:"countries"` // global market only
	DMAs              []SemDMA        `json:"dmas"`      // us market only
}

// SemMeta serves partition dates plus the geo list for the selected market so
// the frontend can populate its filters in one call, sharing the underlying
// per-market meta cache with TrendsMetaHandler.
func (h *APIHandler) SemMeta(w http.ResponseWriter, r *http.Request) {
	market, err := parseSemMarket(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := "opendata:sem_meta:" + market
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	data := &SemMeta{RefreshDates: []string{}, Countries: []TrendsCountry{}, DMAs: []SemDMA{}}
	if market == semMarketUS {
		us, err := h.getTrendsUSMeta(r.Context())
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data.LatestRefreshDate = us.LatestRefreshDate
		data.RefreshDates = us.RefreshDates
		data.DMAs = us.DMAs
	} else {
		gl, err := h.getTrendsGlobalMeta(r.Context())
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data.LatestRefreshDate = gl.LatestRefreshDate
		data.RefreshDates = gl.RefreshDates
		data.Countries = gl.Countries
	}

	h.cache.SetWithTTL(key, data, trendsMetaTTL)
	writeJSON(w, data)
}

type SemDashboardData struct {
	Matrix []SemMatrixRow `json:"matrix"`
}

// SemDashboard serves the breakout matrix / opportunity table rows for one
// (market, geo, refresh_date) selection. For the US market geo is an optional
// DMA name (empty = national); for global it is a required country code.
func (h *APIHandler) SemDashboard(w http.ResponseWriter, r *http.Request) {
	market, err := parseSemMarket(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	refreshDate, err := parseTrendsDate(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	geo := strings.TrimSpace(r.URL.Query().Get("geo"))
	if market == semMarketGlobal {
		geo = strings.ToUpper(geo)
		if geo == "" {
			writeError(w, "geo (country code) is required for the global market", http.StatusBadRequest)
			return
		}
	}

	key := fmt.Sprintf("opendata:sem_dashboard:%s:%s:%s", market, refreshDate, geo)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		qctx := context.WithoutCancel(r.Context())
		var matrix []SemMatrixRow
		var err error
		if market == semMarketUS {
			matrix, err = h.bq.GetSemMatrixUS(qctx, refreshDate, geo)
		} else {
			matrix, err = h.bq.GetSemMatrixGlobal(qctx, refreshDate, geo)
		}
		if err != nil {
			return nil, err
		}
		if matrix == nil {
			matrix = []SemMatrixRow{}
		}
		data := &SemDashboardData{Matrix: matrix}
		h.cache.SetWithTTL(key, data, trendsSnapshotTTL)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

type SemGeoData struct {
	// Week is the Sunday (YYYY-MM-DD) starting the snapshot's latest complete
	// week, which every row describes; empty when there are no rows.
	Week string      `json:"week"`
	Rows []SemGeoRow `json:"rows"`
}

// SemGeo serves one term's per-geo interest (W2): all 210 DMAs for the US
// market, or the selected country's regions for global.
func (h *APIHandler) SemGeo(w http.ResponseWriter, r *http.Request) {
	market, refreshDate, geo, term, ok := parseSemTermSelection(w, r)
	if !ok {
		return
	}
	source := parseSemTermSource(r)

	// US geo breakdown is always across all 210 DMAs, so omit geo from the US cache key.
	geoKey := ""
	if market == semMarketGlobal {
		geoKey = strings.ToUpper(geo)
	}
	key := fmt.Sprintf("opendata:sem_geo:%s:%s:%s:%s:%s", market, refreshDate, geoKey, source, strings.ToLower(term))
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		qctx := context.WithoutCancel(r.Context())
		var rows []SemGeoRow
		var err error
		if market == semMarketUS {
			rows, err = h.bq.GetSemGeoUSSourced(qctx, refreshDate, term, source)
		} else {
			rows, err = h.bq.GetSemGeoGlobalSourced(qctx, refreshDate, geoKey, term, source)
		}
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []SemGeoRow{}
		}
		data := &SemGeoData{Rows: rows}
		if len(rows) > 0 {
			data.Week = rows[0].Week
		}
		h.cache.SetWithTTL(key, data, trendsSnapshotTTL)
		return data, nil
	})
	if err != nil {
		// The raw error can name projects, tables and SQL: log it, don't echo it.
		slog.Error("sem geo query failed", "market", market, "refresh_date", refreshDate, "error", err)
		writeError(w, "failed to load geo interest", http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

type SemPulseData struct {
	SnapshotTime string        `json:"snapshot_time"`
	Rows         []SemPulseRow `json:"rows"`
}

// SemPulse serves the latest US intraday snapshot (W5). US-only, no params.
func (h *APIHandler) SemPulse(w http.ResponseWriter, r *http.Request) {
	const key = "opendata:sem_pulse"
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		rows, err := h.bq.GetSemPulse(context.WithoutCancel(r.Context()))
		if err != nil {
			return nil, err
		}
		data := &SemPulseData{Rows: rows}
		if len(rows) > 0 {
			data.SnapshotTime = rows[0].SnapshotTime
		} else {
			data.Rows = []SemPulseRow{}
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

type SemTermData struct {
	History []SemHistoryPoint `json:"history"`
}

// SemTerm serves one term's weekly history for the drill-down panel (W6).
func (h *APIHandler) SemTerm(w http.ResponseWriter, r *http.Request) {
	market, refreshDate, geo, term, ok := parseSemTermSelection(w, r)
	if !ok {
		return
	}
	source := parseSemTermSource(r)

	// US term history is national (averages all DMAs), so omit geo from the US cache key.
	geoKey := ""
	if market == semMarketGlobal {
		geoKey = strings.ToUpper(geo)
	}
	key := fmt.Sprintf("opendata:sem_term:%s:%s:%s:%s:%s", market, refreshDate, geoKey, source, strings.ToLower(term))
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		qctx := context.WithoutCancel(r.Context())
		var history []SemHistoryPoint
		var err error
		if market == semMarketUS {
			history, err = h.bq.GetSemTermHistoryUSSourced(qctx, refreshDate, term, source)
		} else {
			history, err = h.bq.GetSemTermHistoryGlobalSourced(qctx, refreshDate, geoKey, term, source)
		}
		if err != nil {
			return nil, err
		}
		if history == nil {
			history = []SemHistoryPoint{}
		}
		data := &SemTermData{History: history}
		h.cache.SetWithTTL(key, data, trendsSnapshotTTL)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

func parseSemTermSource(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source"))) {
	case "rising":
		return "rising"
	case "top":
		return "top"
	default:
		return ""
	}
}

// semSafetyDays is the news-context window; 14 daily partitions ≈ 42 MB.
const semSafetyDays = 14

type SemSafetyData struct {
	Rows []SemSafetyRow `json:"rows"`
}

// SemSafety serves the market's 14-day news tone + conflict share (W3) over
// the latest 14 complete UTC days (ending yesterday UTC so an in-progress
// partial day does not skew the 3-day tone or daily bars).
func (h *APIHandler) SemSafety(w http.ResponseWriter, r *http.Request) {
	market, err := parseSemMarket(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	iso := "US"
	if market == semMarketGlobal {
		iso = strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("geo")))
		if iso == "" {
			writeError(w, "geo (country code) is required for the global market", http.StatusBadRequest)
			return
		}
	}
	actor, ok := semActorCountry[iso]
	if !ok {
		writeError(w, fmt.Sprintf("no GDELT actor-code mapping for country %q", iso), http.StatusBadRequest)
		return
	}

	end := civil.DateOf(time.Now().UTC()).AddDays(-1)
	start := end.AddDays(-(semSafetyDays - 1))

	key := fmt.Sprintf("opendata:sem_safety:%s:%s", actor, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := trendsFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		rows, err := h.bq.GetSemSafetyDaily(context.WithoutCancel(r.Context()), start, end, actor)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []SemSafetyRow{}
		}
		data := &SemSafetyData{Rows: rows}
		h.cache.SetWithTTL(key, data, trendsMetaTTL)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// parseSemTermSelection validates the shared (market, refresh_date, geo, term)
// query params of the per-term endpoints, writing the 400 itself on failure.
func parseSemTermSelection(w http.ResponseWriter, r *http.Request) (market string, refreshDate civil.Date, geo, term string, ok bool) {
	market, err := parseSemMarket(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	refreshDate, err = parseTrendsDate(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	geo = r.URL.Query().Get("geo")
	if market == semMarketGlobal && geo == "" {
		writeError(w, "geo (country code) is required for the global market", http.StatusBadRequest)
		return
	}
	term = r.URL.Query().Get("term")
	if term == "" {
		writeError(w, "term is required", http.StatusBadRequest)
		return
	}
	return market, refreshDate, geo, term, true
}

func parseSemMarket(r *http.Request) (string, error) {
	market := r.URL.Query().Get("market")
	if market != semMarketGlobal && market != semMarketUS {
		return "", fmt.Errorf("market must be %q or %q", semMarketGlobal, semMarketUS)
	}
	return market, nil
}
