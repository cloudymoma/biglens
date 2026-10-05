---
type: BigQuery View
title: Unified Topic & News Trends Mart
description: Primary analytical mart joining daily Google search trends with GDELT geopolitical news context, tone, and conflict share by date and country (international Trends only — excludes the US).
resource: bigquery:trends_gdelt_analytics.vw_topic_news_trends_unified
tags:
  - core_mart
  - agent_primary
  - unified
---

# Definition

Joins [vw_search_trends_daily](/views/vw_search_trends_daily) with aggregated country news summaries from [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily) on ISO country code and date (GDELT's FIPS codes are already decoded to ISO in the events view via [dim_fips_iso_country](/tables/dim_fips_iso_country)). This is the default analytical view for the conversational AI agent.

**Grain & aggregation caveat:** `search_score` is the latest trend week's regional-average score (where the week starts on Sunday), whereas the `country_*` news columns aggregate GDELT events ingested on `date` only and **repeat across all 25 `search_term` rows** for each `(date, country_code)`. When rolling up country-level news metrics, always use `ANY_VALUE` or `MAX` per `(date, country_code)` — never `SUM` across terms.

**Coverage:** the search side comes from Google Trends' international tables, and international views exclude the US — `country_code = 'US'` returns no rows (use [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily) for US news events, and [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma) for US search questions).

# Schema
- `date` (DATE) — Snapshot calendar date.
- `country_name` (STRING) & `country_code` (STRING) — [country](/dimensions/country).
- `search_term` (STRING) — [search_term](/dimensions/search_term).
- `search_rank` (INT64) — [search_rank](/metrics/search_rank).
- `search_score` (INT64) — Equal-weighted mean of sub-national region scores for the latest trend week ([search_score](/metrics/search_score)).
- `is_historical_peak` (BOOLEAN) — True if the term reached `score = 100` in at least one sub-national region in the latest trend week (does not imply `search_score = 100`).
- `country_daily_news_events` (INT64) — Total GDELT events whose action location is in the country on `date` (repeated on each term row).
- `country_daily_media_mentions` (INT64) — First-15-minute-window [media_mentions_count](/metrics/media_mentions_count) summed over event rows on `date` (repeated on each term row).
- `country_avg_tone` (FLOAT64) — Event-weighted mean [sentiment_tone](/metrics/sentiment_tone) over events whose action location is in the country on `date`.
- `country_avg_goldstein` (FLOAT64) — Event-weighted mean [goldstein_scale](/metrics/goldstein_scale) over events whose action location is in the country on `date`.
- `conflict_event_share_pct` (FLOAT64) — [conflict_event_share](/metrics/conflict_event_share).
- `dominant_news_category` (STRING) — Most frequent non-null [cameo_event_category](/dimensions/cameo_event_category).
- `dominant_actor` (STRING) — Most frequent non-null primary [actor](/dimensions/actor).

# Relationships
- Joins: [vw_search_trends_daily](/views/vw_search_trends_daily) and [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- Governed by: [rank_vs_score_divergence](/glossary/rank_vs_score_divergence)
