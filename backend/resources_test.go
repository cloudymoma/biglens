package main

import "testing"

func TestValidResourceProject(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"my-project-123", true},
		{"abcdef", true},
		{"a12345", true},
		{"Bad-Caps", false}, // uppercase not allowed
		{"1starts-with-digit", false},
		{"ab", false}, // too short (min 6 chars total)
		{"has_underscore", false},
		{"ends-with-hyphen-", false},
		{"", false},
		{"proj; DROP TABLE", false},
	}
	for _, c := range cases {
		if got := validResourceProject(c.in); got != c.ok {
			t.Errorf("validResourceProject(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}

func TestParseMachineType(t *testing.T) {
	cases := []struct {
		in     string
		family string
		vcpus  int
		memGB  float64
	}{
		{"e2-standard-4", "e2", 4, 16},
		{"n2-highmem-8", "n2", 8, 64},
		{"n2-highcpu-16", "n2", 16, 16},
		{"n1-standard-1", "n1", 1, 3.75},
		{"e2-medium", "e2", 2, 4},
		{"e2-small", "e2", 2, 2},
		{"e2-micro", "e2", 2, 1},
		{"f1-micro", "f1", 1, 0.6},
		{"g1-small", "g1", 1, 1.7},
		{"c3-standard-22", "c3", 22, 88},
		{"c3-highcpu-4", "c3", 4, 8},
		{"c4-standard-8", "c4", 8, 30},
		{"c4-highmem-8", "c4", 8, 62},
		{"m1-megamem-96", "m1", 96, 1433.6},
		{"m2-ultramem-208", "m2", 208, 5888},
		{"m3-ultramem-32", "m3", 32, 976},
		{"custom-4-8192", "custom", 4, 8},
		{"e2-custom-4-8192", "e2", 4, 8},
		{"n2-custom-8-65536-ext", "n2", 8, 64},
		{"e2-custom-small-2048", "e2", 2, 2},
		{"weird-shape", "weird", 0, 0},
	}
	for _, c := range cases {
		fam, v, m := parseMachineType(c.in)
		if fam != c.family || v != c.vcpus || m != c.memGB {
			t.Errorf("parseMachineType(%q) = (%q,%d,%v), want (%q,%d,%v)",
				c.in, fam, v, m, c.family, c.vcpus, c.memGB)
		}
	}
}

func TestClassifyWorkload(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"gke-prod-pool-1-abc", map[string]string{"goog-gke-node": ""}, "GKE"},
		{"cluster-m", map[string]string{"goog-dataproc-cluster-name": "cluster"}, "Dataproc"},
		{"airflow-worker", map[string]string{"goog-composer-environment": "env1"}, "Composer"},
		{"gke-fallback-node", nil, "GKE"}, // name-prefix fallback
		{"plain-vm", map[string]string{"env": "prod"}, "GCE"},
		{"plain-vm-2", nil, "GCE"},
	}
	for _, c := range cases {
		if got := classifyWorkload(c.name, c.labels); got != c.want {
			t.Errorf("classifyWorkload(%q,%v) = %q, want %q", c.name, c.labels, got, c.want)
		}
	}
}

func TestBuildFindings(t *testing.T) {
	vms := []VMInstance{
		{Name: "vm-run", Zone: "us-central1-a", Status: "RUNNING"},
		{Name: "vm-stop", Zone: "us-central1-a", Status: "TERMINATED"},
		{Name: "vm-susp", Zone: "us-central1-a", Status: "SUSPENDED"},
	}
	disks := []DiskInfo{
		{Name: "d-used", Zone: "us-central1-a", Users: []string{"vm-run"}},
		{Name: "d-orphan", Zone: "us-central1-a", SizeGB: 500},
	}
	buckets := []BucketInfo{
		{Name: "b-uniform", Location: "US", UniformAccess: true},
		{Name: "b-acl", Location: "US", UniformAccess: false},
	}
	vpcs := []VPCInfo{{Name: "default", AutoCreate: true}, {Name: "prod-vpc"}}
	addrs := []AddressInfo{
		{Name: "ip-used", Region: "us-central1", Status: "IN_USE"},
		{Name: "ip-idle", Region: "us-central1", Status: "RESERVED"},
		{Name: "ip-internal-idle", Region: "us-central1", Type: "INTERNAL", Status: "RESERVED"},
		{Name: "ip-psa-range", Region: "global", Purpose: "VPC_PEERING", Status: "RESERVED"},
	}
	fws := []FirewallInfo{
		{Name: "allow-ssh-world", Direction: "INGRESS", SourceRanges: []string{"0.0.0.0/0"}, Allowed: []string{"tcp:22"}},
		{Name: "allow-range-world", Direction: "INGRESS", SourceRanges: []string{"0.0.0.0/0"}, Allowed: []string{"tcp:0-65535"}},
		{Name: "allow-ipv6-ssh", Direction: "INGRESS", SourceRanges: []string{"::/0"}, Allowed: []string{"tcp:22"}},
		{Name: "allow-http-world", Direction: "INGRESS", SourceRanges: []string{"0.0.0.0/0"}, Allowed: []string{"tcp:80"}},
		{Name: "deny-all-world", Direction: "INGRESS", Action: "DENY", SourceRanges: []string{"0.0.0.0/0"}, Denied: []string{"all"}},
		{Name: "allow-all-disabled", Direction: "INGRESS", SourceRanges: []string{"0.0.0.0/0"}, Allowed: []string{"all"}, Disabled: true},
		{Name: "internal-only", Direction: "INGRESS", SourceRanges: []string{"10.0.0.0/8"}, Allowed: []string{"tcp:22"}},
		{Name: "egress-open", Direction: "EGRESS", SourceRanges: []string{"0.0.0.0/0"}, Allowed: []string{"all"}},
	}

	got := buildFindings(vms, disks, buckets, vpcs, addrs, fws)

	byResource := map[string]Finding{}
	for _, f := range got {
		byResource[f.Resource] = f
	}
	want := []struct {
		resource, severity, category string
	}{
		{"allow-ssh-world", "high", "open_firewall"},
		{"allow-range-world", "high", "open_firewall"},
		{"allow-ipv6-ssh", "high", "open_firewall"},
		{"allow-http-world", "medium", "open_firewall"},
		{"d-orphan", "medium", "unattached_disk"},
		{"ip-idle", "medium", "unused_address"},
		{"vm-stop", "low", "stopped_vm"},
		{"vm-susp", "low", "stopped_vm"},
		{"b-acl", "low", "non_uniform_bucket"},
		{"default", "low", "default_network"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d: %+v", len(got), len(want), got)
	}
	for _, w := range want {
		f, ok := byResource[w.resource]
		if !ok {
			t.Errorf("missing finding for %s", w.resource)
			continue
		}
		if f.Severity != w.severity || f.Category != w.category {
			t.Errorf("%s: got (%s,%s), want (%s,%s)", w.resource, f.Severity, f.Category, w.severity, w.category)
		}
		if f.Summary == "" {
			t.Errorf("%s: empty summary", w.resource)
		}
	}
	// Ordered high first, low last.
	if got[0].Severity != "high" || got[len(got)-1].Severity != "low" {
		t.Errorf("findings not ordered by severity: %+v", got)
	}
	// Disabled, DENY, egress, internal firewall rules, internal/PSA IPs must NOT be flagged.
	for _, bad := range []string{"deny-all-world", "allow-all-disabled", "egress-open", "internal-only", "d-used", "ip-used", "ip-internal-idle", "ip-psa-range", "vm-run", "b-uniform", "prod-vpc"} {
		if _, ok := byResource[bad]; ok {
			t.Errorf("%s should not be flagged", bad)
		}
	}
}

func TestAssetSummaries(t *testing.T) {
	items := []AssetItem{
		{AssetType: "compute.googleapis.com/Instance", Location: "us-central1-a", Created: "2026-08-01T00:00:00Z"},
		{AssetType: "compute.googleapis.com/Disk", Location: "us-central1-a", Created: "2026-08-03T00:00:00Z"},
		{AssetType: "storage.googleapis.com/Bucket", Location: "us", Created: "2026-07-01T00:00:00Z"},
	}
	if got := serviceOfAssetType("compute.googleapis.com/Instance"); got != "compute" {
		t.Errorf("serviceOfAssetType = %q, want compute", got)
	}
	if got := serviceOfAssetType("weird"); got != "weird" {
		t.Errorf("serviceOfAssetType fallback = %q, want weird", got)
	}
	svc := countAssetsByService(items)
	if len(svc) != 2 || svc[0].Name != "compute" || svc[0].Count != 2 || svc[1].Name != "storage" {
		t.Errorf("countAssetsByService = %+v", svc)
	}
	loc := countAssetsByLocation(items)
	if len(loc) != 2 || loc[0].Name != "us-central1-a" || loc[0].Count != 2 {
		t.Errorf("countAssetsByLocation = %+v", loc)
	}
	rec := recentAssets(items, 2)
	if len(rec) != 2 || rec[0].Created != "2026-08-03T00:00:00Z" {
		t.Errorf("recentAssets = %+v", rec)
	}
	if got := recentAssets(items, 10); len(got) != 3 {
		t.Errorf("recentAssets cap = %d items, want 3", len(got))
	}
}
