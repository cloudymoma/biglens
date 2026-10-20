package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Curated stablecoin facts (stablecoin.md §4.5). The embedded file is the
// reviewed, versioned baseline; an optional operator override is merged on top
// with the same validation. A fact ships in the embedded file only after it
// has been checked against its source; until then an asset is stored as
// {"status": "unknown"} and nothing is guessed.
//
//go:embed stablecoin_facts.json
var stableFactsEmbedded []byte

const stableFactsUnknown = "unknown"

// stableFactsAssets is the asset whitelist of the facts file (the core set).
var stableFactsAssets = map[string]bool{"usdt": true, "usdc": true, "eurc": true}

var (
	stableAttestationTypes = map[string]bool{"attestation": true, "audit": true}
	stableReserveBuckets   = map[string]bool{"cash_deposits": true, "tbills_le93d": true, "repo": true, "gov_mmf": true,
		"other_tbills": true, "gold": true, "btc": true, "secured_loans": true, "other": true}
	stableRegimes     = map[string]bool{"EU-MiCA": true, "US-GENIUS": true, "HK-SO": true, "SG-MAS": true, "JP-PSA": true}
	stableRegStatuses = map[string]bool{"authorized": true, "not_authorized": true, "pending_regime": true,
		"not_applicable": true, "unknown": true}
	stableEventKinds = map[string]bool{"depeg": true, "attestation": true, "regulatory": true, "control": true, "incident": true}
)

type stableFactsFile struct {
	Version string                       `json:"version"`
	Assets  map[string]*stableAssetFacts `json:"assets"`
	// Annotations are historical depeg events for the peg history (Appendix
	// C). They are top-level because several concern coins outside the core
	// set (UST, BUSD, DAI, FDUSD, USDe), shown as context.
	Annotations []stableAnnotation `json:"annotations,omitempty"`
}

// stableAnnotation is one historical depeg event. Until it has been checked
// against a source (M4) it carries "status": "unknown" and no source_url.
type stableAnnotation struct {
	Date      string   `json:"date"`               // YYYY-MM-DD, or YYYY-MM when only the month is known
	EndDate   string   `json:"end_date,omitempty"` // YYYY-MM-DD, for multi-day events
	Asset     string   `json:"asset"`              // ticker, e.g. USDC, UST, USDe
	Title     string   `json:"title"`
	Extreme   string   `json:"extreme,omitempty"`   // approximate extreme, as text
	LowPrice  *float64 `json:"low_price,omitempty"` // in the coin's own currency
	Status    string   `json:"status,omitempty"`    // "unknown": not checked yet
	SourceURL string   `json:"source_url,omitempty"`
}

type stableAssetFacts struct {
	Status              string              `json:"status,omitempty"` // "unknown": not curated yet
	Issuer              *stableIssuerFacts  `json:"issuer,omitempty"`
	Attestations        []stableAttestation `json:"attestations,omitempty"`
	CadenceDays         int                 `json:"cadence_days,omitempty"`
	AttestationsChecked string              `json:"attestations_checked,omitempty"`
	ReserveMix          []stableReserveMix  `json:"reserve_mix,omitempty"`
	Regulatory          []stableRegulatory  `json:"regulatory,omitempty"`
	Redemption          []stableRedemption  `json:"redemption,omitempty"`
	Events              []stableFactEvent   `json:"events,omitempty"`
}

type stableIssuerFacts struct {
	Name     string               `json:"name"`
	Entities []stableIssuerEntity `json:"entities,omitempty"`
}

type stableIssuerEntity struct {
	Name         string `json:"name"`
	Jurisdiction string `json:"jurisdiction"`
	Role         string `json:"role"`
	SourceURL    string `json:"source_url"`
	AsOf         string `json:"as_of"`
}

type stableAttestation struct {
	PeriodEnd         string `json:"period_end"`
	Published         string `json:"published"`
	Firm              string `json:"firm"`
	Type              string `json:"type"`
	ReservesNative    string `json:"reserves_native,omitempty"`
	LiabilitiesNative string `json:"liabilities_native,omitempty"`
	URL               string `json:"url"`
}

type stableReserveMix struct {
	AsOf      string                `json:"as_of"`
	Buckets   []stableReserveBucket `json:"buckets"`
	SourceURL string                `json:"source_url"`
}

type stableReserveBucket struct {
	ID       string   `json:"id"`
	SharePct *float64 `json:"share_pct"`
}

type stableRegulatory struct {
	Regime    string `json:"regime"`
	Status    string `json:"status"`
	Entity    string `json:"entity,omitempty"`
	AsOf      string `json:"as_of"`
	SourceURL string `json:"source_url"`
	Note      string `json:"note,omitempty"`
}

type stableRedemption struct {
	Channel     string `json:"channel"`
	Eligibility string `json:"eligibility"`
	MinNative   string `json:"min_native,omitempty"`
	Fee         string `json:"fee,omitempty"`
	AsOf        string `json:"as_of"`
	SourceURL   string `json:"source_url"`
}

type stableFactEvent struct {
	Date      string   `json:"date"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	LowPrice  *float64 `json:"low_price,omitempty"`
	SourceURL string   `json:"source_url"`
}

// stableFacts is the merged, validated fact set served to handlers.
type stableFacts struct {
	Version         string
	OverrideVersion string
	OverrideStatus  string // none | applied | invalid | unreadable
	Assets          map[string]stableAssetFacts
	Overridden      map[string][]string // asset → sections set by the operator override (UI badge)
	Annotations     []stableAnnotation
	// AnnotationsOverridden: the operator override replaced the annotations.
	AnnotationsOverridden bool
}

func (f stableFacts) isOverridden(asset, section string) bool {
	for _, s := range f.Overridden[asset] {
		if s == section {
			return true
		}
	}
	return false
}

// parseStableFacts decodes a facts file strictly (unknown fields, unknown
// assets and trailing data are errors) and reports which top-level sections
// each asset object sets, so an override can be merged section by section.
func parseStableFacts(data []byte) (*stableFactsFile, map[string]map[string]bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f stableFactsFile
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("decode: %w", err)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("trailing data after the facts object")
	}
	if f.Version == "" {
		return nil, nil, errors.New("version is required")
	}
	var raw struct {
		Assets map[string]map[string]json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("decode sections: %w", err)
	}
	present := make(map[string]map[string]bool, len(f.Assets))
	for asset, a := range f.Assets {
		if !stableFactsAssets[asset] {
			return nil, nil, fmt.Errorf("unknown asset %q", asset)
		}
		if a == nil {
			return nil, nil, fmt.Errorf("%s: null asset object", asset)
		}
		if a.Status != "" && a.Status != stableFactsUnknown {
			return nil, nil, fmt.Errorf("%s: status %q (allowed: unknown)", asset, a.Status)
		}
		sections := make(map[string]bool, len(raw.Assets[asset]))
		for k := range raw.Assets[asset] {
			sections[k] = true
		}
		if a.Status == stableFactsUnknown && len(sections) > 1 {
			return nil, nil, fmt.Errorf("%s: status unknown cannot carry facts", asset)
		}
		present[asset] = sections
	}
	return &f, present, nil
}

// parseStableFactsAndValidate parses a complete facts file and validates
// every asset and annotation in it.
func parseStableFactsAndValidate(data []byte) (*stableFactsFile, map[string]map[string]bool, error) {
	f, present, err := parseStableFacts(data)
	if err != nil {
		return nil, nil, err
	}
	for asset, a := range f.Assets {
		if err := validateStableAsset(asset, a); err != nil {
			return nil, nil, err
		}
	}
	if err := validateStableAnnotations(f.Annotations); err != nil {
		return nil, nil, err
	}
	return f, present, nil
}

// validateStableAnnotations: every field the UI shows is required, and a
// checked annotation (no "status": "unknown") needs an https source.
func validateStableAnnotations(list []stableAnnotation) error {
	for i, a := range list {
		wrap := func(err error) error { return fmt.Errorf("annotations[%d]: %w", i, err) }
		if _, err := time.Parse(time.DateOnly, a.Date); err != nil {
			if _, err := time.Parse("2006-01", a.Date); err != nil {
				return wrap(fmt.Errorf("date %q (want YYYY-MM-DD or YYYY-MM)", a.Date))
			}
		}
		if a.EndDate != "" {
			if err := stableFactDate("end_date", a.EndDate); err != nil {
				return wrap(err)
			}
			start := a.Date
			if len(start) == len("2006-01") {
				start += "-01"
			}
			if a.EndDate < start {
				return wrap(errors.New("end_date before date"))
			}
		}
		if err := firstStableErr(stableFactRequired("asset", a.Asset), stableFactRequired("title", a.Title)); err != nil {
			return wrap(err)
		}
		if a.LowPrice != nil && !(*a.LowPrice > 0) {
			return wrap(errors.New("low_price must be positive"))
		}
		switch a.Status {
		case stableFactsUnknown:
			if a.SourceURL != "" {
				if err := stableFactURL("source_url", a.SourceURL); err != nil {
					return wrap(err)
				}
			}
		case "":
			if err := stableFactURL("source_url", a.SourceURL); err != nil {
				return wrap(err)
			}
		default:
			return wrap(fmt.Errorf("status %q (allowed: unknown)", a.Status))
		}
	}
	return nil
}

func stableFactDate(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return fmt.Errorf("%s %q is not YYYY-MM-DD", field, v)
	}
	return nil
}

func stableFactURL(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s must be an https URL", field)
	}
	return nil
}

func stableFactRequired(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func stableFactDecimal(field, v string) error {
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("%s %q is not a non-negative decimal", field, v)
	}
	return nil
}

func firstStableErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateStableAsset applies the §4.5 rules to one complete asset entry.
func validateStableAsset(asset string, a *stableAssetFacts) error {
	wrap := func(section string, i int, err error) error {
		if err == nil {
			return nil
		}
		if i < 0 {
			return fmt.Errorf("%s.%s: %w", asset, section, err)
		}
		return fmt.Errorf("%s.%s[%d]: %w", asset, section, i, err)
	}
	if a == nil {
		return fmt.Errorf("%s: missing", asset)
	}
	empty := a.Issuer == nil && len(a.Attestations) == 0 && a.CadenceDays == 0 && a.AttestationsChecked == "" &&
		len(a.ReserveMix) == 0 && len(a.Regulatory) == 0 && len(a.Redemption) == 0 && len(a.Events) == 0
	switch a.Status {
	case stableFactsUnknown:
		if !empty {
			return fmt.Errorf("%s: status unknown cannot carry facts", asset)
		}
		return nil
	case "":
		if empty {
			return fmt.Errorf("%s: no facts; use {\"status\": \"unknown\"}", asset)
		}
	default:
		return fmt.Errorf("%s: status %q (allowed: unknown)", asset, a.Status)
	}
	if a.Issuer != nil {
		if err := stableFactRequired("name", a.Issuer.Name); err != nil {
			return wrap("issuer", -1, err)
		}
		for i, e := range a.Issuer.Entities {
			if err := firstStableErr(stableFactRequired("name", e.Name), stableFactRequired("jurisdiction", e.Jurisdiction),
				stableFactRequired("role", e.Role), stableFactURL("source_url", e.SourceURL), stableFactDate("as_of", e.AsOf)); err != nil {
				return wrap("issuer.entities", i, err)
			}
		}
	}
	if a.CadenceDays < 0 || a.CadenceDays > 366 {
		return wrap("cadence_days", -1, fmt.Errorf("%d outside [1, 366]", a.CadenceDays))
	}
	if len(a.Attestations) > 0 && a.CadenceDays == 0 {
		return wrap("cadence_days", -1, errors.New("required when attestations are listed"))
	}
	for i, at := range a.Attestations {
		if err := firstStableErr(stableFactDate("period_end", at.PeriodEnd), stableFactDate("published", at.Published),
			stableFactRequired("firm", at.Firm), stableFactURL("url", at.URL),
			stableFactDecimal("reserves_native", at.ReservesNative), stableFactDecimal("liabilities_native", at.LiabilitiesNative)); err != nil {
			return wrap("attestations", i, err)
		}
		if !stableAttestationTypes[at.Type] {
			return wrap("attestations", i, fmt.Errorf("type %q (allowed: attestation, audit)", at.Type))
		}
		if at.Published < at.PeriodEnd {
			return wrap("attestations", i, errors.New("published before period_end"))
		}
	}
	if a.AttestationsChecked != "" {
		if err := stableFactDate("attestations_checked", a.AttestationsChecked); err != nil {
			return fmt.Errorf("%s: %w", asset, err)
		}
	}
	for i, m := range a.ReserveMix {
		if err := firstStableErr(stableFactDate("as_of", m.AsOf), stableFactURL("source_url", m.SourceURL)); err != nil {
			return wrap("reserve_mix", i, err)
		}
		if len(m.Buckets) == 0 {
			return wrap("reserve_mix", i, errors.New("buckets are required"))
		}
		seen := make(map[string]bool, len(m.Buckets))
		var sum float64
		for _, b := range m.Buckets {
			if !stableReserveBuckets[b.ID] {
				return wrap("reserve_mix", i, fmt.Errorf("bucket id %q is not allowed", b.ID))
			}
			if seen[b.ID] {
				return wrap("reserve_mix", i, fmt.Errorf("duplicate bucket %q", b.ID))
			}
			seen[b.ID] = true
			if b.SharePct == nil {
				return wrap("reserve_mix", i, fmt.Errorf("bucket %q: share_pct is required", b.ID))
			}
			if *b.SharePct < 0 || *b.SharePct > 100 {
				return wrap("reserve_mix", i, fmt.Errorf("bucket %q: share_pct %v outside [0, 100]", b.ID, *b.SharePct))
			}
			sum += *b.SharePct
		}
		if math.Abs(sum-100) > 0.5 {
			return wrap("reserve_mix", i, fmt.Errorf("share_pct sums to %.2f, want 100 ± 0.5", sum))
		}
	}
	for i, r := range a.Regulatory {
		if !stableRegimes[r.Regime] {
			return wrap("regulatory", i, fmt.Errorf("regime %q is not allowed", r.Regime))
		}
		if !stableRegStatuses[r.Status] {
			return wrap("regulatory", i, fmt.Errorf("status %q is not allowed", r.Status))
		}
		if r.Status != "unknown" && r.Status != "not_applicable" && r.Entity == "" {
			return wrap("regulatory", i, errors.New("entity is required"))
		}
		if err := firstStableErr(stableFactDate("as_of", r.AsOf), stableFactURL("source_url", r.SourceURL)); err != nil {
			return wrap("regulatory", i, err)
		}
	}
	for i, r := range a.Redemption {
		if err := firstStableErr(stableFactRequired("channel", r.Channel), stableFactRequired("eligibility", r.Eligibility),
			stableFactDecimal("min_native", r.MinNative), stableFactDate("as_of", r.AsOf), stableFactURL("source_url", r.SourceURL)); err != nil {
			return wrap("redemption", i, err)
		}
	}
	for i, e := range a.Events {
		if err := firstStableErr(stableFactDate("date", e.Date), stableFactRequired("title", e.Title), stableFactURL("source_url", e.SourceURL)); err != nil {
			return wrap("events", i, err)
		}
		if !stableEventKinds[e.Kind] {
			return wrap("events", i, fmt.Errorf("kind %q is not allowed", e.Kind))
		}
		if e.LowPrice != nil && !(*e.LowPrice > 0) {
			return wrap("events", i, errors.New("low_price must be positive"))
		}
	}
	return nil
}

// loadStableFacts loads the embedded facts and merges the operator override
// at overridePath (empty: none).
func loadStableFacts(overridePath string, read func(string) ([]byte, error)) stableFacts {
	return loadStableFactsFrom(stableFactsEmbedded, overridePath, read)
}

// loadStableFactsFrom merges an override over base. An override that cannot
// be read, fails validation, or makes a merged asset invalid is ignored as a
// whole (details are logged, the status is surfaced in /meta).
func loadStableFactsFrom(base []byte, overridePath string, read func(string) ([]byte, error)) stableFacts {
	out := stableFacts{OverrideStatus: "none", Assets: map[string]stableAssetFacts{}}
	if f, _, err := parseStableFactsAndValidate(base); err != nil {
		// Unreachable for shipped builds: tests validate the embedded file.
		slog.Error("stablecoin facts: embedded file invalid", "error", err)
	} else {
		out.Version, out.Annotations = f.Version, f.Annotations
		for asset, a := range f.Assets {
			out.Assets[asset] = *a
		}
	}
	if overridePath == "" {
		return out
	}
	data, err := read(overridePath)
	if err != nil {
		slog.Warn("stablecoin facts override unreadable; using embedded facts", "path", overridePath, "error", err)
		out.OverrideStatus = "unreadable"
		return out
	}
	merged, badges, version, err := mergeStableFacts(out.Assets, data)
	var annotations []stableAnnotation
	var hasAnnotations bool
	if err == nil {
		annotations, hasAnnotations, err = parseStableOverrideAnnotations(data)
	}
	if err != nil {
		slog.Warn("stablecoin facts override rejected; using embedded facts", "path", overridePath, "error", err)
		out.OverrideStatus = "invalid"
		return out
	}
	out.Assets, out.Overridden, out.OverrideVersion, out.OverrideStatus = merged, badges, version, "applied"
	if hasAnnotations {
		out.Annotations, out.AnnotationsOverridden = annotations, true
	}
	slog.Info("stablecoin facts override applied", "path", overridePath, "version", version, "sections", badges,
		"annotations", hasAnnotations)
	return out
}

// parseStableOverrideAnnotations returns an override's annotations and
// whether it sets the key at all (an override replaces the list as a whole).
func parseStableOverrideAnnotations(data []byte) ([]stableAnnotation, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, false, err
	}
	if _, ok := top["annotations"]; !ok {
		return nil, false, nil
	}
	f, _, err := parseStableFacts(data)
	if err != nil {
		return nil, false, err
	}
	if err := validateStableAnnotations(f.Annotations); err != nil {
		return nil, false, err
	}
	if f.Annotations == nil {
		f.Annotations = []stableAnnotation{}
	}
	return f.Annotations, true, nil
}

func mergeStableFacts(base map[string]stableAssetFacts, data []byte) (map[string]stableAssetFacts, map[string][]string, string, error) {
	o, present, err := parseStableFacts(data)
	if err != nil {
		return nil, nil, "", err
	}
	merged := make(map[string]stableAssetFacts, len(base))
	for k, v := range base {
		merged[k] = v
	}
	badges := map[string][]string{}
	for asset, oa := range o.Assets {
		sections := present[asset]
		if len(sections) == 0 {
			continue
		}
		m := merged[asset]
		if sections["status"] {
			m = stableAssetFacts{Status: stableFactsUnknown}
		}
		for section := range sections {
			switch section {
			case "issuer":
				m.Issuer = oa.Issuer
			case "attestations":
				m.Attestations = oa.Attestations
			case "cadence_days":
				m.CadenceDays = oa.CadenceDays
			case "attestations_checked":
				m.AttestationsChecked = oa.AttestationsChecked
			case "reserve_mix":
				m.ReserveMix = oa.ReserveMix
			case "regulatory":
				m.Regulatory = oa.Regulatory
			case "redemption":
				m.Redemption = oa.Redemption
			case "events":
				m.Events = oa.Events
			}
		}
		if !sections["status"] && m.Status == stableFactsUnknown {
			m.Status = "" // the operator curated an asset the embedded file has no facts for
		}
		if err := validateStableAsset(asset, &m); err != nil {
			return nil, nil, "", fmt.Errorf("merged: %w", err)
		}
		merged[asset] = m
		names := make([]string, 0, len(sections))
		for s := range sections {
			names = append(names, s)
		}
		sort.Strings(names)
		badges[asset] = names
	}
	return merged, badges, o.Version, nil
}
