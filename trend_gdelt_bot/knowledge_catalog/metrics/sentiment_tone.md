---
type: Metric
title: Sentiment Average Tone (-100 to +100)
description: Average emotional sentiment tone of news media coverage for an event or topic.
tags:
  - metric
  - gdelt
  - sentiment
---

# Definition

Measures percentage of positive words minus percentage of negative words across the text of articles reporting the event:
- Real-world range: Typically between `-10.0` and `+10.0`.
- `< -2.0`: Substantially negative news coverage.
- `> +2.0`: Substantially positive news coverage.
- `-2.0 to +2.0`: Neutral / standard reportage tone.

# Related Concepts
- Used in: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
