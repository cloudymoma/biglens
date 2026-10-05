package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

type APIHandler struct {
	bq     *BQClient
	cache  *Cache
	bundle *OKFBundle
	res    ResourceAPI
	risk   *addressRiskService
	sf     singleflight.Group
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
	Billing         *StorageStats     `json:"billing"`
	Breakdown       *StorageBreakdown `json:"breakdown"`
	TopTables       []TopTable        `json:"top_tables"`
	SearchIndexes   []SearchIndexInfo `json:"search_indexes"`
	DatasetStorage  []DatasetStorage  `json:"dataset_storage"`
	ColdTables      []ColdTable       `json:"cold_tables"`
	DegradedWidgets []string          `json:"degraded_widgets,omitempty"`
}

func searchIndexesCacheKey(f QueryFilters) string {
	return "storage_search_indexes:" + f.Region + ":" + f.Dataset + ":" + f.Table
}

func (h *APIHandler) StorageDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	if filters.Dataset != "" && !datasetNameRe.MatchString(filters.Dataset) {
		writeError(w, "invalid dataset name", http.StatusBadRequest)
		return
	}
	key := filters.CacheKey("storage_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data StorageDashboardData
	var mu sync.Mutex
	addDegraded := func(name string) {
		mu.Lock()
		data.DegradedWidgets = append(data.DegradedWidgets, name)
		mu.Unlock()
	}
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		stats, bd, ds, err := h.bq.GetStorageOverview(ctx, filters)
		if err != nil {
			return err
		}
		data.Billing = stats
		data.Breakdown = bd
		data.DatasetStorage = ds
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
		idxKey := searchIndexesCacheKey(filters)
		if cached, ok := h.cache.Get(idxKey); ok {
			if indexes, ok := cached.([]SearchIndexInfo); ok {
				data.SearchIndexes = indexes
				return nil
			}
		}
		indexes, err := h.bq.GetSearchIndexes(ctx, filters)
		if err != nil {
			slog.Warn("search indexes widget degraded", "error", err)
			addDegraded("search_indexes")
			return nil
		}
		h.cache.SetWithTTL(idxKey, indexes, time.Hour)
		data.SearchIndexes = indexes
		return nil
	})

	g.Go(func() error {
		// Cold-table detection needs jobs history; degrade to empty if the
		// caller lacks bigquery.jobs.listAll rather than failing storage.
		cold, err := h.bq.GetColdTables(ctx, filters)
		if err != nil {
			slog.Warn("cold tables widget degraded", "error", err)
			addDegraded("cold_tables")
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
	SlotTimeline      []SlotBucket       `json:"slot_timeline"`
	SlotBucketSeconds int64              `json:"slot_bucket_seconds"`
	TopJobs           []TopSlotJob       `json:"top_jobs"`
	SlotUsage         []SlotUsage        `json:"slot_usage"`
	QueueStats        *QueueStats        `json:"queue_stats"`
	Reservations      []ReservationPoint `json:"reservations"`
	DegradedWidgets   []string           `json:"degraded_widgets,omitempty"`
}

func (h *APIHandler) ComputeDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("compute_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	data := ComputeDashboardData{SlotBucketSeconds: filters.TimelineBucketSeconds()}
	var mu sync.Mutex
	addDegraded := func(name string) {
		mu.Lock()
		data.DegradedWidgets = append(data.DegradedWidgets, name)
		mu.Unlock()
	}
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
			addDegraded("reservations")
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
	DegradedWidgets []string         `json:"degraded_widgets,omitempty"`
}

func (h *APIHandler) InsightsDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := filters.CacheKey("insights_dashboard")

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data InsightsDashboardData
	var mu sync.Mutex
	addDegraded := func(name string) {
		mu.Lock()
		data.DegradedWidgets = append(data.DegradedWidgets, name)
		mu.Unlock()
	}
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
			addDegraded("perf_insights")
			return nil
		}
		data.PerfInsights = pi
		return nil
	})

	g.Go(func() error {
		rq, err := h.bq.GetRepeatedQueries(ctx, filters)
		if err != nil {
			slog.Warn("repeated queries widget degraded", "error", err)
			addDegraded("repeated_queries")
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
	Summary         *IAMSummary      `json:"summary"`
	Timeline        []UsageTimepoint `json:"timeline"`
	TopCallers      []TopCaller      `json:"top_callers"`
	Inactive7       []InactiveEmail  `json:"inactive_7d"`
	Inactive30      []InactiveEmail  `json:"inactive_30d"`
	Inactive90      []InactiveEmail  `json:"inactive_90d"`
	NewActors       []NewActor       `json:"new_actors"`
	OffHours        []OffHoursCell   `json:"off_hours"`
	OffHoursTop     []OffHoursUser   `json:"off_hours_top"`
	Exfil           []ExfilSignal    `json:"exfil_signals"`
	DegradedWidgets []string         `json:"degraded_widgets,omitempty"`
}

func iamCacheKey(region, timeRange string, emails []string) string {
	return "iam_dashboard:" + region + ":" + timeRange + ":" + strings.Join(emails, ",")
}

func (h *APIHandler) IAMDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	emails := parseEmails(r)
	key := iamCacheKey(filters.Region, filters.TimeRange, emails)

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	var data IAMDashboardData
	var mu sync.Mutex
	addDegraded := func(name string) {
		mu.Lock()
		data.DegradedWidgets = append(data.DegradedWidgets, name)
		mu.Unlock()
	}
	g, ctx := errgroup.WithContext(r.Context())

	g.Go(func() error {
		s, tc, err := h.bq.GetIdentityStats(ctx, filters.Region, emails, filters.TimeRange, 20)
		if err != nil {
			slog.Warn("identity stats widget degraded", "error", err)
			addDegraded("top_callers")
			return nil
		}
		data.Summary = s
		data.TopCallers = tc
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
		// The 180-day scan is independent of time_range and emails, so cache
		// the raw rows per region for 30 minutes with singleflight deduplication
		// and slice both Inactive (7/30/90d) and NewActors in Go.
		longKey := "iam_long_window_180d:" + filters.Region
		var rows []longWindowIAMRow
		if cached, ok := h.cache.Get(longKey); ok {
			rows, _ = cached.([]longWindowIAMRow)
		}
		if rows == nil {
			v, err, _ := h.sf.Do(longKey, func() (any, error) {
				if cached, ok := h.cache.Get(longKey); ok {
					return cached, nil
				}
				fetched, err := h.bq.GetLongWindowIAM(ctx, filters.Region)
				if err != nil {
					return nil, err
				}
				h.cache.SetWithTTL(longKey, fetched, 30*time.Minute)
				return fetched, nil
			})
			if err != nil {
				slog.Warn("long-window IAM widgets degraded", "error", err)
				addDegraded("inactive_emails")
				addDegraded("new_actors")
				return nil
			}
			rows, _ = v.([]longWindowIAMRow)
		}
		i7, na := splitLongWindowIAM(rows, 7, emails)
		data.Inactive7 = i7
		data.Inactive30, data.Inactive90 = bucketInactiveEmails(i7)
		data.NewActors = na
		return nil
	})

	g.Go(func() error {
		cells, top, err := h.bq.GetOffHours(ctx, filters.Region, emails, filters.TimeRange)
		if err != nil {
			slog.Warn("off-hours widget degraded", "error", err)
			addDegraded("off_hours")
			return nil
		}
		data.OffHours = cells
		data.OffHoursTop = top
		return nil
	})

	g.Go(func() error {
		ex, err := h.bq.GetExfilSignals(ctx, filters.Region, emails, filters.TimeRange)
		if err != nil {
			slog.Warn("exfil signals widget degraded", "error", err)
			addDegraded("exfil_signals")
			return nil
		}
		data.Exfil = ex
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
	region = validateRegion(region)
	prefix := r.URL.Query().Get("q")

	cacheKey := "iam_distinct_emails_180d:" + region
	var allEmails []string
	if cached, ok := h.cache.Get(cacheKey); ok {
		allEmails = cached.([]string)
	} else {
		v, err, _ := h.sf.Do(cacheKey, func() (any, error) {
			if c, ok := h.cache.Get(cacheKey); ok {
				return c, nil
			}
			emails, err := h.bq.GetDistinctEmails180d(r.Context(), region)
			if err != nil {
				return nil, err
			}
			h.cache.SetWithTTL(cacheKey, emails, 30*time.Minute)
			return emails, nil
		})
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		allEmails = v.([]string)
	}

	writeJSON(w, filterEmailsByPrefix(allEmails, prefix, 20))
}

func parseEmails(r *http.Request) []string {
	raw := r.URL.Query().Get("emails")
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range splitTrimmed(raw, ",") {
		norm := strings.ToLower(e)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	sort.Strings(out)
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

// --- Dashboard 5b: Access Posture (IAM & Security tab) ---

type SecurityDashboardData struct {
	PublicFlags            []PublicFlag      `json:"public_flags"`
	Principals             []PrincipalGrant  `json:"principals"`
	UnusedGrants           []PrincipalGrant  `json:"unused_grants"`
	ProjectBindings        []ProjectBinding  `json:"project_bindings"`
	TagBypassers           []string          `json:"tag_bypassers"`
	ProjectIAMError        string            `json:"project_iam_error"`
	DatasetPosture         []DatasetPosture  `json:"dataset_posture"`
	RLSPolicies            []RLSPolicy       `json:"rls_policies"`
	RLSScan                RLSScan           `json:"rls_scan"`
	SensitiveColumns       []SensitiveColumn `json:"sensitive_columns"`
	UntaggedSensitiveTotal int64             `json:"untagged_sensitive_total"`
	DatasetsScanned        int               `json:"datasets_scanned"`
	DatasetsTotal          int               `json:"datasets_total"`
	GrantsDatasetsFailed   int               `json:"grants_datasets_failed,omitempty"`
	DegradedWidgets        []string          `json:"degraded_widgets,omitempty"`
}

func (h *APIHandler) SecurityDashboard(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r)
	key := "security_dashboard:" + filters.Region

	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := h.sf.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}

		var data SecurityDashboardData
		var mu sync.Mutex
		addDegraded := func(name string) {
			mu.Lock()
			data.DegradedWidgets = append(data.DegradedWidgets, name)
			mu.Unlock()
		}
		g, ctx := errgroup.WithContext(r.Context())

		var activePrincipals map[string]bool

		g.Go(func() error {
			dp, err := h.bq.GetDatasetPosture(ctx, filters.Region)
			if err != nil {
				return err
			}
			data.DatasetPosture = dp
			datasets, total := datasetNamesFromPosture(dp)
			data.DatasetsScanned = len(datasets)
			data.DatasetsTotal = total

			g.Go(func() error {
				h.fillRLS(ctx, filters.Region, datasets, &data, addDegraded)
				return nil
			})

			grants, failed := h.bq.GetObjectGrants(ctx, filters.Region, datasets)
			data.PublicFlags = publicFlags(grants)
			data.Principals = buildPrincipalGrants(grants)
			if failed > 0 {
				data.GrantsDatasetsFailed = failed
				addDegraded("object_grants")
			}
			return nil
		})

		g.Go(func() error {
			active, err := h.bq.GetActivePrincipals(ctx, filters.Region, "90d")
			if err != nil {
				slog.Warn("unused grants widget degraded", "error", err)
				addDegraded("unused_grants")
				return nil
			}
			activePrincipals = active
			return nil
		})

		g.Go(func() error {
			// Needs resourcemanager.projects.getIamPolicy; degrade with a hint.
			bindings, bypassers, err := h.bq.GetProjectBindings(ctx)
			if err != nil {
				slog.Warn("project IAM widget degraded", "error", err)
				addDegraded("project_iam")
				data.ProjectIAMError = "Project-level bindings unavailable — grant the service account roles/browser (or resourcemanager.projects.getIamPolicy)."
				return nil
			}
			data.ProjectBindings = bindings
			data.TagBypassers = bypassers
			return nil
		})

		g.Go(func() error {
			// COLUMN_FIELD_PATHS region support varies; degrade to empty.
			sc, untaggedTotal, err := h.bq.GetSensitiveColumns(ctx, filters.Region)
			if err != nil {
				slog.Warn("sensitive columns widget degraded", "error", err)
				addDegraded("sensitive_columns")
				return nil
			}
			data.SensitiveColumns = sc
			data.UntaggedSensitiveTotal = untaggedTotal
			return nil
		})

		if err := g.Wait(); err != nil {
			return nil, err
		}

		if activePrincipals != nil {
			merged := mergeProjectPrincipals(data.Principals, data.ProjectBindings)
			data.UnusedGrants = computeUnusedGrants(merged, activePrincipals)
		}

		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, v)
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
