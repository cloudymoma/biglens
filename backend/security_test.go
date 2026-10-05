package main

import (
	"reflect"
	"strings"
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
		{"projectOwner:mycompany", KindProjectRole, "projectOwner:mycompany"},
		{"projectEditor:mycompany", KindProjectRole, "projectEditor:mycompany"},
		{"projectViewer:mycompany", KindProjectRole, "projectViewer:mycompany"},
		{"specialGroup:projectReaders", KindProjectRole, "projectReaders"},
		{"specialGroup:allAuthenticatedUsers", KindPublic, "allAuthenticatedUsers"},
		{"allUsers", KindPublic, "allUsers"},
		{"allAuthenticatedUsers", KindPublic, "allAuthenticatedUsers"},
		{"iamMember:deleted:user:x", KindDeleted, "deleted:user:x"},
	}
	for _, tt := range tests {
		kind, id := classifyGrantee(tt.in)
		if kind != tt.kind || id != tt.id {
			t.Errorf("classifyGrantee(%q) = %v,%q want %v,%q", tt.in, kind, id, tt.kind, tt.id)
		}
	}
}

func TestPublicFlagsExcludesDefaultProjectRoles(t *testing.T) {
	grants := []ObjectGrant{
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataOwner", Grantee: "projectOwner:my-proj"},
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataEditor", Grantee: "projectEditor:my-proj"},
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataViewer", Grantee: "projectViewer:my-proj"},
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataViewer", Grantee: "specialGroup:projectReaders"},
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataViewer", Grantee: "allAuthenticatedUsers"},
		{Dataset: "ds1", ObjectType: "SCHEMA", Role: "roles/bigquery.dataViewer", Grantee: "domain:example.com"},
	}
	flags := publicFlags(grants)
	if len(flags) != 2 {
		t.Fatalf("publicFlags = %+v, want 2 flags (allAuthenticatedUsers and domain:example.com)", flags)
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
		{"roles/owner_withcond_abc123", true},
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
		"roles/owner_withcond_deadbeef":               {"user:c@x.com"},
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
		{Principal: "Used@x.com", Kind: KindUser, Datasets: []string{"ds1"}, Roles: []string{"roles/bigquery.dataViewer"}},
		{Principal: "idle@x.com", Kind: KindUser, Datasets: []string{"ds1"}, Roles: []string{"roles/bigquery.dataViewer"}},
		{Principal: "sa@p.iam.gserviceaccount.com", Kind: KindServiceAccount, Datasets: []string{"ds1"}, Roles: []string{"roles/bigquery.dataEditor"}, WriteCapable: true},
		{Principal: "analysts@x.com", Kind: KindGroup, Datasets: []string{"ds1"}},
	}
	bindings := []ProjectBinding{
		{Role: "roles/bigquery.admin", Members: []string{"user:proj_idle@x.com"}},
	}
	merged := mergeProjectPrincipals(principals, bindings)
	active := map[string]bool{"used@x.com": true}
	got := computeUnusedGrants(merged, active)
	var names []string
	for _, p := range got {
		names = append(names, p.Principal)
	}
	want := []string{"idle@x.com", "proj_idle@x.com", "sa@p.iam.gserviceaccount.com"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("unused = %v want %v (groups excluded, active excluded case-insensitively, project role holders included)", names, want)
	}
}

func TestSensitiveColumnsSQL(t *testing.T) {
	sql := sensitiveColumnsSQL("`p`.`region-us`")
	for _, want := range []string{
		"COUNTIF(NOT tagged) OVER () AS untagged_total",
		"WHERE t.table_type IN ('BASE TABLE', 'CLONE', 'SNAPSHOT')",
		"AND c.data_type NOT LIKE 'STRUCT%'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sensitiveColumnsSQL missing %q in:\n%s", want, sql)
		}
	}
}

