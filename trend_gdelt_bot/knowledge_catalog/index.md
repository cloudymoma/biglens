---
okf_version: "0.1"
---

# Google Trends & GDELT Analytics Knowledge Bundle

An Open Knowledge Format (OKF) knowledge catalog defining the semantic layer, metrics, dimensions, schemas, and analytical domain knowledge for the Google Trends and GDELT 2.0 Conversational Agent.

## Datasets
- [Google Trends Dataset](/datasets/google_trends)
- [GDELT 2.0 Dataset](/datasets/gdelt)
- [Trends & GDELT Curated Analytics](/datasets/trends_gdelt_analytics)

## Source Tables
- [International Top Terms Table](/tables/international_top_terms)
- [International Top Rising Terms Table](/tables/international_top_rising_terms)
- [GDELT Events Partitioned Table](/tables/events_partitioned)
- [GDELT GKG Partitioned Table](/tables/gkg_partitioned)
- [FIPS to ISO Country Code Mapping](/tables/dim_fips_iso_country)

## Curated Semantic Views
- [Daily Search Trends View](/views/vw_search_trends_daily)
- [Rising Search Trends View](/views/vw_search_trends_rising)
- [Daily GDELT News Events View](/views/vw_gdelt_news_events_daily)
- [Daily GDELT Themes View](/views/vw_gdelt_gkg_themes_daily)
- [Unified Topic & News Analytics Mart](/views/vw_topic_news_trends_unified)

## Core Metrics
- [Search Interest Score Metric](/metrics/search_score)
- [Search Volume Rank Metric](/metrics/search_rank)
- [Percent Gain Metric](/metrics/percent_gain)
- [Goldstein Stability Scale Metric](/metrics/goldstein_scale)
- [Sentiment Tone Metric](/metrics/sentiment_tone)
- [Conflict Event Share Metric](/metrics/conflict_event_share)
- [Media Mentions Count Metric](/metrics/media_mentions_count)

## Dimensions & Entities
- [Country Dimension](/dimensions/country)
- [Search Term Dimension](/dimensions/search_term)
- [CAMEO Event Category Dimension](/dimensions/cameo_event_category)
- [QuadClass Dimension](/dimensions/quadclass)
- [Actor Entity Dimension](/dimensions/actor)
- [GKG News Theme Dimension](/dimensions/gkg_theme)

## Glossary & Business Logic
- [Search Share Normalization Math](/glossary/search_share_normalization)
- [Rank vs. Score Divergence Rule](/glossary/rank_vs_score_divergence)
- [CAMEO Taxonomy Standard](/glossary/cameo_taxonomy)
- [Media Pulse vs. Incident Registry](/glossary/media_pulse_vs_incident)
- [Partitioning & Query Optimization](/glossary/partitioning_best_practices)
