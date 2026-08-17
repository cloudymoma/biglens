---
type: Metric
title: Conflict Event Share (%)
description: Percentage of daily news events in a country classified as Verbal Conflict (QuadClass 3) or Material Conflict (QuadClass 4).
tags:
  - metric
  - gdelt
  - conflict
---

# Definition

$$\text{Conflict Share \%} = \frac{\text{Count of Events with QuadClass } \in \{3, 4\}}{\text{Total Recorded Events}} \times 100$$

A high conflict share (>30-40%) indicates acute geopolitical tension or crisis.

# Related Concepts
- Defined by: [quadclass](/dimensions/quadclass)
- Used in: [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
