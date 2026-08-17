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
- `actor_country_code` (STRING) — Country affiliation of the actor.

# Used in
- [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily)
