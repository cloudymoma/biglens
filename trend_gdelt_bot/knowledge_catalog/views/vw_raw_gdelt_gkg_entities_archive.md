---
type: BigQuery View
title: Raw GDELT GKG Entities Archive View (Tier 2)
description: Per-article GKG persons, organizations, and themes as clean arrays plus full tone vector, rolling 2-year window — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive
tags:
  - tier2_raw_view
  - drill_down
  - gdelt
  - entities
---

# Definition

Tier 2 proxy view over [gkg_partitioned](/tables/gkg_partitioned) exposing the entity-level detail the curated [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily) drops: every mentioned person, organization, and theme per article. The packed `"Name,charOffset;Name,charOffset"` V2 strings are pre-parsed into deduplicated `ARRAY<STRING>` columns (offsets stripped), so queries filter with `'Name' IN UNNEST(persons)` — no manual SPLIT needed.

**Hard cost bound:** GKG is tens of terabytes, so a rolling **2-year window** (`_PARTITIONDATE >= CURRENT_DATE() - 730 days`) is baked into the view as a blast-radius limit for unfiltered queries. Queries should still ALWAYS filter `partition_date`.

# Schema
- `partition_date` (DATE) — Ingestion partition (ALWAYS filter; view hard-bounded to 730 days)
- `gkg_record_id` (STRING)
- `media_source` (STRING) — Outlet domain
- `document_url` (STRING)
- `persons` (ARRAY<STRING>) — Deduplicated person names, offsets stripped
- `organizations` (ARRAY<STRING>) — Deduplicated organization names
- `themes` (ARRAY<STRING>) — Deduplicated GKG theme codes ([gkg_theme](/dimensions/gkg_theme))
- `sentiment_tone`, `positive_score`, `negative_score`, `polarity_score` (FLOAT64) — ([sentiment_tone](/metrics/sentiment_tone))

# Relationships
- Derived from: [gkg_partitioned](/tables/gkg_partitioned)
- Curated counterpart (last 30 days, first theme only): [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
