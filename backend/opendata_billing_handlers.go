package main

// HTTP handlers for the GCP Billing open-data section
// (/api/opendata/gcp_billing/*). Unlike other open-data sections this one
// queries user-configured datasets; conf.yaml is the source of truth.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"golang.org/x/sync/singleflight"
)

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
