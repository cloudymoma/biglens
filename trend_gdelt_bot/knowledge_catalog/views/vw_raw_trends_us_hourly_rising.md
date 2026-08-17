---
type: BigQuery View
title: Raw US Hourly Rising Terms View (Tier 2 / Real-Time)
description: Intraday US rising/breakout terms per Nielsen DMA with percent_gain — the freshest breakout signal in the stack.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_us_hourly_rising
tags:
  - tier2_raw_view
  - real_time
  - trends
  - rising
  - us_dma
---

# Definition

Tier 2 proxy view over `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly`: the rising/breakout companion of [vw_raw_trends_us_hourly](/views/vw_raw_trends_us_hourly), adding per-DMA `percent_gain`. Use PROACTIVELY for US "spiking / breaking out right now" questions. Same rules: pin `snapshot_time = MAX(snapshot_time)` (DATETIME) and `week = MAX(week)` for current values; ~30-day snapshot retention. US only.

# Schema
- `snapshot_time` (DATETIME) — Intraday snapshot timestamp (partition key; always pin)
- `week` (DATE) — Trend week start
- `dma_name` (STRING), `dma_id` (INTEGER) — Nielsen DMA
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — Rank among rising queries in the DMA (1 = fastest riser)
- `search_score` (INTEGER) — Weekly 0-100 interest ([search_score](/metrics/search_score))
- `percent_gain` (INTEGER) — Week-over-week gain per DMA ([percent_gain](/metrics/percent_gain))

# Relationships
- Derived from: `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly`
- Daily counterpart: [vw_raw_trends_us_dma_rising](/views/vw_raw_trends_us_dma_rising)
