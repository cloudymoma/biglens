---
type: Metric
title: Search Interest Score (0-100)
description: Relative search interest index normalized against a term's own historical peak share in a given region.
tags:
  - metric
  - search_trends
  - score
---

# Definition & Mathematical Calculation

Search Score is computed in 3 steps:
1. **Query Share ($P_{i, t, g}$)** = $\frac{\text{Term Searches}}{\text{Total All Searches in Region } g \text{ at Time } t}$
2. **Historical Peak Share ($P_{i, \text{max}, g}$)** = $\max_{t \in T} P_{i, t, g}$
3. **Scaled Index** = $\text{round}\left( \frac{P_{i, t, g}}{P_{i, \text{max}, g}} \times 100 \right)$

- `100`: Peak historical search interest for that term in that location.
- `50`: Half of peak search interest.
- `< 1`: Negligible interest relative to its peak.

*Note:* Do not compare scores across different terms as absolute volumes.

# Related Concepts
- Governed by: [search_share_normalization](/glossary/search_share_normalization)
- Contrast with: [search_rank](/metrics/search_rank) and [rank_vs_score_divergence](/glossary/rank_vs_score_divergence)
- Used in: [vw_search_trends_daily](/views/vw_search_trends_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
