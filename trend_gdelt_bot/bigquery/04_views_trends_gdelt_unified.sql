-- =============================================================================
-- Unified Trend & News Analytics View
-- Combines: vw_search_trends_daily and vw_gdelt_news_events_daily
-- Dataset: trends_gdelt_analytics
-- =============================================================================

CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_topic_news_trends_unified`
OPTIONS (
  description = "Unified daily analytics mart correlating Google search trends with GDELT geopolitical news events and sentiment by country and date."
) AS
WITH daily_country_news_summary AS (
  -- country_code here is already ISO 3166 (mapped from GDELT's FIPS codes in
  -- vw_gdelt_news_events_daily), so it joins 1:1 with the Google Trends ISO
  -- country codes below. The IS NOT NULL filter keeps only countries covered
  -- by both datasets.
  SELECT
    report_date,
    country_code,
    COUNT(1) AS total_news_events,
    SUM(media_mentions_count) AS total_media_mentions,
    ROUND(AVG(sentiment_tone), 2) AS country_avg_tone,
    ROUND(AVG(goldstein_scale), 2) AS country_avg_goldstein,
    -- Conflict share: percentage of events in QuadClass 3 (Verbal Conflict) or 4 (Material Conflict)
    ROUND(100.0 * COUNTIF(quad_class_id IN (3, 4)) / NULLIF(COUNT(1), 0), 1) AS conflict_event_share_pct,
    -- Top reported event category
    APPROX_TOP_COUNT(event_category, 1)[SAFE_OFFSET(0)].value AS dominant_news_category,
    -- Top reported actor
    APPROX_TOP_COUNT(primary_actor, 1)[SAFE_OFFSET(0)].value AS dominant_actor
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
ALTER COLUMN date SET OPTIONS (description = "Calendar date of the snapshot (YYYY-MM-DD)."),
ALTER COLUMN country_name SET OPTIONS (description = "Country display name."),
ALTER COLUMN country_code SET OPTIONS (description = "2-letter ISO country code."),
ALTER COLUMN search_term SET OPTIONS (description = "Search query string appearing in Google Trends daily top 25."),
ALTER COLUMN search_rank SET OPTIONS (description = "Daily rank by total search volume (1 = #1 searched query)."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest score (0-100) normalized to the term's all-time historical peak."),
ALTER COLUMN is_historical_peak SET OPTIONS (description = "True if search score equals 100 on this date."),
ALTER COLUMN country_daily_news_events SET OPTIONS (description = "Total number of global news events recorded in GDELT for this country on this date."),
ALTER COLUMN country_daily_media_mentions SET OPTIONS (description = "Total media mentions summed over event rows in this country (media-attention pulse; one article reporting several events contributes to each of them, so this is not a distinct article count)."),
ALTER COLUMN country_avg_tone SET OPTIONS (description = "Average emotional tone of news coverage in this country on this date (-100 to +100; below -2 is negative, above +2 is positive)."),
ALTER COLUMN country_avg_goldstein SET OPTIONS (description = "Average Goldstein stability impact score (-10 = extreme conflict, +10 = high cooperation)."),
ALTER COLUMN conflict_event_share_pct SET OPTIONS (description = "Percentage of news events in this country classified as Verbal or Material Conflict (0-100%)."),
ALTER COLUMN dominant_news_category SET OPTIONS (description = "The most frequently reported CAMEO event category in the country on this date."),
ALTER COLUMN dominant_actor SET OPTIONS (description = "The most frequently reported primary actor in news coverage for this country on this date.");
