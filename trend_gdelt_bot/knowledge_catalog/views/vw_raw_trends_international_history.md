---
type: BigQuery View
title: Raw International Trends History View (Tier 2)
description: Unaggregated ~5-year weekly search-interest history per term, country, and region — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_international_history
tags:
  - tier2_raw_view
  - drill_down
  - trends
---

# Definition

Tier 2 proxy view over [international_top_terms](/tables/international_top_terms): a renamed, unfiltered pass-through that exists so the agent can attach the public table's data without cross-organization data-source errors. Unlike [vw_search_trends_daily](/views/vw_search_trends_daily) (which pins the latest week and aggregates regions), this view keeps every weekly history row. Use ONLY on explicit request for multi-year trend trajectories or sub-national region detail.

**Query rules:** each `snapshot_date` partition repeats the full ~5-year weekly history, and old partitions expire on a rolling basis. For historical curves pin `snapshot_date = (SELECT MAX(snapshot_date) ...)` and scan `week`; NEVER range over `snapshot_date`. For current scores additionally pin `week = MAX(week)`. See [Partitioning & Query Optimization](/glossary/partitioning_best_practices).

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; always pin)
- `week` (DATE) — Trend week start; the time axis for curves
- `country_name` (STRING)
- `country_code` (STRING) — ISO 3166-1 alpha-2 ([country](/dimensions/country))
- `region_name` (STRING) — Sub-national region
- `region_code` (STRING) — ISO 3166-2 code
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — Daily top-25 rank ([search_rank](/metrics/search_rank))
- `search_score` (INTEGER) — Weekly 0-100 interest ([search_score](/metrics/search_score))

# Relationships
- Derived from: [international_top_terms](/tables/international_top_terms)
- Curated counterpart: [vw_search_trends_daily](/views/vw_search_trends_daily)
