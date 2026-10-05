package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func billingTestConfig() *Config {
	cfg := &Config{}
	cfg.GCPBilling.Datasets = []string{"my-project.billing_ds"}
	return cfg
}

func TestParseBillingFilter(t *testing.T) {
	cfg := billingTestConfig()
	tests := []struct {
		name    string
		query   string
		wantErr bool
		check   func(t *testing.T, f BillingFilter)
	}{
		{"dataset required", "", true, nil},
		{"unconfigured dataset rejected", "dataset=other.ds", true, nil},
		{"defaults to last 30 days", "dataset=my-project.billing_ds", false, func(t *testing.T, f BillingFilter) {
			if f.End.DaysSince(f.Start) != 30 {
				t.Errorf("window = %d days, want 30", f.End.DaysSince(f.Start))
			}
		}},
		{"explicit range", "dataset=my-project.billing_ds&start=2026-01-01&end=2026-03-01", false, func(t *testing.T, f BillingFilter) {
			if f.Start.String() != "2026-01-01" || f.End.String() != "2026-03-01" {
				t.Errorf("range = %s..%s", f.Start, f.End)
			}
		}},
		{"start after end", "dataset=my-project.billing_ds&start=2026-03-01&end=2026-01-01", true, nil},
		{"bad date", "dataset=my-project.billing_ds&start=notadate", true, nil},
		{"bad invoice month", "dataset=my-project.billing_ds&invoice_month=2026-07", true, nil},
		{"invoice month ok", "dataset=my-project.billing_ds&invoice_month=202607", false, func(t *testing.T, f BillingFilter) {
			if f.InvoiceMonth != "202607" {
				t.Errorf("invoice month = %q", f.InvoiceMonth)
			}
		}},
		{"csv filters", "dataset=my-project.billing_ds&projects=p1,p2&services=BigQuery&accounts=A-1-1", false, func(t *testing.T, f BillingFilter) {
			if len(f.Projects) != 2 || len(f.Services) != 1 || len(f.Accounts) != 1 {
				t.Errorf("filters = %+v", f)
			}
		}},
		{"label pair", "dataset=my-project.billing_ds&label=env:prod", false, func(t *testing.T, f BillingFilter) {
			if f.LabelKey != "env" || f.LabelValue != "prod" {
				t.Errorf("label = %q:%q", f.LabelKey, f.LabelValue)
			}
		}},
		{"label without colon", "dataset=my-project.billing_ds&label=env", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/gcp_billing/overview?"+tt.query, nil)
			f, err := parseBillingFilter(r, cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, f)
			}
		})
	}
}

func TestBillingResourcesFromResult(t *testing.T) {
	const bucketID = "//storage.googleapis.com/projects/_/buckets/dingopdf"
	bucket := BillingResourceRow{ID: bucketID, Name: "dingopdf", GlobalName: bucketID,
		Service: "Cloud Storage", Project: "p1", Net: 1.25}
	got := billingResourcesFromResult(billingResourcesResult{
		Resources: []BillingResourceRow{bucket}, UnattributedNet: 384.1, TotalNet: 437.81,
	})
	if !got.Available || len(got.Resources) != 1 || got.Resources[0] != bucket {
		t.Errorf("resources = %+v, want available with the one bucket row", got)
	}
	if got.UnattributedNet != 384.1 || got.TotalNet != 437.81 {
		t.Errorf("unattributed/total = %v/%v, want 384.1/437.81", got.UnattributedNet, got.TotalNet)
	}
	// The frontend keys rows by id (the grouping key).
	if b, _ := json.Marshal(bucket); !strings.Contains(string(b), `"id":"`+bucketID+`"`) {
		t.Errorf("row JSON %s missing id", b)
	}

	// No listed resources (a search without hits, or only unattributed rows)
	// still reports the unattributed share, and lists [] rather than null.
	b, err := json.Marshal(billingResourcesFromResult(billingResourcesResult{UnattributedNet: 12.5, TotalNet: 12.5}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"available":true`, `"resources":[]`, `"unattributed_net":12.5`, `"total_net":12.5`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s missing %s", b, want)
		}
	}
}
