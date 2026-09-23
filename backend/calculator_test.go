package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func usCentral1() StorageRates { return calcStorageRegions[0].Rates }

func storageReq(v StorageVolumes, free bool) StorageCalcRequest {
	return StorageCalcRequest{Unit: "GiB", Volumes: v, Rates: usCentral1(), FreeTier: free}
}

// ------------------------------------------------------------ storage

func TestCalculatorPresetsUseRegionalListPrices(t *testing.T) {
	p := calculatorPresets()
	if p.StorageRegions[0].Region != "us-central1" {
		t.Fatalf("first region = %q, want us-central1", p.StorageRegions[0].Region)
	}
	// Iowa logical storage is dearer than the US multi-region; the physical
	// rates are the same. The old frontend shipped the multi-region numbers
	// under a us-central1 label.
	near(t, "us-central1 active logical", p.StorageRegions[0].Rates.ActiveLogical, 0.023)
	near(t, "us-central1 long-term logical", p.StorageRegions[0].Rates.LongTermLogical, 0.016)
	near(t, "us active logical", p.StorageRegions[1].Rates.ActiveLogical, 0.02)
	near(t, "us long-term logical", p.StorageRegions[1].Rates.LongTermLogical, 0.01)
	if p.FreeTierGiB != 10 || p.HoursPerMonth != 730 || p.SlotIncrement != 50 {
		t.Errorf("constants = %+v", p)
	}
}

func TestStorageEstimateBillsEachModelOnItsOwnBytes(t *testing.T) {
	est := storageEstimate(storageReq(StorageVolumes{1000, 500, 400, 100, 50}, false))
	near(t, "logical", est.Logical.Total, 1000*0.023+500*0.016)
	// fail-safe bills at the active physical rate
	near(t, "physical", est.Physical.Total, 400*0.04+100*0.02+50*0.04)
	near(t, "fail-safe", est.Physical.FailSafe, 50*0.04)
}

func TestStorageEstimateMatchesReviewWorkedExample(t *testing.T) {
	// 100 TiB active + 50 TiB long-term logical in us-central1 with free tier.
	req := StorageCalcRequest{Unit: "TiB", Volumes: StorageVolumes{ActiveLogical: 100, LongTermLogical: 50}, Rates: usCentral1(), FreeTier: true}
	est := storageEstimate(req)
	near(t, "active", est.Logical.Active, (102400-10)*0.023)
	near(t, "long-term", est.Logical.LongTerm, (51200-10)*0.016)
	near(t, "total", est.Logical.Total, 3174.01)
}

func TestStorageFreeTierAppliesToEverySKU(t *testing.T) {
	est := storageEstimate(storageReq(StorageVolumes{5, 100, 0, 30, 0}, true))
	near(t, "active logical below tier", est.Logical.Active, 0)
	near(t, "long-term logical gets its own 10 GiB", est.Logical.LongTerm, 90*0.016)
	near(t, "long-term physical gets its own 10 GiB", est.Physical.LongTerm, 20*0.02)
}

func TestStorageFreeTierIsPooledAcrossActivePhysicalAndFailSafe(t *testing.T) {
	// 5 GiB active + 5 GiB fail-safe share the active-physical SKU's 10 GiB.
	est := storageEstimate(storageReq(StorageVolumes{ActivePhysical: 5, FailSafe: 5}, true))
	near(t, "active", est.Physical.Active, 0)
	near(t, "fail-safe", est.Physical.FailSafe, 0)
	// 12 GiB active uses the whole allowance; fail-safe pays in full.
	est = storageEstimate(storageReq(StorageVolumes{ActivePhysical: 12, FailSafe: 5}, true))
	near(t, "active over tier", est.Physical.Active, 2*0.04)
	near(t, "fail-safe unrelieved", est.Physical.FailSafe, 5*0.04)
}

func TestStorageBreakEvenRatioFollowsRates(t *testing.T) {
	// All-active data: physical wins once logical/physical > 0.04/0.023.
	est := storageEstimate(storageReq(StorageVolumes{ActiveLogical: 1000, ActivePhysical: 100}, false))
	near(t, "break-even", est.BreakEvenRatio, 0.04/0.023)
	near(t, "data ratio", est.DataCompressionRatio, 10)
	if est.Cheaper != "physical" {
		t.Errorf("cheaper = %q, want physical", est.Cheaper)
	}
	near(t, "savings", est.Savings, 1000*0.023-100*0.04)
	near(t, "annual", est.AnnualRecommended, 100*0.04*12)

	// A 1.5x ratio is below break-even for active data, so logical wins.
	est = storageEstimate(storageReq(StorageVolumes{ActiveLogical: 150, ActivePhysical: 100}, false))
	if est.Cheaper != "logical" {
		t.Errorf("cheaper = %q, want logical", est.Cheaper)
	}
	// Fail-safe counts in the effective ratio but not the data ratio.
	est = storageEstimate(storageReq(StorageVolumes{ActiveLogical: 300, ActivePhysical: 100, FailSafe: 50}, false))
	near(t, "data ratio", est.DataCompressionRatio, 3)
	near(t, "effective ratio", est.EffectiveCompressionRatio, 2)
}

func TestStorageDiscountAndUnits(t *testing.T) {
	list := storageEstimate(storageReq(StorageVolumes{ActiveLogical: 1000}, false))
	req := storageReq(StorageVolumes{ActiveLogical: 1000}, false)
	req.DiscountPct = 25
	near(t, "25% off", storageEstimate(req).Logical.Total, list.Logical.Total*0.75)
	req.DiscountPct = 150
	near(t, "clamped to 100%", storageEstimate(req).Logical.Total, 0)

	tib := StorageCalcRequest{Unit: "TiB", Volumes: StorageVolumes{ActiveLogical: 1}, Rates: usCentral1()}
	near(t, "1 TiB", storageEstimate(tib).Logical.Total, 1024*0.023)
}

func TestStorageRequestValidation(t *testing.T) {
	bad := storageReq(StorageVolumes{ActiveLogical: -1}, false)
	if bad.validate() == nil {
		t.Error("negative volume accepted")
	}
	bad = storageReq(StorageVolumes{}, false)
	bad.Unit = "MB"
	if bad.validate() == nil {
		t.Error("unknown unit accepted")
	}
}

// -------------------------------------------------------------- slots

func burst() PeakWindow { return PeakWindow{ID: "w", Start: "02:00", End: "03:30", Slots: 4000} }

func slotsReq(edition string, baseline, committed float64, windows ...PeakWindow) SlotsCalcRequest {
	ed, _ := editionPreset(edition)
	return SlotsCalcRequest{
		Edition: edition, BaselineSlots: baseline, CommittedSlots: committed, Windows: windows,
		PaygRate: ed.Payg, CommitRate: ed.Commit["1yr"],
	}
}

func TestTimeToBucketAndWindowMask(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "02:00": 8, "03:30": 14, "24:00": 0, "23:59": 0} {
		got, err := timeToBucket(in)
		if err != nil || got != want {
			t.Errorf("timeToBucket(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "2:00pm", "25:00", "10:60", "1000"} {
		if _, err := timeToBucket(bad); err == nil {
			t.Errorf("timeToBucket(%q) accepted", bad)
		}
	}
	count := func(m [calcBuckets]bool) int {
		n := 0
		for _, b := range m {
			if b {
				n++
			}
		}
		return n
	}
	plain := windowMask(burst())
	if count(plain) != 6 || !plain[8] || plain[14] {
		t.Errorf("02:00-03:30 covers %d buckets, [8]=%v [14]=%v", count(plain), plain[8], plain[14])
	}
	wrapped := windowMask(PeakWindow{Start: "22:00", End: "02:00"})
	if count(wrapped) != 16 || !wrapped[95] || !wrapped[0] {
		t.Errorf("22:00-02:00 wrap covers %d buckets", count(wrapped))
	}
	if count(windowMask(PeakWindow{Start: "05:00", End: "05:00"})) != 0 {
		t.Error("zero-length window is not empty")
	}
}

func TestSlotsPayAsYouGoOnly(t *testing.T) {
	est := slotsEstimate(slotsReq("enterprise", 1000, 0, burst()))
	// 1000 slots x 24h + 4000 slots x 1.5h, all PAYG at $0.06.
	near(t, "daily", est.Daily.Total, (24000+6000)*0.06)
	near(t, "uncommitted baseline", est.Daily.UncommittedBaseline, 24000*0.06)
	near(t, "autoscaled", est.Daily.Autoscaled, 6000*0.06)
	near(t, "committed", est.Daily.Committed, 0)
	near(t, "monthly", est.Monthly.Total, est.Daily.Total*730/24)
	near(t, "peak", est.PeakSlots, 5000)
	if len(est.Warnings) != 0 {
		t.Errorf("unexpected warnings %v", est.Warnings)
	}
}

func TestSlotsCommitmentIsA24x7FloorAtCommitRate(t *testing.T) {
	est := slotsEstimate(slotsReq("enterprise", 1000, 1000, burst()))
	near(t, "committed", est.Daily.Committed, 1000*24*0.048)
	near(t, "uncommitted baseline", est.Daily.UncommittedBaseline, 0)
	near(t, "autoscaled", est.Daily.Autoscaled, 4000*1.5*0.06)
	near(t, "utilization", est.CommitUtilization, 1)
	// Baseline above the commitment bills 24x7 at PAYG (docs: 1000 baseline
	// on 800 committed -> 200 at PAYG).
	est = slotsEstimate(slotsReq("enterprise", 1000, 800))
	near(t, "committed", est.Daily.Committed, 800*24*0.048)
	near(t, "uncommitted baseline", est.Daily.UncommittedBaseline, 200*24*0.06)
	near(t, "autoscaled", est.Daily.Autoscaled, 0)
	// Commitment above the baseline raises the floor and absorbs bursts.
	est = slotsEstimate(slotsReq("enterprise", 500, 1000, PeakWindow{Start: "02:00", End: "03:00", Slots: 400}))
	near(t, "autoscaled absorbed", est.Daily.Autoscaled, 0)
	near(t, "utilization", est.CommitUtilization, (500*24+400)/(1000*24.0))
}

func TestSlotsAutoscaleRoundsUpTo50PerBucket(t *testing.T) {
	// Two overlapping 1,010-slot bursts autoscale 2,020 -> 2,050, not 2 x 1,050.
	w1 := PeakWindow{Start: "02:00", End: "03:00", Slots: 1010}
	w2 := PeakWindow{Start: "02:00", End: "03:00", Slots: 1010}
	est := slotsEstimate(slotsReq("enterprise", 0, 0, w1, w2))
	near(t, "autoscaled", est.Daily.Autoscaled, 2050*1*0.06)
	near(t, "billed", est.Profile.Billed[8], 2050)
	near(t, "demand", est.Profile.Demand[8], 2020)
}

func TestSlotsBaselineAndCommitmentRoundUpTo50(t *testing.T) {
	est := slotsEstimate(slotsReq("enterprise", 1010, 20))
	near(t, "baseline", est.EffectiveBaseline, 1050)
	near(t, "committed", est.EffectiveCommitted, 50)
	near(t, "committed cost", est.Daily.Committed, 50*24*0.048)
	near(t, "uncommitted baseline cost", est.Daily.UncommittedBaseline, 1000*24*0.06)
	if len(est.Warnings) != 2 {
		t.Errorf("warnings = %v, want baseline and commitment rounding notes", est.Warnings)
	}
}

func TestSlotsStandardEditionRules(t *testing.T) {
	est := slotsEstimate(slotsReq("standard", 1000, 500, PeakWindow{Start: "02:00", End: "03:00", Slots: 2000}))
	near(t, "baseline zeroed", est.EffectiveBaseline, 0)
	near(t, "commitment zeroed", est.EffectiveCommitted, 0)
	near(t, "profile baseline", est.Profile.Baseline[0], 0)
	// Only the burst bills, autoscaled and capped at 1,600.
	near(t, "billed peak", est.Profile.Billed[8], 1600)
	near(t, "daily", est.Daily.Total, 1600*1*0.04)
	if len(est.Sweep) != 0 || est.BestCommit != nil {
		t.Error("standard edition should not sweep commitments")
	}
	joined := strings.Join(est.Warnings, "\n")
	for _, want := range []string{"baseline slots were set to 0", "committed slots were set to 0", "1,600 slots"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q missing %q", joined, want)
		}
	}
}

func TestSlotsHourlySplit(t *testing.T) {
	est := slotsEstimate(slotsReq("enterprise", 0, 0, burst()))
	near(t, "02:00 hour", est.HourlyCost[2].Autoscaled, 4000*0.06)
	near(t, "03:00 half hour", est.HourlyCost[3].Autoscaled, 4000*0.5*0.06)
	near(t, "04:00 idle", est.HourlyCost[4].Total, 0)
}

func TestSlotsCommitmentSweepFindsCheapestLevel(t *testing.T) {
	est := slotsEstimate(slotsReq("enterprise", 1000, 0, burst()))
	if est.Sweep[0].Committed != 0 || est.Sweep[len(est.Sweep)-1].Committed != 5000 {
		t.Errorf("sweep range %v..%v", est.Sweep[0].Committed, est.Sweep[len(est.Sweep)-1].Committed)
	}
	if est.Sweep[1].Committed != 50 {
		t.Errorf("sweep step = %v, want 50", est.Sweep[1].Committed)
	}
	// The flat 1000 is used 24x7 so committing it is cheaper; the 90-minute
	// burst is not worth a 24x7 commitment.
	if est.BestCommit == nil || est.BestCommit.Committed != 1000 {
		t.Errorf("best = %+v, want 1000", est.BestCommit)
	}
}

func TestSlotsRequestValidation(t *testing.T) {
	if r := slotsReq("premium", 0, 0); r.validate() == nil {
		t.Error("unknown edition accepted")
	}
	if r := slotsReq("enterprise", -5, 0); r.validate() == nil {
		t.Error("negative baseline accepted")
	}
	if r := slotsReq("enterprise", 0, 0, PeakWindow{Start: "9am", End: "10:00"}); r.validate() == nil {
		t.Error("bad time accepted")
	}
}

// ----------------------------------------------------------- handlers

func TestCalculatorHandlersRoundTrip(t *testing.T) {
	h := &APIHandler{}

	rec := httptest.NewRecorder()
	h.CalculatorPresets(rec, httptest.NewRequest("GET", "/api/calculator/presets", nil))
	var presets CalculatorPresets
	if err := json.NewDecoder(rec.Body).Decode(&presets); err != nil || len(presets.Editions) != 3 {
		t.Fatalf("presets: status %d err %v editions %d", rec.Code, err, len(presets.Editions))
	}

	body, _ := json.Marshal(StorageCalcRequest{Unit: "TiB", Volumes: StorageVolumes{ActiveLogical: 100, LongTermLogical: 50}, Rates: presets.StorageRegions[0].Rates, FreeTier: true})
	rec = httptest.NewRecorder()
	h.CalculateStorage(rec, httptest.NewRequest("POST", "/api/calculator/storage", bytes.NewReader(body)))
	var se StorageEstimate
	if err := json.NewDecoder(rec.Body).Decode(&se); err != nil || rec.Code != 200 {
		t.Fatalf("storage: status %d err %v", rec.Code, err)
	}
	near(t, "storage total", se.Logical.Total, 3174.01)

	body, _ = json.Marshal(slotsReq("standard", 1000, 0))
	rec = httptest.NewRecorder()
	h.CalculateSlots(rec, httptest.NewRequest("POST", "/api/calculator/slots", bytes.NewReader(body)))
	var sl SlotsEstimate
	if err := json.NewDecoder(rec.Body).Decode(&sl); err != nil || rec.Code != 200 {
		t.Fatalf("slots: status %d err %v", rec.Code, err)
	}
	if sl.EffectiveBaseline != 0 || len(sl.Warnings) == 0 {
		t.Errorf("standard baseline = %v warnings %v", sl.EffectiveBaseline, sl.Warnings)
	}

	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/calculator/storage", ""},
		{"POST", "/api/calculator/storage", "{not json"},
		{"POST", "/api/calculator/slots", `{"edition":"gold"}`},
		{"POST", "/api/calculator/storage", `{"unit":"GiB","volumes":{"active_logical":-1}}`},
	} {
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.path == "/api/calculator/storage" {
			h.CalculateStorage(rec, req)
		} else {
			h.CalculateSlots(rec, req)
		}
		if rec.Code < 400 {
			t.Errorf("%s %s %q: status %d, want 4xx", tc.method, tc.path, tc.body, rec.Code)
		}
	}
}
