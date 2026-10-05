---
type: Dimension
title: QuadClass Classification Dimension
description: GDELT's coarsest 4-way geopolitical event grouping.
tags:
  - dimension
  - gdelt
---

# Levels
- `1`: **Verbal Cooperation** (CAMEO `01–05`, e.g. public statements, appeals, diplomatic consultations)
- `2`: **Material Cooperation** (CAMEO `06–09`, e.g. foreign aid, yield/concessions, investigations)
- `3`: **Verbal Conflict** (CAMEO `10–14`, e.g. demands, disapproval, threats, civilian protests/demos)
- `4`: **Material Conflict** (CAMEO `15–20`, e.g. military posturing, coercion, assault, armed clashes, mass violence)

# Used in
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
- Drives metric: [conflict_event_share](/metrics/conflict_event_share)
