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
	trendsTopTable    = "`bigquery-public-data.google_trends.international_top_terms`"
	trendsRisingTable = "`bigquery-public-data.google_trends.international_top_rising_terms`"
)

// --- Meta: available partitions and countries ---

type TrendsCountry struct {
	Name string `json:"name" bigquery:"name"`
	Code string `json:"code" bigquery:"code"`
}

// GetTrendsRefreshDates returns recent partition dates, newest first. The
// lookback bound keeps the scan pruned to a handful of partitions.
func (b *BQClient) GetTrendsRefreshDates(ctx context.Context) ([]string, error) {
	q := b.client.Query(`
		SELECT FORMAT_DATE('%Y-%m-%d', refresh_date) AS d
		FROM ` + trendsTopTable + `
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
	// Rows are region-grained; average up to country level.
	q := b.client.Query(`
		SELECT term, rank, CAST(COALESCE(AVG(score), 0) AS INT64) AS score
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = (SELECT MAX(week) FROM ` + trendsTopTable + ` WHERE refresh_date = @refresh_date)
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
			CAST(COALESCE(AVG(percent_gain), 0) AS INT64) AS percent_gain,
			CAST(COALESCE(AVG(score), 0) AS INT64) AS score
		FROM ` + trendsRisingTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND week = (SELECT MAX(week) FROM ` + trendsRisingTable + ` WHERE refresh_date = @refresh_date)
		GROUP BY term, rank
		ORDER BY percent_gain DESC
		LIMIT 25`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
	}
	return collectRows[TrendsRisingTerm](q, ctx)
}

// --- Widget 3.2: One term's latest score across countries ---

type TrendsGeoPoint struct {
	CountryCode string `json:"country_code" bigquery:"country_code"`
	CountryName string `json:"country_name" bigquery:"country_name"`
	Score       int64  `json:"score" bigquery:"score"`
	Rank        int64  `json:"rank" bigquery:"rank"`
}

func (b *BQClient) GetTrendsGeo(ctx context.Context, refreshDate civil.Date, term string) ([]TrendsGeoPoint, error) {
	q := b.client.Query(`
		SELECT
			country_code,
			country_name,
			CAST(COALESCE(AVG(score), 0) AS INT64) AS score,
			MIN(rank) AS rank
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND LOWER(term) = LOWER(@term)
		  AND week = (SELECT MAX(week) FROM ` + trendsTopTable + ` WHERE refresh_date = @refresh_date)
		GROUP BY country_code, country_name
		ORDER BY score DESC`)
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
	lowered := make([]string, 0, len(terms))
	for _, t := range terms {
		lowered = append(lowered, strings.ToLower(t))
	}

	q := b.client.Query(`
		SELECT
			term,
			FORMAT_DATE('%Y-%m-%d', week) AS week,
			CAST(COALESCE(AVG(score), 0) AS INT64) AS score
		FROM ` + trendsTopTable + `
		WHERE refresh_date = @refresh_date
		  AND country_code = @country_code
		  AND LOWER(term) IN UNNEST(@terms)
		GROUP BY term, week
		ORDER BY week ASC`)
	q.Parameters = []bigquery.QueryParameter{
		{Name: "refresh_date", Value: refreshDate},
		{Name: "country_code", Value: countryCode},
		{Name: "terms", Value: lowered},
	}
	return collectRows[TrendsHistoryPoint](q, ctx)
}
