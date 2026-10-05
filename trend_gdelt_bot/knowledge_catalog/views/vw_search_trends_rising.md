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

Aggregates [international_top_rising_terms](/tables/international_top_rising_terms) exposing the national percentage gain, pinned to the latest trend week per snapshot (~30-day rolling snapshot retention).

**Coverage:** international views exclude the US — `country_code = 'US'` returns no rows. Use the `vw_raw_trends_us_*` views (e.g. [vw_raw_trends_us_dma_rising](/views/vw_raw_trends_us_dma_rising), or [vw_raw_trends_us_hourly_rising](/views/vw_raw_trends_us_hourly_rising) for right-now questions) for US questions.

# Schema
- `snapshot_date` (DATE)
- `country_name` (STRING)
- `country_code` (STRING)
- `search_term` (STRING)
- `rank` (INT64) — National rising rank (1 = fastest riser)
- `search_score` (INT64) — Equal-weighted mean of sub-national region scores across regions above threshold (NULL when all regions are below threshold)
- `max_percent_gain` (INT64) — National percentage gain ([percent_gain](/metrics/percent_gain); identical across all regions in the country)
- `avg_percent_gain` (FLOAT64) — National percentage gain (identical to `max_percent_gain`; retained for schema compatibility)

# Relationships
- Derived from: [international_top_rising_terms](/tables/international_top_rising_terms)
