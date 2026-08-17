---
type: BigQuery View
title: Daily GDELT Themes View
description: Parsed GDELT Global Knowledge Graph themes and parsed tone vectors.
resource: bigquery:trends_gdelt_analytics.vw_gdelt_gkg_themes_daily
tags:
  - curated_view
  - themes
---

# Definition

Extracts primary themes and splits comma-separated tone vectors from [gkg_partitioned](/tables/gkg_partitioned). Raw `V2Themes` entries are `THEME_NAME,charOffset`; the view strips the character offset so `primary_theme` is a clean theme name. Retains a 30-day window (vs 90 days elsewhere) because GKG is by far the largest source table.

# Schema
- `report_date` (DATE)
- `primary_theme` (STRING) — First listed theme, offset stripped ([gkg_theme](/dimensions/gkg_theme))
- `media_source` (STRING) — News outlet domain
- `sentiment_tone` (FLOAT64)
- `polarity_score` (FLOAT64)

# Relationships
- Derived from: [gkg_partitioned](/tables/gkg_partitioned)
