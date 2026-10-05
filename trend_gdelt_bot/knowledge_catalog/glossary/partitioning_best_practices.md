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

1. **Google Trends**: Always include a constant date range filter (`refresh_date >= DATE_SUB(CURRENT_DATE(), INTERVAL X DAY)`); to pin the latest snapshot without triggering a full-table scalar subquery scan, pair a narrow constant range (`INTERVAL 7 DAY`) with `QUALIFY refresh_date = MAX(refresh_date) OVER ()`.
2. **GDELT 2.0**: Always filter by `_PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL X DAY)`.
3. **Scan Limits**: Without partition filters, GDELT tables can scan multiple terabytes per query.
4. **Tier 2 proxy views**: The `vw_raw_*` views project the partition pseudo-column as a regular column (`partition_date` for GDELT, `snapshot_date` for Trends) — pseudo-columns are not visible through views otherwise. Filtering these columns with a constant date/datetime range prunes partitions through the view exactly like filtering the base table.
5. **Hourly trends (US real-time)**: `google_trends_hourly.*` is HOUR-partitioned on a DATETIME `refresh_time` (exposed as `snapshot_time`), with several intraday snapshots per day, ~30-day snapshot retention, and a ~1-YEAR weekly history per snapshot (vs ~5 years in the daily tables). Filter with a constant 2-day DATETIME range (`WHERE snapshot_time >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 2 DAY)`) and `QUALIFY snapshot_time = MAX(snapshot_time) OVER ()` — it is a DATETIME, not a DATE.

# Correctness Rules When Querying Raw Tables

6. **Pin the trend week**: Each Trends `refresh_date` partition carries 5 years of weekly history. For current values add `QUALIFY refresh_date = MAX(refresh_date) OVER () AND week = MAX(week) OVER ()` inside a narrow constant `refresh_date` range. The curated views already do this.
7. **Never range over snapshots for history**: For historical trend curves, pin the LATEST `refresh_date`/`snapshot_date` and scan `week`. Ranging over snapshot dates averages overlapping 5-year histories into garbage.
8. **Never join FIPS to ISO country codes**: GDELT `ActionGeo_CountryCode` is FIPS 10-4; Trends `country_code` is ISO 3166 (FIPS 'GB' = Gabon, ISO 'GB' = United Kingdom). Decode via [dim_fips_iso_country](/tables/dim_fips_iso_country) — the curated views already expose ISO `country_code`.
9. **Anchor on the latest available snapshot**: Trends publishes with a 1–2 day lag; combine a 7-day constant `snapshot_date` window with `QUALIFY snapshot_date = MAX(snapshot_date) OVER ()` rather than assuming `CURRENT_DATE()` or `CURRENT_DATE() - 1` exists.
