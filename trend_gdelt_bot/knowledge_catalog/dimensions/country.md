---
type: Dimension
title: Country & Geographic Dimension
description: Geographic attributes including ISO 2-letter country code, country name, and designated market areas.
tags:
  - dimension
  - geography
---

# Attributes
- `country_code` (STRING) — ISO 3166-1 alpha-2 code (e.g. 'US', 'GB', 'DE', 'JP', 'UA'). This is the canonical country key across all curated views.
- `country_name` (STRING) — Common English name.
- `region_name` / `dma_name` (STRING) — Sub-national region or metro area.
- `fips_country_code` (STRING) — Raw FIPS 10-4 code from GDELT `ActionGeo_CountryCode` (exposed in the GDELT views only).

# Two Code Systems — Never Join Raw Codes

Google Trends uses **ISO 3166-1 alpha-2**; raw GDELT `ActionGeo_CountryCode` uses **FIPS 10-4**. The same letters mean different countries in each system (FIPS 'GB' = Gabon vs ISO 'GB' = United Kingdom; FIPS 'CH' = China vs ISO 'CH' = Switzerland). The curated GDELT views already decode FIPS to ISO through [dim_fips_iso_country](/tables/dim_fips_iso_country), so `country_code` is safe to join everywhere in the semantic layer.

# Used in
- [vw_search_trends_daily](/views/vw_search_trends_daily)
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
