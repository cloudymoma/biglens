#!/usr/bin/env bash
# =============================================================================
# BigLens GCP IAP Deployment Preflight Helper
# Pinned Terraform Execution Engine (Target: v1.15.8)
#
# Resolution Priority:
#  1. terraform/.bin/terraform (if exists and is v1.15.8)
#  2. System terraform in PATH (if exists and is exactly v1.15.8)
#  3. Download standalone v1.15.8 binary to terraform/.bin/
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

TARGET_TF_VERSION="1.15.8"
LOCAL_BIN_DIR="${SCRIPT_DIR}/.bin"
LOCAL_TF="${LOCAL_BIN_DIR}/terraform"

echo "============================================================"
echo " BigLens GCP IAP Deployment Preflight"
echo " Pinned Terraform Version: v${TARGET_TF_VERSION}"
echo "============================================================"

# Helper function to check if a binary is exactly target version
is_exact_version() {
  local bin="$1"
  if [ ! -x "${bin}" ] && ! command -v "${bin}" &> /dev/null; then
    return 1
  fi
  local ver_str
  ver_str=$("${bin}" version 2>/dev/null | head -n1 | sed -E 's/^Terraform v([0-9]+\.[0-9]+\.[0-9]+).*/\1/')
  [ "${ver_str}" = "${TARGET_TF_VERSION}" ]
}

# 1. Check local .bin/terraform
TF_CMD=""
if [ -x "${LOCAL_TF}" ] && is_exact_version "${LOCAL_TF}"; then
  TF_CMD="${LOCAL_TF}"
# 2. Check system terraform
elif command -v terraform &> /dev/null && is_exact_version "terraform"; then
  TF_CMD="terraform"
fi

# 3. Else download standalone binary into .bin/
if [ -z "${TF_CMD}" ]; then
  echo "📥 Exact Terraform v${TARGET_TF_VERSION} not found in .bin/ or PATH."
  echo "Downloading standalone Terraform v${TARGET_TF_VERSION} binary..."
  mkdir -p "${LOCAL_BIN_DIR}"

  if ! command -v curl &> /dev/null || ! command -v unzip &> /dev/null; then
    echo "❌ Error: 'curl' and 'unzip' are required to download the standalone Terraform binary."
    exit 1
  fi

  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"
  if [ "${ARCH}" = "x86_64" ]; then
    ARCH="amd64"
  elif [ "${ARCH}" = "aarch64" ] || [ "${ARCH}" = "arm64" ]; then
    ARCH="arm64"
  fi

  TMP_ZIP="/tmp/terraform_${TARGET_TF_VERSION}_${OS}_${ARCH}.zip"
  curl -fsSL "https://releases.hashicorp.com/terraform/${TARGET_TF_VERSION}/terraform_${TARGET_TF_VERSION}_${OS}_${ARCH}.zip" -o "${TMP_ZIP}"
  unzip -q -o "${TMP_ZIP}" -d "${LOCAL_BIN_DIR}"
  chmod +x "${LOCAL_TF}"
  rm -f "${TMP_ZIP}"

  TF_CMD="${LOCAL_TF}"
  echo "✓ Downloaded standalone Terraform v${TARGET_TF_VERSION} to ${LOCAL_TF}"
fi

echo "✓ Using Terraform: $("${TF_CMD}" version | head -n1)"

# Check gcloud installation
if ! command -v gcloud &> /dev/null; then
  echo "❌ Error: gcloud CLI is not installed."
  exit 1
fi

# Check terraform.tfvars exists
if [ ! -f "terraform.tfvars" ]; then
  echo "⚠️  terraform.tfvars not found."
  if [ -f "terraform.tfvars.example" ]; then
    echo "Creating terraform.tfvars from template..."
    cp terraform.tfvars.example terraform.tfvars
    echo "📝 Please edit terraform/terraform.tfvars with your project_id and domain, then re-run ./deploy.sh."
    exit 1
  fi
fi

# Extract project_id from tfvars
PROJECT_ID=$(grep -E '^\s*project_id\s*=' terraform.tfvars | head -n1 | cut -d'=' -f2 | tr -d ' "' || true)

if [ -z "${PROJECT_ID}" ] || [ "${PROJECT_ID}" = "your-gcp-project-id" ]; then
  echo "❌ Error: Please specify a valid 'project_id' in terraform/terraform.tfvars."
  exit 1
fi

echo "✓ Target Project: ${PROJECT_ID}"
echo "✓ Enabling required Google Cloud APIs..."
gcloud services enable \
  compute.googleapis.com \
  iap.googleapis.com \
  bigquery.googleapis.com \
  dataplex.googleapis.com \
  datalineage.googleapis.com \
  logging.googleapis.com \
  monitoring.googleapis.com \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  --project="${PROJECT_ID}"

echo "✓ Initializing Terraform..."
"${TF_CMD}" init

echo "✓ Planning Terraform deployment..."
"${TF_CMD}" plan -out=tfplan

echo "============================================================"
read -p "Do you want to apply this Terraform plan? (y/N): " -r CONFIRM
echo
if [[ "${CONFIRM}" =~ ^[Yy]$ ]]; then
  "${TF_CMD}" apply tfplan
  rm -f tfplan
  echo "🎉 Deployment initiated successfully!"
else
  rm -f tfplan
  echo "Deployment cancelled."
fi
