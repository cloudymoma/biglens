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

Tier 2 proxy view over `bigquery-public-data.google_trends.top_terms`, exposing the US metro-level (Nielsen DMA) granularity that the international tables do not carry. Same weekly-history-per-snapshot layout as [vw_raw_trends_international_history](/views/vw_raw_trends_international_history): pin `snapshot_date = MAX(snapshot_date)`, and pin `week = MAX(week)` for current values. Deduplicate the repeated history rows with `COUNT(DISTINCT dma_name)` when counting metros. Use ONLY on explicit request for US DMA/metro breakdowns.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; always pin)
- `week` (DATE) — Trend week start
- `dma_name` (STRING) — Nielsen DMA name, e.g. 'New York NY'
- `dma_id` (INTEGER) — Numeric Nielsen DMA identifier
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — Daily US top-25 rank ([search_rank](/metrics/search_rank))
- `search_score` (INTEGER) — Weekly 0-100 interest per DMA ([search_score](/metrics/search_score))

# Relationships
- Derived from: `bigquery-public-data.google_trends.top_terms` (US companion of [international_top_terms](/tables/international_top_terms))
- Curated counterpart (country level): [vw_search_trends_daily](/views/vw_search_trends_daily)
