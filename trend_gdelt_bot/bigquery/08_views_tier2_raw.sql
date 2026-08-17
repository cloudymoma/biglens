-- =============================================================================
-- Tier 2 Raw Drill-Down Proxy Views
-- Sources: bigquery-public-data.google_trends, gdelt-bq.gdeltv2
-- Dataset: trends_gdelt_analytics
--
-- WHY THESE EXIST (the "proxy view" pattern): BigQuery Conversational
-- Analytics agents cannot attach external-project tables (gdelt-bq.*,
-- bigquery-public-data.*) as Data Sources — the metadata indexer rejects
-- cross-organization lookups. These views live in trends_gdelt_analytics, so
-- the agent attaches them like any local object, while SQL execution still
-- resolves to the public source tables at query time under this project's
-- billing.
--
-- Unlike the Tier 1 curated views (90/30-day windows, aggregated), these are
-- UNAGGREGATED pass-throughs for explicit drill-down requests: full history,
-- US metro (DMA) granularity, intraday hourly snapshots, region-level rising
-- percent gains, and GKG entity lists. Columns are still
-- projected and renamed (never SELECT *): the raw GDELT/Trends column names
-- and packed "Name,offset;Name,offset" strings are hostile to NL-to-SQL, and
-- pseudo-columns like _PARTITIONDATE are not visible through a view unless
-- explicitly projected. Projecting _PARTITIONDATE AS partition_date lets
-- agent filters prune partitions through the view.
-- =============================================================================

-- View 1: Full Weekly History of International Top Terms
--
-- Each refresh_date partition carries the full ~5-year weekly score history
-- for that day's top terms (old partitions expire on a rolling basis). So
-- historical trend curves come from PINNING the latest snapshot_date and
-- scanning week — NEVER from ranging over snapshot_date, which averages
-- overlapping 5-year histories into garbage.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_international_history`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Unaggregated weekly search-interest history (~5 years) per term/country/region from Google Trends. Pin snapshot_date = MAX(snapshot_date) and scan week for historical curves; add week = MAX(week) for current values. Prefer vw_search_trends_daily for standard rankings."
) AS
SELECT
  refresh_date AS snapshot_date,
  week,
  country_name,
  country_code,
  region_name,
  region_code,
  term AS search_term,
  rank,
  score AS search_score
FROM
  `bigquery-public-data.google_trends.international_top_terms`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_international_history`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Trends snapshot refresh date (partition key — ALWAYS pin to MAX(snapshot_date) unless comparing snapshots; each snapshot repeats the full weekly history)."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to. The time axis for historical curves (~261 weeks per snapshot)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 3166-1 alpha-2 country code (e.g. 'GB', 'JP'). Joinable to Tier 1 views."),
ALTER COLUMN region_name SET OPTIONS (description = "Sub-national region name (state / province / prefecture)."),
ALTER COLUMN region_code SET OPTIONS (description = "ISO 3166-2 sub-national region code (e.g. 'GB-ENG')."),
ALTER COLUMN search_term SET OPTIONS (description = "Search query string that charted in the country's daily top 25."),
ALTER COLUMN rank SET OPTIONS (description = "Daily cross-sectional popularity rank (1-25) of the term on snapshot_date; constant across the term's history rows within one snapshot."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for this term in this region during this week, normalized to the term's own historical peak. NULL when volume is below reporting threshold.");

-- View 2: US Designated Market Area (DMA) Top Terms
--
-- US-only metro-level granularity (Nielsen DMAs) that the international
-- tables do not carry. Same weekly-history-per-snapshot layout as View 1:
-- pin snapshot_date, and pin week for current values.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_us_dma`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Daily top 25 US search terms at Designated Market Area (Nielsen metro) granularity, with ~5-year weekly history per snapshot. Pin snapshot_date = MAX(snapshot_date); add week = MAX(week) for current values. US only — use vw_search_trends_daily for countries."
) AS
SELECT
  refresh_date AS snapshot_date,
  week,
  dma_name,
  dma_id,
  term AS search_term,
  rank,
  score AS search_score
FROM
  `bigquery-public-data.google_trends.top_terms`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_us_dma`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Trends snapshot refresh date (partition key — ALWAYS pin to MAX(snapshot_date) unless comparing snapshots; each snapshot repeats the full weekly history)."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to."),
ALTER COLUMN dma_name SET OPTIONS (description = "Nielsen Designated Market Area name, e.g. 'New York NY' or 'San Francisco-Oakland-San Jose CA'."),
ALTER COLUMN dma_id SET OPTIONS (description = "Numeric Nielsen DMA identifier."),
ALTER COLUMN search_term SET OPTIONS (description = "Search query string that charted in the US daily top 25."),
ALTER COLUMN rank SET OPTIONS (description = "Daily cross-sectional popularity rank (1-25) of the term on snapshot_date."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for this term in this DMA during this week, normalized to the term's own historical peak. NULL when volume is below reporting threshold.");

-- View 3: Full Weekly History of International RISING Terms
--
-- Region-level breakout detail the Tier 1 rising view aggregates away:
-- per-region percent_gain plus the weekly score history behind each rising
-- term. Same snapshot layout and pinning rules as View 1.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_international_rising_history`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Unaggregated rising/breakout search terms per country REGION with week-over-week percent_gain and ~5-year weekly score history. Pin snapshot_date = MAX(snapshot_date); add week = MAX(week) for current values. Prefer vw_search_trends_rising for country-level rising terms."
) AS
SELECT
  refresh_date AS snapshot_date,
  week,
  country_name,
  country_code,
  region_name,
  region_code,
  term AS search_term,
  rank,
  score AS search_score,
  percent_gain
FROM
  `bigquery-public-data.google_trends.international_top_rising_terms`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_international_rising_history`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Trends snapshot refresh date (partition key — ALWAYS pin to MAX(snapshot_date) unless comparing snapshots; each snapshot repeats the full weekly history)."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 3166-1 alpha-2 country code (e.g. 'GB', 'JP'). Joinable to Tier 1 views."),
ALTER COLUMN region_name SET OPTIONS (description = "Sub-national region name (state / province / prefecture) where the term is rising."),
ALTER COLUMN region_code SET OPTIONS (description = "ISO 3166-2 sub-national region code (e.g. 'GB-ENG')."),
ALTER COLUMN search_term SET OPTIONS (description = "Rising/breakout search query string."),
ALTER COLUMN rank SET OPTIONS (description = "Rank among the day's rising queries in this region (1 = fastest riser); constant across the term's history rows within one snapshot."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for this term in this region during this week, normalized to the term's own historical peak. NULL when volume is below reporting threshold."),
ALTER COLUMN percent_gain SET OPTIONS (description = "Week-over-week percentage gain in search interest for this region. Values in the thousands indicate breakout queries.");

-- View 4: US DMA-Level RISING Terms
--
-- US metro-level companion of View 3: which terms are breaking out in which
-- Nielsen DMA, with percent_gain. Same pinning rules as View 2.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_us_dma_rising`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Rising/breakout US search terms at Designated Market Area (Nielsen metro) granularity with week-over-week percent_gain and weekly history. Pin snapshot_date = MAX(snapshot_date); add week = MAX(week) for current values. US only."
) AS
SELECT
  refresh_date AS snapshot_date,
  week,
  dma_name,
  dma_id,
  term AS search_term,
  rank,
  score AS search_score,
  percent_gain
FROM
  `bigquery-public-data.google_trends.top_rising_terms`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_us_dma_rising`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Trends snapshot refresh date (partition key — ALWAYS pin to MAX(snapshot_date) unless comparing snapshots; each snapshot repeats the full weekly history)."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to."),
ALTER COLUMN dma_name SET OPTIONS (description = "Nielsen Designated Market Area name, e.g. 'New York NY'."),
ALTER COLUMN dma_id SET OPTIONS (description = "Numeric Nielsen DMA identifier."),
ALTER COLUMN search_term SET OPTIONS (description = "Rising/breakout search query string."),
ALTER COLUMN rank SET OPTIONS (description = "Rank among the day's rising queries in this DMA (1 = fastest riser)."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for this term in this DMA during this week, normalized to the term's own historical peak. NULL when volume is below reporting threshold."),
ALTER COLUMN percent_gain SET OPTIONS (description = "Week-over-week percentage gain in search interest for this DMA. Values in the thousands indicate breakout queries.");

-- Views 5 & 6: INTRADAY Hourly US Trends (top & rising)
--
-- Source: bigquery-public-data.google_trends_hourly — US-only, DMA-level,
-- HOUR-partitioned on refresh_time (DATETIME), several snapshots per day
-- with ~30-day snapshot retention and a ~1-YEAR weekly history per snapshot
-- (verified live 2026-08-17: 129 snapshots over 31 days; latest snapshot =
-- 25 terms x 210 DMAs x 53 weeks). This is the FRESHEST source in the stack:
-- the daily tables lag 1-2 days, so "what is trending right now / today in
-- the US" should route here. Pin snapshot_time = MAX(snapshot_time), and
-- week = MAX(week) for current values.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_us_hourly`
OPTIONS (
  description = "TIER 2 DRILL-DOWN / REAL-TIME: Intraday US top 25 search terms per Nielsen DMA, refreshed several times per day (~30-day snapshot retention, ~1-year weekly history per snapshot). FRESHEST trends source — use for 'right now / today' US questions; daily views lag 1-2 days. Pin snapshot_time = MAX(snapshot_time); add week = MAX(week) for current values. US only."
) AS
SELECT
  refresh_time AS snapshot_time,
  week,
  dma_name,
  dma_id,
  term AS search_term,
  rank,
  score AS search_score
FROM
  `bigquery-public-data.google_trends_hourly.top_terms_hourly`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_us_hourly`
ALTER COLUMN snapshot_time SET OPTIONS (description = "Intraday snapshot timestamp (DATETIME, hour-partition key — ALWAYS pin to MAX(snapshot_time); each snapshot repeats the full ~1-year weekly history). Snapshots retained ~30 days."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to (~53 weeks per snapshot)."),
ALTER COLUMN dma_name SET OPTIONS (description = "Nielsen Designated Market Area name, e.g. 'New York NY'."),
ALTER COLUMN dma_id SET OPTIONS (description = "Numeric Nielsen DMA identifier."),
ALTER COLUMN search_term SET OPTIONS (description = "Search query string in the US top 25 as of this intraday snapshot."),
ALTER COLUMN rank SET OPTIONS (description = "Cross-sectional popularity rank (1-25) as of this intraday snapshot."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for this term in this DMA during this week, normalized to the term's own historical peak. NULL when volume is below reporting threshold.");

CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`
OPTIONS (
  description = "TIER 2 DRILL-DOWN / REAL-TIME: Intraday rising/breakout US search terms per Nielsen DMA with percent_gain, refreshed several times per day (~30-day snapshot retention). FRESHEST breakout signal — use for 'breaking out right now' US questions. Pin snapshot_time = MAX(snapshot_time); add week = MAX(week) for current values. US only."
) AS
SELECT
  refresh_time AS snapshot_time,
  week,
  dma_name,
  dma_id,
  term AS search_term,
  rank,
  score AS search_score,
  percent_gain
FROM
  `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly`;

ALTER VIEW `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`
ALTER COLUMN snapshot_time SET OPTIONS (description = "Intraday snapshot timestamp (DATETIME, hour-partition key — ALWAYS pin to MAX(snapshot_time); each snapshot repeats the full ~1-year weekly history). Snapshots retained ~30 days."),
ALTER COLUMN week SET OPTIONS (description = "Start date (Monday) of the trend week this score row belongs to."),
ALTER COLUMN dma_name SET OPTIONS (description = "Nielsen Designated Market Area name."),
ALTER COLUMN dma_id SET OPTIONS (description = "Numeric Nielsen DMA identifier."),
ALTER COLUMN search_term SET OPTIONS (description = "Rising/breakout search query string as of this intraday snapshot."),
ALTER COLUMN rank SET OPTIONS (description = "Rank among rising queries in this DMA (1 = fastest riser) as of this intraday snapshot."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) normalized to the term's own historical peak. NULL when volume is below reporting threshold."),
ALTER COLUMN percent_gain SET OPTIONS (description = "Week-over-week percentage gain in search interest for this DMA. Values in the thousands indicate breakout queries.");

-- View 7: Multi-Year GDELT Events Archive (2015 - present)
--
-- Unfiltered pass-through of the full GDELT 2.0 event archive with the same
-- decoding/renaming as the Tier 1 daily view (FIPS→ISO join, CAMEO root
-- decode, QuadClass names) plus archive-only columns: global_event_id,
-- event_date, full/base CAMEO codes, actor type codes. Column projection
-- keeps a worst-case unfiltered scan moderate, but queries should still
-- ALWAYS filter partition_date.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_gdelt_events_archive`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Full multi-year GDELT 2.0 news event archive (Feb 2015 - present) with decoded CAMEO/QuadClass and ISO country codes. ALWAYS filter partition_date (e.g. partition_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 180 DAY)). Prefer vw_gdelt_news_events_daily for the last 90 days."
) AS
SELECT
  e._PARTITIONDATE AS partition_date,
  e.GLOBALEVENTID AS global_event_id,
  SAFE.PARSE_DATE('%Y%m%d', CAST(e.SQLDATE AS STRING)) AS event_date,
  -- ISO 3166-1 alpha-2 (joinable to Google Trends); NULL when the action
  -- location's country is not covered by the Trends dataset.
  iso.iso_code AS country_code,
  e.ActionGeo_CountryCode AS fips_country_code,
  e.ActionGeo_FullName AS location_name,
  e.ActionGeo_ADM1Code AS admin1_code,
  e.ActionGeo_Lat AS latitude,
  e.ActionGeo_Long AS longitude,
  e.Actor1Name AS primary_actor,
  e.Actor1CountryCode AS actor1_country_code,
  e.Actor1Type1Code AS actor1_type_code,
  e.Actor2Name AS secondary_actor,
  e.Actor2CountryCode AS actor2_country_code,
  e.Actor2Type1Code AS actor2_type_code,
  e.IsRootEvent = 1 AS is_root_event,
  e.EventCode AS cameo_event_code,
  e.EventBaseCode AS cameo_base_code,
  e.EventRootCode AS cameo_root_code,
  -- Decoded CAMEO Root Taxonomy (same decode as vw_gdelt_news_events_daily)
  CASE e.EventRootCode
    WHEN '01' THEN 'Make Public Statement'
    WHEN '02' THEN 'Appeal'
    WHEN '03' THEN 'Express Intent to Cooperate'
    WHEN '04' THEN 'Consult'
    WHEN '05' THEN 'Engage in Diplomatic Cooperation'
    WHEN '06' THEN 'Engage in Material Cooperation'
    WHEN '07' THEN 'Provide Aid'
    WHEN '08' THEN 'Yield'
    WHEN '09' THEN 'Investigate'
    WHEN '10' THEN 'Demand'
    WHEN '11' THEN 'Disapprove'
    WHEN '12' THEN 'Reject'
    WHEN '13' THEN 'Threaten'
    WHEN '14' THEN 'Protest'
    WHEN '15' THEN 'Exhibit Military Posture'
    WHEN '16' THEN 'Reduce Relations'
    WHEN '17' THEN 'Coerce'
    WHEN '18' THEN 'Assault'
    WHEN '19' THEN 'Fight'
    WHEN '20' THEN 'Use Unconventional Mass Violence'
    ELSE 'Other / Unclassified'
  END AS event_category,
  e.QuadClass AS quad_class_id,
  CASE e.QuadClass
    WHEN 1 THEN 'Verbal Cooperation'
    WHEN 2 THEN 'Material Cooperation'
    WHEN 3 THEN 'Verbal Conflict'
    WHEN 4 THEN 'Material Conflict'
    ELSE 'Unknown'
  END AS quad_class_name,
  e.GoldsteinScale AS goldstein_scale,
  e.AvgTone AS sentiment_tone,
  e.NumMentions AS media_mentions_count,
  e.NumSources AS distinct_sources_count,
  e.NumArticles AS article_count,
  e.SOURCEURL AS source_article_url
FROM
  `gdelt-bq.gdeltv2.events_partitioned` AS e
LEFT JOIN
  `trends_gdelt_analytics.dim_fips_iso_country` AS iso
ON
  e.ActionGeo_CountryCode = iso.fips_code;

ALTER VIEW `trends_gdelt_analytics.vw_raw_gdelt_events_archive`
ALTER COLUMN partition_date SET OPTIONS (description = "Ingestion partition date (from _PARTITIONDATE). ALWAYS filter this column to prune the multi-year archive (e.g. partition_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 180 DAY))."),
ALTER COLUMN global_event_id SET OPTIONS (description = "GDELT globally unique event identifier."),
ALTER COLUMN event_date SET OPTIONS (description = "Date the event occurred (parsed from SQLDATE); can precede partition_date when old events are re-reported."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 3166-1 alpha-2 country code of the action location, mapped from FIPS via dim_fips_iso_country; NULL for countries not covered by Google Trends."),
ALTER COLUMN fips_country_code SET OPTIONS (description = "Raw FIPS 10-4 country code (GDELT native). NEVER join directly to Trends ISO codes (FIPS 'GB' = Gabon, ISO 'GB' = United Kingdom)."),
ALTER COLUMN location_name SET OPTIONS (description = "Full human-readable action location (city, region, country)."),
ALTER COLUMN admin1_code SET OPTIONS (description = "FIPS-based first-order administrative division code of the action location (e.g. 'USCA' = California, US)."),
ALTER COLUMN latitude SET OPTIONS (description = "Latitude of the action location centroid."),
ALTER COLUMN longitude SET OPTIONS (description = "Longitude of the action location centroid."),
ALTER COLUMN primary_actor SET OPTIONS (description = "Name of Actor1 (initiator) as reported, e.g. 'UNITED STATES', 'PROTESTERS'."),
ALTER COLUMN actor1_country_code SET OPTIONS (description = "CAMEO 3-letter country affiliation code of Actor1 (e.g. 'USA', 'CHN')."),
ALTER COLUMN actor1_type_code SET OPTIONS (description = "CAMEO type/role code of Actor1 (e.g. 'GOV' government, 'MIL' military, 'BUS' business)."),
ALTER COLUMN secondary_actor SET OPTIONS (description = "Name of Actor2 (recipient/target) as reported."),
ALTER COLUMN actor2_country_code SET OPTIONS (description = "CAMEO 3-letter country affiliation code of Actor2."),
ALTER COLUMN actor2_type_code SET OPTIONS (description = "CAMEO type/role code of Actor2."),
ALTER COLUMN is_root_event SET OPTIONS (description = "TRUE if this event was the lead/root event of its source article (best row for deduplicating one-story-many-events)."),
ALTER COLUMN cameo_event_code SET OPTIONS (description = "Full 3-4 digit CAMEO action code (300+ subcodes, '010'-'204'), e.g. '1411' = demonstrate for leadership change."),
ALTER COLUMN cameo_base_code SET OPTIONS (description = "3-digit CAMEO base code (level 2 of the taxonomy)."),
ALTER COLUMN cameo_root_code SET OPTIONS (description = "2-digit CAMEO root category code ('01'-'20')."),
ALTER COLUMN event_category SET OPTIONS (description = "Decoded English name of the CAMEO root category (e.g. 'Protest', 'Fight')."),
ALTER COLUMN quad_class_id SET OPTIONS (description = "Primary event classification: 1=Verbal Cooperation, 2=Material Cooperation, 3=Verbal Conflict, 4=Material Conflict."),
ALTER COLUMN quad_class_name SET OPTIONS (description = "Decoded QuadClass name."),
ALTER COLUMN goldstein_scale SET OPTIONS (description = "Goldstein stability impact score (-10.0 extreme conflict/destabilizing to +10.0 high cooperation)."),
ALTER COLUMN sentiment_tone SET OPTIONS (description = "Average tone of coverage (-100 to +100; real-world values typically -10 to +10; < -2 clearly negative, > +2 positive)."),
ALTER COLUMN media_mentions_count SET OPTIONS (description = "Number of mentions of this event across all source documents (media-attention pulse)."),
ALTER COLUMN distinct_sources_count SET OPTIONS (description = "Number of distinct information sources reporting the event."),
ALTER COLUMN article_count SET OPTIONS (description = "Number of source articles containing the event."),
ALTER COLUMN source_article_url SET OPTIONS (description = "URL of a representative news article reporting the event.");

-- View 8: GDELT GKG Entity Archive (rolling 2 years)
--
-- Entity-level drill-down the Tier 1 themes view drops: every mentioned
-- person, organization, and theme per article, plus the full tone vector.
-- The packed "Name,charOffset;Name,charOffset" V2 strings are pre-parsed
-- into clean STRING arrays (offsets stripped) so the agent never has to
-- SPLIT them itself.
--
-- HARD COST BOUND: unlike the events archive, GKG is tens of terabytes and
-- its entity/theme string columns dominate the bytes. A rolling 2-year
-- window is baked into the view as a blast-radius bound for unfiltered
-- agent queries — widen it here deliberately if you truly need older GKG.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive`
OPTIONS (
  description = "TIER 2 DRILL-DOWN: Per-article GDELT Global Knowledge Graph entities (persons, organizations, themes as clean arrays) and full tone vector, rolling 2-year window (hard bound baked into the view for cost safety). ALWAYS also filter partition_date. Prefer vw_gdelt_gkg_themes_daily for the last 30 days."
) AS
SELECT
  _PARTITIONDATE AS partition_date,
  GKGRECORDID AS gkg_record_id,
  SourceCommonName AS media_source,
  DocumentIdentifier AS document_url,
  -- V2 entries are packed as "Name,charOffset;Name,charOffset;..." — strip
  -- offsets and expose clean arrays.
  ARRAY(
    SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
    FROM UNNEST(SPLIT(V2Persons, ';')) AS entry
    WHERE entry != ''
  ) AS persons,
  ARRAY(
    SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
    FROM UNNEST(SPLIT(V2Organizations, ';')) AS entry
    WHERE entry != ''
  ) AS organizations,
  ARRAY(
    SELECT DISTINCT SPLIT(entry, ',')[SAFE_OFFSET(0)]
    FROM UNNEST(SPLIT(V2Themes, ';')) AS entry
    WHERE entry != ''
  ) AS themes,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64) AS sentiment_tone,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(1)] AS FLOAT64) AS positive_score,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(2)] AS FLOAT64) AS negative_score,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(3)] AS FLOAT64) AS polarity_score
FROM
  `gdelt-bq.gdeltv2.gkg_partitioned`
WHERE
  _PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL 730 DAY);

ALTER VIEW `trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive`
ALTER COLUMN partition_date SET OPTIONS (description = "Ingestion partition date (from _PARTITIONDATE). ALWAYS filter this column; the view itself is hard-bounded to the last 730 days."),
ALTER COLUMN gkg_record_id SET OPTIONS (description = "GDELT GKG unique record identifier."),
ALTER COLUMN media_source SET OPTIONS (description = "News outlet domain (e.g. 'bbc.co.uk')."),
ALTER COLUMN document_url SET OPTIONS (description = "URL of the source article."),
ALTER COLUMN persons SET OPTIONS (description = "All person names mentioned in the article (deduplicated, offsets stripped). Query with UNNEST, e.g. WHERE 'Emmanuel Macron' IN UNNEST(persons)."),
ALTER COLUMN organizations SET OPTIONS (description = "All organization names mentioned in the article (deduplicated, offsets stripped). Query with UNNEST."),
ALTER COLUMN themes SET OPTIONS (description = "All GKG theme codes tagged on the article (deduplicated, offsets stripped), e.g. 'TAX_DISEASE', 'PROTEST'. Query with UNNEST."),
ALTER COLUMN sentiment_tone SET OPTIONS (description = "Overall document tone (-100 to +100; real-world values typically -10 to +10)."),
ALTER COLUMN positive_score SET OPTIONS (description = "Percentage of words with positive emotional connotation."),
ALTER COLUMN negative_score SET OPTIONS (description = "Percentage of words with negative emotional connotation."),
ALTER COLUMN polarity_score SET OPTIONS (description = "Percentage of emotionally charged words (how polarized the coverage is).");
