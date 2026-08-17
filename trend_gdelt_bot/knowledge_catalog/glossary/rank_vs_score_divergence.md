---
type: Glossary Term
title: Rank vs. Score Divergence Rule
description: Key business logic explaining why search rank and search score are not monotonically aligned.
tags:
  - glossary
  - analytical_logic
---

# Definition & Agent Rule

- **Rank (1–25)** is a **cross-sectional comparison** among all queries on that single day.
- **Score (0–100)** is a **longitudinal comparison** of a query against its own historical peak.

### Consequence
A term like `"weather"` or `"youtube"` can be **Rank #1** everyday while having a **Score of 65** (steady high volume, but not at its all-time spike). Conversely, an event like a solar eclipse or local election can chart at **Rank #15** while scoring **100** (its all-time maximum volume).

Conversational agents must never sort top leaderboards by score or assume rank 1 implies score 100.
