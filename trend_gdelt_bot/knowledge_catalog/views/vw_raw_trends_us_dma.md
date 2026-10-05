---
type: BigQuery View
title: Raw US DMA Trends View (Tier 2)
description: US-only top search terms at Nielsen Designated Market Area (metro) granularity with weekly history — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_us_dma
tags:
  - tier2_raw_view
  - drill_down
  - trends
  - us_dma
---

# Definition

Tier 2 proxy view over `bigquery-public-data.google_trends.top_terms`, exposing the US metro-level (Nielsen DMA) `search_score` granularity that the international tables do not carry. Filter with a constant range (`snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)`) and pin `QUALIFY snapshot_date = MAX(snapshot_date) OVER ()`, plus `week = MAX(week) OVER ()` for current values. Because every term has a row in all ~210 DMAs (`search_score IS NULL` when below threshold) and `rank` is a national constant, measure metro breadth with `COUNTIF(search_score IS NOT NULL)` (never `COUNT(DISTINCT dma_name)`). Use for US DMA/metro breakdowns and for any US search-trend question — the Tier 1 Trends views exclude the US.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; filter with constant range + QUALIFY)
- `week` (DATE) — Trend week start (Sunday)
- `dma_name` (STRING) — Nielsen DMA name, e.g. 'New York NY'
- `dma_id` (INTEGER) — Numeric Nielsen DMA identifier
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — National daily US top-25 rank ([search_rank](/metrics/search_rank); repeated across all DMAs)
- `search_score` (INTEGER) — Weekly 0-100 interest per DMA ([search_score](/metrics/search_score); NULL when below threshold)

# Relationships
- Derived from: `bigquery-public-data.google_trends.top_terms` (US companion of [international_top_terms](/tables/international_top_terms))
- Curated counterpart (country level): [vw_search_trends_daily](/views/vw_search_trends_daily)
