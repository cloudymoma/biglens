package main

// BQClient query methods for the GCP Billing open-data section. SQL strings
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
