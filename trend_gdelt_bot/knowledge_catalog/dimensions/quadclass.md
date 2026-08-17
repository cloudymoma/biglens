---
type: Dimension
title: QuadClass Classification Dimension
description: GDELT's coarsest 4-way geopolitical event grouping.
tags:
  - dimension
  - gdelt
---

# Levels
- `1`: **Verbal Cooperation** (e.g. speeches, praise, statements)
- `2`: **Material Cooperation** (e.g. foreign aid, trade agreements, joint drills)
- `3`: **Verbal Conflict** (e.g. threats, sanctions warnings, complaints)
- `4`: **Material Conflict** (e.g. armed clashes, assaults, strikes, protests)

# Used in
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- Drives metric: [conflict_event_share](/metrics/conflict_event_share)
