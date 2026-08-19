# Cloud Router for private VM internet egress (package downloads & git)
resource "google_compute_router" "router" {
  count   = var.create_cloud_nat ? 1 : 0
  name    = "${var.deployment_name}-router"
  project = var.project_id
  region  = var.region
  network = var.network
}

# Cloud NAT for private VM internet egress
resource "google_compute_router_nat" "nat" {
  count                              = var.create_cloud_nat ? 1 : 0
  name                               = "${var.deployment_name}-nat"
  project                            = var.project_id
  router                             = google_compute_router.router[0].name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = var.subnetwork != "" ? "LIST_OF_SUBNETWORKS" : "ALL_SUBNETWORKS_ALL_IP_RANGES"

  dynamic "subnetwork" {
    for_each = var.subnetwork != "" ? [var.subnetwork] : []
    content {
      name                    = subnetwork.value
      source_ip_ranges_to_nat = ["ALL_IP_RANGES"]
    }
  }

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}

# Firewall Rule: Allow Google Cloud Health Checks and LB Proxies to reach BigLens port
resource "google_compute_firewall" "allow_health_checks" {
  name        = "${var.deployment_name}-allow-health-checks"
  project     = var.project_id
  network     = var.network
  description = "Allow Google Cloud Load Balancer and Health Check probes to reach BigLens service on port ${var.server_port}"

  allow {
    protocol = "tcp"
    ports    = [tostring(var.server_port)]
  }

  # Official Google Cloud Load Balancer & Health Check probe IP ranges
  source_ranges = [
    "35.191.0.0/16",
    "130.211.0.0/22"
  ]

  target_tags = ["${var.deployment_name}-backend"]
}

# Firewall Rule: Allow Secure SSH through IAP Tunnel (No Public IP needed)
resource "google_compute_firewall" "allow_iap_ssh" {
  name        = "${var.deployment_name}-allow-iap-ssh"
  project     = var.project_id
  network     = var.network
  description = "Allow SSH to VM via Cloud Identity-Aware Proxy (IAP) TCP forwarding"

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  # Official Cloud IAP secure forwarding IP range
  source_ranges = [
    "35.235.240.0/20"
  ]

  target_tags = ["${var.deployment_name}-backend"]
}
