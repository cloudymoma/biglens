package main

// BigQuery Open Data: GDELT 2.0 (real-time global news events + GKG).
//
// Queries the public partitioned tables `gdelt-bq.gdeltv2.events_partitioned`
// and `gdelt-bq.gdeltv2.gkg_partitioned` directly. Every query filters on the
// ingestion-time pseudo-column _PARTITIONDATE with native DATE parameters so
// partition pruning always applies. _PARTITIONDATE is the date a report was
// ingested (UTC), not the event date — exactly the "current news pulse"
// semantics the dashboard needs; it is surfaced as `ingest_date`.

import (
	"context"
	"fmt"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	gdeltEventsTable = "`gdelt-bq.gdeltv2.events_partitioned`"
	gdeltGkgTable    = "`gdelt-bq.gdeltv2.gkg_partitioned`"
)

func gdeltDateParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
	}
}

// --- /events 3.1a: situation summary (date × QuadClass × EventRootCode) ---

// GdeltSummaryRow is one aggregation group. The handler rolls these up into
// overall / daily / quad-class / event-type views, weighting every average by
// event_count (Σ(avg_i×n_i)/Σ(n_i) is exact for combining group AVGs).
type GdeltSummaryRow struct {
	IngestDate    string  `bigquery:"ingest_date"`
	QuadClass     int64   `bigquery:"quad_class"`
	EventRootCode string  `bigquery:"event_root_code"`
	EventCount    int64   `bigquery:"event_count"`
	AvgGoldstein  float64 `bigquery:"avg_goldstein"`
	AvgTone       float64 `bigquery:"avg_tone"`
}

func (b *BQClient) GetGdeltSummary(ctx context.Context, start, end civil.Date) ([]GdeltSummaryRow, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			QuadClass AS quad_class,
			COALESCE(EventRootCode, 'UNK') AS event_root_code,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(GoldsteinScale), 0), 2) AS avg_goldstein,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
		GROUP BY 1, 2, 3`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltSummaryRow](q, ctx)
}

// --- /events 3.1b: geo hotspots, top 500 0.1° cells ---

type GdeltHotspot struct {
	Latitude    float64 `json:"latitude" bigquery:"latitude"`
	Longitude   float64 `json:"longitude" bigquery:"longitude"`
	FipsCountry string  `json:"fips_country" bigquery:"fips_country"`
	EventCount  int64   `json:"event_count" bigquery:"event_count"`
	AvgTone     float64 `json:"avg_tone" bigquery:"avg_tone"`
}

func (b *BQClient) GetGdeltHotspots(ctx context.Context, start, end civil.Date) ([]GdeltHotspot, error) {
	q := b.client.Query(`
		SELECT
			ROUND(ActionGeo_Lat, 1) AS latitude,
			ROUND(ActionGeo_Long, 1) AS longitude,
			COALESCE(ActionGeo_CountryCode, 'UNKNOWN') AS fips_country,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND ActionGeo_Lat IS NOT NULL
			AND ActionGeo_Long IS NOT NULL
		GROUP BY 1, 2, 3
		ORDER BY event_count DESC
		LIMIT 500`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltHotspot](q, ctx)
}

// --- /events 3.1c: breaking conflict news, top 50 deduped by URL ---

type GdeltNews struct {
	IngestDate    string  `json:"ingest_date" bigquery:"ingest_date"`
	FipsCountry   string  `json:"fips_country" bigquery:"fips_country"`
	EventRootCode string  `json:"event_root_code" bigquery:"event_root_code"`
	AvgTone       float64 `json:"avg_tone" bigquery:"avg_tone"`
	SourceURL     string  `json:"source_url" bigquery:"source_url"`
	MentionCount  int64   `json:"mention_count" bigquery:"mention_count"`
}

// GetGdeltConflictNews returns the most-mentioned conflict reports
// (QuadClass 3/4). GDELT emits several event rows per article, so QUALIFY
// keeps only the highest-mention row per SOURCEURL — otherwise one big story
// floods the list.
func (b *BQClient) GetGdeltConflictNews(ctx context.Context, start, end civil.Date) ([]GdeltNews, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COALESCE(ActionGeo_CountryCode, 'UNKNOWN') AS fips_country,
			COALESCE(EventRootCode, 'UNK') AS event_root_code,
			ROUND(COALESCE(AvgTone, 0), 2) AS avg_tone,
			SOURCEURL AS source_url,
			NumMentions AS mention_count
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND QuadClass IN (3, 4)
			AND SOURCEURL IS NOT NULL
		QUALIFY ROW_NUMBER() OVER (PARTITION BY SOURCEURL ORDER BY NumMentions DESC) = 1
		ORDER BY mention_count DESC
		LIMIT 50`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltNews](q, ctx)
}

// --- /gkg 3.2a + 3.2b: top themes / persons by article count ---

type GdeltNamedCount struct {
	Name         string `json:"name" bigquery:"name"`
	ArticleCount int64  `json:"article_count" bigquery:"article_count"`
}

// gdeltEntityQuery counts articles per entity from a GKG V2 list column
// (`NAME,charOffset;NAME,charOffset;...`). The in-row SPLIT + DISTINCT
// dedupes repeat mentions within one article so counts mean "articles", not
// occurrences biased toward long articles; SPLIT is far cheaper than a lazy
// regex over multi-KB strings × millions of rows.
func gdeltEntityQuery(column string, limit int) string {
	return fmt.Sprintf(`
		WITH docs AS (
			SELECT ARRAY(
				SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
				FROM UNNEST(SPLIT(%s, ';')) AS entry
				WHERE entry != ''
			) AS entities
			FROM %s
			WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
				AND %s IS NOT NULL
		)
		SELECT entity AS name, COUNT(*) AS article_count
		FROM docs, UNNEST(entities) AS entity
		WHERE LENGTH(entity) > 2
		GROUP BY name
		ORDER BY article_count DESC
		LIMIT %d`, column, gdeltGkgTable, column, limit)
}

func (b *BQClient) GetGdeltThemes(ctx context.Context, start, end civil.Date) ([]GdeltNamedCount, error) {
	q := b.client.Query(gdeltEntityQuery("V2Themes", 50))
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltNamedCount](q, ctx)
}

func (b *BQClient) GetGdeltPersons(ctx context.Context, start, end civil.Date) ([]GdeltNamedCount, error) {
	q := b.client.Query(gdeltEntityQuery("V2Persons", 20))
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltNamedCount](q, ctx)
}

// --- /gkg 3.2c: top media sources with average tone ---

type GdeltMediaSource struct {
	MediaSource  string  `json:"media_source" bigquery:"media_source"`
	ArticleCount int64   `json:"article_count" bigquery:"article_count"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
}

func (b *BQClient) GetGdeltMediaSources(ctx context.Context, start, end civil.Date) ([]GdeltMediaSource, error) {
	// V2Tone is a composite CSV string; field 0 is the tone score.
	q := b.client.Query(`
		SELECT
			SourceCommonName AS media_source,
			COUNT(1) AS article_count,
			ROUND(COALESCE(AVG(SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64)), 0), 2) AS avg_tone
		FROM ` + gdeltGkgTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND SourceCommonName IS NOT NULL
		GROUP BY media_source
		ORDER BY article_count DESC
		LIMIT 10`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltMediaSource](q, ctx)
}
