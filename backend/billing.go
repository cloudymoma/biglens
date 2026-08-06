package main

// Pure domain logic for the GCP Billing section: dataset
// validation, export-table classification, filter/SQL builders, rollups.
// Everything here is unit-testable without BigQuery.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

// billingProjectRe: GCP project ID, optionally domain-scoped
// ("example.com:project"). Kept strict because the value is interpolated
// into SQL table references.
var billingProjectRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// billingDatasetRe: BigQuery dataset IDs are word characters only (length
// capped separately; Go regexp repeat counts max out at 1000).
var billingDatasetRe = regexp.MustCompile(`^\w+$`)

const billingDatasetMaxLen = 1024

// parseBillingDataset splits "project.dataset" on the LAST dot (project IDs
// may contain dots when domain-scoped) and validates both halves.
func parseBillingDataset(s string) (string, string, error) {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return "", "", fmt.Errorf("expected project.dataset, got %q", s)
	}
	project, dataset := s[:i], s[i+1:]
	if !billingProjectRe.MatchString(project) {
		return "", "", fmt.Errorf("invalid project id %q", project)
	}
	if len(dataset) > billingDatasetMaxLen || !billingDatasetRe.MatchString(dataset) {
		return "", "", fmt.Errorf("invalid dataset id %q", dataset)
	}
	return project, dataset, nil
}

const (
	billingStandardPrefix = "gcp_billing_export_v1_"
	billingResourcePrefix = "gcp_billing_export_resource_v1_"
	billingPricingTable   = "cloud_pricing_export"
)

// billingAccountSuffixRe matches the table-name suffix form of a billing
// account ID, e.g. "010B7A_A27129_D37860".
var billingAccountSuffixRe = regexp.MustCompile(`^[0-9A-F]{6}_[0-9A-F]{6}_[0-9A-F]{6}$`)

// billingAccountFromTable extracts the billing account ID
// ("010B7A-A27129-D37860") from a standard or resource export table name.
func billingAccountFromTable(name string) (string, bool) {
	var suffix string
	switch {
	case strings.HasPrefix(name, billingResourcePrefix):
		suffix = strings.TrimPrefix(name, billingResourcePrefix)
	case strings.HasPrefix(name, billingStandardPrefix):
		suffix = strings.TrimPrefix(name, billingStandardPrefix)
	default:
		return "", false
	}
	if !billingAccountSuffixRe.MatchString(suffix) {
		return "", false
	}
	return strings.ReplaceAll(suffix, "_", "-"), true
}

// BillingTableInfo describes which export tables a configured dataset holds.
// Standard/Resource map billing account ID -> table name.
type BillingTableInfo struct {
	Standard   map[string]string
	Resource   map[string]string
	HasPricing bool
	Currency   string
}

func classifyBillingTables(names []string) BillingTableInfo {
	info := BillingTableInfo{
		Standard: map[string]string{},
		Resource: map[string]string{},
	}
	for _, n := range names {
		if n == billingPricingTable {
			info.HasPricing = true
			continue
		}
		account, ok := billingAccountFromTable(n)
		if !ok {
			continue
		}
		if strings.HasPrefix(n, billingResourcePrefix) {
			info.Resource[account] = n
		} else {
			info.Standard[account] = n
		}
	}
	return info
}

// BillingFilter carries the validated query filters shared by every billing
// endpoint. Start/End form a half-open [Start, End) day window.
type BillingFilter struct {
	DatasetFQN string
	Project    string
	Dataset    string
	Start      civil.Date
	End        civil.Date
	// InvoiceMonth (YYYYMM) switches queries from usage-date mode
	// (regular costs by usage_start_time) to invoice-reconciliation mode
	// (all cost types by invoice.month).
	InvoiceMonth string
	Accounts     []string
	Projects     []string
	Services     []string
	LabelKey     string
	LabelValue   string
}

// Money expressions. NUMERIC aggregation avoids FLOAT64 drift; credits use a
// nested UNNEST subquery — a LEFT JOIN would duplicate cost rows.
const (
	billingGrossExpr   = "ROUND(CAST(SUM(CAST(cost AS NUMERIC)) AS FLOAT64), 2)"
	billingCreditsExpr = "ROUND(CAST(SUM(IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c), 0)) AS FLOAT64), 2)"
	billingNetExpr     = "ROUND(CAST(SUM(CAST(cost AS NUMERIC)) + SUM(IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c), 0)) AS FLOAT64), 2)"
)

// Group-by expressions for GetBillingGroups. Only these constants are ever
// passed; nothing caller-supplied reaches the SQL string.
const (
	billingGroupService = "service.description"
	billingGroupProject = "IFNULL(project.id, '(none)')"
)

// billingWhere builds the WHERE clause + parameters for one export table.
// Export tables are ingestion-time partitioned; _PARTITIONTIME is padded
// ±2 days (35 days after month end in invoice mode) for late-arriving rows.
func billingWhere(f BillingFilter) (string, []bigquery.QueryParameter) {
	var conds []string
	var params []bigquery.QueryParameter

	if f.InvoiceMonth != "" {
		conds = append(conds,
			"invoice.month = @invoice_month",
			"_PARTITIONTIME >= TIMESTAMP(DATE_SUB(PARSE_DATE('%Y%m', @invoice_month), INTERVAL 2 DAY))",
			"_PARTITIONTIME < TIMESTAMP(DATE_ADD(LAST_DAY(PARSE_DATE('%Y%m', @invoice_month)), INTERVAL 35 DAY))",
		)
		params = append(params, bigquery.QueryParameter{Name: "invoice_month", Value: f.InvoiceMonth})
	} else {
		conds = append(conds,
			"_PARTITIONTIME >= TIMESTAMP(DATE_SUB(@start, INTERVAL 2 DAY))",
			"_PARTITIONTIME < TIMESTAMP(DATE_ADD(@end, INTERVAL 2 DAY))",
			"usage_start_time >= TIMESTAMP(@start)",
			"usage_start_time < TIMESTAMP(@end)",
			"cost_type = 'regular'",
		)
		params = append(params,
			bigquery.QueryParameter{Name: "start", Value: f.Start},
			bigquery.QueryParameter{Name: "end", Value: f.End},
		)
	}

	if len(f.Accounts) > 0 {
		conds = append(conds, "billing_account_id IN UNNEST(@accounts)")
		params = append(params, bigquery.QueryParameter{Name: "accounts", Value: f.Accounts})
	}
	if len(f.Projects) > 0 {
		conds = append(conds, "project.id IN UNNEST(@projects)")
		params = append(params, bigquery.QueryParameter{Name: "projects", Value: f.Projects})
	}
	if len(f.Services) > 0 {
		conds = append(conds, "service.description IN UNNEST(@services)")
		params = append(params, bigquery.QueryParameter{Name: "services", Value: f.Services})
	}
	if f.LabelKey != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM UNNEST(labels) fl WHERE fl.key = @label_key AND fl.value = @label_value)")
		params = append(params,
			bigquery.QueryParameter{Name: "label_key", Value: f.LabelKey},
			bigquery.QueryParameter{Name: "label_value", Value: f.LabelValue},
		)
	}
	return strings.Join(conds, "\n\t\t  AND "), params
}

// billingSource returns a parenthesized FROM-clause subquery unioning the
// given export tables with all filters applied inside each branch (so
// partition pruning still works), plus the query parameters. Table names
// must come from INFORMATION_SCHEMA detection.
func billingSource(project, dataset string, tables []string, f BillingFilter) (string, []bigquery.QueryParameter) {
	where, params := billingWhere(f)
	parts := make([]string, len(tables))
	for i, tbl := range tables {
		parts[i] = fmt.Sprintf("SELECT * FROM `%s.%s.%s` WHERE %s", project, dataset, tbl, where)
	}
	return "(" + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + ")", params
}

func (f BillingFilter) cacheKey(endpoint string) string {
	return fmt.Sprintf("billing:%s:%s:%s:%s:%s:%s:%s:%s:%s:%s",
		endpoint, f.DatasetFQN, f.Start, f.End, f.InvoiceMonth,
		strings.Join(f.Accounts, ","), strings.Join(f.Projects, ","),
		strings.Join(f.Services, ","), f.LabelKey, f.LabelValue)
}

// standardTables returns the standard export tables for the selected
// accounts (all accounts when the filter is empty), sorted for stable SQL.
func (f BillingFilter) standardTables(info BillingTableInfo) []string {
	return selectBillingTables(info.Standard, f.Accounts)
}

func (f BillingFilter) resourceTables(info BillingTableInfo) []string {
	return selectBillingTables(info.Resource, f.Accounts)
}

func selectBillingTables(byAccount map[string]string, accounts []string) []string {
	var out []string
	for acct, tbl := range byAccount {
		if len(accounts) == 0 || slices.Contains(accounts, acct) {
			out = append(out, tbl)
		}
	}
	slices.Sort(out)
	return out
}

// billingLabelGroupSQL groups cost by the values of ONE label key.
// A single-key LEFT JOIN keeps unlabeled rows visible and avoids the
// double-counting that multi-key label joins cause.
func billingLabelGroupSQL(src string) string {
	return fmt.Sprintf(`
		SELECT
			IFNULL(l.value, '(unlabeled)') AS name,
			%s AS gross, %s AS net, %s AS credits
		FROM %s t
		LEFT JOIN UNNEST(t.labels) l ON l.key = @group_label_key
		GROUP BY name ORDER BY net DESC LIMIT 50`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src)
}

// rollupBillingProjection estimates end-of-month net spend: month-to-date
// net + average net of the last (up to) 7 complete days in the current
// month × remaining days. Returns nil when the daily series has no rows in
// the current month (projection would be meaningless).
func rollupBillingProjection(daily []BillingDailyRow, today civil.Date) *float64 {
	monthPrefix := fmt.Sprintf("%04d-%02d-", today.Year, int(today.Month))
	var mtd float64
	var complete []float64
	for _, d := range daily {
		if !strings.HasPrefix(d.Date, monthPrefix) {
			continue
		}
		mtd += d.Net
		if d.Date < today.String() { // complete days only
			complete = append(complete, d.Net)
		}
	}
	if len(complete) == 0 {
		return nil
	}
	if len(complete) > 7 {
		complete = complete[len(complete)-7:]
	}
	var sum float64
	for _, v := range complete {
		sum += v
	}
	rate := sum / float64(len(complete))
	// civil.Date does not normalize day 0, so lean on time.Date for the
	// last day of the current month.
	daysInMonth := time.Date(today.Year, today.Month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	remaining := daysInMonth - today.Day + 1 // today itself is incomplete
	p := mtd + rate*float64(remaining)
	p = float64(int64(p*100)) / 100
	return &p
}
