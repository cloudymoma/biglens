package main

import (
	"context"
	"fmt"
	"log/slog"
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


// --- Widget 2.1: Concurrent Slot Usage (Time-Series) ---

type SlotTimepoint struct {
	PeriodStart     string  `json:"period_start" bigquery:"period_start"`
	ConcurrentSlots float64 `json:"concurrent_slots" bigquery:"concurrent_slots"`
}

func (b *BQClient) GetConcurrentSlots(ctx context.Context, filters QueryFilters) ([]SlotTimepoint, error) {
	where, params := filters.JobsWhere("period_start")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			FORMAT_TIMESTAMP("%%Y-%%m-%%dT%%H:%%M:%%SZ", period_start) AS period_start,
			SUM(period_slot_ms) / 1000 AS concurrent_slots
		FROM %s.INFORMATION_SCHEMA.JOBS_TIMELINE_BY_PROJECT
		%s
		GROUP BY period_start
		ORDER BY period_start ASC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[SlotTimepoint](q, ctx)
}

// --- Widget 2.2: Slot Gluttons (Top Jobs) ---

type TopSlotJob struct {
	JobID      string  `json:"job_id" bigquery:"job_id"`
	UserEmail  string  `json:"user_email" bigquery:"user_email"`
	TotalSlotMs int64  `json:"total_slot_ms" bigquery:"total_slot_ms"`
}

func (b *BQClient) GetTopSlotJobs(ctx context.Context, filters QueryFilters) ([]TopSlotJob, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			job_id,
			user_email,
			total_slot_ms
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
	BytesBilled int64 `json:"bytes_billed" bigquery:"bytes_billed"`
}

func (b *BQClient) GetCostSummary(ctx context.Context, filters QueryFilters) (*CostSummary, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			IFNULL(SUM(total_bytes_billed), 0) AS bytes_billed
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

// --- Widget 3.2: Spend by User (Treemap) ---

type UserSpend struct {
	UserEmail  string `json:"user_email" bigquery:"user_email"`
	TotalBytes int64  `json:"total_bytes" bigquery:"total_bytes"`
}

func (b *BQClient) GetSpendByUser(ctx context.Context, filters QueryFilters) ([]UserSpend, error) {
	where, params := filters.JobsWhere("creation_time")
	q := b.client.Query(fmt.Sprintf(
		`SELECT
			user_email,
			IFNULL(SUM(total_bytes_billed), 0) AS total_bytes
		FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		%s
		GROUP BY user_email
		ORDER BY total_bytes DESC`,
		b.regionRef(filters.Region), where))
	q.Parameters = params

	return collectRows[UserSpend](q, ctx)
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
