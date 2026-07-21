package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func testManifest() Manifest {
	return Manifest{
		Project:         "du-hast-mich",
		Location:        "global",
		Query:           "type=TABLE",
		LineageLocation: "us-central1",
		ImportedAt:      "2026-07-21T10:00:00Z",
		Truncated:       true,
		EntryTypeCounts: map[string]int{"BigQuery Table": 12, "BigQuery Dataset": 3},
	}
}

// The manifest must survive a write/read round-trip so a recorded import
// scope can be reproduced after a server restart.
func TestManifestRoundTrip(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	want := testManifest()

	if err := b.WriteManifest(want); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := b.ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", *got, want)
	}
}

// Reading a manifest from a bundle that never imported must report
// os.ErrNotExist so callers can distinguish "never imported" from real errors.
func TestReadManifestMissing(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	if _, err := b.ReadManifest(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadManifest on empty bundle: got %v, want os.ErrNotExist", err)
	}
}

// The manifest is bundle metadata, not knowledge: it must never surface as a
// concept in the graph or search.
func TestManifestIsNotAConcept(t *testing.T) {
	b := NewOKFBundle(t.TempDir())
	if err := b.WriteManifest(testManifest()); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := b.WriteConcept(Concept{ID: "bigquery/p/ds", Type: "BigQuery Dataset", Title: "ds"}); err != nil {
		t.Fatalf("WriteConcept: %v", err)
	}

	concepts, err := b.ListConcepts()
	if err != nil {
		t.Fatalf("ListConcepts: %v", err)
	}
	if len(concepts) != 1 || concepts[0].ID != "bigquery/p/ds" {
		t.Errorf("ListConcepts sees the manifest: %+v", concepts)
	}

	g, err := b.BuildGraph()
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("BuildGraph includes the manifest: %+v", g.Nodes)
	}
}

// Refresh must re-run the import recorded in the manifest — not whatever
// query happens to be in the request.
func TestImportQueryFor(t *testing.T) {
	withManifest := NewOKFBundle(t.TempDir())
	if err := withManifest.WriteManifest(testManifest()); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	empty := NewOKFBundle(t.TempDir())

	tests := []struct {
		name    string
		url     string
		bundle  *OKFBundle
		want    string
		wantErr bool
	}{
		{"plain import uses q param", "/api/catalog/import?q=foo", withManifest, "foo", false},
		{"refresh uses recorded query", "/api/catalog/import?refresh=1&q=ignored", withManifest, "type=TABLE", false},
		{"refresh without manifest fails", "/api/catalog/import?refresh=1", empty, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tt.url, nil)
			got, err := importQueryFor(r, tt.bundle)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("importQueryFor: %v", err)
			}
			if got != tt.want {
				t.Errorf("importQueryFor = %q, want %q", got, tt.want)
			}
		})
	}
}
