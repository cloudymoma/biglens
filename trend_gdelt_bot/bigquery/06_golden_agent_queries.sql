-- =============================================================================
-- Golden / Verified Queries for BigQuery Conversational Analytical Agent
-- Dataset: trends_gdelt_analytics
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Query 1: Latest Daily Top Search Terms in a Specific Country
-- Intent: "What are the top 10 search terms in Great Britain right now?"
-- Note: Trends snapshots publish with a lag of 1-2 days, so pin the latest
-- available snapshot date via QUALIFY instead of assuming CURRENT_DATE() - N
-- exists. The 7-day range bound keeps partition pruning effective.
-- -----------------------------------------------------------------------------
SELECT
  search_term,
  search_rank,
  search_score,
  is_historical_peak
FROM
  `trends_gdelt_analytics.vw_topic_news_trends_unified`
WHERE
  country_code = 'GB'
  AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
QUALIFY
  date = MAX(date) OVER ()
ORDER BY
  search_rank ASC
LIMIT 10;

-- -----------------------------------------------------------------------------
-- Query 2: Breakout / All-Time Peak Terms (Score = 100) vs Everyday Volume (Rank)
-- Intent: "Which search terms reached their all-time peak popularity in the US last week?"
-- -----------------------------------------------------------------------------
SELECT
  date,
  search_term,
  search_rank,
  search_score
FROM
  `trends_gdelt_analytics.vw_topic_news_trends_unified`
WHERE
  country_code = 'US'
  AND is_historical_peak = TRUE
  AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
ORDER BY
  date DESC, search_rank ASC;

-- -----------------------------------------------------------------------------
-- Query 3: Search Terms Trending During Negative Geopolitical News Events
-- Intent: "Show search trends in countries where news sentiment was heavily negative (Tone < -2.0)."
-- -----------------------------------------------------------------------------
SELECT
  date,
  country_name,
  country_avg_tone,
  dominant_news_category,
  conflict_event_share_pct,
  search_term,
  search_rank,
  search_score
FROM
  `trends_gdelt_analytics.vw_topic_news_trends_unified`
WHERE
  date >= DATE_SUB(CURRENT_DATE(), INTERVAL 14 DAY)
  AND country_avg_tone < -2.0
  AND conflict_event_share_pct > 30.0
ORDER BY
  conflict_event_share_pct DESC, search_rank ASC
LIMIT 20;

-- -----------------------------------------------------------------------------
-- Query 4: Cross-Country Search Diffusion
-- Intent: "Which terms charted in the top 5 across 3 or more countries simultaneously?"
-- -----------------------------------------------------------------------------
SELECT
  date,
  search_term,
  COUNT(DISTINCT country_code) AS country_count,
  STRING_AGG(country_name, ', ' ORDER BY country_name) AS countries,
  AVG(search_score) AS avg_global_score
FROM
  `trends_gdelt_analytics.vw_topic_news_trends_unified`
WHERE
  date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
  AND search_rank <= 5
GROUP BY
  date, search_term
HAVING
  country_count >= 3
ORDER BY
  country_count DESC, date DESC;

-- -----------------------------------------------------------------------------
-- Query 5: Breaking Conflict Events with High Media Attention
-- Intent: "What are the most heavily covered conflict events in GDELT recently?"
-- Note: GDELT emits several event rows per source article, so QUALIFY keeps
-- only the highest-mention row per article URL — otherwise one big story
-- floods the list.
-- -----------------------------------------------------------------------------
SELECT
  report_date,
  country_code,
  location_name,
  primary_actor,
  secondary_actor,
  event_category,
  goldstein_scale,
  sentiment_tone,
  media_mentions_count,
  source_article_url
FROM
  `trends_gdelt_analytics.vw_gdelt_news_events_daily`
WHERE
  report_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 3 DAY)
  AND quad_class_id IN (3, 4) -- Verbal or Material Conflict
  AND source_article_url IS NOT NULL
QUALIFY
  ROW_NUMBER() OVER (PARTITION BY source_article_url ORDER BY media_mentions_count DESC) = 1
ORDER BY
  media_mentions_count DESC
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 6 (TIER 2): Multi-Year Weekly Trend Trajectory for a Term
-- Intent: "Show the 5-year search interest curve for the UK's current #1 term."
-- Note: Each snapshot_date carries the FULL ~5-year weekly history, so pin
-- snapshot_date = MAX(snapshot_date) and scan week. NEVER range over
-- snapshot_date for history — that averages overlapping histories.
-- -----------------------------------------------------------------------------
SELECT
  search_term,
  week,
  CAST(AVG(search_score) AS INT64) AS avg_weekly_score
FROM
  `trends_gdelt_analytics.vw_raw_trends_international_history`
WHERE
  snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_international_history`)
  AND country_code = 'GB'
  AND rank = 1
GROUP BY
  search_term, week
ORDER BY
  week ASC;

-- -----------------------------------------------------------------------------
-- Query 7 (TIER 2): US Metro-Level (DMA) Breakdown of Today's Top Terms
-- Intent: "Which search terms chart in the top 3 across the most US metro areas?"
-- Note: COUNT(DISTINCT dma_name) also collapses the repeated weekly-history
-- rows within the pinned snapshot.
-- -----------------------------------------------------------------------------
SELECT
  search_term,
  COUNT(DISTINCT dma_name) AS dma_count,
  MIN(rank) AS best_rank
FROM
  `trends_gdelt_analytics.vw_raw_trends_us_dma`
WHERE
  snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_us_dma`)
  AND rank <= 3
GROUP BY
  search_term
ORDER BY
  dma_count DESC
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 8 (TIER 2): Historical News Event Archive Lookup (beyond 90 days)
-- Intent: "What were the most covered protest events in France in Q1 2023?"
-- Note: partition_date filter is MANDATORY on the archive view — it prunes
-- a decade of partitions. is_root_event + QUALIFY deduplicate one-story-
-- many-events noise.
-- -----------------------------------------------------------------------------
SELECT
  event_date,
  location_name,
  primary_actor,
  secondary_actor,
  cameo_event_code,
  event_category,
  media_mentions_count,
  source_article_url
FROM
  `trends_gdelt_analytics.vw_raw_gdelt_events_archive`
WHERE
  partition_date BETWEEN '2023-01-01' AND '2023-03-31'
  AND country_code = 'FR'
  AND cameo_root_code = '14' -- Protest
  AND is_root_event
QUALIFY
  ROW_NUMBER() OVER (PARTITION BY source_article_url ORDER BY media_mentions_count DESC) = 1
ORDER BY
  media_mentions_count DESC
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 9 (TIER 2): Entity-Level News Coverage from the GKG Archive
-- Intent: "Show the most negative coverage mentioning Emmanuel Macron in the last 90 days."
-- Note: persons/organizations/themes are clean arrays — filter with
-- IN UNNEST(...). The view is hard-bounded to a rolling 2-year window.
-- -----------------------------------------------------------------------------
SELECT
  partition_date,
  media_source,
  document_url,
  sentiment_tone,
  organizations
FROM
  `trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive`
WHERE
  partition_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY)
  AND 'Emmanuel Macron' IN UNNEST(persons)
ORDER BY
  sentiment_tone ASC
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 10 (TIER 2 / REAL-TIME): What Is Trending in the US RIGHT NOW
-- Intent: "What are Americans searching for right now / today?"
-- Note: The hourly views are the FRESHEST source (several intraday snapshots
-- per day; the daily views lag 1-2 days). Pin BOTH snapshot_time and week,
-- then aggregate across DMAs for the national picture.
-- -----------------------------------------------------------------------------
WITH latest_snapshot AS (
  SELECT *
  FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`
  WHERE snapshot_time = (SELECT MAX(snapshot_time) FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`)
  QUALIFY week = MAX(week) OVER ()
)
SELECT
  search_term,
  MIN(rank) AS best_rank,
  CAST(AVG(search_score) AS INT64) AS avg_dma_score,
  COUNT(DISTINCT dma_name) AS active_dma_count
FROM
  latest_snapshot
GROUP BY
  search_term
ORDER BY
  best_rank ASC
LIMIT 25;

-- -----------------------------------------------------------------------------
-- Query 11 (TIER 2 / REAL-TIME): US Terms Breaking Out RIGHT NOW
-- Intent: "Which searches are spiking/breaking out in the US at this moment, and where?"
-- -----------------------------------------------------------------------------
WITH latest_snapshot AS (
  SELECT *
  FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`
  WHERE snapshot_time = (SELECT MAX(snapshot_time) FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`)
  QUALIFY week = MAX(week) OVER ()
)
SELECT
  search_term,
  MAX(percent_gain) AS max_percent_gain,
  COUNT(DISTINCT dma_name) AS rising_dma_count,
  STRING_AGG(DISTINCT dma_name ORDER BY dma_name LIMIT 5) AS sample_dmas
FROM
  latest_snapshot
GROUP BY
  search_term
ORDER BY
  max_percent_gain DESC
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 12 (TIER 2): Where a Term Is Rising — US Metro (DMA) Breakout Map
-- Intent: "In which US metro areas is the top rising term breaking out the hardest?"
-- -----------------------------------------------------------------------------
SELECT
  search_term,
  dma_name,
  percent_gain,
  rank
FROM
  `trends_gdelt_analytics.vw_raw_trends_us_dma_rising`
WHERE
  snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_us_dma_rising`)
  AND rank = 1
QUALIFY
  week = MAX(week) OVER ()
ORDER BY
  percent_gain DESC
LIMIT 20;

-- -----------------------------------------------------------------------------
-- Query 13 (TIER 2): Region-Level Rising Terms Inside a Country
-- Intent: "Which regions of Japan are driving today's biggest breakout query?"
-- Note: Tier 1 vw_search_trends_rising aggregates regions away — this view
-- keeps the per-region percent_gain.
-- -----------------------------------------------------------------------------
SELECT
  search_term,
  region_name,
  percent_gain,
  search_score
FROM
  `trends_gdelt_analytics.vw_raw_trends_international_rising_history`
WHERE
  snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_international_rising_history`)
  AND country_code = 'JP'
QUALIFY
  week = MAX(week) OVER ()
ORDER BY
  percent_gain DESC
LIMIT 20;

-- Query 14 (graph traversal via GRAPH_TABLE) lives in 07_graph_golden_query.sql:
-- it requires an Enterprise edition reservation, so it is preflighted separately.
