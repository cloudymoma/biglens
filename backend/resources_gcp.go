package main

// ResClients is the single network boundary for the GCP Resources section.
// It converts SDK types into the plain structs in resources.go; no shaping
// logic lives here beyond field mapping.

import (
	"context"
	"fmt"
	"strings"
	"time"

	asset "cloud.google.com/go/asset/apiv1"
	"cloud.google.com/go/asset/apiv1/assetpb"
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ResClients struct {
	asset      *asset.Client
	instances  *compute.InstancesClient
	disks      *compute.DisksClient
	networks   *compute.NetworksClient
	subnets    *compute.SubnetworksClient
	addresses  *compute.AddressesClient
	gAddresses *compute.GlobalAddressesClient
	firewalls  *compute.FirewallsClient
	fwdRules   *compute.ForwardingRulesClient
	gFwdRules  *compute.GlobalForwardingRulesClient
	storage    *gcs.Client
	metrics    *monitoring.MetricClient
}

func NewResClients(ctx context.Context, cfg *Config) (*ResClients, error) {
	var opts []option.ClientOption
	if cfg.BigQuery.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.BigQuery.CredentialsPath))
	}
	c := &ResClients{}
	var err error
	if c.asset, err = asset.NewClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("asset client: %w", err)
	}
	if c.instances, err = compute.NewInstancesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("instances client: %w", err)
	}
	if c.disks, err = compute.NewDisksRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("disks client: %w", err)
	}
	if c.networks, err = compute.NewNetworksRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("networks client: %w", err)
	}
	if c.subnets, err = compute.NewSubnetworksRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("subnetworks client: %w", err)
	}
	if c.addresses, err = compute.NewAddressesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("addresses client: %w", err)
	}
	if c.gAddresses, err = compute.NewGlobalAddressesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("global addresses client: %w", err)
	}
	if c.firewalls, err = compute.NewFirewallsRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("firewalls client: %w", err)
	}
	if c.fwdRules, err = compute.NewForwardingRulesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("forwarding rules client: %w", err)
	}
	if c.gFwdRules, err = compute.NewGlobalForwardingRulesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("global forwarding rules client: %w", err)
	}
	if c.storage, err = gcs.NewClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	if c.metrics, err = monitoring.NewMetricClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("monitoring client: %w", err)
	}
	return c, nil
}

// lastSegment turns a resource URL into its short name.
func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// scopeName extracts e.g. "us-central1-a" from aggregated-list keys like
// "zones/us-central1-a" or "regions/us-central1".
func scopeName(key string) string { return lastSegment(key) }

func (c *ResClients) SearchAssets(ctx context.Context, project, query, assetType string) ([]AssetItem, bool, error) {
	req := &assetpb.SearchAllResourcesRequest{
		Scope: "projects/" + project,
		Query: query,
	}
	if assetType != "" {
		req.AssetTypes = []string{assetType}
	}
	it := c.asset.SearchAllResources(ctx, req)
	var out []AssetItem
	for {
		r, err := it.Next()
		if err == iterator.Done {
			return out, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("asset search: %w", err)
		}
		item := AssetItem{
			Name:        r.GetName(),
			AssetType:   r.GetAssetType(),
			DisplayName: r.GetDisplayName(),
			Location:    r.GetLocation(),
			State:       r.GetState(),
			Labels:      r.GetLabels(),
		}
		if t := r.GetCreateTime(); t != nil {
			item.Created = t.AsTime().UTC().Format(time.RFC3339)
		}
		if t := r.GetUpdateTime(); t != nil {
			item.Updated = t.AsTime().UTC().Format(time.RFC3339)
		}
		out = append(out, item)
		if len(out) >= resExplorerMax {
			return out, true, nil
		}
	}
}

func (c *ResClients) ListInstances(ctx context.Context, project string) ([]VMInstance, error) {
	it := c.instances.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: project})
	var out []VMInstance
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list instances: %w", err)
		}
		for _, inst := range pair.Value.GetInstances() {
			mt := lastSegment(inst.GetMachineType())
			_, vcpus, mem := parseMachineType(mt)
			out = append(out, VMInstance{
				Name:        inst.GetName(),
				Zone:        scopeName(pair.Key),
				MachineType: mt,
				Status:      inst.GetStatus(),
				Workload:    classifyWorkload(inst.GetName(), inst.GetLabels()),
				VCPUs:       vcpus,
				MemoryGB:    mem,
				Labels:      inst.GetLabels(),
				Created:     inst.GetCreationTimestamp(),
			})
		}
	}
}

func (c *ResClients) ListDisks(ctx context.Context, project string) ([]DiskInfo, error) {
	it := c.disks.AggregatedList(ctx, &computepb.AggregatedListDisksRequest{Project: project})
	var out []DiskInfo
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list disks: %w", err)
		}
		for _, d := range pair.Value.GetDisks() {
			users := make([]string, 0, len(d.GetUsers()))
			for _, u := range d.GetUsers() {
				users = append(users, lastSegment(u))
			}
			out = append(out, DiskInfo{
				Name:    d.GetName(),
				Zone:    scopeName(pair.Key),
				Type:    lastSegment(d.GetType()),
				SizeGB:  d.GetSizeGb(),
				Users:   users,
				Created: d.GetCreationTimestamp(),
			})
		}
	}
}

func (c *ResClients) ListBuckets(ctx context.Context, project string) ([]BucketInfo, error) {
	it := c.storage.Buckets(ctx, project)
	var out []BucketInfo
	for {
		b, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list buckets: %w", err)
		}
		out = append(out, BucketInfo{
			Name:                   b.Name,
			Location:               b.Location,
			StorageClass:           b.StorageClass,
			UniformAccess:          b.UniformBucketLevelAccess.Enabled,
			PublicAccessPrevention: b.PublicAccessPrevention.String(),
			Created:                b.Created.UTC().Format(time.RFC3339),
		})
	}
}

// BucketBytes reads the latest daily point of storage/v2/total_bytes per
// bucket per storage class. The metric is daily and can lag ~24h; a 48h
// window guarantees at least one point for established buckets.
func (c *ResClients) BucketBytes(ctx context.Context, project string) (map[string]map[string]float64, error) {
	now := time.Now().UTC()
	it := c.metrics.ListTimeSeries(ctx, &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + project,
		Filter: `metric.type="storage.googleapis.com/storage/v2/total_bytes"`,
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(now.Add(-48 * time.Hour)),
			EndTime:   timestamppb.New(now),
		},
		View: monitoringpb.ListTimeSeriesRequest_FULL,
	})
	out := map[string]map[string]float64{}
	for {
		ts, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bucket bytes: %w", err)
		}
		bucket := ts.GetResource().GetLabels()["bucket_name"]
		class := ts.GetMetric().GetLabels()["storage_class"]
		points := ts.GetPoints()
		if bucket == "" || len(points) == 0 {
			continue
		}
		if out[bucket] == nil {
			out[bucket] = map[string]float64{}
		}
		// Points are returned newest first.
		out[bucket][class] += points[0].GetValue().GetDoubleValue()
	}
}

func (c *ResClients) ListNetworks(ctx context.Context, project string) ([]VPCInfo, error) {
	it := c.networks.List(ctx, &computepb.ListNetworksRequest{Project: project})
	var out []VPCInfo
	for {
		n, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		out = append(out, VPCInfo{Name: n.GetName(), AutoCreate: n.GetAutoCreateSubnetworks()})
	}
}

func (c *ResClients) ListSubnets(ctx context.Context, project string) ([]SubnetInfo, error) {
	it := c.subnets.AggregatedList(ctx, &computepb.AggregatedListSubnetworksRequest{Project: project})
	var out []SubnetInfo
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list subnets: %w", err)
		}
		for _, s := range pair.Value.GetSubnetworks() {
			out = append(out, SubnetInfo{
				Name:                s.GetName(),
				Region:              scopeName(pair.Key),
				Network:             lastSegment(s.GetNetwork()),
				CIDR:                s.GetIpCidrRange(),
				PrivateGoogleAccess: s.GetPrivateIpGoogleAccess(),
			})
		}
	}
}

func (c *ResClients) ListAddresses(ctx context.Context, project string) ([]AddressInfo, error) {
	var out []AddressInfo
	it := c.addresses.AggregatedList(ctx, &computepb.AggregatedListAddressesRequest{Project: project})
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list addresses: %w", err)
		}
		for _, a := range pair.Value.GetAddresses() {
			out = append(out, addressInfo(a, scopeName(pair.Key)))
		}
	}
	git := c.gAddresses.List(ctx, &computepb.ListGlobalAddressesRequest{Project: project})
	for {
		a, err := git.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list global addresses: %w", err)
		}
		out = append(out, addressInfo(a, "global"))
	}
}

func addressInfo(a *computepb.Address, region string) AddressInfo {
	users := make([]string, 0, len(a.GetUsers()))
	for _, u := range a.GetUsers() {
		users = append(users, lastSegment(u))
	}
	return AddressInfo{
		Name:    a.GetName(),
		Region:  region,
		Address: a.GetAddress(),
		Type:    a.GetAddressType(),
		Status:  a.GetStatus(),
		Users:   users,
	}
}

func (c *ResClients) ListFirewalls(ctx context.Context, project string) ([]FirewallInfo, error) {
	it := c.firewalls.List(ctx, &computepb.ListFirewallsRequest{Project: project})
	var out []FirewallInfo
	for {
		f, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list firewalls: %w", err)
		}
		allowed := make([]string, 0, len(f.GetAllowed()))
		for _, a := range f.GetAllowed() {
			s := a.GetIPProtocol()
			if len(a.GetPorts()) > 0 {
				s += ":" + strings.Join(a.GetPorts(), ",")
			}
			allowed = append(allowed, s)
		}
		out = append(out, FirewallInfo{
			Name:         f.GetName(),
			Network:      lastSegment(f.GetNetwork()),
			Direction:    f.GetDirection(),
			Priority:     int(f.GetPriority()),
			SourceRanges: f.GetSourceRanges(),
			Allowed:      allowed,
			TargetTags:   f.GetTargetTags(),
			Disabled:     f.GetDisabled(),
		})
	}
}

func (c *ResClients) ListForwardingRules(ctx context.Context, project string) ([]ForwardingRuleInfo, error) {
	var out []ForwardingRuleInfo
	it := c.fwdRules.AggregatedList(ctx, &computepb.AggregatedListForwardingRulesRequest{Project: project})
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list forwarding rules: %w", err)
		}
		for _, fr := range pair.Value.GetForwardingRules() {
			out = append(out, forwardingRuleInfo(fr, scopeName(pair.Key)))
		}
	}
	git := c.gFwdRules.List(ctx, &computepb.ListGlobalForwardingRulesRequest{Project: project})
	for {
		fr, err := git.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list global forwarding rules: %w", err)
		}
		out = append(out, forwardingRuleInfo(fr, "global"))
	}
}

func forwardingRuleInfo(fr *computepb.ForwardingRule, region string) ForwardingRuleInfo {
	ports := fr.GetPortRange()
	if ports == "" && len(fr.GetPorts()) > 0 {
		ports = strings.Join(fr.GetPorts(), ",")
	}
	return ForwardingRuleInfo{
		Name:      fr.GetName(),
		Region:    region,
		IPAddress: fr.GetIPAddress(),
		Scheme:    fr.GetLoadBalancingScheme(),
		Target:    lastSegment(fr.GetTarget()),
		Ports:     ports,
	}
}
