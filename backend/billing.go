package main

// Pure domain logic for the GCP Billing section: dataset
// validation, export-table classification, filter/SQL builders, rollups.
// Everything here is unit-testable without BigQuery.

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

var billingPacificLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PST", -8*3600)
	}
	return loc
}()

func billingToday() civil.Date {
	return civil.DateOf(time.Now().In(billingPacificLoc))
}

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
// billingNetSumExpr is the unrounded NUMERIC net, for queries that add group
// nets up again before rounding.
const (
	billingGrossExpr   = "ROUND(CAST(SUM(CAST(cost AS NUMERIC)) AS FLOAT64), 2)"
	billingCreditsExpr = "ROUND(CAST(SUM(IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c), 0)) AS FLOAT64), 2)"
	billingNetSumExpr  = "SUM(CAST(cost AS NUMERIC)) + SUM(IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c), 0))"
	billingNetExpr     = "ROUND(CAST(" + billingNetSumExpr + " AS FLOAT64), 2)"
)

// Group-by expressions for GetBillingGroups. Only these constants are ever
// passed; nothing caller-supplied reaches the SQL string.
const (
	billingGroupService = "IFNULL(service.id, '(none)')"
	billingGroupProject = "IFNULL(project.id, '(none)')"
)

// billingWhere builds the WHERE clause + parameters for one export table.
// Export tables are ingestion-time partitioned (UTC export date >= usage date),
// while Cloud Billing reports and invoices align day boundaries to
// America/Los_Angeles.
func billingWhere(f BillingFilter) (string, []bigquery.QueryParameter) {
	var conds []string
	var params []bigquery.QueryParameter

	if f.InvoiceMonth != "" {
		conds = append(conds,
			"invoice.month = @invoice_month",
			"_PARTITIONTIME >= TIMESTAMP(PARSE_DATE('%Y%m', @invoice_month))",
			"_PARTITIONTIME < TIMESTAMP(DATE_ADD(LAST_DAY(PARSE_DATE('%Y%m', @invoice_month)), INTERVAL 14 DAY))",
		)
		params = append(params, bigquery.QueryParameter{Name: "invoice_month", Value: f.InvoiceMonth})
	} else {
		conds = append(conds,
			"_PARTITIONTIME >= TIMESTAMP(@start)",
			"_PARTITIONTIME < TIMESTAMP(DATE_ADD(@end, INTERVAL 3 DAY))",
			"usage_start_time >= TIMESTAMP(@start, 'America/Los_Angeles')",
			"usage_start_time < TIMESTAMP(@end, 'America/Los_Angeles')",
			"cost_type = 'regular'",
		)
		params = append(params,
			bigquery.QueryParameter{Name: "start", Value: f.Start},
			bigquery.QueryParameter{Name: "end", Value: f.End},
		)
	}

	// Note: account selection is already enforced via table selection
	// (standardTables / resourceTables). Filtering billing_account_id in SQL
	// is omitted because reseller parent export tables store the subaccount ID
	// in billing_account_id and would drop subaccount usage rows.
	if len(f.Projects) > 0 {
		conds = append(conds, "project.id IN UNNEST(@projects)")
		params = append(params, bigquery.QueryParameter{Name: "projects", Value: f.Projects})
	}
	if len(f.Services) > 0 {
		conds = append(conds, "(service.description IN UNNEST(@services) OR service.id IN UNNEST(@services))")
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
// partition pruning still works), plus the query parameters.
func billingSource(project, dataset string, tables []string, f BillingFilter) (string, []bigquery.QueryParameter) {
	where, params := billingWhere(f)
	parts := make([]string, len(tables))
	for i, tbl := range tables {
		parts[i] = fmt.Sprintf("SELECT * FROM `%s.%s.%s` WHERE %s", project, dataset, tbl, where)
	}
	return "(" + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + ")", params
}

// billingMetaPartitionSource builds a partition-only UNION ALL source over the
// given tables for metadata discovery queries.
func billingMetaPartitionSource(project, dataset string, tables []string, start, end civil.Date) (string, []bigquery.QueryParameter) {
	where := "_PARTITIONTIME >= TIMESTAMP(@meta_start) AND _PARTITIONTIME < TIMESTAMP(@meta_end)"
	params := []bigquery.QueryParameter{
		{Name: "meta_start", Value: start},
		{Name: "meta_end", Value: end},
	}
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

// billingResourcesSQL ranks resources in the detailed export by net cost,
// as a single row: the top 50 resources plus two window totals.
//
// A resource is keyed by resource.global_name — the export's unique
// identifier — falling back to resource.name, which is user-chosen and not
// unique. Both columns hold empty strings rather than NULL on many rows, so
// an empty value counts as missing. Rows with neither are not listed, but
// their net comes back as unattributed_net, next to the window's whole net
// (total_net), so the UI can say how much the list leaves out. When search
// is set, @resource_q matches a resource if it matches any of its rows; it
// narrows the list, never the two totals.
func billingResourcesSQL(src string, search bool) string {
	matched := "TRUE"
	if search {
		matched = `LOGICAL_OR(STRPOS(LOWER(IFNULL(resource.name, '')), LOWER(@resource_q)) > 0
					OR STRPOS(LOWER(IFNULL(resource.global_name, '')), LOWER(@resource_q)) > 0)`
	}
	return fmt.Sprintf(`
		WITH by_resource AS (
			SELECT
				COALESCE(NULLIF(resource.global_name, ''), NULLIF(resource.name, '')) AS rid,
				IFNULL(ANY_VALUE(NULLIF(resource.name, '')), '') AS name,
				IFNULL(ANY_VALUE(NULLIF(resource.global_name, '')), '') AS global_name,
				IFNULL(ANY_VALUE(service.description), '') AS service,
				IFNULL(ANY_VALUE(project.id), '') AS project,
				%s AS net,
				%s AS matched
			FROM %s
			GROUP BY rid
		)
		SELECT
			ARRAY_AGG(IF(rid IS NOT NULL AND matched,
					STRUCT(rid AS id, name, global_name, service, project, ROUND(CAST(net AS FLOAT64), 2) AS net),
					NULL)
				IGNORE NULLS ORDER BY net DESC, rid LIMIT 50) AS resources,
			ROUND(CAST(IFNULL(SUM(IF(rid IS NULL, net, 0)), 0) AS FLOAT64), 2) AS unattributed_net,
			ROUND(CAST(IFNULL(SUM(net), 0) AS FLOAT64), 2) AS total_net
		FROM by_resource`,
		billingNetSumExpr, matched, src)
}

// billingWindowCoversMTD reports whether the filter window covers the entire
// month-to-date range [monthStart, today) in usage mode. Projections are only
// valid when all elapsed days of the current month are included in the window.
func billingWindowCoversMTD(f BillingFilter, today civil.Date) bool {
	if f.InvoiceMonth != "" {
		return false
	}
	monthStart := civil.Date{Year: today.Year, Month: today.Month, Day: 1}
	return !f.Start.After(monthStart) && !f.End.Before(today)
}

// rollupBillingProjection estimates end-of-month net spend: month-to-date
// net + average net of the last (up to) 7 complete calendar days in the current
// month × remaining days. Returns nil when the daily series has no complete
// days in the current month (projection would be meaningless).
func rollupBillingProjection(daily []BillingDailyRow, today civil.Date) *float64 {
	if today.Day <= 1 {
		return nil
	}
	monthStart := civil.Date{Year: today.Year, Month: today.Month, Day: 1}
	var mtd float64
	byDate := map[civil.Date]float64{}
	for _, d := range daily {
		dt, err := civil.ParseDate(d.Date)
		if err != nil || dt.Year != today.Year || dt.Month != today.Month {
			continue
		}
		mtd += d.Net
		if dt.Before(today) { // complete days only
			byDate[dt] += d.Net
		}
	}
	if len(byDate) == 0 {
		return nil
	}
	runStart := today.AddDays(-7)
	if runStart.Before(monthStart) {
		runStart = monthStart
	}
	completeDays := today.DaysSince(runStart)
	if completeDays <= 0 {
		return nil
	}
	var sum float64
	hasRecent := false
	for dt := runStart; dt.Before(today); dt = dt.AddDays(1) {
		if v, ok := byDate[dt]; ok {
			sum += v
			hasRecent = true
		}
	}
	var rate float64
	if hasRecent {
		rate = sum / float64(completeDays)
	} else {
		for _, v := range byDate {
			sum += v
		}
		rate = sum / float64(today.DaysSince(monthStart))
	}
	// civil.Date does not normalize day 0, so lean on time.Date for the
	// last day of the current month.
	daysInMonth := time.Date(today.Year, today.Month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	remaining := daysInMonth - today.Day + 1 // today itself is incomplete
	p := mtd + rate*float64(remaining)
	p = math.Round(p*100) / 100
	return &p
}

// rollupBillingGroupRows returns up to limit rows, rolling any excess rows into
// a trailing otherLabel row so table totals still reconcile with window KPIs.
func rollupBillingGroupRows(rows []BillingGroupRow, limit int, otherLabel string) []BillingGroupRow {
	if len(rows) <= limit {
		return slices.Clone(rows)
	}
	var otherGross, otherNet, otherCredits float64
	for _, r := range rows[limit:] {
		otherGross += r.Gross
		otherNet += r.Net
		otherCredits += r.Credits
	}
	return append(slices.Clone(rows[:limit]), BillingGroupRow{
		Name:    otherLabel,
		Gross:   math.Round(otherGross*100) / 100,
		Net:     math.Round(otherNet*100) / 100,
		Credits: math.Round(otherCredits*100) / 100,
	})
}

