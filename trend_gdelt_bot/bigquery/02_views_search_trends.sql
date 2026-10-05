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
  description = "Daily Top 25 search terms per country from Google Trends (~30-day rolling snapshot retention), pinned to the latest trend week per snapshot, with averaged regional scores and regional peak indicators. International views exclude the US; use vw_raw_trends_us_* for US questions."
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
  -- Equal-weighted mean across sub-national regions with a reportable score (NULL when all regions are below threshold)
  CAST(ROUND(AVG(score)) AS INT64) AS search_score,
  -- Flag whether this term is at its local historical peak (score 100) in at least one region
  LOGICAL_OR(score = 100) AS is_historical_peak,
  -- Count of sub-national regions with a reportable score (score IS NOT NULL)
  COUNTIF(score IS NOT NULL) AS active_regions_count
FROM
  latest_week
GROUP BY
  snapshot_date,
  country_name,
  country_code,
  search_term;

-- Column Descriptions
ALTER VIEW `trends_gdelt_analytics.vw_search_trends_daily`
ALTER COLUMN snapshot_date SET OPTIONS (description = "Date when the Trends snapshot was refreshed (partition key; source retains ~30 days of daily snapshots, each carrying ~5 years of weekly history)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 2-letter country code (e.g., 'GB', 'JP', 'FR'). The US is not included; use vw_raw_trends_us_* for US questions."),
ALTER COLUMN search_term SET OPTIONS (description = "The search query string that charted in top 25."),
ALTER COLUMN rank SET OPTIONS (description = "Daily cross-sectional search popularity rank (1 = highest daily search volume, 25 = 25th highest)."),
ALTER COLUMN search_score SET OPTIONS (description = "Equal-weighted mean of sub-national region scores (0-100, each normalized to that region's own ~5-year peak) for the latest trend week across regions above Google's reporting threshold; NULL when all regions are below threshold."),
ALTER COLUMN is_historical_peak SET OPTIONS (description = "Boolean flag indicating whether the search term is at its local 5-year peak (score = 100) in at least one sub-national region for the latest trend week (does NOT imply the regional-average search_score is 100)."),
ALTER COLUMN active_regions_count SET OPTIONS (description = "Count of sub-national regions with a non-null search score (above Google Trends privacy threshold) in the latest trend week.");

-- View 2: Curated Daily Rising / Breakout Queries
--
-- Same latest-week pinning as vw_search_trends_daily: rising-term rows also
-- carry weekly history per refresh_date partition.
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_search_trends_rising`
OPTIONS (
  description = "Breakout and surging search terms with national percentage gain (~30-day rolling snapshot retention), pinned to the latest trend week per snapshot. International views exclude the US; use vw_raw_trends_us_* for US questions."
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
  CAST(ROUND(AVG(score)) AS INT64) AS search_score,
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
ALTER COLUMN snapshot_date SET OPTIONS (description = "Date when the Trends snapshot was refreshed (partition key; source retains ~30 days of daily snapshots)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country."),
ALTER COLUMN country_code SET OPTIONS (description = "ISO 2-letter country code (e.g., 'GB', 'JP', 'FR'). The US is not included; use vw_raw_trends_us_* for US questions."),
ALTER COLUMN search_term SET OPTIONS (description = "The rising/breakout search query string."),
ALTER COLUMN rank SET OPTIONS (description = "National rank of the term among the day's rising queries (1 = fastest riser)."),
ALTER COLUMN search_score SET OPTIONS (description = "Equal-weighted mean of sub-national region scores (0-100) for the latest trend week across regions above the reporting threshold; NULL when all regions are below threshold."),
ALTER COLUMN max_percent_gain SET OPTIONS (description = "National percentage gain reported by Google Trends for this rising term on snapshot_date (percent_gain is a country-level constant repeated across all region rows)."),
ALTER COLUMN avg_percent_gain SET OPTIONS (description = "National percentage gain (identical to max_percent_gain because percent_gain is a country-level constant across regions; retained for schema compatibility).");
