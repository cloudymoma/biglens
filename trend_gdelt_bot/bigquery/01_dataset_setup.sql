-- =============================================================================
-- Trend & GDELT Analytics: Dataset Setup
-- Target: BigQuery
-- =============================================================================

-- Create the curated analytics dataset where views and materialized tables will reside
CREATE SCHEMA IF NOT EXISTS `trends_gdelt_analytics`
OPTIONS (
  location = "US",
  description = "Curated semantic layer for Google Trends and GDELT 2.0 open data analytics agent."
);

-- FIPS 10-4 -> ISO 3166-1 alpha-2 country code mapping.
--
-- GDELT's ActionGeo_CountryCode uses FIPS 10-4 while Google Trends'
-- country_code uses ISO 3166-1 alpha-2. The two systems collide on the same
-- letters with different meanings (FIPS 'GB' = Gabon vs ISO 'GB' = United
-- Kingdom; FIPS 'CH' = China vs ISO 'CH' = Switzerland), so the raw codes
-- must NEVER be joined directly. Covers the 42 countries present in
-- bigquery-public-data.google_trends.international_top_terms (verified
-- against live data 2026-08-16); extend if Google Trends adds countries.
CREATE OR REPLACE TABLE `trends_gdelt_analytics.dim_fips_iso_country` (
  fips_code STRING OPTIONS (description = "FIPS 10-4 country code as used by GDELT ActionGeo_CountryCode."),
  iso_code STRING OPTIONS (description = "ISO 3166-1 alpha-2 country code as used by Google Trends country_code."),
  country_name STRING OPTIONS (description = "Common English country name.")
) AS
SELECT fips_code, iso_code, country_name FROM UNNEST([
  STRUCT('AR' AS fips_code, 'AR' AS iso_code, 'Argentina' AS country_name),
  ('AU', 'AT', 'Austria'),
  ('AS', 'AU', 'Australia'),
  ('BE', 'BE', 'Belgium'),
  ('BR', 'BR', 'Brazil'),
  ('CA', 'CA', 'Canada'),
  ('SZ', 'CH', 'Switzerland'),
  ('CI', 'CL', 'Chile'),
  ('CO', 'CO', 'Colombia'),
  ('EZ', 'CZ', 'Czech Republic'),
  ('GM', 'DE', 'Germany'),
  ('DA', 'DK', 'Denmark'),
  ('EG', 'EG', 'Egypt'),
  ('SP', 'ES', 'Spain'),
  ('FI', 'FI', 'Finland'),
  ('FR', 'FR', 'France'),
  ('UK', 'GB', 'United Kingdom'),
  ('HU', 'HU', 'Hungary'),
  ('ID', 'ID', 'Indonesia'),
  ('IS', 'IL', 'Israel'),
  ('IN', 'IN', 'India'),
  ('IT', 'IT', 'Italy'),
  ('JA', 'JP', 'Japan'),
  ('KS', 'KR', 'South Korea'),
  ('MX', 'MX', 'Mexico'),
  ('MY', 'MY', 'Malaysia'),
  ('NI', 'NG', 'Nigeria'),
  ('NL', 'NL', 'Netherlands'),
  ('NO', 'NO', 'Norway'),
  ('NZ', 'NZ', 'New Zealand'),
  ('RP', 'PH', 'Philippines'),
  ('PL', 'PL', 'Poland'),
  ('PO', 'PT', 'Portugal'),
  ('RO', 'RO', 'Romania'),
  ('SA', 'SA', 'Saudi Arabia'),
  ('SW', 'SE', 'Sweden'),
  ('TH', 'TH', 'Thailand'),
  ('TU', 'TR', 'Turkey'),
  ('TW', 'TW', 'Taiwan'),
  ('UP', 'UA', 'Ukraine'),
  ('VM', 'VN', 'Vietnam'),
  ('SF', 'ZA', 'South Africa')
]);
