package main

// BigQuery Open Data: SEM Insights.
//
// Joins the Google Trends rising tables against the top-25 tables inside a
// single pinned snapshot to surface "arbitrage" keywords: terms with high
// week-over-week momentum (percent_gain) that have not yet reached the
// mainstream top-25 chart (volume_rank = 0 in the result). Two markets:
//   - global: international_top_terms / international_top_rising_terms,
//     filtered to one country, geo spread counted in regions.
//   - us: top_terms / top_rising_terms at Nielsen DMA grain, optionally
//     filtered to one DMA (empty geo = national view across 210 DMAs).
//
// Every query filters on the partition key refresh_date and pins
// week = MAX(week) within that partition — the partition carries the full
// 5-year weekly history, so an unpinned scan would mix snapshots.

import (
	"context"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	semUSTopTable    = "`bigquery-public-data.google_trends.top_terms`"
	semUSRisingTable = "`bigquery-public-data.google_trends.top_rising_terms`"
	semHourlyTable   = "`bigquery-public-data.google_trends_hourly.top_terms_hourly`"
)

// --- Meta: US partition dates and DMA list (global reuses the trends meta) ---

// GetSemUSRefreshDates returns recent US-table partition dates, newest first.
// The US tables publish on their own schedule, so the dates are queried
// separately from the international ones.
func (b *BQClient) GetSemUSRefreshDates(ctx context.Context) ([]string, error) {
	q := b.client.Query(`
		SELECT FORMAT_DATE('%Y-%m-%d', refresh_date) AS d
		FROM ` + semUSTopTable + `
		WHERE refresh_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 45 DAY)
		GROUP BY d
		ORDER BY d DESC`)

	type dateRow struct {
		D string `bigquery:"d"`
	}
	rows, err := collectRows[dateRow](q, ctx)
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(rows))
	for _, r := range rows {
		dates = append(dates, r.D)
	}
	return dates, nil
}

type SemDMA struct {
	Name string `json:"name" bigquery:"name"`
	ID   int64  `json:"id" bigquery:"id"`
}

func (b *BQClient) GetSemDMAs(ctx context.Context, refreshDate civil.Date) ([]SemDMA, error) {
	q := b.client.Query(`
		SELECT dma_name AS name, dma_id AS id
		FROM ` + semUSTopTable + `
		WHERE refresh_date = @refresh_date
		GROUP BY name, id
		ORDER BY name ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
	}
	return collectRows[SemDMA](q, ctx)
}

// --- Widget 1 + 4: breakout matrix / opportunity table rows ---

// SemMatrixRow is one rising term joined against the top-25 chart of the same
// snapshot. VolumeRank 0 means the term is not charting anywhere in the
// selected geo's top 25 ("Unranked" — momentum before mainstream volume).
// Score 0 means the rising table has no normalized score yet ("too new").
type SemMatrixRow struct {
	Term        string `json:"term" bigquery:"term"`
	VolumeRank  int64  `json:"volume_rank" bigquery:"volume_rank"`
	PercentGain int64  `json:"percent_gain" bigquery:"percent_gain"`
	Score       int64  `json:"score" bigquery:"score"`
	GeoSpread   int64  `json:"geo_spread" bigquery:"geo_spread"`
	RisingRank  int64  `json:"rising_rank" bigquery:"rising_rank"`
}

// GetSemMatrixUS returns the rising→top-25 join for the US market. Empty dma
// aggregates nationally: MAX(percent_gain) is the strongest local breakout,
// geo_spread counts the DMAs where the term is rising.
func (b *BQClient) GetSemMatrixUS(ctx context.Context, refreshDate civil.Date, dma string) ([]SemMatrixRow, error) {
	q := b.client.Query(`
		WITH rising AS (
			SELECT
				term,
				CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain,
				CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
				COUNT(DISTINCT dma_name) AS geo_spread,
				MIN(rank) AS rising_rank
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND week = (SELECT MAX(week) FROM ` + semUSRisingTable + ` WHERE refresh_date = @refresh_date)
			  AND (@dma = '' OR dma_name = @dma)
			GROUP BY term
		),
		charting AS (
			SELECT term, MIN(rank) AS volume_rank
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND week = (SELECT MAX(week) FROM ` + semUSTopTable + ` WHERE refresh_date = @refresh_date)
			  AND (@dma = '' OR dma_name = @dma)
			GROUP BY term
		)
		SELECT
			r.term,
			COALESCE(c.volume_rank, 0) AS volume_rank,
			r.percent_gain,
			r.score,
			r.geo_spread,
			r.rising_rank
		FROM rising r
		LEFT JOIN charting c ON LOWER(r.term) = LOWER(c.term)
		ORDER BY r.percent_gain DESC
		LIMIT 100`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "dma", Value: dma},
	}
	return collectRows[SemMatrixRow](q, ctx)
}

// GetSemMatrixGlobal returns the rising→top-25 join for one country in the
// international tables; geo_spread counts the country's regions.
func (b *BQClient) GetSemMatrixGlobal(ctx context.Context, refreshDate civil.Date, countryCode string) ([]SemMatrixRow, error) {
	q := b.client.Query(`
		WITH rising AS (
			SELECT
				term,
				CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain,
				CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
				COUNT(DISTINCT region_name) AS geo_spread,
				MIN(rank) AS rising_rank
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = (SELECT MAX(week) FROM ` + trendsRisingTable + ` WHERE refresh_date = @refresh_date)
			GROUP BY term
		),
		charting AS (
			SELECT term, MIN(rank) AS volume_rank
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = (SELECT MAX(week) FROM ` + trendsTopTable + ` WHERE refresh_date = @refresh_date)
			GROUP BY term
		)
		SELECT
			r.term,
			COALESCE(c.volume_rank, 0) AS volume_rank,
			r.percent_gain,
			r.score,
			r.geo_spread,
			r.rising_rank
		FROM rising r
		LEFT JOIN charting c ON LOWER(r.term) = LOWER(c.term)
		ORDER BY r.percent_gain DESC
		LIMIT 100`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
	}
	return collectRows[SemMatrixRow](q, ctx)
}

// --- Widget 2: per-geo demand for one term (bid-modifier table) ---

// SemGeoRow is one geo's demand for a single term: score is the normalized
// search interest (the higher of the top-25 and rising readings — both are
// 0–100), RisingRank/PercentGain are 0 when the term is not rising there.
// The suggested Google Ads location bid modifier is derived client-side from
// score vs the average across geos.
type SemGeoRow struct {
	Geo         string `json:"geo" bigquery:"geo"`
	Score       int64  `json:"score" bigquery:"score"`
	RisingRank  int64  `json:"rising_rank" bigquery:"rising_rank"`
	PercentGain int64  `json:"percent_gain" bigquery:"percent_gain"`
}

// GetSemGeoUS returns one term's demand across all 210 DMAs (always national:
// the point of the widget is choosing where to bid).
func (b *BQClient) GetSemGeoUS(ctx context.Context, refreshDate civil.Date, term string) ([]SemGeoRow, error) {
	q := b.client.Query(`
		WITH charting AS (
			SELECT dma_name AS geo, CAST(COALESCE(AVG(score), 0) AS INT64) AS score
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND week = (SELECT MAX(week) FROM ` + semUSTopTable + ` WHERE refresh_date = @refresh_date)
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		),
		rising AS (
			SELECT dma_name AS geo, CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
				MIN(rank) AS rising_rank, CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND week = (SELECT MAX(week) FROM ` + semUSRisingTable + ` WHERE refresh_date = @refresh_date)
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		)
		SELECT
			COALESCE(c.geo, r.geo) AS geo,
			GREATEST(COALESCE(c.score, 0), COALESCE(r.score, 0)) AS score,
			COALESCE(r.rising_rank, 0) AS rising_rank,
			COALESCE(r.percent_gain, 0) AS percent_gain
		FROM charting c
		FULL OUTER JOIN rising r ON c.geo = r.geo
		ORDER BY score DESC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "term", Value: term},
	}
	return collectRows[SemGeoRow](q, ctx)
}

// GetSemGeoGlobal returns one term's demand across the selected country's regions.
func (b *BQClient) GetSemGeoGlobal(ctx context.Context, refreshDate civil.Date, countryCode, term string) ([]SemGeoRow, error) {
	q := b.client.Query(`
		WITH charting AS (
			SELECT region_name AS geo, CAST(COALESCE(AVG(score), 0) AS INT64) AS score
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = (SELECT MAX(week) FROM ` + trendsTopTable + ` WHERE refresh_date = @refresh_date)
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		),
		rising AS (
			SELECT region_name AS geo, CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
				MIN(rank) AS rising_rank, CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = (SELECT MAX(week) FROM ` + trendsRisingTable + ` WHERE refresh_date = @refresh_date)
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		)
		SELECT
			COALESCE(c.geo, r.geo) AS geo,
			GREATEST(COALESCE(c.score, 0), COALESCE(r.score, 0)) AS score,
			COALESCE(r.rising_rank, 0) AS rising_rank,
			COALESCE(r.percent_gain, 0) AS percent_gain
		FROM charting c
		FULL OUTER JOIN rising r ON c.geo = r.geo
		ORDER BY score DESC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
		{Name: "term", Value: term},
	}
	return collectRows[SemGeoRow](q, ctx)
}

// --- Widget 5: US real-time hourly pulse ---

// SemPulseRow is one term from the latest intraday snapshot. Consecutive
// hourly snapshots carry fully disjoint top-25 sets (verified live: zero term
// overlap across 3 days of snapshots), so there is no cross-snapshot Δrank;
// the acceleration signal is instead week-over-week within the snapshot's own
// weekly history: Score (current, partial week) vs PrevWeekScore.
type SemPulseRow struct {
	Term          string `json:"term" bigquery:"term"`
	Rank          int64  `json:"rank" bigquery:"rank"`
	Score         int64  `json:"score" bigquery:"score"`
	PrevWeekScore int64  `json:"prev_week_score" bigquery:"prev_week_score"`
	SnapshotTime  string `json:"-" bigquery:"snapshot_time"`
}

// GetSemPulse returns the national top 25 from the newest hourly snapshot
// (~4 snapshots/day vs the daily tables' 1–2 day lag). The 24 h lookback
// bound keeps the HOUR-partitioned scan pruned (~50 MB).
func (b *BQClient) GetSemPulse(ctx context.Context) ([]SemPulseRow, error) {
	q := b.client.Query(`
		WITH latest AS (
			SELECT term, rank, week, score, refresh_time,
				DENSE_RANK() OVER (ORDER BY refresh_time DESC) AS snap_no
			FROM ` + semHourlyTable + `
			WHERE refresh_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 1 DAY)
		),
		weekly AS (
			SELECT term, MIN(rank) AS rank, week,
				CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
				MAX(refresh_time) AS refresh_time,
				DENSE_RANK() OVER (PARTITION BY term ORDER BY week DESC) AS wk_no
			FROM latest
			WHERE snap_no = 1
			GROUP BY term, week
		)
		SELECT c.term, c.rank, c.score,
			COALESCE(p.score, 0) AS prev_week_score,
			FORMAT_DATETIME('%Y-%m-%dT%H:%M:%S', c.refresh_time) AS snapshot_time
		FROM (SELECT * FROM weekly WHERE wk_no = 1) c
		LEFT JOIN (SELECT term, score FROM weekly WHERE wk_no = 2) p ON c.term = p.term
		ORDER BY c.rank ASC
		LIMIT 25`)
	return collectRows[SemPulseRow](q, ctx)
}

// --- Widget 6: term drill-down (weekly history from the pinned partition) ---

type SemHistoryPoint struct {
	Week  string `json:"week" bigquery:"week"`
	Score int64  `json:"score" bigquery:"score"`
}

// GetSemTermHistoryUS returns the term's full weekly history from one US
// partition. The rising table is unioned in because arbitrage terms — the
// main drill-down target — often have no top-25 rows yet.
func (b *BQClient) GetSemTermHistoryUS(ctx context.Context, refreshDate civil.Date, term string) ([]SemHistoryPoint, error) {
	q := b.client.Query(`
		WITH pts AS (
			SELECT week, score FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date AND LOWER(term) = LOWER(@term)
			UNION ALL
			SELECT week, score FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date AND LOWER(term) = LOWER(@term)
		)
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(COALESCE(AVG(score), 0) AS INT64) AS score
		FROM pts
		GROUP BY week
		ORDER BY week ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "term", Value: term},
	}
	return collectRows[SemHistoryPoint](q, ctx)
}

// GetSemTermHistoryGlobal is the international-table variant, scoped to one country.
func (b *BQClient) GetSemTermHistoryGlobal(ctx context.Context, refreshDate civil.Date, countryCode, term string) ([]SemHistoryPoint, error) {
	q := b.client.Query(`
		WITH pts AS (
			SELECT week, score FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date AND country_code = @country_code AND LOWER(term) = LOWER(@term)
			UNION ALL
			SELECT week, score FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date AND country_code = @country_code AND LOWER(term) = LOWER(@term)
		)
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(COALESCE(AVG(score), 0) AS INT64) AS score
		FROM pts
		GROUP BY week
		ORDER BY week ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
		{Name: "term", Value: term},
	}
	return collectRows[SemHistoryPoint](q, ctx)
}

// --- Widget 3: brand safety (GDELT news tone + conflict share) ---

// semActorCountry maps the Trends ISO-3166 alpha-2 country codes to the
// actor codes GDELT events actually carry (ISO3-style CAMEO; verified live
// 2026-08-18 against Actor1CountryCode — e.g. ZAF not SAF, GBR not UKG).
// Trends geo is region/DMA-grained but GDELT tone is country-grained, so the
// safety signal is always market-level.
var semActorCountry = map[string]string{
	"AR": "ARG", "AT": "AUT", "AU": "AUS", "BE": "BEL", "BR": "BRA",
	"CA": "CAN", "CH": "CHE", "CL": "CHL", "CO": "COL", "CZ": "CZE",
	"DE": "DEU", "DK": "DNK", "EG": "EGY", "ES": "ESP", "FI": "FIN",
	"FR": "FRA", "GB": "GBR", "HU": "HUN", "ID": "IDN", "IL": "ISR",
	"IN": "IND", "IT": "ITA", "JP": "JPN", "KR": "KOR", "MX": "MEX",
	"MY": "MYS", "NG": "NGA", "NL": "NLD", "NO": "NOR", "NZ": "NZL",
	"PH": "PHL", "PL": "POL", "PT": "PRT", "RO": "ROU", "SA": "SAU",
	"SE": "SWE", "TH": "THA", "TR": "TUR", "TW": "TWN", "UA": "UKR",
	"US": "USA", "VN": "VNM", "ZA": "ZAF",
}

// SemSafetyRow is one day of news context for a market. ConflictShare is the
// fraction of events in CAMEO QuadClass 3/4 (verbal/material conflict).
type SemSafetyRow struct {
	IngestDate    string  `json:"ingest_date" bigquery:"ingest_date"`
	EventCount    int64   `json:"event_count" bigquery:"event_count"`
	AvgTone       float64 `json:"avg_tone" bigquery:"avg_tone"`
	ConflictShare float64 `json:"conflict_share" bigquery:"conflict_share"`
}

// GetSemSafetyDaily mirrors GetGdeltCountryDaily (which cannot be reused
// as-is: it lacks the conflict share that drives the red threshold), adding
// COUNTIF(QuadClass IN (3,4)) over the same actor-country partition scan.
func (b *BQClient) GetSemSafetyDaily(ctx context.Context, start, end civil.Date, country string) ([]SemSafetyRow, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone,
			ROUND(SAFE_DIVIDE(COUNTIF(QuadClass IN (3, 4)), COUNT(1)), 4) AS conflict_share
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND ` + gdeltInvolvesCountry + `
		GROUP BY 1
		ORDER BY 1`)
	q.Parameters = gdeltCountryParams(start, end, country)
	return collectRows[SemSafetyRow](q, ctx)
}
