package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func seedMaintenanceChain(t *testing.T, store *riskStore, now time.Time) {
	t.Helper()
	ctx := context.Background()
	// Seed rollup for 2026-10-14 (T0-1) and 2026-10-15 (T0).
	// On 2026-10-14: coinbase 50 BTC + 0.1 BTC fee = 5,010,000,000; spend 10 BTC from 2026-10-10; created = 59.9 BTC + 0.1 = 6,000,000,000.
	// On 2026-10-15: coinbase 50 BTC = 5,000,000,000; spend 5 BTC from 2026-10-10; created = 5,500,000,000.
	rollupPayload := btcRollupPayload{
		CoverageStart: 1,
		Blocks: []btcBlockRow{
			{Height: 1, TS: "2026-10-14T12:00:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 100, SubsidySats: 5_000_000_000},
			{Height: 2, TS: "2026-10-15T12:00:00Z", Date: "2026-10-15", Bits: "17021ef0", TransactionCount: 100, SubsidySats: 5_000_000_000},
		},
		Days: []BtcOnchainDay{
			{
				Date:                  "2026-10-14",
				Blocks:                1,
				ScheduledIssuanceSats: 5_000_000_000,
				ClaimedIssuanceSats:   5_000_000_000,
				CoinbaseSats:          5_010_000_000,
				FeesSats:              10_000_000,
				CreatedSats:           6_000_000_000,
				OpretValueSats:        0,
			},
			{
				Date:                  "2026-10-15",
				Blocks:                1,
				ScheduledIssuanceSats: 5_000_000_000,
				ClaimedIssuanceSats:   5_000_000_000,
				CoinbaseSats:          5_000_000_000,
				FeesSats:              0,
				CreatedSats:           5_500_000_000,
				OpretValueSats:        0,
			},
		},
	}
	if err := store.applyBtcRollupBatch(ctx, rollupPayload, "2026-10-14", "2026-10-15", now); err != nil {
		t.Fatal(err)
	}

	caPayload := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2026-10-14", CreateDay: "2026-10-10", Sats: 1_000_000_000, Inputs: 1},
			{SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			coinageMetrics("2026-10-14", map[string]int64{"2026-10-10": 1_000_000_000}, nil, false),
			coinageMetrics("2026-10-15", map[string]int64{"2026-10-10": 500_000_000}, nil, false),
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, caPayload, "2026-10-14", "2026-10-15", now); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceAppliesPendingBaseline(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seedMaintenanceChain(t, store, now)

	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate CLI baseline write at AsOf = 2026-10-15 (state after 2026-10-14):
	// {2026-10-10: 4,000,000,000, 2026-10-14: 6,000,000,000} -> total 10,000,000,000 sats.
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	st, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Pending || st.Generation != 1 {
		t.Fatalf("after commitBtcBaseline: Pending=%v Gen=%d", st.Pending, st.Generation)
	}

	maint := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatalf("maint.run: %v", err)
	}

	// Pending flag must be cleared.
	if _, hasPend, _ := store.getBtcKV(ctx, btcBaselinePendingKey); hasPend {
		t.Fatalf("expected baseline_pending flag to be cleared after maintenance")
	}

	// UTXO state must be rolled forward through 2026-10-15:
	// 2026-10-10: 4B - 0.5B = 3.5B; 2026-10-14: 6B; 2026-10-15: 5.5B -> total 15B.
	asOf, err := store.getBtcUTXOStateAsOf(ctx)
	if err != nil || asOf != "2026-10-15" {
		t.Fatalf("utxo_state_as_of = %q, want 2026-10-15 (err=%v)", asOf, err)
	}
	uState, err := store.loadBtcUTXOState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uState.totalSats() != 15_000_000_000 {
		t.Fatalf("uState totalSats = %d, want 15,000,000,000", uState.totalSats())
	}

	// Holder rows for 2026-10-13 (reconstructed backward before 2026-10-14),
	// 2026-10-14 (init day), and 2026-10-15 (rolled forward) must be upserted.
	rs := newBtcReadStore(store, settings, func() time.Time { return now })
	hDays, err := rs.holderDays(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatal(err)
	}
	if len(hDays) != 3 {
		t.Fatalf("len(holderDays) = %d, want 3: %+v", len(hDays), hDays)
	}
	// On 2026-10-13 (before 2026-10-14): supply = 5,000,000,000.
	// On 2026-10-14: supply = 10,000,000,000.
	// On 2026-10-15: supply = 15,000,000,000.
	if hDays[0].SupplySats != 5_000_000_000 || hDays[1].SupplySats != 10_000_000_000 || hDays[2].SupplySats != 15_000_000_000 {
		t.Fatalf("holderDays supplies = [%d, %d, %d], want [5B, 10B, 15B]",
			hDays[0].SupplySats, hDays[1].SupplySats, hDays[2].SupplySats)
	}
}

func TestMaintenanceNeverRollsForwardOverPending(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seedMaintenanceChain(t, store, now)

	// Pre-populate stale/corrupted UTXO state at 2026-10-14 (wrong 999 sats).
	staleState := utxoState{"2026-10-10": 999}
	if err := store.applyHolderMaintenanceTx(ctx, &staleState, "2026-10-14", 1, false, nil, nil, now); err != nil {
		t.Fatal(err)
	}

	// Now write a new pending baseline (generation 2, AsOf 2026-10-15).
	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	if _, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil); err != nil {
		t.Fatal(err)
	}

	// Maintenance must apply generation 2 instead of rolling forward from the stale 999-sat state.
	maint := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatalf("maint.run: %v", err)
	}
	uState, err := store.loadBtcUTXOState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uState.totalSats() != 15_000_000_000 {
		t.Fatalf("expected pending baseline to reset state to 15B sats, got %d", uState.totalSats())
	}
}

func TestRecomputeHoldersUpsertOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// Seed an older holder row for 2026-01-01 that is no longer in the matrix.
	oldRow := BtcHolderDay{
		Date:               "2026-01-01",
		BaselineGeneration: 1,
		SupplySats:         1_000_000_000,
		SupplyBTC:          10.0,
		HODLWavesPct:       map[string]float64{"<1d": 100},
	}
	if err := store.applyHolderMaintenanceTx(ctx, nil, "", 1, false, []BtcHolderDay{oldRow}, nil, now); err != nil {
		t.Fatal(err)
	}

	seedMaintenanceChain(t, store, now)
	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	if _, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil); err != nil {
		t.Fatal(err)
	}

	maint := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatal(err)
	}

	rs := newBtcReadStore(store, settings, func() time.Time { return now })
	rows, err := rs.holderDays(ctx, "2026-01-01", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Date != "2026-01-01" || rows[0].SupplySats != 1_000_000_000 {
		t.Fatalf("expected older 2026-01-01 holder row to be preserved (upsert-only), got %+v", rows)
	}
}

func TestRecomputeWaitsForRollup(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// State is at 2026-10-14. Coin-age has advanced to 2026-10-15, but rollup is still at 2026-10-14.
	initState := utxoState{"2026-10-10": 4_000_000_000, "2026-10-14": 6_000_000_000}
	if err := store.applyHolderMaintenanceTx(ctx, &initState, "2026-10-14", 1, false, nil, nil, now); err != nil {
		t.Fatal(err)
	}

	rollupPayload := btcRollupPayload{
		CoverageStart: 1,
		Blocks: []btcBlockRow{
			{Height: 1, TS: "2026-10-14T12:00:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 100, SubsidySats: 5_000_000_000},
		},
		Days: []BtcOnchainDay{
			{Date: "2026-10-14", Blocks: 1, ScheduledIssuanceSats: 5_000_000_000, ClaimedIssuanceSats: 5_000_000_000, CoinbaseSats: 5_010_000_000, FeesSats: 10_000_000, CreatedSats: 6_000_000_000},
		},
	}
	if err := store.applyBtcRollupBatch(ctx, rollupPayload, "2026-10-14", "2026-10-14", now); err != nil {
		t.Fatal(err)
	}
	caPayload := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			coinageMetrics("2026-10-15", map[string]int64{"2026-10-10": 500_000_000}, nil, false),
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, caPayload, "2026-10-15", "2026-10-15", now); err != nil {
		t.Fatal(err)
	}

	maint := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatal(err)
	}

	// Because rollup cursor is still 2026-10-14 (< 2026-10-15), state must stay at 2026-10-14.
	asOf, _ := store.getBtcUTXOStateAsOf(ctx)
	if asOf != "2026-10-14" {
		t.Fatalf("expected UTXO state to wait for rollup at 2026-10-14, got %q", asOf)
	}
}

func TestMaintenanceUnblocksPriceHistoryIncompleteOnceComplete(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seedMaintenanceChain(t, store, now)

	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	if _, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil); err != nil {
		t.Fatal(err)
	}

	// First run with nil prices -> holder rows are written with Blocked = "price_history_incomplete".
	maintNoPrices := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maintNoPrices.run(ctx); err != nil {
		t.Fatalf("first maint.run: %v", err)
	}
	rs := newBtcReadStore(store, settings, func() time.Time { return now })
	hDays1, err := rs.holderDays(ctx, "2026-10-14", "2026-10-15")
	if err != nil || len(hDays1) != 2 {
		t.Fatalf("holderDays after first run: len=%d err=%v", len(hDays1), err)
	}
	if hDays1[1].Blocked != "price_history_incomplete" || hDays1[1].RealizedCapUSD != nil {
		t.Fatalf("expected 2026-10-15 blocked by price_history_incomplete, got %+v", hDays1[1])
	}

	// Now seed complete daily prices from 2010-08-18 through 2026-10-15 and run maintenance again with no new chain days.
	ps := newBtcPriceSyncer(store, settings, func() time.Time { return now })
	var pricePts []btcPriceDay
	start, _ := time.Parse("2006-01-02", btcPriceCoverageStart)
	end, _ := time.Parse("2006-01-02", "2026-10-15")
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		pricePts = append(pricePts, btcPriceDay{
			Day:    d.Format("2006-01-02"),
			Close:  60000.0,
			Source: "test",
		})
	}
	ps.putRows(ctx, pricePts)

	maintWithPrices := newBtcHolderMaintenance(store, ps, func() time.Time { return now })
	if err := maintWithPrices.run(ctx); err != nil {
		t.Fatalf("second maint.run with complete prices: %v", err)
	}
	hDays2, err := rs.holderDays(ctx, "2026-10-14", "2026-10-15")
	if err != nil || len(hDays2) != 2 {
		t.Fatalf("holderDays after second run: len=%d err=%v", len(hDays2), err)
	}
	if hDays2[1].Blocked != "" || hDays2[1].RealizedCapUSD == nil || hDays2[1].MVRV == nil {
		t.Fatalf("expected 2026-10-15 unblocked with RealizedCapUSD and MVRV, got %+v", hDays2[1])
	}
}

func TestBtcHolderEngineHaltedExposedAndDeduped(t *testing.T) {
	ctx := context.Background()
	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	seedMaintenanceChain(t, store, now)

	settings := (BitcoinConfig{}).settings()
	settings.Jobs = true
	budget := newBtcBudget(store, settings, func() time.Time { return now })
	resID, err := budget.reserve(ctx, btcBaselineJobID, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	baseRows := []btcUTXOBaselineRow{
		{CreateDay: "2026-10-10", Sats: 4_000_000_000, UTXOs: 4},
		{CreateDay: "2026-10-14", Sats: 6_000_000_000, UTXOs: 6},
	}
	if _, err := store.commitBtcBaseline(ctx, budget, settings, resID, "2026-10-15", baseRows, 1<<30, now, nil); err != nil {
		t.Fatal(err)
	}

	maint := newBtcHolderMaintenance(store, nil, func() time.Time { return now })
	if err := maint.run(ctx); err != nil {
		t.Fatal(err)
	}

	// Now add 2026-10-16 where 1 unmatched input (1,000 sats) causes spent_sats (499,999,000)
	// to differ from CreatedSats - (CoinbaseSats - FeesSats) = 500,000,000.
	rollup16 := btcRollupPayload{
		CoverageStart: 1,
		Blocks: []btcBlockRow{
			{Height: 3, TS: "2026-10-16T12:00:00Z", Date: "2026-10-16", Bits: "17021ef0", TransactionCount: 100, SubsidySats: 5_000_000_000},
		},
		Days: []BtcOnchainDay{
			{
				Date:                  "2026-10-16",
				Blocks:                1,
				ScheduledIssuanceSats: 5_000_000_000,
				ClaimedIssuanceSats:   5_000_000_000,
				CoinbaseSats:          5_000_000_000,
				FeesSats:              0,
				CreatedSats:           5_500_000_000,
				OpretValueSats:        0,
			},
		},
	}
	if err := store.applyBtcRollupBatch(ctx, rollup16, "2026-10-14", "2026-10-16", now); err != nil {
		t.Fatal(err)
	}
	ca16 := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2026-10-16", CreateDay: "2026-10-14", Sats: 499_999_000, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			coinageMetrics("2026-10-16", map[string]int64{"2026-10-14": 499_999_000}, nil, false),
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, ca16, "2026-10-14", "2026-10-16", now); err != nil {
		t.Fatal(err)
	}

	if err := maint.run(ctx); err != nil {
		t.Fatal(err)
	}

	// State cursor must not advance past 2026-10-15.
	asOf, err := store.getBtcUTXOStateAsOf(ctx)
	if err != nil || asOf != "2026-10-15" {
		t.Fatalf("utxo_state_as_of = %q, want 2026-10-15 (err=%v)", asOf, err)
	}

	h := newTestBitcoinHandler(t)
	h.bq = nil
	h.btc.now = func() time.Time { return now }
	h.btc.read = newBtcReadStore(store, settings, h.btc.now)

	req := httptest.NewRequest("GET", "/api/opendata/bitcoin/onchain/holders?days=30", nil)
	rec := httptest.NewRecorder()
	h.BitcoinOnchainHolders(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		HolderEngine struct {
			State     string `json:"state"`
			HaltedDay string `json:"halted_day"`
			Reason    string `json:"reason"`
			Since     string `json:"since"`
			Remedy    string `json:"remedy"`
		} `json:"holder_engine"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.HolderEngine.State != "halted" || resp.HolderEngine.HaltedDay != "2026-10-16" || resp.HolderEngine.Reason != "supply_identity_mismatch" {
		t.Fatalf("holder_engine = %+v, want state=halted halted_day=2026-10-16 reason=supply_identity_mismatch", resp.HolderEngine)
	}
	if resp.HolderEngine.Since == "" || resp.HolderEngine.Remedy == "" {
		t.Fatalf("holder_engine missing since or remedy: %+v", resp.HolderEngine)
	}

	// Running maintenance an hour later on the same halted day must preserve Since (deduped).
	firstSince := resp.HolderEngine.Since
	later := now.Add(time.Hour)
	maintLater := newBtcHolderMaintenance(store, nil, func() time.Time { return later })
	if err := maintLater.run(ctx); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	h.BitcoinOnchainHolders(rec2, req)
	var resp2 struct {
		HolderEngine struct {
			State     string `json:"state"`
			HaltedDay string `json:"halted_day"`
			Reason    string `json:"reason"`
			Since     string `json:"since"`
			Remedy    string `json:"remedy"`
		} `json:"holder_engine"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.HolderEngine.Since != firstSince {
		t.Fatalf("expected repeated halt on same day to preserve Since=%q, got %q", firstSince, resp2.HolderEngine.Since)
	}
}
