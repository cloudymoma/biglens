---
type: Metric
title: Goldstein Geopolitical Stability Scale (-10 to +10)
description: Standard political-science score measuring the theoretical impact of an event type on a country's stability.
tags:
  - metric
  - gdelt
  - geopolitics
---

# Definition

Theoretical scale defined by political scientist Joshua Goldstein:
- `+10.0`: Highest de-escalation/cooperation (e.g. military retreat or surrender `0874`; `+8.0` sign formal agreement `057`; `+7.4` provide humanitarian aid `073`).
- `0.0`: Neutral verbal interaction (e.g. make public statement `010`, consult `040`).
- `-10.0`: Extreme destabilization/conflict (e.g. use conventional military force `190`, unconventional mass violence `20`; `-9.0` assault `18`).

*Crucial Rule:* Goldstein score is fixed per CAMEO event code — it rates the nature of the action type, not the individual news article.

# Related Concepts
- Used in: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
