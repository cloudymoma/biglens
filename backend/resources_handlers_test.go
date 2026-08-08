package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeResourceAPI implements ResourceAPI; each field overrides one method.
// Unset methods return empty results.
type fakeResourceAPI struct {
	searchErr error
	assets    []AssetItem
	instances []VMInstance
	disks     []DiskInfo
	buckets   []BucketInfo
	bytes     map[string]map[string]float64
	networks  []VPCInfo
	subnets   []SubnetInfo
	addrs     []AddressInfo
	firewalls []FirewallInfo
	fwdRules  []ForwardingRuleInfo
	listErr   error
}

func (f *fakeResourceAPI) SearchAssets(ctx context.Context, project, query, assetType string) ([]AssetItem, bool, error) {
	return f.assets, false, f.searchErr
}
func (f *fakeResourceAPI) ListInstances(ctx context.Context, project string) ([]VMInstance, error) {
	return f.instances, f.listErr
}
func (f *fakeResourceAPI) ListDisks(ctx context.Context, project string) ([]DiskInfo, error) {
	return f.disks, f.listErr
}
func (f *fakeResourceAPI) ListBuckets(ctx context.Context, project string) ([]BucketInfo, error) {
	return f.buckets, f.listErr
}
func (f *fakeResourceAPI) BucketBytes(ctx context.Context, project string) (map[string]map[string]float64, error) {
	return f.bytes, nil
}
func (f *fakeResourceAPI) ListNetworks(ctx context.Context, project string) ([]VPCInfo, error) {
	return f.networks, f.listErr
}
func (f *fakeResourceAPI) ListSubnets(ctx context.Context, project string) ([]SubnetInfo, error) {
	return f.subnets, f.listErr
}
func (f *fakeResourceAPI) ListAddresses(ctx context.Context, project string) ([]AddressInfo, error) {
	return f.addrs, f.listErr
}
func (f *fakeResourceAPI) ListFirewalls(ctx context.Context, project string) ([]FirewallInfo, error) {
	return f.firewalls, f.listErr
}
func (f *fakeResourceAPI) ListForwardingRules(ctx context.Context, project string) ([]ForwardingRuleInfo, error) {
	return f.fwdRules, f.listErr
}

// resourcesTestHandler builds an APIHandler with a temp-file config (so
// SaveConfig works) and a fake ResourceAPI.
func resourcesTestHandler(t *testing.T, projects []string, fake ResourceAPI) *APIHandler {
	t.Helper()
	cfg := &Config{path: filepath.Join(t.TempDir(), "conf.yaml")}
	cfg.GCPResources.Projects = projects
	return &APIHandler{
		bq:    &BQClient{config: cfg},
		cache: NewCache(time.Minute),
		res:   fake,
	}
}

func TestResourceProjectValidation(t *testing.T) {
	h := resourcesTestHandler(t, []string{"proj-alpha"}, &fakeResourceAPI{})
	cases := []struct {
		q       string
		wantErr string
	}{
		{"", "project is required"},
		{"?project=not-configured-x", "not configured"},
		{"?project=proj-alpha", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/gcp_resources/overview"+c.q, nil)
		_, err := h.resourceProject(r)
		if c.wantErr == "" && err != nil {
			t.Errorf("%q: unexpected error %v", c.q, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%q: err = %v, want containing %q", c.q, err, c.wantErr)
		}
	}
}

func TestResourcesConfigGet(t *testing.T) {
	h := resourcesTestHandler(t, []string{"proj-alpha"}, &fakeResourceAPI{})
	w := httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("GET", "/api/gcp_resources/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "proj-alpha") {
		t.Errorf("body missing project: %s", w.Body.String())
	}
}

func TestResourcesConfigGetProbeError(t *testing.T) {
	h := resourcesTestHandler(t, []string{"proj-alpha"},
		&fakeResourceAPI{searchErr: fmt.Errorf("PermissionDenied")})
	w := httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("GET", "/api/gcp_resources/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "roles/cloudasset.viewer") {
		t.Errorf("probe error not translated to actionable message: %s", w.Body.String())
	}
}

func TestResourcesConfigPost(t *testing.T) {
	h := resourcesTestHandler(t, []string{"proj-alpha"}, &fakeResourceAPI{})

	// add: invalid project id rejected, nothing persisted
	w := httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("POST", "/api/gcp_resources/config",
		strings.NewReader(`{"action":"add","project":"Bad_ID"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid id: status %d, want 400", w.Code)
	}

	// add: duplicate rejected
	w = httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("POST", "/api/gcp_resources/config",
		strings.NewReader(`{"action":"add","project":"proj-alpha"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("duplicate: status %d, want 400", w.Code)
	}

	// add: valid project accepted and persisted
	w = httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("POST", "/api/gcp_resources/config",
		strings.NewReader(`{"action":"add","project":"proj-beta"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("add: status %d: %s", w.Code, w.Body.String())
	}
	if len(h.bq.config.GCPResources.Projects) != 2 {
		t.Errorf("projects = %v, want 2 entries", h.bq.config.GCPResources.Projects)
	}

	// add: unreachable project rejected, not persisted
	h2 := resourcesTestHandler(t, nil, &fakeResourceAPI{searchErr: fmt.Errorf("PermissionDenied")})
	w = httptest.NewRecorder()
	h2.ResourcesConfig(w, httptest.NewRequest("POST", "/api/gcp_resources/config",
		strings.NewReader(`{"action":"add","project":"proj-gamma"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("unreachable add: status %d, want 400", w.Code)
	}
	if len(h2.bq.config.GCPResources.Projects) != 0 {
		t.Errorf("unreachable project was persisted: %v", h2.bq.config.GCPResources.Projects)
	}

	// remove
	w = httptest.NewRecorder()
	h.ResourcesConfig(w, httptest.NewRequest("POST", "/api/gcp_resources/config",
		strings.NewReader(`{"action":"remove","project":"proj-alpha"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("remove: status %d: %s", w.Code, w.Body.String())
	}
	if len(h.bq.config.GCPResources.Projects) != 1 || h.bq.config.GCPResources.Projects[0] != "proj-beta" {
		t.Errorf("after remove: %v", h.bq.config.GCPResources.Projects)
	}
}
