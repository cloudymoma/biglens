package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The embedded file must load and cover the core set. Since the M4 curation
// every core asset carries facts checked against their sources: the
// attestation history with the maintainer check, the cadence, and the EU
// MiCA and US GENIUS rows; the regimes block holds the GENIUS key dates.
func TestStableFactsEmbeddedValid(t *testing.T) {
	f, _, err := parseStableFactsAndValidate(stableFactsEmbedded)
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
		if a.Status != "" || len(a.Attestations) == 0 || a.CadenceDays == 0 || a.AttestationsChecked == "" || a.Issuer == nil {
			t.Errorf("%s: want curated facts (attestations, cadence, attestations_checked, issuer): %+v", asset, a)
		}
		regimes := map[string]bool{}
		for _, r := range a.Regulatory {
			regimes[r.Regime] = true
		}
		if !regimes["EU-MiCA"] || !regimes["US-GENIUS"] {
			t.Errorf("%s: regulatory rows %+v, want EU-MiCA and US-GENIUS", asset, a.Regulatory)
		}
	}
	if len(f.Regimes) == 0 || f.Regimes[0].Regime != "US-GENIUS" || len(f.Regimes[0].KeyDates) == 0 {
		t.Errorf("regimes = %+v, want the US-GENIUS key dates", f.Regimes)
	}
	if f.Version == "2026-10-20.1" {
		t.Errorf("facts version = %q, want bumped version", f.Version)
	}
	var eurcGenius *stableRegulatory
	for i := range f.Assets["eurc"].Regulatory {
		if f.Assets["eurc"].Regulatory[i].Regime == "US-GENIUS" {
			eurcGenius = &f.Assets["eurc"].Regulatory[i]
		}
	}
	if eurcGenius == nil || eurcGenius.Status != "pending_regime" || !strings.Contains(eurcGenius.Note, "2028-07-18") || !strings.Contains(eurcGenius.Note, "2026-16796") {
		t.Errorf("eurc US-GENIUS = %+v, want pending_regime with §18 / 2028-07-18 and Treasury NPRM Q75 note", eurcGenius)
	}
	if u := f.Assets["usdc"].Redemption[0].SourceURL; u != "https://help.circle.com/support/en/usdc-eurc-redemption-structure" {
		t.Errorf("usdc Circle Mint redemption source_url = %q", u)
	}
	if u := f.Assets["eurc"].Redemption[0].SourceURL; u != "https://help.circle.com/support/en/usdc-eurc-redemption-structure" {
		t.Errorf("eurc Circle Mint redemption source_url = %q", u)
	}
	if tl := f.Assets["usdt"].Issuer.Entities[2]; !strings.HasSuffix(tl.SourceURL, "31-10-2025.pdf") {
		t.Errorf("Tether Limited source_url = %q, want 2025-09-30 BDO report", tl.SourceURL)
	}
	got := loadStableFacts("", os.ReadFile)
	if got.Version == "" || got.OverrideStatus != "none" || len(got.Overridden) != 0 || len(got.Regimes) == 0 {
		t.Errorf("loadStableFacts without override = %+v", got)
	}
}

// Attestations and reserve mixes may carry a note, and a reserve mix says
// whether it was attested (§4.5 schema, extended in M4).
func TestStableFactsNotesAndAttested(t *testing.T) {
	src := strings.Replace(stableFactsTestBase, `"url": "https://tether.to/r"}]`,
		`"url": "https://tether.to/r", "note": "covers every fiat-denominated Tether token"}]`, 1)
	src = strings.Replace(src, `"source_url": "https://tether.to/r", "buckets"`, `"source_url": "https://tether.to/r", "attested": true, "note": "T-bills mapped to ≤ 93 days", "buckets"`, 1)
	if src == stableFactsTestBase {
		t.Fatal("edit did not change the input (test bug)")
	}
	f, _, err := parseStableFactsAndValidate([]byte(src))
	if err != nil {
		t.Fatalf("notes rejected: %v", err)
	}
	a := f.Assets["usdt"]
	if a.Attestations[0].Note == "" || a.ReserveMix[0].Attested == nil || !*a.ReserveMix[0].Attested || a.ReserveMix[0].Note == "" {
		t.Errorf("notes not decoded: %+v %+v", a.Attestations[0], a.ReserveMix[0])
	}
}

func TestStableFactsRegimes(t *testing.T) {
	base := `{"version": "t", "assets": {"usdt": {"status": "unknown"}}, "regimes": [%s]}`
	good := `{"regime": "US-GENIUS", "key_dates": [{"date": "2025-07-18", "label": "Enacted"}, {"date": "2027-01-18", "label": "Takes effect at the latest"}], "note": "proposed rules only", "as_of": "2026-10-20", "source_url": "https://www.govinfo.gov/x"}`
	if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", good, 1))); err != nil {
		t.Fatalf("valid regimes rejected: %v", err)
	}
	bad := map[string]string{
		"regime enum":       strings.Replace(good, `"US-GENIUS"`, `"UK-FCA"`, 1),
		"no key dates":      `{"regime": "US-GENIUS", "key_dates": [], "as_of": "2026-10-20", "source_url": "https://www.govinfo.gov/x"}`,
		"bad key date":      strings.Replace(good, `"2027-01-18"`, `"2027/01/18"`, 1),
		"no label":          strings.Replace(good, `"label": "Enacted"`, `"label": ""`, 1),
		"no source":         strings.Replace(good, `, "source_url": "https://www.govinfo.gov/x"`, ``, 1),
		"http source":       strings.Replace(good, `https://www.govinfo.gov/x`, `http://www.govinfo.gov/x`, 1),
		"no as_of":          strings.Replace(good, `"as_of": "2026-10-20", `, ``, 1),
		"duplicate regime":  good + ", " + good,
		"unknown field":     strings.Replace(good, `"note"`, `"rating": "A", "note"`, 1),
		"key date bad link": strings.Replace(good, `"label": "Enacted"}`, `"label": "Enacted", "source_url": "javascript:x"}`, 1),
	}
	for name, r := range bad {
		if _, _, err := parseStableFactsAndValidate([]byte(strings.Replace(base, "%s", r, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An operator override replaces the list as a whole and is badged.
	override := `{"version": "op-1", "regimes": [{"regime": "US-GENIUS", "key_dates": [{"date": "2026-12-01", "label": "Final rules"}], "as_of": "2026-11-01", "source_url": "https://www.federalregister.gov/x"}]}`
	got := loadStableFactsFrom(stableFactsEmbedded, writeStableOverride(t, override), os.ReadFile)
	if got.OverrideStatus != "applied" || !got.RegimesOverridden || len(got.Regimes) != 1 || got.Regimes[0].KeyDates[0].Date != "2026-12-01" {
		t.Errorf("override = status %s, overridden %v, %+v", got.OverrideStatus, got.RegimesOverridden, got.Regimes)
	}
	invalid := `{"version": "op-2", "regimes": [{"regime": "US-GENIUS", "key_dates": [], "as_of": "2026-11-01", "source_url": "https://www.federalregister.gov/x"}]}`
	got = loadStableFactsFrom(stableFactsEmbedded, writeStableOverride(t, invalid), os.ReadFile)
	if got.OverrideStatus != "invalid" || got.RegimesOverridden || len(got.Regimes) == 0 || got.Regimes[0].KeyDates[0].Date == "2026-12-01" {
		t.Errorf("invalid override = status %s, %+v", got.OverrideStatus, got.Regimes)
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
