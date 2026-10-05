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
-- Query 2 (TIER 2 / US): Terms That Hit Their All-Time (5-Year) Peak in the US Last Week
-- Intent: "Which search terms reached their all-time peak popularity in the US last week?"
-- Note: Tier 1 (vw_search_trends_daily/_rising, vw_topic_news_trends_unified)
-- is built from Google Trends' international tables, which do NOT include the
-- US — country_code = 'US' there always returns 0 rows. US questions must use
-- the vw_raw_trends_us_* views.
-- Definition: the US tables carry no national score. search_score is
-- normalized per DMA (metro) to the term's own peak in that DMA over the
-- snapshot's ~5-year weekly history, so a term "peaked last week" in a metro
-- when its score is 100 in the latest COMPLETE Sunday-Saturday week before
-- the snapshot date. Each term is summarized by how many DMAs hit that peak,
-- out of the DMAs with a reportable score (NULL = below Google's threshold).
-- Limitations: only the latest snapshot's 25 national top terms carry
-- history, so a term that peaked last week but has since left the top 25 is
-- not visible (an empty result does not mean no US term peaked); "all-time"
-- means the ~5-year window; the in-progress current week is skipped because
-- a partial week is not comparable to full weeks (it inflates one-day spikes).
-- week_start names the week evaluated: with the 1-2 day publishing lag it can
-- be the week before the calendar's last week. The constant 3-day bound +
-- QUALIFY pins the latest snapshot and keeps partition pruning.
-- -----------------------------------------------------------------------------
WITH latest_snapshot AS (
  SELECT snapshot_date, week, search_term, rank, search_score
  FROM `trends_gdelt_analytics.vw_raw_trends_us_dma`
  WHERE snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 3 DAY)
  QUALIFY snapshot_date = MAX(snapshot_date) OVER ()
),
last_complete_week AS (
  SELECT week, search_term, rank, search_score
  FROM latest_snapshot
  WHERE DATE_ADD(week, INTERVAL 6 DAY) < snapshot_date  -- the week ended before the snapshot
  QUALIFY week = MAX(week) OVER ()
)
SELECT
  week AS week_start,
  search_term,
  ANY_VALUE(rank) AS national_rank,  -- rank in the latest daily US top 25
  COUNTIF(search_score = 100) AS dmas_at_peak,
  COUNTIF(search_score IS NOT NULL) AS dmas_with_signal,
  ROUND(100 * SAFE_DIVIDE(COUNTIF(search_score = 100), COUNTIF(search_score IS NOT NULL)), 1) AS pct_dmas_at_peak
FROM
  last_complete_week
GROUP BY
  week_start, search_term
HAVING
  dmas_at_peak > 0
ORDER BY
  dmas_at_peak DESC, national_rank;

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
-- Query 11 (TIER 2 / REAL-TIME): US Terms Breaking Out RIGHT NOW, and Where
-- Intent: "Which searches are spiking/breaking out in the US at this moment, and where?"
-- Note: percent_gain and rank are NATIONAL values repeated on every DMA row,
-- and every term has a row in all ~210 DMAs (NULL score = below Google's
-- reporting threshold), so COUNT(DISTINCT dma_name) is ~210 for every term.
-- "Where" comes from each DMA's own latest-week search_score: how many DMAs
-- report a score, how many are at their local peak (100), and the hottest
-- DMAs (ties broken by the week-over-week change of their score).
-- Limitations: a DMA's score is relative to that DMA's own ~1-year peak for
-- the term (how unusual local interest is, not search volume); the latest
-- week is still in progress (it can hold a single day early in the week),
-- which can push a one-day spike to 100 in most DMAs at once.
-- The constant 2-day bound + QUALIFY pins the latest snapshot and keeps
-- partition pruning; a filter like = (SELECT MAX(snapshot_time) ...) would
-- scan all partitions.
-- -----------------------------------------------------------------------------
WITH latest_snapshot AS (
  SELECT week, dma_name, search_term, rank, percent_gain, search_score
  FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`
  WHERE snapshot_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 2 DAY)
  QUALIFY snapshot_time = MAX(snapshot_time) OVER ()
),
latest_week AS (
  SELECT
    dma_name, search_term, rank, percent_gain, search_score,
    search_score - LAG(search_score) OVER (PARTITION BY search_term, dma_name ORDER BY week) AS dma_score_wow_change
  FROM latest_snapshot
  QUALIFY week = MAX(week) OVER ()
)
SELECT
  search_term,
  ANY_VALUE(percent_gain) AS national_percent_gain,  -- same value on every DMA row
  ANY_VALUE(rank) AS national_rank,
  COUNTIF(search_score IS NOT NULL) AS dmas_with_signal,
  COUNTIF(search_score = 100) AS dmas_at_local_peak,
  ARRAY_AGG(
    IF(search_score IS NOT NULL, STRUCT(dma_name, search_score, dma_score_wow_change), NULL)
    IGNORE NULLS
    -- ties: DMAs below threshold last week (NULL change) first, then the biggest rise
    ORDER BY search_score DESC, dma_score_wow_change IS NULL DESC, dma_score_wow_change DESC, dma_name
    LIMIT 5
  ) AS hottest_dmas
FROM
  latest_week
GROUP BY
  search_term
ORDER BY
  national_percent_gain DESC, national_rank
LIMIT 15;

-- -----------------------------------------------------------------------------
-- Query 12 (TIER 2): Where Today's Top Rising Term Is Hottest — US Metros (DMA)
-- Intent: "In which US metro areas is the top rising term breaking out the hardest?"
-- Note: rank and percent_gain are NATIONAL values repeated unchanged on every
-- DMA row (the table is a full term x ~210-DMA grid), so ORDER BY percent_gain
-- only returns arbitrary metros. Report percent_gain once, as the national
-- figure, and rank metros by their OWN latest-week search_score, with the
-- week-over-week change of that score as a local "warming" proxy.
-- Limitations: a DMA's score is relative to that DMA's own ~5-year peak for
-- the term (how unusual local interest is, not search volume); NULL = below
-- Google's reporting threshold (dropped, never treated as 0); the latest week
-- is still in progress, and many metros can tie at 100 (their own peak),
-- hence the tie-breakers.
-- The constant 3-day bound (Trends publishes with a 1-2 day lag) + QUALIFY
-- pins the latest snapshot and keeps partition pruning; a filter like
-- = (SELECT MAX(snapshot_date) ...) would scan all partitions.
-- -----------------------------------------------------------------------------
WITH top_riser AS (
  SELECT snapshot_date, week, dma_name, search_term, percent_gain, search_score
  FROM `trends_gdelt_analytics.vw_raw_trends_us_dma_rising`
  WHERE snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 3 DAY)
    AND rank = 1  -- the national #1 rising term
  QUALIFY snapshot_date = MAX(snapshot_date) OVER ()
)
SELECT
  search_term,
  dma_name,
  percent_gain AS national_percent_gain,  -- same value on every DMA row
  search_score AS dma_score_latest_week,
  search_score - LAG(search_score) OVER (PARTITION BY search_term, dma_name ORDER BY week) AS dma_score_wow_change
FROM
  top_riser
QUALIFY
  week = MAX(week) OVER ()
  AND search_score IS NOT NULL
ORDER BY
  dma_score_latest_week DESC,
  dma_score_wow_change DESC NULLS FIRST,  -- NULL = below threshold last week (newly emerged)
  dma_name
LIMIT 20;

-- -----------------------------------------------------------------------------
-- Query 13 (TIER 2): Where a Country's Top Breakout Query Is Hottest — Regions
-- Intent: "Which regions of Japan are driving today's biggest breakout query?"
-- Note: Tier 1 vw_search_trends_rising aggregates regions away; this view
-- keeps one row per region, but rank and percent_gain are COUNTRY-level
-- values repeated on every region row, so they only identify the country's
-- #1 rising term. Regions are compared by their own latest-week search_score
-- and its week-over-week change. Same limitations and pruning pattern as
-- Query 12: scores are relative to each region's own ~5-year peak (how
-- unusual local interest is, not search volume, so the data cannot say which
-- region contributes the most searches), NULL = below the reporting
-- threshold, and the latest week is still in progress.
-- -----------------------------------------------------------------------------
WITH top_breakout AS (
  SELECT snapshot_date, week, region_name, search_term, percent_gain, search_score
  FROM `trends_gdelt_analytics.vw_raw_trends_international_rising_history`
  WHERE snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 3 DAY)
    AND country_code = 'JP'
    AND rank = 1  -- the country's #1 rising term
  QUALIFY snapshot_date = MAX(snapshot_date) OVER ()
)
SELECT
  search_term,
  region_name,
  percent_gain AS national_percent_gain,  -- same value on every region row
  search_score AS region_score_latest_week,
  search_score - LAG(search_score) OVER (PARTITION BY search_term, region_name ORDER BY week) AS region_score_wow_change
FROM
  top_breakout
QUALIFY
  week = MAX(week) OVER ()
  AND search_score IS NOT NULL
ORDER BY
  region_score_latest_week DESC,
  region_score_wow_change DESC NULLS FIRST,  -- NULL = below threshold last week (newly emerged)
  region_name
LIMIT 20;

-- Query 14 (graph traversal via GRAPH_TABLE) lives in 07_graph_golden_query.sql:
-- it requires an Enterprise edition reservation, so it is preflighted separately.
