package main

// BigQuery Open Data: NOAA GHCN-Daily (global land-station daily weather).
//
// Queries the yearly sharded tables `bigquery-public-data.ghcn_d.ghcnd_YYYY`.
// The tables are long-format (one row per station × date × element, values in
// tenths for temps/PRCP) and are neither partitioned nor clustered, so every
// query scans the referenced columns of the whole year table — cost is
// bounded by the handlers (≤31-day windows ⇒ at most two year tables) plus
// caching, not by pruning. Table names are built only from validated integer
// years; user input reaches SQL exclusively through query parameters.

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	ghcndProject        = "bigquery-public-data"
	ghcndDataset        = "ghcn_d"
	ghcndStationsTable  = "`bigquery-public-data.ghcn_d.ghcnd_stations`"
	weatherMaxBytesBill = 4 * 1024 * 1024 * 1024 // 4 GiB
)

func ghcndTable(year int) string {
	return fmt.Sprintf("`%s.%s.ghcnd_%d`", ghcndProject, ghcndDataset, year)
}

// ghcndFrom returns the FROM source covering [startYear, endYear]. Windows
// are ≤31 days, so at most two consecutive year tables are ever involved.
// The UNION ALL subquery selects only the needed columns to keep the scan
// (and bytes billed) minimal.
func ghcndFrom(startYear, endYear int) string {
	if startYear == endYear {
		return ghcndTable(startYear)
	}
	const cols = "SELECT id, date, element, value, qflag FROM "
	return fmt.Sprintf("(%s%s UNION ALL %s%s)",
		cols, ghcndTable(startYear), cols, ghcndTable(endYear))
}

// --- /meta: recent per-day coverage ---

// WeatherCoverageRow is one recent day's temperature-station count. MAX(date)
// alone is a trap: GHCN backfills over ~4 days, so the newest date can hold
// a near-empty network (observed: 3 stations, 0 TMAX on the latest day).
// The handler picks a usable default day from these counts.
type WeatherCoverageRow struct {
	Date         string `bigquery:"date"`
	TmaxStations int64  `bigquery:"tmax_stations"`
}

// GetWeatherTableYearAndLastMod resolves the active GHCN-Daily yearly table
// (falling back one year in early January before the new year's table exists)
// and returns its LastModifiedTime via a free tables.get metadata call.
func (b *BQClient) GetWeatherTableYearAndLastMod(ctx context.Context) (int, time.Time) {
	year := time.Now().UTC().Year()
	tbl := b.client.DatasetInProject(ghcndProject, ghcndDataset).Table(fmt.Sprintf("ghcnd_%d", year))
	md, err := tbl.Metadata(ctx)
	if err != nil {
		year--
		tbl = b.client.DatasetInProject(ghcndProject, ghcndDataset).Table(fmt.Sprintf("ghcnd_%d", year))
		md, _ = tbl.Metadata(ctx)
	}
	var lastMod time.Time
	if md != nil {
		lastMod = md.LastModifiedTime
	}
	return year, lastMod
}

// GetWeatherRecentCoverageForYear runs the 10-day TMAX coverage query on the
// specified yearly table. Because (id, date, element) is unique in GHCN-Daily,
// COUNTIF avoids scanning the 11-char id column (~48% fewer bytes scanned).
func (b *BQClient) GetWeatherRecentCoverageForYear(ctx context.Context, year int) ([]WeatherCoverageRow, error) {
	// A window ending in the first days of January would under-report because
	// it can't see into the previous year's table; acceptable for a default-
	// date heuristic (the fallbacks below still yield a valid day).
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', date) AS date,
			COUNTIF(element = 'TMAX' AND qflag IS NULL) AS tmax_stations
		FROM %s
		WHERE date > DATE_SUB((SELECT MAX(date) FROM %s), INTERVAL 10 DAY)
		GROUP BY date
		ORDER BY date DESC`, ghcndTable(year), ghcndTable(year)))
	q.MaxBytesBilled = weatherMaxBytesBill
	return collectRows[WeatherCoverageRow](q, ctx)
}

// GetWeatherRecentCoverage returns per-day TMAX station counts for the 10
// days up to MAX(date), newest first, along with the table's LastModifiedTime.
func (b *BQClient) GetWeatherRecentCoverage(ctx context.Context) ([]WeatherCoverageRow, time.Time, error) {
	year, lastMod := b.GetWeatherTableYearAndLastMod(ctx)
	rows, err := b.GetWeatherRecentCoverageForYear(ctx, year)
	return rows, lastMod, err
}

// --- /dashboard: one-day snapshot pivot (map + KPIs + leaderboards) ---

// WeatherStationRow is one station's pivoted metrics for the snapshot day.
// The station id is grouped on in SQL (names are not unique) but not
// serialized; `country` is the 2-letter FIPS prefix of the GHCN station id.
// Metric fields use bigquery.NullFloat64, which JSON-marshals to number|null.
type WeatherStationRow struct {
	Name      string               `json:"name" bigquery:"name"`
	State     string               `json:"state" bigquery:"state"`
	Country   string               `json:"country" bigquery:"country"`
	Latitude  float64              `json:"latitude" bigquery:"latitude"`
	Longitude float64              `json:"longitude" bigquery:"longitude"`
	TmaxC     bigquery.NullFloat64 `json:"tmax_c" bigquery:"tmax_c"`
	TminC     bigquery.NullFloat64 `json:"tmin_c" bigquery:"tmin_c"`
	PrcpMM    bigquery.NullFloat64 `json:"prcp_mm" bigquery:"prcp_mm"`
	SnowMM    bigquery.NullFloat64 `json:"snow_mm" bigquery:"snow_mm"`
}

func (b *BQClient) GetWeatherSnapshot(ctx context.Context, day civil.Date) ([]WeatherStationRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			COALESCE(s.name, t.id) AS name,
			COALESCE(s.state, '') AS state,
			SUBSTR(t.id, 1, 2) AS country,
			ROUND(s.latitude, 2) AS latitude,
			ROUND(s.longitude, 2) AS longitude,
			ROUND(MAX(IF(t.element = 'TMAX', t.value / 10, NULL)), 1) AS tmax_c,
			ROUND(MAX(IF(t.element = 'TMIN', t.value / 10, NULL)), 1) AS tmin_c,
			ROUND(MAX(IF(t.element = 'PRCP', t.value / 10, NULL)), 1) AS prcp_mm,
			MAX(IF(t.element = 'SNOW', t.value, NULL)) AS snow_mm
		FROM %s t
		JOIN %s s ON s.id = t.id
		WHERE t.date = @snap_date
			AND t.element IN ('TMAX', 'TMIN', 'PRCP', 'SNOW')
			AND t.qflag IS NULL
			AND s.latitude IS NOT NULL
			AND s.longitude IS NOT NULL
		GROUP BY t.id, name, state, country, latitude, longitude`,
		ghcndTable(day.Year), ghcndStationsTable))
	q.Parameters = []bigquery.QueryParameter{{Name: "snap_date", Value: day}}
	q.MaxBytesBilled = weatherMaxBytesBill
	return collectRows[WeatherStationRow](q, ctx)
}

// --- /dashboard: daily trend over the trailing window ---

// WeatherDailyRow is one day's network-wide aggregates. Averages are means
// across that element's reporting stations (a network mean, not a physical
// global average — the frontend labels it accordingly).
type WeatherDailyRow struct {
	Date         string               `json:"date" bigquery:"date"`
	AvgTmaxC     bigquery.NullFloat64 `json:"avg_tmax_c" bigquery:"avg_tmax_c"`
	AvgTminC     bigquery.NullFloat64 `json:"avg_tmin_c" bigquery:"avg_tmin_c"`
	AvgPrcpMM    bigquery.NullFloat64 `json:"avg_prcp_mm" bigquery:"avg_prcp_mm"`
	TmaxStations int64                `json:"tmax_stations" bigquery:"tmax_stations"`
	PrcpStations int64                `json:"prcp_stations" bigquery:"prcp_stations"`
}

func (b *BQClient) GetWeatherDaily(ctx context.Context, start, end civil.Date) ([]WeatherDailyRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', date) AS date,
			ROUND(AVG(IF(element = 'TMAX', value / 10, NULL)), 2) AS avg_tmax_c,
			ROUND(AVG(IF(element = 'TMIN', value / 10, NULL)), 2) AS avg_tmin_c,
			ROUND(AVG(IF(element = 'PRCP', value / 10, NULL)), 2) AS avg_prcp_mm,
			COUNTIF(element = 'TMAX') AS tmax_stations,
			COUNTIF(element = 'PRCP') AS prcp_stations
		FROM %s
		WHERE date BETWEEN @start_date AND @end_date
			AND element IN ('TMAX', 'TMIN', 'PRCP')
			AND qflag IS NULL
		GROUP BY date
		ORDER BY date`, ghcndFrom(start.Year, end.Year)))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
	}
	q.MaxBytesBilled = weatherMaxBytesBill
	return collectRows[WeatherDailyRow](q, ctx)
}

type weatherDashboardBundle struct {
	Daily    []WeatherDailyRow   `bigquery:"daily"`
	Stations []WeatherStationRow `bigquery:"stations"`
}

// GetWeatherDashboardConsolidated scans ghcnd_YYYY once to produce both the
// trailing daily trend and the snapshot day's station pivot (~50% fewer bytes).
func (b *BQClient) GetWeatherDashboardConsolidated(ctx context.Context, start, snap civil.Date) ([]WeatherStationRow, []WeatherDailyRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		WITH base AS (
			SELECT id, date, element, value
			FROM %s
			WHERE date BETWEEN @start_date AND @snap_date
				AND element IN ('TMAX', 'TMIN', 'PRCP', 'SNOW')
				AND qflag IS NULL
		)
		SELECT
			ARRAY(
				SELECT AS STRUCT
					FORMAT_DATE('%%Y-%%m-%%d', date) AS date,
					ROUND(AVG(IF(element = 'TMAX', value / 10, NULL)), 2) AS avg_tmax_c,
					ROUND(AVG(IF(element = 'TMIN', value / 10, NULL)), 2) AS avg_tmin_c,
					ROUND(AVG(IF(element = 'PRCP', value / 10, NULL)), 2) AS avg_prcp_mm,
					COUNTIF(element = 'TMAX') AS tmax_stations,
					COUNTIF(element = 'PRCP') AS prcp_stations
				FROM base
				WHERE element IN ('TMAX', 'TMIN', 'PRCP')
				GROUP BY date
				ORDER BY date
			) AS daily,
			ARRAY(
				SELECT AS STRUCT
					COALESCE(s.name, t.id) AS name,
					COALESCE(s.state, '') AS state,
					SUBSTR(t.id, 1, 2) AS country,
					ROUND(s.latitude, 2) AS latitude,
					ROUND(s.longitude, 2) AS longitude,
					ROUND(MAX(IF(t.element = 'TMAX', t.value / 10, NULL)), 1) AS tmax_c,
					ROUND(MAX(IF(t.element = 'TMIN', t.value / 10, NULL)), 1) AS tmin_c,
					ROUND(MAX(IF(t.element = 'PRCP', t.value / 10, NULL)), 1) AS prcp_mm,
					MAX(IF(t.element = 'SNOW', t.value, NULL)) AS snow_mm
				FROM base t
				JOIN %s s ON s.id = t.id
				WHERE t.date = @snap_date
					AND s.latitude IS NOT NULL
					AND s.longitude IS NOT NULL
				GROUP BY t.id, name, state, country, latitude, longitude
			) AS stations`, ghcndFrom(start.Year, snap.Year), ghcndStationsTable))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "snap_date", Value: snap},
	}
	q.MaxBytesBilled = weatherMaxBytesBill
	rows, err := collectRows[weatherDashboardBundle](q, ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return []WeatherStationRow{}, []WeatherDailyRow{}, nil
	}
	b0 := rows[0]
	if b0.Stations == nil {
		b0.Stations = []WeatherStationRow{}
	}
	if b0.Daily == nil {
		b0.Daily = []WeatherDailyRow{}
	}
	return b0.Stations, b0.Daily, nil
}

