# BigLens

[![Build](https://github.com/cloudymoma/biglens/actions/workflows/build.yml/badge.svg)](https://github.com/cloudymoma/biglens/actions/workflows/build.yml)

[English](README.md) | 简体中文

BigQuery 实时可观测性仪表盘。BigLens 通过查询 BigQuery 的 `INFORMATION_SCHEMA` 视图，将存储成本、计算槽位使用、用户级别开销和优化建议汇集在一个深色主题的 Web 界面中。

![存储分析](miscs/biglens_1.png)

![计算分析](miscs/biglens_2.png)

## 快速开始

### 前置条件

- **Go 1.22+**
- **Node.js 20+** 及 npm
- 具有 BigQuery 元数据访问权限的 **Google Cloud 凭证**（`roles/bigquery.resourceViewer`）

### 1. 配置

复制配置模板并填入你的 GCP 项目 ID：

```bash
cp conf.yaml.template conf.yaml
```

编辑 `conf.yaml`：

```yaml
server:
  port: 1983
  mode: "debug"        # "debug" 或 "release"

bigquery:
  project_id: "your-gcp-project-id"
  credentials_path: "" # 可选，留空则使用 GOOGLE_APPLICATION_CREDENTIALS
```

| 字段 | 说明 |
|---|---|
| `server.port` | 仪表盘 HTTP 端口（默认 `1983`） |
| `server.mode` | `debug` 详细日志，`release` 生产模式 |
| `bigquery.project_id` | 你的 GCP 项目 ID |
| `bigquery.credentials_path` | 服务账号 JSON 密钥路径。留空则使用应用默认凭证（`gcloud auth application-default login`） |

### 2. 构建并启动

```bash
make serve
```

该命令一键完成：
1. 安装前端依赖并构建 React 应用
2. 将前端静态文件复制到 Go 服务端
3. 编译 Go 二进制文件
4. 启动服务

在浏览器中打开 **http://localhost:1983** 即可使用。

### 其他 Make 命令

```bash
make build-frontend   # 仅构建 React 前端
make build-backend    # 仅编译 Go 后端
make build-all        # 构建前后端但不启动
make clean            # 清理构建产物
```

### 开发模式

前端热重载 + 后端 API 服务：

```bash
# 终端 1：启动 Go 后端
make build-backend && ./bin/biglens-server

# 终端 2：启动 Vite 开发服务器（自动代理 /api/* 到 1983 端口）
cd frontend && npm run dev
```

## 仪表盘

BigLens 提供四个仪表盘视图，均基于 `INFORMATION_SCHEMA` 查询驱动：

| 仪表盘 | 组件 |
|---|---|
| **存储** | 逻辑/物理计费模拟器、活跃/长期存储环形图、Top 10 最大表 |
| **计算** | 并发槽位使用面积图 (JOBS_TIMELINE)、槽位消耗 Top 10 作业 |
| **成本** | 按需成本估算（$6.25/TiB）、用户维度开销树状图 |
| **洞察** | BigQuery 活跃优化建议列表 |

### 全局筛选器

所有仪表盘共享侧边栏筛选面板：

- **区域** — 可搜索的 BQ 区域下拉框（默认 `us`）
- **数据集** / **表** — 将指标范围限定到特定数据集或表
- **用户邮箱** — 按用户或服务账号隔离指标
- **时间范围** — 24 小时、7 天、30 天或 90 天回溯

## 架构

```
frontend/          React 19 + Vite + ECharts + Tailwind CSS v4
backend/           Go net/http 服务
  main.go          HTTP 服务、路由、中间件
  bigquery.go      所有 INFORMATION_SCHEMA 查询
  handlers.go      仪表盘接口，使用 errgroup 并发查询
  cache.go         内存 TTL 缓存（sync.Map，10 分钟过期）
  filters.go       全局筛选器解析与 SQL 条件构建
  config.go        YAML 配置加载
```

后端使用 `errgroup` 对同一仪表盘的所有组件查询并行执行，并将结果缓存 10 分钟以减少 BigQuery API 调用。

## BigQuery INFORMATION_SCHEMA

BigLens 完全构建在 BigQuery 的 `INFORMATION_SCHEMA` 之上——这是一组只读系统视图，用于暴露 BigQuery 资源的元数据。这些视图提供存储指标、作业执行历史、槽位使用情况和优化建议，均可通过标准 SQL 查询。

![BigQuery INFORMATION_SCHEMA 指南](miscs/bq_meta_guide.png)

完整文档请参阅 Google Cloud 官方参考：
[BigQuery INFORMATION_SCHEMA 简介](https://cloud.google.com/bigquery/docs/information-schema-intro)
