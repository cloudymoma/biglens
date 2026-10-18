package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func seedTestPrices(t *testing.T, h *APIHandler, fromStr, toStr string, fn func(idx int, d civil.Date) float64) {
	t.Helper()
	from, err := civil.ParseDate(fromStr)
	if err != nil {
		t.Fatal(err)
	}
	to, err := civil.ParseDate(toStr)
	if err != nil {
		t.Fatal(err)
	}
	var rows []btcPriceDay
	idx := 0
	for d := from; !d.After(to); d = d.AddDays(1) {
		rows = append(rows, btcPriceDay{
			Day:    d.String(),
			Close:  fn(idx, d),
			Source: "coinbase",
		})
		idx++
	}
	h.btc.prices.putRows(context.Background(), rows)
}

func stubValuationUpstreams(t *testing.T, cmJSON, fngJSON []byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cm":
			w.Write(cmJSON)
		case "/fng":
			w.Write(fngJSON)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	origCM := btcCoinMetricsBaseURL
	origFnG := btcAlternativeMeFnGURL
	t.Cleanup(func() {
		btcCoinMetricsBaseURL = origCM
		btcAlternativeMeFnGURL = origFnG
	})
	btcCoinMetricsBaseURL = srv.URL + "/cm"
	btcAlternativeMeFnGURL = srv.URL + "/fng"
}

func TestBitcoinValuationRanges(t *testing.T) {
	fngRaw, err := os.ReadFile("testdata/bitcoin/alternative_fng.json")
	if err != nil {
		t.Fatal(err)
	}
	stubValuationUpstreams(t, []byte(`{"data":[]}`), fngRaw)

	h := newTestBitcoinHandler(t)
	// Seed 2012-01-01 .. 2026-10-15 (5,402 days) so all 4 halving epochs and 1400d WMA200 are warm.
	seedTestPrices(t, h, "2012-01-01", "2026-10-15", func(idx int, _ civil.Date) float64 {
		return 100.0 + float64(idx)*15.0
	})

	callRange := func(rng string) BtcValuationData {
		req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation?range="+rng, nil)
		rec := httptest.NewRecorder()
		h.BitcoinValuation(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("range=%s status=%d body=%s", rng, rec.Code, rec.Body.String())
		}
		var out BtcValuationData
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	d1y := callRange("1y")
	d4y := callRange("4y")
	dAll := callRange("all")

	if len(d1y.Series) != 365 {
		t.Errorf("1y len = %d, want 365", len(d1y.Series))
	}
	if len(d4y.Series) != 1460 {
		t.Errorf("4y len = %d, want 1460", len(d4y.Series))
	}
	if len(dAll.Series) != 5402 {
		t.Errorf("all len = %d, want 5402", len(dAll.Series))
	}
	if len(d1y.Cycles) != 4 || len(d4y.Cycles) != 4 || len(dAll.Cycles) != 4 {
		t.Errorf("cycles len = %d / %d / %d, want 4 epochs in every range", len(d1y.Cycles), len(d4y.Cycles), len(dAll.Cycles))
	}

	last1y := d1y.Series[len(d1y.Series)-1]
	last4y := d4y.Series[len(d4y.Series)-1]
	lastAll := dAll.Series[len(dAll.Series)-1]
	if last1y.SMA200 == nil || last1y.WMA200 == nil || last1y.Mayer == nil {
		t.Fatalf("expected warm indicators at tip: %+v", last1y)
	}
	if math.Abs(*last1y.WMA200-*last4y.WMA200) > 1e-9 || math.Abs(*last1y.WMA200-*lastAll.WMA200) > 1e-9 {
		t.Errorf("WMA200 changed across range slices: 1y=%v 4y=%v all=%v", *last1y.WMA200, *last4y.WMA200, *lastAll.WMA200)
	}
}

func TestBitcoinValuationLatest(t *testing.T) {
	fngRaw, err := os.ReadFile("testdata/bitcoin/alternative_fng.json")
	if err != nil {
		t.Fatal(err)
	}
	stubValuationUpstreams(t, []byte(`{"data":[]}`), fngRaw)

	h := newTestBitcoinHandler(t)
	// Seed 1,600 days ending 2026-10-15 with an explicit peak before the end so drawdown < 0.
	seedTestPrices(t, h, "2022-05-30", "2026-10-15", func(idx int, d civil.Date) float64 {
		if d.String() == "2026-10-01" {
			return 100_000.0
		}
		return 20_000.0 + float64(idx)*40.0
	})

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation?range=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinValuation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out BtcValuationData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WarmingUp {
		t.Errorf("WarmingUp = true, want false")
	}
	lat := out.Latest
	if lat.Date != "2026-10-15" {
		t.Errorf("Latest.Date = %q, want 2026-10-15", lat.Date)
	}
	if lat.ATHUSD == nil || *lat.ATHUSD != 100_000.0 || lat.ATHDate != "2026-10-01" {
		t.Errorf("ATH = %v on %q, want 100000 on 2026-10-01", lat.ATHUSD, lat.ATHDate)
	}
	if lat.DrawdownPct == nil || *lat.DrawdownPct >= 0 {
		t.Errorf("DrawdownPct = %v, want < 0", lat.DrawdownPct)
	}
	if lat.SMA200 == nil || lat.WMA200 == nil || lat.WMA200MonthlyPct == nil || lat.Mayer == nil || lat.MayerPctlSince2011 == nil {
		t.Errorf("missing indicator in Latest: %+v", lat)
	}
	if lat.HalvingCycleDay == nil || *lat.HalvingCycleDay != 908 {
		t.Errorf("HalvingCycleDay = %v, want 908 (2024-04-20 .. 2026-10-15)", lat.HalvingCycleDay)
	}
	if lat.FearGreedValue == nil || *lat.FearGreedValue != 71 || lat.FearGreedClass != "Greed" {
		t.Errorf("FearGreed = %v (%q), want 71 (Greed)", lat.FearGreedValue, lat.FearGreedClass)
	}
	if len(out.FearGreed) != 4 {
		t.Errorf("len(FearGreed) = %d, want 4", len(out.FearGreed))
	}
}

func TestBitcoinValuationCM(t *testing.T) {
	fngRaw, err := os.ReadFile("testdata/bitcoin/alternative_fng.json")
	if err != nil {
		t.Fatal(err)
	}

	// Build 370 days of synthetic CM rows (2025-10-11 .. 2026-10-15) so 365-day Puell is warm.
	start, _ := civil.ParseDate("2025-10-11")
	type cmItem struct {
		Asset      string `json:"asset"`
		Time       string `json:"time"`
		CapMVRVCur string `json:"CapMVRVCur"`
		IssTotNtv  string `json:"IssTotNtv"`
	}
	var cmData []cmItem
	for i := 0; i < 370; i++ {
		d := start.AddDays(i)
		mvrv := 1.5 + float64(i)*0.002
		cmData = append(cmData, cmItem{
			Asset:      "btc",
			Time:       d.String() + "T00:00:00.000000000Z",
			CapMVRVCur: fmt.Sprintf("%.4f", mvrv),
			IssTotNtv:  "450",
		})
	}
	cmBytes, _ := json.Marshal(map[string]any{"data": cmData})
	stubValuationUpstreams(t, cmBytes, fngRaw)

	h := newTestBitcoinHandler(t)
	seedTestPrices(t, h, "2025-10-11", "2026-10-15", func(idx int, _ civil.Date) float64 {
		if idx == 369 {
			return 100_000.0 // 2x the prior 50,000 average -> Puell near 2.0
		}
		return 50_000.0
	})

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation?range=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinValuation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out BtcValuationData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Latest.CMMVRV == nil || math.Abs(*out.Latest.CMMVRV-2.238) > 1e-3 {
		t.Errorf("CMMVRV = %v, want 2.238", out.Latest.CMMVRV)
	}
	if out.Latest.CMMVRVPctlSince2011 == nil || math.Abs(*out.Latest.CMMVRVPctlSince2011-100.0) > 1e-6 {
		t.Errorf("CMMVRVPctlSince2011 = %v, want 100", out.Latest.CMMVRVPctlSince2011)
	}
	if out.Latest.PuellCM == nil || *out.Latest.PuellCM < 1.95 || *out.Latest.PuellCM > 2.01 {
		t.Errorf("PuellCM = %v, want ~1.994", out.Latest.PuellCM)
	}
}

func TestBitcoinValuationRealizedPriceGate(t *testing.T) {
	fngRaw, err := os.ReadFile("testdata/bitcoin/alternative_fng.json")
	if err != nil {
		t.Fatal(err)
	}
	cmBytes := []byte(`{"data":[{"asset":"btc","time":"2026-10-15T00:00:00.000000000Z","CapMVRVCur":"2.00"}]}`)
	stubValuationUpstreams(t, cmBytes, fngRaw)

	h := newTestBitcoinHandler(t)
	store := openTestRiskStore(t)
	h.btc.store = store
	h.btc.read = newBtcReadStore(store, h.btc.settings, nil)
	h.btc.prices = newBtcPriceSyncer(store, h.btc.settings, func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	})

	rp := 41_000.0
	sth := 68_000.0
	lth := 29_000.0
	mvrv := 2.10
	if err := store.applyHolderMaintenanceTx(context.Background(), nil, "", 1, false, []BtcHolderDay{
		{
			Date:               "2026-10-15",
			BaselineGeneration: 1,
			SupplySats:         19_700_000 * 100_000_000,
			SupplyBTC:          19_700_000,
			RealizedPriceUSD:   &rp,
			STHRealizedPrice:   &sth,
			LTHRealizedPrice:   &lth,
			MVRV:               &mvrv,
		},
	}, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// 1. Incomplete price coverage (missing 2010..2025) must block realized prices and BigLens MVRV.
	seedTestPrices(t, h, "2026-01-01", "2026-10-15", func(_ int, _ civil.Date) float64 {
		return 82_000.0
	})
	req1 := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation?range=1y", nil)
	rec1 := httptest.NewRecorder()
	h.BitcoinValuation(rec1, req1)
	var gated BtcValuationData
	if err := json.Unmarshal(rec1.Body.Bytes(), &gated); err != nil {
		t.Fatal(err)
	}
	if gated.PriceCoverage.Complete {
		t.Fatalf("expected incomplete price coverage")
	}
	if gated.Latest.RealizedPriceBlocked != "price_history_incomplete" {
		t.Errorf("RealizedPriceBlocked = %q, want price_history_incomplete", gated.Latest.RealizedPriceBlocked)
	}
	if gated.Latest.RealizedPriceUSD != nil || gated.Latest.BigLensMVRV != nil {
		t.Errorf("expected nil realized price / BigLensMVRV when coverage incomplete, got %+v", gated.Latest)
	}

	// 2. Complete price coverage (2010-08-18 .. 2026-10-15) unlocks realized prices and MVRV delta vs CM.
	seedTestPrices(t, h, "2010-08-18", "2026-10-15", func(_ int, _ civil.Date) float64 {
		return 82_000.0
	})
	req2 := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation?range=1y", nil)
	rec2 := httptest.NewRecorder()
	h.BitcoinValuation(rec2, req2)
	var unlocked BtcValuationData
	if err := json.Unmarshal(rec2.Body.Bytes(), &unlocked); err != nil {
		t.Fatal(err)
	}
	if !unlocked.PriceCoverage.Complete {
		t.Fatalf("expected complete price coverage, missing=%d", unlocked.PriceCoverage.MissingDays)
	}
	if unlocked.Latest.RealizedPriceBlocked != "" {
		t.Errorf("RealizedPriceBlocked = %q, want empty", unlocked.Latest.RealizedPriceBlocked)
	}
	if unlocked.Latest.RealizedPriceUSD == nil || *unlocked.Latest.RealizedPriceUSD != 41_000.0 {
		t.Errorf("RealizedPriceUSD = %v, want 41000", unlocked.Latest.RealizedPriceUSD)
	}
	if unlocked.Latest.BigLensMVRV == nil || *unlocked.Latest.BigLensMVRV != 2.10 {
		t.Errorf("BigLensMVRV = %v, want 2.10", unlocked.Latest.BigLensMVRV)
	}
	if unlocked.Latest.MVRVDeltaPct == nil || math.Abs(*unlocked.Latest.MVRVDeltaPct-5.0) > 1e-6 {
		t.Errorf("MVRVDeltaPct = %v, want +5.0%%", unlocked.Latest.MVRVDeltaPct)
	}
}

func TestBitcoinValuationWarmingUp(t *testing.T) {
	stubValuationUpstreams(t, []byte(`{"data":[]}`), []byte(`{"data":[]}`))

	h := newTestBitcoinHandler(t)
	// No prices seeded at all -> WarmingUp = true, status 200.
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/valuation", nil)
	rec := httptest.NewRecorder()
	h.BitcoinValuation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out BtcValuationData
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.WarmingUp {
		t.Errorf("WarmingUp = false, want true when price history is empty")
	}
	if len(out.Series) != 0 {
		t.Errorf("len(Series) = %d, want 0", len(out.Series))
	}
}
