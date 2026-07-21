package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/dataplex/apiv1/dataplexpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Entry labels become sorted frontmatter tags; the entry-source update time
// becomes the concept timestamp (falling back to the catalog update time).
func TestEntryTagsAndTimestamp(t *testing.T) {
	srcTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	e := &dataplexpb.Entry{
		Name:       "projects/p/locations/us/entryGroups/g/entries/tbl",
		EntryType:  "projects/p/locations/us/entryTypes/bigquery-table",
		UpdateTime: timestamppb.New(srcTime.Add(time.Hour)),
		EntrySource: &dataplexpb.EntrySource{
			Labels:     map[string]string{"env": "prod", "team": ""},
			UpdateTime: timestamppb.New(srcTime),
		},
	}
	c := entryBaseConcept(e)
	if want := []string{"env:prod", "team"}; strings.Join(c.Tags, ",") != strings.Join(want, ",") {
		t.Errorf("tags = %v, want %v", c.Tags, want)
	}

	// Junk labels (seen live: per-character keys like "&", " ", "A" with
	// value "true" on marketplace entries) must be dropped: only valid GCP
	// label syntax (lowercase key) survives.
	e.EntrySource.Labels = map[string]string{"&": "true", " ": "true", "A": "true", "a": "true", "BigQuery": "true", "env": "prod"}
	if got := entryBaseConcept(e).Tags; strings.Join(got, ",") != "env:prod" {
		t.Errorf("junk labels not filtered: %v", got)
	}
	if c.Timestamp != "2026-07-01T12:00:00Z" {
		t.Errorf("timestamp = %q, want entry-source update time", c.Timestamp)
	}

	// No entry-source time -> catalog update time.
	e.EntrySource.UpdateTime = nil
	if got := entryBaseConcept(e).Timestamp; got != "2026-07-01T13:00:00Z" {
		t.Errorf("fallback timestamp = %q", got)
	}
}

// SearchEntries pages can overlap (seen live: 865 results for ~500 distinct
// entries), so entries must be de-duplicated by resource name before import —
// otherwise later duplicates overwrite concepts and their relationship edges.
func TestDedupeEntries(t *testing.T) {
	a := &dataplexpb.Entry{Name: "projects/p/locations/us/entryGroups/g/entries/a"}
	b := &dataplexpb.Entry{Name: "projects/p/locations/us/entryGroups/g/entries/b"}
	got, dups := dedupeEntries([]*dataplexpb.Entry{a, b, a, a})
	if len(got) != 2 || dups != 2 {
		t.Errorf("dedupeEntries = %d entries, %d dups; want 2, 2", len(got), dups)
	}
	if got[0].GetName() != a.GetName() || got[1].GetName() != b.GetName() {
		t.Errorf("order not preserved: %v", got)
	}
}

// Distinct FQNs slugging to the same bundle path must not overwrite each
// other; the second gets a -N suffix.
func TestUniqueID(t *testing.T) {
	set := map[string]bool{"a/b": true, "a/b-2": true}
	if got := uniqueID("a/b", set); got != "a/b-3" {
		t.Errorf("uniqueID = %q, want a/b-3", got)
	}
}

// Entry paths must never be silently lost: duplicates get -N suffixes and
// reserved basenames (index/log) are renamed, each counted as a collision.
func TestSafeConceptID(t *testing.T) {
	r := &ImportResult{}
	set := map[string]bool{"repo/defs/x": true}
	if got := safeConceptID("repo/defs/x", set, r); got != "repo/defs/x-2" {
		t.Errorf("collision: got %q", got)
	}
	if got := safeConceptID("repo/defs/index", set, r); got != "repo/defs/index-asset" {
		t.Errorf("reserved: got %q", got)
	}
	if got := safeConceptID("fresh/id", set, r); got != "fresh/id" {
		t.Errorf("untouched id changed: %q", got)
	}
	if r.IDCollisions != 2 {
		t.Errorf("IDCollisions = %d, want 2", r.IDCollisions)
	}
}

// CRLF bodies (browser textarea edits) must still match the Relationships
// heading; otherwise every re-import appends a duplicate section.
func TestUpdateRelationshipsSectionCRLF(t *testing.T) {
	body := "intro\r\n\r\n# Relationships\r\n\r\n- Parent: [old](/old)\r\n\r\n# Notes\r\nkeep\r\n"
	got := updateRelationshipsSection(body, "# Relationships\n\n- Parent: [new](/new)")
	if strings.Count(got, "# Relationships") != 1 {
		t.Errorf("duplicate Relationships section:\n%s", got)
	}
	if !strings.Contains(got, "[new](/new)") || strings.Contains(got, "[old](/old)") {
		t.Errorf("section not replaced:\n%s", got)
	}
	if !strings.Contains(got, "# Notes") {
		t.Errorf("trailing section lost:\n%s", got)
	}
}

// Filtering by the "Untyped" bucket (as shown in the types dropdown) must
// match concepts whose frontmatter has no type.
func TestSearchUntypedFilter(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	// Write raw content: WriteConcept would default the type on serialize.
	path, _ := b.safePath("bare")
	if err := os.WriteFile(path, []byte("---\ntitle: bare\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := b.Search("", "Untyped", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "bare" {
		t.Errorf("Untyped filter = %+v, want the bare concept", got)
	}
}

// Search must support an exact tag facet alongside the text query and type.
func TestSearchTagFilter(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	if err := b.WriteConcept(Concept{ID: "a", Type: "T", Tags: []string{"pii", "env:prod"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteConcept(Concept{ID: "b", Type: "T"}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Search("", "", "pii")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("tag filter results = %+v, want only a", got)
	}
	all, _ := b.Search("", "", "")
	if len(all) != 2 {
		t.Errorf("no tag filter should return all, got %d", len(all))
	}
}

// WriteIndexes generates a reserved index.md per directory (and the root)
// listing child concepts; indexes never become concepts themselves.
func TestWriteIndexes(t *testing.T) {
	root := t.TempDir()
	b := NewOKFBundle(root)
	for _, c := range []Concept{
		{ID: "bigquery/p/ds", Type: "BigQuery Dataset", Title: "ds"},
		{ID: "bigquery/p/ds/tbl", Type: "BigQuery Table", Title: "tbl"},
		{ID: "top", Type: "Note", Title: "top"},
	} {
		if err := b.WriteConcept(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.WriteIndexes(); err != nil {
		t.Fatal(err)
	}

	rootIdx, err := os.ReadFile(filepath.Join(root, "index.md"))
	if err != nil {
		t.Fatalf("root index.md: %v", err)
	}
	if !strings.Contains(string(rootIdx), "[top](/top)") {
		t.Errorf("root index missing child link:\n%s", rootIdx)
	}

	dirIdx, err := os.ReadFile(filepath.Join(root, "bigquery/p/ds/index.md"))
	if err != nil {
		t.Fatalf("nested index.md: %v", err)
	}
	if !strings.Contains(string(dirIdx), "[tbl](/bigquery/p/ds/tbl)") {
		t.Errorf("nested index missing child link:\n%s", dirIdx)
	}

	concepts, err := b.ListConcepts()
	if err != nil {
		t.Fatal(err)
	}
	if len(concepts) != 3 {
		t.Errorf("indexes leaked into concepts: %d", len(concepts))
	}
}
