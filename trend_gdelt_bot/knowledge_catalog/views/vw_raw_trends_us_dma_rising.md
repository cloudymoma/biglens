---
type: BigQuery View
title: Raw US DMA Rising Terms View (Tier 2)
description: US rising/breakout terms at Nielsen DMA (metro) granularity with national rank/percent_gain and per-DMA search_score — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_us_dma_rising
tags:
  - tier2_raw_view
  - drill_down
  - trends
  - rising
  - us_dma
---

# Definition

Tier 2 proxy view over `bigquery-public-data.google_trends.top_rising_terms`: the US metro-level companion of [vw_raw_trends_international_rising_history](/views/vw_raw_trends_international_rising_history), showing which US rising terms have measurable search interest in which Nielsen DMA (`search_score`) alongside the national breakout rank (`rank`) and growth rate (`percent_gain`). Same pinning rules as [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma): filter with a constant range `WHERE snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)` and `QUALIFY snapshot_date = MAX(snapshot_date) OVER () AND week = MAX(week) OVER ()` for current values; use `COUNTIF(search_score IS NOT NULL)` for national active-DMA breadth.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; filter with a constant range + `QUALIFY`)
- `week` (DATE) — Trend week start
- `dma_name` (STRING), `dma_id` (INTEGER) — Nielsen DMA
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — National US rising rank (1–25) as of `snapshot_date`, repeated identically across all DMAs and historical weeks (compare DMAs using `search_score`)
- `search_score` (INTEGER) — Weekly 0-100 interest in this DMA ([search_score](/metrics/search_score); NULL when below threshold)
- `percent_gain` (INTEGER) — National US breakout percentage gain as of `snapshot_date`, repeated identically across all DMAs and historical weeks ([percent_gain](/metrics/percent_gain))

# Relationships
- Derived from: `bigquery-public-data.google_trends.top_rising_terms`
- Curated counterpart (country level, international): [vw_search_trends_rising](/views/vw_search_trends_rising)
