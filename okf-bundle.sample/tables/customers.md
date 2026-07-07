---
type: BigQuery Table
title: Customers
description: One row per customer.
resource: bigquery:demo.sales.customers
tags:
  - core
  - pii
---

# Schema

- customer_id (STRING)
- email (STRING) — classified as [PII](/glossary/pii)

# Relationships

- Parent: [sales](/datasets/sales)
