---
type: BigQuery Table
title: International Top Terms
description: Raw Google Trends daily snapshot of the top 25 search queries across ~50 countries and sub-national regions.
resource: bigquery:bigquery-public-data.google_trends.international_top_terms
tags:
  - source_table
  - google_trends
---

# Schema & Partitioning
- `refresh_date` (DATE) — **Partition key**: Date the snapshot was published.
- `country_name` (STRING) — Full country name.
- `country_code` (STRING) — 2-letter ISO country code.
- `region_name` (STRING) — Sub-national region or metro area name.
- `region_code` (STRING) — Sub-national region code.
- `week` (DATE) — Sunday start date of the weekly trend analysis.
- `term` (STRING) — Search term text.
- `rank` (INT64) — Position in top 25 (1–25).
- `score` (INT64) — Relative search interest index (0–100) for that week.

# Weekly History Semantics (Important)

Each `refresh_date` partition contains the **full 5-year weekly score history** (~261 `week` rows per term/region) for that day's top-25 terms — not just current values. Queries about *current* interest must pin `week = MAX(week)` within the partition; otherwise scores average five years of history and every term trivially has a score-100 week (the peak that defines its normalization). The curated views already apply this pin.

# Relationships
- Dataset: [google_trends](/datasets/google_trends)
- Powers: [vw_search_trends_daily](/views/vw_search_trends_daily)
- Governed by: [search_share_normalization](/glossary/search_share_normalization) and [partitioning_best_practices](/glossary/partitioning_best_practices)
