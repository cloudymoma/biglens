package main

import (
	"reflect"
	"testing"
)

func TestClassifyGrantee(t *testing.T) {
	tests := []struct {
		in   string
		kind GranteeKind
		id   string
	}{
		{"user:alice@example.com", KindUser, "alice@example.com"},
		{"serviceAccount:sa@p.iam.gserviceaccount.com", KindServiceAccount, "sa@p.iam.gserviceaccount.com"},
		{"group:analysts@example.com", KindGroup, "analysts@example.com"},
		{"domain:example.com", KindDomain, "example.com"},
		{"specialGroup:projectReaders", KindSpecial, "projectReaders"},
		{"allUsers", KindPublic, "allUsers"},
		{"allAuthenticatedUsers", KindPublic, "allAuthenticatedUsers"},
		{"iamMember:deleted:user:x", KindSpecial, "deleted:user:x"},
	}
	for _, tt := range tests {
		kind, id := classifyGrantee(tt.in)
		if kind != tt.kind || id != tt.id {
			t.Errorf("classifyGrantee(%q) = %v,%q want %v,%q", tt.in, kind, id, tt.kind, tt.id)
		}
	}
}

func TestIsWriteRole(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"roles/bigquery.dataViewer", false},
		{"roles/bigquery.dataEditor", true},
		{"roles/bigquery.dataOwner", true},
		{"roles/bigquery.admin", true},
		{"WRITER", true},
		{"OWNER", true},
		{"READER", false},
	}
	for _, tt := range tests {
		if got := isWriteRole(tt.role); got != tt.want {
			t.Errorf("isWriteRole(%q) = %v want %v", tt.role, got, tt.want)
		}
	}
}

func TestFilterProjectBindings(t *testing.T) {
	in := map[string][]string{
		"roles/bigquery.admin":                        {"user:a@x.com"},
		"roles/dataplex.catalogViewer":                {"group:g@x.com"},
		"roles/datacatalog.categoryFineGrainedReader": {"user:b@x.com"},
		"roles/compute.admin":                         {"user:evil@x.com"},
		"roles/owner":                                 {"user:c@x.com"},
	}
	out := filterProjectBindings(in)
	roles := map[string]ProjectBinding{}
	for _, b := range out {
		roles[b.Role] = b
	}
	if _, ok := roles["roles/compute.admin"]; ok {
		t.Fatal("compute.admin must be filtered out (BigQuery/Catalog scope only)")
	}
	for _, want := range []string{"roles/bigquery.admin", "roles/dataplex.catalogViewer", "roles/datacatalog.categoryFineGrainedReader", "roles/owner"} {
		if _, ok := roles[want]; !ok {
			t.Fatalf("missing whitelisted role %s", want)
		}
	}
	if !roles["roles/owner"].Basic {
		t.Error("roles/owner must be flagged Basic")
	}
	if roles["roles/bigquery.admin"].Basic {
		t.Error("bigquery.admin must not be flagged Basic")
	}
}

func TestComputeUnusedGrants(t *testing.T) {
	principals := []PrincipalGrant{
		{Principal: "used@x.com", Kind: KindUser},
		{Principal: "idle@x.com", Kind: KindUser},
		{Principal: "sa@p.iam.gserviceaccount.com", Kind: KindServiceAccount},
		{Principal: "analysts@x.com", Kind: KindGroup},
	}
	active := map[string]bool{"used@x.com": true}
	got := computeUnusedGrants(principals, active)
	var names []string
	for _, p := range got {
		names = append(names, p.Principal)
	}
	want := []string{"idle@x.com", "sa@p.iam.gserviceaccount.com"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("unused = %v want %v (groups excluded, active excluded)", names, want)
	}
}
