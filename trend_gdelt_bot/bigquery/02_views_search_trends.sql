-- =============================================================================
-- Google Trends Curated Views
-- Source: bigquery-public-data.google_trends
-- Dataset: trends_gdelt_analytics
-- =============================================================================

-- View 1: Curated Daily Top Search Terms
--
-- Each refresh_date partition carries the full 5-year weekly score history
-- (~261 week rows per term/region) for that day's top terms. The latest week
-- per refresh_date is pinned so search_score reflects CURRENT interest;
-- without the pin, AVG(score) averages 5 years of history and the peak flag
-- is trivially true (every term has a score-100 week by definition of the
-- normalization).
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_search_trends_daily`
OPTIONS (
  description = "Daily Top 25 search terms per country from Google Trends, pinned to the latest trend week per snapshot, with averaged regional scores and peak indicators."
) AS
WITH latest_week AS (
  SELECT *
  FROM
    `bigquery-public-data.google_trends.international_top_terms`
  WHERE
    refresh_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY)
  QUALIFY
    week = MAX(week) OVER (PARTITION BY refresh_date)
)
SELECT
  refresh_date AS snapshot_date,
  country_name,
  country_code,
  term AS search_term,
  MIN(rank) AS rank,
  -- Score averaged across DMA/regions for the country (0-100 normalized to term historical peak)
  CAST(COALESCE(AVG(score), 0) AS INT64) AS search_score,
  -- Flag whether this term is currently at its all-time peak popularity (score 100)
  LOGICAL_OR(score = 100) AS is_historical_peak,
  COUNT(DISTINCT region_name) AS active_regions_count
FROM
  latest_week
GROUP BY
  snapshot_date,
  country_name,
  country_code,
  search_term;

-- Column Descriptions
ALTER VIEW `trends_gdelt_analytics.vw_search_trends_daily`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Date when the Trends snapshot was refreshed (partition key)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 2-letter country code (e.g., 'US', 'GB', 'FR')."),
ALTER COLUMN search_term SET OPTIONS (description = "The search query string that charted in top 25."),
ALTER COLUMN rank SET OPTIONS (description = "Daily cross-sectional search popularity rank (1 = highest daily search volume, 25 = 25th highest)."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for the latest trend week, normalized against the term's OWN historical peak share. 100 indicates peak volume."),
ALTER COLUMN is_historical_peak SET OPTIONS (description = "Boolean flag indicating whether the search term is at its peak search interest (100) in at least one region for the latest trend week."),
ALTER COLUMN active_regions_count SET OPTIONS (description = "Count of distinct sub-national regions/DMAs reporting this term.");

-- View 2: Curated Daily Rising / Breakout Queries
--
-- Same latest-week pinning as vw_search_trends_daily: rising-term rows also
-- carry weekly history per refresh_date partition.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_search_trends_rising`
OPTIONS (
  description = "Breakout and surging search terms with week-over-week percentage gain, pinned to the latest trend week per snapshot."
) AS
WITH latest_week AS (
  SELECT *
  FROM
    `bigquery-public-data.google_trends.international_top_rising_terms`
  WHERE
    refresh_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY)
  QUALIFY
    week = MAX(week) OVER (PARTITION BY refresh_date)
)
SELECT
  refresh_date AS snapshot_date,
  country_name,
  country_code,
  term AS search_term,
  MIN(rank) AS rank,
  CAST(COALESCE(AVG(score), 0) AS INT64) AS search_score,
  MAX(percent_gain) AS max_percent_gain,
  AVG(percent_gain) AS avg_percent_gain
FROM
  latest_week
GROUP BY
  snapshot_date,
  country_name,
  country_code,
  search_term;

-- Column Descriptions
ALTER VIEW `trends_gdelt_analytics.vw_search_trends_rising`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Date when the Trends snapshot was refreshed (partition key)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 2-letter country code (e.g., 'US', 'GB', 'FR')."),
ALTER COLUMN search_term SET OPTIONS (description = "The rising/breakout search query string."),
ALTER COLUMN rank SET OPTIONS (description = "Rank of the term among the day's rising queries (1 = fastest riser)."),
ALTER COLUMN search_score SET OPTIONS (description = "Relative search interest (0-100) for the latest trend week, normalized against the term's OWN historical peak share; 0 when volume is below reporting threshold."),
ALTER COLUMN max_percent_gain SET OPTIONS (description = "Largest week-over-week percentage gain in search interest across the country's regions. Values in the thousands indicate breakout queries."),
ALTER COLUMN avg_percent_gain SET OPTIONS (description = "Average week-over-week percentage gain in search interest across the country's regions.");
