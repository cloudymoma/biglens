package main

// HTTP handlers for the SEM Insights open-data dashboard:
// /api/opendata/sem/{meta,dashboard,geo,pulse,term}.

import (
	"fmt"
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
// the frontend can populate its filters in one call.
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

	var dates []string
	if market == semMarketUS {
		dates, err = h.bq.GetSemUSRefreshDates(r.Context())
	} else {
		dates, err = h.bq.GetTrendsRefreshDates(r.Context())
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := &SemMeta{RefreshDates: []string{}, Countries: []TrendsCountry{}, DMAs: []SemDMA{}}
	if len(dates) == 0 {
		writeJSON(w, data)
		return
	}

	latest, err := civil.ParseDate(dates[0])
	if err != nil {
		writeError(w, fmt.Sprintf("unexpected refresh_date %q: %v", dates[0], err), http.StatusInternalServerError)
		return
	}
	data.LatestRefreshDate = dates[0]
	data.RefreshDates = dates

	if market == semMarketUS {
		dmas, err := h.bq.GetSemDMAs(r.Context(), latest)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data.DMAs = dmas
	} else {
		countries, err := h.bq.GetTrendsCountries(r.Context(), latest)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data.Countries = countries
	}

	h.cache.Set(key, data)
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
	geo := r.URL.Query().Get("geo")
	if market == semMarketGlobal && geo == "" {
		writeError(w, "geo (country code) is required for the global market", http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:sem_dashboard:%s:%s:%s", market, refreshDate, geo)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var matrix []SemMatrixRow
	if market == semMarketUS {
		matrix, err = h.bq.GetSemMatrixUS(r.Context(), refreshDate, geo)
	} else {
		matrix, err = h.bq.GetSemMatrixGlobal(r.Context(), refreshDate, geo)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if matrix == nil {
		matrix = []SemMatrixRow{}
	}

	data := &SemDashboardData{Matrix: matrix}
	h.cache.Set(key, data)
	writeJSON(w, data)
}

type SemGeoData struct {
	Rows []SemGeoRow `json:"rows"`
}

// SemGeo serves one term's per-geo demand (W2 bid-modifier table): all 210
// DMAs for the US market, or the selected country's regions for global.
func (h *APIHandler) SemGeo(w http.ResponseWriter, r *http.Request) {
	market, refreshDate, geo, term, ok := parseSemTermSelection(w, r)
	if !ok {
		return
	}

	key := fmt.Sprintf("opendata:sem_geo:%s:%s:%s:%s", market, refreshDate, geo, strings.ToLower(term))
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var rows []SemGeoRow
	var err error
	if market == semMarketUS {
		rows, err = h.bq.GetSemGeoUS(r.Context(), refreshDate, term)
	} else {
		rows, err = h.bq.GetSemGeoGlobal(r.Context(), refreshDate, geo, term)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []SemGeoRow{}
	}

	data := &SemGeoData{Rows: rows}
	h.cache.Set(key, data)
	writeJSON(w, data)
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

	rows, err := h.bq.GetSemPulse(r.Context())
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := &SemPulseData{Rows: rows}
	if len(rows) > 0 {
		data.SnapshotTime = rows[0].SnapshotTime
	} else {
		data.Rows = []SemPulseRow{}
	}

	h.cache.Set(key, data)
	writeJSON(w, data)
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

	key := fmt.Sprintf("opendata:sem_term:%s:%s:%s:%s", market, refreshDate, geo, strings.ToLower(term))
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var history []SemHistoryPoint
	var err error
	if market == semMarketUS {
		history, err = h.bq.GetSemTermHistoryUS(r.Context(), refreshDate, term)
	} else {
		history, err = h.bq.GetSemTermHistoryGlobal(r.Context(), refreshDate, geo, term)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if history == nil {
		history = []SemHistoryPoint{}
	}

	data := &SemTermData{History: history}
	h.cache.Set(key, data)
	writeJSON(w, data)
}

// semSafetyDays is the news-context window; 14 daily partitions ≈ 42 MB.
const semSafetyDays = 14

type SemSafetyData struct {
	Rows []SemSafetyRow `json:"rows"`
}

// SemSafety serves the market's 14-day news tone + conflict share (W3).
// GDELT tone is country-grained: the US market is always US-national, the
// global market uses the selected country. The window always ends today
// (news context, unlike the snapshot-pinned trends widgets).
func (h *APIHandler) SemSafety(w http.ResponseWriter, r *http.Request) {
	market, err := parseSemMarket(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	iso := "US"
	if market == semMarketGlobal {
		iso = r.URL.Query().Get("geo")
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

	end := civil.DateOf(time.Now().UTC())
	start := end.AddDays(-(semSafetyDays - 1))

	key := fmt.Sprintf("opendata:sem_safety:%s:%s", actor, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	rows, err := h.bq.GetSemSafetyDaily(r.Context(), start, end, actor)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []SemSafetyRow{}
	}

	data := &SemSafetyData{Rows: rows}
	h.cache.Set(key, data)
	writeJSON(w, data)
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
