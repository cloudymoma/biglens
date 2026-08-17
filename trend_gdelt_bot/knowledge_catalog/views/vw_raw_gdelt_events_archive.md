---
type: BigQuery View
title: Raw GDELT Events Archive View (Tier 2)
description: Full multi-year GDELT 2.0 event archive (Feb 2015 - present) with decoded CAMEO/QuadClass and ISO country codes — Tier 2 drill-down proxy view.
resource: bigquery:trends_gdelt_analytics.vw_raw_gdelt_events_archive
tags:
  - tier2_raw_view
  - drill_down
  - gdelt
  - archive
---

# Definition

Tier 2 proxy view over [events_partitioned](/tables/events_partitioned) covering the full archive since February 2015 (the curated [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily) keeps only 90 days). Applies the same semantics as the curated view — FIPS→ISO decode via [dim_fips_iso_country](/tables/dim_fips_iso_country), CAMEO root decode, QuadClass names — and adds archive-only columns: `global_event_id`, `event_date`, full/base CAMEO codes (300+ subcodes '010'–'204'), actor type codes, and `is_root_event`. `_PARTITIONDATE` is projected as `partition_date` so filters prune partitions through the view.

**Query rules:** a `partition_date` filter is MANDATORY (`>= DATE_SUB(...)` or an explicit `BETWEEN` range) — the archive spans a decade and terabytes. Use `is_root_event` plus per-URL `QUALIFY ROW_NUMBER()` to deduplicate one-story-many-events noise.

# Schema (key columns)
- `partition_date` (DATE) — Ingestion partition (ALWAYS filter)
- `global_event_id` (INTEGER), `event_date` (DATE)
- `country_code` (STRING, ISO mapped) / `fips_country_code` (STRING, raw) — ([country](/dimensions/country))
- `location_name`, `admin1_code`, `latitude`, `longitude`
- `primary_actor`, `actor1_country_code`, `actor1_type_code`, `secondary_actor`, `actor2_country_code`, `actor2_type_code` — ([actor](/dimensions/actor))
- `is_root_event` (BOOL)
- `cameo_event_code`, `cameo_base_code`, `cameo_root_code`, `event_category` — ([cameo_event_category](/dimensions/cameo_event_category))
- `quad_class_id`, `quad_class_name` — ([quadclass](/dimensions/quadclass))
- `goldstein_scale` ([goldstein_scale](/metrics/goldstein_scale)), `sentiment_tone` ([sentiment_tone](/metrics/sentiment_tone))
- `media_mentions_count` ([media_mentions_count](/metrics/media_mentions_count)), `distinct_sources_count`, `article_count`, `source_article_url`

# Relationships
- Derived from: [events_partitioned](/tables/events_partitioned) + [dim_fips_iso_country](/tables/dim_fips_iso_country)
- Curated counterpart (last 90 days): [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
