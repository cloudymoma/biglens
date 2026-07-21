package main

import (
	"strings"
	"testing"

	"cloud.google.com/go/dataplex/apiv1/dataplexpb"
)

// A definition EntryLink must resolve to (asset, term): the SOURCE reference
// is the asset being defined, the TARGET is the glossary term.
func TestDefinitionEdge(t *testing.T) {
	link := &dataplexpb.EntryLink{
		EntryLinkType: definitionLinkType,
		EntryReferences: []*dataplexpb.EntryLink_EntryReference{
			{Name: "projects/p/locations/us/entryGroups/g/entries/tbl", Type: dataplexpb.EntryLink_EntryReference_SOURCE},
			{Name: "projects/p/locations/us/entryGroups/g/entries/term", Type: dataplexpb.EntryLink_EntryReference_TARGET},
		},
	}
	asset, term := definitionEdge(link)
	if asset != "projects/p/locations/us/entryGroups/g/entries/tbl" {
		t.Errorf("asset = %q", asset)
	}
	if term != "projects/p/locations/us/entryGroups/g/entries/term" {
		t.Errorf("term = %q", term)
	}

	// A link with a missing side must not produce an edge.
	partial := &dataplexpb.EntryLink{
		EntryReferences: []*dataplexpb.EntryLink_EntryReference{
			{Name: "projects/p/locations/us/entryGroups/g/entries/tbl", Type: dataplexpb.EntryLink_EntryReference_SOURCE},
		},
	}
	if a, tm := definitionEdge(partial); tm != "" || a == "" {
		t.Errorf("partial link: asset=%q term=%q, want asset set and term empty", a, tm)
	}
}

// LookupEntryLinks needs a projects/P/locations/L scope derived from the
// entry's own resource name.
func TestEntryScope(t *testing.T) {
	tests := []struct{ name, want string }{
		{"projects/p/locations/us-central1/entryGroups/g/entries/e", "projects/p/locations/us-central1"},
		{"projects/p/locations/global", "projects/p/locations/global"},
	}
	for _, tt := range tests {
		if got := entryScope(tt.name); got != tt.want {
			t.Errorf("entryScope(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Glossary terms appear in the Relationships section as "Defined by" links.
func TestBuildRelationshipBodyWithTerms(t *testing.T) {
	body := buildRelationshipBody("bigquery/p/ds", []string{"bigquery/p/ds/src"}, []string{"glossaries/g/terms/pii"})
	for _, want := range []string{
		"- Parent: [bigquery/p/ds](/bigquery/p/ds)",
		"- Derived from: [bigquery/p/ds/src](/bigquery/p/ds/src)",
		"- Defined by: [glossaries/g/terms/pii](/glossaries/g/terms/pii)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if buildRelationshipBody("", nil, nil) != "" {
		t.Error("empty relationships must render nothing")
	}
}

// Kind is decided per line: a link on a continuation line below a bullet must
// NOT inherit the bullet's kind.
func TestClassifyLinksPerLine(t *testing.T) {
	body := "- Parent: [ds](/ds)\n  See [note](/note) here.\n"
	got := map[string]string{}
	for _, tl := range classifyLinks(body, "tbl") {
		got[tl.Target] = tl.Kind
	}
	if got["ds"] != "containment" || got["note"] != "reference" {
		t.Errorf("classifyLinks = %v, want ds:containment note:reference", got)
	}
}

// The graph must carry a kind per edge, classified from the Relationships
// bullet labels, so the frontend can style containment, lineage, and
// definition edges distinctly.
func TestBuildGraphEdgeKinds(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	mustWrite := func(c Concept) {
		t.Helper()
		if err := b.WriteConcept(c); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(Concept{ID: "ds", Type: "BigQuery Dataset"})
	mustWrite(Concept{ID: "src", Type: "BigQuery Table"})
	mustWrite(Concept{ID: "term", Type: "Glossary Term"})
	mustWrite(Concept{ID: "note", Type: "Note"})
	mustWrite(Concept{
		ID:   "tbl",
		Type: "BigQuery Table",
		Body: "See also [note](/note).\n\n# Relationships\n\n" +
			"- Parent: [ds](/ds)\n" +
			"- Derived from: [src](/src)\n" +
			"- Defined by: [term](/term)\n",
	})

	g, err := b.BuildGraph()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, e := range g.Edges {
		if e.Source == "tbl" {
			kinds[e.Target] = e.Kind
		}
	}
	want := map[string]string{
		"ds":   "containment",
		"src":  "lineage",
		"term": "definition",
		"note": "reference",
	}
	for target, kind := range want {
		if kinds[target] != kind {
			t.Errorf("edge tbl->%s kind = %q, want %q", target, kinds[target], kind)
		}
	}
	if len(g.Edges) != len(want) {
		t.Errorf("edges = %d, want %d (%+v)", len(g.Edges), len(want), g.Edges)
	}
}
