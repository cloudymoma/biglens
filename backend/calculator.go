package main

// Pricing catalog and pure cost math for the BigQuery pricing calculator
// (/api/calculator/*). The frontend only collects inputs and renders what
// this file returns, so every number on screen is reproducible in a unit
// test here.
//
// Rates are list prices from cloud.google.com/bigquery/pricing, checked
// 2026-09. Monthly $/GiB figures are the published hourly rates x 730.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	calcHoursPerMonth = 730.0
	calcFreeTierGiB   = 10.0 // per storage SKU, per month
	calcSlotIncrement = 50.0 // commitments, baselines and autoscaling all move in 50s
	calcStandardMax   = 1600.0
	calcBuckets       = 96 // a day in 15-minute buckets
	calcBucketHours   = 24.0 / calcBuckets
)

var calcUnitGiB = map[string]float64{"GiB": 1, "TiB": 1024, "PiB": 1024 * 1024}

// ------------------------------------------------------------ catalog

type StorageRates struct {
	ActiveLogical    float64 `json:"active_logical"`
	LongTermLogical  float64 `json:"long_term_logical"`
	ActivePhysical   float64 `json:"active_physical"`
	LongTermPhysical float64 `json:"long_term_physical"`
}

type StorageRegionPreset struct {
	Region string       `json:"region"`
	Label  string       `json:"label"`
	Rates  StorageRates `json:"rates"` // $/GiB/month
}

type EditionPreset struct {
	Edition  string             `json:"edition"`
	Label    string             `json:"label"`
	Payg     float64            `json:"payg"`                // $/slot-hour
	Commit   map[string]float64 `json:"commit"`              // term -> $/slot-hour; empty when unavailable
	MaxSlots float64            `json:"max_slots,omitempty"` // per reservation; 0 = quota only
	Baseline bool               `json:"baseline"`            // edition supports baseline slots
}

type CalculatorPresets struct {
	StorageRegions []StorageRegionPreset `json:"storage_regions"`
	Editions       []EditionPreset       `json:"editions"`
	FreeTierGiB    float64               `json:"free_tier_gib"`
	HoursPerMonth  float64               `json:"hours_per_month"`
	SlotIncrement  float64               `json:"slot_increment"`
}

var calcStorageRegions = []StorageRegionPreset{
	{Region: "us-central1", Label: "Iowa (us-central1)", Rates: StorageRates{0.023, 0.016, 0.04, 0.02}},
	{Region: "us", Label: "US (multi-region)", Rates: StorageRates{0.02, 0.01, 0.04, 0.02}},
}

// Commit rates are resource-based commitments (dedicated slots billed 24x7).
// Spend-based CUDs are a different product and are not modelled.
var calcEditions = []EditionPreset{
	{Edition: "standard", Label: "Standard", Payg: 0.04, Commit: map[string]float64{}, MaxSlots: calcStandardMax},
	{Edition: "enterprise", Label: "Enterprise", Payg: 0.06, Commit: map[string]float64{"1yr": 0.048, "3yr": 0.036}, Baseline: true},
	{Edition: "enterprise_plus", Label: "Enterprise Plus", Payg: 0.10, Commit: map[string]float64{"1yr": 0.08, "3yr": 0.06}, Baseline: true},
}

func calculatorPresets() CalculatorPresets {
	return CalculatorPresets{
		StorageRegions: calcStorageRegions,
		Editions:       calcEditions,
		FreeTierGiB:    calcFreeTierGiB,
		HoursPerMonth:  calcHoursPerMonth,
		SlotIncrement:  calcSlotIncrement,
	}
}

func editionPreset(edition string) (EditionPreset, bool) {
	for _, e := range calcEditions {
		if e.Edition == edition {
			return e, true
		}
	}
	return EditionPreset{}, false
}

func discounted(rate, pct float64) float64 {
	return rate * (1 - math.Min(100, math.Max(0, pct))/100)
}

func nonNegative(name string, vals ...float64) error {
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("%s must be a non-negative number", name)
		}
	}
	return nil
}

// ------------------------------------------------------------ storage

// StorageVolumes is what BigQuery reports per table: active physical bytes
// already include time travel; fail-safe is separate.
type StorageVolumes struct {
	ActiveLogical    float64 `json:"active_logical"`
	LongTermLogical  float64 `json:"long_term_logical"`
	ActivePhysical   float64 `json:"active_physical"`
	LongTermPhysical float64 `json:"long_term_physical"`
	FailSafe         float64 `json:"fail_safe"`
}

type StorageCalcRequest struct {
	Unit        string         `json:"unit"` // GiB | TiB | PiB
	Volumes     StorageVolumes `json:"volumes"`
	Rates       StorageRates   `json:"rates"` // $/GiB/month before discount
	DiscountPct float64        `json:"discount_pct"`
	FreeTier    bool           `json:"free_tier"`
}

type StorageModelCost struct {
	Active   float64 `json:"active"`
	LongTerm float64 `json:"long_term"`
	FailSafe float64 `json:"fail_safe"`
	Total    float64 `json:"total"`
}

type StorageEstimate struct {
	Logical           StorageModelCost `json:"logical"`
	Physical          StorageModelCost `json:"physical"`
	Cheaper           string           `json:"cheaper"` // "logical" | "physical"
	Savings           float64          `json:"savings"` // per month
	AnnualRecommended float64          `json:"annual_recommended"`
	// logical bytes / physical bytes, without and with fail-safe.
	DataCompressionRatio      float64 `json:"data_compression_ratio"`
	EffectiveCompressionRatio float64 `json:"effective_compression_ratio"`
	// Weighted physical rate / weighted logical rate: physical billing wins
	// once the effective compression ratio exceeds this.
	BreakEvenRatio float64 `json:"break_even_ratio"`
}

func (r *StorageCalcRequest) validate() error {
	if _, ok := calcUnitGiB[r.Unit]; !ok {
		return fmt.Errorf("unit must be GiB, TiB or PiB")
	}
	v, rt := r.Volumes, r.Rates
	if err := nonNegative("volumes", v.ActiveLogical, v.LongTermLogical, v.ActivePhysical, v.LongTermPhysical, v.FailSafe); err != nil {
		return err
	}
	if err := nonNegative("rates", rt.ActiveLogical, rt.LongTermLogical, rt.ActivePhysical, rt.LongTermPhysical); err != nil {
		return err
	}
	return nonNegative("discount_pct", r.DiscountPct)
}

func storageEstimate(req StorageCalcRequest) StorageEstimate {
	gib := calcUnitGiB[req.Unit]
	v := StorageVolumes{
		ActiveLogical:    req.Volumes.ActiveLogical * gib,
		LongTermLogical:  req.Volumes.LongTermLogical * gib,
		ActivePhysical:   req.Volumes.ActivePhysical * gib,
		LongTermPhysical: req.Volumes.LongTermPhysical * gib,
		FailSafe:         req.Volumes.FailSafe * gib,
	}
	r := StorageRates{
		ActiveLogical:    discounted(req.Rates.ActiveLogical, req.DiscountPct),
		LongTermLogical:  discounted(req.Rates.LongTermLogical, req.DiscountPct),
		ActivePhysical:   discounted(req.Rates.ActivePhysical, req.DiscountPct),
		LongTermPhysical: discounted(req.Rates.LongTermPhysical, req.DiscountPct),
	}
	free := 0.0
	if req.FreeTier {
		free = calcFreeTierGiB
	}

	// Each SKU gets its own 10 GiB. Fail-safe bytes bill under the active
	// physical SKU, so they share that SKU's allowance.
	logical := StorageModelCost{
		Active:   math.Max(0, v.ActiveLogical-free) * r.ActiveLogical,
		LongTerm: math.Max(0, v.LongTermLogical-free) * r.LongTermLogical,
	}
	logical.Total = logical.Active + logical.LongTerm

	freeForActive := math.Min(v.ActivePhysical, free)
	physical := StorageModelCost{
		Active:   (v.ActivePhysical - freeForActive) * r.ActivePhysical,
		FailSafe: math.Max(0, v.FailSafe-(free-freeForActive)) * r.ActivePhysical,
		LongTerm: math.Max(0, v.LongTermPhysical-free) * r.LongTermPhysical,
	}
	physical.Total = physical.Active + physical.FailSafe + physical.LongTerm

	logicalBytes := v.ActiveLogical + v.LongTermLogical
	physicalData := v.ActivePhysical + v.LongTermPhysical
	physicalBytes := physicalData + v.FailSafe

	est := StorageEstimate{Logical: logical, Physical: physical, Cheaper: "logical"}
	if physical.Total < logical.Total {
		est.Cheaper = "physical"
	}
	est.Savings = math.Abs(logical.Total - physical.Total)
	recommended := logical.Total
	if est.Cheaper == "physical" {
		recommended = physical.Total
	}
	est.AnnualRecommended = recommended * 12
	est.DataCompressionRatio = ratio(logicalBytes, physicalData)
	est.EffectiveCompressionRatio = ratio(logicalBytes, physicalBytes)

	// Break-even ignores the free tier: it is about marginal rates.
	if logicalBytes > 0 && physicalBytes > 0 {
		wl := (v.ActiveLogical*r.ActiveLogical + v.LongTermLogical*r.LongTermLogical) / logicalBytes
		wp := ((v.ActivePhysical+v.FailSafe)*r.ActivePhysical + v.LongTermPhysical*r.LongTermPhysical) / physicalBytes
		est.BreakEvenRatio = ratio(wp, wl)
	}
	return est
}

func ratio(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return num / den
}

// -------------------------------------------------------------- slots

type PeakWindow struct {
	ID    string  `json:"id"`
	Start string  `json:"start"` // "HH:MM"
	End   string  `json:"end"`   // "HH:MM"; end <= start wraps past midnight
	Slots float64 `json:"slots"` // added on top of the baseline while active
}

type SlotsCalcRequest struct {
	Edition        string       `json:"edition"` // standard | enterprise | enterprise_plus
	BaselineSlots  float64      `json:"baseline_slots"`
	CommittedSlots float64      `json:"committed_slots"`
	Windows        []PeakWindow `json:"windows"`
	PaygRate       float64      `json:"payg_rate"`   // $/slot-hour before discount
	CommitRate     float64      `json:"commit_rate"` // $/slot-hour before discount
	DiscountPct    float64      `json:"discount_pct"`
}

// SlotCost splits spend into the three ways a slot can be billed.
type SlotCost struct {
	Committed           float64 `json:"committed"`            // commitment, 24x7 at the commit rate
	UncommittedBaseline float64 `json:"uncommitted_baseline"` // baseline above the commitment, 24x7 at PAYG
	Autoscaled          float64 `json:"autoscaled"`           // bursts above the floor, PAYG in 50-slot steps
	Payg                float64 `json:"payg"`                 // UncommittedBaseline + Autoscaled
	Total               float64 `json:"total"`
}

func (c *SlotCost) add(o SlotCost) {
	c.Committed += o.Committed
	c.UncommittedBaseline += o.UncommittedBaseline
	c.Autoscaled += o.Autoscaled
	c.Payg += o.Payg
	c.Total += o.Total
}

func (c SlotCost) scale(f float64) SlotCost {
	return SlotCost{c.Committed * f, c.UncommittedBaseline * f, c.Autoscaled * f, c.Payg * f, c.Total * f}
}

type SlotProfile struct {
	Baseline []float64   `json:"baseline"` // per bucket, as entered
	Windows  [][]float64 `json:"windows"`  // one series per window, per bucket
	Demand   []float64   `json:"demand"`   // baseline + all windows
	Billed   []float64   `json:"billed"`   // slots actually charged after rounding and caps
}

type SweepPoint struct {
	Committed float64 `json:"committed"`
	Monthly   float64 `json:"monthly"`
}

type SlotsEstimate struct {
	Profile    SlotProfile `json:"profile"`
	HourlyCost []SlotCost  `json:"hourly_cost"` // 24 entries
	Daily      SlotCost    `json:"daily"`
	Monthly    SlotCost    `json:"monthly"`
	// Inputs after edition rules and 50-slot rounding were applied.
	EffectiveBaseline  float64      `json:"effective_baseline"`
	EffectiveCommitted float64      `json:"effective_committed"`
	PeakSlots          float64      `json:"peak_slots"` // demand as entered
	AvgSlots           float64      `json:"avg_slots"`
	CommittedSlotHours float64      `json:"committed_slot_hours"` // per day
	UsedCommittedHours float64      `json:"used_committed_slot_hours"`
	PaygSlotHours      float64      `json:"payg_slot_hours"`
	CommitUtilization  float64      `json:"commit_utilization"`    // 0..1, 0 when nothing committed
	Sweep              []SweepPoint `json:"sweep"`                 // empty when the edition has no commitments
	BestCommit         *SweepPoint  `json:"best_commit,omitempty"` // cheapest point of the sweep
	Warnings           []string     `json:"warnings"`
}

func (r *SlotsCalcRequest) validate() error {
	if _, ok := editionPreset(r.Edition); !ok {
		return fmt.Errorf("unknown edition %q", r.Edition)
	}
	if err := nonNegative("slots", r.BaselineSlots, r.CommittedSlots); err != nil {
		return err
	}
	if err := nonNegative("rates", r.PaygRate, r.CommitRate, r.DiscountPct); err != nil {
		return err
	}
	for _, w := range r.Windows {
		if _, err := timeToBucket(w.Start); err != nil {
			return err
		}
		if _, err := timeToBucket(w.End); err != nil {
			return err
		}
		if err := nonNegative("window slots", w.Slots); err != nil {
			return err
		}
	}
	return nil
}

func timeToBucket(hhmm string) (int, error) {
	h, m, ok := strings.Cut(hhmm, ":")
	hi, err1 := strconv.Atoi(h)
	mi, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hi < 0 || hi > 24 || mi < 0 || mi > 59 {
		return 0, fmt.Errorf("invalid time %q, want HH:MM", hhmm)
	}
	return int(math.Round(float64(hi*60+mi)/(calcBucketHours*60))) % calcBuckets, nil
}

// Buckets covered by [start, end). end <= start wraps through midnight, so
// "22:00 -> 02:00" is four hours; start == end is an empty window.
func windowMask(w PeakWindow) [calcBuckets]bool {
	var mask [calcBuckets]bool
	s, _ := timeToBucket(w.Start)
	e, _ := timeToBucket(w.End)
	for i := s; i != e; i = (i + 1) % calcBuckets {
		mask[i] = true
	}
	return mask
}

func roundUp50(slots float64) float64 {
	if slots <= 0 {
		return 0
	}
	return math.Ceil(slots/calcSlotIncrement) * calcSlotIncrement
}

func slotsEstimate(req SlotsCalcRequest) SlotsEstimate {
	ed, _ := editionPreset(req.Edition)
	var warnings []string

	baseline, committed := req.BaselineSlots, req.CommittedSlots
	if !ed.Baseline {
		if baseline > 0 {
			warnings = append(warnings, fmt.Sprintf("%s edition is autoscaling only; baseline slots were set to 0.", ed.Label))
		}
		baseline = 0
	}
	if len(ed.Commit) == 0 {
		if committed > 0 {
			warnings = append(warnings, fmt.Sprintf("%s edition has no capacity commitments; committed slots were set to 0.", ed.Label))
		}
		committed = 0
	}
	if r := roundUp50(baseline); r != baseline {
		warnings = append(warnings, fmt.Sprintf("Baseline rounded up to %s slots (50-slot increments).", fmtSlots(r)))
		baseline = r
	}
	if r := roundUp50(committed); r != committed {
		warnings = append(warnings, fmt.Sprintf("Commitment rounded up to %s slots (50-slot minimum and increments).", fmtSlots(r)))
		committed = r
	}

	// The profile shows demand as entered (before rounding); only the
	// edition rule is applied so Standard never shows a baseline.
	entered := req.BaselineSlots
	if !ed.Baseline {
		entered = 0
	}
	profile := buildProfile(entered, req.Windows)
	rates := struct{ payg, commit float64 }{discounted(req.PaygRate, req.DiscountPct), discounted(req.CommitRate, req.DiscountPct)}

	est := costProfile(profile, baseline, committed, ed.MaxSlots, rates.payg, rates.commit)
	est.Warnings = warnings
	if ed.MaxSlots > 0 && est.PeakSlots > ed.MaxSlots {
		est.Warnings = append(est.Warnings, fmt.Sprintf("%s edition is capped at %s slots per reservation; demand above that is not billed here.", ed.Label, fmtSlots(ed.MaxSlots)))
	}
	if est.Warnings == nil {
		est.Warnings = []string{}
	}

	est.Sweep = []SweepPoint{}
	if len(ed.Commit) > 0 {
		top := math.Max(roundUp50(est.PeakSlots), committed)
		for c := 0.0; c <= top; c += calcSlotIncrement {
			p := SweepPoint{Committed: c, Monthly: costProfile(profile, baseline, c, ed.MaxSlots, rates.payg, rates.commit).Monthly.Total}
			est.Sweep = append(est.Sweep, p)
			if est.BestCommit == nil || p.Monthly < est.BestCommit.Monthly {
				pt := p
				est.BestCommit = &pt
			}
		}
	}
	return est
}

func buildProfile(baseline float64, windows []PeakWindow) SlotProfile {
	p := SlotProfile{
		Baseline: make([]float64, calcBuckets),
		Windows:  make([][]float64, len(windows)),
		Demand:   make([]float64, calcBuckets),
		Billed:   make([]float64, calcBuckets),
	}
	for i := range p.Baseline {
		p.Baseline[i] = baseline
		p.Demand[i] = baseline
	}
	for wi, w := range windows {
		mask := windowMask(w)
		p.Windows[wi] = make([]float64, calcBuckets)
		for i, on := range mask {
			if on {
				p.Windows[wi][i] = w.Slots
				p.Demand[i] += w.Slots
			}
		}
	}
	return p
}

// costProfile bills one day. Committed slots are a 24x7 floor at the commit
// rate; baseline above the commitment is a 24x7 floor at PAYG; anything
// above max(baseline, committed) autoscales in 50-slot steps at PAYG.
func costProfile(profile SlotProfile, baseline, committed, maxSlots, payg, commit float64) SlotsEstimate {
	floor := math.Max(baseline, committed)
	uncommitted := math.Max(0, baseline-committed)
	est := SlotsEstimate{Profile: profile, HourlyCost: make([]SlotCost, 24)}
	sum := 0.0

	for i, demand := range profile.Demand {
		autoscaled := roundUp50(math.Max(0, demand-floor))
		billed := floor + autoscaled
		if maxSlots > 0 && billed > maxSlots {
			billed = maxSlots
			autoscaled = math.Max(0, billed-floor)
		}
		est.Profile.Billed[i] = billed

		c := SlotCost{
			Committed:           committed * commit * calcBucketHours,
			UncommittedBaseline: uncommitted * payg * calcBucketHours,
			Autoscaled:          autoscaled * payg * calcBucketHours,
		}
		c.Payg = c.UncommittedBaseline + c.Autoscaled
		c.Total = c.Committed + c.Payg
		est.HourlyCost[int(float64(i)*calcBucketHours)].add(c)

		est.UsedCommittedHours += math.Min(demand, committed) * calcBucketHours
		est.PaygSlotHours += (uncommitted + autoscaled) * calcBucketHours
		est.PeakSlots = math.Max(est.PeakSlots, demand)
		sum += demand
	}
	for _, h := range est.HourlyCost {
		est.Daily.add(h)
	}
	est.Monthly = est.Daily.scale(calcHoursPerMonth / 24)
	est.EffectiveBaseline = baseline
	est.EffectiveCommitted = committed
	est.AvgSlots = sum / calcBuckets
	est.CommittedSlotHours = committed * 24
	est.CommitUtilization = ratio(est.UsedCommittedHours, est.CommittedSlotHours)
	return est
}

func fmtSlots(n float64) string {
	s := strconv.FormatFloat(n, 'f', 0, 64)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
