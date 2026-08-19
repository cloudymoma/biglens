# Static External IP Address for Load Balancer
resource "google_compute_global_address" "biglens_ip" {
  name        = "${var.deployment_name}-static-ip"
  project     = var.project_id
  description = "Static external IP address for BigLens HTTPS Load Balancer"
}

# Health Check against BigLens port
resource "google_compute_health_check" "biglens_hc" {
  name                = "${var.deployment_name}-health-check"
  project             = var.project_id
  check_interval_sec  = 10
  timeout_sec         = 5
  healthy_threshold   = 2
  unhealthy_threshold = 3

  http_health_check {
    port         = var.server_port
    request_path = "/"
  }
}

# Backend Service with Cloud Identity-Aware Proxy (IAP) Enabled
resource "google_compute_backend_service" "biglens_backend" {
  name                  = "${var.deployment_name}-backend-service"
  project               = var.project_id
  protocol              = "HTTP"
  port_name             = "http-app"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  timeout_sec           = 60
  health_checks         = [google_compute_health_check.biglens_hc.id]

  backend {
    group           = google_compute_instance_group.biglens_ig.self_link
    balancing_mode  = "UTILIZATION"
    max_utilization = 0.8
  }

  iap {
    enabled              = true
    oauth2_client_id     = var.iap_client_id != "" ? var.iap_client_id : null
    oauth2_client_secret = var.iap_client_secret != "" ? var.iap_client_secret : null
  }
}

# Google-Managed SSL Certificate with unique domain hash and create_before_destroy lifecycle
resource "google_compute_managed_ssl_certificate" "biglens_ssl" {
  name    = "${var.deployment_name}-ssl-${substr(sha256(var.domain), 0, 8)}"
  project = var.project_id

  managed {
    domains = [var.domain]
  }

  lifecycle {
    create_before_destroy = true
  }
}

# URL Map routing HTTPS traffic to backend service
resource "google_compute_url_map" "biglens_url_map" {
  name            = "${var.deployment_name}-url-map"
  project         = var.project_id
  default_service = google_compute_backend_service.biglens_backend.id
}

# Target HTTPS Proxy combining URL Map and Managed SSL Cert
resource "google_compute_target_https_proxy" "biglens_https_proxy" {
  name             = "${var.deployment_name}-https-proxy"
  project          = var.project_id
  url_map          = google_compute_url_map.biglens_url_map.id
  ssl_certificates = [google_compute_managed_ssl_certificate.biglens_ssl.id]
}

# Global Forwarding Rule on Port 443 (HTTPS)
resource "google_compute_global_forwarding_rule" "biglens_forwarding_rule" {
  name                  = "${var.deployment_name}-forwarding-rule"
  project               = var.project_id
  ip_protocol           = "TCP"
  port_range            = "443"
  target                = google_compute_target_https_proxy.biglens_https_proxy.id
  ip_address            = google_compute_global_address.biglens_ip.id
  load_balancing_scheme = "EXTERNAL_MANAGED"
}

# --- HTTP (Port 80) -> HTTPS (Port 443) Redirect ---

resource "google_compute_url_map" "http_redirect" {
  name    = "${var.deployment_name}-http-redirect"
  project = var.project_id

  default_url_redirect {
    https_redirect         = true
    redirect_response_code = "MOVED_PERMANENTLY_DEFAULT"
    strip_query            = false
  }
}

resource "google_compute_target_http_proxy" "http_proxy" {
  name    = "${var.deployment_name}-http-proxy"
  project = var.project_id
  url_map = google_compute_url_map.http_redirect.id
}

resource "google_compute_global_forwarding_rule" "http_forwarding_rule" {
  name                  = "${var.deployment_name}-http-forwarding-rule"
  project               = var.project_id
  ip_protocol           = "TCP"
  port_range            = "80"
  target                = google_compute_target_http_proxy.http_proxy.id
  ip_address            = google_compute_global_address.biglens_ip.id
  load_balancing_scheme = "EXTERNAL_MANAGED"
}
