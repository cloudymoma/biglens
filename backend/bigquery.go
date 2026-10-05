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
	ActiveLogical    int64  `bigquery:"active_logical"`
	LongTermLogical  int64  `bigquery:"long_term_logical"`
	ActivePhysical   int64  `bigquery:"active_physical"`
	LongTermPhysical int64  `bigquery:"long_term_physical"`
	TimeTravel       int64  `bigquery:"time_travel"`
	FailSafe         int64  `bigquery:"fail_safe"`
}

func storageOverviewSQL(regionRef, where string) string {
	return fmt.Sprintf(
		`SELECT
			IFNULL(table_schema, '') AS dataset,
			GROUPING(table_schema) AS is_rollup,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_logical,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_logical,
			COALESCE(SUM(active_physical_bytes), 0) AS active_physical,
			COALESCE(SUM(long_term_physical_bytes), 0) AS long_term_physical,
			COALESCE(SUM(time_travel_physical_bytes), 0) AS time_travel,
			COALESCE(SUM(fail_safe_physical_bytes), 0) AS fail_safe
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s
		GROUP BY ROLLUP(table_schema)
		ORDER BY is_rollup DESC, (active_logical + long_term_logical) DESC
		LIMIT 51`,
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
		if len(datasets) < 50 {
			datasets = append(datasets, DatasetStorage{
				Dataset:          r.Dataset,
				ActiveLogical:    r.ActiveLogical,
				LongTermLogical:  r.LongTermLogical,
				ActivePhysical:   r.ActivePhysical,
				LongTermPhysical: r.LongTermPhysical,
				TimeTravel:       r.TimeTravel,
				FailSafe:         r.FailSafe,
			})
		}
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

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage stats query failed: %w", err)
	}

	var stats StorageStats
	err = it.Next(&stats)
	if err == iterator.Done {
		return &StorageStats{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage stats iteration failed: %w", err)
	}
	return &stats, nil
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

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage breakdown query failed: %w", err)
	}

	var bd StorageBreakdown
	err = it.Next(&bd)
	if err == iterator.Done {
		return &StorageBreakdown{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage breakdown iteration failed: %w", err)
	}
	return &bd, nil
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
		`SELECT
			table_schema AS dataset,
			COALESCE(SUM(IF(NOT deleted, active_logical_bytes, 0)), 0) AS active_logical,
			COALESCE(SUM(IF(NOT deleted, long_term_logical_bytes, 0)), 0) AS long_term_logical,
			COALESCE(SUM(active_physical_bytes), 0) AS active_physical,
			COALESCE(SUM(long_term_physical_bytes), 0) AS long_term_physical,
			COALESCE(SUM(time_travel_physical_bytes), 0) AS time_travel,
			COALESCE(SUM(fail_safe_physical_bytes), 0) AS fail_safe
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s
		GROUP BY dataset
		ORDER BY active_logical + long_term_logical DESC
		LIMIT 50`,
		b.regionRef(filters.Region), storageBaseTableWhere(where)))
	q.Parameters = params

	return collectRows[DatasetStorage](q, ctx)
}

// --- Widget 1.6: Cold Tables (no references in the time window) ---

type ColdTable struct {
	Dataset     string `json:"dataset" bigquery:"dataset"`
	TableName   string `json:"table_name" bigquery:"table_name"`
	TotalBytes  int64  `json:"total_bytes" bigquery:"total_bytes"`
	StorageTier string `json:"storage_tier" bigquery:"storage_tier"`
}

func (b *BQClient) GetColdTables(ctx context.Context, filters QueryFilters) ([]ColdTable, error) {
	where, params := filters.StorageWhere()
	extra := strings.TrimPrefix(where, " WHERE ")
	if extra != "" {
		extra = " AND " + extra
	}
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			ts.table_schema AS dataset,
			ts.table_name,
			(ts.active_logical_bytes + ts.long_term_logical_bytes) AS total_bytes,
			CASE WHEN ts.long_term_logical_bytes > ts.active_logical_bytes THEN 'LONG_TERM' ELSE 'ACTIVE' END AS storage_tier
		FROM %[1]s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT ts
		WHERE ts.deleted = FALSE
			AND NOT STARTS_WITH(ts.table_schema, '_')
			AND NOT EXISTS (
				SELECT 1
				FROM %[1]s.INFORMATION_SCHEMA.JOBS_BY_PROJECT j, UNNEST(j.referenced_tables) rt
				WHERE j.creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %[2]s)
					AND rt.dataset_id = ts.table_schema
					AND rt.table_id = ts.table_name
			)%[3]s
		ORDER BY total_bytes DESC
		LIMIT 20`,
		b.regionRef(filters.Region), filters.TimeInterval(), extra))
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

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("queue stats query failed: %w", err)
	}
	var qs QueueStats
	err = it.Next(&qs)
	if err == iterator.Done {
		return &QueueStats{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue stats iteration failed: %w", err)
	}
	return &qs, nil
}

// --- Widget 2.4: Reservation Utilization ---

type ReservationPoint struct {
	PeriodStart string  `json:"period_start" bigquery:"period_start"`
	Assigned    float64 `json:"assigned" bigquery:"assigned"`
	Autoscale   float64 `json:"autoscale" bigquery:"autoscale"`
}

// GetReservationTimeline returns baseline + autoscaled capacity per minute.
// Projects without reservations (or without permission on the admin
// project) get an empty result; callers treat errors as "no reservations".
func (b *BQClient) GetReservationTimeline(ctx context.Context, filters QueryFilters) ([]ReservationPoint, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", period_start) AS period_start,
			CAST(SUM(slots_assigned) AS FLOAT64) AS assigned,
			CAST(SUM(IFNULL(autoscale.current_slots, 0)) AS FLOAT64) AS autoscale
		FROM %s.INFORMATION_SCHEMA.RESERVATIONS_TIMELINE
		WHERE period_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)
		GROUP BY period_start
		ORDER BY period_start ASC`,
		b.regionRef(filters.Region), filters.TimeInterval()))

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
	BytesBilled    int64 `json:"bytes_billed" bigquery:"bytes_billed"`
	BytesProcessed int64 `json:"bytes_processed" bigquery:"bytes_processed"`
	TotalSlotMs    int64 `json:"total_slot_ms" bigquery:"total_slot_ms"`
}

func (b *BQClient) GetCostSummary(ctx context.Context, filters QueryFilters) (*CostSummary, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			IFNULL(SUM(total_bytes_billed), 0) AS bytes_billed,
			IFNULL(SUM(total_bytes_processed), 0) AS bytes_processed,
			IFNULL(SUM(total_slot_ms), 0) AS total_slot_ms
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s AND (statement_type IS NULL OR statement_type != 'SCRIPT')`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("cost summary query failed: %w", err)
	}

	var cs CostSummary
	err = it.Next(&cs)
	if err == iterator.Done {
		return &CostSummary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cost summary iteration failed: %w", err)
	}
	return &cs, nil
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

func (b *BQClient) GetRecommendations(ctx context.Context, region string) ([]Recommendation, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			recommender,
			description,
			primary_impact.category AS category,
			0 AS projected_savings_usd
		FROM %s.INFORMATION_SCHEMA.RECOMMENDATIONS
		WHERE state = 'ACTIVE'
		ORDER BY recommender`,
		b.regionRef(region)))

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

func collectRows[T any](q *bigquery.Query, ctx context.Context) ([]T, error) {
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
