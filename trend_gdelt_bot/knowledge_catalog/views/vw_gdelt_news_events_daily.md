---
type: BigQuery View
title: Daily GDELT News Events View
description: Cleaned global news events with decoded CAMEO categories, QuadClasses, sentiment tone, and stability scores.
resource: bigquery:trends_gdelt_analytics.vw_gdelt_news_events_daily
tags:
  - curated_view
  - gdelt
  - news_events
---

# Definition

Decodes raw numeric and alphanumeric CAMEO codes from [events_partitioned](/tables/events_partitioned) into human-readable text categories, and decodes the FIPS 10-4 action-location country code to ISO 3166 via [dim_fips_iso_country](/tables/dim_fips_iso_country).

# Schema
- `report_date` (DATE) — Reporting (ingestion) date.
- `country_code` (STRING) — ISO 3166-1 alpha-2 code of the action location, mapped from FIPS. NULL when the country is not covered by Google Trends (and therefore not in the mapping).
- `fips_country_code` (STRING) — Raw FIPS 10-4 code from `ActionGeo_CountryCode` (full global coverage).
- `location_name` (STRING) — Full geographic location description.
- `primary_actor` (STRING) — First actor.
- `secondary_actor` (STRING) — Second actor.
- `cameo_root_code` (STRING) — Root code ('01'–'20').
- `event_category` (STRING) — Decoded CAMEO name (e.g. 'Protest', 'Fight', 'Make Public Statement').
- `quad_class_id` (INT64) & `quad_class_name` (STRING) — [quadclass](/dimensions/quadclass).
- `goldstein_scale` (FLOAT64) — [goldstein_scale](/metrics/goldstein_scale).
- `sentiment_tone` (FLOAT64) — [sentiment_tone](/metrics/sentiment_tone).
- `media_mentions_count` (INT64) — [media_mentions_count](/metrics/media_mentions_count).
- `source_article_url` (STRING) — Canonical source citation.

# Relationships
- Derived from: [events_partitioned](/tables/events_partitioned) and [dim_fips_iso_country](/tables/dim_fips_iso_country)
- Feeds: [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
