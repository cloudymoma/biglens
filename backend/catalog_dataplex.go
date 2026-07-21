package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	dataplex "cloud.google.com/go/dataplex/apiv1"
	"cloud.google.com/go/dataplex/apiv1/dataplexpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// maxImportEntries caps a single import to keep the graph and the bundle
// manageable. Truncation is surfaced to the caller, never silent.
const maxImportEntries = 1000

// importConcurrency bounds the parallel per-entry RPCs (aspect fetch and
// lineage search) during an import.
const importConcurrency = 16

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
	LineageDropped   int            `json:"lineage_dropped"`
	Preserved        int            `json:"preserved"`
	Pruned           int            `json:"pruned"`
	Truncated        bool           `json:"truncated"`
	LineageError     string         `json:"lineage_error,omitempty"`
	PruneError       string         `json:"prune_error,omitempty"`
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
	if query == "" {
		query = "*" // record the effective query in the manifest, not ""
	}
	entries, truncated, err := c.searchEntries(ctx, query)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{TypeCounts: map[string]int{}, Truncated: truncated}

	// Fetch detailed aspects per entry (best-effort), bounded-parallel: one
	// RPC per entry done serially takes minutes on real projects and blows
	// past HTTP timeouts. Each goroutine writes only its own slice slot.
	fullEntries := make([]*dataplexpb.Entry, len(entries))
	aspectErrs := make([]error, len(entries))
	sem := make(chan struct{}, importConcurrency)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entryKey := e.GetName()
			if entryKey == "" {
				entryKey = e.GetFullyQualifiedName()
			}
			fe, aErr := c.LookupEntry(ctx, entryKey)
			if aErr != nil {
				aspectErrs[i] = aErr
				fullEntries[i] = e
				return
			}
			fullEntries[i] = fe
		}()
	}
	wg.Wait()
	for _, aErr := range aspectErrs {
		if aErr != nil {
			result.AspectFailed++
			if result.AspectError == "" {
				result.AspectError = aErr.Error()
			}
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
	// but does not abort the import. Lineage is regional, so each asset carries
	// its own region (derived from the entry resource name).
	assets := make(map[string]lineageAsset, len(items))
	for _, it := range items {
		assets[it.concept.ID] = lineageAsset{
			FQN:      it.fqn,
			Location: entryLocation(it.entry.GetName()),
		}
	}
	lineageByID := c.fetchLineage(ctx, assets, result)

	// Existing concepts map to detect user_managed entries.
	existingConcepts, _ := bundle.ListConcepts()
	existingByID := make(map[string]Concept, len(existingConcepts))
	for _, x := range existingConcepts {
		existingByID[x.ID] = x
	}

	// Assemble bodies (overview + schema + containment parent + lineage sources) and write.
	for _, it := range items {
		parent := containment[it.concept.ID]
		sources := lineageByID[it.concept.ID]
		relBody := buildRelationshipBody(parent, sources)

		fc := it.concept

		if existing, ok := existingByID[fc.ID]; ok && existing.UserManaged {
			// User-managed concept: keep the user's body and annotations
			// (description, tags, timestamp); refresh only identity metadata
			// (type/title/resource/fqn) and the # Relationships section.
			fc.UserManaged = true
			fc.Description = existing.Description
			fc.Tags = existing.Tags
			fc.Timestamp = existing.Timestamp
			fc.Body = updateRelationshipsSection(existing.Body, relBody)
			result.Preserved++
		} else {
			fc.Body = buildConceptBody(it.entry, parent, sources)
		}

		fc.Links = extractLinks(fc.Body, fc.ID)
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

	// Prune on import: delete any existing concept that is NOT user_managed
	// and NOT in the new import result. Skipped when the search was truncated:
	// concepts beyond the cap are still live in the catalog, not stale, and
	// pruning them would silently shrink the graph.
	if !truncated {
		importedSet := make(map[string]bool, len(items))
		for _, it := range items {
			importedSet[it.concept.ID] = true
		}
		for _, existing := range existingConcepts {
			if !importedSet[existing.ID] && !existing.UserManaged {
				if err := bundle.DeleteConcept(existing.ID); err != nil {
					if result.PruneError == "" {
						result.PruneError = err.Error()
					}
				} else {
					result.Pruned++
				}
			}
		}
	}

	result.Edges = result.ContainmentEdges + result.LineageEdges
	result.ElapsedMs = time.Since(start).Milliseconds()

	// Record the import scope so it can be reproduced (Refresh) and shown
	// ("last imported …"). Concepts are already on disk, so a manifest
	// failure is surfaced as an error rather than silently ignored.
	manifest := Manifest{
		Project:         c.project,
		Location:        c.location,
		Query:           query,
		LineageLocation: c.cfg.Catalog.LineageLocation,
		ImportedAt:      time.Now().UTC().Format(time.RFC3339),
		Truncated:       truncated,
		EntryTypeCounts: result.TypeCounts,
	}
	if err := bundle.WriteManifest(manifest); err != nil {
		return nil, fmt.Errorf("import succeeded but manifest write failed: %w", err)
	}
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

// lineageAsset carries what fetchLineage needs per imported concept: identity
// (FQN) and the region whose lineage store holds the asset's links.
type lineageAsset struct {
	FQN      string
	Location string
}

// fetchLineage queries the Data Lineage API for upstream sources of each asset
// and returns targetConceptID -> []sourceConceptID for sources present in the
// imported set. Best-effort: errors are recorded on result.LineageError.
func (c *CatalogClient) fetchLineage(ctx context.Context, assets map[string]lineageAsset, result *ImportResult) map[string][]string {
	out := map[string][]string{}
	lc, err := NewLineageClient(ctx, c.cfg)
	if err != nil {
		result.LineageError = err.Error()
		return out
	}
	defer lc.Close()

	idByFQN := make(map[string]string, len(assets))
	for id, a := range assets {
		if a.FQN != "" {
			idByFQN[a.FQN] = id
			if norm := normalizeFQN(a.FQN); norm != "" {
				idByFQN[norm] = id
			}
		}
	}

	// One lineage search per asset, bounded-parallel (serial RPCs take
	// minutes on real projects). Goroutines write only their own slice slot;
	// results are aggregated serially afterwards.
	type lineageQuery struct {
		id string
		a  lineageAsset
	}
	var queries []lineageQuery
	for id, a := range assets {
		if a.FQN != "" {
			queries = append(queries, lineageQuery{id: id, a: a})
		}
	}
	type lineageReply struct {
		ups []string
		err error
	}
	replies := make([]lineageReply, len(queries))
	sem := make(chan struct{}, importConcurrency)
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ups, err := lc.UpstreamFQNs(ctx, lc.locationFor(q.a.Location), q.a.FQN)
			replies[i] = lineageReply{ups: ups, err: err}
		}()
	}
	wg.Wait()

	for i, q := range queries {
		r := replies[i]
		if r.err != nil {
			if result.LineageError == "" {
				result.LineageError = r.err.Error() // record first hard error (e.g. API disabled / no IAM)
			}
			continue
		}
		for _, srcFQN := range r.ups {
			srcID, found := matchFQN(srcFQN, idByFQN)
			if found {
				if srcID != q.id && !containsString(out[q.id], srcID) {
					out[q.id] = append(out[q.id], srcID)
				}
			} else {
				result.LineageDropped++
			}
		}
	}
	return out
}

func normalizeFQN(fqn string) string {
	s := strings.TrimSpace(fqn)
	s = strings.ReplaceAll(s, "`", "")
	if idx := strings.Index(s, ":"); idx >= 0 {
		prefix := s[:idx+1]
		rest := s[idx+1:]
		rest = strings.ReplaceAll(rest, ":", ".")
		s = prefix + rest
	}
	return s
}

func stripPartitionOrShardSuffix(fqn string) string {
	if idx := strings.Index(fqn, "@"); idx >= 0 {
		fqn = fqn[:idx]
	}
	parts := strings.Split(fqn, ".")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if i := strings.LastIndex(last, "_20"); i >= 0 && len(last[i:]) >= 9 {
			suffix := last[i+1:]
			allDigits := true
			for _, r := range suffix {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				parts[len(parts)-1] = last[:i]
				fqn = strings.Join(parts, ".")
			}
		}
	}
	return fqn
}

func matchFQN(srcFQN string, idByFQN map[string]string) (string, bool) {
	if srcFQN == "" {
		return "", false
	}
	if id, ok := idByFQN[srcFQN]; ok {
		return id, true
	}
	norm := normalizeFQN(srcFQN)
	if id, ok := idByFQN[norm]; ok {
		return id, true
	}
	stripped := stripPartitionOrShardSuffix(norm)
	if id, ok := idByFQN[stripped]; ok {
		return id, true
	}
	return "", false
}

func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// --- mapping helpers ---

// slugifyFQN converts a fully-qualified name into a bundle concept path (ID).
// Note: IDs are path locations within the OKF bundle; FQNs are canonical identity.
// e.g. "bigquery:proj.ds.tbl" -> "bigquery/proj/ds/tbl".
func slugifyFQN(fqn string) string {
	repl := strings.NewReplacer(":", "/", ".", "/", " ", "_", "`", "")
	slug := strings.Trim(repl.Replace(fqn), "/")
	if slug == "" {
		return "entry"
	}
	return slug
}

// entryLocation extracts the location segment from an entry resource name
// (projects/P/locations/LOC/entryGroups/...). Returns "" when absent.
func entryLocation(name string) string {
	const marker = "/locations/"
	i := strings.Index(name, marker)
	if i < 0 {
		return ""
	}
	rest := name[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
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
		FQN:         e.GetFullyQualifiedName(),
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

// updateRelationshipsSection replaces the "# Relationships" section of an
// existing body (its heading line through the next top-level "# " heading or
// EOF) with newRelBody, preserving user content before AND after it. The
// section is appended when absent. The heading match is line-anchored so
// prose mentions or deeper headings ("## Relationships") are not treated as
// the section.
func updateRelationshipsSection(existingBody, newRelBody string) string {
	lines := strings.Split(existingBody, "\n")
	start, end := -1, len(lines)
	for i, ln := range lines {
		t := strings.TrimRight(ln, " \t")
		if start < 0 {
			if t == "# Relationships" {
				start = i
			}
			continue
		}
		if strings.HasPrefix(t, "# ") {
			end = i
			break
		}
	}

	before := lines
	var after []string
	if start >= 0 {
		before = lines[:start]
		after = lines[end:]
	}

	var parts []string
	if pre := strings.TrimRight(strings.Join(before, "\n"), "\n "); pre != "" {
		parts = append(parts, pre)
	}
	if newRelBody != "" {
		parts = append(parts, newRelBody)
	}
	if post := strings.Trim(strings.Join(after, "\n"), "\n "); post != "" {
		parts = append(parts, post)
	}
	return strings.Join(parts, "\n\n")
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
