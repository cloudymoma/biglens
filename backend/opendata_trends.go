package main

// BigQuery Open Data: Google Trends.
//
// Queries the public dataset `bigquery-public-data.google_trends`. Every query
// filters on the partition key `refresh_date` to avoid full-table scans; a
// single partition holds the 5-year weekly history for that day's top terms,
// so "current" widgets additionally pin week = MAX(week).
//
// Future open-data sources get their own opendata_<name>.go following the
// same shape: typed rows + BQClient methods, wired up in opendata_handlers.go.

import (
	"context"
	"strings"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	trendsTopTable        = "`bigquery-public-data.google_trends.international_top_terms`"
	trendsRisingTable     = "`bigquery-public-data.google_trends.international_top_rising_terms`"
	trendsPartitionsTable = "`bigquery-public-data.google_trends.INFORMATION_SCHEMA.PARTITIONS`"
)

// --- Meta: available partitions and countries ---

type TrendsCountry struct {
	Name string `json:"name" bigquery:"name"`
	Code string `json:"code" bigquery:"code"`
}

// getTrendsTablePartitionDates lists recent non-empty partition dates for a
// google_trends table via INFORMATION_SCHEMA.PARTITIONS (~10 MB metadata scan
// instead of scanning ~2 GB of refresh_date column values across 45 partitions).
func (b *BQClient) getTrendsTablePartitionDates(ctx context.Context, tableName string) ([]string, error) {
	q := b.client.Query(`
		SELECT FORMAT_DATE('%Y-%m-%d', SAFE.PARSE_DATE('%Y%m%d', partition_id)) AS d
		FROM ` + trendsPartitionsTable + `
		WHERE table_name = @table_name
		  AND total_rows > 0
		  AND SAFE.PARSE_DATE('%Y%m%d', partition_id) >= DATE_SUB(CURRENT_DATE(), INTERVAL 45 DAY)
		ORDER BY d DESC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "table_name", Value: tableName},
	}

	type dateRow struct {
		D string `bigquery:"d"`
	}
	rows, err := collectRows[dateRow](q, ctx)
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.D != "" {
			dates = append(dates, r.D)
		}
	}
	return dates, nil
}

// GetTrendsRefreshDates returns recent international partition dates, newest first.
func (b *BQClient) GetTrendsRefreshDates(ctx context.Context) ([]string, error) {
	return b.getTrendsTablePartitionDates(ctx, "international_top_terms")
}

func (b *BQClient) GetTrendsCountries(ctx context.Context, refreshDate civil.Date) ([]TrendsCountry, error) {
	q := b.client.Query(`
		SELECT country_name AS name, country_code AS code
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		GROUP BY name, code
		ORDER BY name ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
	}
	return collectRows[TrendsCountry](q, ctx)
}

// --- Widget 1.1 / 1.2: Top 25 terms for a country ---

type TrendsTopTerm struct {
	Term  string `json:"term" bigquery:"term"`
	Rank  int64  `json:"rank" bigquery:"rank"`
	Score int64  `json:"score" bigquery:"score"`
}

func (b *BQClient) GetTrendsTopTerms(ctx context.Context, refreshDate civil.Date, countryCode string) ([]TrendsTopTerm, error) {
	// Rows are region-grained; average up to country level, counting NULL region scores as 0.
	q := b.client.Query(`
		SELECT term, rank, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = ` + semLatestCompleteWeek(trendsTopTable) + `
		GROUP BY term, rank
		ORDER BY rank ASC
		LIMIT 25`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
	}
	return collectRows[TrendsTopTerm](q, ctx)
}

// --- Widget 2.1 / 2.2: Top rising terms for a country ---

type TrendsRisingTerm struct {
	Term        string `json:"term" bigquery:"term"`
	Rank        int64  `json:"rank" bigquery:"rank"`
	PercentGain int64  `json:"percent_gain" bigquery:"percent_gain"`
	Score       int64  `json:"score" bigquery:"score"`
}

func (b *BQClient) GetTrendsRisingTerms(ctx context.Context, refreshDate civil.Date, countryCode string) ([]TrendsRisingTerm, error) {
	q := b.client.Query(`
		SELECT
			term,
			rank,
			CAST(COALESCE(ANY_VALUE(percent_gain), 0) AS INT64) AS percent_gain,
			CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + trendsRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = ` + semLatestCompleteWeek(trendsRisingTable) + `
		GROUP BY term, rank
		ORDER BY percent_gain DESC
		LIMIT 25`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
	}
	return collectRows[TrendsRisingTerm](q, ctx)
}

// --- Widget 3.2: One term's latest complete-week score across countries ---

type TrendsGeoPoint struct {
	CountryCode string `json:"country_code" bigquery:"country_code"`
	CountryName string `json:"country_name" bigquery:"country_name"`
	Score       int64  `json:"score" bigquery:"score"`
	Rank        int64  `json:"rank" bigquery:"rank"`
}

func (b *BQClient) GetTrendsGeo(ctx context.Context, refreshDate civil.Date, term string) ([]TrendsGeoPoint, error) {
	q := b.client.Query(`
		WITH charting AS (
			SELECT
				country_code,
				country_name,
				CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				MIN(rank) AS rank
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND LOWER(term) = LOWER(@term)
			  AND week = ` + semLatestCompleteWeek(trendsTopTable) + `
			GROUP BY country_code, country_name
		),
		rising AS (
			SELECT
				country_code,
				country_name,
				CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				MIN(rank) AS rank
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND LOWER(term) = LOWER(@term)
			  AND week = ` + semLatestCompleteWeek(trendsRisingTable) + `
			GROUP BY country_code, country_name
		)
		SELECT
			COALESCE(c.country_code, r.country_code) AS country_code,
			COALESCE(c.country_name, r.country_name) AS country_name,
			GREATEST(COALESCE(c.score, 0), COALESCE(r.score, 0)) AS score,
			COALESCE(c.rank, r.rank, 0) AS rank
		FROM charting c
		FULL OUTER JOIN rising r ON c.country_code = r.country_code
		ORDER BY score DESC, rank ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "term", Value: term},
	}
	return collectRows[TrendsGeoPoint](q, ctx)
}

// --- Widget 4.1: 5-year weekly history for up to maxTrendsCompareTerms ---

type TrendsHistoryPoint struct {
	Term  string `json:"term" bigquery:"term"`
	Week  string `json:"week" bigquery:"week"`
	Score int64  `json:"score" bigquery:"score"`
}

func (b *BQClient) GetTrendsHistory(ctx context.Context, refreshDate civil.Date, countryCode string, terms []string) ([]TrendsHistoryPoint, error) {
	q := b.client.Query(`
		WITH combined AS (
			SELECT term, week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
			FROM ` + trendsTopTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) IN UNNEST(@terms)
			GROUP BY term, week
			UNION ALL
			SELECT term, week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
			FROM ` + trendsRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND country_code = @country_code
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) IN UNNEST(@terms)
			GROUP BY term, week
		)
		SELECT
			term,
			FORMAT_DATE('%Y-%m-%d', week) AS week,
			MAX(score) AS score
		FROM combined
		GROUP BY term, week
		ORDER BY week ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
		{Name: "terms", Value: lowerAll(terms)},
	}
	return collectRows[TrendsHistoryPoint](q, ctx)
}

// --- US market variants ---
//
// The US is absent from the international tables; it lives in the DMA-grained
// `top_terms` / `top_rising_terms` tables (constants in opendata_sem.go).
// Both carry one national rank and percent_gain per term replicated across all
// 210 DMAs; empty dma aggregates scores nationally (with NULL DMA scores
// counted as 0), while a DMA name narrows scores to that metro.

func (b *BQClient) GetTrendsTopTermsUS(ctx context.Context, refreshDate civil.Date, dma string) ([]TrendsTopTerm, error) {
	q := b.client.Query(`
		SELECT term, rank, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + semUSTopTable + `
		WHERE refresh_date = @refresh_date
		  AND week = ` + semLatestCompleteWeek(semUSTopTable) + `
		  AND (@dma = '' OR dma_name = @dma)
		GROUP BY term, rank
		ORDER BY rank ASC
		LIMIT 25`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "dma", Value: dma},
	}
	return collectRows[TrendsTopTerm](q, ctx)
}

func (b *BQClient) GetTrendsRisingTermsUS(ctx context.Context, refreshDate civil.Date, dma string) ([]TrendsRisingTerm, error) {
	q := b.client.Query(`
		SELECT
			term,
			rank,
			CAST(COALESCE(ANY_VALUE(percent_gain), 0) AS INT64) AS percent_gain,
			CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
		FROM ` + semUSRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND week = ` + semLatestCompleteWeek(semUSRisingTable) + `
		  AND (@dma = '' OR dma_name = @dma)
		GROUP BY term, rank
		ORDER BY percent_gain DESC
		LIMIT 25`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "dma", Value: dma},
	}
	return collectRows[TrendsRisingTerm](q, ctx)
}

// GetTrendsGeoUS is the US view of GetTrendsGeo: the term's score in each DMA
// where it charts or rises (top-25 ∪ rising) in the latest complete week, DMA
// names in country_name, with missing DMA scores as 0.
func (b *BQClient) GetTrendsGeoUS(ctx context.Context, refreshDate civil.Date, term string) ([]TrendsGeoPoint, error) {
	q := b.client.Query(`
		WITH charting AS (
			SELECT dma_name AS geo, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSTopTable) + `
			  AND LOWER(term) = LOWER(@term)
			GROUP BY geo
		),
		rising AS (
			SELECT dma_name AS geo, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score,
				MIN(rank) AS rising_rank, CAST(COALESCE(MAX(percent_gain), 0) AS INT64) AS percent_gain
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND week = ` + semLatestCompleteWeek(semUSRisingTable) + `
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

	type dmaRow struct {
		Geo   string `bigquery:"geo"`
		Score int64  `bigquery:"score"`
	}
	rows, err := collectRows[dmaRow](q, ctx)
	if err != nil {
		return nil, err
	}
	points := make([]TrendsGeoPoint, 0, len(rows))
	for _, r := range rows {
		points = append(points, TrendsGeoPoint{CountryName: r.Geo, Score: r.Score})
	}
	return points, nil
}

func (b *BQClient) GetTrendsHistoryUS(ctx context.Context, refreshDate civil.Date, dma string, terms []string) ([]TrendsHistoryPoint, error) {
	q := b.client.Query(`
		WITH combined AS (
			SELECT term, week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
			FROM ` + semUSTopTable + `
			WHERE refresh_date = @refresh_date
			  AND (@dma = '' OR dma_name = @dma)
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) IN UNNEST(@terms)
			GROUP BY term, week
			UNION ALL
			SELECT term, week, CAST(ROUND(COALESCE(AVG(IFNULL(score, 0)), 0)) AS INT64) AS score
			FROM ` + semUSRisingTable + `
			WHERE refresh_date = @refresh_date
			  AND (@dma = '' OR dma_name = @dma)
			  AND DATE_ADD(week, INTERVAL 7 DAY) <= @refresh_date
			  AND LOWER(term) IN UNNEST(@terms)
			GROUP BY term, week
		)
		SELECT
			term,
			FORMAT_DATE('%Y-%m-%d', week) AS week,
			MAX(score) AS score
		FROM combined
		GROUP BY term, week
		ORDER BY week ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "dma", Value: dma},
		{Name: "terms", Value: lowerAll(terms)},
	}
	return collectRows[TrendsHistoryPoint](q, ctx)
}

func lowerAll(terms []string) []string {
	lowered := make([]string, 0, len(terms))
	for _, t := range terms {
		lowered = append(lowered, strings.ToLower(t))
	}
	return lowered
}
