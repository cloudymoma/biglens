package main

// HTTP handlers for the NOAA GHCN-Daily Open Data dashboard:
//
//	GET /api/opendata/weather/meta                          → latest observation date
//	GET /api/opendata/weather/dashboard?date=...&days=30    → snapshot + trend
//
// Both resolve the latest available date through latestWeatherDate (cached,
// singleflight) so the dashboard can validate its upper bound without an
// extra round-trip per request.

import (
	"fmt"
	"net/http"
	"strconv"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	weatherDefaultDays = 30
	weatherMinDays     = 7
	// weatherMaxDays caps the trend window so a request touches at most two
	// year tables (the GHCN-D shards are unpartitioned full-table scans).
	weatherMaxDays = 31
)

// weatherMinDate bounds time travel; every ghcnd_YYYY table from 1899 on
// exists, so any ≤31-day window ending 1900-01-01 or later is servable.
var weatherMinDate = civil.Date{Year: 1900, Month: 1, Day: 1}

// weatherSettledTmaxStations is the "usable day" bar for the default
// snapshot: a settled day has ~6k TMAX stations, the newest 1-2 days can
// have near zero (backfill). Half-settled is plenty for a live-looking map.
const weatherSettledTmaxStations = 3000

var weatherFlight singleflight.Group

type WeatherMetaData struct {
	// LatestDate is MAX(date) — the date picker's upper bound.
	LatestDate string `json:"latest_date"`
	// DefaultDate is the freshest day with meaningful coverage — what the
	// dashboard shows first. Can equal LatestDate once backfill catches up.
	DefaultDate string `json:"default_date"`
}

// pickWeatherDefaultDate selects the newest day (rows are newest-first) with
// settled coverage, falling back to the best-covered day, then to the
// newest. Never empty for non-empty input.
func pickWeatherDefaultDate(rows []WeatherCoverageRow) string {
	best := ""
	var bestCount int64 = -1
	for _, r := range rows {
		if r.TmaxStations >= weatherSettledTmaxStations {
			return r.Date
		}
		if r.TmaxStations > bestCount {
			best, bestCount = r.Date, r.TmaxStations
		}
	}
	return best
}

// weatherMeta returns the cached meta (latest + default date), querying
// recent coverage on a miss under singleflight.
func (h *APIHandler) weatherMeta(r *http.Request) (*WeatherMetaData, error) {
	const key = "opendata:weather:meta"
	if cached, ok := h.cache.Get(key); ok {
		if meta, ok := cached.(*WeatherMetaData); ok {
			return meta, nil
		}
	}
	v, err, _ := weatherFlight.Do(key, func() (any, error) {
		coverage, err := h.bq.GetWeatherRecentCoverage(r.Context())
		if err != nil {
			return nil, err
		}
		if len(coverage) == 0 {
			return nil, fmt.Errorf("ghcn_d returned no recent observations")
		}
		meta := &WeatherMetaData{
			LatestDate:  coverage[0].Date,
			DefaultDate: pickWeatherDefaultDate(coverage),
		}
		h.cache.Set(key, meta)
		return meta, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*WeatherMetaData), nil
}

func (h *APIHandler) WeatherMeta(w http.ResponseWriter, r *http.Request) {
	meta, err := h.weatherMeta(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, meta)
}

// parseWeatherParams validates the snapshot date and trailing window.
func parseWeatherParams(r *http.Request, latest civil.Date) (snap civil.Date, days int, err error) {
	q := r.URL.Query()
	snap, err = civil.ParseDate(q.Get("date"))
	if err != nil {
		return snap, 0, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", q.Get("date"))
	}
	if snap.Before(weatherMinDate) {
		return snap, 0, fmt.Errorf("date must be on or after %s", weatherMinDate)
	}
	if snap.After(latest) {
		return snap, 0, fmt.Errorf("date must not be after the latest observation (%s)", latest)
	}
	days = weatherDefaultDays
	if raw := q.Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
		if err != nil {
			return snap, 0, fmt.Errorf("invalid days %q: expected an integer", raw)
		}
	}
	if days < weatherMinDays || days > weatherMaxDays {
		return snap, 0, fmt.Errorf("days must be between %d and %d", weatherMinDays, weatherMaxDays)
	}
	return snap, days, nil
}

// --- /dashboard ---

type WeatherExtreme struct {
	Station      string  `json:"station"`
	CountryState string  `json:"country_state"`
	Value        float64 `json:"value"`
}

type WeatherOverall struct {
	StationsReporting int             `json:"stations_reporting"`
	Hottest           *WeatherExtreme `json:"hottest"`
	Coldest           *WeatherExtreme `json:"coldest"`
	Wettest           *WeatherExtreme `json:"wettest"`
	SnowStations      int             `json:"snow_stations"`
}

type WeatherDashboardData struct {
	SnapshotDate string              `json:"snapshot_date"`
	Overall      WeatherOverall      `json:"overall"`
	Stations     []WeatherStationRow `json:"stations"`
	Daily        []WeatherDailyRow   `json:"daily"`
}

func (h *APIHandler) WeatherDashboard(w http.ResponseWriter, r *http.Request) {
	meta, err := h.weatherMeta(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	latest, err := civil.ParseDate(meta.LatestDate)
	if err != nil {
		writeError(w, fmt.Sprintf("unexpected latest date %q: %v", meta.LatestDate, err), http.StatusInternalServerError)
		return
	}
	snap, days, err := parseWeatherParams(r, latest)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:weather:dash:%s:%d", snap, days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := weatherFlight.Do(key, func() (any, error) {
		data := WeatherDashboardData{
			SnapshotDate: snap.String(),
			Stations:     []WeatherStationRow{},
			Daily:        []WeatherDailyRow{},
		}
		start := snap.AddDays(-(days - 1))

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			stations, err := h.bq.GetWeatherSnapshot(ctx, snap)
			if err != nil {
				return err
			}
			if stations != nil {
				data.Stations = stations
			}
			return nil
		})
		g.Go(func() error {
			daily, err := h.bq.GetWeatherDaily(ctx, start, snap)
			if err != nil {
				return err
			}
			if daily != nil {
				data.Daily = daily
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.Overall = rollupWeatherOverall(data.Stations)
		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

func weatherPlace(s WeatherStationRow) string {
	if s.State != "" {
		return s.State + ", " + s.Country
	}
	return s.Country
}

// rollupWeatherOverall derives the KPI row from the snapshot rows. A station
// missing a metric is skipped for that pick but still counts as reporting.
func rollupWeatherOverall(stations []WeatherStationRow) WeatherOverall {
	overall := WeatherOverall{StationsReporting: len(stations)}
	for _, s := range stations {
		if s.TmaxC.Valid && (overall.Hottest == nil || s.TmaxC.Float64 > overall.Hottest.Value) {
			overall.Hottest = &WeatherExtreme{Station: s.Name, CountryState: weatherPlace(s), Value: s.TmaxC.Float64}
		}
		if s.TminC.Valid && (overall.Coldest == nil || s.TminC.Float64 < overall.Coldest.Value) {
			overall.Coldest = &WeatherExtreme{Station: s.Name, CountryState: weatherPlace(s), Value: s.TminC.Float64}
		}
		if s.PrcpMM.Valid && (overall.Wettest == nil || s.PrcpMM.Float64 > overall.Wettest.Value) {
			overall.Wettest = &WeatherExtreme{Station: s.Name, CountryState: weatherPlace(s), Value: s.PrcpMM.Float64}
		}
		if s.SnowMM.Valid && s.SnowMM.Float64 > 0 {
			overall.SnowStations++
		}
	}
	return overall
}
