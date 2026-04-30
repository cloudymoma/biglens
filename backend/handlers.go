package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
)

type APIHandler struct {
	bq    *BQClient
	cache *Cache
}

func NewAPIHandler(bq *BQClient) *APIHandler {
	return &APIHandler{
		bq:    bq,
		cache: NewCache(10 * time.Minute),
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
	Billing   *StorageStats     `json:"billing"`
	Breakdown *StorageBreakdown `json:"breakdown"`
	TopTables []TopTable        `json:"top_tables"`
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

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 2: Slots & Compute ---

type ComputeDashboardData struct {
	SlotTimeline []SlotTimepoint `json:"slot_timeline"`
	TopJobs      []TopSlotJob    `json:"top_jobs"`
	SlotUsage    []SlotUsage     `json:"slot_usage"`
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
		timeline, err := h.bq.GetConcurrentSlots(ctx, filters)
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

	if err := g.Wait(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cache.Set(key, &data)
	writeJSON(w, &data)
}

// --- Dashboard 3: Pricing & Cost ---

type CostDashboardData struct {
	Summary     *CostSummary `json:"summary"`
	SpendByUser []UserSpend  `json:"spend_by_user"`
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
		spend, err := h.bq.GetSpendByUser(ctx, filters)
		if err != nil {
			return err
		}
		data.SpendByUser = spend
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
}

func (h *APIHandler) InsightsDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("insights_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	recs, err := h.bq.GetRecommendations(r.Context(), filters.Region)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := &InsightsDashboardData{Recommendations: recs}
	h.cache.Set(key, data)
	writeJSON(w, data)
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
