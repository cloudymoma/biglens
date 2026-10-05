---
type: BigQuery View
title: Raw US Hourly Trends View (Tier 2 / Real-Time)
description: Intraday US top 25 search terms per Nielsen DMA, several snapshots per day — the freshest trends source in the stack.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_us_hourly
tags:
  - tier2_raw_view
  - real_time
  - trends
  - us_dma
---

# Definition

Tier 2 proxy view over `bigquery-public-data.google_trends_hourly.top_terms_hourly` (HOUR-partitioned on a DATETIME `refresh_time`, exposed as `snapshot_time`). This is the **freshest** trends source: several intraday snapshots per day versus the 1–2 day lag of the daily tables, so US "right now / today" questions route here PROACTIVELY (the one exception to Tier 2's explicit-request-only rule). Verified live 2026-08-17: ~30-day snapshot retention; each snapshot = 25 national terms × 210 DMAs × ~53 weeks (a ~1-YEAR weekly history, shorter than the daily tables' ~5 years).

**Query rules:** filter with a constant 2-day DATETIME range (`WHERE snapshot_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 2 DAY)`) so BigQuery prunes hourly partitions statically, then use `QUALIFY snapshot_time = MAX(snapshot_time) OVER () AND week = MAX(week) OVER ()` for current values; aggregate across DMAs (`MIN(rank)`, `COUNTIF(search_score IS NOT NULL)`) for the national picture. US only.

# Schema
- `snapshot_time` (DATETIME) — Intraday snapshot timestamp (partition key; filter with a constant 2-day DATETIME range + `QUALIFY`)
- `week` (DATE) — Trend week start (~53 weeks per snapshot)
- `dma_name` (STRING), `dma_id` (INTEGER) — Nielsen DMA
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — National US top-25 rank as of `snapshot_time`, repeated across DMAs ([search_rank](/metrics/search_rank); compare DMAs using `search_score`)
- `search_score` (INTEGER) — Weekly 0-100 interest per DMA ([search_score](/metrics/search_score); NULL when below threshold)

# Relationships
- Derived from: `bigquery-public-data.google_trends_hourly.top_terms_hourly`
- Daily counterparts: [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma) (US), [vw_search_trends_daily](/views/vw_search_trends_daily) (international, curated)
