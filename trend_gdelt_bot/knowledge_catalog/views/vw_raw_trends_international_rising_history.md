---
type: BigQuery View
title: Raw International Rising Terms History View (Tier 2)
description: Region-level rising/breakout terms with per-region percent_gain and weekly score history — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_international_rising_history
tags:
  - tier2_raw_view
  - drill_down
  - trends
  - rising
---

# Definition

Tier 2 proxy view over [international_top_rising_terms](/tables/international_top_rising_terms) keeping the sub-national region detail the curated [vw_search_trends_rising](/views/vw_search_trends_rising) aggregates away: per-region `percent_gain` plus each rising term's weekly score history. Same snapshot layout and pinning rules as [vw_raw_trends_international_history](/views/vw_raw_trends_international_history): pin `snapshot_date = MAX(snapshot_date)`; add `week = MAX(week)` for current values. Use ONLY on explicit request for region-level rising detail or rising-term history.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; always pin)
- `week` (DATE) — Trend week start
- `country_name`, `country_code` (STRING) — ISO 3166-1 ([country](/dimensions/country))
- `region_name`, `region_code` (STRING) — Sub-national region (ISO 3166-2)
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — Rank among rising queries in the region
- `search_score` (INTEGER) — Weekly 0-100 interest ([search_score](/metrics/search_score))
- `percent_gain` (INTEGER) — Week-over-week gain ([percent_gain](/metrics/percent_gain))

# Relationships
- Derived from: [international_top_rising_terms](/tables/international_top_rising_terms)
- Curated counterpart: [vw_search_trends_rising](/views/vw_search_trends_rising)
