# Compute Engine Instance (No External IP)
resource "google_compute_instance" "biglens_vm" {
  name         = "${var.deployment_name}-server"
  project      = var.project_id
  zone         = var.zone
  machine_type = var.machine_type

  tags = ["${var.deployment_name}-backend"]

  labels = local.common_labels

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = var.boot_disk_size_gb
      type  = var.boot_disk_type
    }
  }

  network_interface {
    network    = var.network
    subnetwork = var.subnetwork != "" ? var.subnetwork : null
    # Omitting access_config ensures NO external IP is allocated
  }

  service_account {
    email  = local.sa_email
    scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }

  metadata_startup_script = templatefile("${path.module}/scripts/startup.sh.tftpl", {
    git_repo_url        = var.git_repo_url
    git_branch          = var.git_branch
    server_port         = var.server_port
    bigquery_project_id = local.bigquery_project_id
    dataplex_location   = var.dataplex_location
  })

  allow_stopping_for_update = true

  # Ensure Cloud NAT is available before instance starts so startup script can download packages
  depends_on = [google_compute_router_nat.nat]
}

# Unmanaged Instance Group to attach VM to Load Balancer Backend Service
resource "google_compute_instance_group" "biglens_ig" {
  name        = "${var.deployment_name}-instance-group"
  project     = var.project_id
  zone        = var.zone
  description = "Unmanaged instance group for BigLens server"

  instances = [
    google_compute_instance.biglens_vm.self_link
  ]

  named_port {
    name = "http-app"
    port = var.server_port
  }
}
