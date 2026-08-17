---
type: Dimension
title: Search Term Dimension
description: The textual query submitted by users to Google Search.
tags:
  - dimension
  - search_trends
---

# Attributes
- `search_term` (STRING) — Normalized query string.
- `is_historical_peak` (BOOLEAN) — Indicates if query reached score 100 on the snapshot date.

# Used in
- [vw_search_trends_daily](/views/vw_search_trends_daily)
- [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
