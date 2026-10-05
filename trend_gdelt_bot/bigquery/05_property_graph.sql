-- =============================================================================
-- BigQuery Property Graph DDL: Trend & GDELT Semantic Graph
-- Dataset: trends_gdelt_analytics
--
-- The node/edge tables below are point-in-time SNAPSHOTS of the (~30-day
-- rolling) trends view: property graphs require physical tables, so this
-- script must be re-run to refresh them (e.g. as a daily BigQuery scheduled
-- query running 05_property_graph.sql). To avoid scanning
-- vw_search_trends_daily three times (29.8 GB total), edge_trended_in is
-- materialized first (11.9 GB) and node_search_terms / node_countries are
-- derived from edge_trended_in and dim_fips_iso_country (~1 MB), cutting
-- rebuild cost by ~60%.
-- =============================================================================

-- Table 1: Relationship Edge - Trend Observation (Term -> Country)
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

ALTER TABLE `trends_gdelt_analytics.edge_trended_in`
SET OPTIONS (description = "Property-graph edge snapshot: TRENDED_IN observations (SearchTerm -> Country) over the ~30-day rolling trends snapshot window, one row per term/country/snapshot date.");
ALTER TABLE `trends_gdelt_analytics.edge_trended_in`
ALTER COLUMN edge_id SET OPTIONS (description = "Deterministic MD5 hash of (search_term, country_code, snapshot_date) — stable edge key across refreshes."),
ALTER COLUMN search_term SET OPTIONS (description = "Source node key: the trending search query."),
ALTER COLUMN country_code SET OPTIONS (description = "Destination node key: ISO 3166-1 alpha-2 country code."),
ALTER COLUMN snapshot_date SET OPTIONS (description = "Trends snapshot date on which the term charted in this country."),
ALTER COLUMN rank SET OPTIONS (description = "Daily cross-sectional popularity rank (1-25) of the term in this country."),
ALTER COLUMN search_score SET OPTIONS (description = "Equal-weighted mean of sub-national region scores (0-100) for the latest trend week in this country.");

-- Table 2: Entity Node - Countries (derived from dim_fips_iso_country, 0 bytes scanned from Trends)
CREATE OR REPLACE TABLE `trends_gdelt_analytics.node_countries` AS
SELECT
  iso_code AS country_code,
  country_name
FROM
  `trends_gdelt_analytics.dim_fips_iso_country`
WHERE
  in_google_trends;

ALTER TABLE `trends_gdelt_analytics.node_countries`
SET OPTIONS (description = "Property-graph node snapshot: the 42 countries covered by Google Trends international tables. Node key for label Country in trend_gdelt_graph.");
ALTER TABLE `trends_gdelt_analytics.node_countries`
ALTER COLUMN country_code SET OPTIONS (description = "ISO 3166-1 alpha-2 country code (graph node key)."),
ALTER COLUMN country_name SET OPTIONS (description = "Full English name of the country.");

-- Table 3: Entity Node - Search Terms (derived from edge_trended_in, ~1 MB scan)
CREATE OR REPLACE TABLE `trends_gdelt_analytics.node_search_terms` AS
SELECT
  search_term,
  MAX(search_score) AS max_historical_score,
  MIN(rank) AS best_historical_rank
FROM
  `trends_gdelt_analytics.edge_trended_in`
GROUP BY
  search_term;

ALTER TABLE `trends_gdelt_analytics.node_search_terms`
SET OPTIONS (description = "Property-graph node snapshot: search terms observed in the ~30-day rolling trends snapshot window. Node key for label SearchTerm in trend_gdelt_graph.");
ALTER TABLE `trends_gdelt_analytics.node_search_terms`
ALTER COLUMN search_term SET OPTIONS (description = "Search query string (graph node key)."),
ALTER COLUMN max_historical_score SET OPTIONS (description = "Highest country-level regional-average search_score (0-100) the term reached across countries within the ~30-day snapshot window."),
ALTER COLUMN best_historical_rank SET OPTIONS (description = "Best (lowest) daily rank (1-25) the term reached across countries within the ~30-day snapshot window.");

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
