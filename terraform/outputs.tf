output "load_balancer_ip" {
  description = "The static public IP address of the Global HTTPS Load Balancer."
  value       = google_compute_global_address.biglens_ip.address
}

output "dashboard_url" {
  description = "The secure HTTPS URL to access BigLens."
  value       = "https://${var.domain}"
}

output "dns_instruction" {
  description = "Action required: DNS A-Record configuration."
  value       = "Create a DNS A-Record pointing '${var.domain}' to '${google_compute_global_address.biglens_ip.address}'. Google Managed SSL will activate automatically once DNS resolves."
}

output "vm_internal_ip" {
  description = "Internal RFC 1918 IP address of the BigLens VM."
  value       = google_compute_instance.biglens_vm.network_interface[0].network_ip
}

output "iap_ssh_command" {
  description = "Command to securely SSH into the VM through IAP without a public IP."
  value       = "gcloud compute ssh ${google_compute_instance.biglens_vm.name} --project=${var.project_id} --zone=${var.zone} --tunnel-through-iap"
}
