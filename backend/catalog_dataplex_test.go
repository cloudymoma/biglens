package main

import (
	"strings"
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

func TestFQNLineageMatch(t *testing.T) {
	idByFQN := map[string]string{
		"bigquery:proj.ds.orders": "bigquery/proj/ds/orders",
		"bigquery:proj.ds.users":  "bigquery/proj/ds/users",
	}

	// Add normalized keys as done in fetchLineage
	for id, fqn := range map[string]string{"bigquery/proj/ds/orders": "bigquery:proj.ds.orders", "bigquery/proj/ds/users": "bigquery:proj.ds.users"} {
		if norm := normalizeFQN(fqn); norm != "" {
			idByFQN[norm] = id
		}
	}

	tests := []struct {
		srcFQN    string
		wantID    string
		wantFound bool
	}{
		{"bigquery:proj.ds.orders", "bigquery/proj/ds/orders", true},
		{"bigquery:proj:ds.orders", "bigquery/proj/ds/orders", true},
		{"bigquery:`proj.ds.orders`", "bigquery/proj/ds/orders", true},
		{"bigquery:proj.ds.orders_20260719", "bigquery/proj/ds/orders", true},
		{"bigquery:proj.ds.orders@20260719", "bigquery/proj/ds/orders", true},
		{"bigquery:other_proj.ds.table", "", false},
	}

	for _, tt := range tests {
		gotID, found := matchFQN(tt.srcFQN, idByFQN)
		if found != tt.wantFound || gotID != tt.wantID {
			t.Errorf("matchFQN(%q) = (%q, %v), want (%q, %v)", tt.srcFQN, gotID, found, tt.wantID, tt.wantFound)
		}
	}
}

func TestOKFFQNFrontmatter(t *testing.T) {
	c := Concept{
		ID:          "bigquery/proj/ds/users",
		Type:        "BigQuery Table",
		Title:       "Users",
		Description: "User accounts",
		Resource:    "//bigquery.googleapis.com/projects/proj/datasets/ds/tables/users",
		FQN:         "bigquery:proj.ds.users",
		Body:        "# Overview\n\nUser table",
	}

	serialized := serializeConcept(c)
	bundle := NewOKFBundle(t.TempDir())
	parsed := bundle.parseContent(c.ID, serialized)

	if parsed.FQN != "bigquery:proj.ds.users" {
		t.Errorf("parsed.FQN = %q, want bigquery:proj.ds.users", parsed.FQN)
	}
}

func TestUpdateRelationshipsSection(t *testing.T) {
	existingBody := "# Overview\n\nUser custom overview\n\n# Schema\n\n- `id` (INT64)\n\n# Relationships\n\n- Parent: [old](/old)"
	newRel := "# Relationships\n\n- Parent: [new](/new)"

	got := updateRelationshipsSection(existingBody, newRel)
	want := "# Overview\n\nUser custom overview\n\n# Schema\n\n- `id` (INT64)\n\n# Relationships\n\n- Parent: [new](/new)"
	if got != want {
		t.Errorf("updateRelationshipsSection mismatch.\nGot:\n%s\n\nWant:\n%s", got, want)
	}

	// Appends when no relationships section existed
	existingNoRel := "# Overview\n\nUser custom overview"
	gotAppended := updateRelationshipsSection(existingNoRel, newRel)
	wantAppended := "# Overview\n\nUser custom overview\n\n# Relationships\n\n- Parent: [new](/new)"
	if gotAppended != wantAppended {
		t.Errorf("updateRelationshipsSection append mismatch.\nGot:\n%s\n\nWant:\n%s", gotAppended, wantAppended)
	}

	// User content AFTER the relationships section must survive the refresh.
	existingWithTail := "# Overview\n\nUser overview\n\n# Relationships\n\n- Parent: [old](/old)\n\n# Notes\n\nHand-written notes"
	gotTail := updateRelationshipsSection(existingWithTail, newRel)
	wantTail := "# Overview\n\nUser overview\n\n# Relationships\n\n- Parent: [new](/new)\n\n# Notes\n\nHand-written notes"
	if gotTail != wantTail {
		t.Errorf("updateRelationshipsSection tail-preserve mismatch.\nGot:\n%s\n\nWant:\n%s", gotTail, wantTail)
	}

	// A prose mention or deeper heading is not the section: nothing replaced,
	// fresh section appended at the end.
	existingProse := "# Overview\n\nSee the ## Relationships idea below\n\n## Relationships\n\nuser subsection"
	gotProse := updateRelationshipsSection(existingProse, newRel)
	wantProse := existingProse + "\n\n" + newRel
	if gotProse != wantProse {
		t.Errorf("updateRelationshipsSection prose/h2 mismatch.\nGot:\n%s\n\nWant:\n%s", gotProse, wantProse)
	}
}

func TestUserManagedProtectionAndPruning(t *testing.T) {
	dir := t.TempDir()
	bundle := NewOKFBundle(dir)

	// 1. Untouched imported concept (will be refreshed)
	_ = bundle.WriteConcept(Concept{
		ID:          "bigquery/proj/ds/users",
		Type:        "BigQuery Table",
		Title:       "Users",
		UserManaged: false,
		Body:        "# Overview\n\nOld overview",
	})

	// 2. User-managed concept (body + annotations will be preserved)
	_ = bundle.WriteConcept(Concept{
		ID:          "bigquery/proj/ds/orders",
		Type:        "BigQuery Table",
		Title:       "Orders",
		Description: "User-edited description",
		Tags:        []string{"pii", "core"},
		UserManaged: true,
		Body:        "# Overview\n\nUser custom overview for orders",
	})

	// 3. Stale concept (not in new import set, UserManaged=false -> will be pruned)
	_ = bundle.WriteConcept(Concept{
		ID:          "bigquery/proj/ds/stale",
		Type:        "BigQuery Table",
		Title:       "Stale",
		UserManaged: false,
		Body:        "Stale content",
	})

	// 4. Hand-authored user concept (not in import set, UserManaged=true -> MUST NOT be pruned)
	_ = bundle.WriteConcept(Concept{
		ID:          "notes/my_custom_note",
		Type:        "Note",
		Title:       "My Note",
		UserManaged: true,
		Body:        "Hand-authored note",
	})

	// Perform simulated import processing over the bundle
	existingConcepts, err := bundle.ListConcepts()
	if err != nil {
		t.Fatalf("ListConcepts: %v", err)
	}
	existingByID := make(map[string]Concept, len(existingConcepts))
	for _, x := range existingConcepts {
		existingByID[x.ID] = x
	}

	importedItems := []Concept{
		{
			ID:          "bigquery/proj/ds/users",
			Type:        "BigQuery Table",
			Title:       "Users",
			Description: "Fresh description",
			Body:        "# Overview\n\nFresh overview\n\n# Relationships\n\n- Parent: [bigquery/proj/ds](/bigquery/proj/ds)",
		},
		{
			ID:          "bigquery/proj/ds/orders",
			Type:        "BigQuery Table",
			Title:       "Orders",
			Description: "Fresh orders description",
			Body:        "# Overview\n\nFresh overview\n\n# Relationships\n\n- Parent: [bigquery/proj/ds](/bigquery/proj/ds)",
		},
	}

	result := &ImportResult{TypeCounts: map[string]int{}}
	importedSet := make(map[string]bool)

	for _, item := range importedItems {
		importedSet[item.ID] = true
		fc := item
		relBody := buildRelationshipBody("bigquery/proj/ds", nil)

		if existing, ok := existingByID[fc.ID]; ok && existing.UserManaged {
			// Mirrors Import: keep body + annotations, refresh identity metadata.
			fc.UserManaged = true
			fc.Description = existing.Description
			fc.Tags = existing.Tags
			fc.Timestamp = existing.Timestamp
			fc.Body = updateRelationshipsSection(existing.Body, relBody)
			result.Preserved++
		}
		_ = bundle.WriteConcept(fc)
		result.Imported++
	}

	for _, existing := range existingConcepts {
		if !importedSet[existing.ID] && !existing.UserManaged {
			if err := bundle.DeleteConcept(existing.ID); err == nil {
				result.Pruned++
			}
		}
	}

	if result.Preserved != 1 {
		t.Errorf("result.Preserved = %d, want 1", result.Preserved)
	}
	if result.Pruned != 1 {
		t.Errorf("result.Pruned = %d, want 1", result.Pruned)
	}

	// Verify user-managed concept body survived
	ordersDetail, err := bundle.GetConcept("bigquery/proj/ds/orders")
	if err != nil {
		t.Fatalf("GetConcept orders: %v", err)
	}
	if !strings.Contains(ordersDetail.Concept.Body, "User custom overview for orders") {
		t.Errorf("orders body did not preserve user edits. Got:\n%s", ordersDetail.Concept.Body)
	}
	if ordersDetail.Concept.Description != "User-edited description" {
		t.Errorf("orders description not preserved: %q", ordersDetail.Concept.Description)
	}
	if len(ordersDetail.Concept.Tags) != 2 || ordersDetail.Concept.Tags[0] != "pii" {
		t.Errorf("orders tags not preserved: %v", ordersDetail.Concept.Tags)
	}

	// Verify stale concept was pruned
	if _, err := bundle.GetConcept("bigquery/proj/ds/stale"); err == nil {
		t.Errorf("expected stale concept to be pruned, but it still exists")
	}

	// Verify hand-authored note was NOT pruned
	noteDetail, err := bundle.GetConcept("notes/my_custom_note")
	if err != nil {
		t.Errorf("hand-authored note was incorrectly pruned: %v", err)
	} else if strings.TrimSpace(noteDetail.Concept.Body) != "Hand-authored note" {
		t.Errorf("hand-authored note body altered: %q", noteDetail.Concept.Body)
	}
}
