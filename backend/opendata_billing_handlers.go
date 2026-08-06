package main

// HTTP handlers for the GCP Billing open-data section
// (/api/opendata/gcp_billing/*). Unlike other open-data sections this one
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

	"cloud.google.com/go/civil"
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
	if !slices.Contains(cfg.OpenData.GCPBilling.Datasets, f.DatasetFQN) {
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
	key := "opendata:billing:tables:" + datasetFQN
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
	for _, fqn := range h.bq.config.OpenData.GCPBilling.Datasets {
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
	datasets := cfg.OpenData.GCPBilling.Datasets

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
		cfg.OpenData.GCPBilling.Datasets = append(datasets, req.Dataset)
	case "remove":
		i := slices.Index(datasets, req.Dataset)
		if i < 0 {
			writeError(w, fmt.Sprintf("dataset %s is not configured", req.Dataset), http.StatusBadRequest)
			return
		}
		cfg.OpenData.GCPBilling.Datasets = slices.Delete(slices.Clone(datasets), i, i+1)
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
