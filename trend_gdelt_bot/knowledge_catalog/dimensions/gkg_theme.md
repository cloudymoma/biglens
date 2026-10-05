---
type: Dimension
title: GKG News Theme Dimension
description: Categorical news topics and thematic taxonomies recognized across global media (e.g. HEALTH, ECONOMY, ELECTION, CYBER).
tags:
  - dimension
  - themes
  - gdelt
---

# Attributes
- `primary_theme` (STRING) — First substantive GKG theme assigned to the article (skipping generic meta/taxonomy prefixes `TAX_`, `WB_`, `EPU_`, `CRISISLEX_`, `UNGP_`, with fallback to the first non-empty theme).
- `themes` (ARRAY<STRING>) — Full array of distinct GKG theme codes attached to the article (`UNNEST(themes)` to filter or aggregate across all themes on an article).

# Used in
- [vw_gdelt_gkg_themes_daily](/views/vw_gdelt_gkg_themes_daily)
