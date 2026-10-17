package main

import (
	"math"
	"strings"
	"testing"
)

func TestHolderEngineHandBuiltChain(t *testing.T) {
	d0 := "2026-10-10"
	d1 := "2026-10-11"

	u0 := utxoState{d0: 5_000_000_000}
	u := u0.clone()

	// On d1: coinbase = 5,010,000,000 (50 BTC + 0.1 BTC fee), one tx spends
	// 1,000,000,000 from d0 and creates 990,000,000 -> created = 6,000,000,000.
	spentD1 := map[string]int64{d0: 1_000_000_000}
	var (
		coinbaseSats int64 = 5_010_000_000
		feesSats     int64 = 10_000_000
		opretSats    int64 = 0
		createdSats  int64 = 6_000_000_000
	)

	prevSum := u.totalSats()
	applyDay(u, d1, spentD1, createdSats, true)
	nextSum := u.totalSats()

	if u[d0] != 4_000_000_000 || u[d1] != 6_000_000_000 {
		t.Fatalf("U1 = %+v, want {d0: 4B, d1: 6B}", u)
	}
	if err := checkSupplyIdentity(prevSum, nextSum, coinbaseSats, feesSats, opretSats); err != nil {
		t.Fatalf("checkSupplyIdentity: %v", err)
	}
	if err := checkBucketSigns(u, d1); err != nil {
		t.Fatalf("checkBucketSigns: %v", err)
	}

	prices := map[string]float64{
		d0: 1.0,
		d1: 2.0,
	}
	hm := holderMetrics(d1, 1, u, prices, true)
	if hm.SupplySats != 10_000_000_000 {
		t.Fatalf("SupplySats = %d, want 10,000,000,000", hm.SupplySats)
	}
	if hm.RealizedCapUSD == nil || math.Abs(*hm.RealizedCapUSD-160.0) > 1e-9 {
		t.Fatalf("RealizedCapUSD = %v, want 160.0", hm.RealizedCapUSD)
	}
	if hm.MVRV == nil || math.Abs(*hm.MVRV-1.25) > 1e-9 {
		t.Fatalf("MVRV = %v, want 1.25", hm.MVRV)
	}
	if hm.SupplyInProfitPct == nil || math.Abs(*hm.SupplyInProfitPct-40.0) > 1e-9 {
		t.Fatalf("SupplyInProfitPct = %v, want 40.0", hm.SupplyInProfitPct)
	}

	cm := coinageMetrics(d1, spentD1, prices, true)
	if cm.SOPR == nil || math.Abs(*cm.SOPR-2.0) > 1e-9 {
		t.Fatalf("SOPR = %v, want 2.0", cm.SOPR)
	}
	if cm.CDDSatDays != 1_000_000_000 {
		t.Fatalf("CDDSatDays = %d, want 1,000,000,000", cm.CDDSatDays)
	}
	if math.Abs(cm.DormancyDays-1.0) > 1e-9 {
		t.Fatalf("DormancyDays = %v, want 1.0", cm.DormancyDays)
	}

	// Backward step restores U0 exactly.
	applyDay(u, d1, spentD1, createdSats, false)
	if len(u) != 1 || u[d0] != u0[d0] {
		t.Fatalf("after backward applyDay, u = %+v, want %+v", u, u0)
	}
}

func TestHolderEngineNegativeAgeRows(t *testing.T) {
	d := "2026-10-15"
	dPlus1 := "2026-10-16"

	u := utxoState{"2026-10-01": 1000}
	// On day d, a block dated d spends 100 sats created by its parent block (dated d+1).
	spentD := map[string]int64{
		"2026-10-01": 200,
		dPlus1:       100,
	}
	applyDay(u, d, spentD, 500, true)
	if u[dPlus1] != -100 {
		t.Fatalf("future bucket %s after day %s = %d, want -100", dPlus1, d, u[dPlus1])
	}
	if err := checkBucketSigns(u, d); err != nil {
		t.Fatalf("expected transient -100 in d+1 to pass checkBucketSigns on day d, got %v", err)
	}

	// CDD clamps negative age to 0.
	cm := coinageMetrics(d, spentD, nil, false)
	// 200 sats * 14 days + 100 sats * max(0, -1) = 2800 sat-days.
	if cm.CDDSatDays != 2800 {
		t.Fatalf("CDDSatDays = %d, want 2800 (negative age clamped to 0)", cm.CDDSatDays)
	}

	// On day d+1, block dated d+1 creates 600 sats (which includes the 100 sats spent on d).
	applyDay(u, dPlus1, map[string]int64{}, 600, true)
	if u[dPlus1] != 500 {
		t.Fatalf("bucket %s after day %s = %d, want 500 (>= 0)", dPlus1, dPlus1, u[dPlus1])
	}
	if err := checkBucketSigns(u, dPlus1); err != nil {
		t.Fatalf("checkBucketSigns after d+1: %v", err)
	}
}

func TestHolderEngineInversionAcrossBaseline(t *testing.T) {
	// Baseline covers blocks with timestamp < T0 (2026-10-15).
	// Suppose block N (dated T0 = 2026-10-15) created 300 sats, and child block N+1
	// (dated T0-1 = 2026-10-14) spent 100 of those sats before T0.
	// Neither output nor spend is in the < T0 unspent baseline for bucket T0,
	// but day T0's created_sats includes all 300 sats created on T0.
	// Pre-subtracting S_{T0-1}[T0] = 100 initializes bucket T0 to -100 so that
	// after applying day T0 (created = 300, spent = 0), bucket T0 is 200 (exact).
	t0 := "2026-10-15"
	baseline := map[string]int64{
		"2026-10-10": 5000,
	}
	crossBoundary := map[string]int64{
		"2026-10-10": 400, // normal spend on T0-1, ignored by initFromBaseline (c < T0)
		t0:           100, // cross-boundary inversion spend on T0-1 (c >= T0)
	}

	u := initFromBaseline(baseline, t0, crossBoundary)
	if u[t0] != -100 || u["2026-10-10"] != 5000 {
		t.Fatalf("initFromBaseline = %+v, want {2026-10-10: 5000, 2026-10-15: -100}", u)
	}

	// Roll forward day T0 where 300 sats were created on T0 (100 of which were already spent on T0-1).
	applyDay(u, t0, nil, 300, true)
	if u[t0] != 200 {
		t.Fatalf("bucket %s after rolling forward T0 = %d, want 200", t0, u[t0])
	}
	if err := checkBucketSigns(u, t0); err != nil {
		t.Fatalf("checkBucketSigns after T0: %v", err)
	}
}

func TestHolderPriceGate(t *testing.T) {
	u := utxoState{"2026-10-01": 100_000_000}
	hm := holderMetrics("2026-10-15", 1, u, map[string]float64{"2026-10-15": 80000}, false)
	if hm.Blocked != "price_history_incomplete" {
		t.Fatalf("Blocked = %q, want price_history_incomplete", hm.Blocked)
	}
	if hm.RealizedCapUSD != nil || hm.RealizedPriceUSD != nil || hm.MVRV != nil || hm.SupplyInProfitPct != nil {
		t.Fatalf("expected price-dependent fields to be nil when priceComplete=false: %+v", hm)
	}
	if hm.SupplySats != 100_000_000 || len(hm.HODLWavesPct) != 12 {
		t.Fatalf("expected price-independent fields to still be computed: %+v", hm)
	}
}

func TestSupplyIdentityMismatch(t *testing.T) {
	// Off by 1 satoshi -> must fail (no tolerance).
	err := checkSupplyIdentity(1000, 1500, 501, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "supply_identity_mismatch") {
		t.Fatalf("expected 1-sat mismatch to fail checkSupplyIdentity, got %v", err)
	}
}

func TestIssuanceBound(t *testing.T) {
	// Exact match -> no warning, no error.
	if warn, err := checkIssuanceBound(45_000_000_000, 45_000_000_000); err != nil || warn != "" {
		t.Fatalf("exact match: warn=%q err=%v", warn, err)
	}
	// Underclaim -> warning, no error.
	if warn, err := checkIssuanceBound(44_999_999_999, 45_000_000_000); err != nil || !strings.Contains(warn, "underclaimed_issuance") {
		t.Fatalf("underclaim: warn=%q err=%v", warn, err)
	}
	// Overclaim by 1 sat -> error.
	if _, err := checkIssuanceBound(45_000_000_001, 45_000_000_000); err == nil || !strings.Contains(err.Error(), "issuance_bound_exceeded") {
		t.Fatalf("expected overclaim by 1 sat to error, got %v", err)
	}
}

func TestHODLWaveBands(t *testing.T) {
	u := utxoState{
		"2026-10-15": 100, // age 0 -> <1d
		"2026-10-12": 100, // age 3 -> 1d_1w
		"2026-10-01": 100, // age 14 -> 1w_1m
		"2026-08-15": 100, // age 61 -> 1m_3m
		"2026-06-01": 100, // age 136 -> 3m_6m
		"2026-01-01": 100, // age 287 -> 6m_12m
		"2025-01-01": 100, // age 652 -> 1y_2y
		"2024-01-01": 100, // age 1018 -> 2y_3y
		"2022-10-15": 100, // age 1461 -> 3y_5y
		"2020-10-15": 100, // age 2191 -> 5y_7y
		"2018-10-15": 100, // age 2922 -> 7y_10y
		"2015-01-01": 100, // age 4305 -> >10y
	}
	hm := holderMetrics("2026-10-15", 1, u, nil, false)
	if len(hm.HODLWavesPct) != 12 {
		t.Fatalf("len(HODLWavesPct) = %d, want 12", len(hm.HODLWavesPct))
	}
	wantPct := 100.0 / 12.0
	for _, b := range btcAgeBands {
		if math.Abs(hm.HODLWavesPct[b]-wantPct) > 1e-6 {
			t.Errorf("band %s = %v, want %v", b, hm.HODLWavesPct[b], wantPct)
		}
	}
}

func TestLTHSTHSplit(t *testing.T) {
	// 2026-10-15 minus 154 days = 2026-05-14 (STH, < 155 d).
	// 2026-10-15 minus 155 days = 2026-05-13 (LTH, >= 155 d).
	u := utxoState{
		"2026-05-14": 300_000_000, // 3 BTC STH @ $60k
		"2026-05-13": 700_000_000, // 7 BTC LTH @ $40k
	}
	prices := map[string]float64{
		"2026-05-14": 60000,
		"2026-05-13": 40000,
		"2026-10-15": 80000,
	}
	hm := holderMetrics("2026-10-15", 1, u, prices, true)
	if hm.STHSupplySats != 300_000_000 || hm.LTHSupplySats != 700_000_000 {
		t.Fatalf("STH=%d LTH=%d, want 300M / 700M", hm.STHSupplySats, hm.LTHSupplySats)
	}
	if hm.STHRealizedPrice == nil || math.Abs(*hm.STHRealizedPrice-60000) > 1e-6 {
		t.Fatalf("STHRealizedPrice = %v, want 60000", hm.STHRealizedPrice)
	}
	if hm.LTHRealizedPrice == nil || math.Abs(*hm.LTHRealizedPrice-40000) > 1e-6 {
		t.Fatalf("LTHRealizedPrice = %v, want 40000", hm.LTHRealizedPrice)
	}
}
