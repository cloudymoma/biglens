---
type: BigQuery View
title: Raw US DMA Rising Terms View (Tier 2)
description: US rising/breakout terms at Nielsen DMA (metro) granularity with per-DMA percent_gain — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_us_dma_rising
tags:
  - tier2_raw_view
  - drill_down
  - trends
  - rising
  - us_dma
---

# Definition

Tier 2 proxy view over `bigquery-public-data.google_trends.top_rising_terms`: the US metro-level companion of [vw_raw_trends_international_rising_history](/views/vw_raw_trends_international_rising_history), showing which terms are breaking out in which Nielsen DMA and how hard (`percent_gain`). Same pinning rules as [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma): pin `snapshot_date = MAX(snapshot_date)`; add `week = MAX(week)` for current values; use `COUNT(DISTINCT dma_name)` for national rollups.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; always pin)
- `week` (DATE) — Trend week start
- `dma_name` (STRING), `dma_id` (INTEGER) — Nielsen DMA
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — Rank among rising queries in the DMA (1 = fastest riser)
- `search_score` (INTEGER) — Weekly 0-100 interest ([search_score](/metrics/search_score))
- `percent_gain` (INTEGER) — Week-over-week gain per DMA ([percent_gain](/metrics/percent_gain))

# Relationships
- Derived from: `bigquery-public-data.google_trends.top_rising_terms`
- Curated counterpart (country level, international): [vw_search_trends_rising](/views/vw_search_trends_rising)
