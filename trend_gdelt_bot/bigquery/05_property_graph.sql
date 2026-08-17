-- =============================================================================
-- BigQuery Property Graph DDL: Trend & GDELT Semantic Graph
-- Dataset: trends_gdelt_analytics
--
-- The node/edge tables below are point-in-time SNAPSHOTS of the (rolling
-- 90-day) trends view: property graphs require physical tables, so this
-- script must be re-run to refresh them (e.g. as a daily BigQuery scheduled
-- query). Edge IDs are deterministic hashes of the natural key
-- (term, country, date), so refreshes are idempotent. The graph currently
-- models the Google Trends half of the domain (terms x countries); GDELT
-- news context lives in the relational views.
-- =============================================================================

-- Table 1: Entity Node - Countries
CREATE OR REPLACE TABLE `trends_gdelt_analytics.node_countries` AS
SELECT DISTINCT
  country_code,
  country_name
FROM
  `trends_gdelt_analytics.vw_search_trends_daily`
WHERE
  country_code IS NOT NULL;

-- Table 2: Entity Node - Search Terms
CREATE OR REPLACE TABLE `trends_gdelt_analytics.node_search_terms` AS
SELECT
  search_term,
  MAX(search_score) AS max_historical_score,
  MIN(rank) AS best_historical_rank
FROM
  `trends_gdelt_analytics.vw_search_trends_daily`
GROUP BY
  search_term;

-- Table 3: Relationship Edge - Trend Observation (Term -> Country)
CREATE OR REPLACE TABLE `trends_gdelt_analytics.edge_trended_in` AS
SELECT
  -- Deterministic edge key over the view's natural grain
  -- (snapshot_date, country_code, search_term) — stable across refreshes.
  TO_HEX(MD5(CONCAT(search_term, '|', country_code, '|', CAST(snapshot_date AS STRING)))) AS edge_id,
  search_term,
  country_code,
  snapshot_date,
  rank,
  search_score
FROM
  `trends_gdelt_analytics.vw_search_trends_daily`;

-- Define the Property Graph (BigQuery DDL: NODE TABLES, not VERTEX TABLES;
-- REFERENCES targets the node table alias).
CREATE OR REPLACE PROPERTY GRAPH `trends_gdelt_analytics.trend_gdelt_graph`
  NODE TABLES (
    `trends_gdelt_analytics.node_countries` AS node_countries
      KEY (country_code)
      LABEL Country
      PROPERTIES (country_code, country_name),
    `trends_gdelt_analytics.node_search_terms` AS node_search_terms
      KEY (search_term)
      LABEL SearchTerm
      PROPERTIES (search_term, max_historical_score, best_historical_rank)
  )
  EDGE TABLES (
    `trends_gdelt_analytics.edge_trended_in` AS edge_trended_in
      KEY (edge_id)
      SOURCE KEY (search_term) REFERENCES node_search_terms (search_term)
      DESTINATION KEY (country_code) REFERENCES node_countries (country_code)
      LABEL TRENDED_IN
      PROPERTIES (edge_id, snapshot_date, rank, search_score)
  );
