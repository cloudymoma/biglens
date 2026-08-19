# Service Account for BigLens VM
resource "google_service_account" "biglens_sa" {
  count        = var.create_service_account ? 1 : 0
  account_id   = "${var.deployment_name}-vm-sa"
  display_name = "BigLens VM Service Account"
  description  = "Dedicated service account for BigLens Compute Engine instance"
  project      = var.project_id
}

locals {
  sa_email = var.create_service_account ? google_service_account.biglens_sa[0].email : var.custom_service_account_email
}

# Grant BigQuery permissions on the query project
resource "google_project_iam_member" "bq_user" {
  project = local.bigquery_project_id
  role    = "roles/bigquery.user"
  member  = "serviceAccount:${local.sa_email}"
}

resource "google_project_iam_member" "bq_data_viewer" {
  project = local.bigquery_project_id
  role    = "roles/bigquery.dataViewer"
  member  = "serviceAccount:${local.sa_email}"
}

# Grant Dataplex / Knowledge Catalog Viewer permissions
resource "google_project_iam_member" "dataplex_viewer" {
  project = local.bigquery_project_id
  role    = "roles/dataplex.viewer"
  member  = "serviceAccount:${local.sa_email}"
}

# Grant Data Lineage Viewer permissions
resource "google_project_iam_member" "datalineage_viewer" {
  project = local.bigquery_project_id
  role    = "roles/datalineage.viewer"
  member  = "serviceAccount:${local.sa_email}"
}

# Grant Cloud Logging and Monitoring write permissions on infra project
resource "google_project_iam_member" "log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${local.sa_email}"
}

resource "google_project_iam_member" "metric_writer" {
  project = var.project_id
  role    = "roles/monitoring.metricWriter"
  member  = "serviceAccount:${local.sa_email}"
}
