---
type: Metric
title: Search Percent Gain (%)
description: National percentage growth reported by Google Trends for surging / breakout queries relative to the baseline period.
tags:
  - metric
  - search_trends
  - growth
---

# Definition

`percent_gain` is the national percentage increase in search interest reported directly by the Google Trends rising-terms tables (`international_top_rising_terms`, `top_rising_terms`, `top_rising_terms_hourly`) relative to Google Trends' preceding baseline period.

In sub-national tables (regions or US Nielsen DMAs), `percent_gain` is a country-level value repeated across every sub-region row for the same `(refresh_date, term)` — sub-regions are differentiated by their own `score` (0–100), not by `percent_gain`. Breakout terms emerging from a near-zero baseline routinely exceed `+1,000%`.

# Related Concepts
- Used in: [vw_search_trends_rising](/views/vw_search_trends_rising)

