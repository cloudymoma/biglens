# BigLens

[![Build](https://github.com/cloudymoma/biglens/actions/workflows/build.yml/badge.svg)](https://github.com/cloudymoma/biglens/actions/workflows/build.yml)

English | [简体中文](README_cn.md)

A real-time BigQuery observability dashboard. BigLens queries BigQuery's `INFORMATION_SCHEMA` views to surface storage costs, compute slot usage, per-user spend, and optimization recommendations — all from a single dark-themed web UI.

![Storage Analysis](miscs/biglens_1.png)

![Compute Analysis](miscs/biglens_2.png)

## Quick Start

### Prerequisites

- **Go 1.22+**
- **Node.js 20+** and npm
- **Google Cloud credentials** with BigQuery metadata access (`roles/bigquery.resourceViewer`)

### 1. Configure

Copy the template and fill in your GCP project ID:

```bash
cp conf.yaml.template conf.yaml
```

Edit `conf.yaml`:

```yaml
server:
  port: 1983
  mode: "debug"        # "debug" or "release"

bigquery:
  project_id: "your-gcp-project-id"
  credentials_path: "" # optional, falls back to GOOGLE_APPLICATION_CREDENTIALS
```

| Field | Description |
|---|---|
| `server.port` | HTTP port for the dashboard (default `1983`) |
| `server.mode` | `debug` for verbose logging, `release` for production |
| `bigquery.project_id` | Your GCP project ID |
| `bigquery.credentials_path` | Path to a service account JSON key. Leave empty to use Application Default Credentials (`gcloud auth application-default login`) |

### 2. Build & Launch

```bash
make serve
```

This single command:
1. Installs frontend dependencies and builds the React app
2. Copies the static bundle into the Go server
3. Compiles the Go binary
4. Starts the server

Open **http://localhost:1983** in your browser.

### Other Make Targets

```bash
make build-frontend   # Build only the React frontend
make build-backend    # Build only the Go binary
make build-all        # Build both without launching
make clean            # Remove build artifacts
```

### Development Mode

To run the frontend with hot reload while the backend serves API requests:

```bash
# Terminal 1: start the Go backend
make build-backend && ./bin/biglens-server

# Terminal 2: start Vite dev server (proxies /api/* to port 1983)
cd frontend && npm run dev
```

## Dashboards

BigLens provides four dashboard views, each powered by `INFORMATION_SCHEMA` queries:

| Dashboard | Widgets |
|---|---|
| **Storage** | Logical vs. physical billing simulator, active/long-term donut chart, top 10 heaviest tables |
| **Compute** | Concurrent slot usage area chart (JOBS_TIMELINE), top slot-consuming jobs |
| **Cost** | On-demand cost extrapolation ($6.25/TiB), spend-by-user treemap |
| **Insights** | Active BigQuery recommendations feed |

### Global Filters

All dashboards share a sidebar filter panel:

- **Region** — Searchable dropdown for the BQ region (defaults to `us`)
- **Dataset** / **Table** — Scope metrics to a specific dataset or table
- **User Email** — Isolate metrics to a specific user or service account
- **Time Range** — 24h, 7d, 30d, or 90d lookback

## Dataplex Knowledge Catalog

The **Dataplex** view turns your data catalog into an interactive 2D/3D graph
you can search, explore, and edit. It is built on the
[Open Knowledge Format (OKF)](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md):
a git-friendly bundle of markdown files where each file is a *concept* (a node)
and markdown links between them are *edges*.

- **Graph** — force-directed, toggle between **2D** and **3D**. Nodes are
  colored by their `type` (BigQuery Table / View / Dataset, Glossary Term,
  Metric, …); edges are relationships.
- **Bottom tabs** — **Search** (by name, type, or tag), **Details** (frontmatter,
  body, and connections of the selected node), and **Edit** (create, update, or
  delete concepts — written straight to the markdown bundle).
- **Import from Dataplex** — pulls live entries from the Dataplex Universal
  Catalog (`SearchEntries`) into the OKF bundle and wires two kinds of edges:
  - **Containment** — `dataset ⊃ table`, derived from the entry hierarchy.
  - **Lineage** — source → derived table ETL data flow, from the
    [Data Lineage API](https://cloud.google.com/data-catalog/docs/concepts/about-data-lineage)
    (best-effort; if the API is disabled or untracked, import still succeeds
    with containment edges and reports lineage as skipped).
  Edits stay local in the bundle (reversible via git); they are **not** written
  back to Dataplex.

Configure the bundle and import source in `conf.yaml`:

```yaml
catalog:
  bundle_path: "okf-bundle"   # directory holding the OKF markdown bundle
  dataplex:
    project_id: ""            # defaults to bigquery.project_id when empty
    location: "global"        # Dataplex search location, e.g. "global" or "us"
  lineage_location: "us"      # Data Lineage API region (regional, not "global")
```

The runtime bundle `okf-bundle/` is git-ignored (it may hold imported
metadata). A reference sample ships in `okf-bundle.sample/` — copy it in to see
the graph before importing:

```bash
cp -r okf-bundle.sample/. okf-bundle/
```

Importing requires `roles/dataplex.catalogViewer`; lineage edges additionally
require the Data Lineage API enabled and `roles/datalineage.viewer`.

## BigQuery Open Data

The **BigQuery Open Data** view hosts dashboards built on
[Google Cloud public datasets](https://cloud.google.com/bigquery/public-data).
Queries run in your configured project (billed there) against
`bigquery-public-data`; every query filters on the partition key to keep scans
small, and results are served through the same 10-minute cache as the other
dashboards.

### Google Trends

The first dashboard, powered by `bigquery-public-data.google_trends`
(`international_top_terms` / `international_top_rising_terms`):

| Widget | Description |
|---|---|
| **Top Terms Leaderboard** | Top 25 terms per country with inline score bars |
| **Term Cloud** | Tag cloud sized by score, top-5 ranks highlighted |
| **Surging Terms** | Top 10 rising queries by `percent_gain`, plus a breakdown table |
| **Cross-Country Interest** | A term's latest score wherever it charts in the top 25 |
| **Interest Over Time** | 5-year weekly history, compare up to 5 terms, drag to zoom |

Filters: country, snapshot date (`refresh_date` partition), and a term search
over the day's charts. Clicking any term focuses the geographic view and adds
it to the comparison chart.

#### Reading the numbers

The dashboard surfaces three metrics straight from the dataset — they measure
different things, so they don't move together:

- **Rank (1–25)** — the term's position in the country's daily top chart,
  ordered by raw search volume for that snapshot. This is what sorts the
  leaderboard.
- **Score (0–100)** — Google's *relative* search-interest index for the latest
  week: each term is normalized against its own all-time peak, where 100 means
  "this week is (or ties) the term's peak popularity". The dataset reports it
  per region, so BigLens averages it across all of a country's regions and
  rounds to an integer (`CAST(COALESCE(AVG(score), 0) AS INT64)`; a NULL score
  counts as 0). The inline bar next to each leaderboard row visualizes this
  value.
- **Gain (%)** — for rising terms only: the week-over-week percentage increase
  in search volume (`percent_gain`). A brand-new breakout query can show gains
  of several thousand percent.

Because rank reflects *absolute daily volume* while score reflects *interest
relative to the term's own history*, a #14 term can score 100 (it just hit its
all-time high) while the #1 term scores lower (huge volume, but past its peak
week). The leaderboard therefore intentionally sorts by rank, not score.

### GDELT News Pulse

A real-time global news sentiment and geopolitical monitoring dashboard,
powered by the [GDELT Project](https://www.gdeltproject.org/) 2.0 tables
`gdelt-bq.gdeltv2.events_partitioned` and `gdelt-bq.gdeltv2.gkg_partitioned`.
GDELT machine-reads news media worldwide in 100+ languages and refreshes
every 15 minutes; BigLens queries the partitioned tables directly (no
intermediate tables or views).

| Widget | Description |
|---|---|
| **Global Event Hotspots** | World map of the top 500 locations — bubble size = event count, color = average tone |
| **Sentiment Gauge** | Weighted global average tone for the selected range |
| **Volume & Tone Trend** | Daily event count (bars) vs. daily average tone (line) |
| **Cooperation vs Conflict** | Donut of the event mix across the four QuadClasses |
| **Risk Matrix** | Event types plotted by Goldstein score (x) vs. activity (y, log) — lower-right = high-volume destabilizing |
| **Conflict Categories** | Event counts per CAMEO conflict root code (Protest, Coerce, Assault, Fight, …) |
| **Breaking Conflict Reports** | Top 50 most-mentioned conflict articles, one row per source URL |
| **Trending Themes** | Treemap of the top 50 GKG themes by article count |
| **Most Covered People / Leading Media Sources** | Top 20 people and top 10 outlets (colored by average tone) |

Filters: quick ranges (3 / 7 / 30 days) plus a custom UTC date range. The
event panels accept up to 90 days; the theme/entity (GKG) panel up to 30 days
and loads independently, so the fast event charts never wait for it.

#### Understanding the data

GDELT is an index of *news coverage*, not a registry of verified incidents.
Each row is one machine-coded "who did what to whom" statement extracted from
a news report, so the same real-world incident covered by many outlets
produces many rows — counts measure **media attention**, which is exactly
what a news-pulse dashboard should show.

- **Date reported** — the UTC date GDELT ingested the report
  (`_PARTITIONDATE`), not the date the underlying event happened. This is the
  right axis for "what is the news covering right now", and it is also the
  table's partition key, so every query prunes to only the selected days.
- **Tone** — the average emotional tone of the language in the articles
  describing an event, from GDELT's sentiment engine. The scale is
  −100…+100 but real-world values almost always fall in −10…+10; below −2
  reads as clearly negative coverage, above +2 as positive.
- **Goldstein scale (−10…+10)** — a standard political-science score of an
  event *type*'s theoretical impact on a country's stability (e.g. "Provide
  aid" is strongly positive, "Fight" strongly negative). It is fixed per
  CAMEO event type — it rates the kind of action, not the individual article.
- **QuadClass (1–4)** — GDELT's coarsest event grouping: Verbal Cooperation,
  Material Cooperation, Verbal Conflict, Material Conflict. Classes 3–4 drive
  the "Conflict Share" metric and the breaking-reports list.
- **CAMEO root codes ('01'–'20')** — the 20 top-level event categories of the
  [CAMEO taxonomy](http://data.gdeltproject.org/documentation/CAMEO.Manual.1.1b3.pdf)
  (Appeal, Consult, Threaten, Protest, Fight, …). Codes '10'+ are the
  conflict side. The API returns raw codes; the UI maps them to labels.
- **Mentions** — how many times an event was mentioned across all monitored
  documents (`NumMentions`); the prominence signal that ranks the
  breaking-reports table.
- **Themes / People (GKG)** — from the Global Knowledge Graph, which tags
  every *article* with themes (e.g. `PROTEST`, `WB_2670_JOBS`) and named
  people. Their weights are **article counts**: an article mentioning a theme
  ten times still counts once, so long articles don't dominate the treemap.
- **Media source tone** — the average document tone (first field of the GKG
  `V2Tone` composite) across everything an outlet published in the range.

#### How the numbers are calculated

- **Weighted averages, never averages of averages.** BigQuery returns one
  row per (day × QuadClass × event type) group with that group's `AVG` and
  `COUNT`; the Go backend combines them as `Σ(avg×n)/Σ(n)`, which is
  mathematically identical to averaging the raw rows. A plain mean of group
  averages would let a 10-event group distort the global tone as much as a
  100,000-event group.
- **Hotspots** are event coordinates rounded to a 0.1° grid (~11 km) and
  aggregated per cell; the map shows the 500 busiest cells.
- **Breaking reports** are deduplicated by `SOURCEURL` (keeping each
  article's highest-mention event row), because GDELT emits several event
  rows per article and one big story would otherwise flood the top 50.
- **Cost guardrails**: native `DATE` parameters against the partition key,
  hard span caps (90 / 30 days), server-side `GROUP BY + LIMIT`, the shared
  10-minute cache, and request coalescing (`singleflight`) so concurrent
  identical requests trigger a single BigQuery job. A full cache miss on the
  default 3-day window scans well under 1 GB — a fraction of a cent at
  on-demand pricing.

### Adding another public dataset

1. Backend: create `backend/opendata_<name>.go` (typed rows + `BQClient`
   methods) and `backend/opendata_<name>_handlers.go`, routed under
   `/api/opendata/<name>/*`.
2. Frontend: build the dashboard component in `frontend/src/opendata/` and
   register it in `frontend/src/opendata/registry.tsx` — the sidebar entry,
   header, and routing come for free.

## Architecture

```
frontend/            React 19 + Vite + ECharts + Tailwind CSS v4
  catalog/           Dataplex graph view (react-force-graph 2D/3D, three.js)
backend/             Go net/http server
  main.go            HTTP server, routing, middleware
  bigquery.go        All INFORMATION_SCHEMA queries
  handlers.go        Dashboard endpoints with errgroup concurrency
  catalog_handlers.go  OKF graph/search/concept/import endpoints
  okf.go             OKF bundle engine (parse, graph, read/write concepts)
  catalog_dataplex.go  Dataplex SearchEntries -> OKF concept mapping
  cache.go           In-memory TTL cache (sync.Map, 10-min TTL)
  filters.go         Global filter parsing & SQL clause builders
  config.go          YAML config loader
```

The backend uses `errgroup` to run all widget queries for a dashboard in parallel, and caches results for 10 minutes to reduce BigQuery API calls.

## BigQuery INFORMATION_SCHEMA

BigLens is built entirely on BigQuery's `INFORMATION_SCHEMA` — a set of read-only system views that expose metadata about your BigQuery resources. These views provide storage metrics, job execution history, slot utilization, and optimization recommendations, all queryable with standard SQL.

![BigQuery INFORMATION_SCHEMA Guide](miscs/bq_meta_guide.png)

For full documentation, see the official Google Cloud reference:
[BigQuery INFORMATION_SCHEMA Introduction](https://cloud.google.com/bigquery/docs/information-schema-intro)
