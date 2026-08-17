---
type: BigQuery Table
title: GDELT 2.0 Global Knowledge Graph (GKG) Partitioned
description: Deep content analysis of global news media extracting themes, persons, organizations, locations, and multi-dimensional sentiment tones.
resource: bigquery:gdelt-bq.gdeltv2.gkg_partitioned
tags:
  - source_table
  - gdelt
  - themes
---

# Schema & Partitioning
- `_PARTITIONDATE` (DATE) — **Partition key**: Ingestion date.
- `GKGRECORDID` (STRING) — Unique record ID.
- `V2Themes` (STRING) — Semicolon-delimited list of GKG themes.
- `V2Persons` (STRING) — Semicolon-delimited list of people recognized.
- `V2Organizations` (STRING) — Semicolon-delimited list of organizations.
- `V2Tone` (STRING) — Comma-delimited sentiment vector (Tone, Positive Score, Negative Score, Polarity).
- `SourceCommonName` (STRING) — Domain name of news outlet.
- `DocumentIdentifier` (STRING) — Canonical article URL.

# Relationships
- Dataset: [gdelt](/datasets/gdelt)
- Powers: [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
