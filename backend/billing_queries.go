package main

// BQClient query methods for the GCP Billing section. SQL strings
// come from builders in opendata_billing.go; table identifiers are only ever
// the validated project/dataset plus names read from INFORMATION_SCHEMA.

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

// billingMaxBytesBilled caps every billing query at 10 GiB to prevent runaway
// full-history scans.
const billingMaxBytesBilled int64 = 10 << 30

func (b *BQClient) newBillingQuery(sql string) *bigquery.Query {
	q := b.client.Query(sql)
	q.MaxBytesBilled = billingMaxBytesBilled
	return q
}

// ListBillingTables lists table names in the dataset via the free BigQuery
// tables.list Metadata API (avoiding a 10 MB minimum INFORMATION_SCHEMA scan).
// project and dataset MUST already be validated by parseBillingDataset.
func (b *BQClient) ListBillingTables(ctx context.Context, project, dataset string) ([]string, error) {
	it := b.client.DatasetInProject(project, dataset).Tables(ctx)
	var names []string
	for {
		tbl, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list tables in %s.%s: %w", project, dataset, err)
		}
		names = append(names, tbl.TableID)
	}
	slices.Sort(names)
	return names, nil
}

type billingCurrencyRow struct {
	Currency string `bigquery:"currency"`
}

// GetBillingCurrency reads the account currency from recent partitions of a
// standard export table using a deterministic date parameter so BigQuery can
// cache the result.
func (b *BQClient) GetBillingCurrency(ctx context.Context, project, dataset, table string) (string, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT currency FROM `+"`%s.%s.%s`"+`
		WHERE _PARTITIONTIME >= TIMESTAMP(@since)
		  AND currency IS NOT NULL
		LIMIT 1`, project, dataset, table))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "since", Value: billingToday().AddDays(-7)},
	}
	rows, err := collectRows[billingCurrencyRow](q, ctx)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0].Currency, nil
}

type BillingProjectOption struct {
	ID   string `json:"id" bigquery:"id"`
	Name string `json:"name" bigquery:"name"`
}

type billingStringRow struct {
	V string `bigquery:"v"`
}

// billingMetaFilter widens a filter to a fixed 13-month window with no
// optional filters: enough for a year of invoice months in the dropdowns
// while keeping the scan bounded.
func billingMetaFilter(f BillingFilter) BillingFilter {
	f.End = f.End.AddDays(1)
	f.Start = civil.Date{Year: f.End.Year - 1, Month: f.End.Month, Day: 1}
	f.InvoiceMonth = ""
	f.Accounts, f.Projects, f.Services = nil, nil, nil
	f.LabelKey, f.LabelValue = "", ""
	return f
}

type billingMetaDimRow struct {
	ProjectID    string `bigquery:"pid"`
	ProjectName  string `bigquery:"pname"`
	Service      string `bigquery:"svc"`
	InvoiceMonth string `bigquery:"im"`
}

// GetBillingMetaDimensions fetches distinct projects, services, and invoice
// months in a single partition-pruned scan instead of 3 separate 13-month scans.
func (b *BQClient) GetBillingMetaDimensions(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingProjectOption, []string, []string, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT
			IFNULL(project.id, '') AS pid,
			IFNULL(ANY_VALUE(project.name), '') AS pname,
			IFNULL(service.description, '') AS svc,
			IFNULL(invoice.month, '') AS im
		FROM %s
		GROUP BY pid, svc, im`, src))
	q.Parameters = params
	rows, err := collectRows[billingMetaDimRow](q, ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	projMap := map[string]string{}
	svcSet := map[string]struct{}{}
	imSet := map[string]struct{}{}
	for _, r := range rows {
		if r.ProjectID != "" {
			if projMap[r.ProjectID] == "" || r.ProjectName != "" {
				projMap[r.ProjectID] = r.ProjectName
			}
		}
		if r.Service != "" {
			svcSet[r.Service] = struct{}{}
		}
		if r.InvoiceMonth != "" {
			imSet[r.InvoiceMonth] = struct{}{}
		}
	}
	projects := make([]BillingProjectOption, 0, len(projMap))
	for id, name := range projMap {
		projects = append(projects, BillingProjectOption{ID: id, Name: name})
	}
	slices.SortFunc(projects, func(a, b BillingProjectOption) int {
		return cmp.Compare(a.ID, b.ID)
	})
	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	slices.Sort(services)
	invoiceMonths := make([]string, 0, len(imSet))
	for m := range imSet {
		invoiceMonths = append(invoiceMonths, m)
	}
	slices.SortFunc(invoiceMonths, func(a, b string) int {
		return cmp.Compare(b, a) // DESC
	})
	return projects, services, invoiceMonths, nil
}

func (b *BQClient) GetBillingProjects(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingProjectOption, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT project.id AS id, ANY_VALUE(project.name) AS name
		FROM %s WHERE project.id IS NOT NULL
		GROUP BY id ORDER BY id`, src))
	q.Parameters = params
	return collectRows[BillingProjectOption](q, ctx)
}

func (b *BQClient) GetBillingServices(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT DISTINCT service.description AS v FROM %s
		WHERE service.description IS NOT NULL ORDER BY v`, src))
	q.Parameters = params
	return billingStrings(q, ctx)
}

func (b *BQClient) GetBillingLabelKeys(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT l.key AS v
		FROM %s t, UNNEST(t.labels) l
		WHERE l.key IS NOT NULL AND l.key != ''
		GROUP BY v
		ORDER BY COUNT(*) DESC, v ASC
		LIMIT 500`, src))
	q.Parameters = params
	return billingStrings(q, ctx)
}

func (b *BQClient) GetBillingInvoiceMonths(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT DISTINCT invoice.month AS v FROM %s ORDER BY v DESC`, src))
	q.Parameters = params
	return billingStrings(q, ctx)
}

func billingStrings(q *bigquery.Query, ctx context.Context) ([]string, error) {
	rows, err := collectRows[billingStringRow](q, ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.V
	}
	return out, nil
}

type BillingKpiRow struct {
	Currency string  `json:"currency" bigquery:"currency"`
	Gross    float64 `json:"gross" bigquery:"gross"`
	Net      float64 `json:"net" bigquery:"net"`
	Credits  float64 `json:"credits" bigquery:"credits"`
	Projects int64   `json:"projects" bigquery:"projects"`
	Services int64   `json:"services" bigquery:"services"`
}

type BillingDailyRow struct {
	Date  string  `json:"date" bigquery:"date"`
	Gross float64 `json:"gross" bigquery:"gross"`
	Net   float64 `json:"net" bigquery:"net"`
}

type BillingGroupRow struct {
	Name    string  `json:"name" bigquery:"name"`
	Gross   float64 `json:"gross" bigquery:"gross"`
	Net     float64 `json:"net" bigquery:"net"`
	Credits float64 `json:"credits" bigquery:"credits"`
}

type billingOverviewRollupRow struct {
	Currency string  `bigquery:"currency"`
	Date     string  `bigquery:"date"`
	Svc      string  `bigquery:"svc"`
	Proj     string  `bigquery:"proj"`
	GCur     int64   `bigquery:"g_cur"`
	GDate    int64   `bigquery:"g_date"`
	GSvc     int64   `bigquery:"g_svc"`
	GProj    int64   `bigquery:"g_proj"`
	Gross    float64 `bigquery:"gross"`
	Net      float64 `bigquery:"net"`
	Credits  float64 `bigquery:"credits"`
	Projects int64   `bigquery:"projects"`
	Services int64   `bigquery:"services"`
}

func billingOverviewRollupSQL(src string) string {
	return fmt.Sprintf(`
		SELECT
			IFNULL(currency, '') AS currency,
			IFNULL(date_val, '') AS date,
			IFNULL(svc_desc, '(none)') AS svc,
			IFNULL(proj_id, '(none)') AS proj,
			g_cur,
			g_date,
			g_svc,
			g_proj,
			gross,
			net,
			credits,
			projects,
			services
		FROM (
			SELECT
				currency,
				date_val,
				ANY_VALUE(svc_desc) AS svc_desc,
				proj_id,
				GROUPING(currency) AS g_cur,
				GROUPING(date_val) AS g_date,
				GROUPING(svc_id) AS g_svc,
				GROUPING(proj_id) AS g_proj,
				%s AS gross,
				%s AS net,
				%s AS credits,
				COUNT(DISTINCT raw_proj_id) AS projects,
				COUNT(DISTINCT raw_svc_id) AS services
			FROM (
				SELECT
					currency,
					FORMAT_TIMESTAMP('%%Y-%%m-%%d', usage_start_time, 'America/Los_Angeles') AS date_val,
					IFNULL(service.id, '(none)') AS svc_id,
					service.id AS raw_svc_id,
					service.description AS svc_desc,
					IFNULL(project.id, '(none)') AS proj_id,
					project.id AS raw_proj_id,
					cost,
					credits
				FROM %s
			)
			GROUP BY GROUPING SETS (
				(currency),
				(date_val),
				(svc_id),
				(proj_id)
			)
		)`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src)
}

// GetBillingOverviewRollup computes KPIs, daily series, service breakdown, and
// project breakdown in a single GROUPING SETS scan instead of 4 separate jobs.
func (b *BQClient) GetBillingOverviewRollup(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingKpiRow, []BillingDailyRow, []BillingGroupRow, []BillingGroupRow, error) {
	q := b.newBillingQuery(billingOverviewRollupSQL(src))
	q.Parameters = params
	rows, err := collectRows[billingOverviewRollupRow](q, ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	kpis := []BillingKpiRow{}
	daily := []BillingDailyRow{}
	svcs := []BillingGroupRow{}
	projs := []BillingGroupRow{}
	for _, r := range rows {
		switch {
		case r.GCur == 0 && r.GDate == 1 && r.GSvc == 1 && r.GProj == 1:
			kpis = append(kpis, BillingKpiRow{
				Currency: r.Currency,
				Gross:    r.Gross,
				Net:      r.Net,
				Credits:  r.Credits,
				Projects: r.Projects,
				Services: r.Services,
			})
		case r.GDate == 0 && r.GCur == 1 && r.GSvc == 1 && r.GProj == 1:
			if r.Date != "" {
				daily = append(daily, BillingDailyRow{
					Date:  r.Date,
					Gross: r.Gross,
					Net:   r.Net,
				})
			}
		case r.GSvc == 0 && r.GCur == 1 && r.GDate == 1 && r.GProj == 1:
			svcs = append(svcs, BillingGroupRow{
				Name:    r.Svc,
				Gross:   r.Gross,
				Net:     r.Net,
				Credits: r.Credits,
			})
		case r.GProj == 0 && r.GCur == 1 && r.GDate == 1 && r.GSvc == 1:
			projs = append(projs, BillingGroupRow{
				Name:    r.Proj,
				Gross:   r.Gross,
				Net:     r.Net,
				Credits: r.Credits,
			})
		}
	}
	slices.SortFunc(kpis, func(a, b BillingKpiRow) int {
		return cmp.Compare(a.Currency, b.Currency)
	})
	slices.SortFunc(daily, func(a, b BillingDailyRow) int {
		return cmp.Compare(a.Date, b.Date)
	})
	sortGroupRowsByNetDesc := func(a, b BillingGroupRow) int {
		if c := cmp.Compare(b.Net, a.Net); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	}
	slices.SortFunc(svcs, sortGroupRowsByNetDesc)
	slices.SortFunc(projs, sortGroupRowsByNetDesc)
	return kpis, daily, svcs, projs, nil
}

func (b *BQClient) GetBillingKpis(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingKpiRow, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT
			currency,
			%s AS gross, %s AS net, %s AS credits,
			COUNT(DISTINCT project.id) AS projects,
			COUNT(DISTINCT service.id) AS services
		FROM %s GROUP BY currency ORDER BY currency`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src))
	q.Parameters = params
	return collectRows[BillingKpiRow](q, ctx)
}

func (b *BQClient) GetBillingDaily(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingDailyRow, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT FORMAT_TIMESTAMP('%%Y-%%m-%%d', usage_start_time, 'America/Los_Angeles') AS date,
			%s AS gross, %s AS net
		FROM %s GROUP BY date ORDER BY date`,
		billingGrossExpr, billingNetExpr, src))
	q.Parameters = params
	return collectRows[BillingDailyRow](q, ctx)
}

func billingGroupsSQL(src, groupExpr string, limit int) string {
	nameExpr := groupExpr
	if groupExpr == billingGroupService {
		nameExpr = "IFNULL(ANY_VALUE(service.description), '(none)')"
	}
	return fmt.Sprintf(`
		WITH all_groups AS (
			SELECT
				%s AS grp_id,
				%s AS name,
				%s AS gross,
				%s AS net,
				%s AS credits
			FROM %s
			GROUP BY grp_id
		),
		ranked AS (
			SELECT
				name, gross, net, credits,
				ROW_NUMBER() OVER (ORDER BY net DESC, grp_id ASC) AS rn
			FROM all_groups
		)
		SELECT name, gross, net, credits FROM ranked WHERE rn <= %d
		UNION ALL
		SELECT
			CONCAT('Other (', CAST(COUNT(*) AS STRING), ' more)') AS name,
			ROUND(SUM(gross), 2) AS gross,
			ROUND(SUM(net), 2) AS net,
			ROUND(SUM(credits), 2) AS credits
		FROM ranked
		WHERE rn > %d
		HAVING COUNT(*) > 0`,
		groupExpr, nameExpr, billingGrossExpr, billingNetExpr, billingCreditsExpr, src, limit, limit)
}

// GetBillingGroups aggregates cost by an expression from the fixed
// billingGroup* whitelist (never caller-supplied strings).
func (b *BQClient) GetBillingGroups(ctx context.Context, src, groupExpr string, limit int, params []bigquery.QueryParameter) ([]BillingGroupRow, error) {
	q := b.newBillingQuery(billingGroupsSQL(src, groupExpr, limit))
	q.Parameters = params
	return collectRows[BillingGroupRow](q, ctx)
}

type BillingSkuRow struct {
	SkuID          string   `json:"sku_id" bigquery:"sku_id"`
	Sku            string   `json:"sku" bigquery:"sku"`
	PricingUnit    string   `json:"pricing_unit" bigquery:"pricing_unit"`
	Usage          float64  `json:"usage" bigquery:"usage"`
	Gross          float64  `json:"gross" bigquery:"gross"`
	Net            float64  `json:"net" bigquery:"net"`
	EffectivePrice *float64 `json:"effective_price" bigquery:"effective_price"`
}

func billingSkusSQL(src string) string {
	return fmt.Sprintf(`
		WITH all_skus AS (
			SELECT
				IFNULL(sku.id, '(none)') AS sku_id,
				IFNULL(ANY_VALUE(sku.description), '(none)') AS sku,
				IFNULL(ANY_VALUE(usage.pricing_unit), '') AS pricing_unit,
				ROUND(SUM(usage.amount_in_pricing_units), 2) AS usage,
				%s AS gross, %s AS net,
				SAFE_DIVIDE(CAST((%s) AS FLOAT64), SUM(usage.amount_in_pricing_units)) AS effective_price
			FROM %s
			WHERE service.description = @sku_service OR service.id = @sku_service
			GROUP BY sku_id
		),
		ranked AS (
			SELECT
				sku_id, sku, pricing_unit, usage, gross, net, effective_price,
				ROW_NUMBER() OVER (ORDER BY net DESC, sku_id ASC) AS rn
			FROM all_skus
		)
		SELECT sku_id, sku, pricing_unit, usage, gross, net, effective_price FROM ranked WHERE rn <= 100
		UNION ALL
		SELECT
			'(other)' AS sku_id,
			CONCAT('Other (', CAST(COUNT(*) AS STRING), ' SKUs)') AS sku,
			'' AS pricing_unit,
			0.0 AS usage,
			ROUND(SUM(gross), 2) AS gross,
			ROUND(SUM(net), 2) AS net,
			CAST(NULL AS FLOAT64) AS effective_price
		FROM ranked
		WHERE rn > 100
		HAVING COUNT(*) > 0`,
		billingGrossExpr, billingNetExpr, billingNetSumExpr, src)
}

// GetBillingSkus breaks one service down by SKU. The service value arrives
// as a query parameter (@sku_service), never interpolated. Effective price
// divides the unrounded NUMERIC net before converting to FLOAT64 so low-cost
// SKUs are not distorted by cent-level rounding.
func (b *BQClient) GetBillingSkus(ctx context.Context, src, service string, params []bigquery.QueryParameter) ([]BillingSkuRow, error) {
	q := b.newBillingQuery(billingSkusSQL(src))
	q.Parameters = append(append([]bigquery.QueryParameter{}, params...),
		bigquery.QueryParameter{Name: "sku_service", Value: service})
	return collectRows[BillingSkuRow](q, ctx)
}

type BillingProjectRow struct {
	ID      string  `json:"id" bigquery:"id"`
	Name    string  `json:"name" bigquery:"name"`
	Gross   float64 `json:"gross" bigquery:"gross"`
	Net     float64 `json:"net" bigquery:"net"`
	Credits float64 `json:"credits" bigquery:"credits"`
}

func billingProjectRowsSQL(src string) string {
	return fmt.Sprintf(`
		WITH all_projects AS (
			SELECT
				IFNULL(project.id, '(none)') AS id,
				IFNULL(ANY_VALUE(project.name), '') AS name,
				%s AS gross, %s AS net, %s AS credits
			FROM %s
			GROUP BY id
		),
		ranked AS (
			SELECT
				id, name, gross, net, credits,
				ROW_NUMBER() OVER (ORDER BY net DESC, id ASC) AS rn
			FROM all_projects
		)
		SELECT id, name, gross, net, credits FROM ranked WHERE rn <= 100
		UNION ALL
		SELECT
			'(other)' AS id,
			CONCAT('Other (', CAST(COUNT(*) AS STRING), ' projects)') AS name,
			ROUND(SUM(gross), 2) AS gross,
			ROUND(SUM(net), 2) AS net,
			ROUND(SUM(credits), 2) AS credits
		FROM ranked
		WHERE rn > 100
		HAVING COUNT(*) > 0`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src)
}

func (b *BQClient) GetBillingProjectRows(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingProjectRow, error) {
	q := b.newBillingQuery(billingProjectRowsSQL(src))
	q.Parameters = params
	return collectRows[BillingProjectRow](q, ctx)
}

func (b *BQClient) GetBillingLabelGroups(ctx context.Context, src, labelKey string, params []bigquery.QueryParameter) ([]BillingGroupRow, error) {
	q := b.newBillingQuery(billingLabelGroupSQL(src))
	q.Parameters = append(append([]bigquery.QueryParameter{}, params...),
		bigquery.QueryParameter{Name: "group_label_key", Value: labelKey})
	return collectRows[BillingGroupRow](q, ctx)
}

type BillingCreditRow struct {
	Type   string  `json:"type" bigquery:"type"`
	Name   string  `json:"name" bigquery:"name"`
	Amount float64 `json:"amount" bigquery:"amount"`
}

// GetBillingCreditRows slices credits by type/name. This query reads ONLY
// credit amounts (no cost column), so expanding the credits array with a
// comma join is correct here — the double-counting hazard only exists when
// cost and credits are summed in the same row set.
func (b *BQClient) GetBillingCreditRows(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingCreditRow, error) {
	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT
			IFNULL(c.type, '(none)') AS type,
			IFNULL(c.name, '') AS name,
			ROUND(CAST(SUM(CAST(c.amount AS NUMERIC)) AS FLOAT64), 2) AS amount
		FROM %s t, UNNEST(t.credits) c
		GROUP BY type, name ORDER BY amount ASC LIMIT 100`, src))
	q.Parameters = params
	return collectRows[BillingCreditRow](q, ctx)
}

func billingCreditsByServiceSQL(src string) string {
	return fmt.Sprintf(`
		WITH all_svc AS (
			SELECT
				%s AS svc_id,
				IFNULL(ANY_VALUE(service.description), '(none)') AS name,
				%s AS gross, %s AS net, %s AS credits
			FROM %s
			GROUP BY svc_id
			HAVING credits != 0
		),
		ranked AS (
			SELECT
				name, gross, net, credits,
				ROW_NUMBER() OVER (ORDER BY credits ASC, svc_id ASC) AS rn
			FROM all_svc
		)
		SELECT name, gross, net, credits FROM ranked WHERE rn <= 50
		UNION ALL
		SELECT
			CONCAT('Other (', CAST(COUNT(*) AS STRING), ' more)') AS name,
			ROUND(SUM(gross), 2) AS gross,
			ROUND(SUM(net), 2) AS net,
			ROUND(SUM(credits), 2) AS credits
		FROM ranked
		WHERE rn > 50
		HAVING COUNT(*) > 0`,
		billingGroupService, billingGrossExpr, billingNetExpr, billingCreditsExpr, src)
}

// GetBillingCreditsByService ranks services that received credits by credit
// magnitude (credits are negative, so ORDER BY credits ASC puts the largest
// credit recipients first even when their net spend is near zero).
func (b *BQClient) GetBillingCreditsByService(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingGroupRow, error) {
	q := b.newBillingQuery(billingCreditsByServiceSQL(src))
	q.Parameters = params
	return collectRows[BillingGroupRow](q, ctx)
}

type BillingResourceRow struct {
	// ID is the grouping key: GlobalName, or Name when the export row has
	// no global name. Unique within one response.
	ID         string  `json:"id" bigquery:"id"`
	Name       string  `json:"name" bigquery:"name"`
	GlobalName string  `json:"global_name" bigquery:"global_name"`
	Service    string  `json:"service" bigquery:"service"`
	Project    string  `json:"project" bigquery:"project"`
	Net        float64 `json:"net" bigquery:"net"`
}

// billingResourcesResult is the single row billingResourcesSQL returns.
type billingResourcesResult struct {
	Resources       []BillingResourceRow `bigquery:"resources"`
	UnattributedNet float64              `bigquery:"unattributed_net"`
	TotalNet        float64              `bigquery:"total_net"`
}

// GetBillingResources ranks resources in the detailed export by net cost
// (see billingResourcesSQL). search ("" = none) matches resource name/global
// name, passed as a parameter.
func (b *BQClient) GetBillingResources(ctx context.Context, src, search string, params []bigquery.QueryParameter) (billingResourcesResult, error) {
	if search != "" {
		params = append(append([]bigquery.QueryParameter{}, params...),
			bigquery.QueryParameter{Name: "resource_q", Value: search})
	}
	q := b.newBillingQuery(billingResourcesSQL(src, search != ""))
	q.Parameters = params
	rows, err := collectRows[billingResourcesResult](q, ctx)
	if err != nil || len(rows) == 0 {
		return billingResourcesResult{}, err
	}
	return rows[0], nil
}

type BillingPriceRow struct {
	SkuID         string   `json:"sku_id" bigquery:"sku_id"`
	Sku           string   `json:"sku" bigquery:"sku"`
	Service       string   `json:"service" bigquery:"service"`
	PricingUnit   string   `json:"pricing_unit" bigquery:"pricing_unit"`
	Currency      string   `json:"currency" bigquery:"currency"`
	ListPrice     float64  `json:"list_price" bigquery:"list_price"`
	ContractPrice *float64 `json:"contract_price" bigquery:"contract_price"`
	DiscountPct   *float64 `json:"discount_pct" bigquery:"discount_pct"`
	Tiers         int64    `json:"tiers" bigquery:"tiers"`
}

type billingPricingLatestPtRow struct {
	LatestPT time.Time `bigquery:"latest_pt"`
}

// GetBillingPricing reads the latest pricing snapshot: first discovers the
// newest _PARTITIONTIME via a 0-byte metadata query, then queries only that
// single partition (reducing scan bytes by ~7.3x). Filters by billing account
// when provided so multi-account datasets do not mix contract prices across
// accounts, and prefers the first non-zero tiered rate so free-tier SKUs do
// not display 0 as their headline price.
func (b *BQClient) GetBillingPricing(ctx context.Context, project, dataset, account string, services []string, search string) ([]BillingPriceRow, string, string, error) {
	table := fmt.Sprintf("`%s.%s.%s`", project, dataset, billingPricingTable)

	ptQ := b.newBillingQuery(fmt.Sprintf(`
		SELECT MAX(_PARTITIONTIME) AS latest_pt
		FROM %s
		WHERE _PARTITIONTIME >= TIMESTAMP(@min_pt)`, table))
	ptQ.Parameters = []bigquery.QueryParameter{
		{Name: "min_pt", Value: billingToday().AddDays(-8)},
	}
	ptRows, err := collectRows[billingPricingLatestPtRow](ptQ, ctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("pricing latest partition: %w", err)
	}
	if len(ptRows) == 0 || ptRows[0].LatestPT.IsZero() {
		return []BillingPriceRow{}, "", "", nil
	}
	latestPT := ptRows[0].LatestPT.UTC()
	asOf := latestPT.Format("2006-01-02")

	where := "_PARTITIONTIME = @latest_pt AND DATE(pricing_as_of_time) = DATE(@latest_pt)"
	params := []bigquery.QueryParameter{
		{Name: "latest_pt", Value: latestPT},
	}
	if account != "" {
		where += " AND billing_account_id = @account"
		params = append(params, bigquery.QueryParameter{Name: "account", Value: account})
	}
	if len(services) > 0 {
		where += " AND service.description IN UNNEST(@services)"
		params = append(params, bigquery.QueryParameter{Name: "services", Value: services})
	}
	if search != "" {
		where += " AND STRPOS(LOWER(sku.description), LOWER(@price_q)) > 0"
		params = append(params, bigquery.QueryParameter{Name: "price_q", Value: search})
	}

	q := b.newBillingQuery(fmt.Sprintf(`
		SELECT
			sku.id AS sku_id,
			ANY_VALUE(sku.description) AS sku,
			ANY_VALUE(service.description) AS service,
			ANY_VALUE(pricing_unit_description) AS pricing_unit,
			IFNULL(ANY_VALUE(account_currency_code), '') AS currency,
			ROUND(CAST(ANY_VALUE(COALESCE(
				(SELECT tr.account_currency_amount FROM UNNEST(list_price.tiered_rates) tr WHERE tr.account_currency_amount > 0 ORDER BY tr.start_usage_amount ASC LIMIT 1),
				(SELECT tr.account_currency_amount FROM UNNEST(list_price.tiered_rates) tr ORDER BY tr.start_usage_amount ASC LIMIT 1)
			)) AS FLOAT64), 6) AS list_price,
			ROUND(CAST(ANY_VALUE(COALESCE(
				(SELECT tr.account_currency_amount FROM UNNEST(billing_account_price.tiered_rates) tr WHERE tr.account_currency_amount > 0 ORDER BY tr.start_usage_amount ASC LIMIT 1),
				(SELECT tr.account_currency_amount FROM UNNEST(billing_account_price.tiered_rates) tr ORDER BY tr.start_usage_amount ASC LIMIT 1)
			)) AS FLOAT64), 6) AS contract_price,
			CAST(ANY_VALUE(billing_account_price.price_info.discount_percent) AS FLOAT64) AS discount_pct,
			ANY_VALUE(ARRAY_LENGTH(list_price.tiered_rates)) AS tiers
		FROM %s
		WHERE %s
		GROUP BY sku_id
		ORDER BY service ASC, sku ASC
		LIMIT 200`, table, where))
	q.Parameters = params
	rows, err := collectRows[BillingPriceRow](q, ctx)
	if err != nil {
		return nil, "", "", err
	}
	var currency string
	for _, r := range rows {
		if r.Currency != "" {
			currency = r.Currency
			break
		}
	}
	return rows, asOf, currency, nil
}
