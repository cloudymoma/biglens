---
type: Metric
title: Search Volume Rank (1-25)
description: Ordinal ranking of the top 25 queries ordered by daily comparative search volume within a country or metro area.
tags:
  - metric
  - search_trends
  - rank
---

# Definition

Measures cross-sectional daily volume hierarchy:
- `1`: The single most searched query on that date in that country.
- `25`: The 25th most searched query on that date.

The top terms leaderboard is sorted strictly by `search_rank ASC`.

# Related Concepts
- Contrast with: [search_score](/metrics/search_score)
- Used in: [vw_search_trends_daily](/views/vw_search_trends_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
