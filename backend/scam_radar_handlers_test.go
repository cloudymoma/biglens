package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func newTestScamRadarHandler(t *testing.T, store *riskStore) *APIHandler {
	t.Helper()
	cache := NewCache(10 * time.Minute)
	svc := newAddressRiskService(store, nil)
	svc.invalidateCache = func() {
		invalidateAddressRiskCaches(cache)
	}
	return &APIHandler{
		cache: cache,
		risk:  svc,
	}
}

func TestScamRadarEmptyDB(t *testing.T) {
	store := newTestRiskStore(t)
	h := newTestScamRadarHandler(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil)
	h.AddressRiskScamRadar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp scamRadarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Empty {
		t.Fatalf("Empty = false, want true on empty DB")
	}
	if resp.BTCRBF != nil {
		t.Fatalf("BTCRBF = %+v, want nil on empty DB", resp.BTCRBF)
	}
	if len(resp.FakeTokens) != 0 {
		t.Fatalf("FakeTokens len = %d, want 0", len(resp.FakeTokens))
	}
	for _, srcID := range []string{scamEthSourceID, scamTronSourceID, scamBTCSourceID, tronStablecoinSourceID} {
		cov, ok := resp.Coverage[srcID]
		if !ok {
			t.Fatalf("missing coverage key %q", srcID)
		}
		if cov.Status != "empty" {
			t.Fatalf("coverage[%s] = %+v, want status=empty", srcID, cov)
		}
	}
}

func TestScamRadarTrendZeroFillsMissingDays(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)

	// Seed two non-consecutive days: 2026-10-04 and 2026-10-06 (2026-10-05 is missing).
	if err := store.applyScamBatch(ctx, scamEthSourceID, "eth", []scamDayBatch{
		{
			Day: "2026-10-04",
			Stats: map[string]float64{
				"eth_poison_hits":    100,
				"eth_poison_victims": 50,
				"eth_fake_transfers": 300,
			},
		},
		{
			Day: "2026-10-06",
			Stats: map[string]float64{
				"eth_poison_hits":    220,
				"eth_poison_victims": 90,
				"eth_fake_transfers": 450,
			},
		},
	}, "2026-10-04", "2026-10-06", time.Now()); err != nil {
		t.Fatalf("applyScamBatch eth: %v", err)
	}

	if err := store.applyScamBatch(ctx, scamTronSourceID, "tron", []scamDayBatch{
		{
			Day: "2026-10-06",
			Stats: map[string]float64{
				"tron_poison_hits":    80,
				"tron_poison_victims": 70,
				"tron_candidates":     12000,
			},
		},
	}, "2026-10-06", "2026-10-06", time.Now()); err != nil {
		t.Fatalf("applyScamBatch tron: %v", err)
	}

	if err := store.applyScamBatch(ctx, scamBTCSourceID, "btc", []scamDayBatch{
		{
			Day: "2026-10-06",
			Stats: map[string]float64{
				"btc_txs":     1000,
				"btc_rbf_txs": 666,
			},
		},
	}, "2026-10-06", "2026-10-06", time.Now()); err != nil {
		t.Fatalf("applyScamBatch btc: %v", err)
	}

	h := newTestScamRadarHandler(t, store)
	rec := httptest.NewRecorder()
	h.AddressRiskScamRadar(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp scamRadarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Empty {
		t.Fatalf("Empty = true, want false")
	}
	if len(resp.Trend) != 30 {
		t.Fatalf("Trend len = %d, want 30 days", len(resp.Trend))
	}
	// Last point is 2026-10-06 (has_data=true)
	last := resp.Trend[29]
	if last.Day != "2026-10-06" || !last.HasData || last.ETHPoisonHits != 220 || last.TronPoisonHits != 80 || last.ETHFakeTransfers != 450 {
		t.Fatalf("Trend[29] = %+v, want 2026-10-06 with hits 220/80/450 and has_data=true", last)
	}
	// 2026-10-05 is missing -> zero-filled with has_data=false
	gap := resp.Trend[28]
	if gap.Day != "2026-10-05" || gap.HasData || gap.ETHPoisonHits != 0 || gap.TronPoisonHits != 0 || gap.ETHFakeTransfers != 0 {
		t.Fatalf("Trend[28] (gap day) = %+v, want 2026-10-05 zero-filled with has_data=false", gap)
	}
	// 2026-10-04 has data
	prev := resp.Trend[27]
	if prev.Day != "2026-10-04" || !prev.HasData || prev.ETHPoisonHits != 100 || prev.ETHFakeTransfers != 300 {
		t.Fatalf("Trend[27] = %+v, want 2026-10-04 with 100/300 and has_data=true", prev)
	}
	// BTC RBF card populated from 2026-10-06
	if resp.BTCRBF == nil || resp.BTCRBF.Txs != 1000 || resp.BTCRBF.RBFTxs != 666 || resp.BTCRBF.Percent != 66.6 {
		t.Fatalf("BTCRBF = %+v, want 666/1000 (66.6%%)", resp.BTCRBF)
	}
}

func TestScamRadarFakeTokensTop10In7DayWindow(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)

	// Old fake token last seen outside 7-day window (2026-09-20 vs latest 2026-10-06)
	var rows []fakeTokenRow
	rows = append(rows, fakeTokenRow{
		Contract:   "0xold0000000000000000000000000000000000000",
		Symbol:     "USDT_OLD",
		Transfers:  999999,
		Recipients: 50000,
		FirstSeen:  "2026-09-15",
		LastSeen:   "2026-09-20",
	})
	// 12 fake tokens inside the 7-day window [2026-09-30 .. 2026-10-06]
	for i := 1; i <= 12; i++ {
		rows = append(rows, fakeTokenRow{
			Contract:   fmt.Sprintf("0x%040x", i),
			Symbol:     fmt.Sprintf("USDT_%d", i),
			Transfers:  i * 100,
			Recipients: i * 10,
			FirstSeen:  "2026-10-01",
			LastSeen:   "2026-10-06",
		})
	}
	if err := store.applyScamBatch(ctx, scamEthSourceID, "eth", []scamDayBatch{
		{
			Day:        "2026-10-06",
			FakeTokens: rows,
			Stats:      map[string]float64{"eth_fake_transfers": 7800},
		},
	}, "2026-10-01", "2026-10-06", time.Now()); err != nil {
		t.Fatalf("applyScamBatch: %v", err)
	}

	h := newTestScamRadarHandler(t, store)
	rec := httptest.NewRecorder()
	h.AddressRiskScamRadar(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil))

	var resp scamRadarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.FakeTokens) != 10 {
		t.Fatalf("FakeTokens len = %d, want 10", len(resp.FakeTokens))
	}
	// Top 1 must be i=12 (transfers=1200), not the old token (transfers=999999, last_seen=2026-09-20)
	if resp.FakeTokens[0].Transfers != 1200 || resp.FakeTokens[0].Symbol != "USDT_12" {
		t.Fatalf("FakeTokens[0] = %+v, want USDT_12 with 1200 transfers", resp.FakeTokens[0])
	}
	if resp.FakeTokens[9].Transfers != 300 || resp.FakeTokens[9].Symbol != "USDT_3" {
		t.Fatalf("FakeTokens[9] = %+v, want USDT_3 with 300 transfers", resp.FakeTokens[9])
	}
	for _, ft := range resp.FakeTokens {
		if ft.Contract == "0xold0000000000000000000000000000000000000" {
			t.Fatalf("expired fake token outside 7d window must not appear in Top 10")
		}
		if !strings.HasPrefix(ft.ExplorerURL, "https://etherscan.io/") {
			t.Fatalf("ExplorerURL = %q, want etherscan link", ft.ExplorerURL)
		}
	}
}

func TestScamRadarCacheInvalidatedOnChange(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	h := newTestScamRadarHandler(t, store)

	// 1st request caches empty=true
	rec1 := httptest.NewRecorder()
	h.AddressRiskScamRadar(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil))
	var resp1 scamRadarResponse
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	if !resp1.Empty {
		t.Fatalf("first response expected Empty=true")
	}

	// Run dailyBQSyncer with onChange wired to h.risk.invalidateCache
	job := &fakeDailyJob{
		id:       scamBTCSourceID,
		ready:    true,
		dryBytes: 1 << 20,
		runResult: dailyResult{
			Days: []scamDayBatch{
				{
					Day:   "2026-10-06",
					Stats: map[string]float64{"btc_txs": 500, "btc_rbf_txs": 250},
				},
			},
		},
	}
	syncer := &dailyBQSyncer{
		store:       store,
		job:         job,
		now:         func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) },
		initialDays: 1,
		onChange:    h.risk.invalidateCache,
	}
	syncer.syncOnce(ctx)

	// 2nd request must see the updated data (cache was invalidated by onChange)
	rec2 := httptest.NewRecorder()
	h.AddressRiskScamRadar(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil))
	var resp2 scamRadarResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2.Empty {
		t.Fatalf("second response still Empty=true; expected cache invalidation via onChange")
	}
	if resp2.BTCRBF == nil || resp2.BTCRBF.Percent != 50.0 {
		t.Fatalf("BTCRBF after sync = %+v, want 50.0%%", resp2.BTCRBF)
	}
	_ = civil.Date{}
}

func TestScamRadarNilStoreReturns503(t *testing.T) {
	h := newTestScamRadarHandler(t, nil)
	rec := httptest.NewRecorder()
	h.AddressRiskScamRadar(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/address-risk/scam-radar", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "address risk database unavailable") {
		t.Fatalf("body = %q, want fixed 'address risk database unavailable'", rec.Body.String())
	}
}
