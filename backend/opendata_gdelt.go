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
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	gdeltEventsTable   = "`gdelt-bq.gdeltv2.events_partitioned`"
	gdeltGkgTable      = "`gdelt-bq.gdeltv2.gkg_partitioned`"
	gdeltMentionsTable = "`gdelt-bq.gdeltv2.eventmentions_partitioned`"
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

// --- /dyads: bilateral tension board + actor-country picker list ---
//
// The Country & Relations tab is keyed on Actor*CountryCode (CAMEO 3-letter
// codes, ISO-like), NOT ActionGeo_CountryCode (FIPS): actor codes give
// uniform "events involving X" semantics and avoid mixing the two systems.

type GdeltDyadRow struct {
	CountryA     string  `json:"country_a" bigquery:"country_a"`
	CountryB     string  `json:"country_b" bigquery:"country_b"`
	EventCount   int64   `json:"event_count" bigquery:"event_count"`
	AvgGoldstein float64 `json:"avg_goldstein" bigquery:"avg_goldstein"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
}

// GetGdeltDyads returns the busiest country pairs. LEAST/GREATEST merges the
// two directions (RUS→UKR and UKR→RUS) into one undirected pair.
func (b *BQClient) GetGdeltDyads(ctx context.Context, start, end civil.Date) ([]GdeltDyadRow, error) {
	q := b.client.Query(`
		SELECT
			LEAST(Actor1CountryCode, Actor2CountryCode) AS country_a,
			GREATEST(Actor1CountryCode, Actor2CountryCode) AS country_b,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(GoldsteinScale), 0), 2) AS avg_goldstein,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND Actor1CountryCode IS NOT NULL
			AND Actor2CountryCode IS NOT NULL
			AND Actor1CountryCode != Actor2CountryCode
		GROUP BY 1, 2
		ORDER BY event_count DESC
		LIMIT 30`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltDyadRow](q, ctx)
}

type GdeltCountryCount struct {
	Country    string `json:"country" bigquery:"country"`
	EventCount int64  `json:"event_count" bigquery:"event_count"`
}

func (b *BQClient) GetGdeltActorCountries(ctx context.Context, start, end civil.Date) ([]GdeltCountryCount, error) {
	q := b.client.Query(`
		SELECT c AS country, COUNT(1) AS event_count
		FROM ` + gdeltEventsTable + `, UNNEST([Actor1CountryCode, Actor2CountryCode]) AS c
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND c IS NOT NULL
		GROUP BY 1
		ORDER BY event_count DESC
		LIMIT 60`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltCountryCount](q, ctx)
}

// --- /country: drill-down on one actor country ---

func gdeltCountryParams(start, end civil.Date, country string) []bigquery.QueryParameter {
	return append(gdeltDateParams(start, end), bigquery.QueryParameter{Name: "country", Value: country})
}

// gdeltInvolvesCountry filters to events where either actor is @country.
const gdeltInvolvesCountry = `(Actor1CountryCode = @country OR Actor2CountryCode = @country)`

type GdeltCountryDaily struct {
	IngestDate   string  `json:"ingest_date" bigquery:"ingest_date"`
	EventCount   int64   `json:"event_count" bigquery:"event_count"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
	AvgGoldstein float64 `json:"avg_goldstein" bigquery:"avg_goldstein"`
}

func (b *BQClient) GetGdeltCountryDaily(ctx context.Context, start, end civil.Date, country string) ([]GdeltCountryDaily, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone,
			ROUND(COALESCE(AVG(GoldsteinScale), 0), 2) AS avg_goldstein
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND ` + gdeltInvolvesCountry + `
		GROUP BY 1
		ORDER BY 1`)
	q.Parameters = gdeltCountryParams(start, end, country)
	return collectRows[GdeltCountryDaily](q, ctx)
}

// GdeltCountryEventType uses the full 4-digit CAMEO EventCode — the whole
// point of the drill-down is finer grain than the 20 root codes.
type GdeltCountryEventType struct {
	EventCode    string  `json:"event_code" bigquery:"event_code"`
	EventCount   int64   `json:"event_count" bigquery:"event_count"`
	AvgGoldstein float64 `json:"avg_goldstein" bigquery:"avg_goldstein"`
}

func (b *BQClient) GetGdeltCountryEventTypes(ctx context.Context, start, end civil.Date, country string) ([]GdeltCountryEventType, error) {
	q := b.client.Query(`
		SELECT
			COALESCE(EventCode, 'UNK') AS event_code,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(GoldsteinScale), 0), 2) AS avg_goldstein
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND ` + gdeltInvolvesCountry + `
		GROUP BY 1
		ORDER BY event_count DESC
		LIMIT 25`)
	q.Parameters = gdeltCountryParams(start, end, country)
	return collectRows[GdeltCountryEventType](q, ctx)
}

type GdeltPartnerRow struct {
	Partner      string  `json:"partner" bigquery:"partner"`
	EventCount   int64   `json:"event_count" bigquery:"event_count"`
	AvgGoldstein float64 `json:"avg_goldstein" bigquery:"avg_goldstein"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
}

func (b *BQClient) GetGdeltCountryPartners(ctx context.Context, start, end civil.Date, country string) ([]GdeltPartnerRow, error) {
	q := b.client.Query(`
		SELECT
			IF(Actor1CountryCode = @country, Actor2CountryCode, Actor1CountryCode) AS partner,
			COUNT(1) AS event_count,
			ROUND(COALESCE(AVG(GoldsteinScale), 0), 2) AS avg_goldstein,
			ROUND(COALESCE(AVG(AvgTone), 0), 2) AS avg_tone
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND Actor1CountryCode IS NOT NULL
			AND Actor2CountryCode IS NOT NULL
			AND Actor1CountryCode != Actor2CountryCode
			AND ` + gdeltInvolvesCountry + `
		GROUP BY 1
		ORDER BY event_count DESC
		LIMIT 15`)
	q.Parameters = gdeltCountryParams(start, end, country)
	return collectRows[GdeltPartnerRow](q, ctx)
}

type GdeltCountryEvent struct {
	IngestDate   string  `json:"ingest_date" bigquery:"ingest_date"`
	Actor1       string  `json:"actor1" bigquery:"actor1"`
	Actor2       string  `json:"actor2" bigquery:"actor2"`
	EventCode    string  `json:"event_code" bigquery:"event_code"`
	Goldstein    float64 `json:"goldstein" bigquery:"goldstein"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
	MentionCount int64   `json:"mention_count" bigquery:"mention_count"`
	SourceCount  int64   `json:"source_count" bigquery:"source_count"`
	SourceURL    string  `json:"source_url" bigquery:"source_url"`
}

// GetGdeltCountryEvents returns the most-mentioned individual events
// involving the country, deduped by SOURCEURL (GDELT emits several event
// rows per article).
func (b *BQClient) GetGdeltCountryEvents(ctx context.Context, start, end civil.Date, country string) ([]GdeltCountryEvent, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COALESCE(Actor1Name, '') AS actor1,
			COALESCE(Actor2Name, '') AS actor2,
			COALESCE(EventCode, 'UNK') AS event_code,
			ROUND(COALESCE(GoldsteinScale, 0), 1) AS goldstein,
			ROUND(COALESCE(AvgTone, 0), 2) AS avg_tone,
			NumMentions AS mention_count,
			NumSources AS source_count,
			SOURCEURL AS source_url
		FROM ` + gdeltEventsTable + `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND ` + gdeltInvolvesCountry + `
			AND SOURCEURL IS NOT NULL
		QUALIFY ROW_NUMBER() OVER (PARTITION BY SOURCEURL ORDER BY NumMentions DESC) = 1
		ORDER BY mention_count DESC
		LIMIT 30`)
	q.Parameters = gdeltCountryParams(start, end, country)
	return collectRows[GdeltCountryEvent](q, ctx)
}

// --- /stories: story spread from the mentions stream ---

type GdeltStoryRow struct {
	Mentions      int64   `json:"mentions" bigquery:"mentions"`
	Outlets       int64   `json:"outlets" bigquery:"outlets"`
	AvgConfidence float64 `json:"avg_confidence" bigquery:"avg_confidence"`
	AvgTone       float64 `json:"avg_tone" bigquery:"avg_tone"`
	FirstSeen     string  `json:"first_seen" bigquery:"first_seen"`
	SpanMinutes   int64   `json:"span_minutes" bigquery:"span_minutes"`
	Actor1        string  `json:"actor1" bigquery:"actor1"`
	Actor2        string  `json:"actor2" bigquery:"actor2"`
	EventCode     string  `json:"event_code" bigquery:"event_code"`
	Location      string  `json:"location" bigquery:"location"`
	SourceURL     string  `json:"source_url" bigquery:"source_url"`
}

// GetGdeltStories ranks events by how many DISTINCT outlets picked them up —
// spread across independent sources beats raw mention count, which one wire
// service can inflate. Confidence >= 40 drops GDELT's least certain
// event-mention matches. MentionTimeDate is a yyyymmddhhmmss integer;
// SAFE.PARSE_TIMESTAMP tolerates malformed stamps.
func (b *BQClient) GetGdeltStories(ctx context.Context, start, end civil.Date) ([]GdeltStoryRow, error) {
	q := b.client.Query(`
		WITH m AS (
			SELECT
				GLOBALEVENTID,
				COUNT(1) AS mentions,
				COUNT(DISTINCT MentionSourceName) AS outlets,
				MIN(MentionTimeDate) AS first_raw,
				MAX(MentionTimeDate) AS last_raw,
				ROUND(AVG(Confidence), 1) AS avg_confidence,
				ROUND(AVG(MentionDocTone), 2) AS avg_tone
			FROM ` + gdeltMentionsTable + `
			WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
				AND Confidence >= 40
			GROUP BY 1
			ORDER BY outlets DESC
			LIMIT 200
		)
		SELECT
			m.mentions,
			m.outlets,
			m.avg_confidence,
			m.avg_tone,
			COALESCE(FORMAT_TIMESTAMP('%Y-%m-%d %H:%M',
				SAFE.PARSE_TIMESTAMP('%Y%m%d%H%M%S', CAST(m.first_raw AS STRING))), '') AS first_seen,
			COALESCE(TIMESTAMP_DIFF(
				SAFE.PARSE_TIMESTAMP('%Y%m%d%H%M%S', CAST(m.last_raw AS STRING)),
				SAFE.PARSE_TIMESTAMP('%Y%m%d%H%M%S', CAST(m.first_raw AS STRING)), MINUTE), 0) AS span_minutes,
			COALESCE(e.Actor1Name, '') AS actor1,
			COALESCE(e.Actor2Name, '') AS actor2,
			COALESCE(e.EventCode, 'UNK') AS event_code,
			COALESCE(e.ActionGeo_FullName, '') AS location,
			COALESCE(e.SOURCEURL, '') AS source_url
		FROM m
		JOIN ` + gdeltEventsTable + ` AS e ON e.GLOBALEVENTID = m.GLOBALEVENTID
		WHERE e._PARTITIONDATE BETWEEN @start_date AND @end_date
		ORDER BY m.outlets DESC
		LIMIT 25`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltStoryRow](q, ctx)
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

// --- /impact: numeric impact counts from GKG V2Counts ---
//
// V2Counts entries are `Type#Count#ObjectType#LocType#FullName#FIPS#...`,
// semicolon-separated. Only the core human-impact types are surfaced; the
// CRISISLEX taxa duplicate them with more noise. All metrics count ARTICLES
// REPORTING a figure, never sum the figures themselves — the same incident
// is reported by hundreds of outlets, so sums would be meaningless.

// gdeltImpactTypes is interpolated into IN(...) lists; values are fixed
// literals, never user input.
const gdeltImpactTypes = `('KILL', 'WOUND', 'ARREST', 'KIDNAP', 'DISPLACED', 'SEIZE')`

type GdeltImpactDaily struct {
	IngestDate   string `json:"ingest_date" bigquery:"ingest_date"`
	CountType    string `json:"count_type" bigquery:"count_type"`
	ArticleCount int64  `json:"article_count" bigquery:"article_count"`
}

// GetGdeltImpactDaily counts articles per day per impact type. The in-row
// DISTINCT dedupes repeat counts within one article (same shape as
// gdeltEntityQuery).
func (b *BQClient) GetGdeltImpactDaily(ctx context.Context, start, end civil.Date) ([]GdeltImpactDaily, error) {
	q := b.client.Query(`
		WITH docs AS (
			SELECT _PARTITIONDATE AS d,
				ARRAY(
					SELECT DISTINCT SPLIT(entry, '#')[SAFE_OFFSET(0)]
					FROM UNNEST(SPLIT(V2Counts, ';')) AS entry
					WHERE entry != ''
				) AS types
			FROM ` + gdeltGkgTable + `
			WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
				AND V2Counts IS NOT NULL
		)
		SELECT FORMAT_DATE('%Y-%m-%d', d) AS ingest_date, t AS count_type, COUNT(1) AS article_count
		FROM docs, UNNEST(types) AS t
		WHERE t IN ` + gdeltImpactTypes + `
		GROUP BY 1, 2
		ORDER BY 1, 2`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltImpactDaily](q, ctx)
}

type GdeltImpactCountry struct {
	FipsCountry  string `json:"fips_country" bigquery:"fips_country"`
	ArticleCount int64  `json:"article_count" bigquery:"article_count"`
}

func (b *BQClient) GetGdeltImpactCountries(ctx context.Context, start, end civil.Date) ([]GdeltImpactCountry, error) {
	q := b.client.Query(`
		WITH docs AS (
			SELECT
				ARRAY(
					SELECT DISTINCT SPLIT(entry, '#')[SAFE_OFFSET(5)]
					FROM UNNEST(SPLIT(V2Counts, ';')) AS entry
					WHERE entry != ''
						AND SPLIT(entry, '#')[SAFE_OFFSET(0)] IN ` + gdeltImpactTypes + `
				) AS countries
			FROM ` + gdeltGkgTable + `
			WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
				AND V2Counts IS NOT NULL
		)
		SELECT c AS fips_country, COUNT(1) AS article_count
		FROM docs, UNNEST(countries) AS c
		WHERE c IS NOT NULL AND c != ''
		GROUP BY 1
		ORDER BY article_count DESC
		LIMIT 20`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltImpactCountry](q, ctx)
}

type GdeltImpactIncident struct {
	CountType    string `json:"count_type" bigquery:"count_type"`
	Num          int64  `json:"num" bigquery:"num"`
	Location     string `json:"location" bigquery:"location"`
	ArticleCount int64  `json:"article_count" bigquery:"article_count"`
	SampleURL    string `json:"sample_url" bigquery:"sample_url"`
}

// GetGdeltImpactIncidents surfaces the most-reported (type, figure,
// location) triples. Grouping on the triple naturally dedups one incident
// reported by many outlets into a single row ranked by coverage. ARREST and
// SEIZE are excluded here: routine small figures dominate them.
func (b *BQClient) GetGdeltImpactIncidents(ctx context.Context, start, end civil.Date) ([]GdeltImpactIncident, error) {
	q := b.client.Query(`
		WITH entries AS (
			SELECT
				SPLIT(entry, '#')[SAFE_OFFSET(0)] AS count_type,
				SAFE_CAST(SPLIT(entry, '#')[SAFE_OFFSET(1)] AS INT64) AS num,
				SPLIT(entry, '#')[SAFE_OFFSET(4)] AS location,
				DocumentIdentifier AS url
			FROM ` + gdeltGkgTable + `, UNNEST(SPLIT(V2Counts, ';')) AS entry
			WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
				AND V2Counts IS NOT NULL
				AND entry != ''
		)
		SELECT
			count_type,
			num,
			COALESCE(NULLIF(location, ''), 'Unknown location') AS location,
			COUNT(DISTINCT url) AS article_count,
			ANY_VALUE(url) AS sample_url
		FROM entries
		WHERE count_type IN ('KILL', 'WOUND', 'KIDNAP', 'DISPLACED')
			AND num >= 1 AND num < 10000000
		GROUP BY 1, 2, 3
		ORDER BY article_count DESC
		LIMIT 30`)
	q.Parameters = gdeltDateParams(start, end)
	return collectRows[GdeltImpactIncident](q, ctx)
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

// --- /industry: theme-driven industry pulse ---
//
// Each vertical is a fixed list of GKG theme names validated against live
// GKG data on 2026-07-28 (plan Task 1). Names are matched at entry starts
// of V2Themes (`NAME,offset;NAME,offset;...`) with prefix semantics, so a
// root like TAX_DISEASE covers its whole family. The list is compiled into
// one RE2 pattern bound as @theme_re — never interpolated, never client
// input. Both Go's regexp and BigQuery use RE2, so the unit tests validate
// exactly the semantics BigQuery will apply.

var industryThemes = map[string][]string{
	// Validated against live GKG data 2026-07-28 (2-day window; keep rule
	// articles >= 500 and <= 25% of total). "retail" is intentionally thin —
	// GKG has no e-commerce themes; the user chose this Retail & Consumer
	// slice over dropping the vertical.
	"finance":    {"ECON_STOCKMARKET", "ECON_INFLATION", "WB_450_DEBT", "ECON_DEBT", "ECON_INTEREST_RATES", "ECON_CURRENCY_EXCHANGE_RATE", "ECON_CENTRALBANK", "ECON_IPO", "ECON_BANKRUPTCY"},
	"retail":     {"TAX_FNCACT_RETAILER", "WB_358_RETAIL_PAYMENTS", "WB_364_CONSUMER_PROTECTION", "WB_1017_CONSUMER_PROTECTION_LAW"},
	"biomedical": {"GENERAL_HEALTH", "MEDICAL", "TAX_DISEASE", "WB_1406_DISEASES", "HEALTH_PANDEMIC", "HEALTH_VACCINATION"},
	"education":  {"EDUCATION", "WB_470_EDUCATION"},
}

func industryKeys() []string {
	keys := make([]string, 0, len(industryThemes))
	for k := range industryThemes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// industryThemeRegex anchors every theme to an entry start so FOO never
// matches XFOO, while keeping prefix semantics (FOO matches FOO_BAR).
func industryThemeRegex(themes []string) string {
	return `(?:^|;)(?:` + strings.Join(themes, "|") + `)`
}

const gdeltIndustryFilter = `
		WHERE _PARTITIONDATE BETWEEN @start_date AND @end_date
			AND V2Themes IS NOT NULL
			AND REGEXP_CONTAINS(V2Themes, @theme_re)`

func gdeltIndustryParams(start, end civil.Date, themeRe string) []bigquery.QueryParameter {
	return append(gdeltDateParams(start, end), bigquery.QueryParameter{Name: "theme_re", Value: themeRe})
}

type GdeltIndustryDaily struct {
	IngestDate   string  `json:"ingest_date" bigquery:"ingest_date"`
	ArticleCount int64   `json:"article_count" bigquery:"article_count"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
}

func (b *BQClient) GetGdeltIndustryDaily(ctx context.Context, start, end civil.Date, themeRe string) ([]GdeltIndustryDaily, error) {
	// V2Tone is a composite CSV string; field 0 is the tone score.
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			COUNT(1) AS article_count,
			ROUND(COALESCE(AVG(SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64)), 0), 2) AS avg_tone
		FROM ` + gdeltGkgTable + gdeltIndustryFilter + `
		GROUP BY 1
		ORDER BY 1`)
	q.Parameters = gdeltIndustryParams(start, end, themeRe)
	return collectRows[GdeltIndustryDaily](q, ctx)
}

type GdeltIndustryOrg struct {
	Name         string  `json:"name" bigquery:"name"`
	ArticleCount int64   `json:"article_count" bigquery:"article_count"`
	AvgTone      float64 `json:"avg_tone" bigquery:"avg_tone"`
}

// GetGdeltIndustryOrgs counts articles per organization within the vertical.
// The in-row SPLIT + DISTINCT dedupes repeat mentions within one article so
// counts mean "articles" (same shape as gdeltEntityQuery); tone is the
// article tone averaged over the articles naming the org.
func (b *BQClient) GetGdeltIndustryOrgs(ctx context.Context, start, end civil.Date, themeRe string) ([]GdeltIndustryOrg, error) {
	q := b.client.Query(`
		WITH docs AS (
			SELECT
				SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64) AS tone,
				ARRAY(
					SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
					FROM UNNEST(SPLIT(V2Organizations, ';')) AS entry
					WHERE entry != ''
				) AS orgs
			FROM ` + gdeltGkgTable + gdeltIndustryFilter + `
				AND V2Organizations IS NOT NULL
		)
		SELECT
			org AS name,
			COUNT(1) AS article_count,
			ROUND(COALESCE(AVG(tone), 0), 2) AS avg_tone
		FROM docs, UNNEST(orgs) AS org
		WHERE LENGTH(org) > 2
		GROUP BY 1
		ORDER BY article_count DESC
		LIMIT 25`)
	q.Parameters = gdeltIndustryParams(start, end, themeRe)
	return collectRows[GdeltIndustryOrg](q, ctx)
}

// GetGdeltIndustrySubtopics counts articles per vertical theme, showing
// which slice of the vertical drives the coverage. Reuses GdeltNamedCount.
func (b *BQClient) GetGdeltIndustrySubtopics(ctx context.Context, start, end civil.Date, themeRe string) ([]GdeltNamedCount, error) {
	q := b.client.Query(`
		WITH docs AS (
			SELECT ARRAY(
				SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
				FROM UNNEST(SPLIT(V2Themes, ';')) AS entry
				WHERE entry != '' AND REGEXP_CONTAINS(entry, @theme_re)
			) AS themes
			FROM ` + gdeltGkgTable + gdeltIndustryFilter + `
		)
		SELECT theme AS name, COUNT(1) AS article_count
		FROM docs, UNNEST(themes) AS theme
		GROUP BY 1
		ORDER BY article_count DESC
		LIMIT 20`)
	q.Parameters = gdeltIndustryParams(start, end, themeRe)
	return collectRows[GdeltNamedCount](q, ctx)
}

// GetGdeltIndustryOutlets reuses GdeltMediaSource (media_source /
// article_count / avg_tone) for the vertical's most active outlets.
func (b *BQClient) GetGdeltIndustryOutlets(ctx context.Context, start, end civil.Date, themeRe string) ([]GdeltMediaSource, error) {
	q := b.client.Query(`
		SELECT
			SourceCommonName AS media_source,
			COUNT(1) AS article_count,
			ROUND(COALESCE(AVG(SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64)), 0), 2) AS avg_tone
		FROM ` + gdeltGkgTable + gdeltIndustryFilter + `
			AND SourceCommonName IS NOT NULL
		GROUP BY media_source
		ORDER BY article_count DESC
		LIMIT 15`)
	q.Parameters = gdeltIndustryParams(start, end, themeRe)
	return collectRows[GdeltMediaSource](q, ctx)
}

type GdeltIndustryArticle struct {
	IngestDate string  `json:"ingest_date" bigquery:"ingest_date"`
	URL        string  `json:"url" bigquery:"url"`
	Source     string  `json:"source" bigquery:"source"`
	Tone       float64 `json:"tone" bigquery:"tone"`
}

// GetGdeltIndustryArticles returns the most negative articles in the window
// — the vertical's risk feed. GKG re-processes updated pages, so QUALIFY
// keeps one row per URL.
func (b *BQClient) GetGdeltIndustryArticles(ctx context.Context, start, end civil.Date, themeRe string) ([]GdeltIndustryArticle, error) {
	q := b.client.Query(`
		SELECT
			FORMAT_DATE('%Y-%m-%d', _PARTITIONDATE) AS ingest_date,
			DocumentIdentifier AS url,
			COALESCE(SourceCommonName, '') AS source,
			ROUND(SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64), 2) AS tone
		FROM ` + gdeltGkgTable + gdeltIndustryFilter + `
			AND DocumentIdentifier IS NOT NULL
			AND SAFE_CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64) IS NOT NULL
		QUALIFY ROW_NUMBER() OVER (PARTITION BY DocumentIdentifier ORDER BY _PARTITIONDATE) = 1
		ORDER BY tone ASC
		LIMIT 25`)
	q.Parameters = gdeltIndustryParams(start, end, themeRe)
	return collectRows[GdeltIndustryArticle](q, ctx)
}
