---
type: BigQuery View
title: Rising Search Trends View
description: Standardized view of breakout and surging search queries with growth percentages (international only — excludes the US).
resource: bigquery:trends_gdelt_analytics.vw_search_trends_rising
tags:
  - curated_view
  - rising_trends
---

# Definition

Aggregates [international_top_rising_terms](/tables/international_top_rising_terms) exposing max and average percentage gains, pinned to the latest trend week per snapshot.

**Coverage:** international views exclude the US — `country_code = 'US'` returns no rows. Use the `vw_raw_trends_us_*` views (e.g. [vw_raw_trends_us_dma_rising](/views/vw_raw_trends_us_dma_rising), or [vw_raw_trends_us_hourly_rising](/views/vw_raw_trends_us_hourly_rising) for right-now questions) for US questions.

# Schema
- `snapshot_date` (DATE)
- `country_name` (STRING)
- `country_code` (STRING)
- `search_term` (STRING)
- `rank` (INT64)
- `search_score` (INT64)
- `max_percent_gain` (FLOAT64) — [percent_gain](/metrics/percent_gain)
- `avg_percent_gain` (FLOAT64)

# Relationships
- Derived from: [international_top_rising_terms](/tables/international_top_rising_terms)
