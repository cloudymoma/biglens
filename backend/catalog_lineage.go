package main

import (
	"context"
	"fmt"

	lineage "cloud.google.com/go/datacatalog/lineage/apiv1"
	"cloud.google.com/go/datacatalog/lineage/apiv1/lineagepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const maxLinksPerAsset = 100

// LineageClient wraps the Data Lineage API, which exposes source->target data
// flow (e.g. ETL/query transformations between tables). Lineage is regional
// and lives in a separate API from the catalog: every search names one
// location, and links live wherever the producing job ran.
type LineageClient struct {
	client  *lineage.Client
	project string
	// fixedLocation, when non-empty, overrides per-asset location derivation
	// (set from config for users who want to pin the lineage region).
	fixedLocation string
}

func NewLineageClient(ctx context.Context, cfg *Config) (*LineageClient, error) {
	project := cfg.Catalog.Dataplex.ProjectID
	if project == "" {
		project = cfg.BigQuery.ProjectID
	}
	if project == "" {
		return nil, fmt.Errorf("no project configured for lineage")
	}
	fixed := cfg.Catalog.LineageLocation
	if fixed == "" {
		if loc := cfg.Catalog.Dataplex.Location; loc != "" && loc != "global" {
			fixed = loc // an explicit regional catalog location pins lineage too
		}
	}

	var opts []option.ClientOption
	if cfg.BigQuery.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.BigQuery.CredentialsPath))
	}
	c, err := lineage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create lineage client: %w", err)
	}
	return &LineageClient{client: c, project: project, fixedLocation: fixed}, nil
}

func (l *LineageClient) Close() error { return l.client.Close() }

// locationFor picks the lineage location to query for an asset: the configured
// override if set, else the asset's own region, else "us" ("global" is not a
// valid lineage location).
func (l *LineageClient) locationFor(assetLocation string) string {
	if l.fixedLocation != "" {
		return l.fixedLocation
	}
	if assetLocation != "" && assetLocation != "global" {
		return assetLocation
	}
	return "us"
}

// UpstreamFQNs returns the fully-qualified names of assets that flow INTO the
// given target asset (its direct upstream sources), searching lineage in the
// given location.
func (l *LineageClient) UpstreamFQNs(ctx context.Context, location, targetFQN string) ([]string, error) {
	req := &lineagepb.SearchLinksRequest{
		Parent: fmt.Sprintf("projects/%s/locations/%s", l.project, location),
		Criteria: &lineagepb.SearchLinksRequest_Target{
			Target: &lineagepb.EntityReference{FullyQualifiedName: targetFQN},
		},
		PageSize: maxLinksPerAsset,
	}
	it := l.client.SearchLinks(ctx, req)
	var out []string
	for {
		link, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("search links for %q: %w", targetFQN, err)
		}
		if s := link.GetSource(); s != nil && s.GetFullyQualifiedName() != "" {
			out = append(out, s.GetFullyQualifiedName())
		}
	}
	return out, nil
}
