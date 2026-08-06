package main

// BQClient query methods for the GCP Billing section. SQL strings
// come from builders in opendata_billing.go; table identifiers are only ever
// the validated project/dataset plus names read from INFORMATION_SCHEMA.

import (
	"context"
	"fmt"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

type billingTableNameRow struct {
	TableName string `bigquery:"table_name"`
}

// ListBillingTables lists table names in the dataset. project and dataset
// MUST already be validated by parseBillingDataset.
func (b *BQClient) ListBillingTables(ctx context.Context, project, dataset string) ([]string, error) {
	q := b.client.Query(fmt.Sprintf(
		"SELECT table_name FROM `%s.%s`.INFORMATION_SCHEMA.TABLES ORDER BY table_name",
		project, dataset))
	rows, err := collectRows[billingTableNameRow](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables in %s.%s: %w", project, dataset, err)
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.TableName
	}
	return names, nil
}

type billingCurrencyRow struct {
	Currency string `bigquery:"currency"`
}

// GetBillingCurrency reads the account currency from recent partitions of a
// standard export table.
func (b *BQClient) GetBillingCurrency(ctx context.Context, project, dataset, table string) (string, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT currency FROM `+"`%s.%s.%s`"+`
		WHERE _PARTITIONTIME >= TIMESTAMP(DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY))
		  AND currency IS NOT NULL
		LIMIT 1`, project, dataset, table))
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

func (b *BQClient) GetBillingProjects(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingProjectOption, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT project.id AS id, ANY_VALUE(project.name) AS name
		FROM %s WHERE project.id IS NOT NULL
		GROUP BY id ORDER BY id`, src))
	q.Parameters = params
	return collectRows[BillingProjectOption](q, ctx)
}

func (b *BQClient) GetBillingServices(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT DISTINCT service.description AS v FROM %s
		WHERE service.description IS NOT NULL ORDER BY v`, src))
	q.Parameters = params
	return billingStrings(q, ctx)
}

func (b *BQClient) GetBillingLabelKeys(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT DISTINCT l.key AS v FROM %s t, UNNEST(t.labels) l ORDER BY v LIMIT 200`, src))
	q.Parameters = params
	return billingStrings(q, ctx)
}

func (b *BQClient) GetBillingInvoiceMonths(ctx context.Context, src string, params []bigquery.QueryParameter) ([]string, error) {
	q := b.client.Query(fmt.Sprintf(`
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

func (b *BQClient) GetBillingKpis(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingKpiRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			currency,
			%s AS gross, %s AS net, %s AS credits,
			COUNT(DISTINCT project.id) AS projects,
			COUNT(DISTINCT service.description) AS services
		FROM %s GROUP BY currency ORDER BY currency`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src))
	q.Parameters = params
	return collectRows[BillingKpiRow](q, ctx)
}

func (b *BQClient) GetBillingDaily(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingDailyRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT FORMAT_TIMESTAMP('%%Y-%%m-%%d', usage_start_time) AS date,
			%s AS gross, %s AS net
		FROM %s GROUP BY date ORDER BY date`,
		billingGrossExpr, billingNetExpr, src))
	q.Parameters = params
	return collectRows[BillingDailyRow](q, ctx)
}

// GetBillingGroups aggregates cost by an expression from the fixed
// billingGroup* whitelist (never caller-supplied strings).
func (b *BQClient) GetBillingGroups(ctx context.Context, src, groupExpr string, limit int, params []bigquery.QueryParameter) ([]BillingGroupRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT %s AS name, %s AS gross, %s AS net, %s AS credits
		FROM %s GROUP BY name ORDER BY net DESC LIMIT %d`,
		groupExpr, billingGrossExpr, billingNetExpr, billingCreditsExpr, src, limit))
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

// GetBillingSkus breaks one service down by SKU. The service value arrives
// as a query parameter (@sku_service), never interpolated.
func (b *BQClient) GetBillingSkus(ctx context.Context, src, service string, params []bigquery.QueryParameter) ([]BillingSkuRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			sku.id AS sku_id,
			ANY_VALUE(sku.description) AS sku,
			IFNULL(ANY_VALUE(usage.pricing_unit), '') AS pricing_unit,
			ROUND(SUM(usage.amount_in_pricing_units), 2) AS usage,
			%s AS gross, %s AS net,
			SAFE_DIVIDE(%s, SUM(usage.amount_in_pricing_units)) AS effective_price
		FROM %s
		WHERE service.description = @sku_service
		GROUP BY sku_id ORDER BY net DESC LIMIT 100`,
		billingGrossExpr, billingNetExpr, billingNetExpr, src))
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

func (b *BQClient) GetBillingProjectRows(ctx context.Context, src string, params []bigquery.QueryParameter) ([]BillingProjectRow, error) {
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			IFNULL(project.id, '(none)') AS id,
			IFNULL(ANY_VALUE(project.name), '') AS name,
			%s AS gross, %s AS net, %s AS credits
		FROM %s GROUP BY id ORDER BY net DESC LIMIT 100`,
		billingGrossExpr, billingNetExpr, billingCreditsExpr, src))
	q.Parameters = params
	return collectRows[BillingProjectRow](q, ctx)
}

func (b *BQClient) GetBillingLabelGroups(ctx context.Context, src, labelKey string, params []bigquery.QueryParameter) ([]BillingGroupRow, error) {
	q := b.client.Query(billingLabelGroupSQL(src))
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
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			IFNULL(c.type, '(none)') AS type,
			IFNULL(c.name, '') AS name,
			ROUND(CAST(SUM(CAST(c.amount AS NUMERIC)) AS FLOAT64), 2) AS amount
		FROM %s t, UNNEST(t.credits) c
		GROUP BY type, name ORDER BY amount ASC LIMIT 100`, src))
	q.Parameters = params
	return collectRows[BillingCreditRow](q, ctx)
}

type BillingResourceRow struct {
	Name       string  `json:"name" bigquery:"name"`
	GlobalName string  `json:"global_name" bigquery:"global_name"`
	Service    string  `json:"service" bigquery:"service"`
	Project    string  `json:"project" bigquery:"project"`
	Net        float64 `json:"net" bigquery:"net"`
}

// GetBillingResources lists top-spending resources from the detailed
// export. search ("" = none) matches resource name/global name, passed as
// a parameter.
func (b *BQClient) GetBillingResources(ctx context.Context, src, search string, params []bigquery.QueryParameter) ([]BillingResourceRow, error) {
	where := "(resource.name IS NOT NULL OR resource.global_name IS NOT NULL)"
	if search != "" {
		where += " AND (STRPOS(LOWER(IFNULL(resource.name,'')), LOWER(@resource_q)) > 0 OR STRPOS(LOWER(IFNULL(resource.global_name,'')), LOWER(@resource_q)) > 0)"
		params = append(append([]bigquery.QueryParameter{}, params...),
			bigquery.QueryParameter{Name: "resource_q", Value: search})
	}
	q := b.client.Query(fmt.Sprintf(`
		SELECT
			IFNULL(resource.name, '') AS name,
			IFNULL(ANY_VALUE(resource.global_name), '') AS global_name,
			ANY_VALUE(service.description) AS service,
			IFNULL(ANY_VALUE(project.id), '') AS project,
			%s AS net
		FROM %s
		WHERE %s
		GROUP BY name ORDER BY net DESC LIMIT 50`,
		billingNetExpr, src, where))
	q.Parameters = params
	return collectRows[BillingResourceRow](q, ctx)
}

type BillingPriceRow struct {
	SkuID         string   `json:"sku_id" bigquery:"sku_id"`
	Sku           string   `json:"sku" bigquery:"sku"`
	Service       string   `json:"service" bigquery:"service"`
	PricingUnit   string   `json:"pricing_unit" bigquery:"pricing_unit"`
	ListPrice     float64  `json:"list_price" bigquery:"list_price"`
	ContractPrice *float64 `json:"contract_price" bigquery:"contract_price"`
	DiscountPct   *float64 `json:"discount_pct" bigquery:"discount_pct"`
	Tiers         int64    `json:"tiers" bigquery:"tiers"`
}

type billingPricingAsOfRow struct {
	AsOf string `bigquery:"as_of"`
}

// GetBillingPricing reads the latest pricing snapshot: partition-pruned to
// the last 7 days, then restricted to the newest pricing_as_of_time.
// First tier (start_usage_amount 0) is shown as the headline price.
func (b *BQClient) GetBillingPricing(ctx context.Context, project, dataset string, services []string, search string) ([]BillingPriceRow, string, error) {
	table := fmt.Sprintf("`%s.%s.%s`", project, dataset, billingPricingTable)
	where := "DATE(_PARTITIONTIME) >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)"
	var params []bigquery.QueryParameter
	if len(services) > 0 {
		where += " AND service.description IN UNNEST(@services)"
		params = append(params, bigquery.QueryParameter{Name: "services", Value: services})
	}
	if search != "" {
		where += " AND STRPOS(LOWER(sku.description), LOWER(@price_q)) > 0"
		params = append(params, bigquery.QueryParameter{Name: "price_q", Value: search})
	}

	asOfQ := b.client.Query(fmt.Sprintf(`
		SELECT FORMAT_TIMESTAMP('%%Y-%%m-%%d', MAX(pricing_as_of_time)) AS as_of
		FROM %s WHERE DATE(_PARTITIONTIME) >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)`, table))
	asOfRows, err := collectRows[billingPricingAsOfRow](asOfQ, ctx)
	if err != nil {
		return nil, "", fmt.Errorf("pricing as-of: %w", err)
	}
	if len(asOfRows) == 0 || asOfRows[0].AsOf == "" {
		return []BillingPriceRow{}, "", nil
	}
	asOf := asOfRows[0].AsOf
	// DATE(pricing_as_of_time) compares against a DATE, so the parameter
	// must be a civil.Date, not a string.
	asOfDate, err := civil.ParseDate(asOf)
	if err != nil {
		return nil, "", fmt.Errorf("unexpected pricing as-of %q: %w", asOf, err)
	}

	q := b.client.Query(fmt.Sprintf(`
		SELECT
			sku.id AS sku_id,
			ANY_VALUE(sku.description) AS sku,
			ANY_VALUE(service.description) AS service,
			ANY_VALUE(pricing_unit_description) AS pricing_unit,
			ROUND(CAST(ANY_VALUE((SELECT tr.account_currency_amount FROM UNNEST(list_price.tiered_rates) tr WHERE tr.start_usage_amount = 0 LIMIT 1)) AS FLOAT64), 6) AS list_price,
			ROUND(CAST(ANY_VALUE((SELECT tr.account_currency_amount FROM UNNEST(billing_account_price.tiered_rates) tr WHERE tr.start_usage_amount = 0 LIMIT 1)) AS FLOAT64), 6) AS contract_price,
			CAST(ANY_VALUE(billing_account_price.price_info.discount_percent) AS FLOAT64) AS discount_pct,
			ANY_VALUE(ARRAY_LENGTH(list_price.tiered_rates)) AS tiers
		FROM %s
		WHERE %s AND DATE(pricing_as_of_time) = @as_of_date
		GROUP BY sku_id ORDER BY list_price DESC LIMIT 200`, table, where))
	q.Parameters = append(params, bigquery.QueryParameter{Name: "as_of_date", Value: asOfDate})
	rows, err := collectRows[BillingPriceRow](q, ctx)
	return rows, asOf, err
}
