-- =============================================================================
-- Golden / Verified Query for the BigQuery Property Graph
-- Dataset: trends_gdelt_analytics
--
-- REQUIRES: a BigQuery reservation with Enterprise or Enterprise Plus edition.
-- GRAPH_TABLE queries (even dry runs) are rejected on on-demand billing, which
-- is why this query lives apart from 06_golden_agent_queries.sql — init.sh
-- preflights it separately and only warns when the edition is missing.
-- On on-demand projects, answer the same question with a self-join on
-- vw_search_trends_daily (see Query 4 in 06 for the aggregate variant).
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Query 6: Graph Traversal - Cross-Country Search Diffusion (GQL GRAPH_TABLE)
-- Intent: "Which search terms charted in the top 10 in both the UK and France on the same day? Compare their ranks."
-- Note: Uses the BigQuery Property Graph `trend_gdelt_graph` to perform
-- relationship pattern matching without manual SQL self-joins.
-- -----------------------------------------------------------------------------
SELECT *
FROM GRAPH_TABLE(
  `trends_gdelt_analytics.trend_gdelt_graph`
  MATCH (t:SearchTerm)-[e1:TRENDED_IN]->(c1:Country {country_code: 'GB'}),
        (t)-[e2:TRENDED_IN]->(c2:Country {country_code: 'FR'})
  WHERE e1.snapshot_date = e2.snapshot_date
    AND e1.rank <= 10 AND e2.rank <= 10
  COLUMNS (
    t.search_term,
    e1.snapshot_date AS date,
    e1.rank AS uk_rank,
    e2.rank AS fr_rank,
    e1.search_score AS uk_score,
    e2.search_score AS fr_score
  )
)
ORDER BY date DESC, uk_rank ASC
LIMIT 15;
