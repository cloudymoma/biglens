package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

type APIHandler struct {
	bq     *BQClient
	cache  *Cache
	bundle *OKFBundle
}

func NewAPIHandler(bq *BQClient) *APIHandler {
	return &APIHandler{
		bq:     bq,
		cache:  NewCache(10 * time.Minute),
		bundle: NewOKFBundle(bq.config.Catalog.BundlePath),
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	slog.Error("api error", "status", code, "error", msg)
	http.Error(w, msg, code)
}

// --- Dashboard 1: Storage Analysis ---

type StorageDashboardData struct {
	Billing        *StorageStats     `json:"billing"`
	Breakdown      *StorageBreakdown `json:"breakdown"`
	TopTables      []TopTable        `json:"top_tables"`
	SearchIndexes  []SearchIndexInfo `json:"search_indexes"`
	DatasetStorage []DatasetStorage  `json:"dataset_storage"`
	ColdTables     []ColdTable       `json:"cold_tables"`
}

func (h *APIHandler) StorageDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("storage_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data StorageDashboardData
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		stats, err := h.bq.GetStorageStats(ctx, filters)
		if err != nil {
			return err
		}
		data.Billing = stats
		return nil
	})

	g.Go(func() error {
		bd, err := h.bq.GetStorageBreakdown(ctx, filters)
		if err != nil {
			return err
		}
		data.Breakdown = bd
		return nil
	})

	g.Go(func() error {
		tables, err := h.bq.GetTopTables(ctx, filters)
		if err != nil {
			return err
		}
		data.TopTables = tables
		return nil
	})

	g.Go(func() error {
		indexes, err := h.bq.GetSearchIndexes(ctx, filters)
		if err != nil {
			return err
		}
		data.SearchIndexes = indexes
		return nil
	})

	g.Go(func() error {
		ds, err := h.bq.GetDatasetStorage(ctx, filters)
		if err != nil {
			return err
		}
		data.DatasetStorage = ds
		return nil
	})

	g.Go(func() error {
		// Cold-table detection needs jobs history; degrade to empty if the
		// caller lacks bigquery.jobs.listAll rather than failing storage.
		cold, err := h.bq.GetColdTables(ctx, filters)
		if err != nil {
			slog.Warn("cold tables widget degraded", "error", err)
			return nil
		}
		data.ColdTables = cold
		return nil
	})

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 2: Slots & Compute ---

type ComputeDashboardData struct {
	SlotTimeline []SlotStatePoint   `json:"slot_timeline"`
	TopJobs      []TopSlotJob       `json:"top_jobs"`
	SlotUsage    []SlotUsage        `json:"slot_usage"`
	QueueStats   *QueueStats        `json:"queue_stats"`
	Reservations []ReservationPoint `json:"reservations"`
}

func (h *APIHandler) ComputeDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("compute_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data ComputeDashboardData
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		timeline, err := h.bq.GetConcurrentSlotsByState(ctx, filters)
		if err != nil {
			return err
		}
		data.SlotTimeline = timeline
		return nil
	})

	g.Go(func() error {
		jobs, err := h.bq.GetTopSlotJobs(ctx, filters)
		if err != nil {
			return err
		}
		data.TopJobs = jobs
		return nil
	})

	g.Go(func() error {
		usage, err := h.bq.GetSlotUsage(ctx, filters)
		if err != nil {
			return err
		}
		data.SlotUsage = usage
		return nil
	})

	g.Go(func() error {
		qs, err := h.bq.GetQueueStats(ctx, filters)
		if err != nil {
			return err
		}
		data.QueueStats = qs
		return nil
	})

	g.Go(func() error {
		// RESERVATIONS_TIMELINE is empty or unauthorized on pure on-demand
		// projects; the widget shows an empty state instead of an error.
		res, err := h.bq.GetReservationTimeline(ctx, filters)
		if err != nil {
			slog.Warn("reservation widget degraded", "error", err)
			return nil
		}
		data.Reservations = res
		return nil
	})

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 3: Pricing & Cost ---

type CostDashboardData struct {
	Summary   *CostSummary `json:"summary"`
	SpendBy   []SpendEntry `json:"spend_by"`
	DailyCost []DailyCost  `json:"daily_cost"`
}

func (h *APIHandler) CostDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("cost_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data CostDashboardData
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		cs, err := h.bq.GetCostSummary(ctx, filters)
		if err != nil {
			return err
		}
		data.Summary = cs
		return nil
	})

	g.Go(func() error {
		spend, err := h.bq.GetSpend(ctx, filters)
		if err != nil {
			return err
		}
		data.SpendBy = spend
		return nil
	})

	g.Go(func() error {
		daily, err := h.bq.GetDailyCost(ctx, filters)
		if err != nil {
			return err
		}
		data.DailyCost = daily
		return nil
	})

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 4: Insights ---

type InsightsDashboardData struct {
	Recommendations []Recommendation `json:"recommendations"`
	ErrorStats      []ErrorStat      `json:"error_stats"`
	FailingUsers    []FailingUser    `json:"failing_users"`
	PerfInsights    []PerfInsightJob `json:"perf_insights"`
	RepeatedQueries []RepeatedQuery  `json:"repeated_queries"`
}

func (h *APIHandler) InsightsDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("insights_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data InsightsDashboardData
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		recs, err := h.bq.GetRecommendations(ctx, filters.Region)
		if err != nil {
			return err
		}
		data.Recommendations = recs
		return nil
	})

	g.Go(func() error {
		es, err := h.bq.GetErrorStats(ctx, filters)
		if err != nil {
			return err
		}
		data.ErrorStats = es
		return nil
	})

	g.Go(func() error {
		fu, err := h.bq.GetTopFailingUsers(ctx, filters)
		if err != nil {
			return err
		}
		data.FailingUsers = fu
		return nil
	})

	g.Go(func() error {
		// performance_insights schema availability varies by region/edition;
		// degrade to empty rather than failing the whole tab.
		pi, err := h.bq.GetPerfInsightJobs(ctx, filters)
		if err != nil {
			slog.Warn("perf insights widget degraded", "error", err)
			return nil
		}
		data.PerfInsights = pi
		return nil
	})

	g.Go(func() error {
		rq, err := h.bq.GetRepeatedQueries(ctx, filters)
		if err != nil {
			slog.Warn("repeated queries widget degraded", "error", err)
			return nil
		}
		data.RepeatedQueries = rq
		return nil
	})

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 6: Jobs Explorer ---

type JobsDashboardData struct {
	Jobs []JobRow `json:"jobs"`
}

func (h *APIHandler) JobsDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("jobs_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	jobs, err := h.bq.ListJobs(r.Context(), filters)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := &JobsDashboardData{Jobs: jobs}
	h.cache.Set(key, data)
	writeJSON(w, data)
}

// --- Dashboard 5: IAM Security ---

type IAMDashboardData struct {
	Summary   *IAMSummary      `json:"summary"`
	Timeline  []UsageTimepoint `json:"timeline"`
	TopCallers []TopCaller     `json:"top_callers"`
	Inactive7  []InactiveEmail `json:"inactive_7d"`
	Inactive30 []InactiveEmail `json:"inactive_30d"`
	Inactive90 []InactiveEmail `json:"inactive_90d"`
}

func (h *APIHandler) IAMDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	emails := parseEmails(r)
	key := filters.CacheKey("iam_dashboard") + ":" + strings.Join(emails, ",")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data IAMDashboardData
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		s, err := h.bq.GetIAMSummary(ctx, filters.Region, filters.TimeRange)
		if err != nil {
			return err
		}
		data.Summary = s
		return nil
	})

	g.Go(func() error {
		t, err := h.bq.GetUsageTimeline(ctx, filters.Region, emails, filters.TimeRange)
		if err != nil {
			return err
		}
		data.Timeline = t
		return nil
	})

	g.Go(func() error {
		tc, err := h.bq.GetTopCallers(ctx, filters.Region, emails, filters.TimeRange, 20)
		if err != nil {
			return err
		}
		data.TopCallers = tc
		return nil
	})

	g.Go(func() error {
		// The >=30 and >=90 day lists are subsets of the >=7 day list, so one
		// 180-day JOBS_BY_PROJECT scan covers all three buckets.
		i, err := h.bq.GetInactiveEmails(ctx, filters.Region, 7)
		if err != nil {
			return err
		}
		data.Inactive7 = i
		data.Inactive30, data.Inactive90 = bucketInactiveEmails(i)
		return nil
	})

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// bucketInactiveEmails splits a >=7-day inactivity list into its >=30 and
// >=90 day subsets, preserving order.
func bucketInactiveEmails(inactive []InactiveEmail) (i30, i90 []InactiveEmail) {
	for _, e := range inactive {
		if e.DaysIdle >= 30 {
			i30 = append(i30, e)
		}
		if e.DaysIdle >= 90 {
			i90 = append(i90, e)
		}
	}
	return i30, i90
}

func (h *APIHandler) SearchEmails(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	if region == "" {
		region = "us"
	}
	prefix := r.URL.Query().Get("q")

	emails, err := h.bq.SearchEmails(r.Context(), region, prefix, 20)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if emails == nil {
		emails = []string{}
	}
	writeJSON(w, emails)
}

func parseEmails(r *http.Request) []string {
	raw := r.URL.Query().Get("emails")
	if raw == "" {
		return nil
	}
	var out []string
	for _, e := range splitTrimmed(raw, ",") {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// splitTrimmed splits s on sep, trims whitespace from each part, and drops
// empty parts.
func splitTrimmed(s, sep string) []string {
	parts := make([]string, 0)
	for _, p := range strings.Split(s, sep) {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// --- Regions ---

var bqRegions = []string{
	"us", "eu",
	"us-central1", "us-east1", "us-east4", "us-east5", "us-south1", "us-west1", "us-west2", "us-west3", "us-west4",
	"europe-central2", "europe-north1", "europe-southwest1", "europe-west1", "europe-west2", "europe-west3", "europe-west4", "europe-west6", "europe-west8", "europe-west9", "europe-west12",
	"asia-east1", "asia-east2", "asia-northeast1", "asia-northeast2", "asia-northeast3", "asia-south1", "asia-south2", "asia-southeast1", "asia-southeast2",
	"australia-southeast1", "australia-southeast2",
	"me-central1", "me-central2", "me-west1",
	"africa-south1",
	"northamerica-northeast1", "northamerica-northeast2",
	"southamerica-east1", "southamerica-west1",
}

func (h *APIHandler) ListRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, bqRegions)
}

// --- Legacy individual endpoints (kept for compatibility) ---

func (h *APIHandler) StorageStats(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	stats, err := h.bq.GetStorageStats(r.Context(), filters)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (h *APIHandler) SlotUsage(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	usage, err := h.bq.GetSlotUsage(r.Context(), filters)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, usage)
}

func (h *APIHandler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	datasets, err := h.bq.ListDatasets(r.Context())
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, datasets)
}

func (h *APIHandler) ListTables(w http.ResponseWriter, r *http.Request) {
	datasetID := r.URL.Query().Get("datasetId")
	if datasetID == "" {
		writeError(w, "datasetId is required", http.StatusBadRequest)
		return
	}
	tables, err := h.bq.ListTables(r.Context(), datasetID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, tables)
}

func (h *APIHandler) Config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.bq.config.BigQuery)
}
