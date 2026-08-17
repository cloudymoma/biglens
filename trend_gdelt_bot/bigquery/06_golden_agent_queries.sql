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

-- Query 6 (graph traversal via GRAPH_TABLE) lives in 07_graph_golden_query.sql:
-- it requires an Enterprise edition reservation, so it is preflighted separately.
