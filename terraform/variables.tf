variable "project_id" {
  description = "The Google Cloud Project ID where BigLens infrastructure will be deployed."
  type        = string
}

variable "bigquery_project_id" {
  description = "The Google Cloud Project ID for BigQuery dataset queries (defaults to project_id if left empty)."
  type        = string
  default     = ""
}

variable "dataplex_location" {
  description = "Default Dataplex / Knowledge Catalog location (e.g. us, us-central1)."
  type        = string
  default     = "us"
}

variable "region" {
  description = "The GCP region for the Compute Engine VM, subnet, and Cloud NAT."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "The GCP zone for the Compute Engine VM."
  type        = string
  default     = "us-central1-a"
}

variable "deployment_name" {
  description = "Base prefix name used for all GCP resources."
  type        = string
  default     = "biglens"
}

variable "network" {
  description = "The VPC network name where the VM will be attached."
  type        = string
  default     = "default"
}

variable "subnetwork" {
  description = "The VPC subnetwork name where the VM will be attached. Required when using a custom-mode VPC network; optional for default or auto-mode VPCs."
  type        = string
  default     = ""
}

variable "create_cloud_nat" {
  description = "Whether to provision Cloud Router + Cloud NAT for private VM egress (required for package downloads on private VMs)."
  type        = bool
  default     = true
}

variable "domain" {
  description = "The fully qualified domain name (FQDN) for BigLens (e.g. biglens.yourcompany.com)."
  type        = string
}

variable "iap_client_id" {
  description = "Optional OAuth 2.0 Client ID for Identity-Aware Proxy (leave empty to use Google-managed IAP client)."
  type        = string
  default     = ""
}

variable "iap_client_secret" {
  description = "Optional OAuth 2.0 Client Secret for Identity-Aware Proxy (leave empty to use Google-managed IAP client)."
  type        = string
  default     = ""
  sensitive   = true

  validation {
    condition = (
      (var.iap_client_id == "" && var.iap_client_secret == "") ||
      (var.iap_client_id != "" && var.iap_client_secret != "")
    )
    error_message = "Both iap_client_id and iap_client_secret must be provided together, or both left empty for Google-managed IAP."
  }
}

variable "allowed_users_and_groups" {
  description = "List of members permitted to access BigLens via IAP (e.g. ['user:alice@example.com', 'group:marketing@example.com', 'domain:example.com'])."
  type        = list(string)
  default     = []
}

variable "machine_type" {
  description = "Compute Engine machine type."
  type        = string
  default     = "e2-standard-2"
}

variable "boot_disk_size_gb" {
  description = "Root boot disk size in GB."
  type        = number
  default     = 50
}

variable "boot_disk_type" {
  description = "Root boot disk type (pd-standard, pd-balanced, pd-ssd)."
  type        = string
  default     = "pd-balanced"
}

variable "create_service_account" {
  description = "Whether to create a dedicated service account for the BigLens VM."
  type        = bool
  default     = true
}

variable "custom_service_account_email" {
  description = "Existing Service Account email to attach to the VM (used only if create_service_account is false)."
  type        = string
  default     = ""

  validation {
    condition     = var.create_service_account || var.custom_service_account_email != ""
    error_message = "custom_service_account_email must be provided when create_service_account is false."
  }
}

variable "server_port" {
  description = "The internal port BigLens listens on."
  type        = number
  default     = 1983
}

variable "git_repo_url" {
  description = "Git repository URL to clone on the VM."
  type        = string
  default     = "https://github.com/cloudymoma/biglens.git"
}

variable "git_branch" {
  description = "Git branch to checkout and build on the VM."
  type        = string
  default     = "main"
}

variable "custom_labels" {
  description = "Additional labels to apply to all supported resources."
  type        = map(string)
  default     = {}
}
