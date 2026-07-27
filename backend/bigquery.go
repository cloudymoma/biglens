package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"cloud.google.com/go/bigquery"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type BQClient struct {
	client *bigquery.Client
	config *Config
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

	return &BQClient{
		client: client,
		config: cfg,
	}, nil
}

func (b *BQClient) regionRef(region string) string {
	return fmt.Sprintf("`%s`.`region-%s`", b.config.BigQuery.ProjectID, region)
}

// --- Widget 1.1: Logical vs. Physical Billing Simulator ---

type StorageStats struct {
	LogicalBytes  int64 `json:"logical_bytes" bigquery:"logical_bytes"`
	PhysicalBytes int64 `json:"physical_bytes" bigquery:"physical_bytes"`
	TotalBytes    int64 `json:"total_bytes" bigquery:"total_bytes"`
}

func (b *BQClient) GetStorageStats(ctx context.Context, filters QueryFilters) (*StorageStats, error) {
	where, params := filters.StorageWhere()
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			SUM(total_logical_bytes) AS logical_bytes,
			SUM(total_physical_bytes) AS physical_bytes,
			SUM(total_logical_bytes + total_physical_bytes) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s`,
		b.regionRef(filters.Region), where))
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
			SUM(active_logical_bytes) AS active_bytes,
			SUM(long_term_logical_bytes) AS long_term_bytes
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s`,
		b.regionRef(filters.Region), where))
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
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			table_schema AS dataset,
			table_name,
			(active_logical_bytes + long_term_logical_bytes) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s
		ORDER BY total_bytes DESC
		LIMIT 10`,
		b.regionRef(filters.Region), where))
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
			SUM(active_logical_bytes) AS active_logical,
			SUM(long_term_logical_bytes) AS long_term_logical,
			SUM(active_physical_bytes) AS active_physical,
			SUM(long_term_physical_bytes) AS long_term_physical,
			SUM(time_travel_physical_bytes) AS time_travel,
			SUM(fail_safe_physical_bytes) AS fail_safe
		FROM %s.INFORMATION_SCHEMA.TABLE_STORAGE_BY_PROJECT%s
		GROUP BY dataset
		ORDER BY SUM(active_logical_bytes) + SUM(long_term_logical_bytes) DESC
		LIMIT 50`,
		b.regionRef(filters.Region), where))
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

func (b *BQClient) GetSearchIndexes(ctx context.Context, filters QueryFilters) ([]SearchIndexInfo, error) {
	var datasets []string
	if filters.Dataset != "" {
		datasets = []string{filters.Dataset}
	} else {
		// Fetch datasets in the region
		q := b.client.Query(fmt.Sprintf(
			`SELECT schema_name FROM %s.INFORMATION_SCHEMA.SCHEMATA`,
			b.regionRef(filters.Region)))
		rows, err := collectRows[struct {
			SchemaName string `bigquery:"schema_name"`
		}](q, ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch datasets for region %s: %w", filters.Region, err)
		}
		for _, row := range rows {
			datasets = append(datasets, row.SchemaName)
		}
	}

	if len(datasets) == 0 {
		return nil, nil
	}

	var results []SearchIndexInfo
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)
	// Limit concurrency to 10 to be gentle to BigQuery rate limits
	sem := make(chan struct{}, 10)

	for _, ds := range datasets {
		ds := ds // capture loop variable
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			queryStr := fmt.Sprintf(
				`SELECT 
					index_schema, 
					table_name, 
					index_name, 
					index_status, 
					coverage_percentage, 
					total_logical_bytes, 
					total_storage_bytes
				FROM `+"`"+`%s.%s.INFORMATION_SCHEMA.SEARCH_INDEXES`+"`"+``,
				b.config.BigQuery.ProjectID, ds,
			)

			var params []bigquery.QueryParameter
			if filters.Table != "" {
				queryStr += " WHERE table_name = @table_name"
				params = append(params, bigquery.QueryParameter{Name: "table_name", Value: filters.Table})
			}

			q := b.client.Query(queryStr)
			q.Parameters = params

			rows, err := collectRows[SearchIndexInfo](q, ctx)
			if err != nil {
				// Warn and ignore (e.g. linked datasets or permission issues on specific datasets)
				slog.Warn("skipping search indexes query for dataset due to error", "dataset", ds, "error", err)
				return nil
			}

			mu.Lock()
			results = append(results, rows...)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}


// --- Widget 2.1: Concurrent Slot Usage by State (Time-Series) ---

// SlotStatePoint carries one (period, state) slot reading; PENDING vs
// RUNNING split reveals slot starvation (sustained PENDING).
type SlotStatePoint struct {
	PeriodStart string  `json:"period_start" bigquery:"period_start"`
	State       string  `json:"state" bigquery:"state"`
	Slots       float64 `json:"slots" bigquery:"slots"`
}

func (b *BQClient) GetConcurrentSlotsByState(ctx context.Context, filters QueryFilters) ([]SlotStatePoint, error) {
	where, params := filters.TimelineWhere("period_start")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", period_start) AS period_start,
			state,
			SUM(period_slot_ms) / 1000 AS slots
		FROM %s.INFORMATION_SCHEMA.JOBS_TIMELINE_BY_PROJECT
		%s AND state IN ('PENDING', 'RUNNING')
		GROUP BY period_start, state
		ORDER BY period_start ASC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[SlotStatePoint](q, ctx)
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
		%s AND start_time IS NOT NULL AND end_time IS NOT NULL`,
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
		%s
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
		%s
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
		%s AND statement_type != 'SCRIPT'`,
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
// dataset/table dimensions unnest referenced_tables, so a job referencing N
// tables is counted once under each of them.
func (b *BQClient) GetSpend(ctx context.Context, filters QueryFilters) ([]SpendEntry, error) {
	nameExpr, from := "user_email", ""
	switch filters.GroupBy {
	case "dataset":
		nameExpr, from = "rt.dataset_id", ", UNNEST(referenced_tables) rt"
	case "table":
		nameExpr, from = "CONCAT(rt.dataset_id, '.', rt.table_id)", ", UNNEST(referenced_tables) rt"
	case "reservation":
		nameExpr = "IFNULL(reservation_id, 'on-demand')"
	}

	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			%s AS name,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT%s
		%s AND statement_type != 'SCRIPT'
		GROUP BY name
		ORDER BY total_bytes DESC
		LIMIT 25`,
		nameExpr, b.regionRef(filters.Region), from, where))
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
		%s AND statement_type != 'SCRIPT'
		GROUP BY day
		ORDER BY day ASC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[DailyCost](q, ctx)
}

// --- Widget 4.1: Active Recommendations ---

type Recommendation struct {
	Recommender        string  `json:"recommender" bigquery:"recommender"`
	Description        string  `json:"description" bigquery:"description"`
	Category           string  `json:"category" bigquery:"category"`
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
