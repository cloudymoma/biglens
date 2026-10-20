package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

// sigBps is a numeric raw reading for the sampled-policy tests.
func sigBps(dev float64, thr [4]float64) stableRaw {
	v := math.Abs(dev)
	return stableRaw{Level: stableLevelFor(v, thr), Value: &v, Cap: stableLevelCritical}
}

func TestStableLevelForBoundaries(t *testing.T) {
	for _, c := range []struct {
		dev  float64
		want int
	}{{0, 0}, {29.99, 0}, {30, 1}, {(1.003 - 1) * 1e4, 1}, {99.99, 1}, {100, 2}, {299.99, 2}, {300, 3}, {1947, 3}} {
		if got := stableLevelFor(c.dev, stableS1Thr); got != c.want {
			t.Errorf("S1 level(%v bps) = %d, want %d", c.dev, got, c.want)
		}
	}
	for _, c := range []struct {
		disp float64
		want int
	}{{24.99, 0}, {25, 1}, {99.99, 1}, {100, 2}, {5000, 2}} {
		if got := stableLevelFor(c.disp, stableS2Thr); got != c.want {
			t.Errorf("S2 level(%v bps) = %d, want %d", c.disp, got, c.want)
		}
	}
}

func TestStableS1Raw(t *testing.T) {
	for _, c := range []struct {
		dev  float64
		want int
	}{{-11, 0}, {-30, 1}, {30, 1}, {-100, 2}, {-389, 3}} {
		p := StablePeg{Asset: "usdt", Numeraire: "USD", State: "ok", Consensus: stableF(1 + c.dev/1e4), DevBps: stableF(c.dev)}
		if r := stableS1Raw(p); r.Level != c.want || r.Value == nil || math.Abs(*r.Value-math.Abs(c.dev)) > 1e-9 {
			t.Errorf("S1(%v bps) = level %d value %v, want %d", c.dev, r.Level, r.Value, c.want)
		}
	}
	// Fewer than 2 distinct venues: no consensus, so S1 is unknown (§6.2 S13).
	single := StablePeg{Asset: "usdt", Numeraire: "USD", State: "single_venue", SingleVenuePrice: stableF(0.9)}
	if r := stableS1Raw(single); r.Level != stableLevelUnknown || !strings.Contains(r.Reason, "2 distinct venues") {
		t.Errorf("single venue S1 = %d %q, want unknown (fewer than 2 distinct venues)", r.Level, r.Reason)
	}

	// Immediate critical: 2 included venues each ≥ 500 bps off in one sample.
	mk := func(asset string, mids map[string]float64) StablePeg {
		p := StablePeg{Asset: asset, Numeraire: "USD", AsOf: "2026-10-20T12:00:00Z"}
		for _, v := range []string{"bitstamp", "coinbase", "kraken"} {
			if m, ok := mids[v]; ok {
				p.Quotes = append(p.Quotes, StableQuote{Venue: v, Pair: v, Kind: "direct", Mid: stableF(m), VolUSD: stableF(1e6), Included: true})
			}
		}
		stableFinishPeg(&p)
		return p
	}
	if r := stableS1Raw(mk("usdc", map[string]float64{"kraken": 0.95, "bitstamp": 0.9495, "coinbase": 0.999})); !r.Immediate {
		t.Errorf("two venues ≥ 500 bps off: immediate = false, want true (inputs %v)", r.Inputs)
	}
	if r := stableS1Raw(mk("usdc", map[string]float64{"kraken": 0.94, "bitstamp": 0.999, "coinbase": 0.9995})); r.Immediate {
		t.Error("one venue ≥ 500 bps off: immediate = true, want false")
	}
	if r := stableS1Raw(mk("usdc", map[string]float64{"kraken": 0.9501, "bitstamp": 0.95})); r.Immediate {
		t.Error("499 and 500 bps: immediate = true, want false (each venue must be ≥ 500 bps off)")
	}

	// EURC is capped at warning without a second direct EUR venue (§5.3).
	f, tr := false, true
	eurc := StablePeg{Asset: "eurc", Numeraire: "EUR", State: "ok", Consensus: stableF(0.95), DevBps: stableF(-500), SecondDirectEURVenue: &f}
	if r := stableS1Raw(eurc); r.Level != stableLevelWarning || r.Cap != stableLevelWarning {
		t.Errorf("EURC without a second direct venue: level %d cap %d, want warning/warning", r.Level, r.Cap)
	}
	eurc.SecondDirectEURVenue = &tr
	if r := stableS1Raw(eurc); r.Level != stableLevelCritical || r.Cap != stableLevelCritical {
		t.Errorf("EURC with a second direct venue: level %d cap %d, want critical/critical", r.Level, r.Cap)
	}
}

func TestStableSampledPolicy(t *testing.T) {
	thr := stableS1Thr
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	var h stableHyst
	step := func(dev float64) string {
		t.Helper()
		at = at.Add(5 * time.Minute)
		var st string
		h, st, _ = stableSampledStep(h, sigBps(dev, thr), thr, at)
		return st
	}
	expect := func(label string, devs []float64, want []string) {
		t.Helper()
		for i, d := range devs {
			if got := step(d); got != want[i] {
				t.Fatalf("%s: sample %d (%v bps) = %s, want %s", label, i+1, d, got, want[i])
			}
		}
	}
	rep := func(v float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	reps := func(s string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	expect("warming up", []float64{10, 10, 10}, []string{"unknown", "unknown", "normal"})
	expect("enter watch after 3", []float64{35, 35, 35}, []string{"normal", "normal", "watch"})
	expect("enter warning after 3", []float64{150, 150, 150}, []string{"watch", "watch", "warning"})
	// Step down only after 12 consecutive samples below 80% of the level's
	// threshold (80 bps for warning), one level at a time.
	expect("warning holds 11", rep(50, 11), reps("warning", 11))
	expect("warning → watch on the 12th", []float64{50}, []string{"watch"})
	// 25 bps is below watch (30) but not below 80% of it (24): no step down.
	expect("hysteresis band", rep(25, 20), reps("watch", 20))
	// A sample at or above 80% restarts the count.
	expect("interrupted", append(append(rep(10, 11), 26), rep(10, 11)...), reps("watch", 23))
	expect("watch → normal on the 12th", []float64{10}, []string{"normal"})
	// From critical: three steps of 12 samples each.
	expect("enter critical", []float64{350, 350, 350}, []string{"normal", "normal", "critical"})
	expect("critical holds", rep(200, 11), reps("critical", 11))
	expect("critical → warning", []float64{200}, []string{"warning"})
	expect("warning → watch", rep(70, 12), append(reps("warning", 11), "watch"))
	expect("watch → normal", rep(10, 12), append(reps("watch", 11), "normal"))
	// From normal, 3 consecutive samples at or above warning enter warning
	// even when one of them was below critical; critical needs its own 3.
	expect("mixed levels", []float64{350, 150, 350, 350}, []string{"normal", "normal", "warning", "warning"})
	expect("critical on 3 at critical", []float64{350}, []string{"critical"})
}

func TestStableSampledUnknownGapImmediate(t *testing.T) {
	thr := stableS1Thr
	t0 := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	var h stableHyst
	var st, why string
	for i := 1; i <= 3; i++ {
		h, st, _ = stableSampledStep(h, sigBps(5, thr), thr, t0.Add(time.Duration(i)*5*time.Minute))
	}
	if st != "normal" {
		t.Fatalf("after 3 samples = %s, want normal", st)
	}
	// An unknown sample shows unknown with its reason and keeps the counters.
	h, st, why = stableSampledStep(h, stableRaw{Level: stableLevelUnknown, Reason: "fewer than 2 distinct venues"}, thr, t0.Add(20*time.Minute))
	if st != "unknown" || why != "fewer than 2 distinct venues" {
		t.Fatalf("unknown sample = %s %q", st, why)
	}
	h, st, _ = stableSampledStep(h, sigBps(5, thr), thr, t0.Add(25*time.Minute))
	if st != "normal" {
		t.Fatalf("known sample within 15 min of the last known = %s, want normal (resumed)", st)
	}
	// Samples more than 15 minutes apart are not consecutive: warming up.
	h, st, why = stableSampledStep(h, sigBps(5, thr), thr, t0.Add(41*time.Minute))
	if st != "unknown" || why != stableReasonWarmingUp {
		t.Fatalf("after a 16-minute gap = %s %q, want unknown (warming up)", st, why)
	}
	// Immediate critical is not delayed, not even while warming up.
	imm := sigBps(520, thr)
	imm.Immediate = true
	if _, st, _ = stableSampledStep(stableHyst{}, imm, thr, t0); st != "critical" {
		t.Errorf("immediate critical on the first sample = %s, want critical", st)
	}
	// EURC's cap applies to the immediate path too.
	imm.Cap, imm.Level = stableLevelWarning, stableLevelWarning
	if _, st, _ = stableSampledStep(stableHyst{}, imm, thr, t0); st != "warning" {
		t.Errorf("capped immediate critical = %s, want warning", st)
	}
	// Categorical signals (S13) step down after 12 samples below the level.
	var c stableHyst
	cat := func(lvl int, i int) string {
		var s string
		c, s, _ = stableSampledStep(c, stableRaw{Level: lvl, Cap: stableLevelCritical}, [4]float64{}, t0.Add(time.Duration(i)*5*time.Minute))
		return s
	}
	for i := 1; i <= 3; i++ {
		st = cat(stableLevelWatch, i)
	}
	if st != "watch" {
		t.Fatalf("S13 after 3 watch samples = %s", st)
	}
	for i := 4; i <= 14; i++ {
		if st = cat(stableLevelNormal, i); st != "watch" {
			t.Fatalf("S13 normal sample %d = %s, want watch (12 needed)", i-3, st)
		}
	}
	if st = cat(stableLevelNormal, 15); st != "normal" {
		t.Fatalf("S13 after 12 normal samples = %s, want normal", st)
	}
}

func TestStableConfirmPolicy(t *testing.T) {
	var h stableHyst
	var lvl int
	var ok bool
	steps := []struct {
		in, want int
		ok       bool
	}{
		{0, 0, false}, // warming up
		{0, 0, true},  // 2 samples agree
		{1, 0, true},  // a single sample does not change the state
		{0, 0, true},
		{1, 0, true},
		{1, 1, true}, // 2 consecutive samples at watch
		{2, 1, true},
		{0, 1, true}, // disagreeing samples confirm nothing
		{0, 0, true},
		{stableLevelUnknown, 0, true},
		{stableLevelUnknown, stableLevelUnknown, true},
	}
	for i, s := range steps {
		h, lvl, ok = stableConfirmStep(h, s.in)
		if lvl != s.want || ok != s.ok {
			t.Fatalf("step %d (in %d) = %d ok=%v, want %d ok=%v", i+1, s.in, lvl, ok, s.want, s.ok)
		}
	}
}

func TestStableS2S3Raw(t *testing.T) {
	p := StablePeg{Asset: "usdt", InputsUsed: 3, DispBps: stableF(25)}
	if r := stableS2Raw(p); r.Level != stableLevelWatch {
		t.Errorf("S2 25 bps with 3 inputs = %d, want watch", r.Level)
	}
	p.InputsUsed = 2
	if r := stableS2Raw(p); r.Level != stableLevelUnknown {
		t.Errorf("S2 with 2 inputs = %d, want unknown (needs ≥ 3)", r.Level)
	}
	rel := func(mid float64, included bool) StableRelativePeg {
		q := StableQuote{Venue: "uniswap", Pair: stableUniswapPair, Kind: "dex", Mid: stableF(mid), Included: included}
		if !included {
			q.ExcludeReason = "stale"
		}
		return StableRelativePeg{Pair: "USDC/USDT", Quotes: []StableQuote{q}}
	}
	// USDC priced in USDT below 1: USDC is the weaker coin.
	for _, c := range []struct {
		mid              float64
		usdt, usdc       int
		usdtVal, usdcVal float64
	}{
		{0.997, 0, 1, 0, 30},
		{0.9969, 0, 1, 0, 31},
		{1.0101, 2, 0, 101, 0},
		{1.0, 0, 0, 0, 0},
		{0.99975, 0, 0, 0, 2.5},
	} {
		ru, rc := stableS3Raw("usdt", rel(c.mid, true)), stableS3Raw("usdc", rel(c.mid, true))
		if ru.Level != c.usdt || rc.Level != c.usdc || math.Abs(*ru.Value-c.usdtVal) > 1e-6 || math.Abs(*rc.Value-c.usdcVal) > 1e-6 {
			t.Errorf("S3 uniswap %v: usdt %d (%v) usdc %d (%v), want %d (%v) / %d (%v)", c.mid, ru.Level, *ru.Value, rc.Level, *rc.Value,
				c.usdt, c.usdtVal, c.usdc, c.usdcVal)
		}
	}
	if r := stableS3Raw("usdc", rel(0.99, false)); r.Level != stableLevelUnknown {
		t.Errorf("S3 with an excluded pool price = %d, want unknown", r.Level)
	}
	if r := stableS3Raw("eurc", rel(0.99, true)); !r.NA {
		t.Error("S3 for EURC: want n/a (no EURC pool in v1)")
	}
}

// sigFlow is a daily series starting 2024-01-01.
func sigFlow(vals ...float64) stableFlowInput {
	in := stableFlowInput{Source: "aggregator"}
	d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range vals {
		in.Points = append(in.Points, stableFlowPoint{Day: d.AddDate(0, 0, i).Format(time.DateOnly), Value: v})
	}
	return in
}

func TestStableS4Raw(t *testing.T) {
	base := func(n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = 100
		}
		return out
	}
	for _, c := range []struct {
		name string
		in   stableFlowInput
		want int
	}{
		{"−0.5% sustained (24h, inclusive)", sigFlow(append(base(9), 99.5, 99.5)...), stableLevelWatch},
		{"−0.49%", sigFlow(append(base(9), 99.51, 99.51)...), stableLevelNormal},
		{"−1%", sigFlow(append(base(9), 99, 99)...), stableLevelWarning},
		{"−3%", sigFlow(append(base(9), 97, 97)...), stableLevelCritical},
		{"one-day glitch removed", sigFlow(append(base(9), 90, 100, 100)...), stableLevelNormal},
		{"glitch day itself", sigFlow(append(base(9), 90)...), stableLevelNormal},
		{"mints are informational", sigFlow(append(base(9), 110, 110)...), stableLevelNormal},
		// 7d only: −0.3% a day for 9 days (24h −0.3%, 7d ≈ −2.08%).
		{"7d watch", sigFlow(100, 100, 100, 99.7, 99.4009, 99.1027, 98.8054, 98.5090, 98.2134, 97.9188, 97.6250, 97.3322), stableLevelWatch},
	} {
		if r := stableS4Raw(c.in); r.Level != c.want {
			t.Errorf("S4 %s = %d (inputs %v), want %d", c.name, r.Level, r.Inputs, c.want)
		}
	}
	if r := stableS4Raw(sigFlow(100, 100, 99)); r.Level != stableLevelUnknown || r.Reason != stableReasonWarmingUp {
		t.Errorf("S4 with 3 points = %d %q, want unknown (warming up)", r.Level, r.Reason)
	}
	gappy := sigFlow(100, 100, 100, 100, 100, 100)
	gappy.Points[5].Day = "2024-03-01" // neither d−1 nor d−7 has a median
	if r := stableS4Raw(gappy); r.Level != stableLevelUnknown {
		t.Errorf("S4 with no median at d−1 or d−7 = %d, want unknown", r.Level)
	}
}

func TestStableS5Raw(t *testing.T) {
	for _, c := range []struct {
		cov  float64
		want int
	}{{1.0302, 0}, {1.0100, 0}, {1.0099, 1}, {1.0000, 1}, {0.9999, 3}} {
		tp := &StableTetherPoint{Coverage: stableF(c.cov)}
		if r := stableS5Raw("usdt", tp); r.Level != c.want {
			t.Errorf("S5 coverage %v = %d, want %d", c.cov, r.Level, c.want)
		}
	}
	if r := stableS5Raw("usdt", nil); r.Level != stableLevelUnknown {
		t.Error("S5 without a Tether snapshot: want unknown")
	}
	if r := stableS5Raw("usdc", &StableTetherPoint{Coverage: stableF(1)}); !r.NA {
		t.Error("S5 for USDC: want n/a (S6 governs)")
	}
}

func TestStableS6Raw(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	att := func(periodEnd string) stableAttestation {
		return stableAttestation{PeriodEnd: periodEnd, Published: periodEnd, Firm: "Firm", Type: "attestation", URL: "https://example.com/a.pdf"}
	}
	days := func(n int) string { return now.AddDate(0, 0, -n).Format(time.DateOnly) }
	checked := now.AddDate(0, 0, -3).Format(time.DateOnly)
	for _, c := range []struct {
		name    string
		f       stableAssetFacts
		want    int
		stale   bool
		reason  string
		summary string
	}{
		{"monthly 50 d", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(50))}, AttestationsChecked: checked}, 0, false, "", "latest attestation recorded by biglens"},
		{"monthly 51 d", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(51))}, AttestationsChecked: checked}, 1, false, "", ""},
		{"monthly 75 d", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(75))}, AttestationsChecked: checked}, 1, false, "", ""},
		{"monthly 76 d", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(76))}, AttestationsChecked: checked}, 2, false, "", ""},
		{"quarterly 135 d", stableAssetFacts{CadenceDays: 90, Attestations: []stableAttestation{att(days(135))}, AttestationsChecked: checked}, 0, false, "", ""},
		{"quarterly 136 d", stableAssetFacts{CadenceDays: 90, Attestations: []stableAttestation{att(days(136))}, AttestationsChecked: checked}, 1, false, "", ""},
		{"quarterly 166 d", stableAssetFacts{CadenceDays: 90, Attestations: []stableAttestation{att(days(166))}, AttestationsChecked: checked}, 2, false, "", ""},
		{"the latest counts", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(80)), att(days(20))}}, 0, false, "", ""},
		// Over the threshold and not checked within 14 days: S6 unknown, S13 reports stale facts.
		{"stale facts", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(60))}, AttestationsChecked: days(15)}, stableLevelUnknown, true, "curated facts may be stale", ""},
		{"never checked", stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{att(days(60))}}, stableLevelUnknown, true, "curated facts may be stale", ""},
		{"no attestation", stableAssetFacts{Status: stableFactsUnknown}, stableLevelUnknown, false, "no attestation recorded", ""},
	} {
		r, stale := stableS6Raw(c.f, now)
		if r.Level != c.want || stale != c.stale || (c.reason != "" && !strings.Contains(r.Reason, c.reason)) ||
			(c.summary != "" && !strings.Contains(strings.ToLower(r.Summary), c.summary)) {
			t.Errorf("S6 %s = level %d stale %v reason %q summary %q, want %d %v %q %q", c.name, r.Level, stale, r.Reason, r.Summary, c.want, c.stale, c.reason, c.summary)
		}
	}
	short := att(days(10))
	short.ReservesNative, short.LiabilitiesNative = "99.5", "100"
	if r, _ := stableS6Raw(stableAssetFacts{CadenceDays: 30, Attestations: []stableAttestation{short}}, now); r.Level != stableLevelWarning {
		t.Errorf("S6 reserves < liabilities = %d, want warning", r.Level)
	}
}

// sigBaseline returns confirmed control states for every monitored field of
// an asset.
func sigBaseline(asset string) map[string]stableControlState {
	out := map[string]stableControlState{}
	for _, d := range stableControlDeployments {
		if d.Asset != asset {
			continue
		}
		for _, f := range stableControlFields[d.Kind] {
			v := "0x00000000000000000000000000000000000000aa"
			switch f.Kind {
			case "bool":
				v = "false"
			case "authority":
				v = "Auth1111111111111111111111111111111111111111"
			}
			out[asset+"|"+d.Chain+"|"+f.Name] = stableControlState{Asset: asset, Chain: d.Chain, Field: f.Name, Value: &v, Baseline: true}
		}
	}
	return out
}

func TestStableS8Raw(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	states := sigBaseline("usdc")
	if r := stableS8Raw("usdc", states, nil, true, now); r.Level != stableLevelNormal {
		t.Errorf("S8 baselined, no changes = %d %q, want normal", r.Level, r.Reason)
	}
	if r := stableS8Raw("usdc", states, nil, false, now); r.Level != stableLevelUnknown {
		t.Error("S8 with rpc_controls off: want unknown")
	}
	partial := sigBaseline("usdc")
	delete(partial, "usdc|base|pauser")
	if r := stableS8Raw("usdc", partial, nil, true, now); r.Level != stableLevelUnknown || !strings.Contains(r.Reason, "base.pauser") {
		t.Errorf("S8 with an unbaselined field = %d %q, want unknown naming base.pauser", r.Level, r.Reason)
	}
	paused := sigBaseline("usdc")
	tv := "true"
	st := paused["usdc|arb|paused"]
	st.Value = &tv
	paused["usdc|arb|paused"] = st
	if r := stableS8Raw("usdc", paused, nil, true, now); r.Level != stableLevelCritical {
		t.Errorf("S8 paused = %d, want critical", r.Level)
	}
	ev := func(field string, ago time.Duration) stableControlEvent {
		return stableControlEvent{DetectedAt: now.Add(-ago), Asset: "usdc", Chain: "eth", Field: field, Old: "0xa", New: "0xb"}
	}
	for _, c := range []struct {
		name string
		evs  []stableControlEvent
		want string
	}{
		{"implementation changed 2 d ago", []stableControlEvent{ev("implementation", 48*time.Hour)}, stableStateWarning},
		{"master minter changed 6.9 d ago", []stableControlEvent{ev("master_minter", 166*time.Hour)}, stableStateWarning},
		{"owner changed 8 d ago", []stableControlEvent{ev("owner", 8*24*time.Hour)}, stableStateNormal},
		{"rescuer changed 1 d ago", []stableControlEvent{ev("rescuer", 24*time.Hour)}, stableStateInfo},
		{"other asset", []stableControlEvent{{DetectedAt: now, Asset: "eurc", Chain: "eth", Field: "owner"}}, stableStateNormal},
	} {
		if got, _ := stableRawState(stableS8Raw("usdc", states, c.evs, true, now)); got != c.want {
			t.Errorf("S8 %s = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestStableS10S11Raw(t *testing.T) {
	row := func(chain, tier string, issuer, onchain *float64, share *float64) StableSupplyRow {
		return StableSupplyRow{Chain: chain, ChainLabel: chain, Tier: tier, Verified: tier != "", IssuerCirculatingNative: issuer,
			OnchainTotalNative: onchain, IssuerSharePct: share}
	}
	a := &StableSupplyAsset{Asset: "usdc", Chains: []StableSupplyRow{
		row("eth", "native", stableF(48e9), stableF(49e9), stableF(66.5)),
		row("arb", "native", stableF(2.6e9), stableF(2.7e9), stableF(3.6)),
		row("arb.bridged", "bridged", nil, stableF(0.1e9), nil),
		row("hyperliquid", "", stableF(6.6e9), nil, stableF(9.1)),
	}}
	r := stableS10Raw(a)
	if st, _ := stableRawState(r); st != stableStateInfo || !strings.Contains(r.Summary, "eth") {
		t.Errorf("S10 bridged ≈ 0.2%% = %s %q, want info with the largest chain", st, r.Summary)
	}
	a.Chains[2].OnchainTotalNative = stableF(6e9) // 6 / (48 + 2.6 + 6) = 10.6%
	if r := stableS10Raw(a); r.Level != stableLevelWatch {
		t.Errorf("S10 bridged ≈ 10.6%% = %d, want watch", r.Level)
	}
	if r := stableS10Raw(nil); r.Level != stableLevelUnknown {
		t.Error("S10 without supply: want unknown")
	}

	reg := func(status string) stableAssetFacts {
		return stableAssetFacts{Regulatory: []stableRegulatory{{Regime: "EU-MiCA", Status: status, Entity: "E", AsOf: "2026-10-20", SourceURL: "https://example.com"}}}
	}
	if r := stableS11Raw(reg("not_authorized")); r.Level != stableLevelWatch || !strings.Contains(r.Summary, "EU users") {
		t.Errorf("S11 MiCA not_authorized = %d %q, want watch for EU users", r.Level, r.Summary)
	}
	if st, _ := stableRawState(stableS11Raw(reg("authorized"))); st != stableStateInfo {
		t.Errorf("S11 MiCA authorized = %s, want info", st)
	}
	for _, f := range []stableAssetFacts{reg("unknown"), {Status: stableFactsUnknown}, {}} {
		if r := stableS11Raw(f); r.Level != stableLevelUnknown {
			t.Errorf("S11 %+v = %d, want unknown", f, r.Level)
		}
	}
}

func TestStableS12Raw(t *testing.T) {
	a := StableSupplyAsset{Asset: "usdc"}
	a.Reconcile.Aggregator = StableReconAggregator{DiffPct: stableF(0.19)}
	if r := stableS12AggRaw(a); r.Level != stableLevelNormal {
		t.Errorf("S12 aggregator +0.19%% = %d, want normal", r.Level)
	}
	a.Reconcile.Aggregator = StableReconAggregator{DiffPct: stableF(-0.6), AboveWatch: true}
	if r := stableS12AggRaw(a); r.Level != stableLevelWatch {
		t.Errorf("S12 aggregator −0.6%% = %d, want watch", r.Level)
	}
	a.Reconcile.Aggregator = StableReconAggregator{DiffPct: stableF(2.5), AboveWatch: true, AboveWarning: true}
	if r := stableS12AggRaw(a); r.Level != stableLevelWarning {
		t.Errorf("S12 aggregator +2.5%% = %d, want warning", r.Level)
	}
	a.Reconcile.Aggregator = StableReconAggregator{SkipReason: "missing_aggregator"}
	if r := stableS12AggRaw(a); r.Level != stableLevelUnknown {
		t.Errorf("S12 aggregator skipped = %d, want unknown", r.Level)
	}
	a.Reconcile.Onchain = []StableReconOnchain{{Chain: "eth", SkipReason: "not_aligned"}}
	if r := stableS12OnchainRaw(a); r.Level != stableLevelUnknown {
		t.Errorf("S12 on-chain with no compared pair = %d, want unknown", r.Level)
	}
	a.Reconcile.Onchain = append(a.Reconcile.Onchain, StableReconOnchain{Chain: "base", ExceedsTolerance: true, ConsecutiveSamples: 1})
	if r := stableS12OnchainRaw(a); r.Level != stableLevelNormal {
		t.Errorf("S12 on-chain unconfirmed excess = %d, want normal", r.Level)
	}
	a.Reconcile.Onchain[1].Confirmed = true
	if r := stableS12OnchainRaw(a); r.Level != stableLevelWarning {
		t.Errorf("S12 on-chain confirmed excess = %d, want warning", r.Level)
	}
}

func TestStableS13Raw(t *testing.T) {
	for _, c := range []struct {
		name string
		in   stableHealthInput
		want int
	}{
		{"healthy", stableHealthInput{}, stableLevelNormal},
		{"stale source", stableHealthInput{StaleSources: []string{"circle"}}, stableLevelWatch},
		{"stalled sampler", stableHealthInput{StalledSamplers: []string{"controls"}}, stableLevelWatch},
		{"stale facts", stableHealthInput{FactsStale: true}, stableLevelWatch},
		{"tether sum", stableHealthInput{TetherSumMismatch: true}, stableLevelWatch},
		{"all venues failing", stableHealthInput{VenuesFailing: true, FactsStale: true}, stableLevelWarning},
	} {
		if r := stableS13Raw(c.in); r.Level != c.want {
			t.Errorf("S13 %s = %d, want %d", c.name, r.Level, c.want)
		}
	}
	mk := func(codes ...string) *stablePegResult {
		res := &stablePegResult{}
		venues := []string{"coinbase", "kraken", "bitstamp", "okx", "uniswap"}
		for i, c := range codes {
			res.Markets = append(res.Markets, StableQuote{Venue: venues[i], ExcludeReason: c, Included: c == ""})
		}
		return res
	}
	if !stableVenuesFailing(mk("timeout", "5xx", "network_error", "disabled", "")) {
		t.Error("every enabled exchange failed: want failing (Uniswap and disabled venues do not count)")
	}
	if stableVenuesFailing(mk("timeout", "stale", "5xx")) {
		t.Error("a venue answered with a stale quote: want not failing")
	}
	if stableVenuesFailing(mk("disabled", "disabled")) {
		t.Error("no venue enabled: want not failing")
	}
}

func TestStableWorstState(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"normal", "info", "n/a"}, "normal"},
		{[]string{"normal", "unknown"}, "unknown"},
		{[]string{"unknown", "watch", "info"}, "watch"},
		{[]string{"warning", "critical", "watch"}, "critical"},
		{[]string{"n/a"}, "unknown"},
		{nil, "unknown"},
	} {
		if got := stableWorstState(c.in); got != c.want {
			t.Errorf("worst(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
