terraform {
  required_version = ">= 1.9.0, < 2.0.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 6.0.0, < 7.0.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  service_name        = var.deployment_name
  bigquery_project_id = var.bigquery_project_id != "" ? var.bigquery_project_id : var.project_id
  common_labels = merge(
    {
      app        = "biglens"
      managed_by = "terraform"
    },
    var.custom_labels
  )
}
