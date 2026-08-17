---
type: Metric
title: Media Mentions Count
description: Total count of news articles and media sources reporting on a specific event or topic.
tags:
  - metric
  - gdelt
  - media_attention
---

# Definition

Measures the volume of global media attention. Because GDELT is an index of media coverage rather than a police incident blotter, high mention counts reflect breaking, high-virality news stories.

**Counting caveat:** GDELT emits several event rows per source article, so sums of `NumMentions` across events are a media-attention *pulse*, not a distinct-article count. When listing top stories, deduplicate by `source_article_url` first (the golden queries show the pattern).

# Related Concepts
- Defined by: [media_pulse_vs_incident](/glossary/media_pulse_vs_incident)
- Used in: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
