package main

// HTTP handlers for the GCP Resources section (/api/gcp_resources/*).
// conf.yaml (gcp_resources.projects) is the source of truth for which
// projects may be queried; everything else is rejected.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"golang.org/x/sync/singleflight"
)

var resourcesFlight singleflight.Group

type ResProjectInfo struct {
	Project string `json:"project"`
	Error   string `json:"error,omitempty"`
}

type ResConfigResponse struct {
	Projects []ResProjectInfo `json:"projects"`
}

// resourceProject validates the ?project= parameter against the configured
// list — the server must not be usable to probe arbitrary projects.
func (h *APIHandler) resourceProject(r *http.Request) (string, error) {
	p := r.URL.Query().Get("project")
	if p == "" {
		return "", fmt.Errorf("project is required")
	}
	if !slices.Contains(h.bq.config.GCPResources.Projects, p) {
		return "", fmt.Errorf("project %q is not configured", p)
	}
	return p, nil
}

// probeResourceProject checks reachability with the cheapest possible call.
// Cached so the config GET doesn't hammer the Asset API.
func (h *APIHandler) probeResourceProject(ctx context.Context, project string) error {
	key := "resources:probe:" + project
	if cached, ok := h.cache.Get(key); ok {
		if s := cached.(string); s != "" {
			return fmt.Errorf("%s", s)
		}
		return nil
	}
	_, _, err := h.res.SearchAssets(ctx, project, "", "cloudresourcemanager.googleapis.com/Project")
	msg := ""
	if err != nil {
		msg = fmt.Sprintf(
			"cannot access %s: %v — grant roles/viewer and roles/cloudasset.viewer to the BigLens principal and enable the Cloud Asset API",
			project, err)
	}
	h.cache.Set(key, msg)
	if msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (h *APIHandler) ResourcesConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.resourcesConfigGet(w, r)
	case http.MethodPost:
		h.resourcesConfigPost(w, r)
	default:
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) resourcesConfigGet(w http.ResponseWriter, r *http.Request) {
	resp := ResConfigResponse{Projects: []ResProjectInfo{}}
	for _, p := range h.bq.config.GCPResources.Projects {
		info := ResProjectInfo{Project: p}
		if err := h.probeResourceProject(r.Context(), p); err != nil {
			info.Error = err.Error()
		}
		resp.Projects = append(resp.Projects, info)
	}
	writeJSON(w, &resp)
}

type resourcesConfigRequest struct {
	Action  string `json:"action"`
	Project string `json:"project"`
}

func (h *APIHandler) resourcesConfigPost(w http.ResponseWriter, r *http.Request) {
	var req resourcesConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validResourceProject(req.Project) {
		writeError(w, fmt.Sprintf("invalid project id %q", req.Project), http.StatusBadRequest)
		return
	}

	cfg := h.bq.config
	projects := cfg.GCPResources.Projects

	switch req.Action {
	case "add":
		if slices.Contains(projects, req.Project) {
			writeError(w, fmt.Sprintf("project %s is already configured", req.Project), http.StatusBadRequest)
			return
		}
		// Probe before persisting.
		h.cache.Delete("resources:probe:" + req.Project)
		if err := h.probeResourceProject(r.Context(), req.Project); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg.GCPResources.Projects = append(projects, req.Project)
	case "remove":
		i := slices.Index(projects, req.Project)
		if i < 0 {
			writeError(w, fmt.Sprintf("project %s is not configured", req.Project), http.StatusBadRequest)
			return
		}
		cfg.GCPResources.Projects = slices.Delete(slices.Clone(projects), i, i+1)
	default:
		writeError(w, `action must be "add" or "remove"`, http.StatusBadRequest)
		return
	}

	if err := SaveConfig(cfg); err != nil {
		writeError(w, fmt.Sprintf("failed to persist config: %v", err), http.StatusInternalServerError)
		return
	}
	h.resourcesConfigGet(w, r)
}
