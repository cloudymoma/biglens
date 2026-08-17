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
│   ├── 06_golden_agent_queries.sql              # Few-shot verified queries for the AI agent (Tier 1 + Tier 2)
│   ├── 07_graph_golden_query.sql                # Graph GQL example (requires Enterprise edition)
│   └── 08_views_tier2_raw.sql                   # Tier 2 raw drill-down proxy views (full history, US DMA, hourly real-time, rising, GKG entities)
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

### Step 2: Configure Data Sources (Two-Tier Semantic Layer)
In the BigQuery Agent configuration, add the **`trends_gdelt_analytics` dataset** (or select the curated views and tables below).

> [!IMPORTANT]
> **Do NOT add external public datasets (`gdelt-bq.*` or `bigquery-public-data.*`) directly as Agent Data Sources.** BigQuery Conversational Analytics enforces cross-organization resource boundaries and requires all attached data sources to reside in your own Google Cloud project. That is exactly why the Tier 2 **proxy views** below exist: they live in `trends_gdelt_analytics` (so the agent attaches them without cross-org errors) while their SQL still resolves to the public source tables at query time.

#### 🔹 Tier 1: Curated Views (Default — fast, cheap, recent window)

| Table / View / Graph | Purpose & When the Agent Uses It |
| :--- | :--- |
| **`vw_topic_news_trends_unified`** *(Primary Mart)* | **Macro Correlation:** Default view for questions correlating search trends with country news sentiment, Goldstein stability, and conflict share. |
| **`vw_search_trends_rising`** | **Surging & Breakout Queries:** Breakout search terms with week-over-week percentage gain (`percent_gain`). Use for questions like *"Which terms are surging in Japan/Tokyo?"*. |
| **`vw_search_trends_daily`** | **Rankings & Regional Spread:** Daily top 25 rankings, regional coverage counts, and peak flags. |
| **`vw_gdelt_news_events_daily`** | **Granular Event Citations:** Specific actor dyads (`primary_actor`, `secondary_actor`), CAMEO categories, and source article URLs (`source_article_url`). |
| **`vw_gdelt_gkg_themes_daily`** | **Thematic Analysis:** Primary GKG news themes, media source domains, and tone vectors (last 30 days). |
| **`dim_fips_iso_country`** | **Country Dimension:** FIPS 10-4 ↔ ISO 3166-1 country code lookup table. |
| **`trend_gdelt_graph`** *(Property Graph - Preview)* | **Graph Pattern Matching:** Native property graph for cross-country term overlap and bilateral comparisons via ISO GQL / `GRAPH_TABLE` (requires Enterprise reservation). |

#### 🔹 Tier 2: Raw Drill-Down Proxy Views (On explicit request — deep history & fine granularity)

| Proxy View | Public Source Table | Drill-Down Capability Unlocked |
| :--- | :--- | :--- |
| **`vw_raw_trends_international_history`** | `bigquery-public-data.google_trends.international_top_terms` | Full ~5-year weekly historical trend curves per term/country/region. |
| **`vw_raw_trends_international_rising_history`** | `bigquery-public-data.google_trends.international_top_rising_terms` | Region-level rising/breakout terms with per-region `percent_gain` and weekly history. |
| **`vw_raw_trends_us_dma`** | `bigquery-public-data.google_trends.top_terms` | Granular US Designated Market Area (Nielsen metro) rankings. |
| **`vw_raw_trends_us_dma_rising`** | `bigquery-public-data.google_trends.top_rising_terms` | US metro-level rising/breakout terms with per-DMA `percent_gain`. |
| **`vw_raw_trends_us_hourly`** | `bigquery-public-data.google_trends_hourly.top_terms_hourly` | **Real-time:** intraday US top 25 per DMA, several snapshots/day (~30-day retention, ~1-year weekly history). Freshest source — daily tables lag 1–2 days. |
| **`vw_raw_trends_us_hourly_rising`** | `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly` | **Real-time:** intraday US breakout terms per DMA with `percent_gain`. |
| **`vw_raw_gdelt_events_archive`** | `gdelt-bq.gdeltv2.events_partitioned` | Multi-year news event archive (Feb 2015 – present), full 300+ CAMEO subcodes, actor type codes, exact coordinates. |
| **`vw_raw_gdelt_gkg_entities_archive`** | `gdelt-bq.gdeltv2.gkg_partitioned` | Per-article entity lists (`persons`, `organizations`, `themes` as clean arrays) and full tone vectors, rolling 2-year window (hard cost bound baked into the view). |

> [!NOTE]
> If your agent's data-source count is limited, prioritize Tier 1 plus the two GDELT archive views and `vw_raw_trends_us_hourly` — they unlock capabilities Tier 1 cannot answer at all (deep history, entities, and real-time freshness; the daily Trends tables lag 1–2 days). As an additional cost backstop for raw-view queries, consider setting `maximum_bytes_billed` (project default query limit) or custom query quotas.

---

### Step 3: Configure Agent System Instructions & Business Rules
Paste the following domain knowledge and routing rules (from `knowledge_catalog/glossary/`) into the agent's **System Instructions / Business Rules** prompt:

```text
You are a specialized analytical assistant for Google Trends and GDELT 2.0 geopolitical news data.
Always follow this Two-Tier routing hierarchy and domain rules:

1. ROUTING HIERARCHY — TIER 1 (CURATED, DEFAULT):
   Always prefer the Tier 1 curated views for standard analytics, recent trends (last 90 days; GKG themes last 30 days), and cross-dataset correlations:
   - For macro correlations (search trends + country news context): Query `trends_gdelt_analytics.vw_topic_news_trends_unified`.
   - For breakout/surging terms & % growth (e.g. rising queries in Japan/US): Query `trends_gdelt_analytics.vw_search_trends_rising`.
   - For daily search rankings & regional spread: Query `trends_gdelt_analytics.vw_search_trends_daily`.
   - For specific news events, actor dyads, or article URLs: Query `trends_gdelt_analytics.vw_gdelt_news_events_daily`.
   - For news themes & media outlets (last 30 days ONLY): Query `trends_gdelt_analytics.vw_gdelt_gkg_themes_daily`.
   - For country code conversions: Use `trends_gdelt_analytics.dim_fips_iso_country`.
   - Cross-country term overlap, bilateral comparisons, or graph diffusion networks: Query `trends_gdelt_analytics.trend_gdelt_graph` using GRAPH_TABLE and GQL pattern matching — ONLY if the project has an Enterprise/Enterprise Plus reservation. On on-demand billing GRAPH_TABLE fails; answer the same questions with self-joins or GROUP BY on `trends_gdelt_analytics.vw_search_trends_daily` instead.

2. ROUTING HIERARCHY — TIER 2 (RAW DRILL-DOWN, ON EXPLICIT REQUEST ONLY):
   Use the Tier 2 raw proxy views ONLY when the user explicitly asks for data outside the Tier 1 windows or granularity:
   - Multi-year historical trend trajectories per term: Query `trends_gdelt_analytics.vw_raw_trends_international_history`.
   - Region-level rising terms & per-region percent gains: Query `trends_gdelt_analytics.vw_raw_trends_international_rising_history`.
   - US metro / Designated Market Area (DMA) breakdowns: Query `trends_gdelt_analytics.vw_raw_trends_us_dma` (top terms) or `trends_gdelt_analytics.vw_raw_trends_us_dma_rising` (breakouts with percent_gain).
   - News events older than 90 days, full CAMEO subcodes, or actor type codes: Query `trends_gdelt_analytics.vw_raw_gdelt_events_archive`.
   - Person/organization entity mentions, or themes older than 30 days: Query `trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive` (rolling 2-year window).

3. REAL-TIME EXCEPTION — "RIGHT NOW / TODAY" QUESTIONS (US ONLY):
   The daily views lag 1-2 days. For US questions about the current moment or today ("what is trending right now", "what is spiking today"), route PROACTIVELY to the intraday hourly views (several snapshots per day, ~30-day snapshot retention):
   - Current US top terms: `trends_gdelt_analytics.vw_raw_trends_us_hourly`.
   - Current US breakouts: `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`.
   For non-US "right now" questions, use the latest Tier 1 snapshot and state the 1-2 day lag.

4. GUARDRAILS FOR TIER 2 RAW VIEWS:
   - ALWAYS filter `partition_date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` (or an explicit BETWEEN range) on the GDELT archive views — they span many years and terabytes.
   - On `vw_raw_trends_*`, each snapshot (snapshot_date, or snapshot_time on hourly views) carries the FULL weekly history (~5 years daily, ~1 year hourly):
     * For historical curves: pin `snapshot_date = (SELECT MAX(snapshot_date) FROM <view>)` and scan `week`. NEVER range over snapshots for history — that averages overlapping histories into garbage.
     * For current single-snapshot values: pin the snapshot as above AND `week = MAX(week)`.
     * On hourly views pin `snapshot_time = (SELECT MAX(snapshot_time) FROM <view>)` (DATETIME, not DATE).
   - Rank vs breadth on DMA/region views: aggregate with MIN(rank), MAX(percent_gain), COUNT(DISTINCT dma_name / region_name) for national/country rollups.
   - In `vw_raw_gdelt_gkg_entities_archive`, `persons`/`organizations`/`themes` are ARRAY<STRING>; filter with `'Name' IN UNNEST(persons)`.
   - NEVER join `fips_country_code` to ISO country codes directly; the archive views already expose mapped ISO `country_code`.

5. SEARCH SCORE VS. RANK:
   - `search_rank` (1–25) is the cross-sectional popularity hierarchy on that day. Sort top-term leaderboards by `search_rank ASC`.
   - `search_score` (0–100) measures interest relative to the term's OWN historical peak share (100 = all-time peak for that specific term).
   - High rank (e.g. #1 "weather") does NOT imply score 100. A lower-ranking term (e.g. #15 "eclipse") CAN have score 100 if it is at its all-time spike.
   - `is_historical_peak = TRUE` indicates `search_score = 100`.

6. GDELT NEWS METRICS & SENTIMENT:
   - `country_avg_tone` / `sentiment_tone`: Sentiment tone from -100 to +100. Real-world values fall between -10 and +10 (< -2.0 is clearly negative, > +2.0 is positive).
   - `country_avg_goldstein` / `goldstein_scale`: Theoretical stability impact (-10.0 extreme conflict/destabilizing to +10.0 high cooperation).
   - `conflict_event_share_pct`: Share of daily events classified as Verbal Conflict (QuadClass 3) or Material Conflict (QuadClass 4).
   - `country_daily_media_mentions`: Volume of media attention pulse across news articles.

7. PARTITION & DATE PRUNING:
   - Always filter `date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)`, `snapshot_date >= ...`, or `report_date >= ...`.
   - Google Trends snapshots lag 1–2 days; pin the latest snapshot via:
     QUALIFY date = MAX(date) OVER ()   (or snapshot_date = MAX(snapshot_date) OVER ())
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
* **Tier 2 Drill-Down — 5-Year Trend Trajectory (pin the snapshot, scan the weeks):**
  ```sql
  SELECT search_term, week, CAST(AVG(search_score) AS INT64) AS avg_weekly_score
  FROM `trends_gdelt_analytics.vw_raw_trends_international_history`
  WHERE snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_international_history`)
    AND country_code = 'GB' AND rank = 1
  GROUP BY search_term, week
  ORDER BY week ASC;
  ```
* **Tier 2 Real-Time — What Is Trending in the US Right Now (pin snapshot_time AND week):**
  ```sql
  WITH latest_snapshot AS (
    SELECT * FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`
    WHERE snapshot_time = (SELECT MAX(snapshot_time) FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`)
    QUALIFY week = MAX(week) OVER ()
  )
  SELECT search_term, MIN(rank) AS best_rank, CAST(AVG(search_score) AS INT64) AS avg_dma_score,
         COUNT(DISTINCT dma_name) AS active_dma_count
  FROM latest_snapshot
  GROUP BY search_term
  ORDER BY best_rank ASC LIMIT 25;
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
7. *"Show me the 5-year search interest curve for the UK's current #1 term."* (Tier 2 drill-down)
8. *"What were the most heavily covered protest events in France in early 2023, with article links?"* (Tier 2 drill-down)
9. *"What are Americans searching for right now, and which searches are spiking at this moment?"* (Tier 2 real-time hourly)
10. *"In which US metro areas is today's top rising term breaking out the hardest?"* (Tier 2 DMA rising)

---

## 📊 Summary of Curated Views & Graphs

| View / Graph Name | Type | Primary Source | Key Metrics & Dimensions |
| :--- | :--- | :--- | :--- |
| **`vw_search_trends_daily`** | View | `google_trends.international_top_terms` | `snapshot_date`, `country_code` (ISO), `search_term`, `rank`, `search_score` (pinned to latest week), `is_historical_peak`. |
| **`vw_search_trends_rising`** | View | `google_trends.international_top_rising_terms` | `snapshot_date`, `country_code`, `search_term`, `max_percent_gain`, `avg_percent_gain`. |
| **`vw_gdelt_news_events_daily`** | View | `gdelt-bq.gdeltv2.events_partitioned` | `report_date`, `country_code` (mapped ISO), `event_category` (decoded CAMEO), `quad_class_name`, `goldstein_scale`, `sentiment_tone`, `source_article_url`. |
| **`vw_topic_news_trends_unified`** | View | *Unified Analytics Mart* | **Primary Agent Mart:** Correlates daily search terms, rank, and score with country news sentiment tone, Goldstein conflict score, and dominant news categories. |
| **`trend_gdelt_graph`** | Property Graph | `node_countries`, `node_search_terms`, `edge_trended_in` | **Graph Model (Preview):** Nodes for `Country` and `SearchTerm`, edges for `TRENDED_IN`, queryable via ISO GQL / `GRAPH_TABLE`. Requires an Enterprise/Enterprise Plus reservation. |
| **`vw_raw_trends_international_history`** | View (Tier 2) | `google_trends.international_top_terms` | **Drill-Down:** Unaggregated ~5-year weekly score history per term/country/region (`snapshot_date`, `week`, `region_name`, `search_score`). |
| **`vw_raw_trends_international_rising_history`** | View (Tier 2) | `google_trends.international_top_rising_terms` | **Drill-Down:** Region-level rising terms with per-region `percent_gain` and weekly history. |
| **`vw_raw_trends_us_dma`** | View (Tier 2) | `google_trends.top_terms` | **Drill-Down:** US Nielsen DMA metro-level top 25 (`dma_name`, `dma_id`) with weekly history. |
| **`vw_raw_trends_us_dma_rising`** | View (Tier 2) | `google_trends.top_rising_terms` | **Drill-Down:** US DMA-level breakout terms with `percent_gain`. |
| **`vw_raw_trends_us_hourly`** | View (Tier 2) | `google_trends_hourly.top_terms_hourly` | **Real-Time:** Intraday US top 25 per DMA (`snapshot_time` DATETIME); several snapshots/day, ~30-day retention, ~1-year weekly history. |
| **`vw_raw_trends_us_hourly_rising`** | View (Tier 2) | `google_trends_hourly.top_rising_terms_hourly` | **Real-Time:** Intraday US breakouts per DMA with `percent_gain`. |
| **`vw_raw_gdelt_events_archive`** | View (Tier 2) | `gdelt-bq.gdeltv2.events_partitioned` | **Drill-Down:** Full event archive since Feb 2015 (`partition_date`, `global_event_id`, full CAMEO `cameo_event_code`/`cameo_base_code`, actor type codes, mapped ISO `country_code`). |
| **`vw_raw_gdelt_gkg_entities_archive`** | View (Tier 2) | `gdelt-bq.gdeltv2.gkg_partitioned` | **Drill-Down:** Per-article entity arrays (`persons`, `organizations`, `themes`) + full tone vector; rolling 2-year hard bound. |
