package main

// Pure domain logic for the GCP Billing open-data section: dataset
// validation, export-table classification, filter/SQL builders, rollups.
// Everything here is unit-testable without BigQuery.

import (
	"fmt"
	"regexp"
	"strings"
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
