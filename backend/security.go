package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/bigquery"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
)

var datasetNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type GranteeKind string

const (
	KindUser           GranteeKind = "user"
	KindServiceAccount GranteeKind = "serviceAccount"
	KindGroup          GranteeKind = "group"
	KindDomain         GranteeKind = "domain"
	KindProjectRole    GranteeKind = "projectRole"
	KindDeleted        GranteeKind = "deleted"
	KindSpecial        GranteeKind = "special"
	KindPublic         GranteeKind = "public"
)

type ProjectBinding struct {
	Role    string   `json:"role"`
	Basic   bool     `json:"basic"`
	Members []string `json:"members"`
}

type PrincipalGrant struct {
	Principal    string      `json:"principal"`
	Kind         GranteeKind `json:"kind"`
	Datasets     []string    `json:"datasets"`
	Roles        []string    `json:"roles"`
	WriteCapable bool        `json:"write_capable"`
}

func classifyGrantee(g string) (GranteeKind, string) {
	trimmed := strings.TrimPrefix(g, "iamMember:")
	if trimmed == "allUsers" || trimmed == "allAuthenticatedUsers" {
		return KindPublic, trimmed
	}
	prefix, rest, found := strings.Cut(trimmed, ":")
	if !found {
		return KindSpecial, trimmed
	}
	switch prefix {
	case "user":
		return KindUser, rest
	case "serviceAccount":
		return KindServiceAccount, rest
	case "group":
		return KindGroup, rest
	case "domain":
		return KindDomain, rest
	case "projectOwner", "projectEditor", "projectViewer":
		return KindProjectRole, trimmed
	case "deleted":
		return KindDeleted, trimmed
	case "specialGroup":
		switch rest {
		case "allUsers", "allAuthenticatedUsers":
			return KindPublic, rest
		case "projectOwners", "projectWriters", "projectReaders":
			return KindProjectRole, rest
		default:
			return KindSpecial, rest
		}
	default:
		return KindSpecial, rest
	}
}

func isWriteRole(role string) bool {
	role = canonicalRoleName(role)
	switch role {
	case "WRITER", "OWNER":
		return true
	}
	for _, suffix := range []string{".dataEditor", ".dataOwner", ".admin"} {
		if strings.HasSuffix(role, suffix) {
			return true
		}
	}
	return role == "roles/owner" || role == "roles/editor"
}

func canonicalRoleName(role string) string {
	if idx := strings.Index(role, "_withcond_"); idx >= 0 {
		return role[:idx]
	}
	return role
}

// filterProjectBindings keeps only BigQuery / Knowledge Catalog relevant
// roles; everything else in the project policy never reaches the frontend.
func filterProjectBindings(bindings map[string][]string) []ProjectBinding {
	merged := map[string]map[string]bool{}
	for rawRole, members := range bindings {
		role := canonicalRoleName(rawRole)
		basic := role == "roles/owner" || role == "roles/editor" || role == "roles/viewer"
		if !basic &&
			!strings.HasPrefix(role, "roles/bigquery.") &&
			!strings.HasPrefix(role, "roles/dataplex.") &&
			!strings.HasPrefix(role, "roles/datacatalog.") {
			continue
		}
		if merged[role] == nil {
			merged[role] = map[string]bool{}
		}
		for _, m := range members {
			merged[role][m] = true
		}
	}
	var out []ProjectBinding
	for role, mset := range merged {
		basic := role == "roles/owner" || role == "roles/editor" || role == "roles/viewer"
		out = append(out, ProjectBinding{Role: role, Basic: basic, Members: sortedKeys(mset)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// mergeProjectPrincipals combines dataset-level principal grants with
// project-level user/serviceAccount BigQuery/Catalog role bindings so
// project-level role holders are also evaluated for unused grants.
func mergeProjectPrincipals(datasetPrincipals []PrincipalGrant, projectBindings []ProjectBinding) []PrincipalGrant {
	type agg struct {
		display  string
		kind     GranteeKind
		datasets map[string]bool
		roles    map[string]bool
		write    bool
	}
	byKey := map[string]*agg{}
	ensure := func(principal string, kind GranteeKind) *agg {
		key := strings.ToLower(principal)
		a, ok := byKey[key]
		if !ok {
			a = &agg{display: principal, kind: kind, datasets: map[string]bool{}, roles: map[string]bool{}}
			byKey[key] = a
		}
		return a
	}
	for _, p := range datasetPrincipals {
		a := ensure(p.Principal, p.Kind)
		for _, ds := range p.Datasets {
			a.datasets[ds] = true
		}
		for _, r := range p.Roles {
			a.roles[r] = true
		}
		if p.WriteCapable {
			a.write = true
		}
	}
	for _, pb := range projectBindings {
		for _, m := range pb.Members {
			kind, id := classifyGrantee(m)
			if kind != KindUser && kind != KindServiceAccount {
				continue
			}
			a := ensure(id, kind)
			a.datasets["(project)"] = true
			a.roles[pb.Role] = true
			if isWriteRole(pb.Role) {
				a.write = true
			}
		}
	}
	out := make([]PrincipalGrant, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, PrincipalGrant{
			Principal:    a.display,
			Kind:         a.kind,
			Datasets:     sortedKeys(a.datasets),
			Roles:        sortedKeys(a.roles),
			WriteCapable: a.write,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Principal < out[j].Principal })
	return out
}

// computeUnusedGrants returns users/service accounts holding grants but with
// no jobs in the window. Groups/domains are excluded: membership is opaque.
func computeUnusedGrants(principals []PrincipalGrant, active map[string]bool) []PrincipalGrant {
	var out []PrincipalGrant
	for _, p := range principals {
		if p.Kind != KindUser && p.Kind != KindServiceAccount {
			continue
		}
		if !active[p.Principal] && !active[strings.ToLower(p.Principal)] {
			out = append(out, p)
		}
	}
	return out
}

const maxPostureDatasets = 50

func datasetNamesFromPosture(posture []DatasetPosture) ([]string, int) {
	total := len(posture)
	limit := total
	if limit > maxPostureDatasets {
		limit = maxPostureDatasets
	}
	names := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		names = append(names, posture[i].Dataset)
	}
	return names, total
}

func (b *BQClient) GetDatasetNames(ctx context.Context, region string) ([]string, int, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT schema_name FROM %s.INFORMATION_SCHEMA.SCHEMATA
		 WHERE NOT STARTS_WITH(schema_name, '_') ORDER BY schema_name`,
		b.regionRef(region)))
	type row struct {
		SchemaName string `bigquery:"schema_name"`
	}
	rows, err := collectRows[row](q, ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list datasets for posture failed: %w", err)
	}
	total := len(rows)
	if total > maxPostureDatasets {
		rows = rows[:maxPostureDatasets]
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.SchemaName)
	}
	return names, total, nil
}

type ObjectGrant struct {
	Dataset    string `json:"dataset" bigquery:"object_name"`
	ObjectType string `json:"object_type" bigquery:"object_type"`
	Role       string `json:"role" bigquery:"privilege_type"`
	Grantee    string `json:"grantee" bigquery:"grantee"`
}

func extractGrantsFromDatasetMetadata(ds string, md *bigquery.DatasetMetadata) []ObjectGrant {
	if md == nil {
		return nil
	}
	var out []ObjectGrant
	for _, ae := range md.Access {
		if ae == nil || ae.Role == "" {
			continue
		}
		role := string(ae.Role)
		switch ae.Role {
		case bigquery.OwnerRole:
			role = "roles/bigquery.dataOwner"
		case bigquery.WriterRole:
			role = "roles/bigquery.dataEditor"
		case bigquery.ReaderRole:
			role = "roles/bigquery.dataViewer"
		}
		var grantee string
		switch ae.EntityType {
		case bigquery.UserEmailEntity:
			if strings.HasSuffix(strings.ToLower(ae.Entity), ".gserviceaccount.com") {
				grantee = "serviceAccount:" + ae.Entity
			} else {
				grantee = "user:" + ae.Entity
			}
		case bigquery.GroupEmailEntity:
			grantee = "group:" + ae.Entity
		case bigquery.DomainEntity:
			grantee = "domain:" + ae.Entity
		case bigquery.SpecialGroupEntity:
			if ae.Entity == "allAuthenticatedUsers" || ae.Entity == "allUsers" {
				grantee = ae.Entity
			} else {
				grantee = "specialGroup:" + ae.Entity
			}
		case bigquery.IAMMemberEntity:
			grantee = ae.Entity
		default:
			continue
		}
		out = append(out, ObjectGrant{
			Dataset:    ds,
			ObjectType: "SCHEMA",
			Role:       role,
			Grantee:    grantee,
		})
	}
	return out
}

// GetObjectGrants reads dataset ACLs via the free Dataset.Metadata REST API
// (datasets.get) with bounded concurrency, falling back to OBJECT_PRIVILEGES
// if needed, and returns both the collected grants and the number of datasets
// whose ACLs could not be evaluated.
func (b *BQClient) GetObjectGrants(ctx context.Context, region string, datasets []string) ([]ObjectGrant, int) {
	var (
		mu     sync.Mutex
		grants []ObjectGrant
		failed int
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 8)
	)
	for _, ds := range datasets {
		wg.Add(1)
		go func(ds string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if !datasetNameRe.MatchString(ds) {
				slog.Warn("invalid dataset name skipped", "dataset", ds)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}

			if md, err := b.client.Dataset(ds).Metadata(ctx); err == nil {
				g := extractGrantsFromDatasetMetadata(ds, md)
				mu.Lock()
				grants = append(grants, g...)
				mu.Unlock()
				return
			}

			gq := b.client.Query(fmt.Sprintf(
				`SELECT object_name, object_type, privilege_type, grantee
				 FROM %s.INFORMATION_SCHEMA.OBJECT_PRIVILEGES
				 WHERE object_name = @ds`, b.regionRef(region)))
			gq.Parameters = []bigquery.QueryParameter{{Name: "ds", Value: ds}}
			g, err := collectRows[ObjectGrant](gq, ctx)
			if err != nil {
				slog.Warn("object privileges skipped", "dataset", ds, "error", err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}

			mu.Lock()
			grants = append(grants, g...)
			mu.Unlock()
		}(ds)
	}
	wg.Wait()
	return grants, failed
}

type PublicFlag struct {
	Dataset    string      `json:"dataset"`
	ObjectType string      `json:"object_type"`
	Role       string      `json:"role"`
	Grantee    string      `json:"grantee"`
	Kind       GranteeKind `json:"kind"`
}

// publicFlags surfaces grants that widen access beyond named principals:
// public internet, whole domains, and legacy special groups (excluding
// standard project-role convenience groups projectOwners/Writers/Readers).
func publicFlags(grants []ObjectGrant) []PublicFlag {
	var out []PublicFlag
	for _, g := range grants {
		kind, _ := classifyGrantee(g.Grantee)
		if kind == KindPublic || kind == KindDomain || kind == KindSpecial {
			out = append(out, PublicFlag{Dataset: g.Dataset, ObjectType: g.ObjectType, Role: g.Role, Grantee: g.Grantee, Kind: kind})
		}
	}
	return out
}

func buildPrincipalGrants(grants []ObjectGrant) []PrincipalGrant {
	type agg struct {
		kind     GranteeKind
		datasets map[string]bool
		roles    map[string]bool
		write    bool
	}
	byID := map[string]*agg{}
	for _, g := range grants {
		kind, id := classifyGrantee(g.Grantee)
		if kind == KindPublic || kind == KindSpecial || kind == KindProjectRole || kind == KindDeleted {
			continue
		}
		a, ok := byID[id]
		if !ok {
			a = &agg{kind: kind, datasets: map[string]bool{}, roles: map[string]bool{}}
			byID[id] = a
		}
		a.datasets[g.Dataset] = true
		a.roles[g.Role] = true
		if isWriteRole(g.Role) {
			a.write = true
		}
	}
	out := make([]PrincipalGrant, 0, len(byID))
	for id, a := range byID {
		out = append(out, PrincipalGrant{
			Principal: id, Kind: a.kind,
			Datasets: sortedKeys(a.datasets), Roles: sortedKeys(a.roles),
			WriteCapable: a.write,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Principal < out[j].Principal })
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GetProjectBindings reads the project IAM policy once (requesting v3 so
// conditional bindings keep their canonical role names) and whitelists
// BigQuery/Catalog roles. Second return: holders of
// roles/datacatalog.categoryFineGrainedReader (can read policy-tagged columns).
func (b *BQClient) GetProjectBindings(ctx context.Context) ([]ProjectBinding, []string, error) {
	c, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("resource manager client: %w", err)
	}
	defer c.Close()
	policy, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
		Resource: "projects/" + b.config.BigQuery.ProjectID,
		Options:  &iampb.GetPolicyOptions{RequestedPolicyVersion: 3},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("get project iam policy: %w", err)
	}
	raw := map[string][]string{}
	for _, binding := range policy.Bindings {
		raw[binding.Role] = append(raw[binding.Role], binding.Members...)
	}
	bindings := filterProjectBindings(raw)
	var bypassers []string
	for _, bd := range bindings {
		if bd.Role == "roles/datacatalog.categoryFineGrainedReader" {
			bypassers = append(bypassers, bd.Members...)
		}
	}
	return bindings, bypassers, nil
}

type DatasetPosture struct {
	Dataset        string  `json:"dataset" bigquery:"schema_name"`
	KMSKey         string  `json:"kms_key" bigquery:"kms_key"`
	CMEK           bool    `json:"cmek" bigquery:"cmek"`
	DefaultExpDays float64 `json:"default_exp_days" bigquery:"default_exp_days"`
}

func (b *BQClient) GetDatasetPosture(ctx context.Context, region string) ([]DatasetPosture, error) {
	ref := b.regionRef(region)
	q := b.client.Query(fmt.Sprintf(
		`SELECT s.schema_name,
			IFNULL(MAX(IF(o.option_name = 'default_kms_key_name', o.option_value, NULL)), '') AS kms_key,
			IFNULL(LOGICAL_OR(o.option_name = 'default_kms_key_name'), FALSE) AS cmek,
			IFNULL(MAX(IF(o.option_name = 'default_table_expiration_days', SAFE_CAST(o.option_value AS FLOAT64), NULL)), 0) AS default_exp_days
		FROM %s.INFORMATION_SCHEMA.SCHEMATA s
		LEFT JOIN %s.INFORMATION_SCHEMA.SCHEMATA_OPTIONS o
			ON s.catalog_name = o.catalog_name AND s.schema_name = o.schema_name
		WHERE NOT STARTS_WITH(s.schema_name, '_')
		GROUP BY s.schema_name ORDER BY s.schema_name`, ref, ref))
	return collectRows[DatasetPosture](q, ctx)
}

const sensitiveColRegex = `(^|[._])(ssn|social_security|passport|tax_id|e?mail|phone|dob|birth_?date|salary|income|credit_card|iban|swift|password|secret|api_key|auth_token|access_token)($|[._])`

type SensitiveColumn struct {
	Dataset       string `json:"dataset" bigquery:"table_schema"`
	Table         string `json:"table" bigquery:"table_name"`
	Column        string `json:"column" bigquery:"field_path"`
	Tagged        bool   `json:"tagged" bigquery:"tagged"`
	UntaggedTotal int64  `json:"-" bigquery:"untagged_total"`
}

func sensitiveColumnsSQL(regionRef string) string {
	return fmt.Sprintf(
		`SELECT
			table_schema,
			table_name,
			field_path,
			tagged,
			COUNTIF(NOT tagged) OVER () AS untagged_total
		FROM (
			SELECT
				c.table_schema,
				c.table_name,
				c.field_path,
				ARRAY_LENGTH(IFNULL(c.policy_tags, [])) > 0 AS tagged
			FROM %s.INFORMATION_SCHEMA.COLUMN_FIELD_PATHS c
			JOIN %s.INFORMATION_SCHEMA.TABLES t
				USING (table_catalog, table_schema, table_name)
			WHERE t.table_type IN ('BASE TABLE', 'CLONE', 'SNAPSHOT')
				AND NOT STARTS_WITH(c.table_schema, '_')
				AND c.data_type NOT LIKE 'STRUCT%%'
				AND REGEXP_CONTAINS(LOWER(c.field_path), @pattern)
		)
		ORDER BY tagged, table_schema, table_name
		LIMIT 200`, regionRef, regionRef)
}

func (b *BQClient) GetSensitiveColumns(ctx context.Context, region string) ([]SensitiveColumn, int64, error) {
	q := b.client.Query(sensitiveColumnsSQL(b.regionRef(region)))
	q.Parameters = []bigquery.QueryParameter{{Name: "pattern", Value: sensitiveColRegex}}
	rows, err := collectRows[SensitiveColumn](q, ctx)
	if err != nil {
		return nil, 0, err
	}
	var untaggedTotal int64
	if len(rows) > 0 {
		untaggedTotal = rows[0].UntaggedTotal
	}
	return rows, untaggedTotal, nil
}

func (b *BQClient) GetActivePrincipals(ctx context.Context, region, timeRange string) (map[string]bool, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT DISTINCT LOWER(user_email) AS user_email
		 FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		 WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)
		   AND user_email IS NOT NULL
		   AND IFNULL(statement_type, '') != 'SCRIPT'`,
		b.regionRef(region), timeRangeToInterval(timeRange)))
	type row struct {
		UserEmail string `bigquery:"user_email"`
	}
	rows, err := collectRows[row](q, ctx)
	if err != nil {
		return nil, fmt.Errorf("active principals query failed: %w", err)
	}
	active := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.UserEmail != "" {
			active[strings.ToLower(r.UserEmail)] = true
		}
	}
	return active, nil
}
