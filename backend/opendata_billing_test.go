package main

import (
	"fmt"
	"strings"
	"testing"

	"cloud.google.com/go/civil"
)

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

func testFilter() BillingFilter {
	return BillingFilter{
		DatasetFQN: "my-project.billing_ds",
		Project:    "my-project",
		Dataset:    "billing_ds",
		Start:      civil.Date{Year: 2026, Month: 7, Day: 1},
		End:        civil.Date{Year: 2026, Month: 8, Day: 1},
	}
}

func TestBillingWhereUsageMode(t *testing.T) {
	where, params := billingWhere(testFilter())
	for _, want := range []string{
		"_PARTITIONTIME >=", "_PARTITIONTIME <",
		"usage_start_time >= TIMESTAMP(@start)", "usage_start_time < TIMESTAMP(@end)",
		"cost_type = 'regular'",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("usage-mode WHERE missing %q:\n%s", want, where)
		}
	}
	if strings.Contains(where, "invoice.month") {
		t.Error("usage-mode WHERE must not reference invoice.month")
	}
	if len(params) != 2 {
		t.Errorf("params = %v, want start+end only", params)
	}
}

func TestBillingWhereInvoiceMode(t *testing.T) {
	f := testFilter()
	f.InvoiceMonth = "202607"
	where, _ := billingWhere(f)
	if !strings.Contains(where, "invoice.month = @invoice_month") {
		t.Errorf("invoice-mode WHERE missing invoice.month filter:\n%s", where)
	}
	if strings.Contains(where, "cost_type") {
		t.Error("invoice mode must include ALL cost types (tax/adjustment/rounding_error)")
	}
	if !strings.Contains(where, "_PARTITIONTIME") {
		t.Error("invoice mode still needs partition pruning")
	}
}

func TestBillingWhereOptionalFilters(t *testing.T) {
	f := testFilter()
	f.Accounts = []string{"010B7A-A27129-D37860"}
	f.Projects = []string{"p1", "p2"}
	f.Services = []string{"BigQuery"}
	f.LabelKey, f.LabelValue = "env", "prod"
	where, params := billingWhere(f)
	for _, want := range []string{
		"billing_account_id IN UNNEST(@accounts)",
		"project.id IN UNNEST(@projects)",
		"service.description IN UNNEST(@services)",
		"EXISTS (SELECT 1 FROM UNNEST(labels) fl WHERE fl.key = @label_key AND fl.value = @label_value)",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("WHERE missing %q:\n%s", want, where)
		}
	}
	if len(params) != 7 {
		t.Errorf("got %d params, want 7 (start, end, accounts, projects, services, label_key, label_value)", len(params))
	}
}

func TestBillingSourceUnionsTables(t *testing.T) {
	src, _ := billingSource("my-project", "billing_ds",
		[]string{"gcp_billing_export_v1_A", "gcp_billing_export_v1_B"}, testFilter())
	if !strings.Contains(src, "`my-project.billing_ds.gcp_billing_export_v1_A`") ||
		!strings.Contains(src, "`my-project.billing_ds.gcp_billing_export_v1_B`") {
		t.Errorf("source missing table refs:\n%s", src)
	}
	if strings.Count(src, "UNION ALL") != 1 {
		t.Errorf("want exactly 1 UNION ALL for 2 tables:\n%s", src)
	}
	if !strings.HasPrefix(src, "(") || !strings.HasSuffix(src, ")") {
		t.Errorf("source must be a parenthesized subquery:\n%s", src)
	}
}

// Credits must be summed via nested subquery; LEFT JOIN UNNEST(credits)
// double-counts cost rows (official docs gotcha).
func TestBillingCostExprs(t *testing.T) {
	if !strings.Contains(billingNetExpr, "(SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c)") {
		t.Errorf("net expr must use nested credits subquery: %s", billingNetExpr)
	}
	for _, expr := range []string{billingGrossExpr, billingNetExpr, billingCreditsExpr} {
		if strings.Contains(expr, "LEFT JOIN") {
			t.Errorf("cost exprs must never LEFT JOIN credits: %s", expr)
		}
		if !strings.Contains(expr, "NUMERIC") {
			t.Errorf("cost exprs must aggregate as NUMERIC: %s", expr)
		}
	}
}

func TestBillingFilterCacheKey(t *testing.T) {
	a, b := testFilter(), testFilter()
	b.Services = []string{"BigQuery"}
	if a.cacheKey("overview") == b.cacheKey("overview") {
		t.Error("different filters must produce different cache keys")
	}
	if a.cacheKey("overview") == a.cacheKey("services") {
		t.Error("different endpoints must produce different cache keys")
	}
}

func TestStandardTablesSelection(t *testing.T) {
	info := BillingTableInfo{Standard: map[string]string{
		"A-1-1": "gcp_billing_export_v1_A_1_1",
		"B-2-2": "gcp_billing_export_v1_B_2_2",
	}}
	f := testFilter()
	if got := f.standardTables(info); len(got) != 2 {
		t.Errorf("no account filter: want all tables, got %v", got)
	}
	f.Accounts = []string{"B-2-2"}
	got := f.standardTables(info)
	if len(got) != 1 || got[0] != "gcp_billing_export_v1_B_2_2" {
		t.Errorf("account filter: got %v", got)
	}
}

func TestBillingLabelGroupSQL(t *testing.T) {
	sql := billingLabelGroupSQL("(SELECT 1)")
	if !strings.Contains(sql, "LEFT JOIN UNNEST(t.labels) l ON l.key = @group_label_key") {
		t.Errorf("label grouping must LEFT JOIN a single key:\n%s", sql)
	}
	if !strings.Contains(sql, "IFNULL(l.value, '(unlabeled)')") {
		t.Errorf("unlabeled rows must surface as (unlabeled):\n%s", sql)
	}
	if strings.Contains(sql, "LEFT JOIN UNNEST(credits)") {
		t.Error("credits must stay a nested subquery even in label grouping")
	}
}

func TestRollupBillingProjection(t *testing.T) {
	today := civil.Date{Year: 2026, Month: 8, Day: 6} // Aug: 31 days, 5 complete
	daily := []BillingDailyRow{}
	for d := 1; d <= 5; d++ { // Aug 1-5 complete days, $10/day net
		daily = append(daily, BillingDailyRow{Date: fmt.Sprintf("2026-08-%02d", d), Net: 10})
	}
	got := rollupBillingProjection(daily, today)
	if got == nil {
		t.Fatal("projection = nil, want value")
	}
	// MTD net = 50; run-rate = 10/day over last complete days; 26 days remain
	// (Aug 6-31 incl. today, which has no complete data yet) → 50 + 260 = 310.
	if *got != 310 {
		t.Errorf("projection = %v, want 310", *got)
	}

	// Window that doesn't reach the current month → no projection.
	old := []BillingDailyRow{{Date: "2026-06-01", Net: 10}}
	if p := rollupBillingProjection(old, today); p != nil {
		t.Errorf("projection for stale window = %v, want nil", *p)
	}
	if p := rollupBillingProjection(nil, today); p != nil {
		t.Error("projection for empty daily must be nil")
	}
}
