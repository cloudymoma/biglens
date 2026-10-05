---
type: Metric
title: Media Mentions Count
description: Count of mentions of an event across all documents within the first 15-minute GDELT update window in which the event was first logged.
tags:
  - metric
  - gdelt
  - media_attention
---

# Definition

Measures initial global media attention (`NumMentions` in `events_partitioned`), counting mentions across all documents processed within the **first 15-minute GDELT update batch** in which the event was first detected (subsequent mentions in later 15-minute batches are logged to the separate GDELT Mentions table rather than updating `events_partitioned`). Because GDELT is an index of media coverage rather than a police incident blotter, high mention counts reflect breaking, high-virality news stories.

**Counting caveat:** GDELT emits several event rows per source article, so sums of `NumMentions` across events are an initial media-attention *pulse*, not a distinct-article count. When listing top stories, deduplicate by `source_article_url` first (the golden queries show the pattern).

# Related Concepts
- Defined by: [media_pulse_vs_incident](/glossary/media_pulse_vs_incident)
- Used in: [vw_gdelt_news_events_daily](/views/vw_gdelt_news_events_daily), [vw_topic_news_trends_unified](/views/vw_topic_news_trends_unified)
