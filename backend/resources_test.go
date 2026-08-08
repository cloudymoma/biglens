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
		{"Bad-Caps", false},        // uppercase not allowed
		{"1starts-with-digit", false},
		{"ab", false},              // too short (min 6 chars total)
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
		{"custom-4-8192", "custom", 4, 8},
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
