---
type: BigQuery Table
title: GDELT 2.0 Events Partitioned
description: Real-time global news event database partitioned daily by ingestion date.
resource: bigquery:gdelt-bq.gdeltv2.events_partitioned
tags:
  - source_table
  - gdelt
---

# Schema & Partitioning
- `_PARTITIONDATE` (DATE) — **Partition key**: Ingestion date of the report.
- `GLOBALEVENTID` (INT64) — Unique global event ID.
- `SQLDATE` (INT64) — Date of the event occurrence (YYYYMMDD).
- `Actor1Name`, `Actor1CountryCode` (STRING) — First interacting actor (actor codes are CAMEO 3-letter, not ISO).
- `Actor2Name`, `Actor2CountryCode` (STRING) — Second interacting actor.
- `ActionGeo_CountryCode` (STRING) — **FIPS 10-4** country code of the action location — NOT ISO 3166. Decode via [dim_fips_iso_country](/tables/dim_fips_iso_country) before joining to Google Trends data.
- `EventCode` (STRING) — Full CAMEO event code.
- `EventRootCode` (STRING) — Top-level CAMEO category ('01'–'20').
- `QuadClass` (INT64) — 1: Verbal Cooperation, 2: Material Cooperation, 3: Verbal Conflict, 4: Material Conflict.
- `GoldsteinScale` (FLOAT64) — Geopolitical stability impact score (-10 to +10).
- `AvgTone` (FLOAT64) — Emotional sentiment tone (-100 to +100).
- `NumMentions` (INT64) — Total media mentions.
- `SOURCEURL` (STRING) — Web URL of citation article.

# Relationships
- Dataset: [gdelt](/datasets/gdelt)
- Powers: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- Governed by: [cameo_taxonomy](/glossary/cameo_taxonomy) and [media_pulse_vs_incident](/glossary/media_pulse_vs_incident)
