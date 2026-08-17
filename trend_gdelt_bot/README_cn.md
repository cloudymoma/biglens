# Google Trends & GDELT BigQuery 对话式分析智能体

[English](README.md) | 简体中文

一个独立的、可直接投入使用的软件包，用于在 BigQuery 中构建**对话式 AI 数据智能体（Conversational AI Data Agent）**，对 **Google Trends** 搜索趋势与 **GDELT 2.0** 全球新闻情绪及地缘政治事件进行查询、关联与解读。

---

## 📁 目录结构

```text
trend_gdelt_bot/
├── init.sh                                      # 自动化部署脚本（gcloud + bq + kcmd）
├── README.md                                    # 英文指南与 BigQuery UI 教程
├── README_cn.md                                 # 本中文指南
│
├── bigquery/                                    # 语义数据层与 SQL 资产
│   ├── 01_dataset_setup.sql                     # 数据集 DDL + FIPS 10-4 → ISO 3166-1 国家代码映射表
│   ├── 02_views_search_trends.sql               # Trends 精选视图（固定取最新趋势周）
│   ├── 03_views_gdelt_events.sql                # GDELT 每日事件与 GKG 主题精选视图（CAMEO 解码）
│   ├── 04_views_trends_gdelt_unified.sql        # 搜索热词 × 新闻态势统一分析宽表
│   ├── 05_property_graph.sql                    # BigQuery 属性图 DDL（NODE TABLES / ISO GQL）
│   ├── 06_golden_agent_queries.sql              # 供 AI 智能体使用的 few-shot 验证查询（Tier 1 + Tier 2）
│   ├── 07_graph_golden_query.sql                # 图查询 GQL 示例（需 Enterprise 版本预留）
│   └── 08_views_tier2_raw.sql                   # Tier 2 原始下钻代理视图（完整历史、美国 DMA、小时级实时、飙升明细、GKG 实体）
│
└── knowledge_catalog/                           # 开放知识格式（OKF）知识包
    ├── index.md                                 # 目录根索引（okf_version: "0.1"）
    ├── datasets/                                # 数据集概念（Google Trends、GDELT、Analytics）
    ├── tables/                                  # 表结构 + dim_fips_iso_country
    ├── views/                                   # 精选语义视图与血缘
    ├── metrics/                                 # 指标公式（Score、Rank、Goldstein、Tone 等）
    ├── dimensions/                              # 维度定义（国家、CAMEO、QuadClass 等）
    └── glossary/                                # 业务规则与查询路由逻辑
```

---

## ⚡ 一分钟自动化部署（`init.sh`）

[`init.sh`](./init.sh) 一键部署整个 BigQuery 语义层：创建 FIPS → ISO 映射表、构建属性图，并通过 `kcmd` 注册数据集的知识目录 scope。所有表、视图和图的 Dataplex 目录条目由 BigQuery 自动同步，携带 SQL DDL 中内嵌的描述信息；`knowledge_catalog/` 中的 OKF markdown 知识包则直接用于智能体的知识接地（见下方步骤 3）。

脚本是**幂等的** —— 可随时重复执行，以应用 SQL 变更并基于最新数据重建属性图快照表（也可以定时调度，例如每天一次，保持图数据新鲜）。

### 前置条件

1. **Google Cloud CLI（`gcloud` 与 `bq`）**
   * **安装：** 参照 [Google Cloud CLI 官方安装指南](https://cloud.google.com/sdk/docs/install) 安装 `gcloud` 及 `bq` 组件。
   * **认证与项目配置：**
     ```bash
     gcloud auth login
     gcloud auth application-default login
     gcloud config set project <YOUR_PROJECT_ID>
     ```

2. **知识目录 CLI（`kcmd`）**（可选 —— 为 kcmd pull/push 工作流注册数据集 scope）
   * **安装指南：** 参照 [GoogleCloudPlatform/knowledge-catalog README](https://github.com/GoogleCloudPlatform/knowledge-catalog#readme) 构建 `kcmd` 并加入 `PATH`。
   * *注：若未安装 `kcmd`，`init.sh` 会跳过 scope 注册，其余部署照常完成。Dataplex 目录条目无论如何都会从 BigQuery 自动同步，`knowledge_catalog/` 中的本地 markdown 文件也依然可以直接用于智能体接地。*

### 运行部署
```bash
cd trend_gdelt_bot
./init.sh <YOUR_PROJECT_ID>
```
*（不带参数时自动使用当前 `gcloud` 活跃项目。部署位置（`US`）与数据集名称（`trends_gdelt_analytics`）是固定的 —— 所有 SQL 文件按名称引用该数据集，且源公共数据集位于 US 多区域。）*

---

## 🤖 教程：在 BigQuery UI 中构建对话式智能体

`init.sh` 部署完视图后，按以下步骤在 **Google Cloud Console** 中配置你的对话式智能体：

### 步骤 1：打开 BigQuery Studio / Conversational Analytics
1. 进入 [Google Cloud Console](https://console.cloud.google.com/)。
2. 导航到 **BigQuery** → **BigQuery Studio**。
3. 在左侧或顶部导航中选择 **Data Canvas** / **Conversational Analytics**（或 **Create Agent** / **Gemini in BigQuery**）。

---

### 步骤 2：配置数据源（双层架构）
在 BigQuery 智能体配置中，加入 **`trends_gdelt_analytics` 数据集**（或逐个选择下方的视图和表）。双层设计让智能体对常规问题给出快速、安全的回答，同时保留深度下钻能力。

> [!IMPORTANT]
> **切勿将外部公共数据集（`gdelt-bq.*` 或 `bigquery-public-data.*`）直接添加为智能体数据源。** BigQuery Conversational Analytics 强制执行跨组织资源边界，所有挂载的数据源必须位于你自己的 Google Cloud 项目中。这正是下方 Tier 2 **代理视图（proxy view）** 存在的意义：视图对象位于 `trends_gdelt_analytics`（智能体可无跨组织错误地挂载），而其 SQL 在查询时仍会解析到底层公共表。

#### 🔹 Tier 1：精选语义层（首选，默认）
加入整个 `<YOUR_PROJECT_ID>.trends_gdelt_analytics` 数据集（或逐个添加视图）：

| 表 / 视图 / 图 | 用途及智能体何时使用 |
| :--- | :--- |
| **`vw_topic_news_trends_unified`**（主宽表） | **宏观关联：** 关联搜索趋势与国家级新闻情绪、Goldstein 稳定性、冲突占比等问题的默认视图。 |
| **`vw_search_trends_rising`** | **飙升热词：** 爆发式搜索词及其周环比涨幅（`percent_gain`）。 |
| **`vw_search_trends_daily`** | **排名与地区覆盖：** 每日 Top 25 排名、地区覆盖数与峰值标记。（DMA 都会区数据仅限美国 —— 见 Tier 2 的 `top_terms`。） |
| **`vw_gdelt_news_events_daily`** | **细粒度事件引用：** 具体行为体对（`primary_actor`、`secondary_actor`）、CAMEO 类别与新闻源 URL（`source_article_url`）。 |
| **`vw_gdelt_gkg_themes_daily`** | **主题分析：** GKG 主要新闻主题、媒体域名与情绪向量。**仅覆盖最近 30 天**（出于成本考虑 GKG 窗口刻意收紧）。 |
| **`dim_fips_iso_country`** | **国家维度：** FIPS 10-4 ↔ ISO 3166-1 国家代码对照表。 |
| **`trend_gdelt_graph`**（属性图 - 预览） | **图模式匹配：** 原生属性图，用于跨国热词重叠、双边对比及扩散网络分析（ISO GQL / `GRAPH_TABLE`）。⚠️ **需要 BigQuery Enterprise（或 Enterprise Plus）版本预留** —— 按需计费下 `GRAPH_TABLE` 查询会被拒绝。 |

#### 🔹 Tier 2：原始下钻代理视图（按明确请求使用 —— 深度历史与细粒度下钻）
以下代理视图同样位于 `trends_gdelt_analytics` 中（由 `08_views_tier2_raw.sql` 部署），解锁 90 天精选窗口之外的深度下钻能力：

| Tier 2 代理视图 | 底层公共表 | 解锁哪些下钻能力？ |
| :--- | :--- | :--- |
| **`vw_raw_trends_international_history`** | `bigquery-public-data.google_trends.international_top_terms` | 每个词完整约 5 年的周度历史趋势曲线（按国家/地区）。 |
| **`vw_raw_trends_international_rising_history`** | `bigquery-public-data.google_trends.international_top_rising_terms` | 地区（省/州）级飙升词及各地区 `percent_gain` 与周度历史。 |
| **`vw_raw_trends_us_dma`** | `bigquery-public-data.google_trends.top_terms` | 美国指定市场区域（Nielsen DMA）都会区级别的细粒度排名。 |
| **`vw_raw_trends_us_dma_rising`** | `bigquery-public-data.google_trends.top_rising_terms` | 美国 DMA 级飙升词及各都会区 `percent_gain`。 |
| **`vw_raw_trends_us_hourly`** | `bigquery-public-data.google_trends_hourly.top_terms_hourly` | **实时：** 美国 DMA 级日内 Top 25，每天多个快照（快照保留约 30 天，每快照约 1 年周度历史）。最新鲜数据源 —— 每日表滞后 1–2 天。 |
| **`vw_raw_trends_us_hourly_rising`** | `bigquery-public-data.google_trends_hourly.top_rising_terms_hourly` | **实时：** 美国 DMA 级日内飙升词及 `percent_gain`。 |
| **`vw_raw_gdelt_events_archive`** | `gdelt-bq.gdeltv2.events_partitioned` | 多年历史新闻事件档案（2015 年 2 月至今）、300+ 细分 CAMEO 子编码、行为体类型编码与精确坐标。 |
| **`vw_raw_gdelt_gkg_entities_archive`** | `gdelt-bq.gdeltv2.gkg_partitioned` | 文章级实体清单：被提及人物（`persons`）、组织（`organizations`）、主题（`themes`）均为已清洗数组，附完整情绪向量；滚动 2 年窗口（视图内置硬性成本上限）。 |

> [!NOTE]
> 若智能体的数据源数量受限，优先保留 Tier 1 加两个 GDELT 档案视图与 `vw_raw_trends_us_hourly` —— 它们解锁 Tier 1 完全无法回答的能力（深度历史、实体、实时性；每日 Trends 表滞后 1–2 天）。另建议为原始视图查询设置 `maximum_bytes_billed`（项目级查询上限）或自定义配额作为成本兜底。

---

### 步骤 3：配置智能体系统指令与业务规则
将以下领域知识与路由规则（源自 `knowledge_catalog/glossary/`）粘贴到智能体的 **System Instructions / Business Rules** 提示框中（提示词保持英文即可直接使用，智能体可正常响应中文提问）：

```text
You are a specialized analytical assistant for Google Trends and GDELT 2.0 geopolitical news data.
Always follow this Two-Tier routing hierarchy and domain rules:

1. ROUTING HIERARCHY — TIER 1 (CURATED, DEFAULT):
   Always prefer the Tier 1 curated views for standard analytics, recent trends (last 90 days; GKG themes last 30 days), and cross-dataset correlations:
   - For macro correlations (search trends + country news context): Query `trends_gdelt_analytics.vw_topic_news_trends_unified`.
   - For breakout/surging terms & % growth (e.g. rising queries in Japan/US): Query `trends_gdelt_analytics.vw_search_trends_rising`.
   - For daily search rankings & regional spread: Query `trends_gdelt_analytics.vw_search_trends_daily`.
   - For specific news events, actor dyads, or article URLs: Query `trends_gdelt_analytics.vw_gdelt_news_events_daily`.
   - For news themes & media outlets (last 30 days ONLY): Query `trends_gdelt_analytics.vw_gdelt_gkg_themes_daily`.
   - For country code conversions: Use `trends_gdelt_analytics.dim_fips_iso_country`.
   - Cross-country term overlap, bilateral comparisons, or graph diffusion networks: Query `trends_gdelt_analytics.trend_gdelt_graph` using GRAPH_TABLE and GQL pattern matching — ONLY if the project has an Enterprise/Enterprise Plus reservation. On on-demand billing GRAPH_TABLE fails; answer the same questions with self-joins or GROUP BY on `trends_gdelt_analytics.vw_search_trends_daily` instead.

2. ROUTING HIERARCHY — TIER 2 (RAW DRILL-DOWN, ON EXPLICIT REQUEST ONLY):
   Use the Tier 2 raw proxy views ONLY when the user explicitly asks for data outside the Tier 1 windows or granularity:
   - Multi-year historical trend trajectories per term: Query `trends_gdelt_analytics.vw_raw_trends_international_history`.
   - Region-level rising terms & per-region percent gains: Query `trends_gdelt_analytics.vw_raw_trends_international_rising_history`.
   - US metro / Designated Market Area (DMA) breakdowns: Query `trends_gdelt_analytics.vw_raw_trends_us_dma` (top terms) or `trends_gdelt_analytics.vw_raw_trends_us_dma_rising` (breakouts with percent_gain).
   - News events older than 90 days, full CAMEO subcodes, or actor type codes: Query `trends_gdelt_analytics.vw_raw_gdelt_events_archive`.
   - Person/organization entity mentions, or themes older than 30 days: Query `trends_gdelt_analytics.vw_raw_gdelt_gkg_entities_archive` (rolling 2-year window).

3. REAL-TIME EXCEPTION — "RIGHT NOW / TODAY" QUESTIONS (US ONLY):
   The daily views lag 1-2 days. For US questions about the current moment or today ("what is trending right now", "what is spiking today"), route PROACTIVELY to the intraday hourly views (several snapshots per day, ~30-day snapshot retention):
   - Current US top terms: `trends_gdelt_analytics.vw_raw_trends_us_hourly`.
   - Current US breakouts: `trends_gdelt_analytics.vw_raw_trends_us_hourly_rising`.
   For non-US "right now" questions, use the latest Tier 1 snapshot and state the 1-2 day lag.

4. GUARDRAILS FOR TIER 2 RAW VIEWS:
   - ALWAYS filter `partition_date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` (or an explicit BETWEEN range) on the GDELT archive views — they span many years and terabytes.
   - On `vw_raw_trends_*`, each snapshot (snapshot_date, or snapshot_time on hourly views) carries the FULL weekly history (~5 years daily, ~1 year hourly):
     * For historical curves: pin `snapshot_date = (SELECT MAX(snapshot_date) FROM <view>)` and scan `week`. NEVER range over snapshots for history — that averages overlapping histories into garbage.
     * For current single-snapshot values: pin the snapshot as above AND `week = MAX(week)`.
     * On hourly views pin `snapshot_time = (SELECT MAX(snapshot_time) FROM <view>)` (DATETIME, not DATE).
   - Rank vs breadth on DMA/region views: aggregate with MIN(rank), MAX(percent_gain), COUNT(DISTINCT dma_name / region_name) for national/country rollups.
   - In `vw_raw_gdelt_gkg_entities_archive`, `persons`/`organizations`/`themes` are ARRAY<STRING>; filter with `'Name' IN UNNEST(persons)`.
   - NEVER join `fips_country_code` to ISO country codes directly; the archive views already expose mapped ISO `country_code`.

5. SEARCH SCORE VS. RANK:
   - `search_rank` (1–25) is the cross-sectional popularity hierarchy on that day. Sort top-term leaderboards by `search_rank ASC`.
   - `search_score` (0–100) measures interest relative to the term's OWN historical peak share (100 = all-time peak for that specific term).
   - High rank (e.g. #1 "weather") does NOT imply score 100. A lower-ranking term (e.g. #15 "eclipse") CAN have score 100 if it is at its all-time spike.
   - `is_historical_peak = TRUE` indicates `search_score = 100`.

6. GDELT NEWS METRICS & SENTIMENT:
   - `country_avg_tone` / `sentiment_tone`: Sentiment tone from -100 to +100. Real-world values fall between -10 and +10 (< -2.0 is clearly negative, > +2.0 is positive).
   - `country_avg_goldstein` / `goldstein_scale`: Theoretical stability impact (-10.0 extreme conflict/destabilizing to +10.0 high cooperation).
   - `conflict_event_share_pct`: Share of daily events classified as Verbal Conflict (QuadClass 3) or Material Conflict (QuadClass 4).
   - `country_daily_media_mentions`: Volume of media attention pulse across news articles.

7. PARTITION & DATE PRUNING:
   - Always filter `date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` or `report_date >= ...`.
   - Google Trends snapshots lag 1–2 days; pin the latest snapshot via:
     QUALIFY date = MAX(date) OVER ()
```

其中的核心业务规则包括：

* **双层路由：** 常规分析（近 90 天；GKG 主题近 30 天）一律走 Tier 1 精选视图；只有当用户明确要求更长历史、美国 DMA 都会区、地区级飙升明细或 GKG 人物/组织实体时，才下钻到 Tier 2 本地代理视图（`vw_raw_*`）。查询 GDELT 档案视图必须过滤 `partition_date`；查询 Trends 历史视图必须锁定最新 `snapshot_date` 再沿 `week` 展开曲线。
* **实时例外：** 每日表滞后 1–2 天。美国"此刻/今天在搜什么"类问题应主动路由到小时级视图（`vw_raw_trends_us_hourly` / `vw_raw_trends_us_hourly_rising`，锁定 `snapshot_time = MAX(snapshot_time)`）；非美国的实时问题使用 Tier 1 最新快照并向用户说明滞后。
* **国家代码防护：** GDELT 使用 FIPS 10-4，Trends 使用 ISO 3166-1（FIPS 的 `GB` 是加蓬、ISO 的 `GB` 是英国！），二者绝不能直接 JOIN，必须经 `dim_fips_iso_country` 转换。
* **Rank 与 Score 的区别：** `search_rank`（1–25）是当日绝对搜索量的横向排名；`search_score`（0–100）是相对该词自身历史峰值的热度（100 = 创历史新高）。排名第 1 不代表分数 100。
* **分区裁剪：** 所有查询必须带日期过滤，避免对 TB 级底表全表扫描；Trends 快照滞后 1–2 天，用 `QUALIFY date = MAX(date) OVER ()` 锁定最新快照。

---

### 步骤 4：添加验证查询（Golden Queries）
将 [`bigquery/06_golden_agent_queries.sql`](./bigquery/06_golden_agent_queries.sql) 中的 few-shot 查询示例复制到智能体的 **Verified Queries** 配置中（图查询示例位于 [`bigquery/07_graph_golden_query.sql`](./bigquery/07_graph_golden_query.sql) —— 仅当项目拥有 Enterprise 预留时才添加）：

* **今日热搜词：**
  ```sql
  SELECT search_term, search_rank, search_score, is_historical_peak
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE country_code = 'GB' AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
  QUALIFY date = MAX(date) OVER ()
  ORDER BY search_rank ASC LIMIT 10;
  ```
* **搜索飙升 × 负面新闻背景：**
  ```sql
  SELECT date, country_name, country_avg_tone, dominant_news_category, search_term, search_rank, search_score
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE date >= DATE_SUB(CURRENT_DATE(), INTERVAL 14 DAY)
    AND country_avg_tone < -2.0 AND conflict_event_share_pct > 30.0
  ORDER BY conflict_event_share_pct DESC, search_rank ASC LIMIT 20;
  ```
* **历史峰值爆发词：**
  ```sql
  SELECT date, search_term, search_rank, search_score
  FROM `trends_gdelt_analytics.vw_topic_news_trends_unified`
  WHERE country_code = 'US' AND is_historical_peak = TRUE AND date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
  ORDER BY date DESC, search_rank ASC;
  ```
* **Tier 2 下钻 —— 5 年趋势曲线（锁定快照、沿周展开）：**
  ```sql
  SELECT search_term, week, CAST(AVG(search_score) AS INT64) AS avg_weekly_score
  FROM `trends_gdelt_analytics.vw_raw_trends_international_history`
  WHERE snapshot_date = (SELECT MAX(snapshot_date) FROM `trends_gdelt_analytics.vw_raw_trends_international_history`)
    AND country_code = 'GB' AND rank = 1
  GROUP BY search_term, week
  ORDER BY week ASC;
  ```
* **Tier 2 实时 —— 美国此刻热搜（同时锁定 snapshot_time 与 week）：**
  ```sql
  WITH latest_snapshot AS (
    SELECT * FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`
    WHERE snapshot_time = (SELECT MAX(snapshot_time) FROM `trends_gdelt_analytics.vw_raw_trends_us_hourly`)
    QUALIFY week = MAX(week) OVER ()
  )
  SELECT search_term, MIN(rank) AS best_rank, CAST(AVG(search_score) AS INT64) AS avg_dma_score,
         COUNT(DISTINCT dma_name) AS active_dma_count
  FROM latest_snapshot
  GROUP BY search_term
  ORDER BY best_rank ASC LIMIT 25;
  ```
* **图遍历 / 双边重叠（GQL via GRAPH_TABLE —— 需 Enterprise 版本预留）：**
  ```sql
  SELECT *
  FROM GRAPH_TABLE(
    `trends_gdelt_analytics.trend_gdelt_graph`
    MATCH (t:SearchTerm)-[e1:TRENDED_IN]->(c1:Country {country_code: 'GB'}),
          (t)-[e2:TRENDED_IN]->(c2:Country {country_code: 'FR'})
    WHERE e1.snapshot_date = e2.snapshot_date
      AND e1.rank <= 10 AND e2.rank <= 10
    COLUMNS (
      t.search_term,
      e1.snapshot_date AS date,
      e1.rank AS uk_rank,
      e2.rank AS fr_rank,
      e1.search_score AS uk_score,
      e2.search_score AS fr_score
    )
  )
  ORDER BY date DESC, uk_rank ASC
  LIMIT 15;
  ```

---

### 步骤 5：测试自然语言提问

在 BigQuery 对话界面中用以下提问测试智能体（中英文提问均可）：

1. *"最新快照中英国排名前 10 的搜索词是什么？"*
2. *"过去一周美国有哪些搜索词达到了历史峰值热度（分数 100）？"*
3. *"哪些国家的新闻情绪明显负面（Tone < -2.0）且冲突占比高？这些国家的人们在搜索什么？"*
4. *"过去 7 天有哪些搜索词同时在 3 个及以上国家上榜？"*
5. *"为什么排名第 12 的词分数是 100，而排名第 1 的词分数只有 70？"*
6. *"哪些搜索词在同一天同时进入英国和法国的前 10？对比它们的排名。"*
7. *"展示英国当前排名第 1 的搜索词过去 5 年的热度曲线。"*（Tier 2 下钻）
8. *"2023 年初法国报道量最高的抗议事件有哪些？附文章链接。"*（Tier 2 下钻）
9. *"美国人此刻在搜什么？哪些搜索正在飙升？"*（Tier 2 实时小时级）
10. *"今天排名第一的飙升词在哪些美国都会区爆发最猛？"*（Tier 2 DMA 飙升）

---

## 📊 精选视图与图谱一览

| 视图 / 图名称 | 类型 | 主要数据源 | 关键指标与维度 |
| :--- | :--- | :--- | :--- |
| **`vw_search_trends_daily`** | 视图 | `google_trends.international_top_terms` | `snapshot_date`、`country_code`（ISO）、`search_term`、`rank`、`search_score`（固定最新周）、`is_historical_peak`。 |
| **`vw_search_trends_rising`** | 视图 | `google_trends.international_top_rising_terms` | `snapshot_date`、`country_code`、`search_term`、`max_percent_gain`、`avg_percent_gain`。 |
| **`vw_gdelt_news_events_daily`** | 视图 | `gdelt-bq.gdeltv2.events_partitioned` | `report_date`、`country_code`（映射为 ISO）、`event_category`（CAMEO 解码）、`quad_class_name`、`goldstein_scale`、`sentiment_tone`、`source_article_url`。 |
| **`vw_topic_news_trends_unified`** | 视图 | *统一分析宽表* | **智能体主宽表：** 关联每日搜索词、排名、分数与国家新闻情绪、Goldstein 冲突分及主导新闻类别。 |
| **`trend_gdelt_graph`** | 属性图 | `node_countries`、`node_search_terms`、`edge_trended_in` | **图模型（预览）：** `Country` 与 `SearchTerm` 节点、`TRENDED_IN` 边，通过 ISO GQL / `GRAPH_TABLE` 查询。需 Enterprise/Enterprise Plus 预留。 |
| **`vw_raw_trends_international_history`** | 视图（Tier 2） | `google_trends.international_top_terms` | **下钻：** 未聚合的约 5 年周度热度历史（`snapshot_date`、`week`、`region_name`、`search_score`）。 |
| **`vw_raw_trends_international_rising_history`** | 视图（Tier 2） | `google_trends.international_top_rising_terms` | **下钻：** 地区级飙升词及各地区 `percent_gain` 与周度历史。 |
| **`vw_raw_trends_us_dma`** | 视图（Tier 2） | `google_trends.top_terms` | **下钻：** 美国 Nielsen DMA 都会区级 Top 25（`dma_name`、`dma_id`）及周度历史。 |
| **`vw_raw_trends_us_dma_rising`** | 视图（Tier 2） | `google_trends.top_rising_terms` | **下钻：** 美国 DMA 级飙升词及 `percent_gain`。 |
| **`vw_raw_trends_us_hourly`** | 视图（Tier 2） | `google_trends_hourly.top_terms_hourly` | **实时：** 美国 DMA 级日内 Top 25（`snapshot_time` DATETIME）；每天多快照、保留约 30 天、每快照约 1 年周度历史。 |
| **`vw_raw_trends_us_hourly_rising`** | 视图（Tier 2） | `google_trends_hourly.top_rising_terms_hourly` | **实时：** 美国 DMA 级日内飙升词及 `percent_gain`。 |
| **`vw_raw_gdelt_events_archive`** | 视图（Tier 2） | `gdelt-bq.gdeltv2.events_partitioned` | **下钻：** 2015 年 2 月至今的完整事件档案（`partition_date`、`global_event_id`、完整 CAMEO 编码、行为体类型编码、映射 ISO `country_code`）。 |
| **`vw_raw_gdelt_gkg_entities_archive`** | 视图（Tier 2） | `gdelt-bq.gdeltv2.gkg_partitioned` | **下钻：** 文章级实体数组（`persons`、`organizations`、`themes`）及完整情绪向量；滚动 2 年硬性窗口。 |
