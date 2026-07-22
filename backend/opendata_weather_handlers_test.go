package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

func TestWeatherParamsValidation(t *testing.T) {
	latest := civil.Date{Year: 2026, Month: 7, Day: 21}

	tests := []struct {
		name     string
		query    string
		wantErr  string
		wantDays int
	}{
		{name: "valid default days", query: "date=2026-07-21", wantDays: 30},
		{name: "valid explicit days", query: "date=2026-07-01&days=7", wantDays: 7},
		{name: "valid max days", query: "date=1900-01-01&days=31", wantDays: 31},
		{name: "missing date", query: "", wantErr: "invalid date"},
		{name: "garbage date", query: "date=not-a-date", wantErr: "invalid date"},
		{name: "date before floor", query: "date=1899-12-31", wantErr: "on or after 1900-01-01"},
		{name: "date after latest", query: "date=2026-07-22", wantErr: "after the latest observation"},
		{name: "days too small", query: "date=2026-07-21&days=6", wantErr: "between 7 and 31"},
		{name: "days too large", query: "date=2026-07-21&days=32", wantErr: "between 7 and 31"},
		{name: "days not numeric", query: "date=2026-07-21&days=abc", wantErr: "invalid days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/opendata/weather/dashboard?"+tt.query, nil)
			_, days, err := parseWeatherParams(r, latest)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if days != tt.wantDays {
				t.Fatalf("want days %d, got %d", tt.wantDays, days)
			}
		})
	}
}

// The trend window must touch exactly the year tables it spans: one table
// mid-year, a two-table UNION ALL across a year boundary — never a wildcard.
func TestGhcndFrom(t *testing.T) {
	single := ghcndFrom(2026, 2026)
	if single != "`bigquery-public-data.ghcn_d.ghcnd_2026`" {
		t.Fatalf("single year: unexpected FROM %q", single)
	}

	cross := ghcndFrom(2025, 2026)
	if !strings.Contains(cross, "ghcnd_2025") || !strings.Contains(cross, "ghcnd_2026") {
		t.Fatalf("cross year: missing table refs in %q", cross)
	}
	if !strings.Contains(cross, "UNION ALL") {
		t.Fatalf("cross year: expected UNION ALL in %q", cross)
	}
	if strings.Contains(cross, "ghcnd_*") {
		t.Fatalf("cross year: wildcard must never be used: %q", cross)
	}
}

// The default snapshot day must skip the backfilling tail: MAX(date) can
// hold a near-empty network (observed live: 3 stations, 0 TMAX), which
// renders every map/KPI widget blank.
func TestPickWeatherDefaultDate(t *testing.T) {
	tests := []struct {
		name string
		rows []WeatherCoverageRow // newest first, as the query returns them
		want string
	}{
		{
			name: "skips sparse tail to first settled day",
			rows: []WeatherCoverageRow{
				{Date: "2026-07-21", TmaxStations: 0},
				{Date: "2026-07-20", TmaxStations: 1495},
				{Date: "2026-07-19", TmaxStations: 3961},
				{Date: "2026-07-18", TmaxStations: 5904},
			},
			want: "2026-07-19",
		},
		{
			name: "latest day already settled",
			rows: []WeatherCoverageRow{
				{Date: "2026-07-21", TmaxStations: 6000},
				{Date: "2026-07-20", TmaxStations: 6100},
			},
			want: "2026-07-21",
		},
		{
			name: "nothing settled falls back to best-covered day",
			rows: []WeatherCoverageRow{
				{Date: "2026-07-21", TmaxStations: 10},
				{Date: "2026-07-20", TmaxStations: 900},
				{Date: "2026-07-19", TmaxStations: 400},
			},
			want: "2026-07-20",
		},
		{name: "empty input yields empty", rows: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickWeatherDefaultDate(tt.rows); got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func nf(v float64) bigquery.NullFloat64 {
	return bigquery.NullFloat64{Float64: v, Valid: true}
}

// KPI picks must skip stations missing a metric while still counting them
// as reporting — otherwise sparse elements (snow, precip) would distort the
// headline numbers.
func TestRollupWeatherOverall(t *testing.T) {
	stations := []WeatherStationRow{
		{Name: "DEATH VALLEY", State: "CA", Country: "US", TmaxC: nf(48.9), TminC: nf(31.0), PrcpMM: nf(0)},
		{Name: "VOSTOK", Country: "AY", TmaxC: nf(-55.1), TminC: nf(-71.2), SnowMM: nf(20)},
		{Name: "CHERRAPUNJI", Country: "IN", PrcpMM: nf(410.5)},          // no temps: never hottest/coldest
		{Name: "NO METRICS", Country: "XX"},                              // reports nothing usable
		{Name: "ZERO SNOW", Country: "CA", SnowMM: nf(0), TminC: nf(-2)}, // snow 0 ⇒ not a snow station
	}

	got := rollupWeatherOverall(stations)

	if got.StationsReporting != 5 {
		t.Errorf("stations_reporting: want 5, got %d", got.StationsReporting)
	}
	if got.Hottest == nil || got.Hottest.Station != "DEATH VALLEY" || got.Hottest.Value != 48.9 {
		t.Errorf("hottest: want DEATH VALLEY 48.9, got %+v", got.Hottest)
	}
	if got.Hottest.CountryState != "CA, US" {
		t.Errorf("hottest place: want %q, got %q", "CA, US", got.Hottest.CountryState)
	}
	if got.Coldest == nil || got.Coldest.Station != "VOSTOK" || got.Coldest.Value != -71.2 {
		t.Errorf("coldest: want VOSTOK -71.2, got %+v", got.Coldest)
	}
	if got.Coldest.CountryState != "AY" {
		t.Errorf("coldest place: want %q, got %q", "AY", got.Coldest.CountryState)
	}
	if got.Wettest == nil || got.Wettest.Station != "CHERRAPUNJI" || got.Wettest.Value != 410.5 {
		t.Errorf("wettest: want CHERRAPUNJI 410.5, got %+v", got.Wettest)
	}
	if got.SnowStations != 1 {
		t.Errorf("snow_stations: want 1, got %d", got.SnowStations)
	}
}

func TestRollupWeatherOverallEmpty(t *testing.T) {
	got := rollupWeatherOverall(nil)
	if got.StationsReporting != 0 || got.Hottest != nil || got.Coldest != nil || got.Wettest != nil || got.SnowStations != 0 {
		t.Fatalf("empty rollup should be all-zero, got %+v", got)
	}
}
