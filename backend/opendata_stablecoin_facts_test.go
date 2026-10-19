package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The embedded file must load, cover the core set, and (until the M4
// curation) hold nothing but "status": "unknown" placeholders.
func TestStableFactsEmbeddedValid(t *testing.T) {
	f, _, err := parseStableFacts(stableFactsEmbedded)
	if err != nil {
		t.Fatalf("embedded stablecoin_facts.json is invalid: %v", err)
	}
	for _, asset := range []string{"usdt", "usdc", "eurc"} {
		a, ok := f.Assets[asset]
		if !ok || a == nil {
			t.Fatalf("embedded facts lack core asset %q", asset)
		}
		if err := validateStableAsset(asset, a); err != nil {
			t.Errorf("%s: %v", asset, err)
		}
		if a.Status != stableFactsUnknown {
			t.Errorf("%s: status = %q; embedded facts stay unknown until curated with sources (M4)", asset, a.Status)
		}
	}
	got := loadStableFacts("", os.ReadFile)
	if got.Version == "" || got.OverrideStatus != "none" || len(got.Overridden) != 0 {
		t.Errorf("loadStableFacts without override = %+v", got)
	}
}

const stableFactsTestBase = `{
  "version": "test-1",
  "assets": {
    "usdt": {
      "issuer": {"name": "Tether", "entities": [{"name": "Tether Ltd", "jurisdiction": "SV", "role": "issuer", "source_url": "https://tether.to/a", "as_of": "2026-10-01"}]},
      "attestations": [{"period_end": "2026-06-30", "published": "2026-07-31", "firm": "BDO", "type": "attestation", "reserves_native": "1.5", "liabilities_native": "1.4", "url": "https://tether.to/r"}],
      "cadence_days": 90,
      "attestations_checked": "2026-10-10",
      "reserve_mix": [{"as_of": "2026-06-30", "source_url": "https://tether.to/r", "buckets": [{"id": "tbills_le93d", "share_pct": 80}, {"id": "gold", "share_pct": 15}, {"id": "btc", "share_pct": 5}]}],
      "regulatory": [{"regime": "EU-MiCA", "status": "not_authorized", "entity": "Tether Ltd", "as_of": "2026-10-01", "source_url": "https://esma.europa.eu/x", "note": "EU users"}],
      "redemption": [{"channel": "direct", "eligibility": "verified customers", "min_native": "100000", "fee": "0.1%", "as_of": "2026-10-01", "source_url": "https://tether.to/fees"}],
      "events": [{"date": "2022-05-12", "kind": "depeg", "title": "Terra collapse", "low_price": 0.95, "source_url": "https://example.com/e"}]
    },
    "usdc": {"status": "unknown"},
    "eurc": {"status": "unknown"}
  }
}`

func TestStableFactsValidation(t *testing.T) {
	if _, _, err := parseStableFactsAndValidate([]byte(stableFactsTestBase)); err != nil {
		t.Fatalf("valid base rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(string) string
	}{
		{"missing version", func(s string) string { return strings.Replace(s, `"version": "test-1",`, ``, 1) }},
		{"unknown top-level field", func(s string) string {
			return strings.Replace(s, `"version": "test-1",`, `"version": "test-1", "extra": 1,`, 1)
		}},
		{"unknown asset", func(s string) string {
			return strings.Replace(s, `"eurc": {"status": "unknown"}`, `"dai": {"status": "unknown"}`, 1)
		}},
		{"unknown asset field", func(s string) string {
			return strings.Replace(s, `"cadence_days": 90,`, `"cadence_days": 90, "rating": "A",`, 1)
		}},
		{"bad status", func(s string) string {
			return strings.Replace(s, `"usdc": {"status": "unknown"}`, `"usdc": {"status": "maybe"}`, 1)
		}},
		{"unknown status with facts", func(s string) string {
			return strings.Replace(s, `"usdc": {"status": "unknown"}`, `"usdc": {"status": "unknown", "cadence_days": 30}`, 1)
		}},
		{"attestation type enum", func(s string) string { return strings.Replace(s, `"type": "attestation"`, `"type": "review"`, 1) }},
		{"attestation missing firm", func(s string) string { return strings.Replace(s, `"firm": "BDO", `, ``, 1) }},
		{"attestation published before period end", func(s string) string {
			return strings.Replace(s, `"published": "2026-07-31"`, `"published": "2026-06-01"`, 1)
		}},
		{"attestation bad decimal", func(s string) string {
			return strings.Replace(s, `"reserves_native": "1.5"`, `"reserves_native": "lots"`, 1)
		}},
		{"attestations without cadence", func(s string) string { return strings.Replace(s, `"cadence_days": 90,`, ``, 1) }},
		{"bad attestations_checked", func(s string) string {
			return strings.Replace(s, `"attestations_checked": "2026-10-10"`, `"attestations_checked": "10/10/2026"`, 1)
		}},
		{"bad date format", func(s string) string {
			return strings.Replace(s, `"as_of": "2026-10-01", "source_url": "https://esma`, `"as_of": "2026/10/01", "source_url": "https://esma`, 1)
		}},
		{"missing as_of", func(s string) string {
			return strings.Replace(s, `"as_of": "2026-10-01", "source_url": "https://tether.to/fees"`, `"source_url": "https://tether.to/fees"`, 1)
		}},
		{"missing source_url", func(s string) string { return strings.Replace(s, `, "source_url": "https://esma.europa.eu/x"`, ``, 1) }},
		{"non-https url", func(s string) string { return strings.Replace(s, `https://example.com/e`, `http://example.com/e`, 1) }},
		{"javascript url", func(s string) string { return strings.Replace(s, `https://example.com/e`, `javascript:alert(1)`, 1) }},
		{"regime enum", func(s string) string { return strings.Replace(s, `"regime": "EU-MiCA"`, `"regime": "UK-FCA"`, 1) }},
		{"regulatory status enum", func(s string) string {
			return strings.Replace(s, `"status": "not_authorized"`, `"status": "approved"`, 1)
		}},
		{"bucket enum", func(s string) string { return strings.Replace(s, `"id": "btc"`, `"id": "crypto"`, 1) }},
		{"duplicate bucket", func(s string) string { return strings.Replace(s, `"id": "btc"`, `"id": "gold"`, 1) }},
		{"bucket share missing", func(s string) string { return strings.Replace(s, `{"id": "btc", "share_pct": 5}`, `{"id": "btc"}`, 1) }},
		{"share sum 99.0", func(s string) string { return strings.Replace(s, `"share_pct": 5}`, `"share_pct": 4}`, 1) }},
		{"share sum 101.0", func(s string) string { return strings.Replace(s, `"share_pct": 5}`, `"share_pct": 6}`, 1) }},
		{"event kind enum", func(s string) string { return strings.Replace(s, `"kind": "depeg"`, `"kind": "hack"`, 1) }},
		{"event low price not positive", func(s string) string { return strings.Replace(s, `"low_price": 0.95`, `"low_price": 0`, 1) }},
		{"cadence out of range", func(s string) string { return strings.Replace(s, `"cadence_days": 90,`, `"cadence_days": 400,`, 1) }},
		{"trailing data", func(s string) string { return s + `{}` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.edit(stableFactsTestBase)
			if src == stableFactsTestBase {
				t.Fatal("edit did not change the input (test bug)")
			}
			if _, _, err := parseStableFactsAndValidate([]byte(src)); err == nil {
				t.Fatal("invalid facts accepted")
			}
		})
	}
	// Share sums inside 100 ± 0.5 are accepted.
	for _, last := range []string{`"share_pct": 4.6}`, `"share_pct": 5.4}`} {
		src := strings.Replace(stableFactsTestBase, `"share_pct": 5}`, last, 1)
		if _, _, err := parseStableFactsAndValidate([]byte(src)); err != nil {
			t.Errorf("share sum with %s rejected: %v", last, err)
		}
	}
}

func writeStableOverride(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStableFactsOverrideMerge(t *testing.T) {
	path := writeStableOverride(t, `{
  "version": "local-7",
  "assets": {
    "usdt": {"regulatory": [{"regime": "US-GENIUS", "status": "pending_regime", "entity": "Tether", "as_of": "2026-10-15", "source_url": "https://congress.gov/x"}]},
    "eurc": {"cadence_days": 30, "attestations": [{"period_end": "2026-09-30", "published": "2026-10-15", "firm": "Deloitte", "type": "attestation", "url": "https://circle.com/t"}]}
  }
}`)
	got := loadStableFactsFrom([]byte(stableFactsTestBase), path, os.ReadFile)
	if got.OverrideStatus != "applied" || got.OverrideVersion != "local-7" || got.Version != "test-1" {
		t.Fatalf("status/version = %q %q %q", got.OverrideStatus, got.OverrideVersion, got.Version)
	}
	usdt := got.Assets["usdt"]
	if len(usdt.Regulatory) != 1 || usdt.Regulatory[0].Regime != "US-GENIUS" {
		t.Errorf("usdt regulatory not replaced: %+v", usdt.Regulatory)
	}
	if usdt.Issuer == nil || usdt.Issuer.Name != "Tether" || usdt.CadenceDays != 90 || len(usdt.Attestations) != 1 {
		t.Errorf("sections the override did not set must keep the embedded values: %+v", usdt)
	}
	eurc := got.Assets["eurc"]
	if eurc.Status != "" || eurc.CadenceDays != 30 || len(eurc.Attestations) != 1 {
		t.Errorf("eurc override not applied over the unknown placeholder: %+v", eurc)
	}
	want := map[string][]string{"usdt": {"regulatory"}, "eurc": {"attestations", "cadence_days"}}
	if !reflect.DeepEqual(got.Overridden, want) {
		t.Errorf("override badges = %v, want %v", got.Overridden, want)
	}
	if !got.isOverridden("usdt", "regulatory") || got.isOverridden("usdt", "issuer") || got.isOverridden("usdc", "regulatory") {
		t.Error("isOverridden reports the wrong sections")
	}
	if got.Assets["usdc"].Status != stableFactsUnknown {
		t.Errorf("usdc must stay unknown: %+v", got.Assets["usdc"])
	}
}

func TestStableFactsOverrideRejected(t *testing.T) {
	cases := map[string]string{
		"bad enum":           `{"version": "x", "assets": {"usdt": {"regulatory": [{"regime": "EU-MiCA", "status": "approved", "entity": "T", "as_of": "2026-10-01", "source_url": "https://a.b/c"}]}}}`,
		"unknown field":      `{"version": "x", "assets": {"usdt": {"rating": "AAA"}}}`,
		"missing version":    `{"assets": {"usdt": {"cadence_days": 30}}}`,
		"merged invalid":     `{"version": "x", "assets": {"usdc": {"attestations": [{"period_end": "2026-09-30", "published": "2026-10-15", "firm": "D", "type": "audit", "url": "https://c.com/t"}]}}}`,
		"unknown plus facts": `{"version": "x", "assets": {"usdt": {"status": "unknown", "cadence_days": 30}}}`,
		"not json":           `cadence_days: 30`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := loadStableFactsFrom([]byte(stableFactsTestBase), writeStableOverride(t, body), os.ReadFile)
			if got.OverrideStatus != "invalid" {
				t.Errorf("OverrideStatus = %q, want invalid", got.OverrideStatus)
			}
			if len(got.Overridden) != 0 || got.OverrideVersion != "" {
				t.Errorf("rejected override left badges/version: %v %q", got.Overridden, got.OverrideVersion)
			}
			if got.Assets["usdt"].Regulatory[0].Status != "not_authorized" || got.Assets["usdc"].Status != stableFactsUnknown {
				t.Errorf("rejected override changed the embedded facts: %+v", got.Assets)
			}
		})
	}
	missing := loadStableFactsFrom([]byte(stableFactsTestBase), filepath.Join(t.TempDir(), "absent.json"), os.ReadFile)
	if missing.OverrideStatus != "unreadable" || len(missing.Overridden) != 0 {
		t.Errorf("missing override file: status %q badges %v, want unreadable/none", missing.OverrideStatus, missing.Overridden)
	}
	failing := loadStableFactsFrom([]byte(stableFactsTestBase), "/x", func(string) ([]byte, error) { return nil, errors.New("boom") })
	if failing.OverrideStatus != "unreadable" {
		t.Errorf("read error: status %q, want unreadable", failing.OverrideStatus)
	}
}

func TestStableServiceLoadsFacts(t *testing.T) {
	svc := newStablecoinService(&Config{})
	if svc.facts.Version == "" || svc.facts.OverrideStatus != "none" {
		t.Errorf("service facts = %+v", svc.facts)
	}
	cfg := &Config{}
	cfg.Stablecoin.FactsFile = filepath.Join(t.TempDir(), "absent.json")
	if s := newStablecoinService(cfg); s.facts.OverrideStatus != "unreadable" {
		t.Errorf("missing facts_file: status %q, want unreadable (and the service still starts)", s.facts.OverrideStatus)
	}
}
