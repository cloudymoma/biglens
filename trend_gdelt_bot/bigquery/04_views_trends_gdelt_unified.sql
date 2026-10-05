-- =============================================================================
-- Unified Trend & News Analytics View
-- Combines: vw_search_trends_daily and vw_gdelt_news_events_daily
-- Dataset: trends_gdelt_analytics
-- =============================================================================

CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_topic_news_trends_unified`
OPTIONS (
  description = "Unified daily analytics mart correlating Google search trends with GDELT geopolitical news events and sentiment by country and date (~30-day rolling Trends window). Note: country-level news metrics cover `date` only and repeat across the 25 search_term rows for each (date, country_code) — use ANY_VALUE/MAX when rolling up news metrics by country, never SUM across terms. Built on the international Trends views, which exclude the US; use vw_raw_trends_us_* for US search questions."
) AS
WITH daily_country_news_summary AS (
  -- country_code here is already ISO 3166 (mapped from GDELT's FIPS codes in
  -- vw_gdelt_news_events_daily), so it joins 1:1 with the Google Trends ISO
  -- country codes below.
  SELECT
    report_date,
    country_code,
    COUNT(1) AS total_news_events,
    SUM(media_mentions_count) AS total_media_mentions,
    ROUND(AVG(sentiment_tone), 2) AS country_avg_tone,
    ROUND(AVG(goldstein_scale), 2) AS country_avg_goldstein,
    -- Conflict share: percentage of events in QuadClass 3 (Verbal Conflict) or 4 (Material Conflict)
    ROUND(100.0 * COUNTIF(quad_class_id IN (3, 4)) / NULLIF(COUNT(1), 0), 1) AS conflict_event_share_pct,
    -- Top reported event category (ignoring NULLs)
    (SELECT value FROM UNNEST(APPROX_TOP_COUNT(event_category, 2)) WHERE value IS NOT NULL LIMIT 1) AS dominant_news_category,
    -- Top reported actor (ignoring NULLs so missing Actor1Name never masks the leading named actor)
    (SELECT value FROM UNNEST(APPROX_TOP_COUNT(primary_actor, 2)) WHERE value IS NOT NULL LIMIT 1) AS dominant_actor
  FROM
    `trends_gdelt_analytics.vw_gdelt_news_events_daily`
  WHERE
    country_code IS NOT NULL
  GROUP BY
    report_date,
    country_code
)
SELECT
  t.snapshot_date AS date,
  t.country_name,
  t.country_code,
  t.search_term,
  t.rank AS search_rank,
  t.search_score,
  t.is_historical_peak,
  -- Geopolitical & News Context for that country and date
  COALESCE(n.total_news_events, 0) AS country_daily_news_events,
  COALESCE(n.total_media_mentions, 0) AS country_daily_media_mentions,
  n.country_avg_tone,
  n.country_avg_goldstein,
  n.conflict_event_share_pct,
  n.dominant_news_category,
  n.dominant_actor
FROM
  `trends_gdelt_analytics.vw_search_trends_daily` t
LEFT JOIN
  daily_country_news_summary n
ON
  t.snapshot_date = n.report_date
  AND t.country_code = n.country_code;

-- Column Descriptions
ALTER VIEW `trends_gdelt_analytics.vw_topic_news_trends_unified`
ALTER COLUMN date SET OPTIONS (description = "Calendar date of the Trends snapshot and GDELT ingestion partition (YYYY-MM-DD)."),
ALTER COLUMN country_name SET OPTIONS (description = "Country display name."),
ALTER COLUMN country_code SET OPTIONS (description = "2-letter ISO country code (excludes 'US'; use vw_raw_trends_us_* for US search trends)."),
ALTER COLUMN search_term SET OPTIONS (description = "Search query string appearing in Google Trends daily top 25."),
ALTER COLUMN search_rank SET OPTIONS (description = "Daily rank by total search volume (1 = #1 searched query)."),
ALTER COLUMN search_score SET OPTIONS (description = "Equal-weighted mean of sub-national region scores (0-100) for the latest trend week (which starts on Sunday and may be a partial week early in the week), across regions above Google's reporting threshold."),
ALTER COLUMN is_historical_peak SET OPTIONS (description = "True if the term reached score 100 in at least one sub-national region for the latest trend week (does NOT imply the regional-average search_score equals 100)."),
ALTER COLUMN country_daily_news_events SET OPTIONS (description = "Total number of GDELT news events whose action location is in this country on `date` (country-level constant repeated on every term row for that country/date — aggregate with ANY_VALUE/MAX, never SUM across terms)."),
ALTER COLUMN country_daily_media_mentions SET OPTIONS (description = "First-15-minute-window media mentions summed over event rows in this country on `date` (repeated on every term row for that country/date — aggregate with ANY_VALUE/MAX, never SUM across terms)."),
ALTER COLUMN country_avg_tone SET OPTIONS (description = "Event-weighted mean sentiment tone over events whose action location is in this country on `date` (-100 to +100; below -2 is negative, above +2 is positive; repeated on every term row)."),
ALTER COLUMN country_avg_goldstein SET OPTIONS (description = "Event-weighted mean Goldstein stability impact score over events whose action location is in this country on `date` (-10 = extreme conflict, +10 = high cooperation; repeated on every term row)."),
ALTER COLUMN conflict_event_share_pct SET OPTIONS (description = "Percentage of news events in this country on `date` classified as Verbal or Material Conflict (QuadClass 3 or 4, 0-100%; repeated on every term row)."),
ALTER COLUMN dominant_news_category SET OPTIONS (description = "Most frequently reported non-null CAMEO event category in the country on `date`."),
ALTER COLUMN dominant_actor SET OPTIONS (description = "Most frequently reported non-null primary actor (Actor1Name) in news coverage for this country on `date`.");
