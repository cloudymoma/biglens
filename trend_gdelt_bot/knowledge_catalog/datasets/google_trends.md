---
type: BigQuery Dataset
title: Google Trends Public Dataset
description: Public Google Trends dataset hosting daily top 25 and rising search queries across ~50 countries and US metro areas.
resource: bigquery:bigquery-public-data.google_trends
tags:
  - open_data
  - search_trends
  - google
---

# Overview

Hosted in BigQuery as `bigquery-public-data.google_trends`. Contains anonymized, aggregated, and normalized search trend data refreshed daily.

# Tables
- [international_top_terms](/tables/international_top_terms)
- [international_top_rising_terms](/tables/international_top_rising_terms)

# Downstream Semantic Views
- [vw_search_trends_daily](/views/vw_search_trends_daily)
- [vw_search_trends_rising](/views/vw_search_trends_rising)
