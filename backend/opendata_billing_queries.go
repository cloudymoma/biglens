package main

// BQClient query methods for the GCP Billing open-data section. SQL strings
// come from builders in opendata_billing.go; table identifiers are only ever
// the validated project/dataset plus names read from INFORMATION_SCHEMA.

import (
	"context"
	"fmt"
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
