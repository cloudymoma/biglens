---
type: BigQuery View
title: Raw International Rising Terms History View (Tier 2)
description: Region-level weekly search_score history for rising/breakout terms (with national rank and percent_gain) — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_trends_international_rising_history
tags:
  - tier2_raw_view
  - drill_down
  - trends
  - rising
---

# Definition

Tier 2 proxy view over [international_top_rising_terms](/tables/international_top_rising_terms) keeping the sub-national region `search_score` history behind each rising term. Note that `rank` and `percent_gain` are **national-level constants** repeated on every region row — compare regions by their own `search_score`, never by `percent_gain`. Filter with a constant range (`snapshot_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)`) and pin `QUALIFY snapshot_date = MAX(snapshot_date) OVER ()`; add `week = MAX(week) OVER ()` for current values.

# Schema
- `snapshot_date` (DATE) — Trends refresh date (partition key; filter with constant range + QUALIFY)
- `week` (DATE) — Trend week start (Sunday)
- `country_name`, `country_code` (STRING) — ISO 3166-1 ([country](/dimensions/country))
- `region_name`, `region_code` (STRING) — Sub-national region (ISO 3166-2)
- `search_term` (STRING) — ([search_term](/dimensions/search_term))
- `rank` (INTEGER) — National rank among rising queries (repeated across all regions)
- `search_score` (INTEGER) — Weekly 0-100 interest in this region ([search_score](/metrics/search_score); NULL when below threshold)
- `percent_gain` (INTEGER) — National percentage gain ([percent_gain](/metrics/percent_gain); repeated identically across all regions)

# Relationships
- Derived from: [international_top_rising_terms](/tables/international_top_rising_terms)
- Curated counterpart: [vw_search_trends_rising](/views/vw_search_trends_rising)
