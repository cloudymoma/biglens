#!/usr/bin/env bash
# =============================================================================
# Google Trends & GDELT Analytics Agent Setup Script
# Initializes BigQuery Dataset, Curated Views, Property Graph, and Knowledge Catalog
#
# The deployment location (US) and dataset name (trends_gdelt_analytics) are
# fixed: every SQL file in bigquery/ references the dataset by that name, and
# 01_dataset_setup.sql pins location = "US" (the BigQuery public datasets
# gdelt-bq and bigquery-public-data live in the US multi-region, so the
# curated layer must too).
#
# Safe to re-run: all DDL is CREATE OR REPLACE / IF NOT EXISTS, so re-running
# picks up SQL changes and re-materializes the property-graph snapshot tables
# (node_*/edge_*) from the latest data. Re-run it (or schedule it) whenever
# you want the graph refreshed.
# =============================================================================

set -euo pipefail

# Color formatting
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

LOCATION="US"
DATASET_ID="trends_gdelt_analytics"

echo -e "${BOLD}${BLUE}==================================================================${NC}"
echo -e "${BOLD}${BLUE}   Google Trends & GDELT Conversational Analytics Agent Setup     ${NC}"
echo -e "${BOLD}${BLUE}==================================================================${NC}\n"

show_usage() {
  echo -e "${BOLD}Usage:${NC} $0 [PROJECT_ID]"
  echo -e "\n${BOLD}Description:${NC}"
  echo -e "  Deploys the BigQuery dataset, semantic SQL views, mapping tables, property graph,"
  echo -e "  and synchronizes the OKF knowledge catalog for the Trends & GDELT Conversational Agent."
  echo -e "\n${BOLD}Arguments:${NC}"
  echo -e "  ${GREEN}PROJECT_ID${NC}   (Optional) Google Cloud project ID. If omitted, the script automatically"
  echo -e "               uses the active project from: gcloud config get-value project"
  echo -e "\n${BOLD}Fixed Configurations:${NC}"
  echo -e "  • ${BOLD}Location${NC}       : ${GREEN}US${NC} (fixed — source public datasets 'google_trends' and 'gdelt-bq' are in US multi-region)"
  echo -e "  • ${BOLD}Dataset ID${NC}     : ${GREEN}trends_gdelt_analytics${NC} (fixed — referenced by name across all SQL views)"
  echo -e "\n${BOLD}Examples:${NC}"
  echo -e "  $0                      # Use active gcloud project"
  echo -e "  $0 my-target-gcp-proj   # Deploy to specific project"
  echo -e "  $0 --help               # Show this help message\n"
}

# Check for help flags
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  show_usage
  exit 0
fi

# Check for unexpected extra arguments
if [[ $# -gt 1 ]]; then
  echo -e "${RED}Error: Too many arguments provided ($# arguments).${NC}"
  echo -e "${YELLOW}Note: Location ('US') and Dataset ('trends_gdelt_analytics') are fixed and cannot be passed as arguments.${NC}\n"
  show_usage
  exit 1
fi

# -----------------------------------------------------------------------------
# 1. Project & Environment Discovery
# -----------------------------------------------------------------------------
PROJECT_ID="${1:-$(gcloud config get-value project 2>/dev/null || true)}"

if [[ -z "${PROJECT_ID}" || "${PROJECT_ID}" == "(unset)" ]]; then
  echo -e "${RED}Error: No Google Cloud project specified and no active project found in gcloud.${NC}\n"
  show_usage
  exit 1
fi

echo -e "${GREEN}✓ Target Project :${NC} ${BOLD}${PROJECT_ID}${NC}"
echo -e "${GREEN}✓ Location       :${NC} ${BOLD}${LOCATION}${NC} (fixed; source public datasets are US-multi-region)"
echo -e "${GREEN}✓ Target Dataset :${NC} ${BOLD}${DATASET_ID}${NC} (fixed; referenced by name in bigquery/*.sql)\n"

# Check gcloud and bq
command -v gcloud >/dev/null 2>&1 || { echo -e "${RED}Error: 'gcloud' CLI is required but not installed.${NC}"; exit 1; }
command -v bq >/dev/null 2>&1 || { echo -e "${RED}Error: 'bq' CLI is required but not installed.${NC}"; exit 1; }

# Run a SQL file through bq; on failure, show bq's output (bq prints query
# errors to stdout) and abort.
run_sql() {
  local sql_file="$1"
  local output
  if ! output="$(bq query --use_legacy_sql=false --project_id="${PROJECT_ID}" --location="${LOCATION}" < "${sql_file}" 2>&1)"; then
    echo -e "${RED}Error: deployment of ${sql_file} failed:${NC}"
    echo "${output}"
    exit 1
  fi
}

# -----------------------------------------------------------------------------
# 2. BigQuery Deployment
# -----------------------------------------------------------------------------
echo -e "${BLUE}${BOLD}[1/3] Setting up BigQuery dataset and semantic views...${NC}"

echo -e "  → Deploying 01_dataset_setup.sql (schema & FIPS→ISO country dimension)..."
run_sql bigquery/01_dataset_setup.sql

echo -e "  → Deploying 02_views_search_trends.sql (curated Google Trends views)..."
run_sql bigquery/02_views_search_trends.sql

echo -e "  → Deploying 03_views_gdelt_events.sql (curated GDELT news events & GKG themes)..."
run_sql bigquery/03_views_gdelt_events.sql

echo -e "  → Deploying 04_views_trends_gdelt_unified.sql (unified topic-news analytical mart)..."
run_sql bigquery/04_views_trends_gdelt_unified.sql

echo -e "  → Deploying 05_property_graph.sql (BigQuery Property Graph)..."
run_sql bigquery/05_property_graph.sql

echo -e "  → Deploying 08_views_tier2_raw.sql (Tier 2 raw drill-down proxy views)..."
run_sql bigquery/08_views_tier2_raw.sql

echo -e "${GREEN}✓ BigQuery semantic layer deployed successfully.${NC}\n"

# -----------------------------------------------------------------------------
# 3. Knowledge Catalog Scope Registration (kcmd)
#
# Dataplex catalog entries for the BigQuery objects are auto-synced by
# BigQuery itself, including the descriptions embedded in the SQL DDL — no
# push is needed for that. kcmd init just registers/refreshes the dataset
# scope (catalog.yaml) for kcmd pull/push workflows; it is idempotent. The
# OKF markdown bundle in knowledge_catalog/ is consumed directly by the
# agent (see README Step 3), not uploaded via kcmd.
# -----------------------------------------------------------------------------
echo -e "${BLUE}${BOLD}[2/3] Registering Knowledge Catalog scope...${NC}"

if command -v kcmd >/dev/null 2>&1; then
  echo -e "  → 'kcmd' detected. Registering catalog scope for dataset ${PROJECT_ID}.${DATASET_ID}..."
  (
    cd knowledge_catalog
    if kcmd init --bigquery-dataset "${PROJECT_ID}.${DATASET_ID}"; then
      echo -e "${GREEN}✓ Knowledge Catalog scope registered (catalog.yaml). Dataplex entries auto-sync from BigQuery.${NC}"
    else
      echo -e "  ${YELLOW}Warning: 'kcmd init' failed (see output above). The local OKF bundle in${NC}"
      echo -e "  ${YELLOW}'knowledge_catalog/' remains usable directly for agent grounding.${NC}"
    fi
  )
  echo ""
else
  echo -e "  ${YELLOW}Notice: 'kcmd' CLI not found on PATH. Dataplex entries still auto-sync from BigQuery; the local OKF markdown bundle in 'knowledge_catalog/' is ready for direct manual or agent use.${NC}\n"
fi

# -----------------------------------------------------------------------------
# 4. Verification & Golden Query Preflight
# -----------------------------------------------------------------------------
echo -e "${BLUE}${BOLD}[3/3] Running preflight check on Golden Agent Queries...${NC}"

if ! preflight="$(bq query --dry_run --use_legacy_sql=false --project_id="${PROJECT_ID}" --location="${LOCATION}" < bigquery/06_golden_agent_queries.sql 2>&1)"; then
  echo -e "${RED}Error: Golden Queries dry-run FAILED:${NC}"
  echo "${preflight}"
  exit 1
fi
echo -e "${GREEN}✓ Golden Queries dry-run passed successfully.${NC}"

# The graph golden query needs an Enterprise/Enterprise Plus reservation —
# GRAPH_TABLE is rejected (even on dry run) under on-demand billing, so a
# missing edition is a warning here, not a failure.
if graph_preflight="$(bq query --dry_run --use_legacy_sql=false --project_id="${PROJECT_ID}" --location="${LOCATION}" < bigquery/07_graph_golden_query.sql 2>&1)"; then
  echo -e "${GREEN}✓ Graph Golden Query dry-run passed successfully.${NC}\n"
elif grep -qi "Enterprise" <<< "${graph_preflight}"; then
  echo -e "${YELLOW}Notice: Graph Golden Query skipped — GRAPH_TABLE requires a BigQuery${NC}"
  echo -e "${YELLOW}reservation with Enterprise or Enterprise Plus edition. The property graph${NC}"
  echo -e "${YELLOW}is deployed, but graph queries will not run on on-demand billing.${NC}\n"
else
  echo -e "${RED}Error: Graph Golden Query dry-run FAILED:${NC}"
  echo "${graph_preflight}"
  exit 1
fi

# -----------------------------------------------------------------------------
# Done
# -----------------------------------------------------------------------------
echo -e "${BOLD}${GREEN}==================================================================${NC}"
echo -e "${BOLD}${GREEN}   Setup Complete! Your environment is ready.                    ${NC}"
echo -e "${BOLD}${GREEN}==================================================================${NC}"
echo -e "\nNext step: Follow the tutorial in ${BOLD}README.md${NC} to create your"
echo -e "Conversational AI Agent in the BigQuery Console UI (Data Canvas / Conversational Analytics).\n"
