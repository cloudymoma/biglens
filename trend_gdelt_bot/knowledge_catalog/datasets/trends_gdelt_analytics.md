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

Tier 1 (curated, default):
- [vw_search_trends_daily](/views/vw_search_trends_daily)
- [vw_search_trends_rising](/views/vw_search_trends_rising)
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
- [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)

Tier 2 (raw drill-down proxy views — local objects whose SQL resolves to the external public tables at query time, so the agent can attach them without cross-organization data-source errors):
- [vw_raw_trends_international_history](/views/vw_raw_trends_international_history)
- [vw_raw_trends_international_rising_history](/views/vw_raw_trends_international_rising_history)
- [vw_raw_trends_us_dma](/views/vw_raw_trends_us_dma)
- [vw_raw_trends_us_dma_rising](/views/vw_raw_trends_us_dma_rising)
- [vw_raw_trends_us_hourly](/views/vw_raw_trends_us_hourly) — real-time (intraday snapshots)
- [vw_raw_trends_us_hourly_rising](/views/vw_raw_trends_us_hourly_rising) — real-time (intraday snapshots)
- [vw_raw_gdelt_events_archive](/views/vw_raw_gdelt_events_archive)
- [vw_raw_gdelt_gkg_entities_archive](/views/vw_raw_gdelt_gkg_entities_archive)

# Contained Tables
- [dim_fips_iso_country](/tables/dim_fips_iso_country) — FIPS-to-ISO country code mapping.
