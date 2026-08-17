---
type: Glossary Term
title: Partitioning & Query Optimization in BigQuery
description: Critical SQL optimization rules for querying Google Trends and GDELT public datasets.
tags:
  - glossary
  - bigquery
  - cost_optimization
---

# Partition Pruning Rules for LLM Agents

1. **Google Trends**: Always filter by `refresh_date >= DATE_SUB(CURRENT_DATE(), INTERVAL X DAY)` or `refresh_date = (SELECT MAX(refresh_date) ...)`.
2. **GDELT 2.0**: Always filter by `_PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL X DAY)`.
3. **Scan Limits**: Without partition filters, GDELT tables can scan multiple terabytes per query.

# Correctness Rules When Querying Raw Tables

4. **Pin the trend week**: Each Trends `refresh_date` partition carries 5 years of weekly history. For current values add `week = (SELECT MAX(week) FROM ... WHERE refresh_date = <same date>)`. The curated views already do this.
5. **Never join FIPS to ISO country codes**: GDELT `ActionGeo_CountryCode` is FIPS 10-4; Trends `country_code` is ISO 3166 (FIPS 'GB' = Gabon, ISO 'GB' = United Kingdom). Decode via [dim_fips_iso_country](/tables/dim_fips_iso_country) — the curated views already expose ISO `country_code`.
6. **Anchor on the latest available snapshot**: Trends publishes with a 1–2 day lag; prefer `date = (SELECT MAX(date) ...)` over `CURRENT_DATE() - N`.
