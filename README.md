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
