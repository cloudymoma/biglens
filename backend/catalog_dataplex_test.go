package main

import (
	"testing"

	"cloud.google.com/go/dataplex/apiv1/dataplexpb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestPrettyEntryType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"projects/p/locations/global/entryTypes/bigquery-table", "BigQuery Table"},
		{"projects/p/locations/global/entryTypes/bigquery-view", "BigQuery View"},
		{"projects/p/locations/global/entryTypes/glossary-term", "Glossary Term"},
		{"projects/p/locations/global/entryTypes/some-custom-type", "Some Custom Type"},
		{"bigquery-dataset", "BigQuery Dataset"},
		{"", "Untyped"},
	}
	for _, tt := range tests {
		if got := prettyEntryType(tt.in); got != tt.want {
			t.Errorf("prettyEntryType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEntryConceptID(t *testing.T) {
	e := &dataplexpb.Entry{FullyQualifiedName: "bigquery:proj.ds.users"}
	if got := entryConceptID(e); got != "bigquery/proj/ds/users" {
		t.Errorf("entryConceptID = %q, want bigquery/proj/ds/users", got)
	}
	// Falls back to resource name when FQN is empty.
	e2 := &dataplexpb.Entry{Name: "projects/p/locations/global/entryGroups/g/entries/x"}
	if got := entryConceptID(e2); got == "" {
		t.Error("entryConceptID should not be empty for resource-name fallback")
	}
}

func TestEntryBaseConcept(t *testing.T) {
	child := &dataplexpb.Entry{
		Name:               "projects/p/locations/global/entryGroups/@bigquery/entries/table",
		FullyQualifiedName: "bigquery:proj.ds.users",
		EntryType:          "projects/p/locations/global/entryTypes/bigquery-table",
		EntrySource: &dataplexpb.EntrySource{
			DisplayName: "Users",
			Description: "User accounts",
			Resource:    "//bigquery.googleapis.com/projects/proj/datasets/ds/tables/users",
		},
	}
	c := entryBaseConcept(child)
	if c.ID != "bigquery/proj/ds/users" {
		t.Errorf("id = %q", c.ID)
	}
	if c.Type != "BigQuery Table" {
		t.Errorf("type = %q", c.Type)
	}
	if c.Title != "Users" || c.Description != "User accounts" {
		t.Errorf("title/desc = %q / %q", c.Title, c.Description)
	}
}

func TestContainmentParent(t *testing.T) {
	// dataset and table both imported -> table's parent is the dataset.
	set := map[string]bool{
		"bigquery/proj/ds":       true,
		"bigquery/proj/ds/users": true,
	}
	if got := longestPrefixIn("bigquery/proj/ds/users", set); got != "bigquery/proj/ds" {
		t.Errorf("containment parent = %q, want bigquery/proj/ds", got)
	}
	// no ancestor present -> no parent.
	if got := longestPrefixIn("bigquery/proj/ds", map[string]bool{"bigquery/proj/ds": true}); got != "" {
		t.Errorf("expected no parent, got %q", got)
	}
}

func TestSlugifyFQNMatchesConceptID(t *testing.T) {
	// A lineage source FQN must slugify to the same id used for the concept,
	// so lineage edges can be matched against imported nodes.
	e := &dataplexpb.Entry{FullyQualifiedName: "bigquery:proj.ds.orders"}
	if slugifyFQN("bigquery:proj.ds.orders") != entryConceptID(e) {
		t.Error("slugifyFQN and entryConceptID disagree on the same FQN")
	}
}

func TestBuildRelationshipBody(t *testing.T) {
	body := buildRelationshipBody("bigquery/proj/ds", []string{"bigquery/proj/ds/orders"})
	links := extractLinks(body, "bigquery/proj/ds/revenue")
	// containment parent + one lineage source = 2 edges
	if len(links) != 2 {
		t.Fatalf("links = %v, want 2", links)
	}
	if buildRelationshipBody("", nil) != "" {
		t.Error("empty relationships should produce empty body")
	}
}

func TestBuildConceptBody(t *testing.T) {
	overviewStruct, err := structpb.NewStruct(map[string]interface{}{
		"content": "This table stores user profile info.",
	})
	if err != nil {
		t.Fatalf("NewStruct overview: %v", err)
	}

	// "dataType" is what the real dataplex-types schema aspect emits;
	// "profile" uses legacy "type" to cover the fallback.
	schemaStruct, err := structpb.NewStruct(map[string]interface{}{
		"fields": []interface{}{
			map[string]interface{}{
				"name":        "user_id",
				"dataType":    "INT64",
				"mode":        "REQUIRED",
				"description": "Unique ID of user",
			},
			map[string]interface{}{
				"name": "profile",
				"type": "RECORD",
				"mode": "NULLABLE",
				"fields": []interface{}{
					map[string]interface{}{
						"name":        "email",
						"dataType":    "STRING",
						"mode":        "NULLABLE",
						"description": "User email address",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewStruct schema: %v", err)
	}

	entry := &dataplexpb.Entry{
		Aspects: map[string]*dataplexpb.Aspect{
			"dataplex-types.global.overview": {
				AspectType: "projects/p/locations/global/aspectTypes/overview",
				Data:       overviewStruct,
			},
			"dataplex-types.global.schema": {
				AspectType: "projects/p/locations/global/aspectTypes/schema",
				Data:       schemaStruct,
			},
		},
	}

	body := buildConceptBody(entry, "bigquery/proj/ds", []string{"bigquery/proj/ds/raw_users"})

	expected := `# Overview

This table stores user profile info.

# Schema

- ` + "`user_id`" + ` (INT64, REQUIRED): Unique ID of user
- ` + "`profile`" + ` (RECORD, NULLABLE)
  - ` + "`email`" + ` (STRING, NULLABLE): User email address

# Relationships

- Parent: [bigquery/proj/ds](/bigquery/proj/ds)
- Derived from: [bigquery/proj/ds/raw_users](/bigquery/proj/ds/raw_users)`

	if body != expected {
		t.Errorf("buildConceptBody mismatch.\nGot:\n%s\n\nWant:\n%s", body, expected)
	}
}

