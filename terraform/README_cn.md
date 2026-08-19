# BigLens GCP 安全部署 — Terraform 与 Cloud IAP

[English](README.md) | 简体中文

将 BigLens 部署到一台私有的 Google Compute Engine 虚拟机上（无外部 IP），并置于 **全球 HTTPS 负载均衡器**、**Google 托管 SSL 证书** 和 **Cloud Identity-Aware Proxy (IAP)** 之后。

*已使用 **Terraform v1.15.8** 测试验证。*

---

## 🏛️ 架构

```
用户（浏览器）
    │
    ▼ (HTTPS:443 通过域名)
Google Cloud 全球应用负载均衡器（静态公网 IP）
    │
    ├── 端口 80 -> 443 HTTPS 重定向
    ├── Google 托管 SSL 证书
    ├── Identity-Aware Proxy (IAP) [OAuth 2.0 / Workspace SSO / 两步验证]
    │
    ▼ (内部 VPC 网络: 端口 1983)
GCE 虚拟机（仅内部 RFC 1918 IP — 零互联网暴露）
    │
    ├── Cloud NAT（提供安全的出站软件包/git 访问）
    └── BigLens 服务器（以 biglens 用户运行的 systemd 服务）
```

---

## 📋 前置条件

1. **Google Cloud 账号与项目**，并已启用结算。
2. **本地已安装的工具：**
   * [`gcloud` CLI](https://cloud.google.com/sdk/docs/install)，并已完成身份验证：
     ```bash
     gcloud auth login
     gcloud auth application-default login   # Terraform 使用的凭据
     ```
   * `terraform` CLI（随附的 `./deploy.sh` 脚本会自动确保使用经过验证的 **v1.15.8** 独立二进制文件）。
3. **一个域名**（例如 `biglens.yourcompany.com`），你需要能为其创建 DNS A 记录。
4. **OAuth 2.0 客户端凭据（可选）：**
   * 默认情况下，Google Cloud 支持 **Google 托管 IAP**（无需手动创建 OAuth 客户端）。
   * 如果你希望使用自定义 OAuth 客户端：
     * 在 GCP 控制台中，进入 **APIs & Services** → **OAuth consent screen**（选择 Internal）。
     * 进入 **Credentials** → **Create Credentials** → **OAuth client ID**（Web application）。
     * 在 `terraform.tfvars` 中填写 `iap_client_id` 和 `iap_client_secret`。

---

## 🚀 快速部署

### 1. 配置变量
```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
```

编辑 `terraform.tfvars`：
```hcl
project_id          = "my-gcp-project"
bigquery_project_id = "" # 与 project_id 相同时留空
domain              = "biglens.yourcompany.com"

# 访问控制：允许访问的用户或 Google Workspace 群组
allowed_users_and_groups = [
  "group:marketing@yourcompany.com",
  "user:admin@yourcompany.com"
]
```

### 2. 执行部署
```bash
./deploy.sh
```

`deploy.sh` 会解析固定版本的 Terraform 二进制文件、启用所需的 GCP API，然后运行 `terraform plan` 并在应用前请求确认。

*或使用 Terraform v1.15.8 手动执行（请先启用所需的 API — 参见 `deploy.sh` 中的 `gcloud services enable` 列表）：*
```bash
terraform init
terraform apply
```

### 3. 配置 DNS
`terraform apply` 完成后，记下输出的 IP：
```text
Outputs:
load_balancer_ip = "34.120.x.x"
dashboard_url    = "https://biglens.yourcompany.com"
```

在你的域名注册商处创建 **DNS A 记录**：
* **主机名：** `biglens`（或你的子域名）
* **值 / 指向：** `34.120.x.x`（即 `load_balancer_ip`）

> [!IMPORTANT]
> **首次部署提示（502 网关错误）：** 在虚拟机完成软件包安装（`npm install` 和 Go 构建）并启动服务之前的 **约 5–10 分钟** 内，负载均衡器可能返回 `502 Bad Gateway`。
>
> **SSL 证书签发：** DNS A 记录开始解析后，Google 托管 SSL 证书通常需要 **10–20 分钟** 完成签发。

---

## 🔒 访问管理 (IAP)

部署完成后授予或撤销访问权限：
1. 更新 `terraform.tfvars` 中的 `allowed_users_and_groups`：
   ```hcl
   allowed_users_and_groups = [
     "group:analytics@yourcompany.com",
     "user:newhire@yourcompany.com"
   ]
   ```
2. 运行 `terraform apply`（或 `./deploy.sh`）。

---

## 🛠️ 维护与故障排查

### 安全地 SSH 登录虚拟机
虚拟机没有公网 IP。请通过 Cloud IAP 隧道 SSH 登录（运行 `terraform output iap_ssh_command` 可获取适用于你部署的确切命令）：
```bash
gcloud compute ssh biglens-server --zone=us-central1-a --tunnel-through-iap
```

### 查看 BigLens 服务器日志
登录虚拟机后：
```bash
sudo journalctl -u biglens.service -f
```

### 升级 Terraform Provider
仓库中包含的 `.terraform.lock.hcl` 已预先锁定 Linux（`amd64`、`arm64`）、macOS（`amd64`、`arm64`）和 Windows 平台的校验和。未来升级 provider 版本时：
```bash
terraform init -upgrade
terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_amd64 -platform=darwin_arm64 -platform=windows_amd64
```

### 彻底清理
销毁所有云基础设施且不留下孤立资源：
```bash
terraform destroy
```
