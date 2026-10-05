---
type: Glossary Term
title: CAMEO Event Taxonomy Standard
description: Conflict and Mediation Event Observations codebook classifying geopolitical interactions.
tags:
  - glossary
  - gdelt
  - cameo
---

# Definition

CAMEO provides a hierarchical framework for coding political actions (aligned with GDELT's 4-way [quadclass](/dimensions/quadclass) grouping):
- `01–05` (**QuadClass 1 — Verbal Cooperation**): Public statements, appeals, intent to cooperate, consultations, diplomatic cooperation
- `06–09` (**QuadClass 2 — Material Cooperation**): Material cooperation, aid, yield/concessions, and `09 Investigate`
- `10–14` (**QuadClass 3 — Verbal Conflict**): Demands, disapproval, rejections, threats, and `14 Protest` (civilian demonstrations/strikes)
- `15–20` (**QuadClass 4 — Material Conflict**): Force posture, reduce relations, coercion, assault, armed fight, unconventional mass violence

When filtering for conflict vs. cooperation in SQL, prefer `quad_class_id IN (3, 4)` (or `quad_class_id = 4` for physical conflict) rather than hardcoding `cameo_root_code` ranges.
