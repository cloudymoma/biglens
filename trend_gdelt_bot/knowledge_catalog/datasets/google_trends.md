---
type: BigQuery Dataset
title: Google Trends Public Dataset
description: Public Google Trends datasets hosting daily and hourly top 25 and rising search queries across 42 international countries and 210 US Nielsen DMAs.
resource: bigquery:bigquery-public-data.google_trends
tags:
  - open_data
  - search_trends
  - google
---

# Overview

Hosted in BigQuery as `bigquery-public-data.google_trends` (daily international + US DMA tables, ~30-day rolling `refresh_date` retention, ~5 years of weekly `week` history per snapshot) and `bigquery-public-data.google_trends_hourly` (intraday US DMA tables, ~30-day rolling `refresh_time` retention, ~53 weeks of weekly history per snapshot). Note that the `international_*` tables cover **42 countries excluding the United States**; US search trends live in `top_terms`, `top_rising_terms`, and `google_trends_hourly.*`.

# Tables
- [international_top_terms](/tables/international_top_terms) (`bigquery-public-data.google_trends.international_top_terms` — 42 countries, excluding US)
- [international_top_rising_terms](/tables/international_top_rising_terms) (`bigquery-public-data.google_trends.international_top_rising_terms` — 42 countries, excluding US)
- `bigquery-public-data.google_trends.top_terms` (US daily top 25 across 210 Nielsen DMAs)
- `bigquery-public-data.google_trends.top_rising_terms` (US daily rising terms across 210 Nielsen DMAs)
- `bigquery-public-data.google_trends_hourly.top_terms_hourly` (US intraday top 25 across 210 Nielsen DMAs)
- `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly` (US intraday rising terms across 210 Nielsen DMAs)

# Downstream Semantic Views
- Curated (Tier 1, international): [vw_search_trends_daily](/views/vw_search_trends_daily), [vw_search_trends_rising](/views/vw_search_trends_rising)
- Raw Drill-Down (Tier 2): [vw_raw_trends_international_history](/views/vw_raw_trends_international_history), [vw_raw_trends_international_rising_history](/views/vw_raw_trends_international_rising_history), [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma), [vw_raw_trends_us_dma_rising](/views/vw_raw_trends_us_dma_rising), [vw_raw_trends_us_hourly](/views/vw_raw_trends_us_hourly), [vw_raw_trends_us_hourly_rising](/views/vw_raw_trends_us_hourly_rising)
