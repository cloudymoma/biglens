---
type: Glossary Term
title: Search Share Normalization (Google Trends)
description: Mathematical framework used by Google Trends to calculate the 0-100 score index.
tags:
  - glossary
  - google_trends
  - math
---

# Definition

Google Trends data is not raw search counts. It represents normalized query share:

1. **Query Proportion**: $\frac{\text{Query Volume}(term, region, time)}{\text{Total Queries}(region, time)}$
2. **Historical Peak Indexing**: Scaled such that the historical maximum proportion within the region equals **100**.

Because of this, two regions with the same score of 100 do not have the same raw search volume; rather, both experienced their own maximum query concentration for that term.
