package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantYAML  string
		wantBody  string
	}{
		{
			name:     "standard frontmatter",
			content:  "---\ntype: Table\ntitle: Users\n---\n# Body\ntext",
			wantYAML: "type: Table\ntitle: Users",
			wantBody: "# Body\ntext",
		},
		{
			name:     "no frontmatter",
			content:  "# Just a body\nno header",
			wantYAML: "",
			wantBody: "# Just a body\nno header",
		},
		{
			name:     "unclosed frontmatter treated as body",
			content:  "---\ntype: Table\nno closing",
			wantYAML: "",
			wantBody: "---\ntype: Table\nno closing",
		},
		{
			name:     "empty frontmatter",
			content:  "---\n---\nbody",
			wantYAML: "",
			wantBody: "body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotYAML, gotBody := splitFrontmatter(tt.content)
			if gotYAML != tt.wantYAML {
				t.Errorf("yaml = %q, want %q", gotYAML, tt.wantYAML)
			}
			if gotBody != tt.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

func TestResolveLink(t *testing.T) {
	tests := []struct {
		name   string
		href   string
		fromID string
		want   string
	}{
		{"bundle-absolute", "/tables/users", "datasets/sales", "tables/users"},
		{"absolute strips md", "/tables/users.md", "x", "tables/users"},
		{"relative same dir", "./users", "tables/orders", "tables/users"},
		{"relative implicit", "users", "tables/orders", "tables/users"},
		{"relative parent", "../glossary/pii", "tables/orders", "glossary/pii"},
		{"strip anchor", "/tables/users#schema", "x", "tables/users"},
		{"external http", "https://example.com/x", "x", ""},
		{"mailto", "mailto:a@b.com", "x", ""},
		{"self link ignored later", "/tables/orders", "tables/orders", "tables/orders"},
		{"empty", "", "x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveLink(tt.href, tt.fromID); got != tt.want {
				t.Errorf("resolveLink(%q, %q) = %q, want %q", tt.href, tt.fromID, got, tt.want)
			}
		})
	}
}

func TestExtractLinks(t *testing.T) {
	body := "See [users](/tables/users) and [orders](./orders) and [ext](https://x.com) " +
		"and [self](/tables/orders) and [dup](/tables/users)."
	got := extractLinks(body, "tables/orders")
	want := []string{"tables/users"} // orders excluded? no: ./orders -> tables/orders == self, excluded
	// "./orders" resolves to tables/orders which equals fromID -> excluded.
	// external excluded, self excluded, dup deduped.
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
}

// writeBundle creates a temp bundle with the given files (id -> content).
func writeBundle(t *testing.T, files map[string]string) *OKFBundle {
	t.Helper()
	root := t.TempDir()
	for id, content := range files {
		p := filepath.Join(root, filepath.FromSlash(id)+".md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewOKFBundle(root)
}

func TestBuildGraph(t *testing.T) {
	b := writeBundle(t, map[string]string{
		"datasets/sales": "---\ntype: BigQuery Dataset\ntitle: Sales\n---\nHas [users](/tables/users).",
		"tables/users":   "---\ntype: BigQuery Table\ntitle: Users\n---\nIn [sales](/datasets/sales). Broken [x](/nope).",
		"index":          "no frontmatter listing", // not reserved name here (index, not index.md base) - actually file is index.md
	})
	// remove the index concept expectation: index.md is reserved
	g, err := b.BuildGraph()
	if err != nil {
		t.Fatal(err)
	}
	// index.md is reserved -> 2 nodes only
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (%+v)", len(g.Nodes), g.Nodes)
	}
	// edges: sales->users, users->sales ; users->nope dropped (dangling)
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %d, want 2 (%+v)", len(g.Edges), g.Edges)
	}
}

func TestSafePathTraversal(t *testing.T) {
	b := writeBundle(t, map[string]string{"a": "x"})
	bad := []string{"../escape", "../../etc/passwd", "foo/../../bar"}
	for _, id := range bad {
		if _, err := b.safePath(id); err == nil {
			t.Errorf("safePath(%q) should have failed", id)
		}
	}
	good := []string{"a", "tables/users", "/tables/users", "a/b/c"}
	for _, id := range good {
		if _, err := b.safePath(id); err != nil {
			t.Errorf("safePath(%q) unexpected error: %v", id, err)
		}
	}
}

func TestGetConceptNeighbors(t *testing.T) {
	b := writeBundle(t, map[string]string{
		"a": "---\ntype: T\n---\nlink [b](/b)",
		"b": "---\ntype: T\n---\nno links",
		"c": "---\ntype: T\n---\nlink [a](/a)",
	})
	d, err := b.GetConcept("a")
	if err != nil {
		t.Fatal(err)
	}
	// a out-links to b, c in-links to a -> 2 neighbors
	if len(d.Neighbors) != 2 {
		t.Fatalf("neighbors = %d, want 2 (%+v)", len(d.Neighbors), d.Neighbors)
	}
	if d.Concept.Type != "T" {
		t.Errorf("type = %q, want T", d.Concept.Type)
	}
}

// TestSampleBundle verifies the on-disk OKF bundle parses as valid OKF: it
// builds without error and every concept carries the required `type`. The
// bundle is mutable runtime data (it may be empty on a clean checkout, or hold
// imported concepts), so this asserts invariants rather than an exact count.
func TestSampleBundle(t *testing.T) {
	b := NewOKFBundle("../okf-bundle")
	g, err := b.BuildGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) == 0 {
		t.Skip("bundle is empty (clean checkout) — nothing to validate")
	}
	for _, n := range g.Nodes {
		if n.Type == "" {
			t.Errorf("concept %q missing required type", n.ID)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	b := writeBundle(t, map[string]string{})
	in := Concept{
		ID:          "tables/users",
		Type:        "BigQuery Table",
		Title:       "Users",
		Description: "User accounts",
		Resource:    "bigquery:proj.ds.users",
		Tags:        []string{"pii", "core"},
		Body:        "# Schema\n\nid, email\n\nSee [orders](/tables/orders).",
	}
	if err := b.WriteConcept(in); err != nil {
		t.Fatal(err)
	}
	d, err := b.GetConcept("tables/users")
	if err != nil {
		t.Fatal(err)
	}
	got := d.Concept
	if got.Type != in.Type || got.Title != in.Title || got.Description != in.Description ||
		got.Resource != in.Resource || len(got.Tags) != 2 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.Links) != 1 || got.Links[0] != "tables/orders" {
		t.Errorf("links = %v, want [tables/orders]", got.Links)
	}
}

func TestDeleteConcept(t *testing.T) {
	b := writeBundle(t, map[string]string{"a": "---\ntype: T\n---\nx"})
	if err := b.DeleteConcept("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetConcept("a"); err == nil {
		t.Error("expected error reading deleted concept")
	}
	// traversal-protected delete must fail
	if err := b.DeleteConcept("../../etc/passwd"); err == nil {
		t.Error("delete traversal should fail")
	}
}

func TestSearch(t *testing.T) {
	b := writeBundle(t, map[string]string{
		"tables/users": "---\ntype: BigQuery Table\ntitle: Users\ntags: [pii]\n---\nx",
		"tables/orders": "---\ntype: BigQuery Table\ntitle: Orders\n---\nx",
		"glossary/pii":  "---\ntype: Glossary Term\ntitle: PII\n---\nx",
	})
	tests := []struct {
		name       string
		query      string
		typeFilter string
		wantCount  int
	}{
		{"by title", "users", "", 1},
		{"by tag", "pii", "", 2}, // tables/users tag + glossary/pii title
		{"by type filter", "", "BigQuery Table", 2},
		{"type + query", "orders", "BigQuery Table", 1},
		{"no match", "zzz", "", 0},
		{"empty returns all", "", "", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.Search(tt.query, tt.typeFilter, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.wantCount {
				t.Errorf("Search(%q,%q) = %d results, want %d", tt.query, tt.typeFilter, len(got), tt.wantCount)
			}
		})
	}
}
