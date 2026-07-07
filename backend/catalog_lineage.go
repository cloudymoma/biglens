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
// and lives in a separate API from the catalog.
type LineageClient struct {
	client *lineage.Client
	parent string
}

func NewLineageClient(ctx context.Context, cfg *Config) (*LineageClient, error) {
	project := cfg.Catalog.Dataplex.ProjectID
	if project == "" {
		project = cfg.BigQuery.ProjectID
	}
	if project == "" {
		return nil, fmt.Errorf("no project configured for lineage")
	}
	loc := cfg.Catalog.LineageLocation
	if loc == "" {
		loc = cfg.Catalog.Dataplex.Location
	}
	if loc == "" || loc == "global" {
		loc = "us" // lineage is regional; "global" is not valid
	}

	var opts []option.ClientOption
	if cfg.BigQuery.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.BigQuery.CredentialsPath))
	}
	c, err := lineage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create lineage client: %w", err)
	}
	return &LineageClient{
		client: c,
		parent: fmt.Sprintf("projects/%s/locations/%s", project, loc),
	}, nil
}

func (l *LineageClient) Close() error { return l.client.Close() }

// UpstreamFQNs returns the fully-qualified names of assets that flow INTO the
// given target asset (its direct upstream sources).
func (l *LineageClient) UpstreamFQNs(ctx context.Context, targetFQN string) ([]string, error) {
	req := &lineagepb.SearchLinksRequest{
		Parent: l.parent,
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
