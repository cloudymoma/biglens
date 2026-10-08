package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"cloud.google.com/go/bigquery"
	"golang.org/x/sync/errgroup"
	bqv2 "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type BQClient struct {
	client *bigquery.Client
	config *Config
	rls    rlsAPI // row access policies have no INFORMATION_SCHEMA view
}

func NewBQClient(ctx context.Context, cfg *Config) (*BQClient, error) {
	var opts []option.ClientOption
	if cfg.BigQuery.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.BigQuery.CredentialsPath))
	}

	client, err := bigquery.NewClient(ctx, cfg.BigQuery.ProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bigquery client: %w", err)
	}
	if err := client.EnableStorageReadClient(ctx, opts...); err != nil {
		slog.Warn("bigquery storage read client unavailable, using REST pagination", "error", err)
	}

	// Same credentials and scope as the client above, which builds on this
	// REST service too but does not expose it.
	svc, err := bqv2.NewService(ctx, append([]option.ClientOption{option.WithScopes(bigquery.Scope)}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bigquery REST service: %w", err)
	}

	return &BQClient{
		client: client,
		config: cfg,
		rls:    bqRLSAPI{svc: svc, project: cfg.BigQuery.ProjectID},
	}, nil
}

func (b *BQClient) regionRef(region string) string {
	return fmt.Sprintf("`%s`.`region-%s`", b.config.BigQuery.ProjectID, region)
}

// --- Widget 1.1: Logical vs. Physical Billing Simulator ---

type StorageStats struct {
	LogicalBytes     int64 `json:"logical_bytes" bigquery:"logical_bytes"`
	PhysicalBytes    int64 `json:"physical_bytes" bigquery:"physical_bytes"`
	TotalBytes       int64 `json:"total_bytes" bigquery:"total_bytes"`
	ActiveLogical    int64 `json:"active_logical" bigquery:"active_logical"`
	LongTermLogical  int64 `json:"long_term_logical" bigquery:"long_term_logical"`
	ActivePhysical   int64 `json:"active_physical" bigquery:"active_physical"`
	LongTermPhysical int64 `json:"long_term_physical" bigquery:"long_term_physical"`
	TimeTravel       int64 `json:"time_travel" bigquery:"time_travel"`
	FailSafe         int64 `json:"fail_safe" bigquery:"fail_safe"`
}

func storageBaseTableWhere(where string) string {
	if where == "" {
		return " WHERE table_type = 'BASE TABLE'"
	}
	return where + " AND table_type = 'BASE TABLE'"
}

type storageRollupRow struct {
	Dataset          string `bigquery:"dataset"`
	IsRollup         int64  `bigquery:"is_rollup"`
	BillingModel     string `bigquery:"billing_model"`
	ActiveLogical    int64  `bigquery:"active_logical"`
	LongTermLogical  int64  `bigquery:"long_term_logical"`
	ActivePhysical   int64  `bigquery:"active_physical"`
	LongTermPhysical int64  `bigquery:"long_term_physical"`
	TimeTravel       int64  `bigquery:"time_travel"`
	FailSafe         int64  `bigquery:"fail_safe"`
}

func storageOverviewSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`WITH ds_models AS (
			SELECT schema_name, IFNULL(option_value, 'LOGICAL') AS billing_model
			FROM %[1]s.INFORMATION_SCHEMA.SCHEMATA_OPTIONS
			WHERE option_name = 'storage_billing_model'
		)
		SELECT
			IFNULL(table_schema, '') AS dataset,
			GROUPING(table_schema) AS is_rollup,
			IFNULL(ANY_VALUE(dm.billing_model), 'LOGICAL') AS billing_model,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_logical,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_logical,
			COALESCE(SUM(active_physical_bytes), 0) AS active_physical,
			COALESCE(SUM(long_term_physical_bytes), 0) AS long_term_physical,
			COALESCE(SUM(time_travel_physical_bytes), 0) AS time_travel,
			COALESCE(SUM(fail_safe_physical_bytes), 0) AS fail_safe
		FROM %[1]s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT
		LEFT JOIN ds_models dm ON dm.schema_name = table_schema%[2]s
		GROUP BY ROLLUP(table_schema)
		ORDER BY is_rollup DESC, (active_logical + long_term_logical) DESC`,
		regionRef, storageBaseTableWhere(where))
}

func rollupStorageOverview(rows []storageRollupRow) (*StorageStats, *StorageBreakdown, []DatasetStorage) {
	stats := &StorageStats{}
	bd := &StorageBreakdown{}
	datasets := make([]DatasetStorage, 0, len(rows))
	for _, r := range rows {
		if r.IsRollup == 1 {
			logical := r.ActiveLogical + r.LongTermLogical
			physical := r.ActivePhysical + r.LongTermPhysical + r.FailSafe
			stats = &StorageStats{
				LogicalBytes:     logical,
				PhysicalBytes:    physical,
				TotalBytes:       logical,
				ActiveLogical:    r.ActiveLogical,
				LongTermLogical:  r.LongTermLogical,
				ActivePhysical:   r.ActivePhysical,
				LongTermPhysical: r.LongTermPhysical,
				TimeTravel:       r.TimeTravel,
				FailSafe:         r.FailSafe,
			}
			bd = &StorageBreakdown{
				ActiveBytes:   r.ActiveLogical,
				LongTermBytes: r.LongTermLogical,
			}
			continue
		}
		model := r.BillingModel
		if model == "" {
			model = "LOGICAL"
		}
		datasets = append(datasets, DatasetStorage{
			Dataset:          r.Dataset,
			BillingModel:     model,
			ActiveLogical:    r.ActiveLogical,
			LongTermLogical:  r.LongTermLogical,
			ActivePhysical:   r.ActivePhysical,
			LongTermPhysical: r.LongTermPhysical,
			TimeTravel:       r.TimeTravel,
			FailSafe:         r.FailSafe,
		})
	}
	return stats, bd, datasets
}

// GetStorageOverview computes full-project StorageStats, StorageBreakdown, and
// the top 50 DatasetStorage rows in a single GROUP BY ROLLUP query so that
// top-level cost cards reflect all datasets (not just the top 50) and deleted
// tables are excluded from logical bytes while retained for physical TT/FS.
func (b *BQClient) GetStorageOverview(ctx context.Context, filters QueryFilters) (*StorageStats, *StorageBreakdown, []DatasetStorage, error) {
	where, params := filters.StorageWhere()
	q := b.client.Query(storageOverviewSQL(b.regionRef(filters.Region), where))
	q.Parameters = params
	rows, err := collectRows[storageRollupRow](q, ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("storage overview query failed: %w", err)
	}
	stats, bd, ds := rollupStorageOverview(rows)
	return stats, bd, ds, nil
}

func (b *BQClient) GetStorageStats(ctx context.Context, filters QueryFilters) (*StorageStats, error) {
	where, params := filters.StorageWhere()
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes + long_term_logical_bytes, 0)), 0) AS logical_bytes,
			COALESCE(SUM(total_physical_bytes), 0) AS physical_bytes,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes + long_term_logical_bytes, 0)), 0) AS total_bytes,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_logical,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_logical,
			COALESCE(SUM(active_physical_bytes), 0) AS active_physical,
			COALESCE(SUM(long_term_physical_bytes), 0) AS long_term_physical,
			COALESCE(SUM(time_travel_physical_bytes), 0) AS time_travel,
			COALESCE(SUM(fail_safe_physical_bytes), 0) AS fail_safe
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s`,
		b.regionRef(filters.Region), storageBaseTableWhere(where)))
	q.Parameters = params

	rows, err := collectRows[StorageStats](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("storage stats query failed: %w", err)
	}
	if len(rows) == 0 {
		return &StorageStats{}, nil
	}
	return &rows[0], nil
}

// --- Widget 1.2: Active vs. Long-Term Storage Breakdown ---

type StorageBreakdown struct {
	ActiveBytes   int64 `json:"active_bytes" bigquery:"active_bytes"`
	LongTermBytes int64 `json:"long_term_bytes" bigquery:"long_term_bytes"`
}

func (b *BQClient) GetStorageBreakdown(ctx context.Context, filters QueryFilters) (*StorageBreakdown, error) {
	where, params := filters.StorageWhere()
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_bytes,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_bytes
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s`,
		b.regionRef(filters.Region), storageBaseTableWhere(where)))
	q.Parameters = params

	rows, err := collectRows[StorageBreakdown](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("storage breakdown query failed: %w", err)
	}
	if len(rows) == 0 {
		return &StorageBreakdown{}, nil
	}
	return &rows[0], nil
}

// --- Widget 1.3: Top 10 Heaviest Tables ---

type TopTable struct {
	Dataset    string `json:"dataset" bigquery:"dataset"`
	TableName  string `json:"table_name" bigquery:"table_name"`
	TotalBytes int64  `json:"total_bytes" bigquery:"total_bytes"`
}

func (b *BQClient) GetTopTables(ctx context.Context, filters QueryFilters) ([]TopTable, error) {
	where, params := filters.StorageWhere()
	// Dropped tables linger in TABLE_STORAGE until their time-travel and
	// fail-safe windows expire; keep them out of the heaviest-tables list.
	extra := " WHERE deleted = FALSE"
	if where != "" {
		extra = where + " AND deleted = FALSE"
	}
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			table_schema AS dataset,
			table_name,
			(active_logical_bytes + long_term_logical_bytes) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s
		ORDER BY total_bytes DESC
		LIMIT 10`,
		b.regionRef(filters.Region), extra))
	q.Parameters = params

	return collectRows[TopTable](q, ctx)
}

// --- Widget 1.5: Per-Dataset Storage (billing model recommendation) ---

type DatasetStorage struct {
	Dataset          string `json:"dataset" bigquery:"dataset"`
	BillingModel     string `json:"billing_model" bigquery:"billing_model"`
	ActiveLogical    int64  `json:"active_logical" bigquery:"active_logical"`
	LongTermLogical  int64  `json:"long_term_logical" bigquery:"long_term_logical"`
	ActivePhysical   int64  `json:"active_physical" bigquery:"active_physical"`
	LongTermPhysical int64  `json:"long_term_physical" bigquery:"long_term_physical"`
	TimeTravel       int64  `json:"time_travel" bigquery:"time_travel"`
	FailSafe         int64  `json:"fail_safe" bigquery:"fail_safe"`
}

func (b *BQClient) GetDatasetStorage(ctx context.Context, filters QueryFilters) ([]DatasetStorage, error) {
	where, params := filters.StorageWhere()
	q := b.client.Query(fmt.Sprintf(
		`WITH ds_models AS (
			SELECT schema_name, IFNULL(option_value, 'LOGICAL') AS billing_model
			FROM %[1]s.INFORMATION_SCHEMA.SCHEMATA_OPTIONS
			WHERE option_name = 'storage_billing_model'
		)
		SELECT
			table_schema AS dataset,
			IFNULL(ANY_VALUE(dm.billing_model), 'LOGICAL') AS billing_model,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_logical,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_logical,
			COALESCE(SUM(active_physical_bytes), 0) AS active_physical,
			COALESCE(SUM(long_term_physical_bytes), 0) AS long_term_physical,
			COALESCE(SUM(time_travel_physical_bytes), 0) AS time_travel,
			COALESCE(SUM(fail_safe_physical_bytes), 0) AS fail_safe
		FROM %[1]s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT
		LEFT JOIN ds_models dm ON dm.schema_name = table_schema%[2]s
		GROUP BY dataset
		ORDER BY active_logical + long_term_logical DESC
		LIMIT 50`,
		b.regionRef(filters.Region), storageBaseTableWhere(where)))
	q.Parameters = params

	return collectRows[DatasetStorage](q, ctx)
}

// --- Widget 1.6: Cold Tables (no references in the time window) ---

type ColdTable struct {
	Dataset          string `json:"dataset" bigquery:"dataset"`
	TableName        string `json:"table_name" bigquery:"table_name"`
	TotalBytes       int64  `json:"total_bytes" bigquery:"total_bytes"`
	ActiveLogical    int64  `json:"active_logical" bigquery:"active_logical"`
	LongTermLogical  int64  `json:"long_term_logical" bigquery:"long_term_logical"`
	ActivePhysical   int64  `json:"active_physical" bigquery:"active_physical"`
	LongTermPhysical int64  `json:"long_term_physical" bigquery:"long_term_physical"`
	BillingModel     string `json:"billing_model" bigquery:"billing_model"`
	StorageTier      string `json:"storage_tier" bigquery:"storage_tier"`
}

func coldTablesSQL(regionRef, interval, extraWhere string) string {
	return fmt.Sprintf(
		`WITH refs AS (
			SELECT DISTINCT rt.dataset_id, rt.table_id
			FROM %[1]s.INFORMATION_SCHEMA.JOBS_BY_PROJECT j, UNNEST(j.referenced_tables) rt
			WHERE j.creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %[2]s)
				AND rt.project_id = @project
		),
		ds_models AS (
			SELECT schema_name, IFNULL(option_value, 'LOGICAL') AS billing_model
			FROM %[1]s.INFORMATION_SCHEMA.SCHEMATA_OPTIONS
			WHERE option_name = 'storage_billing_model'
		)
		SELECT
			ts.table_schema AS dataset,
			ts.table_name,
			(ts.active_logical_bytes + ts.long_term_logical_bytes) AS total_bytes,
			COALESCE(ts.active_logical_bytes, 0) AS active_logical,
			COALESCE(ts.long_term_logical_bytes, 0) AS long_term_logical,
			COALESCE(ts.active_physical_bytes, 0) + COALESCE(ts.fail_safe_physical_bytes, 0) AS active_physical,
			COALESCE(ts.long_term_physical_bytes, 0) AS long_term_physical,
			IFNULL(dm.billing_model, 'LOGICAL') AS billing_model,
			CASE WHEN ts.long_term_logical_bytes > ts.active_logical_bytes THEN 'LONG_TERM' ELSE 'ACTIVE' END AS storage_tier
		FROM %[1]s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT ts
		LEFT JOIN refs r
			ON r.dataset_id = ts.table_schema AND r.table_id = ts.table_name
		LEFT JOIN ds_models dm
			ON dm.schema_name = ts.table_schema
		WHERE r.table_id IS NULL
			AND ts.deleted = FALSE
			AND ts.table_type = 'BASE TABLE'
			AND (ts.active_logical_bytes + ts.long_term_logical_bytes) > 0
			AND ts.creation_time < TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %[2]s)
			AND NOT STARTS_WITH(ts.table_schema, '_')%[3]s
		ORDER BY total_bytes DESC
		LIMIT 20`,
		regionRef, interval, extraWhere)
}

func (b *BQClient) GetColdTables(ctx context.Context, filters QueryFilters) ([]ColdTable, error) {
	where, params := filters.StorageWhere()
	extra := strings.TrimPrefix(where, " WHERE ")
	if extra != "" {
		extra = " AND " + extra
	}
	params = append(params, bigquery.QueryParameter{Name: "project", Value: b.config.BigQuery.ProjectID})
	q := b.client.Query(coldTablesSQL(b.regionRef(filters.Region), filters.TimeInterval(), extra))
	q.Parameters = params

	return collectRows[ColdTable](q, ctx)
}

// --- Widget 1.4: Search Indexes Info ---

type SearchIndexInfo struct {
	Dataset            string `json:"dataset" bigquery:"index_schema"`
	TableName          string `json:"table_name" bigquery:"table_name"`
	IndexName          string `json:"index_name" bigquery:"index_name"`
	IndexStatus        string `json:"index_status" bigquery:"index_status"`
	CoveragePercentage int64  `json:"coverage_percentage" bigquery:"coverage_percentage"`
	TotalLogicalBytes  int64  `json:"total_logical_bytes" bigquery:"total_logical_bytes"`
	TotalStorageBytes  int64  `json:"total_storage_bytes" bigquery:"total_storage_bytes"`
}

const searchIndexBatchSize = 25

// searchIndexesSQL builds a UNION ALL query over INFORMATION_SCHEMA.SEARCH_INDEXES
// for the given datasets. Because BigQuery identifiers cannot be parameterized,
// every dataset name is validated against datasetNameRe (`^[A-Za-z0-9_]+$`)
// before interpolation.
func searchIndexesSQL(project string, datasets []string, hasTableFilter bool) (string, error) {
	if len(datasets) == 0 {
		return "", fmt.Errorf("no datasets provided")
	}
	where := ""
	if hasTableFilter {
		where = " WHERE table_name = @table_name"
	}
	parts := make([]string, 0, len(datasets))
	for _, ds := range datasets {
		if !datasetNameRe.MatchString(ds) {
			return "", fmt.Errorf("invalid dataset name %q", ds)
		}
		parts = append(parts, fmt.Sprintf(
			`SELECT
				index_schema,
				table_name,
				index_name,
				index_status,
				coverage_percentage,
				total_logical_bytes,
				total_storage_bytes
			FROM `+"`"+`%s.%s.INFORMATION_SCHEMA.SEARCH_INDEXES`+"`"+`%s`,
			project, ds, where))
	}
	return strings.Join(parts, "\nUNION ALL\n"), nil
}

func (b *BQClient) querySearchIndexesBatch(ctx context.Context, datasets []string, table string) ([]SearchIndexInfo, error) {
	sqlStr, err := searchIndexesSQL(b.config.BigQuery.ProjectID, datasets, table != "")
	if err != nil {
		return nil, err
	}
	q := b.client.Query(sqlStr)
	if table != "" {
		q.Parameters = []bigquery.QueryParameter{{Name: "table_name", Value: table}}
	}
	return collectRows[SearchIndexInfo](q, ctx)
}

func (b *BQClient) GetSearchIndexes(ctx context.Context, filters QueryFilters) ([]SearchIndexInfo, error) {
	var datasets []string
	if filters.Dataset != "" {
		if !datasetNameRe.MatchString(filters.Dataset) {
			return nil, fmt.Errorf("invalid dataset name %q", filters.Dataset)
		}
		datasets = []string{filters.Dataset}
	} else {
		q := b.client.Query(fmt.Sprintf(
			`SELECT schema_name FROM %s.INFORMATION_SCHEMA.SCHEMATA
			WHERE NOT STARTS_WITH(schema_name, '_')
			ORDER BY schema_name`,
			b.regionRef(filters.Region)))
		rows, err := collectRows[struct {
			SchemaName string `bigquery:"schema_name"`
		}](q, ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch datasets for region %s: %w", filters.Region, err)
		}
		for _, row := range rows {
			if datasetNameRe.MatchString(row.SchemaName) {
				datasets = append(datasets, row.SchemaName)
			}
		}
	}

	if len(datasets) == 0 {
		return nil, nil
	}

	var results []SearchIndexInfo
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, 4)

	for start := 0; start < len(datasets); start += searchIndexBatchSize {
		end := start + searchIndexBatchSize
		if end > len(datasets) {
			end = len(datasets)
		}
		batch := datasets[start:end]
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			rows, err := b.querySearchIndexesBatch(ctx, batch, filters.Table)
			if err == nil {
				mu.Lock()
				results = append(results, rows...)
				mu.Unlock()
				return nil
			}
			if len(batch) == 1 {
				slog.Warn("skipping search indexes query for dataset due to error", "dataset", batch[0], "error", err)
				return nil
			}
			// A linked dataset or permission gap in the UNION ALL batch fails the
			// combined query; fall back to per-dataset queries for this batch.
			slog.Warn("search indexes batch failed, falling back to per-dataset queries", "batch_size", len(batch), "error", err)
			for _, ds := range batch {
				r, err := b.querySearchIndexesBatch(ctx, []string{ds}, filters.Table)
				if err != nil {
					slog.Warn("skipping search indexes query for dataset due to error", "dataset", ds, "error", err)
					continue
				}
				mu.Lock()
				results = append(results, r...)
				mu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

// --- Widget 2.1: Concurrent Slot Usage by State (Time-Series) ---

// SlotBucket is one fixed-width bucket of the slot timeline, with RUNNING and
// PENDING kept apart. The averages spread the bucket's slot-ms over its full
// length, so idle seconds count as zero; the peaks are the busiest single
// second inside the bucket (PeakTotal counts RUNNING + PENDING).
type SlotBucket struct {
	BucketStart string  `json:"bucket_start" bigquery:"bucket_start"`
	AvgRunning  float64 `json:"avg_running" bigquery:"avg_running"`
	AvgPending  float64 `json:"avg_pending" bigquery:"avg_pending"`
	PeakTotal   float64 `json:"peak_total" bigquery:"peak_total"`
	PeakPending float64 `json:"peak_pending" bigquery:"peak_pending"`
}

// slotTimelineSQL folds every job's timeline rows into one row per second,
// then groups those seconds into @bucket_secs buckets. where must come from
// TimelineWhere, which binds @bucket_secs and keeps the window to whole
// buckets, so dividing by the full bucket length is exact.
func slotTimelineSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`WITH per_second AS (
			SELECT
				period_start,
				SUM(IF(state = 'RUNNING', IFNULL(period_slot_ms, 0), 0)) AS running_ms,
				COUNT(DISTINCT IF(state = 'PENDING', job_id, NULL)) AS pending_jobs
			FROM %s.INFORMATION_SCHEMA.JOBS_TIMELINE_BY_PROJECT
			%s AND state IN ('PENDING', 'RUNNING')
				AND (statement_type IS NULL OR statement_type != 'SCRIPT')
			GROUP BY period_start
		)
		SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", TIMESTAMP_SECONDS(DIV(UNIX_SECONDS(period_start), @bucket_secs) * @bucket_secs)) AS bucket_start,
			SUM(running_ms) / (@bucket_secs * 1000) AS avg_running,
			SUM(pending_jobs) / @bucket_secs AS avg_pending,
			MAX(running_ms) / 1000 AS peak_total,
			CAST(MAX(pending_jobs) AS FLOAT64) AS peak_pending
		FROM per_second
		GROUP BY bucket_start
		ORDER BY bucket_start ASC`,
		regionRef, where)
}

func (b *BQClient) GetConcurrentSlotsByState(ctx context.Context, filters QueryFilters) ([]SlotBucket, error) {
	where, params := filters.TimelineWhere("period_start")
	q := b.client.Query(slotTimelineSQL(b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[SlotBucket](q, ctx)
}

// --- Widget 2.3: Queue Time & Duration KPIs ---

type QueueStats struct {
	AvgQueueMs float64 `json:"avg_queue_ms" bigquery:"avg_queue_ms"`
	P95QueueMs float64 `json:"p95_queue_ms" bigquery:"p95_queue_ms"`
	AvgRunMs   float64 `json:"avg_run_ms" bigquery:"avg_run_ms"`
	P95RunMs   float64 `json:"p95_run_ms" bigquery:"p95_run_ms"`
	JobCount   int64   `json:"job_count" bigquery:"job_count"`
}

func (b *BQClient) GetQueueStats(ctx context.Context, filters QueryFilters) (*QueueStats, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			CAST(IFNULL(AVG(TIMESTAMP_DIFF(start_time, creation_time, MILLISECOND)), 0) AS FLOAT64) AS avg_queue_ms,
			CAST(IFNULL(APPROX_QUANTILES(TIMESTAMP_DIFF(start_time, creation_time, MILLISECOND), 100)[OFFSET(95)], 0) AS FLOAT64) AS p95_queue_ms,
			CAST(IFNULL(AVG(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND)), 0) AS FLOAT64) AS avg_run_ms,
			CAST(IFNULL(APPROX_QUANTILES(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND), 100)[OFFSET(95)], 0) AS FLOAT64) AS p95_run_ms,
			COUNT(*) AS job_count
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND start_time IS NOT NULL AND end_time IS NOT NULL
			AND (statement_type IS NULL OR statement_type != 'SCRIPT')`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	rows, err := collectRows[QueueStats](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("queue stats query failed: %w", err)
	}
	if len(rows) == 0 {
		return &QueueStats{}, nil
	}
	return &rows[0], nil
}

// --- Widget 2.4: Reservation Utilization ---

type ReservationPoint struct {
	PeriodStart string  `json:"period_start" bigquery:"period_start"`
	Assigned    float64 `json:"assigned" bigquery:"assigned"`
	Autoscale   float64 `json:"autoscale" bigquery:"autoscale"`
}

func reservationTimelineSQL(regionRef, interval string) string {
	return fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", TIMESTAMP_SECONDS(DIV(UNIX_SECONDS(period_start), @bucket_secs) * @bucket_secs)) AS period_start,
			CAST(AVG(assigned_slots) AS FLOAT64) AS assigned,
			CAST(AVG(autoscale_slots) AS FLOAT64) AS autoscale
		FROM (
			SELECT
				period_start,
				IFNULL(SUM(slots_assigned), 0) AS assigned_slots,
				IFNULL(SUM(COALESCE(SAFE_DIVIDE(period_autoscale_slot_seconds, 60), CAST(autoscale.current_slots AS FLOAT64), 0)), 0) AS autoscale_slots
			FROM %s.INFORMATION_SCHEMA.RESERVATIONS_TIMELINE
			WHERE period_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)
			GROUP BY period_start
		)
		GROUP BY 1
		ORDER BY 1 ASC`,
		regionRef, interval)
}

// GetReservationTimeline returns bucketed baseline + autoscaled capacity using
// period_autoscale_slot_seconds / 60 (falling back to autoscale.current_slots).
// Projects without reservations (or without permission on the admin
// project) get an empty result; callers treat errors as "no reservations".
func (b *BQClient) GetReservationTimeline(ctx context.Context, filters QueryFilters) ([]ReservationPoint, error) {
	q := b.client.Query(reservationTimelineSQL(b.regionRef(filters.Region), filters.TimeInterval()))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "bucket_secs", Value: filters.TimelineBucketSeconds()},
	}

	return collectRows[ReservationPoint](q, ctx)
}

// --- Widget 2.2: Slot Gluttons (Top Jobs) ---

type TopSlotJob struct {
	JobID       string `json:"job_id" bigquery:"job_id"`
	UserEmail   string `json:"user_email" bigquery:"user_email"`
	TotalSlotMs int64  `json:"total_slot_ms" bigquery:"total_slot_ms"`
	DurationMs  int64  `json:"duration_ms" bigquery:"duration_ms"`
	State       string `json:"state" bigquery:"state"`
	CacheHit    bool   `json:"cache_hit" bigquery:"cache_hit"`
	Reservation string `json:"reservation" bigquery:"reservation"`
}

func (b *BQClient) GetTopSlotJobs(ctx context.Context, filters QueryFilters) ([]TopSlotJob, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			job_id,
			user_email,
			total_slot_ms,
			IFNULL(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND), 0) AS duration_ms,
			state,
			IFNULL(cache_hit, FALSE) AS cache_hit,
			IFNULL(reservation_id, '') AS reservation
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND (statement_type != 'SCRIPT' OR statement_type IS NULL)
		ORDER BY total_slot_ms DESC
		LIMIT 10`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[TopSlotJob](q, ctx)
}

// --- Existing: Hourly Slot Usage ---

type SlotUsage struct {
	Hour     string  `json:"usage_hour" bigquery:"usage_hour"`
	AvgSlots float64 `json:"avg_slots" bigquery:"avg_slots"`
}

func (b *BQClient) GetSlotUsage(ctx context.Context, filters QueryFilters) ([]SlotUsage, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:00:00Z", TIMESTAMP_TRUNC(creation_time, HOUR)) AS usage_hour,
			SUM(total_slot_ms) / (1000 * 60 * 60) AS avg_slots
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_USER
		%s AND (statement_type != 'SCRIPT' OR statement_type IS NULL)
		GROUP BY usage_hour
		ORDER BY usage_hour ASC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[SlotUsage](q, ctx)
}

// --- Widget 3.1: On-Demand Cost Extrapolator ---

type CostSummary struct {
	BytesBilled         int64 `json:"bytes_billed" bigquery:"bytes_billed"`
	OnDemandBytesBilled int64 `json:"ondemand_bytes_billed" bigquery:"ondemand_bytes_billed"`
	BytesProcessed      int64 `json:"bytes_processed" bigquery:"bytes_processed"`
	TotalSlotMs         int64 `json:"total_slot_ms" bigquery:"total_slot_ms"`
}

func (b *BQClient) GetCostSummary(ctx context.Context, filters QueryFilters) (*CostSummary, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			IFNULL(SUM(total_bytes_billed), 0) AS bytes_billed,
			IFNULL(SUM(IF(reservation_id IS NULL, total_bytes_billed, 0)), 0) AS ondemand_bytes_billed,
			IFNULL(SUM(total_bytes_processed), 0) AS bytes_processed,
			IFNULL(SUM(total_slot_ms), 0) AS total_slot_ms
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND (statement_type IS NULL OR statement_type != 'SCRIPT')`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	rows, err := collectRows[CostSummary](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("cost summary query failed: %w", err)
	}
	if len(rows) == 0 {
		return &CostSummary{}, nil
	}
	return &rows[0], nil
}

type costGroupingRow struct {
	GDay                int64  `bigquery:"g_day"`
	GName               int64  `bigquery:"g_name"`
	Day                 string `bigquery:"day"`
	Name                string `bigquery:"name"`
	BytesBilled         int64  `bigquery:"bytes_billed"`
	OnDemandBytesBilled int64  `bigquery:"ondemand_bytes_billed"`
	BytesProcessed      int64  `bigquery:"bytes_processed"`
	TotalSlotMs         int64  `bigquery:"total_slot_ms"`
}

func costOverviewSQL(regionRef, where, spendExpr string, includeSpend bool) string {
	groupSets := "((), (day))"
	gNameExpr := "1 AS g_name,\n\t\t\t\t'' AS spend_name,"
	if includeSpend {
		groupSets = "((), (day), (spend_name))"
		gNameExpr = "GROUPING(spend_name) AS g_name,\n\t\t\t\tspend_name,"
	}
	return fmt.Sprintf(
		`SELECT
			g_day,
			g_name,
			IFNULL(day, '') AS day,
			IFNULL(spend_name, '') AS name,
			bytes_billed,
			ondemand_bytes_billed,
			bytes_processed,
			total_slot_ms
		FROM (
			SELECT
				GROUPING(day) AS g_day,
				%s
				day,
				IFNULL(SUM(total_bytes_billed), 0) AS bytes_billed,
				IFNULL(SUM(IF(reservation_id IS NULL, total_bytes_billed, 0)), 0) AS ondemand_bytes_billed,
				IFNULL(SUM(total_bytes_processed), 0) AS bytes_processed,
				IFNULL(SUM(total_slot_ms), 0) AS total_slot_ms
			FROM (
				SELECT
					FORMAT_DATE("%%Y-%%m-%%d", DATE(creation_time)) AS day,
					%s AS spend_name,
					total_bytes_billed,
					total_bytes_processed,
					total_slot_ms,
					reservation_id
				FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
				%s AND (statement_type IS NULL OR statement_type != 'SCRIPT')
			)
			GROUP BY GROUPING SETS %s
		)
		ORDER BY g_day DESC, g_name DESC, day ASC, bytes_billed DESC`,
		gNameExpr, spendExpr, regionRef, where, groupSets)
}

func rollupCostOverview(rows []costGroupingRow) (*CostSummary, []DailyCost, []SpendEntry) {
	summary := &CostSummary{}
	var daily []DailyCost
	var spend []SpendEntry
	for _, r := range rows {
		switch {
		case r.GDay == 1 && r.GName == 1:
			summary = &CostSummary{
				BytesBilled:         r.BytesBilled,
				OnDemandBytesBilled: r.OnDemandBytesBilled,
				BytesProcessed:      r.BytesProcessed,
				TotalSlotMs:         r.TotalSlotMs,
			}
		case r.GDay == 0 && r.GName == 1:
			if r.Day != "" {
				daily = append(daily, DailyCost{Day: r.Day, BytesBilled: r.BytesBilled})
			}
		case r.GDay == 1 && r.GName == 0:
			if len(spend) < 25 {
				spend = append(spend, SpendEntry{Name: r.Name, TotalBytes: r.BytesBilled})
			}
		}
	}
	return summary, daily, spend
}

// GetCostOverview scans JOBS_BY_PROJECT once using GROUPING SETS to produce
// CostSummary, DailyCost, and (when GroupBy is user or reservation) SpendEntry.
func (b *BQClient) GetCostOverview(ctx context.Context, filters QueryFilters, includeSpend bool) (*CostSummary, []DailyCost, []SpendEntry, error) {
	where, params := filters.JobsWhere("creation_time")
	spendExpr := "IFNULL(user_email, '')"
	if filters.GroupBy == "reservation" {
		spendExpr = "IFNULL(reservation_id, 'on-demand')"
	}
	q := b.client.Query(costOverviewSQL(b.regionRef(filters.Region), where, spendExpr, includeSpend))
	q.Parameters = params

	rows, err := collectRows[costGroupingRow](q, ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cost overview query failed: %w", err)
	}
	s, daily, spend := rollupCostOverview(rows)
	return s, daily, spend, nil
}

// --- Widget 3.2: Spend by <group-by dimension> (Treemap) ---

type SpendEntry struct {
	Name       string `json:"name" bigquery:"name"`
	TotalBytes int64  `json:"total_bytes" bigquery:"total_bytes"`
}

// GetSpend aggregates bytes billed by the filters.GroupBy dimension. The
// dataset/table dimensions unnest referenced_tables, which yields one row per
// referenced table; deduping on job_id keeps each job's bytes counted once per
// group instead of once per referenced table. A job touching N groups still
// contributes its full bytes to each of them.
func (b *BQClient) GetSpend(ctx context.Context, filters QueryFilters) ([]SpendEntry, error) {
	where, params := filters.JobsWhere("creation_time")

	if filters.GroupBy == "dataset" || filters.GroupBy == "table" {
		nameExpr, notNull := "rt.dataset_id", "rt.dataset_id IS NOT NULL"
		if filters.GroupBy == "table" {
			nameExpr = "CONCAT(rt.dataset_id, '.', rt.table_id)"
			notNull = "rt.dataset_id IS NOT NULL AND rt.table_id IS NOT NULL"
		}
		q := b.client.Query(fmt.Sprintf(
			`WITH jobs AS (
				SELECT DISTINCT j.job_id, j.total_bytes_billed, %s AS name
				FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT j, UNNEST(referenced_tables) rt
				%s AND (statement_type IS NULL OR statement_type != 'SCRIPT') AND %s
			)
			SELECT
				name,
				IFNULL(SUM(total_bytes_billed), 0) AS total_bytes
			FROM jobs
			GROUP BY name
			ORDER BY total_bytes DESC
			LIMIT 25`,
			nameExpr, b.regionRef(filters.Region), where, notNull))
		q.Parameters = params
		return collectRows[SpendEntry](q, ctx)
	}

	nameExpr := "user_email"
	if filters.GroupBy == "reservation" {
		nameExpr = "IFNULL(reservation_id, 'on-demand')"
	}
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			%s AS name,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND (statement_type IS NULL OR statement_type != 'SCRIPT')
		GROUP BY name
		ORDER BY total_bytes DESC
		LIMIT 25`,
		nameExpr, b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[SpendEntry](q, ctx)
}

// --- Widget 3.3: Daily Cost Trend ---

type DailyCost struct {
	Day         string `json:"day" bigquery:"day"`
	BytesBilled int64  `json:"bytes_billed" bigquery:"bytes_billed"`
}

func (b *BQClient) GetDailyCost(ctx context.Context, filters QueryFilters) ([]DailyCost, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_DATE("%%Y-%%m-%%d", DATE(creation_time)) AS day,
			IFNULL(SUM(total_bytes_billed), 0) AS bytes_billed
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND (statement_type IS NULL OR statement_type != 'SCRIPT')
		GROUP BY day
		ORDER BY day ASC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[DailyCost](q, ctx)
}

// --- Widget 4.1: Active Recommendations ---

type Recommendation struct {
	Recommender         string  `json:"recommender" bigquery:"recommender"`
	Description         string  `json:"description" bigquery:"description"`
	Category            string  `json:"category" bigquery:"category"`
	ProjectedSavingsUSD float64 `json:"projected_savings_usd" bigquery:"projected_savings_usd"`
}

func recommendationsSQL(regionRef string) string {
	return fmt.Sprintf(
		`SELECT
			recommender,
			description,
			primary_impact.category AS category,
			COALESCE(
				SAFE_CAST(JSON_VALUE(additional_details.overview, '$.bytesSavedMonthly') AS FLOAT64) / POW(1024, 4) * 6.25,
				SAFE_CAST(JSON_VALUE(additional_details.overview, '$.slotMsSavedMonthly') AS FLOAT64) / (1000.0 * 3600.0) * 0.04,
				SAFE_CAST(JSON_VALUE(additional_details.overview, '$.costSavedMonthly') AS FLOAT64),
				0.0
			) AS projected_savings_usd
		FROM %s.INFORMATION_SCHEMA.RECOMMENDATIONS
		WHERE state = 'ACTIVE'
		ORDER BY recommender`,
		regionRef)
}

func (b *BQClient) GetRecommendations(ctx context.Context, region string) ([]Recommendation, error) {
	q := b.client.Query(recommendationsSQL(b.regionRef(region)))

	return collectRows[Recommendation](q, ctx)
}

// --- Dataset / Table listing ---

func (b *BQClient) ListDatasets(ctx context.Context) ([]string, error) {
	it := b.client.Datasets(ctx)
	var datasets []string
	for {
		ds, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		datasets = append(datasets, ds.DatasetID)
	}
	return datasets, nil
}

func (b *BQClient) ListTables(ctx context.Context, datasetID string) ([]string, error) {
	it := b.client.Dataset(datasetID).Tables(ctx)
	var tables []string
	for {
		t, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		tables = append(tables, t.TableID)
	}
	return tables, nil
}

// --- Generic row collector ---

const defaultInfoSchemaMaxBytesBilled int64 = 10 << 30 // 10 GiB safety cap

// applyQueryDefaults tags every BigLens query with app=biglens so the tool's
// own diagnostic jobs can be excluded from JOBS_BY_PROJECT dashboards, and
// applies a 10 GiB MaxBytesBilled safety cap to INFORMATION_SCHEMA queries.
func applyQueryDefaults(q *bigquery.Query) {
	if q.Labels == nil {
		q.Labels = map[string]string{"app": "biglens"}
	} else if _, ok := q.Labels["app"]; !ok {
		q.Labels["app"] = "biglens"
	}
	if q.MaxBytesBilled == 0 && strings.Contains(q.QueryConfig.Q, "INFORMATION_SCHEMA") {
		q.MaxBytesBilled = defaultInfoSchemaMaxBytesBilled
	}
}

func collectRows[T any](q *bigquery.Query, ctx context.Context) ([]T, error) {
	applyQueryDefaults(q)
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}

	var results []T
	for {
		var row T
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	return results, nil
}
