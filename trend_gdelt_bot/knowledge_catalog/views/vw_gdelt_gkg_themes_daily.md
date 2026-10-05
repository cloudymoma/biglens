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

Extracts substantive themes and splits comma-separated tone vectors from [gkg_partitioned](/tables/gkg_partitioned). Raw `V2Themes` entries are `THEME_NAME,charOffset`; the view strips character offsets, exposes the full deduplicated `themes` array (`ARRAY<STRING>`), and selects the first substantive theme (skipping generic `TAX_`, `WB_`, `EPU_`, `CRISISLEX_`, `UNGP_` prefixes when a specific theme exists) as `primary_theme`. Retains a 30-day window because GKG is the largest source table.

# Schema
- `report_date` (DATE)
- `primary_theme` (STRING) — First substantive theme with offset stripped ([gkg_theme](/dimensions/gkg_theme))
- `themes` (ARRAY<STRING>) — All distinct GKG themes on the article with offsets stripped (`UNNEST(themes)` for complete thematic counts)
- `media_source` (STRING) — News outlet domain
- `sentiment_tone` (FLOAT64)
- `polarity_score` (FLOAT64)

# Relationships
- Derived from: [gkg_partitioned](/tables/gkg_partitioned)
