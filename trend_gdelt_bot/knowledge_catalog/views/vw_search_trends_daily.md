---
type: BigQuery View
title: Daily Search Trends Semantic View
description: Cleaned and standardized daily Google Trends top 25 search queries with country-aggregated scores and peak surge flags (international only — excludes the US).
resource: bigquery:trends_gdelt_analytics.vw_search_trends_daily
tags:
  - curated_view
  - search_trends
---

# Definition

Aggregates [international_top_terms](/tables/international_top_terms) at the country and date level (~30-day rolling snapshot retention), **pinned to the latest trend week per snapshot** (each raw partition carries 5 years of weekly history). Averages sub-national region scores across regions with a reportable score (`score IS NOT NULL`) and flags terms reaching score 100 in at least one region.

**Coverage:** international views exclude the US — `country_code = 'US'` returns no rows. Use the `vw_raw_trends_us_*` views (e.g. [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma)) for US questions.

# Schema
- `snapshot_date` (DATE) — Date of snapshot (~30-day rolling retention).
- `country_name` (STRING) — Full country name.
- `country_code` (STRING) — 2-letter ISO country code.
- `search_term` (STRING) — Query text.
- `rank` (INT64) — Top volume rank ([search_rank](/metrics/search_rank)).
- `search_score` (INT64) — Equal-weighted mean of sub-national region scores (0–100, each normalized to that region's own ~5-year peak) across regions above the reporting threshold; NULL when all regions are below threshold ([search_score](/metrics/search_score)).
- `is_historical_peak` (BOOLEAN) — True when `score = 100` in at least one sub-national region for the latest trend week (does not imply the regional-average `search_score` equals 100).
- `active_regions_count` (INT64) — Count of sub-national regions with a non-null `score` (above Google Trends privacy threshold) in the latest trend week.

# Relationships
- Derived from: [international_top_terms](/tables/international_top_terms)
- Feeds: [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
