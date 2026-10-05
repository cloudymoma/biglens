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
	"sync"
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

func (h *APIHandler) configuredResourceProjects() []string {
	configMu.Lock()
	defer configMu.Unlock()
	return slices.Clone(h.bq.config.GCPResources.Projects)
}

// resourceProject validates the ?project= parameter against the configured
// list — the server must not be usable to probe arbitrary projects.
func (h *APIHandler) resourceProject(r *http.Request) (string, error) {
	p := r.URL.Query().Get("project")
	if p == "" {
		return "", fmt.Errorf("project is required")
	}
	if !slices.Contains(h.configuredResourceProjects(), p) {
		return "", fmt.Errorf("project %q is not configured", p)
	}
	return p, nil
}

// probeResourceProject checks reachability with the cheapest possible call.
// Successful probes are cached for the default 10m TTL; errors use a short 1m
// TTL so newly granted IAM permissions take effect quickly.
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
		h.cache.SetWithTTL(key, msg, time.Minute)
		return fmt.Errorf("%s", msg)
	}
	h.cache.Set(key, msg)
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
	projects := h.configuredResourceProjects()
	resp := ResConfigResponse{Projects: make([]ResProjectInfo, len(projects))}
	var g errgroup.Group
	g.SetLimit(4)
	for i, p := range projects {
		i, p := i, p
		g.Go(func() error {
			info := ResProjectInfo{Project: p}
			if err := h.probeResourceProject(r.Context(), p); err != nil {
				info.Error = err.Error()
			}
			resp.Projects[i] = info
			return nil
		})
	}
	_ = g.Wait()
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

	projects := h.configuredResourceProjects()

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
		var dup bool
		if err := UpdateConfig(h.bq.config, func(c *Config) {
			if slices.Contains(c.GCPResources.Projects, req.Project) {
				dup = true
				return
			}
			c.GCPResources.Projects = append(slices.Clone(c.GCPResources.Projects), req.Project)
		}); err != nil {
			writeError(w, fmt.Sprintf("failed to persist config: %v", err), http.StatusInternalServerError)
			return
		}
		if dup {
			writeError(w, fmt.Sprintf("project %s is already configured", req.Project), http.StatusBadRequest)
			return
		}
	case "remove":
		if !slices.Contains(projects, req.Project) {
			writeError(w, fmt.Sprintf("project %s is not configured", req.Project), http.StatusBadRequest)
			return
		}
		var missing bool
		if err := UpdateConfig(h.bq.config, func(c *Config) {
			i := slices.Index(c.GCPResources.Projects, req.Project)
			if i < 0 {
				missing = true
				return
			}
			c.GCPResources.Projects = slices.Delete(slices.Clone(c.GCPResources.Projects), i, i+1)
		}); err != nil {
			writeError(w, fmt.Sprintf("failed to persist config: %v", err), http.StatusInternalServerError)
			return
		}
		if missing {
			writeError(w, fmt.Sprintf("project %s is not configured", req.Project), http.StatusBadRequest)
			return
		}
	default:
		writeError(w, `action must be "add" or "remove"`, http.StatusBadRequest)
		return
	}

	h.resourcesConfigGet(w, r)
}

// Response types for data endpoints
type ResOverviewData struct {
	FetchedAt      string            `json:"fetched_at"`
	TotalResources int               `json:"total_resources"`
	VMsRunning     int               `json:"vms_running"`
	VMsStopped     int               `json:"vms_stopped"`
	Buckets        int               `json:"buckets"`
	VPCs           int               `json:"vpcs"`
	FirewallRules  int               `json:"firewall_rules"`
	ByService      []ResNamedCount   `json:"by_service"`
	ByLocation     []ResNamedCount   `json:"by_location"`
	Recent         []AssetItem       `json:"recent"`
	Truncated      bool              `json:"truncated"`
	PartialErrors  map[string]string `json:"partial_errors,omitempty"`
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
	FetchedAt     string            `json:"fetched_at"`
	Findings      []Finding         `json:"findings"`
	PartialErrors map[string]string `json:"partial_errors,omitempty"`
}

var resourceListNames = []string{
	"assets", "instances", "disks", "buckets", "bucket_bytes",
	"networks", "subnets", "addresses", "firewalls", "fwd_rules",
}

func (h *APIHandler) invalidateResourceLists(project string) {
	for _, name := range resourceListNames {
		h.cache.Delete("resources:list:" + name + ":" + project)
	}
}

func resListCached[T any](h *APIHandler, ctx context.Context, project, listName string,
	fn func(context.Context, string) (T, error)) (T, error) {

	key := "resources:list:" + listName + ":" + project
	if cached, ok := h.cache.Get(key); ok {
		return cached.(T), nil
	}
	v, err, _ := resourcesFlight.Do(key, func() (any, error) {
		res, err := fn(ctx, project)
		if err != nil {
			return res, err
		}
		h.cache.Set(key, res)
		return res, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

type resAssetSearchResult struct {
	items     []AssetItem
	truncated bool
}

func (h *APIHandler) resAllAssets(ctx context.Context, project string) ([]AssetItem, bool, error) {
	res, err := resListCached(h, ctx, project, "assets", func(ctx context.Context, p string) (resAssetSearchResult, error) {
		items, trunc, err := h.res.SearchAssets(ctx, p, "", "")
		return resAssetSearchResult{items: items, truncated: trunc}, err
	})
	return res.items, res.truncated, err
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
		h.invalidateResourceLists(project)
	} else if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	// Detach from initiating request's context so cancellation doesn't kill shared waiters,
	// while bounding upstream latency to 60s.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
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
		var (
			assets                          []AssetItem
			vms                             []VMInstance
			buckets                         []BucketInfo
			vpcs                            []VPCInfo
			fws                             []FirewallInfo
			assetsErr, storageErr           error
			instErr, netErr, fwErr          error
			wg                              sync.WaitGroup
		)
		wg.Add(5)
		go func() { defer wg.Done(); assets, d.Truncated, assetsErr = h.resAllAssets(ctx, project) }()
		go func() { defer wg.Done(); vms, instErr = resListCached(h, ctx, project, "instances", h.res.ListInstances) }()
		go func() { defer wg.Done(); buckets, storageErr = resListCached(h, ctx, project, "buckets", h.res.ListBuckets) }()
		go func() { defer wg.Done(); vpcs, netErr = resListCached(h, ctx, project, "networks", h.res.ListNetworks) }()
		go func() { defer wg.Done(); fws, fwErr = resListCached(h, ctx, project, "firewalls", h.res.ListFirewalls) }()
		wg.Wait()

		computeErr := instErr
		if computeErr == nil {
			computeErr = netErr
		}
		if computeErr == nil {
			computeErr = fwErr
		}
		// Require at least two of the three API families (Asset Inventory, Cloud Storage, Compute Engine)
		// to succeed so a data-only project with Compute Engine disabled degrades gracefully while a
		// widespread credential/API outage still surfaces as 502.
		familiesFailed := 0
		var firstErr error
		for _, err := range []error{assetsErr, storageErr, computeErr} {
			if err != nil {
				familiesFailed++
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if familiesFailed >= 2 {
			return nil, firstErr
		}
		partial := map[string]string{}
		if assetsErr != nil {
			partial["assets"] = assetsErr.Error()
		}
		if storageErr != nil {
			partial["storage"] = storageErr.Error()
		}
		if computeErr != nil {
			partial["compute"] = computeErr.Error()
		}
		if len(partial) > 0 {
			d.PartialErrors = partial
		}

		d.Buckets = len(buckets)
		d.VPCs = len(vpcs)
		d.FirewallRules = len(fws)
		d.TotalResources = len(assets)
		d.ByService = countAssetsByService(assets)
		d.ByLocation = countAssetsByLocation(assets)
		d.Recent = recentAssets(assets, 15)
		for _, vm := range vms {
			if vm.Status == "RUNNING" {
				d.VMsRunning++
			} else if vm.Status == "TERMINATED" || vm.Status == "SUSPENDED" {
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
			v, err := resListCached(h, gctx, project, "instances", h.res.ListInstances)
			if v != nil {
				d.Instances = v
			}
			return err
		})
		g.Go(func() error {
			dk, err := resListCached(h, gctx, project, "disks", h.res.ListDisks)
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
			b, err := resListCached(h, gctx, project, "buckets", h.res.ListBuckets)
			if b != nil {
				d.Buckets = slices.Clone(b)
			}
			return err
		})
		// Monitoring failure only loses sizes, not the bucket list.
		g.Go(func() error {
			bytesByBucket, _ = resListCached(h, gctx, project, "bucket_bytes", h.res.BucketBytes)
			return nil
		})
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
			v, err := resListCached(h, gctx, project, "networks", h.res.ListNetworks)
			if v != nil {
				d.Networks = v
			}
			return err
		})
		g.Go(func() error {
			v, err := resListCached(h, gctx, project, "subnets", h.res.ListSubnets)
			if v != nil {
				d.Subnets = v
			}
			return err
		})
		g.Go(func() error {
			v, err := resListCached(h, gctx, project, "addresses", h.res.ListAddresses)
			if v != nil {
				d.Addresses = v
			}
			return err
		})
		g.Go(func() error {
			v, err := resListCached(h, gctx, project, "firewalls", h.res.ListFirewalls)
			if v != nil {
				d.Firewalls = v
			}
			return err
		})
		g.Go(func() error {
			v, err := resListCached(h, gctx, project, "fwd_rules", h.res.ListForwardingRules)
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
		var (
			items     []AssetItem
			truncated bool
			err       error
		)
		if q == "" && at == "" {
			items, truncated, err = h.resAllAssets(ctx, project)
		} else {
			items, truncated, err = h.res.SearchAssets(ctx, project, q, at)
		}
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
		var (
			vms                                           []VMInstance
			disks                                         []DiskInfo
			buckets                                       []BucketInfo
			vpcs                                          []VPCInfo
			addrs                                         []AddressInfo
			fws                                           []FirewallInfo
			instErr, diskErr, bucketErr, netErr, addrErr, fwErr error
			wg                                            sync.WaitGroup
		)
		wg.Add(6)
		go func() { defer wg.Done(); vms, instErr = resListCached(h, ctx, project, "instances", h.res.ListInstances) }()
		go func() { defer wg.Done(); disks, diskErr = resListCached(h, ctx, project, "disks", h.res.ListDisks) }()
		go func() { defer wg.Done(); buckets, bucketErr = resListCached(h, ctx, project, "buckets", h.res.ListBuckets) }()
		go func() { defer wg.Done(); vpcs, netErr = resListCached(h, ctx, project, "networks", h.res.ListNetworks) }()
		go func() { defer wg.Done(); addrs, addrErr = resListCached(h, ctx, project, "addresses", h.res.ListAddresses) }()
		go func() { defer wg.Done(); fws, fwErr = resListCached(h, ctx, project, "firewalls", h.res.ListFirewalls) }()
		wg.Wait()

		var computeErr error
		for _, err := range []error{instErr, diskErr, netErr, addrErr, fwErr} {
			if err != nil {
				computeErr = err
				break
			}
		}
		if computeErr != nil && bucketErr != nil {
			return nil, computeErr
		}
		partial := map[string]string{}
		if computeErr != nil {
			partial["compute"] = computeErr.Error()
		}
		if bucketErr != nil {
			partial["storage"] = bucketErr.Error()
		}
		var partialOut map[string]string
		if len(partial) > 0 {
			partialOut = partial
		}
		return &ResInsightsData{
			FetchedAt:     resNow(),
			Findings:      buildFindings(vms, disks, buckets, vpcs, addrs, fws),
			PartialErrors: partialOut,
		}, nil
	})
}
