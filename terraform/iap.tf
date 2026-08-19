# Grant IAP Web Access to Allowed Users/Groups (Non-authoritative member binding per user/group)
resource "google_iap_web_backend_service_iam_member" "iap_users" {
  for_each            = toset(var.allowed_users_and_groups)
  project             = var.project_id
  web_backend_service = google_compute_backend_service.biglens_backend.name
  role                = "roles/iap.httpsResourceAccessor"
  member              = each.value
}

# Grant IAP TCP Tunnel Access for secure SSH directly to the BigLens VM instance
resource "google_iap_tunnel_instance_iam_member" "iap_ssh_users" {
  for_each = toset(var.allowed_users_and_groups)
  project  = var.project_id
  zone     = var.zone
  instance = google_compute_instance.biglens_vm.name
  role     = "roles/iap.tunnelResourceAccessor"
  member   = each.value
}
