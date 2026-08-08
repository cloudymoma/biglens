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
	"time"

	"golang.org/x/sync/errgroup"
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

// Response types for data endpoints
type ResOverviewData struct {
	FetchedAt      string          `json:"fetched_at"`
	TotalResources int             `json:"total_resources"`
	VMsRunning     int             `json:"vms_running"`
	VMsStopped     int             `json:"vms_stopped"`
	Buckets        int             `json:"buckets"`
	VPCs           int             `json:"vpcs"`
	FirewallRules  int             `json:"firewall_rules"`
	ByService      []ResNamedCount `json:"by_service"`
	ByLocation     []ResNamedCount `json:"by_location"`
	Recent         []AssetItem     `json:"recent"`
	Truncated      bool            `json:"truncated"`
}
type ResComputeData struct {
	FetchedAt string       `json:"fetched_at"`
	Instances []VMInstance `json:"instances"`
	Disks     []DiskInfo   `json:"disks"`
}
type ResStorageData struct {
	FetchedAt string       `json:"fetched_at"`
	Buckets   []BucketInfo `json:"buckets"`
}
type ResNetworkData struct {
	FetchedAt       string               `json:"fetched_at"`
	Networks        []VPCInfo            `json:"networks"`
	Subnets         []SubnetInfo         `json:"subnets"`
	Addresses       []AddressInfo        `json:"addresses"`
	Firewalls       []FirewallInfo       `json:"firewalls"`
	ForwardingRules []ForwardingRuleInfo `json:"forwarding_rules"`
}
type ResExplorerData struct {
	FetchedAt string      `json:"fetched_at"`
	Items     []AssetItem `json:"items"`
	Truncated bool        `json:"truncated"`
}
type ResInsightsData struct {
	FetchedAt string    `json:"fetched_at"`
	Findings  []Finding `json:"findings"`
}

// resServe is the shared cache/singleflight/refresh wrapper for every data
// endpoint. fetch must be side-effect-free; its result is cached as-is.
func (h *APIHandler) resServe(w http.ResponseWriter, r *http.Request, endpoint string,
	fetch func(ctx context.Context, project string) (any, error)) {

	project, err := h.resourceProject(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := "resources:" + endpoint + ":" + project
	if r.URL.Query().Get("refresh") == "1" {
		h.cache.Delete(key)
	} else if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	// Detach from initiating request's context so cancellation doesn't kill shared waiters.
	fetchCtx := context.WithoutCancel(r.Context())
	data, err, _ := resourcesFlight.Do(key, func() (any, error) {
		return fetch(fetchCtx, project)
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}
	h.cache.Set(key, data)
	writeJSON(w, data)
}

func resNow() string { return time.Now().UTC().Format(time.RFC3339) }

func (h *APIHandler) ResourcesOverview(w http.ResponseWriter, r *http.Request) {
	h.resServe(w, r, "overview", func(ctx context.Context, project string) (any, error) {
		d := ResOverviewData{FetchedAt: resNow()}
		var assets []AssetItem
		var vms []VMInstance
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { assets, d.Truncated, err = h.res.SearchAssets(gctx, project, "", ""); return })
		g.Go(func() (err error) { vms, err = h.res.ListInstances(gctx, project); return })
		g.Go(func() error {
			b, err := h.res.ListBuckets(gctx, project)
			d.Buckets = len(b)
			return err
		})
		g.Go(func() error {
			n, err := h.res.ListNetworks(gctx, project)
			d.VPCs = len(n)
			return err
		})
		g.Go(func() error {
			f, err := h.res.ListFirewalls(gctx, project)
			d.FirewallRules = len(f)
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		d.TotalResources = len(assets)
		d.ByService = countAssetsByService(assets)
		d.ByLocation = countAssetsByLocation(assets)
		d.Recent = recentAssets(assets, 15)
		for _, vm := range vms {
			if vm.Status == "RUNNING" {
				d.VMsRunning++
			} else if vm.Status == "TERMINATED" {
				d.VMsStopped++
			}
		}
		return &d, nil
	})
}

func (h *APIHandler) ResourcesCompute(w http.ResponseWriter, r *http.Request) {
	h.resServe(w, r, "compute", func(ctx context.Context, project string) (any, error) {
		d := ResComputeData{FetchedAt: resNow(), Instances: []VMInstance{}, Disks: []DiskInfo{}}
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			v, err := h.res.ListInstances(gctx, project)
			if v != nil {
				d.Instances = v
			}
			return err
		})
		g.Go(func() error {
			dk, err := h.res.ListDisks(gctx, project)
			if dk != nil {
				d.Disks = dk
			}
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return &d, nil
	})
}

func (h *APIHandler) ResourcesStorage(w http.ResponseWriter, r *http.Request) {
	h.resServe(w, r, "storage", func(ctx context.Context, project string) (any, error) {
		d := ResStorageData{FetchedAt: resNow(), Buckets: []BucketInfo{}}
		var bytesByBucket map[string]map[string]float64
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			b, err := h.res.ListBuckets(gctx, project)
			if b != nil {
				d.Buckets = b
			}
			return err
		})
		// Monitoring failure only loses sizes, not the bucket list.
		g.Go(func() error { bytesByBucket, _ = h.res.BucketBytes(gctx, project); return nil })
		if err := g.Wait(); err != nil {
			return nil, err
		}
		for i := range d.Buckets {
			d.Buckets[i].BytesByClass = bytesByBucket[d.Buckets[i].Name]
		}
		return &d, nil
	})
}

func (h *APIHandler) ResourcesNetwork(w http.ResponseWriter, r *http.Request) {
	h.resServe(w, r, "network", func(ctx context.Context, project string) (any, error) {
		d := ResNetworkData{
			FetchedAt: resNow(), Networks: []VPCInfo{}, Subnets: []SubnetInfo{},
			Addresses: []AddressInfo{}, Firewalls: []FirewallInfo{}, ForwardingRules: []ForwardingRuleInfo{},
		}
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			v, err := h.res.ListNetworks(gctx, project)
			if v != nil {
				d.Networks = v
			}
			return err
		})
		g.Go(func() error {
			v, err := h.res.ListSubnets(gctx, project)
			if v != nil {
				d.Subnets = v
			}
			return err
		})
		g.Go(func() error {
			v, err := h.res.ListAddresses(gctx, project)
			if v != nil {
				d.Addresses = v
			}
			return err
		})
		g.Go(func() error {
			v, err := h.res.ListFirewalls(gctx, project)
			if v != nil {
				d.Firewalls = v
			}
			return err
		})
		g.Go(func() error {
			v, err := h.res.ListForwardingRules(gctx, project)
			if v != nil {
				d.ForwardingRules = v
			}
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return &d, nil
	})
}

func (h *APIHandler) ResourcesExplorer(w http.ResponseWriter, r *http.Request) {
	// Explorer varies by query params, so they join the cache key.
	q := r.URL.Query().Get("query")
	at := r.URL.Query().Get("asset_type")
	h.resServe(w, r, "explorer:"+q+":"+at, func(ctx context.Context, project string) (any, error) {
		items, truncated, err := h.res.SearchAssets(ctx, project, q, at)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []AssetItem{}
		}
		return &ResExplorerData{FetchedAt: resNow(), Items: items, Truncated: truncated}, nil
	})
}

func (h *APIHandler) ResourcesInsights(w http.ResponseWriter, r *http.Request) {
	h.resServe(w, r, "insights", func(ctx context.Context, project string) (any, error) {
		var vms []VMInstance
		var disks []DiskInfo
		var buckets []BucketInfo
		var vpcs []VPCInfo
		var addrs []AddressInfo
		var fws []FirewallInfo
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { vms, err = h.res.ListInstances(gctx, project); return })
		g.Go(func() (err error) { disks, err = h.res.ListDisks(gctx, project); return })
		g.Go(func() (err error) { buckets, err = h.res.ListBuckets(gctx, project); return })
		g.Go(func() (err error) { vpcs, err = h.res.ListNetworks(gctx, project); return })
		g.Go(func() (err error) { addrs, err = h.res.ListAddresses(gctx, project); return })
		g.Go(func() (err error) { fws, err = h.res.ListFirewalls(gctx, project); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return &ResInsightsData{FetchedAt: resNow(), Findings: buildFindings(vms, disks, buckets, vpcs, addrs, fws)}, nil
	})
}
