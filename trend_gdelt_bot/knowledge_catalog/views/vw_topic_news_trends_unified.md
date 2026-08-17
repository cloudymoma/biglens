---
type: BigQuery View
title: Unified Topic & News Trends Mart
description: Primary analytical mart joining daily Google search trends with GDELT geopolitical news context, tone, and conflict share by date and country.
resource: bigquery:trends_gdelt_analytics.vw_topic_news_trends_unified
tags:
  - core_mart
  - agent_primary
  - unified
---

# Definition

Joins [vw_search_trends_daily](/views/vw_search_trends_daily) with aggregated country news summaries from [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily) on ISO country code and date (GDELT's FIPS codes are already decoded to ISO in the events view via [dim_fips_iso_country](/tables/dim_fips_iso_country)). This is the default analytical view for the conversational AI agent.

# Schema
- `date` (DATE) — Snapshot calendar date.
- `country_name` (STRING) & `country_code` (STRING) — [country](/dimensions/country).
- `search_term` (STRING) — [search_term](/dimensions/search_term).
- `search_rank` (INT64) — [search_rank](/metrics/search_rank).
- `search_score` (INT64) — [search_score](/metrics/search_score).
- `is_historical_peak` (BOOLEAN) — True if `search_score = 100`.
- `country_daily_news_events` (INT64) — Total GDELT events in country on date.
- `country_daily_media_mentions` (INT64) — [media_mentions_count](/metrics/media_mentions_count) summed over event rows (media pulse, not a distinct-article count).
- `country_avg_tone` (FLOAT64) — [sentiment_tone](/metrics/sentiment_tone).
- `country_avg_goldstein` (FLOAT64) — [goldstein_scale](/metrics/goldstein_scale).
- `conflict_event_share_pct` (FLOAT64) — [conflict_event_share](/metrics/conflict_event_share).
- `dominant_news_category` (STRING) — [cameo_event_category](/dimensions/cameo_event_category).
- `dominant_actor` (STRING) — [actor](/dimensions/actor).

# Relationships
- Joins: [vw_search_trends_daily](/views/vw_search_trends_daily) and [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- Governed by: [rank_vs_score_divergence](/glossary/rank_vs_score_divergence)
