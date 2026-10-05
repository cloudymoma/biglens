---
type: Dimension
title: Actor & Entity Dimension
description: Geopolitical and societal actors interacting in global news reports (governments, military, rebels, NGOs, companies).
tags:
  - dimension
  - actors
  - gdelt
---

# Attributes
- `primary_actor` (STRING) — Actor initiating action (Actor 1).
- `secondary_actor` (STRING) — Actor receiving action (Actor 2).
- `actor1_country_code`, `actor2_country_code` (STRING) — CAMEO 3-letter country affiliation codes of Actor 1 and Actor 2 (e.g. `'USA'`, `'CHN'`; not ISO 3166).

# Used in
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- [vw_raw_gdelt_events_archive](/views/vw_raw_gdelt_events_archive)
