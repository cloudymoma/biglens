package main

// Pure domain logic for the GCP Resources section: project validation,
// machine-type parsing, workload classification, asset summarization and
// insight rules. Everything here is unit-testable without GCP.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// resourceProjectRe: plain GCP project ID (6-30 chars, lowercase letters,
// digits, hyphens; starts with a letter, doesn't end with a hyphen).
var resourceProjectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func validResourceProject(p string) bool {
	return resourceProjectRe.MatchString(p)
}

type VMInstance struct {
	Name        string            `json:"name"`
	Zone        string            `json:"zone"`
	MachineType string            `json:"machine_type"` // short name, e.g. "e2-standard-4"
	Status      string            `json:"status"`       // RUNNING, TERMINATED, ...
	Workload    string            `json:"workload"`     // "GKE" | "Dataproc" | "Composer" | "GCE"
	VCPUs       int               `json:"vcpus"`        // 0 when unparseable
	MemoryGB    float64           `json:"memory_gb"`    // 0 when unknown
	Labels      map[string]string `json:"labels,omitempty"`
	Created     string            `json:"created"`
}
type DiskInfo struct {
	Name    string   `json:"name"`
	Zone    string   `json:"zone"`
	Type    string   `json:"type"` // short name, e.g. "pd-ssd"
	SizeGB  int64    `json:"size_gb"`
	Users   []string `json:"users,omitempty"` // attached instance names
	Created string   `json:"created"`
}
type BucketInfo struct {
	Name                   string             `json:"name"`
	Location               string             `json:"location"`
	StorageClass           string             `json:"storage_class"`
	UniformAccess          bool               `json:"uniform_access"`
	PublicAccessPrevention string             `json:"public_access_prevention"`
	Created                string             `json:"created"`
	BytesByClass           map[string]float64 `json:"bytes_by_class,omitempty"`
}
type VPCInfo struct {
	Name       string `json:"name"`
	AutoCreate bool   `json:"auto_create"`
}
type SubnetInfo struct {
	Name                string `json:"name"`
	Region              string `json:"region"`
	Network             string `json:"network"` // short VPC name
	CIDR                string `json:"cidr"`
	PrivateGoogleAccess bool   `json:"private_google_access"`
}
type AddressInfo struct {
	Name    string   `json:"name"`
	Region  string   `json:"region"` // "global" for global addresses
	Address string   `json:"address"`
	Type    string   `json:"type"`   // EXTERNAL | INTERNAL
	Status  string   `json:"status"` // IN_USE | RESERVED
	Users   []string `json:"users,omitempty"`
}
type FirewallInfo struct {
	Name         string   `json:"name"`
	Network      string   `json:"network"`
	Direction    string   `json:"direction"`
	Priority     int      `json:"priority"`
	SourceRanges []string `json:"source_ranges,omitempty"`
	Allowed      []string `json:"allowed,omitempty"` // "tcp:22,80", "all"
	TargetTags   []string `json:"target_tags,omitempty"`
	Disabled     bool     `json:"disabled"`
}
type ForwardingRuleInfo struct {
	Name      string `json:"name"`
	Region    string `json:"region"`
	IPAddress string `json:"ip_address"`
	Scheme    string `json:"scheme"` // EXTERNAL, INTERNAL, EXTERNAL_MANAGED, ...
	Target    string `json:"target"` // short target name
	Ports     string `json:"ports"`
}
type AssetItem struct {
	Name        string            `json:"name"` // full resource name
	AssetType   string            `json:"asset_type"`
	DisplayName string            `json:"display_name"`
	Location    string            `json:"location"`
	State       string            `json:"state"`
	Labels      map[string]string `json:"labels,omitempty"`
	Created     string            `json:"created"`
	Updated     string            `json:"updated"`
}

// Per-vCPU memory (GB) by machine-type variant. Shared machine types and
// custom shapes are handled explicitly in parseMachineType.
var machineMemPerVCPU = map[string]float64{
	"standard": 4, "highmem": 8, "highcpu": 1, "megamem": 14, "ultramem": 24,
}

// fixedMachineShapes: shared-core and legacy types that don't follow the
// family-variant-N pattern.
var fixedMachineShapes = map[string]struct {
	vcpus int
	memGB float64
}{
	"e2-micro": {2, 1}, "e2-small": {2, 2}, "e2-medium": {2, 4},
	"f1-micro": {1, 0.6}, "g1-small": {1, 1.7},
}

// parseMachineType derives family/vCPU/memory from a machine-type short name.
// n1 predates the 4GB/vCPU convention (3.75GB). Unknown shapes return zeros —
// the UI shows the raw machine type string regardless.
func parseMachineType(mt string) (string, int, float64) {
	if s, ok := fixedMachineShapes[mt]; ok {
		return mt[:strings.Index(mt, "-")], s.vcpus, s.memGB
	}
	parts := strings.Split(mt, "-")
	family := parts[0]
	if family == "custom" && len(parts) == 3 { // custom-VCPUS-MEMMB
		v, err1 := strconv.Atoi(parts[1])
		mb, err2 := strconv.Atoi(parts[2])
		if err1 == nil && err2 == nil {
			return "custom", v, float64(mb) / 1024
		}
		return "custom", 0, 0
	}
	if len(parts) < 3 {
		return family, 0, 0
	}
	v, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return family, 0, 0
	}
	per := machineMemPerVCPU[parts[1]]
	if family == "n1" && parts[1] == "standard" {
		per = 3.75
	}
	if family == "n1" && parts[1] == "highmem" {
		per = 6.5
	}
	if family == "n1" && parts[1] == "highcpu" {
		per = 0.9
	}
	return family, v, float64(v) * per
}

// classifyWorkload badges an instance by the well-known labels Google stamps
// on managed VMs; the gke- name prefix catches nodes whose labels were lost.
func classifyWorkload(name string, labels map[string]string) string {
	if _, ok := labels["goog-gke-node"]; ok {
		return "GKE"
	}
	if _, ok := labels["goog-dataproc-cluster-name"]; ok {
		return "Dataproc"
	}
	if _, ok := labels["goog-composer-environment"]; ok {
		return "Composer"
	}
	if strings.HasPrefix(name, "gke-") {
		return "GKE"
	}
	return "GCE"
}

type Finding struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Resource string `json:"resource"`
	Location string `json:"location"`
	Summary  string `json:"summary"`
}

// sensitiveFirewallPorts: ports whose exposure to 0.0.0.0/0 is high severity.
var sensitiveFirewallPorts = []string{"22", "3389"}

// firewallOpenSeverity returns "" when the rule is not internet-open ingress,
// otherwise the severity. "all" protocols or a sensitive port → high.
func firewallOpenSeverity(f FirewallInfo) string {
	if f.Disabled || f.Direction != "INGRESS" || !slices.Contains(f.SourceRanges, "0.0.0.0/0") {
		return ""
	}
	for _, a := range f.Allowed {
		proto, ports, hasPorts := strings.Cut(a, ":")
		if proto == "all" {
			return "high"
		}
		if !hasPorts { // protocol with no port list = all ports
			return "high"
		}
		for _, p := range strings.Split(ports, ",") {
			if slices.Contains(sensitiveFirewallPorts, strings.TrimSpace(p)) {
				return "high"
			}
		}
	}
	return "medium"
}

// buildFindings applies every v1 insight rule; output ordered high→medium→low.
func buildFindings(vms []VMInstance, disks []DiskInfo, buckets []BucketInfo,
	vpcs []VPCInfo, addrs []AddressInfo, fws []FirewallInfo) []Finding {

	bySev := map[string][]Finding{}
	add := func(sev, cat, res, loc, summary string) {
		bySev[sev] = append(bySev[sev], Finding{sev, cat, res, loc, summary})
	}

	for _, f := range fws {
		if sev := firewallOpenSeverity(f); sev != "" {
			add(sev, "open_firewall", f.Name, "global",
				fmt.Sprintf("ingress from 0.0.0.0/0 allows %s — internet-exposed", strings.Join(f.Allowed, " ")))
		}
	}
	for _, d := range disks {
		if len(d.Users) == 0 {
			add("medium", "unattached_disk", d.Name, d.Zone,
				fmt.Sprintf("unattached %dGB disk still incurs storage cost", d.SizeGB))
		}
	}
	for _, a := range addrs {
		if a.Status == "RESERVED" {
			add("medium", "unused_address", a.Name, a.Region,
				"reserved static IP not in use is billed hourly")
		}
	}
	for _, v := range vms {
		if v.Status == "TERMINATED" {
			add("low", "stopped_vm", v.Name, v.Zone,
				"stopped VM still incurs cost for attached disks and reserved IPs")
		}
	}
	for _, b := range buckets {
		if !b.UniformAccess {
			add("low", "non_uniform_bucket", b.Name, b.Location,
				"bucket uses legacy per-object ACLs; enable uniform bucket-level access")
		}
	}
	for _, v := range vpcs {
		if v.Name == "default" {
			add("low", "default_network", v.Name, "global",
				"auto-created default network present; prefer purpose-built VPCs with least-privilege firewalls")
		}
	}

	out := append(append(bySev["high"], bySev["medium"]...), bySev["low"]...)
	if out == nil {
		out = []Finding{}
	}
	return out
}

type ResNamedCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func serviceOfAssetType(t string) string {
	if s, _, ok := strings.Cut(t, ".googleapis.com/"); ok {
		return s
	}
	return t
}

func countBy(items []AssetItem, key func(AssetItem) string) []ResNamedCount {
	counts := map[string]int{}
	for _, it := range items {
		counts[key(it)]++
	}
	out := make([]ResNamedCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, ResNamedCount{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func countAssetsByService(items []AssetItem) []ResNamedCount {
	return countBy(items, func(a AssetItem) string { return serviceOfAssetType(a.AssetType) })
}

func countAssetsByLocation(items []AssetItem) []ResNamedCount {
	return countBy(items, func(a AssetItem) string { return a.Location })
}

// recentAssets returns the n newest items by Created (RFC3339 sorts lexically).
func recentAssets(items []AssetItem, n int) []AssetItem {
	out := slices.Clone(items)
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ResourceAPI is the network boundary for GCP resource APIs.
type ResourceAPI interface {
	// SearchAssets returns up to resExplorerMax items; bool reports truncation.
	SearchAssets(ctx context.Context, project, query, assetType string) ([]AssetItem, bool, error)
	ListInstances(ctx context.Context, project string) ([]VMInstance, error)
	ListDisks(ctx context.Context, project string) ([]DiskInfo, error)
	ListBuckets(ctx context.Context, project string) ([]BucketInfo, error)
	// BucketBytes: bucket name -> storage class -> bytes (latest daily point).
	BucketBytes(ctx context.Context, project string) (map[string]map[string]float64, error)
	ListNetworks(ctx context.Context, project string) ([]VPCInfo, error)
	ListSubnets(ctx context.Context, project string) ([]SubnetInfo, error)
	ListAddresses(ctx context.Context, project string) ([]AddressInfo, error)
	ListFirewalls(ctx context.Context, project string) ([]FirewallInfo, error)
	ListForwardingRules(ctx context.Context, project string) ([]ForwardingRuleInfo, error)
}

const resExplorerMax = 1000
