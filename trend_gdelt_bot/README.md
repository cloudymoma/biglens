# Google Trends & GDELT BigQuery Conversational Analytical Agent

English | [简体中文](README_cn.md)

A standalone, production-ready package to build a **Conversational AI Data Agent in BigQuery** that queries, correlates, and explains **Google Trends** search trends and **GDELT 2.0** global news sentiment & geopolitical events.

---

## 📁 Repository Structure

```text
trend_gdelt_bot/
├── init.sh                                      # Automated setup script (gcloud + bq + kcmd)
├── README.md                                    # This guide & BigQuery UI tutorial
├── README_cn.md                                 # Simplified Chinese version of this guide
│
├── bigquery/                                    # Semantic Data Layer & SQL Assets
│   ├── 01_dataset_setup.sql                     # Dataset DDL + FIPS 10-4 → ISO 3166-1 country mapping table
│   ├── 02_views_search_trends.sql               # Curated Trends views (pinned to latest trend week)
│   ├── 03_views_gdelt_events.sql                # Curated GDELT daily events & GKG themes (CAMEO decoded)
│   ├── 04_views_trends_gdelt_unified.sql        # Unified Topic & News Trends analytical mart
│   ├── 05_property_graph.sql                    # BigQuery Property Graph DDL (NODE TABLES / ISO GQL)
│   ├── 06_golden_agent_queries.sql              # Few-shot verified queries for the AI agent
│   └── 07_graph_golden_query.sql                # Graph GQL example (requires Enterprise edition)
│
└── knowledge_catalog/                           # Open Knowledge Format (OKF) Knowledge Bundle
    ├── index.md                                 # Root catalog index (okf_version: "0.1")
    ├── datasets/                                # Dataset concepts (Google Trends, GDELT, Analytics)
    ├── tables/                                  # Table schemas + dim_fips_iso_country
    ├── views/                                   # Curated semantic views & lineage
    ├── metrics/                                 # Metric formulas (Score, Rank, Goldstein, Tone, etc.)
    ├── dimensions/                              # Dimension definitions (Country, CAMEO, QuadClass, etc.)
    └── glossary/                                # Business rules & query grounding logic
```

---

## ⚡ 1-Minute Automated Setup (`init.sh`)

The provided [`init.sh`](./init.sh) deploys the entire BigQuery semantic layer, creates the FIPS → ISO mapping table, builds the Property Graph, and registers the dataset's knowledge catalog scope via `kcmd`. Dataplex catalog entries for every table, view, and graph are auto-synced by BigQuery, carrying the descriptions embedded in the SQL DDL; the OKF markdown bundle in `knowledge_catalog/` grounds the agent directly (see Step 3 below).

The script is **idempotent** — re-run it any time to pick up SQL changes and re-materialize the property-graph snapshot tables from the latest data (or schedule it, e.g. daily, to keep the graph fresh).

### Prerequisites

1. **Google Cloud CLI (`gcloud` and `bq`)**
   * **Installation:** Follow the [Official Google Cloud CLI Installation Guide](https://cloud.google.com/sdk/docs/install) to download and install `gcloud` and the `bq` component.
   * **Authentication & Project Configuration:**
     ```bash
     gcloud auth login
     gcloud auth application-default login
     gcloud config set project <YOUR_PROJECT_ID>
     ```

2. **Knowledge Catalog CLI (`kcmd`)** *(Optional — registers the dataset scope for kcmd pull/push workflows)*
   * **Installation Guide:** Follow the [GoogleCloudPlatform/knowledge-catalog README](https://github.com/GoogleCloudPlatform/knowledge-catalog#readme) to build and install `kcmd` into your `PATH`.
   * *Note: If `kcmd` is not installed, `init.sh` skips scope registration and still deploys everything else. Dataplex catalog entries auto-sync from BigQuery either way, and the local markdown files in `knowledge_catalog/` remain fully functional for direct agent grounding.*

### Run Setup
```bash
cd trend_gdelt_bot
./init.sh <YOUR_PROJECT_ID>
```
*(If no argument is provided, it automatically detects your active `gcloud` project. The location (`US`) and dataset name (`trends_gdelt_analytics`) are fixed — every SQL file references the dataset by name, and the source public datasets live in the US multi-region.)*

---

## 🤖 Tutorial: Building the Conversational Agent in BigQuery UI

Once `init.sh` has deployed the views, follow these steps to configure your conversational agent in the **Google Cloud Console**:

### Step 1: Open BigQuery Studio / Conversational Analytics
1. Go to the [Google Cloud Console](https://console.cloud.google.com/).
2. Navigate to **BigQuery** → **BigQuery Studio**.
3. In the left or top navigation, select **Data Canvas** / **Conversational Analytics** (or **Create Agent** / **Gemini in BigQuery**).

---

### Step 2: Configure Data Sources (Two-Tier Architecture)
In the BigQuery Agent configuration, add both **Tier 1 (Curated Dataset)** and optionally **Tier 2 (Base Public Tables)**. This Two-Tier design gives the agent fast, safe answers for standard questions while preserving deep drill-down capabilities:

#### 🔹 Tier 1: Curated Semantic Layer (Primary & Default)
Add the entire `<YOUR_PROJECT_ID>.trends_gdelt_analytics` dataset (or individual views):

| Table / View / Graph | Purpose & When the Agent Uses It |
| :--- | :--- |
| **`vw_topic_news_trends_unified`** *(Primary Mart)* | **Macro Correlation:** Default view for questions correlating search trends with country news sentiment, Goldstein stability, and conflict share. |
| **`vw_search_trends_rising`** | **Surging Queries:** Breakout search terms with week-over-week percentage gain (`percent_gain`). |
| **`vw_search_trends_daily`** | **Rankings & Regional Spread:** Daily top 25 rankings, regional coverage counts, and peak flags. (DMA metro data is US-only — see Tier 2 `top_terms`.) |
| **`vw_gdelt_news_events_daily`** | **Granular Event Citations:** Specific actor dyads (`primary_actor`, `secondary_actor`), CAMEO categories, and source article URLs (`source_article_url`). |
| **`vw_gdelt_gkg_themes_daily`** | **Thematic Analysis:** Primary GKG news themes, media source domains, and tone vectors. **Covers the last 30 days only** (GKG is deliberately windowed tighter for cost). |
| **`dim_fips_iso_country`** | **Country Dimension:** FIPS 10-4 ↔ ISO 3166-1 country code lookup table. |
| **`trend_gdelt_graph`** *(Property Graph - Preview)* | **Graph Pattern Matching:** Native property graph for cross-country term overlap, bilateral comparisons, and diffusion networks via ISO GQL / `GRAPH_TABLE`. ⚠️ **Requires a BigQuery Enterprise (or Enterprise Plus) reservation** — `GRAPH_TABLE` queries are rejected on on-demand billing. |

#### 🔹 Tier 2: Base Public Tables (For Deep Historical & Granular Drill-Down)
Add these public dataset tables if you want the agent to answer deep drill-down questions that the 90-day curated views omit:

| Base Public Table | Unlocks What Drill-Down Capabilities? |
| :--- | :--- |
| **`bigquery-public-data.google_trends.international_top_terms`** | Full 5-year weekly historical trend curves per term. |
| **`bigquery-public-data.google_trends.top_terms`** | Granular US Designated Market Area (DMA) metro-level rankings. |
| **`gdelt-bq.gdeltv2.events_partitioned`** | Multi-year historical news event archives (2015–present) and 300+ granular CAMEO subcodes (`010`–`204`). |
| **`gdelt-bq.gdeltv2.gkg_partitioned`** | Full text entity networks: all mentioned persons (`V2Persons`), organizations (`V2Organizations`), and 6-vector tone profiles. |

---

### Step 3: Configure Agent System Instructions & Business Rules
Paste the following domain knowledge and routing rules (from `knowledge_catalog/glossary/`) into the agent's **System Instructions / Business Rules** prompt:

```text
You are a specialized analytical assistant for Google Trends and GDELT 2.0 geopolitical news data.
Always follow this Two-Tier routing hierarchy and domain rules:

1. ROUTING HIERARCHY:
   - DEFAULT (TIER 1): Always prefer the curated views in `trends_gdelt_analytics.vw_*` for standard analytics, recent trends (last 90 days; GKG themes last 30 days), and cross-dataset correlations:
     * Macro correlations (search trends + country news context): Query `trends_gdelt_analytics.vw_topic_news_trends_unified`.
     * Breakout/surging terms & % growth: Query `trends_gdelt_analytics.vw_search_trends_rising`.
     * Daily search rankings & regional spread: Query `trends_gdelt_analytics.vw_search_trends_daily`.
     * Specific news events, actor dyads, or article URLs: Query `trends_gdelt_analytics.vw_gdelt_news_events_daily`.
     * News themes & media outlets (last 30 days ONLY): Query `trends_gdelt_analytics.vw_gdelt_gkg_themes_daily`.
     * Country code conversions: Use `trends_gdelt_analytics.dim_fips_iso_country`.
     * Cross-country term overlap, bilateral comparisons, or graph diffusion networks: Query `trends_gdelt_analytics.trend_gdelt_graph` using GRAPH_TABLE and GQL pattern matching — ONLY if the project has an Enterprise/Enterprise Plus reservation. On on-demand billing GRAPH_TABLE fails; answer the same questions with self-joins or GROUP BY on `trends_gdelt_analytics.vw_search_trends_daily` instead.
   - DRILL-DOWN (TIER 2): Query raw public tables ONLY when explicitly asked for:
     * Historical lookbacks older than 90 days (e.g. "over the last 3 years").
     * News themes or media outlets older than 30 days (from `gdelt-bq.gdeltv2.gkg_partitioned`).
     * US Designated Market Areas / Metro DMAs (from `bigquery-public-data.google_trends.top_terms`).
     * Specific organizations or persons from GKG (`gdelt-bq.gdeltv2.gkg_partitioned`).
     * 5-year historical trend trajectories for a specific term.

2. SAFETY GUARDRAILS FOR RAW TABLES:
   - When querying `google_trends.*`, ALWAYS include `week = (SELECT MAX(week) FROM ... WHERE refresh_date = ...)` unless the user specifically requests historical time-series curves.
   - When querying `gdelt-bq.gdeltv2.*`, ALWAYS filter `_PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` to avoid multi-terabyte full-table scans.
   - NEVER join raw GDELT `ActionGeo_CountryCode` (FIPS) to Trends `country_code` (ISO) directly; ALWAYS join via `trends_gdelt_analytics.dim_fips_iso_country`.
   - GKG V2 fields (`V2Themes`, `V2Persons`, `V2Organizations`) store entries as "Name,charOffset;Name,charOffset;...". ALWAYS strip the offsets, e.g. first entry: SPLIT(SPLIT(V2Persons, ';')[SAFE_OFFSET(0)], ',')[SAFE_OFFSET(0)].

3. SEARCH SCORE VS. RANK:
   - `search_rank` (1–25) is the cross-sectional popularity hierarchy on that day. Sort top-term leaderboards by `search_rank ASC`.
   - `search_score` (0–100) measures interest relative to the term's OWN historical peak share (100 = all-time peak for that specific term).
   - High rank (e.g. #1 "weather") does NOT imply score 100. A lower-ranking term (e.g. #15 "eclipse") CAN have score 100 if it is at its all-time spike.
   - `is_historical_peak = TRUE` indicates `search_score = 100`.

4. GDELT NEWS METRICS & SENTIMENT:
   - `country_avg_tone` / `sentiment_tone`: Sentiment tone from -100 to +100. Real-world values fall between -10 and +10 (< -2.0 is clearly negative, > +2.0 is positive).
   - `country_avg_goldstein` / `goldstein_scale`: Theoretical stability impact (-10.0 extreme conflict/destabilizing to +10.0 high cooperation).
   - `conflict_event_share_pct`: Share of daily events classified as Verbal Conflict (QuadClass 3) or Material Conflict (QuadClass 4).
   - `country_daily_media_mentions`: Volume of media attention pulse across news articles.

5. PARTITION & DATE PRUNING:
   - Always filter `date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` or `report_date >= ...`.
   - Google Trends snapshots lag 1–2 days; pin the latest snapshot via:
     QUALIFY date = MAX(date) OVER ()
```

---

### Step 4: Add Verified / Golden Queries
Copy the few-shot query examples from [`bigquery/06_golden_agent_queries.sql`](./bigquery/06_golden_agent_queries.sql) into the agent's **Verified Queries** configuration (the graph example lives in [`bigquery/07_graph_golden_query.sql`](./bigquery/07_graph_golden_query.sql) — add it only if your project has an Enterprise reservation):

* **Top Search Terms Today:**
  ```sql
  SELECT search_term, search_rank, search_score, is_historical_peak
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE country_code = 'GB' AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
  QUALIFY date = MAX(date) OVER ()
  ORDER BY search_rank ASC LIMIT 10;
  ```
* **Search Surges vs. Negative News Context:**
  ```sql
  SELECT date, country_name, country_avg_tone, dominant_news_category, search_term, search_rank, search_score
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE date >= DATE_SUB(CURRENT_DATE(), INTERVAL 14 DAY)
    AND country_avg_tone < -2.0 AND conflict_event_share_pct > 30.0
  ORDER BY conflict_event_share_pct DESC, search_rank ASC LIMIT 20;
  ```
* **Historical Peak Breakouts:**
  ```sql
  SELECT date, search_term, search_rank, search_score
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE country_code = 'US' AND is_historical_peak = TRUE AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
  ORDER BY date DESC, search_rank ASC;
  ```
* **Graph Traversal / Bilateral Overlap (GQL via GRAPH_TABLE — requires Enterprise edition reservation):**
  ```sql
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
  ```

---

### Step 5: Test Natural Language Queries

Test the agent with these prompts directly in the BigQuery chat interface:

1. *"What are the top 10 search terms in Great Britain in the latest snapshot?"*
2. *"Which search terms reached their all-time peak popularity (score 100) in the United States over the last week?"*
3. *"Show me countries experiencing negative news sentiment (Tone < -2.0) with high conflict share, and what people are searching for in those countries."*
4. *"Which search queries trended in 3 or more countries simultaneously over the past 7 days?"*
5. *"Why does a term with rank #12 have a score of 100 while rank #1 has a score of 70?"*
6. *"Which search terms charted in the top 10 in both the UK and France on the same day? Compare their ranks."*

---

## 📊 Summary of Curated Views & Graphs

| View / Graph Name | Type | Primary Source | Key Metrics & Dimensions |
| :--- | :--- | :--- | :--- |
| **`vw_search_trends_daily`** | View | `google_trends.international_top_terms` | `snapshot_date`, `country_code` (ISO), `search_term`, `rank`, `search_score` (pinned to latest week), `is_historical_peak`. |
| **`vw_search_trends_rising`** | View | `google_trends.international_top_rising_terms` | `snapshot_date`, `country_code`, `search_term`, `max_percent_gain`, `avg_percent_gain`. |
| **`vw_gdelt_news_events_daily`** | View | `gdelt-bq.gdeltv2.events_partitioned` | `report_date`, `country_code` (mapped ISO), `event_category` (decoded CAMEO), `quad_class_name`, `goldstein_scale`, `sentiment_tone`, `source_article_url`. |
| **`vw_topic_news_trends_unified`** | View | *Unified Analytics Mart* | **Primary Agent Mart:** Correlates daily search terms, rank, and score with country news sentiment tone, Goldstein conflict score, and dominant news categories. |
| **`trend_gdelt_graph`** | Property Graph | `node_countries`, `node_search_terms`, `edge_trended_in` | **Graph Model (Preview):** Nodes for `Country` and `SearchTerm`, edges for `TRENDED_IN`, queryable via ISO GQL / `GRAPH_TABLE`. Requires an Enterprise/Enterprise Plus reservation. |
