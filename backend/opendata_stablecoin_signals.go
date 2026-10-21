package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Risk signals (stablecoin.md §6). Each signal is a pure function of its
// inputs with a displayed rule. The per-signal policy (§6.1) turns a raw
// reading into a state:
//   - sampled (S1, S2, S3, S13): hysteresis over the 5-minute peg samples;
//   - confirmed (S8, S12): a change needs 2 consecutive samples of the
//     signal's sampler. The controls sampler confirms S8 and the on-chain
//     half of S12; the engine confirms the aggregator half of S12;
//   - level-triggered (S4, S5, S6, S10, S11): re-evaluated on every input.
//
// There is no composite score: the asset summary is the worst state.

const (
	stableStateNormal   = "normal"
	stableStateInfo     = "info"
	stableStateWatch    = "watch"
	stableStateWarning  = "warning"
	stableStateCritical = "critical"
	stableStateUnknown  = "unknown"
	// stableStateNA marks a signal that does not apply to an asset (S3 for
	// EURC, S5 for USDC and EURC).
	stableStateNA = "n/a"

	stableLevelUnknown  = -1
	stableLevelNormal   = 0
	stableLevelWatch    = 1
	stableLevelWarning  = 2
	stableLevelCritical = 3

	stableReasonWarmingUp = "warming up"
)

var stableLevelNames = [4]string{stableStateNormal, stableStateWatch, stableStateWarning, stableStateCritical}

// Thresholds (§6.2), with their calibration references.
const (
	// S1: 2026-10-19 USDT −11, USDC −2, EURC +1…+3 bps (normal); the
	// Bitstamp lows of USDC 2023-03-11 (−1,947 bps) and USDT 2022-05-12
	// (−899 bps) reach critical.
	stableS1WatchBps     = 30.0
	stableS1WarningBps   = 100.0
	stableS1CriticalBps  = 300.0
	stableS1ImmediateBps = 500.0 // each of ≥ 2 included venues, in one sample
	// S2 catches venue-specific dislocations (USDe on one venue, 2025-10-10/11).
	stableS2WatchBps   = 25.0
	stableS2WarningBps = 100.0
	stableS2MinInputs  = 3
	// S3: Uniswap v3 USDC/USDT carries S3 in v1 (the Curve half is v1.1).
	stableS3WatchBps   = 30.0
	stableS3WarningBps = 100.0
	// S4 on the 3-point rolling median: redemption waves accompanied USDT
	// 2022-05 (24h −3.88%, 7d −10.23%) and USDC 2023-03 (24h −3.65%, 7d
	// −15.17%). Mints are informational only.
	stableS4Watch24hPct    = -0.5
	stableS4Watch7dPct     = -2.0
	stableS4Warning24hPct  = -1.0
	stableS4Warning7dPct   = -4.0
	stableS4Critical24hPct = -3.0
	stableS4Critical7dPct  = -8.0
	stableS4MedianPoints   = 3
	// stableS4IssuerMinDays: the issuer's daily series replaces DefiLlama's
	// once it covers a 7d change on 3-point medians (10 consecutive days).
	stableS4IssuerMinDays = 10
	// S5: 2026-10-19 coverage 1.0302 (normal).
	stableS5WatchCoverage    = 1.010
	stableS5CriticalCoverage = 1.000
	// S6: disclosure age in days since the latest period_end, data-driven
	// (§6.2, M4 decision): watch after cadence + p90 of the asset's own
	// publication lag (published − period_end) + 7 days, warning 30 days
	// later. Circle publishes about 28 days after month-end, so USDC's
	// history gives 30 + 31 + 7 = 68 days. With fewer than 3 reports the
	// fixed fallbacks apply.
	stableS6LagPercentile                = 0.9
	stableS6SlackDays                    = 7
	stableS6WarningAfterWatchDays        = 30
	stableS6MinHistory                   = 3
	stableS6MonthlyFallbackWatchDays     = 65
	stableS6MonthlyFallbackWarningDays   = 95
	stableS6QuarterlyFallbackWatchDays   = 150
	stableS6QuarterlyFallbackWarningDays = 180
	stableS6CheckedWithin                = 14 * 24 * time.Hour
	// S8: a confirmed change stays at warning for 7 days.
	stableS8Window = 7 * 24 * time.Hour
	// S10: example USDT on TRON ≈ 51% (largest chain, info).
	stableS10BridgedWatchPct = 10.0

	// Sampled policy (§6.1): enter a level after 3 consecutive samples at or
	// above it; step down one level after 12 consecutive samples below 80%
	// of the current level's threshold.
	stableSampledEnter    = 3
	stableSampledExit     = 12
	stableSampledExitFrac = 0.8
	// stableSampledMaxGap: known samples further apart are not consecutive;
	// the signal restarts (warming up).
	stableSampledMaxGap = 15 * time.Minute
	// Confirmed policy: consecutive samples that must agree.
	stableConfirmSamples = 2
)

var (
	stableS1Thr = [4]float64{0, stableS1WatchBps, stableS1WarningBps, stableS1CriticalBps}
	stableS2Thr = [4]float64{0, stableS2WatchBps, stableS2WarningBps, 0}
	stableS3Thr = [4]float64{0, stableS3WatchBps, stableS3WarningBps, 0}
)

// stableSignalDef is one row of the v1 catalog (§6.2; S7 and S9 are v1.1).
type stableSignalDef struct {
	ID, Key, Dimension, Policy, Rule string
}

var stableSignalDefs = []stableSignalDef{
	{"S1", "peg", "Peg", "sampled", "|deviation| ≥ 30 / 100 / 300 bps (watch / warning / critical) in 3 consecutive 5-min samples; immediate critical when ≥ 2 included venues are each ≥ 500 bps off in one sample; one level down after 12 samples below 80% of the level's threshold; unknown with fewer than 2 distinct venues"},
	{"S2", "dispersion", "Liquidity", "sampled", "spread between the highest and lowest included input ≥ 25 / 100 bps (watch / warning) in 3 consecutive samples; needs ≥ 3 included inputs, else unknown"},
	{"S3", "dex", "Liquidity", "sampled", "Uniswap v3 USDC/USDT |deviation| ≥ 30 / 100 bps (watch / warning) in 3 consecutive samples, attributed to the weaker coin of the pair (the Curve half is v1.1)"},
	{"S4", "flows", "Flows", "level", "on the rolling median of the last 3 daily supply readings: 24h ≤ −0.5% or 7d ≤ −2% (watch) / 24h ≤ −1% or 7d ≤ −4% (warning) / 24h ≤ −3% or 7d ≤ −8% (critical); mints are informational"},
	{"S5", "coverage", "Reserves", "level", "Tether total assets ÷ total liabilities < 1.010 (watch), < 1.000 (critical); n/a for Circle coins, where S6 governs"},
	{"S6", "disclosure", "Disclosure", "level", "age of the latest attestation recorded by BigLens: watch after cadence + p90 of the issuer's publication lag (published − period end) + 7 days, warning 30 days later; with fewer than 3 reports, monthly > 65 / > 95 days and quarterly > 150 / > 180 days; warning if the latest report shows reserves below liabilities; over the threshold without a maintainer check (attestations_checked) in the last 14 days, S6 is unknown and S13 reports curated facts that may be stale"},
	{"S8", "control", "Contract control", "confirmed", "critical if paused (or USDT deprecated) on any monitored deployment; warning if owner, admin, implementation, master minter, pauser, blacklister or an SPL mint/freeze authority changed in the last 7 days; changes are confirmed by 2 samples or 2 RPC endpoints"},
	{"S10", "chain", "Chain / bridge", "level", "bridged representations hold ≥ 10% of the supply on monitored chains (watch); otherwise info with the largest chain's share"},
	{"S11", "regulatory", "Regulation", "level", "not authorized under EU MiCA = watch, for EU users; other statuses are informational; curated facts only, never inferred"},
	{"S12", "reconcile", "Data integrity", "confirmed", "|issuer − DefiLlama| ÷ issuer > 0.5% (watch) / > 2% (warning), confirmed by 2 consecutive supply samples; issuer circulating above a chain's on-chain total by more than max(0.1%, 1M units), confirmed (warning)"},
	{"S13", "data", "Data health", "sampled", "a core source older than 3× its interval or a stalled sampler (watch); all peg venues failing (warning); curated facts may be stale (watch); Tether per-chain sum mismatch (watch); 3 consecutive samples to enter, 12 to leave"},
}

// stableSignalRuleFor adds the asset-specific clause of a rule.
func stableSignalRuleFor(def stableSignalDef, asset string) string {
	if def.ID == "S1" && asset == "eurc" {
		return def.Rule + "; EURC is measured in euros and capped at warning without a second venue quoting EURC in euros"
	}
	return def.Rule
}

// StableSignal is one cell of the risk matrix (§9).
type StableSignal struct {
	ID        string         `json:"id"`
	Key       string         `json:"key"`
	Dimension string         `json:"dimension"`
	Policy    string         `json:"policy"` // sampled | confirmed | level
	State     string         `json:"state"`  // normal | info | watch | warning | critical | unknown | n/a
	Reason    string         `json:"reason,omitempty"`
	Rule      string         `json:"rule"`
	Summary   string         `json:"summary"`
	Since     string         `json:"since,omitempty"`
	AsOf      string         `json:"as_of,omitempty"`
	Inputs    map[string]any `json:"inputs"`
}

// stableRaw is one evaluation of a signal on its inputs, before the policy.
type stableRaw struct {
	Level     int      // stableLevelUnknown, or normal … critical
	Value     *float64 // the measure compared with the thresholds (numeric sampled signals)
	Immediate bool     // S1: immediate critical
	Cap       int      // highest level this sample may reach (EURC S1: warning)
	Info      bool     // level 0 shows as info (noteworthy, not a risk)
	NA        bool     // the signal does not apply to the asset
	Reason    string   // why the reading is unknown or n/a
	Summary   string
	Inputs    map[string]any
	AsOf      time.Time
}

func newStableRaw() stableRaw {
	return stableRaw{Cap: stableLevelCritical, Inputs: map[string]any{}}
}

func stableUnknownRaw(r stableRaw, reason string) stableRaw {
	r.Level, r.Reason = stableLevelUnknown, reason
	return r
}

// stableRawState is the state a level-triggered reading shows.
func stableRawState(r stableRaw) (string, string) {
	switch {
	case r.NA:
		return stableStateNA, r.Reason
	case r.Level < 0:
		reason := r.Reason
		if reason == "" {
			reason = stableReasonWarmingUp
		}
		return stableStateUnknown, reason
	case r.Level == stableLevelNormal && r.Info:
		return stableStateInfo, ""
	}
	return stableLevelNames[min(r.Level, stableLevelCritical)], ""
}

// stableLevelFor maps a non-negative measure to a level; every threshold is
// inclusive, with the same float tolerance as the depeg-day counts (§5.2).
func stableLevelFor(v float64, thr [4]float64) int {
	lvl := stableLevelNormal
	for k := stableLevelWatch; k <= stableLevelCritical; k++ {
		if thr[k] > 0 && stableBpsAtLeast(v, thr[k]) {
			lvl = k
		}
	}
	return lvl
}

func stableRound(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func stableRoundPtr(v *float64, digits int) *float64 {
	if v == nil {
		return nil
	}
	return stableF(stableRound(*v, digits))
}

// --- sampled signals: S1, S2, S3, S13 ---

// stableVenuePrices is each venue's price in a sample: the median of its
// included inputs.
func stableVenuePrices(p StablePeg) map[string]float64 {
	mids := map[string][]float64{}
	for _, q := range p.Quotes {
		if q.Included && q.Mid != nil {
			mids[q.Venue] = append(mids[q.Venue], *q.Mid)
		}
	}
	out := make(map[string]float64, len(mids))
	for v, m := range mids {
		out[v] = stableMedian(m)
	}
	return out
}

func stablePegAsOf(p StablePeg) time.Time {
	t, err := time.Parse(time.RFC3339, p.AsOf)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// stableS1Raw: peg deviation of the consensus (§6.2 S1).
func stableS1Raw(p StablePeg) stableRaw {
	r := newStableRaw()
	r.AsOf = stablePegAsOf(p)
	r.Inputs["numeraire"], r.Inputs["consensus"], r.Inputs["dev_bps"] = p.Numeraire, p.Consensus, stableRoundPtr(p.DevBps, 2)
	r.Inputs["venues_used"], r.Inputs["inputs_used"] = p.VenuesUsed, p.InputsUsed
	if p.SecondDirectEURVenue != nil {
		r.Inputs["second_direct_eur_venue"] = *p.SecondDirectEURVenue
		if !*p.SecondDirectEURVenue {
			r.Cap = stableLevelWarning
		}
	}
	if p.Consensus == nil || p.DevBps == nil {
		return stableUnknownRaw(r, "fewer than 2 distinct venues")
	}
	dev := math.Abs(*p.DevBps)
	r.Value = &dev
	r.Level = stableLevelFor(dev, stableS1Thr)
	venueDev := map[string]float64{}
	off := 0
	for v, px := range stableVenuePrices(p) {
		d := (px - 1) * 1e4
		venueDev[v] = stableRound(d, 2)
		if stableBpsAtLeast(d, stableS1ImmediateBps) {
			off++
		}
	}
	r.Inputs["venue_dev_bps"] = venueDev
	r.Immediate = off >= 2
	r.Inputs["immediate"] = r.Immediate
	if r.Level > r.Cap {
		r.Level = r.Cap
	}
	unit := "bps"
	if p.Numeraire == "EUR" {
		unit = "bps vs €1"
	}
	r.Summary = fmt.Sprintf("consensus %.5f (%+.1f %s) from %d venues", *p.Consensus, *p.DevBps, unit, p.VenuesUsed)
	if r.Cap < stableLevelCritical {
		r.Summary += "; capped at warning (no second venue quoting EURC in euros)"
	}
	return r
}

// stableS2Raw: dispersion of the included inputs (§6.2 S2).
func stableS2Raw(p StablePeg) stableRaw {
	r := newStableRaw()
	r.AsOf = stablePegAsOf(p)
	r.Inputs["disp_bps"], r.Inputs["inputs_used"], r.Inputs["min_inputs"] = stableRoundPtr(p.DispBps, 2), p.InputsUsed, stableS2MinInputs
	if p.InputsUsed < stableS2MinInputs || p.DispBps == nil {
		return stableUnknownRaw(r, fmt.Sprintf("%d included inputs (needs %d)", p.InputsUsed, stableS2MinInputs))
	}
	v := *p.DispBps
	r.Value, r.Level = &v, stableLevelFor(v, stableS2Thr)
	r.Summary = fmt.Sprintf("inputs span %.1f bps across %d inputs", v, p.InputsUsed)
	return r
}

// stableS3Raw: the Uniswap v3 USDC/USDT price, attributed to the weaker
// coin of the pair (§6.2 S3). The pool prices USDC in USDT, so a price below
// 1 means USDC is the weaker coin.
func stableS3Raw(asset string, rel StableRelativePeg) stableRaw {
	r := newStableRaw()
	if asset != "usdt" && asset != "usdc" {
		r.NA, r.Reason = true, "no EURC pool is monitored in v1"
		return r
	}
	r.AsOf, _ = time.Parse(time.RFC3339, rel.AsOf)
	r.Inputs["pool"] = stableUniswapPair
	var uni *StableQuote
	for i := range rel.Quotes {
		if rel.Quotes[i].Kind == "dex" {
			uni = &rel.Quotes[i]
		}
	}
	if uni == nil || !uni.Included || uni.Mid == nil {
		why := "Uniswap pool price unavailable"
		if uni != nil && uni.ExcludeReason != "" {
			why += " (" + uni.ExcludeReason + ")"
		}
		return stableUnknownRaw(r, why)
	}
	dev := (*uni.Mid - 1) * 1e4
	weaker := ""
	switch {
	case dev < 0:
		weaker = "usdc"
	case dev > 0:
		weaker = "usdt"
	}
	v := 0.0
	if asset == weaker {
		v = math.Abs(dev)
	}
	r.Value, r.Level = &v, stableLevelFor(v, stableS3Thr)
	r.Inputs["usdc_in_usdt"], r.Inputs["pool_dev_bps"], r.Inputs["weaker"] = *uni.Mid, stableRound(dev, 2), weaker
	r.Summary = fmt.Sprintf("Uniswap v3 USDC/USDT %.6f (%+.1f bps)", *uni.Mid, dev)
	if weaker != "" {
		r.Summary += "; " + strings.ToUpper(weaker) + " is the weaker coin"
	}
	return r
}

// stableHealthInput carries an asset's S13 inputs.
type stableHealthInput struct {
	StaleSources      []string // core sources with no success for 3 intervals
	StalledSamplers   []string
	VenuesFailing     bool // every enabled peg venue failed in the sample
	FactsStale        bool // S6: curated facts may be stale
	TetherSumMismatch bool // Tether per-chain circulating ≠ total liabilities
}

// stableS13Raw: data health (§6.2 S13), a categorical signal.
func stableS13Raw(h stableHealthInput) stableRaw {
	r := newStableRaw()
	var notes []string
	if h.VenuesFailing {
		r.Level = stableLevelWarning
		notes = append(notes, "all peg venues failing")
	}
	watch := func(note string) {
		r.Level = max(r.Level, stableLevelWatch)
		notes = append(notes, note)
	}
	if len(h.StaleSources) > 0 {
		watch("no success for 3 intervals: " + strings.Join(h.StaleSources, ", "))
	}
	if len(h.StalledSamplers) > 0 {
		watch("stalled samplers: " + strings.Join(h.StalledSamplers, ", "))
	}
	if h.FactsStale {
		watch("curated facts may be stale (S6)")
	}
	if h.TetherSumMismatch {
		watch("Tether per-chain circulating does not sum to total liabilities")
	}
	r.Inputs["stale_sources"], r.Inputs["stalled_samplers"] = stableNonNil(h.StaleSources), stableNonNil(h.StalledSamplers)
	r.Inputs["venues_failing"], r.Inputs["facts_stale"], r.Inputs["tether_sum_mismatch"] = h.VenuesFailing, h.FactsStale, h.TetherSumMismatch
	r.Summary = "all core sources healthy"
	if len(notes) > 0 {
		r.Summary = strings.Join(notes, "; ")
	}
	return r
}

func stableNonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// stableFilterReasons are quote outcomes of a venue that answered.
var stableFilterReasons = map[string]bool{"": true, "stale": true, "wide_spread": true, "low_volume": true, "fx_unavailable": true,
	"leg_excluded": true}

// stableVenuesFailing reports whether every enabled exchange failed in a
// sample: each of its markets carries a fetch error. Uniswap, disabled and
// paused venues do not count.
func stableVenuesFailing(res *stablePegResult) bool {
	if res == nil {
		return false
	}
	answered := map[string]bool{}
	for _, q := range res.Markets {
		if q.Venue == "uniswap" || q.ExcludeReason == "disabled" || q.ExcludeReason == "paused" {
			continue
		}
		answered[q.Venue] = answered[q.Venue] || stableFilterReasons[q.ExcludeReason]
	}
	if len(answered) == 0 {
		return false
	}
	for _, ok := range answered {
		if ok {
			return false
		}
	}
	return true
}

// --- level-triggered signals: S4, S5, S6, S10, S11 ---

type stableFlowPoint struct {
	Day   string // YYYY-MM-DD
	Value float64
}

// stableFlowInput is one asset's daily supply series for S4.
type stableFlowInput struct {
	Source string // issuer | aggregator
	Points []stableFlowPoint
}

// stableS4Raw: supply flows on the rolling median of the last 3 daily
// readings (§6.2 S4). The latest day is compared with the medians of the day
// before and of 7 days before.
func stableS4Raw(in stableFlowInput) stableRaw {
	r := newStableRaw()
	r.Inputs["source"], r.Inputs["median_points"] = in.Source, stableS4MedianPoints
	if len(in.Points) < stableS4MedianPoints+1 {
		return stableUnknownRaw(r, stableReasonWarmingUp)
	}
	med := map[string]float64{}
	for i := stableS4MedianPoints - 1; i < len(in.Points); i++ {
		w := make([]float64, 0, stableS4MedianPoints)
		for j := i - stableS4MedianPoints + 1; j <= i; j++ {
			w = append(w, in.Points[j].Value)
		}
		med[in.Points[i].Day] = stableMedian(w)
	}
	last := in.Points[len(in.Points)-1]
	day, err := time.Parse(time.DateOnly, last.Day)
	if err != nil {
		return stableUnknownRaw(r, "bad day "+last.Day)
	}
	r.AsOf = day
	cur := med[last.Day]
	change := func(back int) *float64 {
		prev, ok := med[day.AddDate(0, 0, -back).Format(time.DateOnly)]
		if !ok || !(prev > 0) {
			return nil
		}
		return stableF((cur/prev - 1) * 100)
	}
	c24, c7 := change(1), change(7)
	r.Inputs["day"], r.Inputs["median_native"] = last.Day, stableRound(cur, 0)
	r.Inputs["change_24h_pct"], r.Inputs["change_7d_pct"] = stableRoundPtr(c24, 3), stableRoundPtr(c7, 3)
	if c24 == nil && c7 == nil {
		return stableUnknownRaw(r, stableReasonWarmingUp)
	}
	at := func(v *float64, thr float64) bool { return v != nil && *v <= thr+1e-9 }
	switch {
	case at(c24, stableS4Critical24hPct) || at(c7, stableS4Critical7dPct):
		r.Level = stableLevelCritical
	case at(c24, stableS4Warning24hPct) || at(c7, stableS4Warning7dPct):
		r.Level = stableLevelWarning
	case at(c24, stableS4Watch24hPct) || at(c7, stableS4Watch7dPct):
		r.Level = stableLevelWatch
	}
	f := func(v *float64) string {
		if v == nil {
			return "—"
		}
		return fmt.Sprintf("%+.2f%%", *v)
	}
	r.Summary = fmt.Sprintf("%s supply (3-day median): 24h %s, 7d %s", in.Source, f(c24), f(c7))
	return r
}

// stableS5Raw: Tether's coverage ratio (§6.2 S5).
func stableS5Raw(asset string, t *StableTetherPoint) stableRaw {
	r := newStableRaw()
	if asset != "usdt" {
		r.NA, r.Reason = true, "Circle publishes no machine-readable balance sheet; S6 governs"
		return r
	}
	if t == nil || t.Coverage == nil {
		return stableUnknownRaw(r, stableReasonWarmingUp)
	}
	r.AsOf, _ = time.Parse(time.RFC3339, t.AsOf)
	c := *t.Coverage
	r.Inputs["coverage"], r.Inputs["update_as_of"] = stableRound(c, 5), t.AsOf
	r.Inputs["total_assets_usd"], r.Inputs["total_liabilities_usd"], r.Inputs["shareholder_equity_usd"] = t.TotalAssetsUSD, t.TotalLiabilitiesUSD, t.ShareholderEquityUSD
	switch {
	case c < stableS5CriticalCoverage:
		r.Level = stableLevelCritical
	case c < stableS5WatchCoverage:
		r.Level = stableLevelWatch
	}
	r.Summary = fmt.Sprintf("total assets ÷ liabilities %.4f (Tether update %s)", c, t.AsOf)
	return r
}

// stableS6Threshold is the age (days since period_end) after which S6
// goes to watch and to warning.
type stableS6Threshold struct {
	WatchAfterDays, WarningAfterDays int
	Method                           string // data-driven | fallback
	History                          int    // reports with both dates
	LagP90Days                       *int   // data-driven only
}

// stableS6Thresholds derives an asset's S6 thresholds (§6.2): cadence + the
// 90th percentile (nearest rank) of its publication lags + 7 days when at
// least 3 reports are recorded, else the fixed monthly or quarterly
// fallbacks. ok is false when neither applies.
func stableS6Thresholds(f stableAssetFacts) (stableS6Threshold, bool) {
	var lags []int
	for _, a := range f.Attestations {
		pe, err1 := time.Parse(time.DateOnly, a.PeriodEnd)
		pub, err2 := time.Parse(time.DateOnly, a.Published)
		if err1 != nil || err2 != nil || pub.Before(pe) {
			continue
		}
		lags = append(lags, int(pub.Sub(pe).Hours()/24))
	}
	th := stableS6Threshold{History: len(lags)}
	if len(lags) >= stableS6MinHistory && f.CadenceDays > 0 {
		sort.Ints(lags)
		p90 := lags[int(math.Ceil(stableS6LagPercentile*float64(len(lags))))-1]
		th.Method, th.LagP90Days = "data-driven", &p90
		th.WatchAfterDays = f.CadenceDays + p90 + stableS6SlackDays
		th.WarningAfterDays = th.WatchAfterDays + stableS6WarningAfterWatchDays
		return th, true
	}
	th.Method = "fallback"
	switch {
	case f.CadenceDays > 0 && f.CadenceDays <= 45:
		th.WatchAfterDays, th.WarningAfterDays = stableS6MonthlyFallbackWatchDays, stableS6MonthlyFallbackWarningDays
	case f.CadenceDays > 45 && f.CadenceDays <= 100:
		th.WatchAfterDays, th.WarningAfterDays = stableS6QuarterlyFallbackWatchDays, stableS6QuarterlyFallbackWarningDays
	default:
		return th, false
	}
	return th, true
}

// stableS6Raw: disclosure age (§6.2 S6). It also reports whether the
// curated facts may be stale, which S13 shows.
func stableS6Raw(f stableAssetFacts, now time.Time) (stableRaw, bool) {
	r := newStableRaw()
	if f.Status == stableFactsUnknown || len(f.Attestations) == 0 {
		return stableUnknownRaw(r, "no attestation recorded by BigLens"), false
	}
	latest := f.Attestations[0]
	for _, a := range f.Attestations[1:] {
		if a.PeriodEnd > latest.PeriodEnd {
			latest = a
		}
	}
	pe, err := time.Parse(time.DateOnly, latest.PeriodEnd)
	if err != nil {
		return stableUnknownRaw(r, "bad period_end"), false
	}
	r.AsOf = pe
	age := int(math.Floor(now.Sub(pe).Hours() / 24))
	th, ok := stableS6Thresholds(f)
	if !ok {
		return stableUnknownRaw(r, fmt.Sprintf("cadence of %d days has no fallback thresholds and fewer than %d reports are recorded", f.CadenceDays,
			stableS6MinHistory)), false
	}
	watchD, warnD := th.WatchAfterDays, th.WarningAfterDays
	r.Inputs["period_end"], r.Inputs["published"], r.Inputs["firm"], r.Inputs["type"], r.Inputs["url"] = latest.PeriodEnd, latest.Published, latest.Firm, latest.Type, latest.URL
	r.Inputs["age_days"], r.Inputs["cadence_days"] = age, f.CadenceDays
	r.Inputs["watch_after_days"], r.Inputs["warning_after_days"], r.Inputs["attestations_checked"] = watchD, warnD, f.AttestationsChecked
	r.Inputs["threshold_method"], r.Inputs["history_count"], r.Inputs["lag_p90_days"] = th.Method, th.History, th.LagP90Days
	r.Summary = fmt.Sprintf("Latest attestation recorded by BigLens: period ending %s (%d days ago, %s, %s)", latest.PeriodEnd, age, latest.Firm, latest.Type)
	shortfall := false
	if res, err1 := strconv.ParseFloat(latest.ReservesNative, 64); err1 == nil {
		if liab, err2 := strconv.ParseFloat(latest.LiabilitiesNative, 64); err2 == nil {
			r.Inputs["reserves_native"], r.Inputs["liabilities_native"] = latest.ReservesNative, latest.LiabilitiesNative
			shortfall = res < liab
		}
	}
	if shortfall {
		r.Level = stableLevelWarning
		r.Summary += "; the report shows reserves below liabilities"
	}
	if age <= watchD {
		return r, false
	}
	checked, err := time.Parse(time.DateOnly, f.AttestationsChecked)
	if err != nil || now.Sub(checked) > stableS6CheckedWithin {
		if shortfall {
			return r, true
		}
		return stableUnknownRaw(r, fmt.Sprintf("curated facts may be stale: the latest attestation recorded by BigLens is %d days old and no maintainer check is recorded in the last 14 days", age)), true
	}
	lvl := stableLevelWatch
	if age > warnD {
		lvl = stableLevelWarning
	}
	r.Level = max(r.Level, lvl)
	return r, false
}

// stableS8WarningFields are the control fields whose change is a warning;
// other changes (rescuer, USDT upgradedAddress, an unpause) are info.
var stableS8WarningFields = map[string]bool{"owner": true, "admin": true, "implementation": true, "master_minter": true,
	"pauser": true, "blacklister": true, "mint_authority": true, "freeze_authority": true}

// stableS8Raw: contract control (§6.2 S8), on confirmed state only.
func stableS8Raw(asset string, states map[string]stableControlState, events []stableControlEvent, enabled bool, now time.Time) stableRaw {
	r := newStableRaw()
	if !enabled {
		return stableUnknownRaw(r, "rpc_controls is disabled")
	}
	var missing, critical, deployments []string
	for _, d := range stableControlDeployments {
		if d.Asset != asset {
			continue
		}
		deployments = append(deployments, d.Chain)
		for _, f := range stableControlFields[d.Kind] {
			st, ok := states[asset+"|"+d.Chain+"|"+f.Name]
			if !ok || st.Value == nil {
				missing = append(missing, d.Chain+"."+f.Name)
				continue
			}
			if st.LastSeen.After(r.AsOf) {
				r.AsOf = st.LastSeen
			}
			if f.Critical && *st.Value == "true" {
				critical = append(critical, d.Chain+"."+f.Name)
			}
		}
	}
	var warn, info []string
	for _, ev := range events {
		if ev.Asset != asset || now.Sub(ev.DetectedAt) > stableS8Window || ev.DetectedAt.After(now) {
			continue
		}
		label := fmt.Sprintf("%s.%s changed %s", ev.Chain, ev.Field, ev.DetectedAt.UTC().Format("2006-01-02 15:04"))
		if stableS8WarningFields[ev.Field] {
			warn = append(warn, label)
		} else {
			info = append(info, label)
		}
	}
	r.Inputs["deployments"], r.Inputs["not_baselined"], r.Inputs["critical_flags"] = deployments, stableNonNil(missing), stableNonNil(critical)
	r.Inputs["changes_7d"], r.Inputs["info_changes_7d"] = stableNonNil(warn), stableNonNil(info)
	switch {
	case len(critical) > 0:
		r.Level, r.Summary = stableLevelCritical, "set on a monitored deployment: "+strings.Join(critical, ", ")
	case len(missing) > 0:
		return stableUnknownRaw(r, stableReasonWarmingUp+": no confirmed baseline yet for "+strings.Join(missing, ", "))
	case len(warn) > 0:
		r.Level, r.Summary = stableLevelWarning, strings.Join(warn, "; ")
	case len(info) > 0:
		r.Info, r.Summary = true, strings.Join(info, "; ")
	default:
		r.Summary = fmt.Sprintf("no changes in 7 days on %d monitored deployments", len(deployments))
	}
	return r
}

// stableS10Raw: chain and bridge exposure (§6.2 S10). The monitored supply
// is each registry deployment's issuer circulating (on-chain total when the
// issuer does not report the chain) plus the bridged representations'
// on-chain totals.
func stableS10Raw(a *StableSupplyAsset) stableRaw {
	r := newStableRaw()
	if a == nil {
		return stableUnknownRaw(r, stableReasonWarmingUp)
	}
	var native, bridged float64
	var skipped []string
	for _, row := range a.Chains {
		if !row.Verified {
			continue
		}
		v := row.IssuerCirculatingNative
		if row.Tier == string(tierBridged) || v == nil {
			v = row.OnchainTotalNative
		}
		if v == nil {
			skipped = append(skipped, row.Chain)
			continue
		}
		if row.Tier == string(tierBridged) {
			bridged += *v
		} else {
			native += *v
		}
	}
	var largest *StableSupplyRow
	for i := range a.Chains {
		if row := &a.Chains[i]; row.IssuerSharePct != nil && (largest == nil || *row.IssuerSharePct > *largest.IssuerSharePct) {
			largest = row
		}
	}
	total := native + bridged
	r.Inputs["monitored_native"], r.Inputs["monitored_bridged"], r.Inputs["skipped_chains"] = stableRound(native, 0), stableRound(bridged, 0), stableNonNil(skipped)
	if largest != nil {
		r.Inputs["largest_chain"], r.Inputs["largest_chain_share_pct"] = largest.Chain, stableRound(*largest.IssuerSharePct, 2)
	}
	if !(total > 0) {
		return stableUnknownRaw(r, "no supply on monitored chains yet")
	}
	share := bridged / total * 100
	r.Inputs["bridged_share_pct"] = stableRound(share, 3)
	r.Summary = fmt.Sprintf("bridged representations hold %.2f%% of the supply on monitored chains", share)
	if largest != nil {
		r.Summary += fmt.Sprintf("; largest chain %s %.1f%% of issuer circulating", largest.ChainLabel, *largest.IssuerSharePct)
	}
	if share >= stableS10BridgedWatchPct {
		r.Level = stableLevelWatch
	} else {
		r.Info = true
	}
	return r
}

// stableS11Raw: regulatory status from the curated facts (§6.2 S11).
func stableS11Raw(f stableAssetFacts) stableRaw {
	r := newStableRaw()
	rows := map[string]stableRegulatory{}
	for _, reg := range f.Regulatory {
		rows[reg.Regime] = reg
		r.Inputs[reg.Regime] = map[string]any{"status": reg.Status, "entity": reg.Entity, "as_of": reg.AsOf, "source_url": reg.SourceURL}
	}
	eu, ok := rows["EU-MiCA"]
	if !ok || eu.Status == "unknown" {
		return stableUnknownRaw(r, "EU MiCA status not curated")
	}
	r.AsOf, _ = time.Parse(time.DateOnly, eu.AsOf)
	if eu.Status == "not_authorized" {
		r.Level, r.Summary = stableLevelWatch, fmt.Sprintf("not authorized under EU MiCA (as of %s): restricted for EU users", eu.AsOf)
	} else {
		r.Info, r.Summary = true, fmt.Sprintf("EU MiCA: %s (%s, as of %s)", strings.ReplaceAll(eu.Status, "_", " "), eu.Entity, eu.AsOf)
	}
	if us, ok := rows["US-GENIUS"]; ok {
		r.Summary += fmt.Sprintf("; US GENIUS: %s (as of %s)", strings.ReplaceAll(us.Status, "_", " "), us.AsOf)
	}
	return r
}

// --- confirmed signal S12 (aggregator half; the on-chain half is confirmed upstream) ---

func stableS12AggRaw(a StableSupplyAsset) stableRaw {
	r := newStableRaw()
	ra := a.Reconcile.Aggregator
	r.Inputs["diff_pct"], r.Inputs["issuer_native"], r.Inputs["aggregator_native"] = stableRoundPtr(ra.DiffPct, 4), ra.IssuerNative, ra.AggregatorNative
	r.Inputs["skew_seconds"] = ra.SkewSeconds
	if ra.SkipReason != "" || ra.DiffPct == nil {
		reason := ra.SkipReason
		if reason == "" {
			reason = "no comparison"
		}
		return stableUnknownRaw(r, reason)
	}
	switch {
	case ra.AboveWarning:
		r.Level = stableLevelWarning
	case ra.AboveWatch:
		r.Level = stableLevelWatch
	}
	r.Summary = fmt.Sprintf("DefiLlama vs issuer %+.3f%%", *ra.DiffPct)
	return r
}

func stableS12OnchainRaw(a StableSupplyAsset) stableRaw {
	r := newStableRaw()
	var compared, confirmed, skipped []string
	for _, o := range a.Reconcile.Onchain {
		if o.SkipReason != "" {
			skipped = append(skipped, o.Chain+" ("+o.SkipReason+")")
			continue
		}
		compared = append(compared, o.Chain)
		if o.Confirmed {
			confirmed = append(confirmed, o.Chain)
		}
	}
	r.Inputs["onchain_compared"], r.Inputs["onchain_confirmed_excess"], r.Inputs["onchain_skipped"] = stableNonNil(compared), stableNonNil(confirmed), stableNonNil(skipped)
	if len(compared) == 0 {
		return stableUnknownRaw(r, "no issuer/on-chain pair compared")
	}
	if len(confirmed) > 0 {
		r.Level = stableLevelWarning
		r.Summary = "issuer circulating exceeds the on-chain total on " + strings.Join(confirmed, ", ")
	} else {
		r.Summary = "issuer circulating within the on-chain total on " + strings.Join(compared, ", ")
	}
	return r
}

// --- policies ---

// stableHyst is the policy state of one signal (stable_signal_state.hyst_json).
type stableHyst struct {
	// Sampled policy.
	Level     int       `json:"level"`
	Samples   int       `json:"samples"` // known samples since the last restart
	Up        [4]int    `json:"up"`      // consecutive samples at or above each level
	Down      int       `json:"down"`    // consecutive samples below the exit threshold
	LastKnown time.Time `json:"last_known"`
	// Confirmed policy (Level holds the confirmed level).
	Pending      int  `json:"pending"`
	PendingCount int  `json:"pending_count"`
	Confirmed    bool `json:"confirmed"`
}

// stableSampledStep applies one sample under the sampled policy (§6.1). A
// level is entered after 3 consecutive samples at or above it, or at once on
// an immediate-critical sample, and left one level at a time after 12
// consecutive samples below 80% of the current level's threshold (below the
// current level for categorical signals, thr all zero). An unknown sample
// shows as unknown and leaves the counters alone; known samples more than
// 15 minutes apart restart the signal (warming up).
func stableSampledStep(h stableHyst, raw stableRaw, thr [4]float64, at time.Time) (stableHyst, string, string) {
	if !h.LastKnown.IsZero() && at.Sub(h.LastKnown) > stableSampledMaxGap {
		h = stableHyst{}
	}
	if raw.Level < 0 {
		reason := raw.Reason
		if reason == "" {
			reason = "no data"
		}
		return h, stableStateUnknown, reason
	}
	numeric := thr[stableLevelWatch] > 0
	h.LastKnown = at
	if h.Samples < math.MaxInt32 {
		h.Samples++
	}
	for k := stableLevelWatch; k <= stableLevelCritical; k++ {
		if raw.Level >= k {
			h.Up[k]++
		} else {
			h.Up[k] = 0
		}
	}
	moved := false
	for k := stableLevelCritical; k > h.Level; k-- {
		if h.Up[k] >= stableSampledEnter {
			h.Level, h.Down, moved = k, 0, true
			break
		}
	}
	if raw.Immediate && h.Level < stableLevelCritical {
		h.Level, h.Down, moved = stableLevelCritical, 0, true
	}
	if raw.Cap > 0 && h.Level > raw.Cap {
		h.Level, h.Down = raw.Cap, 0
	}
	if !moved && h.Level > stableLevelNormal {
		below := raw.Level < h.Level
		if numeric {
			below = raw.Value != nil && *raw.Value < stableSampledExitFrac*thr[h.Level]
		}
		if below {
			h.Down++
		} else {
			h.Down = 0
		}
		if h.Down >= stableSampledExit {
			h.Level, h.Down = h.Level-1, 0
		}
	}
	if h.Samples < stableSampledEnter && h.Level == stableLevelNormal {
		return h, stableStateUnknown, stableReasonWarmingUp
	}
	return h, stableLevelNames[h.Level], ""
}

// stableConfirmStep applies one sample under the confirmed policy (§6.1):
// the level changes only when 2 consecutive samples agree on the new level
// (stableLevelUnknown included). ok is false until the first 2 samples agree.
func stableConfirmStep(h stableHyst, level int) (stableHyst, int, bool) {
	if h.Confirmed && level == h.Level {
		h.PendingCount = 0
		return h, h.Level, true
	}
	if h.PendingCount > 0 && h.Pending == level {
		h.PendingCount++
	} else {
		h.Pending, h.PendingCount = level, 1
	}
	if h.PendingCount >= stableConfirmSamples {
		h.Level, h.Confirmed, h.PendingCount = level, true, 0
	}
	return h, h.Level, h.Confirmed
}

// stableWorstState is an asset's summary (§6.1): the worst state across its
// signals, with info counted as normal and n/a ignored. unknown ranks above
// normal, so missing data never reads as normal: it is the summary when no
// signal is at watch or above and one is unknown.
func stableWorstState(states []string) string {
	rank := map[string]int{stableStateNormal: 0, stableStateInfo: 0, stableStateUnknown: 1, stableStateWatch: 2, stableStateWarning: 3,
		stableStateCritical: 4}
	best, out := -1, stableStateUnknown
	for _, s := range states {
		if r, ok := rank[s]; ok && r > best {
			best, out = r, s
		}
	}
	if out == stableStateInfo {
		return stableStateNormal
	}
	return out
}

// stableAlertCounts counts states at watch or above across assets (§7.1).
func stableAlertCounts(states []string) map[string]int {
	out := map[string]int{stableStateCritical: 0, stableStateWarning: 0, stableStateWatch: 0}
	for _, s := range states {
		if _, ok := out[s]; ok {
			out[s]++
		}
	}
	return out
}
