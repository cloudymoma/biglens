---
type: BigQuery Dataset
title: Trends & GDELT Curated Analytics Dataset
description: Curated analytics dataset hosting cleaned semantic views and analytical marts for conversational AI agents.
resource: bigquery:trends_gdelt_analytics
tags:
  - semantic_layer
  - curated
  - agent_ready
---

# Overview

Serves as the primary semantic access layer for the BigQuery conversational agent. Encapsulates business logic, metric normalization formulas, category decoding, and partitioned filters.

# Contained Views
- [vw_search_trends_daily](/views/vw_search_trends_daily)
- [vw_search_trends_rising](/views/vw_search_trends_rising)
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
- [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)

# Contained Tables
- [dim_fips_iso_country](/tables/dim_fips_iso_country) — FIPS-to-ISO country code mapping.
