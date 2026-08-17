---
type: Metric
title: Search Percent Gain (%)
description: Week-over-week percentage growth rate for surging / breakout queries.
tags:
  - metric
  - search_trends
  - growth
---

# Definition

Calculated as:
$$\text{Percent Gain} = \frac{\text{Current Week Search Volume} - \text{Previous Week Volume}}{\text{Previous Week Volume}} \times 100$$

Breakout terms with zero baseline in the prior week can exceed several thousand percent.

# Related Concepts
- Used in: [vw_search_trends_rising](/views/vw_search_trends_rising)
