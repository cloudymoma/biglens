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
	if g == "allUsers" || g == "allAuthenticatedUsers" {
		return KindPublic, g
	}
	prefix, rest, found := strings.Cut(g, ":")
	if !found {
		return KindSpecial, g
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
	case "specialGroup":
		return KindSpecial, rest
	default:
		return KindSpecial, rest
	}
}

func isWriteRole(role string) bool {
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

// filterProjectBindings keeps only BigQuery / Knowledge Catalog relevant
// roles; everything else in the project policy never reaches the frontend.
func filterProjectBindings(bindings map[string][]string) []ProjectBinding {
	var out []ProjectBinding
	for role, members := range bindings {
		basic := role == "roles/owner" || role == "roles/editor" || role == "roles/viewer"
		if !basic &&
			!strings.HasPrefix(role, "roles/bigquery.") &&
			!strings.HasPrefix(role, "roles/dataplex.") &&
			!strings.HasPrefix(role, "roles/datacatalog.") {
			continue
		}
		out = append(out, ProjectBinding{Role: role, Basic: basic, Members: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
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
		if !active[p.Principal] {
			out = append(out, p)
		}
	}
	return out
}

const maxPostureDatasets = 50

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

type RLSPolicy struct {
	Dataset   string `json:"dataset"`
	Table     string `json:"table" bigquery:"table_name"`
	Policy    string `json:"policy" bigquery:"row_access_policy_name"`
	Predicate string `json:"predicate" bigquery:"filter_predicate"`
	Modified  string `json:"modified" bigquery:"modified"`
}

// GetGrantsAndRLS fans out two metadata queries per dataset with bounded
// concurrency. Individual dataset failures are logged and skipped so one
// permission gap cannot blank the whole posture view.
func (b *BQClient) GetGrantsAndRLS(ctx context.Context, region string, datasets []string) ([]ObjectGrant, []RLSPolicy) {
	var (
		mu     sync.Mutex
		grants []ObjectGrant
		rls    []RLSPolicy
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 8)
	)
	project := b.config.BigQuery.ProjectID
	for _, ds := range datasets {
		wg.Add(1)
		go func(ds string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if !datasetNameRe.MatchString(ds) {
				slog.Warn("invalid dataset name skipped", "dataset", ds)
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
			}

			rq := b.client.Query(fmt.Sprintf(
				"SELECT table_name, row_access_policy_name, filter_predicate, "+
					"FORMAT_TIMESTAMP('%%Y-%%m-%%dT%%H:%%M:%%SZ', last_modified_time) AS modified "+
					"FROM `%s.%s`.INFORMATION_SCHEMA.ROW_ACCESS_POLICIES", project, ds))
			r, err := collectRows[RLSPolicy](rq, ctx)
			if err != nil {
				slog.Warn("row access policies skipped", "dataset", ds, "error", err)
			}
			for i := range r {
				r[i].Dataset = ds
			}

			mu.Lock()
			grants = append(grants, g...)
			rls = append(rls, r...)
			mu.Unlock()
		}(ds)
	}
	wg.Wait()
	return grants, rls
}

type PublicFlag struct {
	Dataset    string      `json:"dataset"`
	ObjectType string      `json:"object_type"`
	Role       string      `json:"role"`
	Grantee    string      `json:"grantee"`
	Kind       GranteeKind `json:"kind"`
}

// publicFlags surfaces grants that widen access beyond named principals:
// public internet, whole domains, and legacy special groups.
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
		if kind == KindPublic || kind == KindSpecial {
			continue // covered by publicFlags
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

// GetProjectBindings reads the project IAM policy once and whitelists
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

const sensitiveColRegex = `ssn|social_security|passport|tax_id|email|phone|address|dob|birth|salary|income|credit_card|iban|swift|password|secret|token|api_key|auth`

type SensitiveColumn struct {
	Dataset string `json:"dataset" bigquery:"table_schema"`
	Table   string `json:"table" bigquery:"table_name"`
	Column  string `json:"column" bigquery:"field_path"`
	Tagged  bool   `json:"tagged" bigquery:"tagged"`
}

func (b *BQClient) GetSensitiveColumns(ctx context.Context, region string) ([]SensitiveColumn, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT table_schema, table_name, field_path,
			ARRAY_LENGTH(IFNULL(policy_tags, [])) > 0 AS tagged
		FROM %s.INFORMATION_SCHEMA.COLUMN_FIELD_PATHS
		WHERE NOT STARTS_WITH(table_schema, '_')
			AND REGEXP_CONTAINS(LOWER(field_path), @pattern)
		ORDER BY tagged, table_schema, table_name LIMIT 200`, b.regionRef(region)))
	q.Parameters = []bigquery.QueryParameter{{Name: "pattern", Value: sensitiveColRegex}}
	return collectRows[SensitiveColumn](q, ctx)
}

func (b *BQClient) GetActivePrincipals(ctx context.Context, region, timeRange string) (map[string]bool, error) {
	q := b.client.Query(fmt.Sprintf(
		`SELECT DISTINCT IFNULL(user_email, '') AS user_email FROM %s.INFORMATION_SCHEMA.JOBS_BY_PROJECT
		 WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %s)`,
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
		active[r.UserEmail] = true
	}
	return active, nil
}
