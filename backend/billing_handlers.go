package main

// HTTP handlers for the GCP Billing section
// (/api/gcp_billing/*). Unlike the BigQuery Open Data sections this one
// queries user-configured datasets; conf.yaml is the source of truth.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

var billingInvoiceMonthRe = regexp.MustCompile(`^\d{6}$`)

func billingCSV(r *http.Request, name string) []string {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func parseBillingFilter(r *http.Request, cfg *Config) (BillingFilter, error) {
	q := r.URL.Query()
	var f BillingFilter

	f.DatasetFQN = q.Get("dataset")
	if f.DatasetFQN == "" {
		return f, fmt.Errorf("dataset is required")
	}
	if !slices.Contains(cfg.GCPBilling.Datasets, f.DatasetFQN) {
		return f, fmt.Errorf("dataset %q is not configured", f.DatasetFQN)
	}
	var err error
	if f.Project, f.Dataset, err = parseBillingDataset(f.DatasetFQN); err != nil {
		return f, err
	}

	f.End = civil.DateOf(time.Now().UTC())
	f.Start = f.End.AddDays(-30)
	if s := q.Get("start"); s != "" {
		if f.Start, err = civil.ParseDate(s); err != nil {
			return f, fmt.Errorf("invalid start date %q: %w", s, err)
		}
	}
	if s := q.Get("end"); s != "" {
		if f.End, err = civil.ParseDate(s); err != nil {
			return f, fmt.Errorf("invalid end date %q: %w", s, err)
		}
	}
	if !f.Start.Before(f.End) {
		return f, fmt.Errorf("start must be before end")
	}

	if m := q.Get("invoice_month"); m != "" {
		if !billingInvoiceMonthRe.MatchString(m) {
			return f, fmt.Errorf("invoice_month must be YYYYMM, got %q", m)
		}
		f.InvoiceMonth = m
	}

	f.Accounts = billingCSV(r, "accounts")
	f.Projects = billingCSV(r, "projects")
	f.Services = billingCSV(r, "services")
	if l := q.Get("label"); l != "" {
		k, v, ok := strings.Cut(l, ":")
		if !ok || k == "" || v == "" {
			return f, fmt.Errorf("label must be key:value, got %q", l)
		}
		f.LabelKey, f.LabelValue = k, v
	}
	return f, nil
}

var billingFlight singleflight.Group

type BillingDatasetInfo struct {
	Dataset         string   `json:"dataset"`
	HasStandard     bool     `json:"has_standard"`
	HasResource     bool     `json:"has_resource"`
	HasPricing      bool     `json:"has_pricing"`
	BillingAccounts []string `json:"billing_accounts"`
	Currency        string   `json:"currency"`
	Error           string   `json:"error,omitempty"`
}

type BillingConfigResponse struct {
	Datasets []BillingDatasetInfo `json:"datasets"`
}

// billingTables returns cached table detection for a configured dataset.
func (h *APIHandler) billingTables(ctx context.Context, datasetFQN string) (BillingTableInfo, error) {
	key := "billing:tables:" + datasetFQN
	if cached, ok := h.cache.Get(key); ok {
		return cached.(BillingTableInfo), nil
	}
	project, dataset, err := parseBillingDataset(datasetFQN)
	if err != nil {
		return BillingTableInfo{}, err
	}
	names, err := h.bq.ListBillingTables(ctx, project, dataset)
	if err != nil {
		return BillingTableInfo{}, err
	}
	info := classifyBillingTables(names)
	for _, table := range info.Standard {
		cur, err := h.bq.GetBillingCurrency(ctx, project, dataset, table)
		if err == nil && cur != "" {
			info.Currency = cur
		}
		break // one table is enough; currency is per billing account file
	}
	h.cache.Set(key, info)
	return info, nil
}

func billingDatasetInfo(info BillingTableInfo, fqn string) BillingDatasetInfo {
	accounts := make([]string, 0, len(info.Standard))
	for acct := range info.Standard {
		accounts = append(accounts, acct)
	}
	slices.Sort(accounts)
	return BillingDatasetInfo{
		Dataset:         fqn,
		HasStandard:     len(info.Standard) > 0,
		HasResource:     len(info.Resource) > 0,
		HasPricing:      info.HasPricing,
		BillingAccounts: accounts,
		Currency:        info.Currency,
	}
}

func (h *APIHandler) BillingConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.billingConfigGet(w, r)
	case http.MethodPost:
		h.billingConfigPost(w, r)
	default:
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) billingConfigGet(w http.ResponseWriter, r *http.Request) {
	resp := BillingConfigResponse{Datasets: []BillingDatasetInfo{}}
	for _, fqn := range h.bq.config.GCPBilling.Datasets {
		info, err := h.billingTables(r.Context(), fqn)
		if err != nil {
			resp.Datasets = append(resp.Datasets, BillingDatasetInfo{Dataset: fqn, Error: err.Error()})
			continue
		}
		resp.Datasets = append(resp.Datasets, billingDatasetInfo(info, fqn))
	}
	writeJSON(w, &resp)
}

type billingConfigRequest struct {
	Action  string `json:"action"`
	Dataset string `json:"dataset"`
}

func (h *APIHandler) billingConfigPost(w http.ResponseWriter, r *http.Request) {
	var req billingConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if _, _, err := parseBillingDataset(req.Dataset); err != nil {
		writeError(w, fmt.Sprintf("invalid dataset (expected project.dataset): %v", err), http.StatusBadRequest)
		return
	}

	cfg := h.bq.config
	datasets := cfg.GCPBilling.Datasets

	switch req.Action {
	case "add":
		if slices.Contains(datasets, req.Dataset) {
			writeError(w, fmt.Sprintf("dataset %s is already configured", req.Dataset), http.StatusBadRequest)
			return
		}
		// Probe before persisting: must be reachable and hold a standard export.
		info, err := h.billingTables(r.Context(), req.Dataset)
		if err != nil {
			writeError(w, fmt.Sprintf(
				"cannot access %s: %v — ensure the BigLens service account has roles/bigquery.dataViewer on it",
				req.Dataset, err), http.StatusBadRequest)
			return
		}
		if len(info.Standard) == 0 {
			writeError(w, fmt.Sprintf(
				"no gcp_billing_export_v1_* table found in %s — enable the standard usage cost export in Cloud Billing first",
				req.Dataset), http.StatusBadRequest)
			return
		}
		cfg.GCPBilling.Datasets = append(datasets, req.Dataset)
	case "remove":
		i := slices.Index(datasets, req.Dataset)
		if i < 0 {
			writeError(w, fmt.Sprintf("dataset %s is not configured", req.Dataset), http.StatusBadRequest)
			return
		}
		cfg.GCPBilling.Datasets = slices.Delete(slices.Clone(datasets), i, i+1)
	default:
		writeError(w, `action must be "add" or "remove"`, http.StatusBadRequest)
		return
	}

	if err := SaveConfig(cfg); err != nil {
		writeError(w, fmt.Sprintf("failed to persist config: %v", err), http.StatusInternalServerError)
		return
	}
	h.billingConfigGet(w, r)
}

type BillingMeta struct {
	Dataset       BillingDatasetInfo     `json:"dataset"`
	Projects      []BillingProjectOption `json:"projects"`
	Services      []string               `json:"services"`
	LabelKeys     []string               `json:"label_keys"`
	InvoiceMonths []string               `json:"invoice_months"`
}

func (h *APIHandler) BillingMeta(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := "billing:meta:" + f.DatasetFQN
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		info, err := h.billingTables(r.Context(), f.DatasetFQN)
		if err != nil {
			return nil, err
		}
		mf := billingMetaFilter(f)
		src, params := billingSource(f.Project, f.Dataset, mf.standardTables(info), mf)

		data := BillingMeta{
			Dataset:       billingDatasetInfo(info, f.DatasetFQN),
			Projects:      []BillingProjectOption{},
			Services:      []string{},
			LabelKeys:     []string{},
			InvoiceMonths: []string{},
		}
		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetBillingProjects(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing projects: %w", err)
			}
			if rows != nil {
				data.Projects = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingServices(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing services: %w", err)
			}
			if rows != nil {
				data.Services = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingLabelKeys(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing label keys: %w", err)
			}
			if rows != nil {
				data.LabelKeys = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingInvoiceMonths(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing invoice months: %w", err)
			}
			if rows != nil {
				data.InvoiceMonths = rows
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
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

type BillingOverviewData struct {
	Kpis              []BillingKpiRow   `json:"kpis"`
	Daily             []BillingDailyRow `json:"daily"`
	TopServices       []BillingGroupRow `json:"top_services"`
	TopProjects       []BillingGroupRow `json:"top_projects"`
	ProjectedMonthNet *float64          `json:"projected_month_net"`
}

// billingStandardSource resolves table detection and builds the standard-
// export FROM clause for f. Shared by overview/services/projects/credits.
func (h *APIHandler) billingStandardSource(ctx context.Context, f BillingFilter) (string, []bigquery.QueryParameter, error) {
	info, err := h.billingTables(ctx, f.DatasetFQN)
	if err != nil {
		return "", nil, err
	}
	tables := f.standardTables(info)
	if len(tables) == 0 {
		return "", nil, fmt.Errorf("no standard billing export table matches the selected accounts in %s", f.DatasetFQN)
	}
	src, params := billingSource(f.Project, f.Dataset, tables, f)
	return src, params, nil
}

func (h *APIHandler) BillingOverview(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := f.cacheKey("overview")
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		src, params, err := h.billingStandardSource(r.Context(), f)
		if err != nil {
			return nil, err
		}
		data := BillingOverviewData{
			Kpis:        []BillingKpiRow{},
			Daily:       []BillingDailyRow{},
			TopServices: []BillingGroupRow{},
			TopProjects: []BillingGroupRow{},
		}
		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetBillingKpis(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing kpis: %w", err)
			}
			if rows != nil {
				data.Kpis = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingDaily(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing daily: %w", err)
			}
			if rows != nil {
				data.Daily = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingGroups(ctx, src, billingGroupService, 5, params)
			if err != nil {
				return fmt.Errorf("billing top services: %w", err)
			}
			if rows != nil {
				data.TopServices = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingGroups(ctx, src, billingGroupProject, 5, params)
			if err != nil {
				return fmt.Errorf("billing top projects: %w", err)
			}
			if rows != nil {
				data.TopProjects = rows
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		data.ProjectedMonthNet = rollupBillingProjection(data.Daily, civil.DateOf(time.Now().UTC()))
		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

type BillingServicesData struct {
	Services []BillingGroupRow `json:"services"`
	Skus     []BillingSkuRow   `json:"skus"`
	Service  string            `json:"service"`
}

func (h *APIHandler) BillingServices(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	service := r.URL.Query().Get("service")
	key := f.cacheKey("services") + ":svc=" + service
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		src, params, err := h.billingStandardSource(r.Context(), f)
		if err != nil {
			return nil, err
		}
		data := BillingServicesData{Services: []BillingGroupRow{}, Skus: []BillingSkuRow{}, Service: service}
		if service == "" {
			rows, err := h.bq.GetBillingGroups(r.Context(), src, billingGroupService, 50, params)
			if err != nil {
				return nil, fmt.Errorf("billing services: %w", err)
			}
			if rows != nil {
				data.Services = rows
			}
		} else {
			rows, err := h.bq.GetBillingSkus(r.Context(), src, service, params)
			if err != nil {
				return nil, fmt.Errorf("billing skus: %w", err)
			}
			if rows != nil {
				data.Skus = rows
			}
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

type BillingProjectsData struct {
	Projects    []BillingProjectRow `json:"projects"`
	LabelGroups []BillingGroupRow   `json:"label_groups"`
	GroupKey    string              `json:"group_key"`
}

func (h *APIHandler) BillingProjects(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	groupLabel := r.URL.Query().Get("group_label")
	key := f.cacheKey("projects") + ":gl=" + groupLabel
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		src, params, err := h.billingStandardSource(r.Context(), f)
		if err != nil {
			return nil, err
		}
		data := BillingProjectsData{Projects: []BillingProjectRow{}, LabelGroups: []BillingGroupRow{}, GroupKey: groupLabel}
		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetBillingProjectRows(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing projects: %w", err)
			}
			if rows != nil {
				data.Projects = rows
			}
			return nil
		})
		if groupLabel != "" {
			g.Go(func() error {
				rows, err := h.bq.GetBillingLabelGroups(ctx, src, groupLabel, params)
				if err != nil {
					return fmt.Errorf("billing label groups: %w", err)
				}
				if rows != nil {
					data.LabelGroups = rows
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
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

type BillingCreditsData struct {
	Credits   []BillingCreditRow `json:"credits"`
	ByService []BillingGroupRow  `json:"by_service"`
}

func (h *APIHandler) BillingCredits(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := f.cacheKey("credits")
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		src, params, err := h.billingStandardSource(r.Context(), f)
		if err != nil {
			return nil, err
		}
		data := BillingCreditsData{Credits: []BillingCreditRow{}, ByService: []BillingGroupRow{}}
		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetBillingCreditRows(ctx, src, params)
			if err != nil {
				return fmt.Errorf("billing credits: %w", err)
			}
			if rows != nil {
				data.Credits = rows
			}
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetBillingGroups(ctx, src, billingGroupService, 50, params)
			if err != nil {
				return fmt.Errorf("billing credits by service: %w", err)
			}
			if rows != nil {
				data.ByService = rows
			}
			return nil
		})
		if err := g.Wait(); err != nil {
			return nil, err
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

type BillingResourcesData struct {
	Available bool                 `json:"available"`
	Resources []BillingResourceRow `json:"resources"`
}

func (h *APIHandler) BillingResources(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	search := r.URL.Query().Get("q")
	key := f.cacheKey("resources") + ":q=" + search
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		info, err := h.billingTables(r.Context(), f.DatasetFQN)
		if err != nil {
			return nil, err
		}
		data := BillingResourcesData{Resources: []BillingResourceRow{}}
		tables := f.resourceTables(info)
		if len(tables) == 0 {
			// No detailed export in this dataset: 200 + available:false so
			// the tab can render its enable-the-export banner.
			return &data, nil
		}
		data.Available = true
		src, params := billingSource(f.Project, f.Dataset, tables, f)
		rows, err := h.bq.GetBillingResources(r.Context(), src, search, params)
		if err != nil {
			return nil, fmt.Errorf("billing resources: %w", err)
		}
		if rows != nil {
			data.Resources = rows
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

type BillingPricingData struct {
	Available bool              `json:"available"`
	AsOf      string            `json:"as_of"`
	Prices    []BillingPriceRow `json:"prices"`
}

func (h *APIHandler) BillingPricing(w http.ResponseWriter, r *http.Request) {
	f, err := parseBillingFilter(r, h.bq.config)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	search := r.URL.Query().Get("q")
	key := f.cacheKey("pricing") + ":q=" + search
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := billingFlight.Do(key, func() (any, error) {
		info, err := h.billingTables(r.Context(), f.DatasetFQN)
		if err != nil {
			return nil, err
		}
		data := BillingPricingData{Prices: []BillingPriceRow{}}
		if !info.HasPricing {
			return &data, nil // 200 + available:false → frontend banner
		}
		data.Available = true
		rows, asOf, err := h.bq.GetBillingPricing(r.Context(), f.Project, f.Dataset, f.Services, search)
		if err != nil {
			return nil, fmt.Errorf("billing pricing: %w", err)
		}
		data.AsOf = asOf
		if rows != nil {
			data.Prices = rows
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
