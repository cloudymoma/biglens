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
// week = MAX(week) within that partition (the W2 geo table pins the latest
// complete week instead, see semLatestCompleteWeek) — the partition carries
// the full 5-year weekly history, so an unpinned scan would mix snapshots.

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
// separately from the international ones via INFORMATION_SCHEMA.PARTITIONS.
func (b *BQClient) GetSemUSRefreshDates(ctx context.Context) ([]string, error) {
	return b.getTrendsTablePartitionDates(ctx, "top_terms")
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
// Score 0 means the rising table has no measurable score in the latest complete week.
type SemMatrixRow struct {
	Term        string `json:"term" bigquery:"term"`
	VolumeRank  int64  `json:"volume_rank" bigquery:"volume_rank"`
	PercentGain int64  `json:"percent_gain" bigquery:"percent_gain"`
	Score       int64  `json:"score" bigquery:"score"`
	GeoSpread   int64  `json:"geo_spread" bigquery:"geo_spread"`
	RisingRank  int64  `json:"rising_rank" bigquery:"rising_rank"`
}

// GetSemMatrixUS returns the rising→top-25 join for the US market in the
// snapshot's latest complete week. Note that percent_gain and rank are
// national constants replicated across all 210 DMAs; selecting a DMA narrows
// score to that metro, while geo_spread counts DMAs with a non-null score.
func (b *BQClient) GetSemMatrixUS(ctx context.Context, refreshDate civil.Date, dma string) ([]SemMatrixRow, error) {
	q := b.client.Query(`
		WITH rising AS (
			SELECT
				term,
				CAST(COALESCE(ANY_VALUE(percent_gain), 0) AS INT64) AS percent_gain,
				CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				COUNT(DISTINCT IF(score IS NOT NULL, dma_name, NULL)) AS geo_spread,
				MIN(rank) AS rising_rank
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSRisingTable) + `
			  AND (@dma = '' OR dma_name = @dma)
			GROUP BY term
		),
		charting AS (
			SELECT term, MIN(rank) AS volume_rank
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSTopTable) + `
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
// international tables; geo_spread counts the country's regions with a
// non-null score in the latest complete week.
func (b *BQClient) GetSemMatrixGlobal(ctx context.Context, refreshDate civil.Date, countryCode string) ([]SemMatrixRow, error) {
	q := b.client.Query(`
		WITH rising AS (
			SELECT
				term,
				CAST(COALESCE(ANY_VALUE(percent_gain), 0) AS INT64) AS percent_gain,
				CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				COUNT(DISTINCT IF(score IS NOT NULL, region_name, NULL)) AS geo_spread,
				MIN(rank) AS rising_rank
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = ` + semLatestCompleteWeek(trendsRisingTable) + `
			GROUP BY term
		),
		charting AS (
			SELECT term, MIN(rank) AS volume_rank
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = ` + semLatestCompleteWeek(trendsTopTable) + `
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

// --- Widget 2: per-geo interest for one term ---

// SemGeoRow is one geo's reading for a single term in the snapshot's latest
// complete week. Score is the higher of the top-25 and rising readings (both
// 0–100) and, like every Trends score, is indexed to that geo's own 5-year
// peak: it says how close the term is to its local high, not how much demand
// the geo has, so scores are not comparable across geos and no bid
// adjustment can be derived from them. Score is NULL (JSON null) when Trends
// reports no value for the geo that week (below its reporting threshold).
// RisingRank/PercentGain are 0 when the term is not rising there. Week is
// reported once per response (SemGeoData.Week).
type SemGeoRow struct {
	Geo         string             `json:"geo" bigquery:"geo"`
	Score       bigquery.NullInt64 `json:"score" bigquery:"score"`
	RisingRank  int64              `json:"rising_rank" bigquery:"rising_rank"`
	PercentGain int64              `json:"percent_gain" bigquery:"percent_gain"`
	Week        string             `json:"-" bigquery:"week"`
}

// semLatestCompleteWeek is a scalar subquery for the newest week in table's
// refresh_date partition that has fully elapsed. Weeks start on Sunday, so
// week W is complete once W + 7 days <= refresh_date. The US partitions also
// carry the in-progress week that starts on the refresh day itself (partition
// 2026-10-04 ends with week 2026-10-04), where most DMAs have no score yet.
// The partition filter stays a plain parameter comparison so the scan is
// pruned to the one partition.
func semLatestCompleteWeek(table string) string {
	return `(SELECT MAX(week) FROM ` + table + `
				WHERE refresh_date = @refresh_date
				  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date)`
}

// GetSemGeoUS returns one term's reading in each of the 210 DMAs (always
// national: the widget lists every geo of the market).
func (b *BQClient) GetSemGeoUS(ctx context.Context, refreshDate civil.Date, term string) ([]SemGeoRow, error) {
	return b.GetSemGeoUSSourced(ctx, refreshDate, term, "")
}

// GetSemGeoUSSourced scans only the specified table when source is "rising" or
// "top", avoiding a redundant second-table scan when the caller already knows
// which table the term came from.
func (b *BQClient) GetSemGeoUSSourced(ctx context.Context, refreshDate civil.Date, term, source string) ([]SemGeoRow, error) {
	var sql string
	switch source {
	case "rising":
		sql = `
		SELECT
			dma_name AS geo,
			FORMAT_DATE('%Y-%m-%d', ANY_VALUE(week)) AS week,
			CAST(AVG(score) AS INT64) AS score,
			COALESCE(MIN(rank), 0) AS rising_rank,
			CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
		FROM ` + semUSRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND week = ` + semLatestCompleteWeek(semUSRisingTable) + `
		  AND LOWER(term) = LOWER(@term)
		GROUP BY geo
		ORDER BY score DESC NULLS LAST, geo`
	case "top":
		sql = `
		SELECT
			dma_name AS geo,
			FORMAT_DATE('%Y-%m-%d', ANY_VALUE(week)) AS week,
			CAST(AVG(score) AS INT64) AS score,
			0 AS rising_rank,
			0 AS percent_gain
		FROM ` + semUSTopTable + `
		WHERE refresh_date = @refresh_date
		  AND week = ` + semLatestCompleteWeek(semUSTopTable) + `
		  AND LOWER(term) = LOWER(@term)
		GROUP BY geo
		ORDER BY score DESC NULLS LAST, geo`
	default:
		sql = `
		WITH charting AS (
			SELECT dma_name AS geo, ANY_VALUE(week) AS week, CAST(AVG(score) AS INT64) AS score
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSTopTable) + `
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		),
		rising AS (
			SELECT dma_name AS geo, ANY_VALUE(week) AS week, CAST(AVG(score) AS INT64) AS score,
				MIN(rank) AS rising_rank, CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSRisingTable) + `
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		)
		SELECT
			COALESCE(c.geo, r.geo) AS geo,
			FORMAT_DATE('%Y-%m-%d', COALESCE(c.week, r.week)) AS week,
			GREATEST(COALESCE(c.score, r.score), COALESCE(r.score, c.score)) AS score,
			COALESCE(r.rising_rank, 0) AS rising_rank,
			COALESCE(r.percent_gain, 0) AS percent_gain
		FROM charting c
		FULL OUTER JOIN rising r ON c.geo = r.geo
		ORDER BY score DESC NULLS LAST, geo`
	}
	q := b.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "term", Value: term},
	}
	return collectRows[SemGeoRow](q, ctx)
}

// GetSemGeoGlobal returns one term's reading in each of the selected
// country's regions.
func (b *BQClient) GetSemGeoGlobal(ctx context.Context, refreshDate civil.Date, countryCode, term string) ([]SemGeoRow, error) {
	return b.GetSemGeoGlobalSourced(ctx, refreshDate, countryCode, term, "")
}

// GetSemGeoGlobalSourced scans only the specified table when source is "rising"
// or "top".
func (b *BQClient) GetSemGeoGlobalSourced(ctx context.Context, refreshDate civil.Date, countryCode, term, source string) ([]SemGeoRow, error) {
	var sql string
	switch source {
	case "rising":
		sql = `
		SELECT
			region_name AS geo,
			FORMAT_DATE('%Y-%m-%d', ANY_VALUE(week)) AS week,
			CAST(AVG(score) AS INT64) AS score,
			COALESCE(MIN(rank), 0) AS rising_rank,
			CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
		FROM ` + trendsRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = ` + semLatestCompleteWeek(trendsRisingTable) + `
		  AND LOWER(term) = LOWER(@term)
		GROUP BY geo
		ORDER BY score DESC NULLS LAST, geo`
	case "top":
		sql = `
		SELECT
			region_name AS geo,
			FORMAT_DATE('%Y-%m-%d', ANY_VALUE(week)) AS week,
			CAST(AVG(score) AS INT64) AS score,
			0 AS rising_rank,
			0 AS percent_gain
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = ` + semLatestCompleteWeek(trendsTopTable) + `
		  AND LOWER(term) = LOWER(@term)
		GROUP BY geo
		ORDER BY score DESC NULLS LAST, geo`
	default:
		sql = `
		WITH charting AS (
			SELECT region_name AS geo, ANY_VALUE(week) AS week, CAST(AVG(score) AS INT64) AS score
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = ` + semLatestCompleteWeek(trendsTopTable) + `
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		),
		rising AS (
			SELECT region_name AS geo, ANY_VALUE(week) AS week, CAST(AVG(score) AS INT64) AS score,
				MIN(rank) AS rising_rank, CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND week = ` + semLatestCompleteWeek(trendsRisingTable) + `
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		)
		SELECT
			COALESCE(c.geo, r.geo) AS geo,
			FORMAT_DATE('%Y-%m-%d', COALESCE(c.week, r.week)) AS week,
			GREATEST(COALESCE(c.score, r.score), COALESCE(r.score, c.score)) AS score,
			COALESCE(r.rising_rank, 0) AS rising_rank,
			COALESCE(r.percent_gain, 0) AS percent_gain
		FROM charting c
		FULL OUTER JOIN rising r ON c.geo = r.geo
		ORDER BY score DESC NULLS LAST, geo`
	}
	q := b.client.Query(sql)
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
// weekly history: Score (current, partial week) vs PrevWeekScore, both
// averaged across all 210 DMAs with missing DMA scores counted as 0 so Δ uses
// a consistent denominator.
type SemPulseRow struct {
	Term          string `json:"term" bigquery:"term"`
	Rank          int64  `json:"rank" bigquery:"rank"`
	Score         int64  `json:"score" bigquery:"score"`
	PrevWeekScore int64  `json:"prev_week_score" bigquery:"prev_week_score"`
	SnapshotTime  string `json:"-" bigquery:"snapshot_time"`
}

// GetSemPulse returns the national top 25 from the newest hourly snapshot
// (~4 snapshots/day vs the daily tables' 1–2 day lag). Pinning refresh_time
// to MAX(refresh_time) within the 24 h partition window avoids sorting all 4
// intraday snapshots.
func (b *BQClient) GetSemPulse(ctx context.Context) ([]SemPulseRow, error) {
	q := b.client.Query(`
		WITH latest_snap AS (
			SELECT MAX(refresh_time) AS max_rt
			FROM ` + semHourlyTable + `
			WHERE refresh_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 1 DAY)
		),
		weekly AS (
			SELECT term, MIN(rank) AS rank, week,
				CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				MAX(refresh_time) AS refresh_time,
				DENSE_RANK() OVER (PARTITION BY term ORDER BY week DESC) AS wk_no
			FROM ` + semHourlyTable + `
			WHERE refresh_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 1 DAY)
			  AND refresh_time = (SELECT max_rt FROM latest_snap)
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

// GetSemTermHistoryUS returns the term's complete weekly history from one US
// partition.
func (b *BQClient) GetSemTermHistoryUS(ctx context.Context, refreshDate civil.Date, term string) ([]SemHistoryPoint, error) {
	return b.GetSemTermHistoryUSSourced(ctx, refreshDate, term, "")
}

// GetSemTermHistoryUSSourced scans only the specified table when source is
// "rising" or "top", and excludes the trailing incomplete week so WoW momentum
// bars are never distorted by a 1-day partial week.
func (b *BQClient) GetSemTermHistoryUSSourced(ctx context.Context, refreshDate civil.Date, term, source string) ([]SemHistoryPoint, error) {
	var sql string
	switch source {
	case "rising":
		sql = `
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + semUSRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
		  AND LOWER(term) = LOWER(@term)
		GROUP BY week
		ORDER BY week ASC`
	case "top":
		sql = `
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + semUSTopTable + `
		WHERE refresh_date = @refresh_date
		  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
		  AND LOWER(term) = LOWER(@term)
		GROUP BY week
		ORDER BY week ASC`
	default:
		sql = `
		WITH pts AS (
			SELECT week, score FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) = LOWER(@term)
			UNION ALL
			SELECT week, score FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) = LOWER(@term)
		)
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM pts
		GROUP BY week
		ORDER BY week ASC`
	}
	q := b.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "term", Value: term},
	}
	return collectRows[SemHistoryPoint](q, ctx)
}

// GetSemTermHistoryGlobal is the international-table variant, scoped to one country.
func (b *BQClient) GetSemTermHistoryGlobal(ctx context.Context, refreshDate civil.Date, countryCode, term string) ([]SemHistoryPoint, error) {
	return b.GetSemTermHistoryGlobalSourced(ctx, refreshDate, countryCode, term, "")
}

// GetSemTermHistoryGlobalSourced scans only the specified table when source is
// "rising" or "top", and excludes any trailing incomplete week.
func (b *BQClient) GetSemTermHistoryGlobalSourced(ctx context.Context, refreshDate civil.Date, countryCode, term, source string) ([]SemHistoryPoint, error) {
	var sql string
	switch source {
	case "rising":
		sql = `
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + trendsRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
		  AND LOWER(term) = LOWER(@term)
		GROUP BY week
		ORDER BY week ASC`
	case "top":
		sql = `
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
		  AND LOWER(term) = LOWER(@term)
		GROUP BY week
		ORDER BY week ASC`
	default:
		sql = `
		WITH pts AS (
			SELECT week, score FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) = LOWER(@term)
			UNION ALL
			SELECT week, score FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) = LOWER(@term)
		)
		SELECT FORMAT_DATE('%Y-%m-%d', week) AS week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM pts
		GROUP BY week
		ORDER BY week ASC`
	}
	q := b.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
		{Name: "term", Value: term},
	}
	return collectRows[SemHistoryPoint](q, ctx)
}

// --- Widget 3: brand safety (GDELT news tone + conflict share) ---

// semActorCountry maps the Trends ISO-3166 alpha-2 country codes to the
// CAMEO 3-letter actor codes GDELT events carry (e.g. ROM for Romania, ZAF
// for South Africa, GBR for United Kingdom).
var semActorCountry = map[string]string{
	"AR": "ARG", "AT": "AUT", "AU": "AUS", "BE": "BEL", "BR": "BRA",
	"CA": "CAN", "CH": "CHE", "CL": "CHL", "CO": "COL", "CZ": "CZE",
	"DE": "DEU", "DK": "DNK", "EG": "EGY", "ES": "ESP", "FI": "FIN",
	"FR": "FRA", "GB": "GBR", "HU": "HUN", "ID": "IDN", "IL": "ISR",
	"IN": "IND", "IT": "ITA", "JP": "JPN", "KR": "KOR", "MX": "MEX",
	"MY": "MYS", "NG": "NGA", "NL": "NLD", "NO": "NOR", "NZ": "NZL",
	"PH": "PHL", "PL": "POL", "PT": "PRT", "RO": "ROM", "SA": "SAU",
	"SE": "SWE", "TH": "THA", "TR": "TUR", "TW": "TWN", "UA": "UKR",
	"US": "USA", "VN": "VNM", "ZA": "ZAF",
}

// semFipsCountry maps ISO-3166 alpha-2 codes to FIPS 10-4 ActionGeo_CountryCode
// so countries with sparse Actor1/2CountryCode tags (e.g. Romania) still match
// events geolocated in that market.
var semFipsCountry = map[string]string{
	"AR": "AR", "AT": "AU", "AU": "AS", "BE": "BE", "BR": "BR",
	"CA": "CA", "CH": "SZ", "CL": "CI", "CO": "CO", "CZ": "EZ",
	"DE": "GM", "DK": "DA", "EG": "EG", "ES": "SP", "FI": "FI",
	"FR": "FR", "GB": "UK", "HU": "HU", "ID": "ID", "IL": "IS",
	"IN": "IN", "IT": "IT", "JP": "JA", "KR": "KS", "MX": "MX",
	"MY": "MY", "NG": "NI", "NL": "NL", "NO": "NO", "NZ": "NZ",
	"PH": "RP", "PL": "PL", "PT": "PO", "RO": "RO", "SA": "SA",
	"SE": "SW", "TH": "TH", "TR": "TU", "TW": "TW", "UA": "UP",
	"US": "US", "VN": "VM", "ZA": "SF",
}

// SemSafetyRow is one day of news context for a market. ConflictShare is the
// fraction of events in CAMEO QuadClass 3/4 (verbal/material conflict).
type SemSafetyRow struct {
	IngestDate    string  `json:"ingest_date" bigquery:"ingest_date"`
	EventCount    int64   `json:"event_count" bigquery:"event_count"`
	AvgTone       float64 `json:"avg_tone" bigquery:"avg_tone"`
	ConflictShare float64 `json:"conflict_share" bigquery:"conflict_share"`
}

// GetSemSafetyDaily queries daily GDELT event count, average tone, and
// QuadClass 3/4 conflict share for a market, matching either CAMEO actor
// country code or FIPS ActionGeo_CountryCode.
func (b *BQClient) GetSemSafetyDaily(ctx context.Context, start, end civil.Date, country string) ([]SemSafetyRow, error) {
	fips := ""
	for iso, cameo := range semActorCountry {
		if cameo == country {
			fips = semFipsCountry[iso]
			break
		}
	}
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone,
			ROUND(SAFE_DIVIDE(COUNTIF(QuadClass IN (3, 4)), COUNT(1)), 4) AS conflict_share
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND (` + gdeltInvolvesCountry + ` OR (@fips_country != '' AND ActionGeo_CountryCode = @fips_country))
		GROUP BY 1
		ORDER BY 1`)
	params := gdeltCountryParams(start, end, country)
	params = append(params, bigquery.QueryParameter{Name: "fips_country", Value: fips})
	q.Parameters = params
	return collectRows[SemSafetyRow](q, ctx)
}
