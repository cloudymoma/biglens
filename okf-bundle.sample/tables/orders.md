---
type: BigQuery Table
title: Orders
description: One row per customer order.
resource: bigquery:demo.sales.orders
tags:
  - core
---

# Schema

- order_id (STRING)
- customer_id (STRING) — references [customers](/tables/customers)
- amount (NUMERIC)

# Relationships

- Parent: [sales](/datasets/sales)
- Powers the [revenue](/views/revenue) view.
