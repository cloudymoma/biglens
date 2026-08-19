# BigLens GCP Secure Deployment with Terraform & Cloud IAP

English | [简体中文](README_cn.md)

Deploy BigLens onto a private Google Compute Engine VM (no external IP) behind a **Global HTTPS Load Balancer**, **Google-Managed SSL**, and **Cloud Identity-Aware Proxy (IAP)**.

*Tested and verified with **Terraform v1.15.8**.*

---

## 🏛️ Architecture

```
User (Browser)
    │
    ▼ (HTTPS:443 via Domain)
Google Cloud Global Application Load Balancer (Static Public IP)
    │
    ├── Port 80 -> 443 HTTPS Redirect
    ├── Google-Managed SSL Certificate
    ├── Identity-Aware Proxy (IAP) [OAuth 2.0 / Workspace SSO / 2FA]
    │
    ▼ (Internal VPC Network: Port 1983)
GCE VM (Internal RFC 1918 IP only — zero internet exposure)
    │
    ├── Cloud NAT (Provides secure outbound package/git access)
    └── BigLens Server (systemd service running as biglens user)
```

---

## 📋 Prerequisites

1. **Google Cloud Account & Project** with billing enabled.
2. **Tools installed locally:**
   * [`gcloud` CLI](https://cloud.google.com/sdk/docs/install), authenticated:
     ```bash
     gcloud auth login
     gcloud auth application-default login   # credentials used by Terraform
     ```
   * `terraform` CLI (The included `./deploy.sh` script automatically ensures the verified **v1.15.8** standalone binary is used).
3. **A Domain Name** (e.g. `biglens.yourcompany.com`) where you can create a DNS A-Record.
4. **OAuth 2.0 Client Credentials (Optional):**
   * By default, Google Cloud supports **Google-Managed IAP** (no manual OAuth client creation needed).
   * If you wish to use a custom OAuth client:
     * In GCP Console, go to **APIs & Services** → **OAuth consent screen** (Internal).
     * Go to **Credentials** → **Create Credentials** → **OAuth client ID** (Web application).
     * Provide `iap_client_id` and `iap_client_secret` in `terraform.tfvars`.

---

## 🚀 Quick Start Deployment

### 1. Configure Variables
```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`:
```hcl
project_id          = "my-gcp-project"
bigquery_project_id = "" # Leave empty if same as project_id
domain              = "biglens.yourcompany.com"

# Access Control: Permitted users or Google Workspace groups
allowed_users_and_groups = [
  "group:marketing@yourcompany.com",
  "user:admin@yourcompany.com"
]
```

### 2. Run Deployment
```bash
./deploy.sh
```
`deploy.sh` resolves the pinned Terraform binary, enables the required GCP APIs, then runs `terraform plan` and asks for confirmation before applying.

*Or manually with Terraform v1.15.8 (first enable the required APIs — see the `gcloud services enable` list in `deploy.sh`):*
```bash
terraform init
terraform apply
```

### 3. Configure DNS
When `terraform apply` finishes, note the output IP:
```text
Outputs:
load_balancer_ip = "34.120.x.x"
dashboard_url    = "https://biglens.yourcompany.com"
```

Create a **DNS A-Record** in your domain registrar:
* **Host:** `biglens` (or your subdomain)
* **Value / Points to:** `34.120.x.x` (the `load_balancer_ip`)

> [!IMPORTANT]
> **Initial Provisioning Notice (502 Gateway):** The Load Balancer may return `502 Bad Gateway` for the first **~5–10 minutes** while the VM finishes package installation (`npm install` and Go build) and starts the service.
> 
> **SSL Provisioning:** Google-Managed SSL certificates typically take **10–20 minutes** to provision after your DNS A-Record begins resolving.

---

## 🔒 Managing Access (IAP)

To grant or revoke access after deployment:
1. Update `allowed_users_and_groups` in `terraform.tfvars`:
   ```hcl
   allowed_users_and_groups = [
     "group:analytics@yourcompany.com",
     "user:newhire@yourcompany.com"
   ]
   ```
2. Run `terraform apply` (or `./deploy.sh`).

---

## 🛠️ Maintenance & Troubleshooting

### Securely SSH into the VM
The VM has no public IP. To SSH into it, use Cloud IAP tunneling (run `terraform output iap_ssh_command` to get the exact command for your deployment):
```bash
gcloud compute ssh biglens-server --zone=us-central1-a --tunnel-through-iap
```

### View BigLens Server Logs
Once inside the VM:
```bash
sudo journalctl -u biglens.service -f
```

### Upgrading Terraform Providers
The repository includes `.terraform.lock.hcl` pre-locked for Linux (`amd64`, `arm64`), macOS (`amd64`, `arm64`), and Windows. To upgrade provider versions in the future:
```bash
terraform init -upgrade
terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_amd64 -platform=darwin_arm64 -platform=windows_amd64
```

### Clean Teardown
To destroy all cloud infrastructure without leaving orphaned resources:
```bash
terraform destroy
```
