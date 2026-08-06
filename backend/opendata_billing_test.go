package main

import "testing"

func TestParseBillingDataset(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantProject string
		wantDataset string
		wantErr     bool
	}{
		{"simple", "my-project.billing_ds", "my-project", "billing_ds", false},
		{"domain scoped", "example.com:my-project.billing_ds", "example.com:my-project", "billing_ds", false},
		{"missing dot", "my-project", "", "", true},
		{"empty dataset", "my-project.", "", "", true},
		{"empty project", ".billing_ds", "", "", true},
		{"backtick injection", "my-project.bad`ds", "", "", true},
		{"sql injection", "p.d; DROP TABLE x", "", "", true},
		{"dash in dataset", "my-project.bad-ds", "", "", true},
		{"uppercase project", "My-Project.ds", "", "", true},
		{"whitespace", "my-project. ds", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, d, err := parseBillingDataset(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if p != tt.wantProject || d != tt.wantDataset {
				t.Errorf("got (%q, %q), want (%q, %q)", p, d, tt.wantProject, tt.wantDataset)
			}
		})
	}
}

func TestBillingAccountFromTable(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"standard export", "gcp_billing_export_v1_010B7A_A27129_D37860", "010B7A-A27129-D37860", true},
		{"resource export", "gcp_billing_export_resource_v1_010B7A_A27129_D37860", "010B7A-A27129-D37860", true},
		{"pricing table", "cloud_pricing_export", "", false},
		{"unrelated table", "my_other_table", "", false},
		{"bad suffix shape", "gcp_billing_export_v1_ABC", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := billingAccountFromTable(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("billingAccountFromTable(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestClassifyBillingTables(t *testing.T) {
	names := []string{
		"cloud_pricing_export",
		"gcp_billing_export_v1_010B7A_A27129_D37860",
		"gcp_billing_export_resource_v1_010B7A_A27129_D37860",
		"gcp_billing_export_v1_FFFFFF_111111_222222",
		"some_view",
	}
	got := classifyBillingTables(names)
	if !got.HasPricing {
		t.Error("HasPricing = false, want true")
	}
	if len(got.Standard) != 2 {
		t.Fatalf("Standard = %v, want 2 entries", got.Standard)
	}
	if got.Standard["010B7A-A27129-D37860"] != "gcp_billing_export_v1_010B7A_A27129_D37860" {
		t.Errorf("Standard[010B7A-A27129-D37860] = %q", got.Standard["010B7A-A27129-D37860"])
	}
	if got.Resource["010B7A-A27129-D37860"] != "gcp_billing_export_resource_v1_010B7A_A27129_D37860" {
		t.Errorf("Resource map wrong: %v", got.Resource)
	}
	if _, ok := got.Resource["FFFFFF-111111-222222"]; ok {
		t.Error("account without resource table must not appear in Resource map")
	}
}
