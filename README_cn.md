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

## BigQuery 公共数据 (Open Data)

**BigQuery Open Data** 视图承载基于
[Google Cloud 公共数据集](https://cloud.google.com/bigquery/public-data)
的仪表盘。查询在你配置的项目中执行（计费在该项目），访问
`bigquery-public-data`；所有查询均带分区键过滤以控制扫描量，结果复用与其他
仪表盘相同的 10 分钟缓存。

### Google Trends

首个仪表盘，基于 `bigquery-public-data.google_trends`
（`international_top_terms` / `international_top_rising_terms`）：

| 组件 | 说明 |
|---|---|
| **热词榜单** | 各国家 Top 25 热词，行内分数进度条 |
| **词云** | 字号随 score 变化，Top 5 高亮 |
| **飙升热词** | 按 `percent_gain` 排序的 Top 10 飙升词及明细表 |
| **跨国热度对比** | 某个词在所有进入 Top 25 国家的最新分数 |
| **历史趋势** | 5 年周度历史，最多同时对比 5 个词，支持拖拽缩放 |

筛选器：国家、快照日期（`refresh_date` 分区）、当日榜单内的关键词搜索。
点击任意热词即可查看其跨国分布，并加入趋势对比图。

#### 指标说明

仪表盘展示的三个数字直接来自数据集，各自度量的维度不同，因此不会同步变化：

- **排名 Rank（1–25）** — 该词在所在国家当日热榜中的位置，按当天的
  *绝对搜索量*排序。热词榜单即按此排序。
- **分数 Score（0–100）** — Google 的*相对*搜索热度指数，基于最新一周数据：
  每个词以自身历史峰值做归一化，100 表示"本周正处于（或追平）该词的
  热度巅峰"。数据集按地区（region）粒度提供该值，BigLens 会对国家内
  所有地区取平均并取整（`CAST(COALESCE(AVG(score), 0) AS INT64)`，
  score 为 NULL 时按 0 计）。榜单每行的内嵌进度条可视化的就是这个值。
- **涨幅 Gain（%）** — 仅飙升热词有此指标：搜索量的周环比增长率
  （`percent_gain`）。全新爆发的查询词涨幅可达数千个百分点。

由于排名反映的是*当日绝对搜索量*，而分数反映的是*相对该词自身历史的
热度*，一个排在第 14 位的词可能分数为 100（刚创下历史新高），而第 1 名
的词分数反而更低（搜索量巨大，但已过热度峰值周）。因此榜单刻意按排名
而非分数排序。

### GDELT 实时舆情（News Pulse）

实时全球新闻情绪与地缘态势监控仪表盘，基于
[GDELT 项目](https://www.gdeltproject.org/) 2.0 数据表
`gdelt-bq.gdeltv2.events_partitioned` 与 `gdelt-bq.gdeltv2.gkg_partitioned`。
GDELT 以机器方式阅读全球 100 多种语言的新闻媒体，每 15 分钟更新一次；
BigLens 直接查询官方分区底表（不创建任何中间表或视图）。

| 组件 | 说明 |
|---|---|
| **全球事件热点图** | 世界地图 Top 500 地点 —— 气泡大小 = 事件数，颜色 = 平均情绪 |
| **情绪仪表盘** | 所选区间的加权全球平均情绪 |
| **热度与情绪走向** | 每日事件数（柱）与每日平均情绪（线）双轴图 |
| **合作与冲突态势** | 四类 QuadClass 事件占比环形图 |
| **风险矩阵** | 事件类型按 Goldstein 分（x）× 活跃度（y，对数轴）分布 —— 右下象限为高活跃 × 高破坏性 |
| **冲突类别排行** | 各 CAMEO 冲突根编码的事件数（抗议、胁迫、袭击、战斗……） |
| **突发冲突新闻** | 提及数最高的 Top 50 冲突报道，每个源 URL 仅一行 |
| **热点主题 Treemap** | Top 50 GKG 主题，按文章数计权 |
| **核心人物 / 媒体阵营** | Top 20 报道人物与 Top 10 媒体（按平均情绪着色） |

筛选器：快捷区间（3 / 7 / 30 天）+ 自定义 UTC 日期区间。事件面板最长支持
90 天；主题/实体（GKG）面板最长 30 天且独立加载，不会拖慢事件图表。

#### 数据含义

GDELT 是*新闻报道*的索引，而非经核实事件的登记册。每行是从一篇新闻报道中
机器编码出的一条"谁对谁做了什么"，因此同一现实事件被多家媒体报道会产生
多行 —— 计数度量的是**媒体关注度**，这正是舆情看板应呈现的语义。

- **报道日期（Date reported）** — GDELT 抓取该报道的 UTC 日期
  （`_PARTITIONDATE`），并非事件实际发生日期。对"当下新闻在报道什么"
  这正是所需的时间轴；它同时也是分区键，所有查询只扫描所选天数的分区。
- **情绪 Tone** — 描述该事件的报道文本的平均情绪值，来自 GDELT 情感
  引擎。理论区间 −100…+100，实际几乎都落在 −10…+10：低于 −2 为明显
  负面报道，高于 +2 为正面。
- **Goldstein 分（−10…+10）** — 政治学标准量表，衡量某*事件类型*对国家
  稳定性的理论影响（如"提供援助"强正面、"战斗"强负面）。该分值按
  CAMEO 事件类型固定 —— 评价的是行为种类，而非单篇报道。
- **QuadClass（1–4）** — GDELT 最粗粒度的事件分类：言语合作、实质合作、
  言语冲突、实质冲突。第 3–4 类构成"冲突占比"指标与突发新闻列表。
- **CAMEO 根编码（'01'–'20'）** —
  [CAMEO 分类体系](http://data.gdeltproject.org/documentation/CAMEO.Manual.1.1b3.pdf)
  的 20 个顶层事件类别（呼吁、磋商、威胁、抗议、战斗……），'10' 及以上
  为冲突侧。API 返回原始编码，标签映射在前端完成。
- **提及数 Mentions** — 事件在全部被监测文档中被提及的次数
  （`NumMentions`），是突发新闻表的排序信号。
- **主题 / 人物（GKG）** — 来自全球知识图谱（Global Knowledge Graph），
  它为每篇*文章*标注主题（如 `PROTEST`、`WB_2670_JOBS`）与人物。权重为
  **文章数**：一篇文章提及某主题十次也只计一次，避免长文偏置 Treemap。
- **媒体情绪** — 该媒体在区间内所有文章的文档情绪（GKG `V2Tone` 复合串
  第 0 段）的平均值。

#### 指标如何计算

- **加权平均，绝不做"均值的均值"。** BigQuery 按（日期 × QuadClass ×
  事件类型）分组返回每组的 `AVG` 与 `COUNT`，Go 后端按
  `Σ(avg×n)/Σ(n)` 上卷 —— 与直接对明细求平均在数学上完全等价。若对
  分组均值做简单平均，一个 10 条事件的小组会与 10 万条事件的大组拥有
  同等权重，扭曲全局情绪。
- **地理热点**：事件坐标取整到 0.1°（约 11 km）网格后按格聚合，地图
  展示最繁忙的 500 个网格。
- **突发新闻**按 `SOURCEURL` 去重（每篇文章仅保留提及数最高的事件行）——
  GDELT 会为同一篇报道生成多条事件行，否则单一大新闻会刷屏 Top 50。
- **成本防护**：原生 `DATE` 参数直连分区键、硬性跨度上限（90 / 30 天）、
  服务端 `GROUP BY + LIMIT`、共享 10 分钟缓存，以及 `singleflight` 请求
  合并（并发相同请求只触发一次 BigQuery 作业）。默认 3 天窗口的一次
  全量缓存未命中扫描量远低于 1 GB —— 按需计费下不足一美分。

### 新增公共数据集仪表盘

1. 后端：新建 `backend/opendata_<name>.go`（行类型 + `BQClient` 方法）与
   `backend/opendata_<name>_handlers.go`，路由挂在
   `/api/opendata/<name>/*` 下。
2. 前端：在 `frontend/src/opendata/` 中实现仪表盘组件，并在
   `frontend/src/opendata/registry.tsx` 注册 —— 侧边栏入口、标题与路由自动生效。

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
