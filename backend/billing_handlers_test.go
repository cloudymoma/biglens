package main

import (
	"net/http/httptest"
	"testing"
)

func billingTestConfig() *Config {
	cfg := &Config{}
	cfg.OpenData.GCPBilling.Datasets = []string{"my-project.billing_ds"}
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
