package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	dataplex "cloud.google.com/go/dataplex/apiv1"
	"cloud.google.com/go/dataplex/apiv1/dataplexpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// maxImportEntries caps a single import to keep the graph and the bundle
// manageable. Truncation is surfaced to the caller, never silent.
const maxImportEntries = 1000

// CatalogClient wraps the Dataplex Universal Catalog search API.
type CatalogClient struct {
	client   *dataplex.CatalogClient
	cfg      *Config
	project  string
	location string
}

// NewCatalogClient builds a Dataplex catalog client. It is created on demand
// (per import) so the server runs without Dataplex permissions when unused.
func NewCatalogClient(ctx context.Context, cfg *Config) (*CatalogClient, error) {
	project := cfg.Catalog.Dataplex.ProjectID
	if project == "" {
		project = cfg.BigQuery.ProjectID
	}
	if project == "" {
		return nil, fmt.Errorf("no project configured for Dataplex (set catalog.dataplex.project_id or bigquery.project_id)")
	}
	location := cfg.Catalog.Dataplex.Location
	if location == "" {
		location = "global"
	}

	var opts []option.ClientOption
	if cfg.BigQuery.CredentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.BigQuery.CredentialsPath))
	}
	client, err := dataplex.NewCatalogClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create dataplex catalog client: %w", err)
	}
	return &CatalogClient{client: client, cfg: cfg, project: project, location: location}, nil
}

func (c *CatalogClient) Close() error { return c.client.Close() }

// ImportResult summarizes an import run.
type ImportResult struct {
	Imported         int            `json:"imported"`
	Edges            int            `json:"edges"`
	ContainmentEdges int            `json:"containment_edges"`
	LineageEdges     int            `json:"lineage_edges"`
	Truncated        bool           `json:"truncated"`
	LineageError     string         `json:"lineage_error,omitempty"`
	AspectError      string         `json:"aspect_error,omitempty"`
	AspectFailed     int            `json:"aspect_failed,omitempty"`
	ElapsedMs        int64          `json:"elapsed_ms,omitempty"`
	TypeCounts       map[string]int `json:"type_counts"`
}

// aspectTypeFilter lists the aspect types fetched per entry. The API requires
// full aspect-type resource names; the system aspect types (schema, overview,
// descriptions) live in the Google-managed "dataplex-types" project.
var aspectTypeFilter = []string{
	"projects/dataplex-types/locations/global/aspectTypes/schema",
	"projects/dataplex-types/locations/global/aspectTypes/overview",
	"projects/dataplex-types/locations/global/aspectTypes/descriptions",
}

// LookupEntry fetches detailed entry aspects (schema, overview, descriptions)
// via GetEntry on the entry's own resource name. GetEntry is used (not the
// LookupEntry RPC) because search results span regional locations, while
// LookupEntry requires the request Name's location to match the entry's.
func (c *CatalogClient) LookupEntry(ctx context.Context, entryName string) (*dataplexpb.Entry, error) {
	req := &dataplexpb.GetEntryRequest{
		Name:        entryName,
		View:        dataplexpb.EntryView_CUSTOM,
		AspectTypes: aspectTypeFilter,
	}
	entry, err := c.client.GetEntry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get entry %q: %w", entryName, err)
	}
	return entry, nil
}

// Import searches the catalog for entries matching query and writes each as an
// OKF concept into the bundle. It wires two kinds of edges:
//   - containment: dataset -> table, derived from the concept-path hierarchy
//   - lineage: source -> target ETL data flow, from the Data Lineage API
//     (best-effort; a missing/disabled Lineage API does not fail the import)
func (c *CatalogClient) Import(ctx context.Context, bundle *OKFBundle, query string) (*ImportResult, error) {
	start := time.Now()
	entries, truncated, err := c.searchEntries(ctx, query)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{TypeCounts: map[string]int{}, Truncated: truncated}

	// Fetch detailed aspects per entry (best-effort).
	fullEntries := make([]*dataplexpb.Entry, len(entries))
	for i, e := range entries {
		entryKey := e.GetName()
		if entryKey == "" {
			entryKey = e.GetFullyQualifiedName()
		}
		fe, aErr := c.LookupEntry(ctx, entryKey)
		if aErr != nil {
			result.AspectFailed++
			if result.AspectError == "" {
				result.AspectError = aErr.Error()
			}
			fullEntries[i] = e
		} else {
			fullEntries[i] = fe
		}
	}

	// Base concepts (frontmatter only; bodies/links assembled below).
	type item struct {
		concept Concept
		fqn     string
		entry   *dataplexpb.Entry
	}
	items := make([]*item, 0, len(entries))
	idSet := make(map[string]bool, len(entries))
	for _, e := range fullEntries {
		base := entryBaseConcept(e)
		items = append(items, &item{concept: base, fqn: e.GetFullyQualifiedName(), entry: e})
		idSet[base.ID] = true
	}

	// Containment edges: each concept links to its longest existing ancestor
	// in the path hierarchy (e.g. bigquery/proj/ds/tbl -> bigquery/proj/ds).
	containment := make(map[string]string, len(items))
	for _, it := range items {
		if p := longestPrefixIn(it.concept.ID, idSet); p != "" {
			containment[it.concept.ID] = p
		}
	}

	// Lineage edges: best-effort. Failure to reach the Lineage API is recorded
	// but does not abort the import.
	fqnByID := make(map[string]string, len(items))
	for _, it := range items {
		fqnByID[it.concept.ID] = it.fqn
	}
	lineageByID := c.fetchLineage(ctx, fqnByID, idSet, result)

	// Assemble bodies (overview + schema + containment parent + lineage sources) and write.
	for _, it := range items {
		parent := containment[it.concept.ID]
		sources := lineageByID[it.concept.ID]
		body := buildConceptBody(it.entry, parent, sources)

		fc := it.concept
		fc.Body = body
		fc.Links = extractLinks(body, fc.ID)
		if err := bundle.WriteConcept(fc); err != nil {
			return nil, fmt.Errorf("write concept %q: %w", fc.ID, err)
		}
		result.Imported++
		result.TypeCounts[fc.Type]++
		if parent != "" {
			result.ContainmentEdges++
		}
		result.LineageEdges += len(sources)
	}
	result.Edges = result.ContainmentEdges + result.LineageEdges
	result.ElapsedMs = time.Since(start).Milliseconds()
	return result, nil
}

func (c *CatalogClient) searchEntries(ctx context.Context, query string) ([]*dataplexpb.Entry, bool, error) {
	if query == "" {
		query = "*"
	}
	req := &dataplexpb.SearchEntriesRequest{
		Name:     fmt.Sprintf("projects/%s/locations/%s", c.project, c.location),
		Query:    query,
		Scope:    fmt.Sprintf("projects/%s", c.project),
		PageSize: 100,
	}
	it := c.client.SearchEntries(ctx, req)
	var entries []*dataplexpb.Entry
	for {
		res, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("search entries: %w", err)
		}
		if e := res.GetDataplexEntry(); e != nil {
			entries = append(entries, e)
		}
		if len(entries) >= maxImportEntries {
			return entries, true, nil
		}
	}
	return entries, false, nil
}

// fetchLineage queries the Data Lineage API for upstream sources of each asset
// and returns targetConceptID -> []sourceConceptID for sources present in the
// imported set. Best-effort: errors are recorded on result.LineageError.
func (c *CatalogClient) fetchLineage(ctx context.Context, fqnByID map[string]string, idSet map[string]bool, result *ImportResult) map[string][]string {
	out := map[string][]string{}
	lc, err := NewLineageClient(ctx, c.cfg)
	if err != nil {
		result.LineageError = err.Error()
		return out
	}
	defer lc.Close()

	for id, fqn := range fqnByID {
		if fqn == "" {
			continue
		}
		ups, err := lc.UpstreamFQNs(ctx, fqn)
		if err != nil {
			result.LineageError = err.Error()
			break // stop on first hard error (e.g. API disabled / no IAM)
		}
		for _, srcFQN := range ups {
			srcID := slugifyFQN(srcFQN)
			if idSet[srcID] && srcID != id {
				out[id] = append(out[id], srcID)
			}
		}
	}
	return out
}

// --- mapping helpers ---

// slugifyFQN converts a fully-qualified name into a bundle concept path.
// e.g. "bigquery:proj.ds.tbl" -> "bigquery/proj/ds/tbl".
func slugifyFQN(fqn string) string {
	repl := strings.NewReplacer(":", "/", ".", "/", " ", "_")
	slug := strings.Trim(repl.Replace(fqn), "/")
	if slug == "" {
		return "entry"
	}
	return slug
}

// entryConceptID derives a stable bundle path from an entry's fully-qualified
// name (preferred) or resource name.
func entryConceptID(e *dataplexpb.Entry) string {
	base := e.GetFullyQualifiedName()
	if base == "" {
		base = e.GetName()
	}
	return slugifyFQN(base)
}

// entryBaseConcept maps a Dataplex entry to an OKF concept's frontmatter
// fields. The relationship body is added later by Import.
func entryBaseConcept(e *dataplexpb.Entry) Concept {
	src := e.GetEntrySource()
	var title, description, resource string
	if src != nil {
		title = src.GetDisplayName()
		description = src.GetDescription()
		resource = src.GetResource()
	}
	if title == "" {
		title = e.GetFullyQualifiedName()
	}
	if resource == "" {
		resource = e.GetName()
	}
	return Concept{
		ID:          entryConceptID(e),
		Type:        prettyEntryType(e.GetEntryType()),
		Title:       title,
		Description: description,
		Resource:    resource,
	}
}

// longestPrefixIn returns the longest proper path-prefix of id that exists in
// set (the containment parent), or "" if none.
func longestPrefixIn(id string, set map[string]bool) string {
	parts := strings.Split(id, "/")
	for i := len(parts) - 1; i >= 1; i-- {
		p := strings.Join(parts[:i], "/")
		if set[p] {
			return p
		}
	}
	return ""
}

// buildConceptBody assembles OKF body sections in order:
// 1. # Overview (from overview / descriptions aspect)
// 2. # Schema (from schema aspect fields)
// 3. # Relationships (containment parent + lineage sources)
func buildConceptBody(entry *dataplexpb.Entry, parentID string, sources []string) string {
	var sections []string

	if overviewSec := renderOverviewSection(entry); overviewSec != "" {
		sections = append(sections, overviewSec)
	}
	if schemaSec := renderSchemaSection(entry); schemaSec != "" {
		sections = append(sections, schemaSec)
	}
	if relSec := buildRelationshipBody(parentID, sources); relSec != "" {
		sections = append(sections, relSec)
	}

	return strings.Join(sections, "\n\n")
}

func renderOverviewSection(entry *dataplexpb.Entry) string {
	overviewData := extractAspectData(entry, "overview")
	descData := extractAspectData(entry, "descriptions")

	var overviewText string
	if overviewData != nil {
		if content, ok := overviewData["content"].(string); ok && strings.TrimSpace(content) != "" {
			overviewText = strings.TrimSpace(content)
		}
	}
	if overviewText == "" && descData != nil {
		for _, k := range []string{"description", "overview", "summary", "content"} {
			if v, ok := descData[k].(string); ok && strings.TrimSpace(v) != "" {
				overviewText = strings.TrimSpace(v)
				break
			}
		}
	}

	if overviewText == "" {
		return ""
	}
	return "# Overview\n\n" + overviewText
}

func renderSchemaSection(entry *dataplexpb.Entry) string {
	schemaData := extractAspectData(entry, "schema")
	if schemaData == nil {
		return ""
	}
	rawFields, ok := schemaData["fields"].([]interface{})
	if !ok || len(rawFields) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Schema\n\n")
	renderFieldList(&b, rawFields, 0)
	return strings.TrimRight(b.String(), "\n")
}

func renderFieldList(b *strings.Builder, fields []interface{}, indentLevel int) {
	indent := strings.Repeat("  ", indentLevel)
	for _, item := range fields {
		fMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := fMap["name"].(string)
		if name == "" {
			continue
		}
		// The dataplex-types schema aspect uses "dataType"; "type" kept as a
		// fallback for custom schema aspects.
		fType, _ := fMap["dataType"].(string)
		if fType == "" {
			fType, _ = fMap["type"].(string)
		}
		mode, _ := fMap["mode"].(string)
		desc, _ := fMap["description"].(string)

		typeMode := fType
		if mode != "" && mode != "NULLABLE" {
			if typeMode != "" {
				typeMode += ", " + mode
			} else {
				typeMode = mode
			}
		} else if mode == "NULLABLE" {
			if typeMode != "" {
				typeMode += ", NULLABLE"
			} else {
				typeMode = "NULLABLE"
			}
		}

		line := indent + fmt.Sprintf("- `%s`", name)
		if typeMode != "" {
			line += fmt.Sprintf(" (%s)", typeMode)
		}
		if desc != "" {
			line += ": " + desc
		}
		b.WriteString(line + "\n")

		if sub, ok := fMap["fields"].([]interface{}); ok && len(sub) > 0 {
			renderFieldList(b, sub, indentLevel+1)
		} else if sub, ok := fMap["subfields"].([]interface{}); ok && len(sub) > 0 {
			renderFieldList(b, sub, indentLevel+1)
		}
	}
}

func extractAspectData(entry *dataplexpb.Entry, aspectKind string) map[string]interface{} {
	if entry == nil || entry.Aspects == nil {
		return nil
	}
	for key, aspect := range entry.Aspects {
		aType := aspect.GetAspectType()
		if isAspectKind(key, aspectKind) || isAspectKind(aType, aspectKind) {
			if aspect.GetData() != nil {
				return aspect.GetData().AsMap()
			}
		}
	}
	return nil
}

func isAspectKind(name, kind string) bool {
	name = strings.ToLower(name)
	kind = strings.ToLower(kind)
	return name == kind || strings.HasSuffix(name, "."+kind) || strings.HasSuffix(name, "/"+kind)
}

// buildRelationshipBody renders the markdown body with containment and lineage
// links (which BuildGraph turns into edges).
func buildRelationshipBody(parentID string, sources []string) string {
	if parentID == "" && len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Relationships\n\n")
	if parentID != "" {
		b.WriteString(fmt.Sprintf("- Parent: [%s](/%s)\n", parentID, parentID))
	}
	for _, s := range sources {
		b.WriteString(fmt.Sprintf("- Derived from: [%s](/%s)\n", s, s))
	}
	return strings.TrimRight(b.String(), "\n")
}

// prettyEntryType turns an EntryType resource name into a human-readable type
// label that drives node coloring. e.g.
// ".../entryTypes/bigquery-table" -> "BigQuery Table".
func prettyEntryType(entryType string) string {
	if entryType == "" {
		return "Untyped"
	}
	last := entryType
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	switch last {
	case "bigquery-table":
		return "BigQuery Table"
	case "bigquery-view":
		return "BigQuery View"
	case "bigquery-dataset":
		return "BigQuery Dataset"
	case "bigquery-model":
		return "BigQuery Model"
	case "glossary-term":
		return "Glossary Term"
	case "glossary-category":
		return "Glossary Category"
	}
	parts := strings.FieldsFunc(last, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	if len(parts) == 0 {
		return "Untyped"
	}
	return strings.Join(parts, " ")
}
