---
type: BigQuery Table
title: FIPS to ISO Country Code Mapping
description: Static mapping between FIPS 10-4 country codes (used by GDELT ActionGeo) and ISO 3166-1 alpha-2 codes (used by Google Trends), covering all Google Trends countries.
resource: bigquery:trends_gdelt_analytics.dim_fips_iso_country
tags:
  - dimension_table
  - geography
---

# Why This Table Exists

GDELT's `ActionGeo_CountryCode` uses **FIPS 10-4** codes while Google Trends' `country_code` uses **ISO 3166-1 alpha-2** codes. The two systems reuse the same letters with different meanings, so raw codes must **never be joined directly**:

| Code | FIPS meaning | ISO meaning |
|------|--------------|-------------|
| `GB` | Gabon | United Kingdom (FIPS `UK`) |
| `CH` | China | Switzerland (FIPS `SZ`) |
| `NI` | Nigeria | Nicaragua |
| `IS` | Israel | Iceland |

# Schema
- `fips_code` (STRING) — FIPS 10-4 code as found in GDELT `ActionGeo_CountryCode`.
- `iso_code` (STRING) — ISO 3166-1 alpha-2 code as found in Google Trends `country_code`.
- `country_name` (STRING) — Common English country name.

# Coverage

Covers the 42 countries present in Google Trends `international_top_terms` (verified against live data 2026-08-16). GDELT events in other countries carry a NULL ISO `country_code` in the curated views.

# Relationships
- Parent: [trends_gdelt_analytics](/datasets/trends_gdelt_analytics)
- Powers: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily) and [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
- Related: [country](/dimensions/country)
