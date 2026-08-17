---
type: BigQuery View
title: Daily Search Trends Semantic View
description: Cleaned and standardized daily Google Trends top 25 search queries with country-aggregated scores and peak surge flags.
resource: bigquery:trends_gdelt_analytics.vw_search_trends_daily
tags:
  - curated_view
  - search_trends
---

# Definition

Aggregates [international_top_terms](/tables/international_top_terms) at the country and date level, **pinned to the latest trend week per snapshot** (each raw partition carries 5 years of weekly history). Standardizes scores across sub-national regions and tags terms currently at peak popularity.

# Schema
- `snapshot_date` (DATE) — Date of snapshot.
- `country_name` (STRING) — Full country name.
- `country_code` (STRING) — 2-letter ISO country code.
- `search_term` (STRING) — Query text.
- `rank` (INT64) — Top volume rank ([search_rank](/metrics/search_rank)).
- `search_score` (INT64) — Relative search interest for the latest trend week ([search_score](/metrics/search_score)).
- `is_historical_peak` (BOOLEAN) — True when the current `search_score = 100` in at least one region (the term is at its all-time peak right now).
- `active_regions_count` (INT64) — Count of sub-national regions reporting this query.

# Relationships
- Derived from: [international_top_terms](/tables/international_top_terms)
- Feeds: [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
