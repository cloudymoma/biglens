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

## Dataplex 知识目录

**Dataplex** 视图将数据目录呈现为可搜索、可浏览、可编辑的 2D/3D 交互式图谱。
它基于
[开放知识格式（OKF）](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md)：
一个对 git 友好的 markdown 文件集合，每个文件是一个*概念*（节点），文件之间的
markdown 链接即为*边*。

- **图谱** — 力导向布局，可在 **2D** 与 **3D** 间切换。节点按 `type` 着色
  （BigQuery 表 / 视图 / 数据集、术语、指标等），边表示关系。
- **底部标签页** — **搜索**（按名称、类型或标签）、**详情**（所选节点的
  frontmatter、正文与关联）、**编辑**（创建、更新、删除概念，直接写入 markdown 包）。
- **从 Dataplex 导入** — 通过 Dataplex 通用目录的 `SearchEntries` 拉取实时条目
  写入 OKF 包，并生成两类边：
  - **包含关系** — `数据集 ⊃ 表`，由条目层级推导。
  - **血缘关系** — 源表 → 派生表的 ETL 数据流，来自
    [Data Lineage API](https://cloud.google.com/data-catalog/docs/concepts/about-data-lineage)
    （尽力而为；若该 API 未启用或无血缘记录，导入仍会成功并生成包含关系边，
    同时提示血缘已跳过）。
  编辑仅保存在本地包中（可通过 git 回滚），**不会**写回 Dataplex。

在 `conf.yaml` 中配置：

```yaml
catalog:
  bundle_path: "okf-bundle"   # OKF markdown 包所在目录
  dataplex:
    project_id: ""            # 为空时回退到 bigquery.project_id
    location: "global"        # Dataplex 搜索区域，如 "global" 或 "us"
  lineage_location: "us"      # Data Lineage API 区域（区域性，不可为 "global"）
```

运行时目录 `okf-bundle/` 已被 git 忽略（可能含导入的元数据）。仓库自带参考示例
`okf-bundle.sample/`，导入前可复制进来查看图谱：

```bash
cp -r okf-bundle.sample/. okf-bundle/
```

导入需要 `roles/dataplex.catalogViewer`；血缘边还需启用 Data Lineage API 并具备
`roles/datalineage.viewer` 权限。

## 架构

```
frontend/            React 19 + Vite + ECharts + Tailwind CSS v4
  catalog/           Dataplex 图谱视图（react-force-graph 2D/3D、three.js）
backend/             Go net/http 服务
  main.go            HTTP 服务、路由、中间件
  bigquery.go        所有 INFORMATION_SCHEMA 查询
  handlers.go        仪表盘接口，使用 errgroup 并发查询
  catalog_handlers.go  OKF 图谱/搜索/概念/导入接口
  okf.go             OKF 包引擎（解析、构图、读写概念）
  catalog_dataplex.go  Dataplex SearchEntries -> OKF 概念映射
  cache.go           内存 TTL 缓存（sync.Map，10 分钟过期）
  filters.go         全局筛选器解析与 SQL 条件构建
  config.go          YAML 配置加载
```

后端使用 `errgroup` 对同一仪表盘的所有组件查询并行执行，并将结果缓存 10 分钟以减少 BigQuery API 调用。

## BigQuery INFORMATION_SCHEMA

BigLens 完全构建在 BigQuery 的 `INFORMATION_SCHEMA` 之上——这是一组只读系统视图，用于暴露 BigQuery 资源的元数据。这些视图提供存储指标、作业执行历史、槽位使用情况和优化建议，均可通过标准 SQL 查询。

![BigQuery INFORMATION_SCHEMA 指南](miscs/bq_meta_guide.png)

完整文档请参阅 Google Cloud 官方参考：
[BigQuery INFORMATION_SCHEMA 简介](https://cloud.google.com/bigquery/docs/information-schema-intro)
