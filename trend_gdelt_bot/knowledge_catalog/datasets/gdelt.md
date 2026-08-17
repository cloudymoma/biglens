---
type: BigQuery Dataset
title: GDELT 2.0 Global Database of Events, Language, and Tone
description: Real-time global database of news events, sentiment tone, geopolitical actors, and themes monitoring broadcast, print, and web news globally in 100+ languages.
resource: bigquery:gdelt-bq.gdeltv2
tags:
  - open_data
  - news
  - geopolitical
  - sentiment
---

# Overview

The GDELT Project machine-reads world news every 15 minutes, mapping world events into CAMEO (Conflict and Mediation Event Observations) codes and tracking emotional sentiment.

# Tables
- [events_partitioned](/tables/events_partitioned)
- [gkg_partitioned](/tables/gkg_partitioned)

# Downstream Semantic Views
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
