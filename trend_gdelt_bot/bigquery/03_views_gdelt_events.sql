-- =============================================================================
-- GDELT 2.0 Global News Events & Sentiment Curated Views
-- Source: gdelt-bq.gdeltv2.events_partitioned, gdelt-bq.gdeltv2.gkg_partitioned
-- Dataset: trends_gdelt_analytics
-- =============================================================================

-- View 1: Curated Daily News Events with Decoded CAMEO & QuadClass
--
-- ActionGeo_CountryCode is FIPS 10-4, NOT ISO 3166 (FIPS 'GB' = Gabon, ISO
-- 'GB' = United Kingdom). It is exposed raw as fips_country_code and decoded
-- to ISO via dim_fips_iso_country as country_code so downstream joins to
-- Google Trends (ISO) are correct. country_code is NULL for countries not in
-- the mapping (i.e. countries Google Trends does not cover).
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_gdelt_news_events_daily`
OPTIONS (
  description = "Daily global news events from GDELT 2.0 with decoded CAMEO categories, QuadClass grouping, sentiment tone, stability scores, and ISO country codes mapped from FIPS."
) AS
SELECT
  e._PARTITIONDATE AS report_date,
  -- ISO 3166-1 alpha-2 (joinable to Google Trends); NULL when the action
  -- location's country is not covered by the Trends dataset.
  iso.iso_code AS country_code,
  e.ActionGeo_CountryCode AS fips_country_code,
  e.ActionGeo_FullName AS location_name,
  e.ActionGeo_Lat AS latitude,
  e.ActionGeo_Long AS longitude,
  e.Actor1Name AS primary_actor,
  e.Actor1CountryCode AS actor1_country_code,
  e.Actor2Name AS secondary_actor,
  e.Actor2CountryCode AS actor2_country_code,
  e.EventCode AS cameo_event_code,
  e.EventRootCode AS cameo_root_code,
  -- Decoded CAMEO Root Taxonomy
  CASE e.EventRootCode
    WHEN '01' THEN 'Make Public Statement'
    WHEN '02' THEN 'Appeal'
    WHEN '03' THEN 'Express Intent to Cooperate'
    WHEN '04' THEN 'Consult'
    WHEN '05' THEN 'Engage in Diplomatic Cooperation'
    WHEN '06' THEN 'Engage in Material Cooperation'
    WHEN '07' THEN 'Provide Aid'
    WHEN '08' THEN 'Yield'
    WHEN '09' THEN 'Investigate'
    WHEN '10' THEN 'Demand'
    WHEN '11' THEN 'Disapprove'
    WHEN '12' THEN 'Reject'
    WHEN '13' THEN 'Threaten'
    WHEN '14' THEN 'Protest'
    WHEN '15' THEN 'Exhibit Military Posture'
    WHEN '16' THEN 'Reduce Relations'
    WHEN '17' THEN 'Coerce'
    WHEN '18' THEN 'Assault'
    WHEN '19' THEN 'Fight'
    WHEN '20' THEN 'Use Unconventional Mass Violence'
    ELSE 'Other / Unclassified'
  END AS event_category,
  e.QuadClass AS quad_class_id,
  CASE e.QuadClass
    WHEN 1 THEN 'Verbal Cooperation'
    WHEN 2 THEN 'Material Cooperation'
    WHEN 3 THEN 'Verbal Conflict'
    WHEN 4 THEN 'Material Conflict'
    ELSE 'Unknown'
  END AS quad_class_name,
  -- Political Stability Impact (-10.0 extreme conflict to +10.0 peace/cooperation)
  e.GoldsteinScale AS goldstein_scale,
  -- Sentiment Tone (-100 to +100, typical range -10 to +10)
  e.AvgTone AS sentiment_tone,
  e.NumMentions AS media_mentions_count,
  e.NumSources AS distinct_sources_count,
  e.NumArticles AS article_count,
  e.SOURCEURL AS source_article_url
FROM
  `gdelt-bq.gdeltv2.events_partitioned` AS e
LEFT JOIN
  `trends_gdelt_analytics.dim_fips_iso_country` AS iso
ON
  e.ActionGeo_CountryCode = iso.fips_code
WHERE
  e._PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY);

-- View 2: Curated Daily GKG Themes and Sentiment
CREATE OR REPLACE VIEW `trends_gdelt_analytics.vw_gdelt_gkg_themes_daily`
OPTIONS (
  description = "Daily news themes and sentiment aggregations from GDELT Global Knowledge Graph."
) AS
SELECT
  _PARTITIONDATE AS report_date,
  -- V2Themes entries are `THEME_NAME,charOffset`; strip the offset so the
  -- theme name is clean (e.g. 'TAX_DISEASE', not 'TAX_DISEASE,1234').
  SPLIT(SPLIT(V2Themes, ';')[SAFE_OFFSET(0)], ',')[SAFE_OFFSET(0)] AS primary_theme,
  SourceCommonName AS media_source,
  DocumentIdentifier AS document_url,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(0)] AS FLOAT64) AS sentiment_tone,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(1)] AS FLOAT64) AS positive_score,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(2)] AS FLOAT64) AS negative_score,
  CAST(SPLIT(V2Tone, ',')[SAFE_OFFSET(3)] AS FLOAT64) AS polarity_score
FROM
  `gdelt-bq.gdeltv2.gkg_partitioned`
WHERE
  _PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
  AND V2Themes IS NOT NULL;
