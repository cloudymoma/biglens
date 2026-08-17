---
type: BigQuery Table
title: International Top Rising Terms
description: Raw Google Trends daily snapshot of rising / breakout queries exhibiting significant growth.
resource: bigquery:bigquery-public-data.google_trends.international_top_rising_terms
tags:
  - source_table
  - google_trends
---

# Schema & Partitioning
- `refresh_date` (DATE) — **Partition key**: Date snapshot published.
- `country_name` (STRING) — Country name.
- `country_code` (STRING) — ISO country code.
- `term` (STRING) — Search term text.
- `rank` (INT64) — Rising rank position.
- `score` (INT64) — Relative search interest index (0–100).
- `percent_gain` (FLOAT64) — Percentage increase in search volume compared to previous period.
- `week` (DATE) — Sunday start date of the weekly analysis. Like [international_top_terms](/tables/international_top_terms), each `refresh_date` partition carries weekly history — pin `week = MAX(week)` for current values (the curated views already do).

# Relationships
- Dataset: [google_trends](/datasets/google_trends)
- Powers: [vw_search_trends_rising](/views/vw_search_trends_rising)
- Associated Metric: [percent_gain](/metrics/percent_gain)
