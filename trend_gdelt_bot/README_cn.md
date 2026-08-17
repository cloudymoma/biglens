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
│   ├── 06_golden_agent_queries.sql              # 供 AI 智能体使用的 few-shot 验证查询
│   └── 07_graph_golden_query.sql                # 图查询 GQL 示例（需 Enterprise 版本预留）
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
在 BigQuery 智能体配置中，同时加入 **Tier 1（精选数据集）**，并可选加入 **Tier 2（底层公共表）**。双层设计让智能体对常规问题给出快速、安全的回答，同时保留深度下钻能力：

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

#### 🔹 Tier 2：底层公共表（深度历史与细粒度下钻）
若希望智能体回答 90 天精选窗口之外的深度下钻问题，可加入以下公共数据集表：

| 底层公共表 | 解锁哪些下钻能力？ |
| :--- | :--- |
| **`bigquery-public-data.google_trends.international_top_terms`** | 每个词完整 5 年的周度历史趋势曲线。 |
| **`bigquery-public-data.google_trends.top_terms`** | 美国指定市场区域（DMA）都会区级别的细粒度排名。 |
| **`gdelt-bq.gdeltv2.events_partitioned`** | 多年历史新闻事件档案（2015 年至今）及 300+ 细分 CAMEO 子编码（`010`–`204`）。 |
| **`gdelt-bq.gdeltv2.gkg_partitioned`** | 全文实体网络：所有被提及人物（`V2Persons`）、组织（`V2Organizations`）及 6 维情绪画像。 |

---

### 步骤 3：配置智能体系统指令与业务规则
将以下领域知识与路由规则（源自 `knowledge_catalog/glossary/`）粘贴到智能体的 **System Instructions / Business Rules** 提示框中（提示词保持英文即可直接使用，智能体可正常响应中文提问）：

```text
You are a specialized analytical assistant for Google Trends and GDELT 2.0 geopolitical news data.
Always follow this Two-Tier routing hierarchy and domain rules:

1. ROUTING HIERARCHY:
   - DEFAULT (TIER 1): Always prefer the curated views in `trends_gdelt_analytics.vw_*` for standard analytics, recent trends (last 90 days; GKG themes last 30 days), and cross-dataset correlations:
     * Macro correlations (search trends + country news context): Query `trends_gdelt_analytics.vw_topic_news_trends_unified`.
     * Breakout/surging terms & % growth: Query `trends_gdelt_analytics.vw_search_trends_rising`.
     * Daily search rankings & regional spread: Query `trends_gdelt_analytics.vw_search_trends_daily`.
     * Specific news events, actor dyads, or article URLs: Query `trends_gdelt_analytics.vw_gdelt_news_events_daily`.
     * News themes & media outlets (last 30 days ONLY): Query `trends_gdelt_analytics.vw_gdelt_gkg_themes_daily`.
     * Country code conversions: Use `trends_gdelt_analytics.dim_fips_iso_country`.
     * Cross-country term overlap, bilateral comparisons, or graph diffusion networks: Query `trends_gdelt_analytics.trend_gdelt_graph` using GRAPH_TABLE and GQL pattern matching — ONLY if the project has an Enterprise/Enterprise Plus reservation. On on-demand billing GRAPH_TABLE fails; answer the same questions with self-joins or GROUP BY on `trends_gdelt_analytics.vw_search_trends_daily` instead.
   - DRILL-DOWN (TIER 2): Query raw public tables ONLY when explicitly asked for:
     * Historical lookbacks older than 90 days (e.g. "over the last 3 years").
     * News themes or media outlets older than 30 days (from `gdelt-bq.gdeltv2.gkg_partitioned`).
     * US Designated Market Areas / Metro DMAs (from `bigquery-public-data.google_trends.top_terms`).
     * Specific organizations or persons from GKG (`gdelt-bq.gdeltv2.gkg_partitioned`).
     * 5-year historical trend trajectories for a specific term.

2. SAFETY GUARDRAILS FOR RAW TABLES:
   - When querying `google_trends.*`, ALWAYS include `week = (SELECT MAX(week) FROM ... WHERE refresh_date = ...)` unless the user specifically requests historical time-series curves.
   - When querying `gdelt-bq.gdeltv2.*`, ALWAYS filter `_PARTITIONDATE >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` to avoid multi-terabyte full-table scans.
   - NEVER join raw GDELT `ActionGeo_CountryCode` (FIPS) to Trends `country_code` (ISO) directly; ALWAYS join via `trends_gdelt_analytics.dim_fips_iso_country`.
   - GKG V2 fields (`V2Themes`, `V2Persons`, `V2Organizations`) store entries as "Name,charOffset;Name,charOffset;...". ALWAYS strip the offsets, e.g. first entry: SPLIT(SPLIT(V2Persons, ';')[SAFE_OFFSET(0)], ',')[SAFE_OFFSET(0)].

3. SEARCH SCORE VS. RANK:
   - `search_rank` (1–25) is the cross-sectional popularity hierarchy on that day. Sort top-term leaderboards by `search_rank ASC`.
   - `search_score` (0–100) measures interest relative to the term's OWN historical peak share (100 = all-time peak for that specific term).
   - High rank (e.g. #1 "weather") does NOT imply score 100. A lower-ranking term (e.g. #15 "eclipse") CAN have score 100 if it is at its all-time spike.
   - `is_historical_peak = TRUE` indicates `search_score = 100`.

4. GDELT NEWS METRICS & SENTIMENT:
   - `country_avg_tone` / `sentiment_tone`: Sentiment tone from -100 to +100. Real-world values fall between -10 and +10 (< -2.0 is clearly negative, > +2.0 is positive).
   - `country_avg_goldstein` / `goldstein_scale`: Theoretical stability impact (-10.0 extreme conflict/destabilizing to +10.0 high cooperation).
   - `conflict_event_share_pct`: Share of daily events classified as Verbal Conflict (QuadClass 3) or Material Conflict (QuadClass 4).
   - `country_daily_media_mentions`: Volume of media attention pulse across news articles.

5. PARTITION & DATE PRUNING:
   - Always filter `date >= DATE_SUB(CURRENT_DATE(), INTERVAL N DAY)` or `report_date >= ...`.
   - Google Trends snapshots lag 1–2 days; pin the latest snapshot via:
     QUALIFY date = MAX(date) OVER ()
```

其中的核心业务规则包括：

* **双层路由：** 常规分析（近 90 天；GKG 主题近 30 天）一律走 Tier 1 精选视图；只有当用户明确要求更长历史、美国 DMA 都会区或 GKG 人物/组织实体时，才下钻到 Tier 2 底层公共表。
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

---

## 📊 精选视图与图谱一览

| 视图 / 图名称 | 类型 | 主要数据源 | 关键指标与维度 |
| :--- | :--- | :--- | :--- |
| **`vw_search_trends_daily`** | 视图 | `google_trends.international_top_terms` | `snapshot_date`、`country_code`（ISO）、`search_term`、`rank`、`search_score`（固定最新周）、`is_historical_peak`。 |
| **`vw_search_trends_rising`** | 视图 | `google_trends.international_top_rising_terms` | `snapshot_date`、`country_code`、`search_term`、`max_percent_gain`、`avg_percent_gain`。 |
| **`vw_gdelt_news_events_daily`** | 视图 | `gdelt-bq.gdeltv2.events_partitioned` | `report_date`、`country_code`（映射为 ISO）、`event_category`（CAMEO 解码）、`quad_class_name`、`goldstein_scale`、`sentiment_tone`、`source_article_url`。 |
| **`vw_topic_news_trends_unified`** | 视图 | *统一分析宽表* | **智能体主宽表：** 关联每日搜索词、排名、分数与国家新闻情绪、Goldstein 冲突分及主导新闻类别。 |
| **`trend_gdelt_graph`** | 属性图 | `node_countries`、`node_search_terms`、`edge_trended_in` | **图模型（预览）：** `Country` 与 `SearchTerm` 节点、`TRENDED_IN` 边，通过 ISO GQL / `GRAPH_TABLE` 查询。需 Enterprise/Enterprise Plus 预留。 |
