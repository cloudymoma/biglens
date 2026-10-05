---
type: Glossary Term
title: Media Pulse vs. Incident Registry
description: Crucial analytical distinction regarding how GDELT counts reflect news media attention rather than police blotters.
tags:
  - glossary
  - gdelt
---

# Definition

GDELT tracks what the global news media is reporting. In GDELT 2.0, `events_partitioned` logs one row per distinct coded `(Actor1, Actor2, EventCode, ActionGeo)` action when first extracted, while a single news article often yields multiple distinct coded event rows and `NumMentions` records how many documents mentioned that action within its first 15-minute update window (subsequent coverage across outlets is logged to the separate Mentions table).

Because major stories involve many actors, sub-actions, and follow-up reports, higher event and mention counts reflect **media attention and virality** rather than a 1:1 physical incident blotter — making them a strong signal for comparing against Google Search query spikes (deduplicate by `source_article_url` when listing individual stories).
