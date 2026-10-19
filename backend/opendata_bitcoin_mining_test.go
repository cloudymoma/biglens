package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newBtcMiningTestServer(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	poolsBody, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "mempool_pools_1w.json"))
	if err != nil {
		t.Fatalf("read mempool_pools_1w.json: %v", err)
	}
	rew144Body, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "mempool_reward_stats_144.json"))
	if err != nil {
		t.Fatalf("read mempool_reward_stats_144.json: %v", err)
	}
	diffBody, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "mempool_difficulty_adjustment.json"))
	if err != nil {
		t.Fatalf("read mempool_difficulty_adjustment.json: %v", err)
	}

	baseDay := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	hashrates := make([]btcMempoolHashratePoint, 0, 65)
	rewards := make([]btcMempoolBlockRewardPt, 0, 65)
	for i := 0; i < 65; i++ {
		d := baseDay.AddDate(0, 0, i)
		eh := 950.0 + float64(i)*1.2 // rising from 950 to ~1026.8 EH/s -> normal ribbon
		hashrates = append(hashrates, btcMempoolHashratePoint{
			Timestamp:   d.Unix(),
			AvgHashrate: eh * 1e18,
		})
		rewards = append(rewards, btcMempoolBlockRewardPt{
			AvgHeight:  960000 + int64(i*144),
			Timestamp:  d.Unix(),
			AvgRewards: 314604167, // ~3.14604167 BTC/block
		})
	}
	hrResp := btcMempoolHashrateResp{
		Hashrates: hashrates,
		Difficulty: []btcMempoolDifficultyPoint{
			{
				Time:       baseDay.Unix(),
				Height:     960000,
				Difficulty: 125_000_000_000_000.0,
				Adjustment: 1.032,
			},
			{
				Time:       time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Unix(),
				Height:     970272,
				Difficulty: 132_716_002_350_731.3,
				Adjustment: 1.041,
			},
		},
		CurrentHashrate:   1.02e21,
		CurrentDifficulty: 132_716_002_350_731.3,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cb/stats":
			_, _ = w.Write([]byte(`{"open":"81170.00","last":"82064.00"}`))
		case "/api/v1/difficulty-adjustment":
			_, _ = w.Write(diffBody)
		case "/api/v1/mining/pools/1w":
			_, _ = w.Write(poolsBody)
		case "/api/v1/mining/reward-stats/144":
			_, _ = w.Write(rew144Body)
		case "/api/v1/mining/hashrate/3m", "/api/v1/mining/hashrate/1y", "/api/v1/mining/hashrate/3y":
			_ = json.NewEncoder(w).Encode(hrResp)
		case "/api/v1/mining/blocks/rewards/3m", "/api/v1/mining/blocks/rewards/1y", "/api/v1/mining/blocks/rewards/3y":
			_ = json.NewEncoder(w).Encode(rewards)
		default:
			http.NotFound(w, r)
		}
	}))

	origMempool := mempoolBaseURL
	origCB := btcCoinbaseStatsURL
	origBitnodes := btcBitnodesLatestURL
	origDBIP := btcDBIPBaseURL
	mempoolBaseURL = srv.URL
	btcCoinbaseStatsURL = srv.URL + "/cb/stats"
	btcBitnodesLatestURL = srv.URL + "/bitnodes"
	btcDBIPBaseURL = srv.URL + "/dbip"

	cleanup := func() {
		mempoolBaseURL = origMempool
		btcCoinbaseStatsURL = origCB
		btcBitnodesLatestURL = origBitnodes
		btcDBIPBaseURL = origDBIP
		srv.Close()
	}
	return srv, cleanup
}

func TestBitcoinMiningHashprice(t *testing.T) {
	_, cleanup := newBtcMiningTestServer(t)
	defer cleanup()

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/mining?period=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMining(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got BtcMiningData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HashpriceNow == nil || got.HashpriceNow.USDPerPHDay == nil {
		t.Fatalf("HashpriceNow = %+v, want non-nil USD and BTC hashprice", got.HashpriceNow)
	}
	// On 2026-10-16 (§10.2): ~0.000477 BTC/PH/day and ~$39.1/PH/day.
	if math.Abs(got.HashpriceNow.BTCPerPHDay-0.0004769) > 0.000005 {
		t.Errorf("HashpriceNow.BTCPerPHDay = %.7f, want ~0.0004769", got.HashpriceNow.BTCPerPHDay)
	}
	if math.Abs(*got.HashpriceNow.USDPerPHDay-39.13) > 0.25 {
		t.Errorf("HashpriceNow.USDPerPHDay = %.2f, want ~39.13", *got.HashpriceNow.USDPerPHDay)
	}
	if len(got.Hashprice) != 65 {
		t.Fatalf("len(Hashprice) = %d, want 65", len(got.Hashprice))
	}
}

func TestBitcoinMiningRibbon(t *testing.T) {
	_, cleanup := newBtcMiningTestServer(t)
	defer cleanup()

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/mining?period=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMining(rec, req)
	var got BtcMiningData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ribbon.State != "normal" {
		t.Errorf("Ribbon.State = %q, want normal", got.Ribbon.State)
	}
	lastPt := got.Hashrate[len(got.Hashrate)-1]
	if lastPt.SMA30EHs == nil || lastPt.SMA60EHs == nil {
		t.Fatalf("last hashrate point missing SMA30/SMA60: %+v", lastPt)
	}
	if *lastPt.SMA30EHs <= *lastPt.SMA60EHs {
		t.Errorf("SMA30 (%.2f) <= SMA60 (%.2f) on rising series", *lastPt.SMA30EHs, *lastPt.SMA60EHs)
	}
}

func TestBitcoinMiningPools(t *testing.T) {
	_, cleanup := newBtcMiningTestServer(t)
	defer cleanup()

	h := newTestBitcoinHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/mining?period=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMining(rec, req)

	var got BtcMiningData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Pools) < 3 {
		t.Fatalf("len(Pools) = %d, want >= 3", len(got.Pools))
	}
	// Foundry 288/1130 = 25.49%, AntPool 243/1130 = 21.50%, F2Pool 184/1130 = 16.28%
	if got.Pools[0].Name != "Foundry USA" || math.Abs(got.Pools[0].SharePct-25.49) > 0.1 {
		t.Errorf("pool[0] = %s %.2f%%, want Foundry USA ~25.5%%", got.Pools[0].Name, got.Pools[0].SharePct)
	}
	if got.Pools[1].Name != "AntPool" || math.Abs(got.Pools[1].SharePct-21.50) > 0.1 {
		t.Errorf("pool[1] = %s %.2f%%, want AntPool ~21.5%%", got.Pools[1].Name, got.Pools[1].SharePct)
	}
	if got.Pools[2].Name != "F2Pool" || math.Abs(got.Pools[2].SharePct-16.28) > 0.1 {
		t.Errorf("pool[2] = %s %.2f%%, want F2Pool ~16.3%%", got.Pools[2].Name, got.Pools[2].SharePct)
	}
	if got.Concentration.Nakamoto.Value != 3 {
		t.Errorf("Concentration.Nakamoto.Value = %v, want 3", got.Concentration.Nakamoto.Value)
	}
	if got.Concentration.Nakamoto.Denominator != 1101 || got.Concentration.Nakamoto.Method == "" || got.Concentration.Nakamoto.Unit == "" {
		t.Errorf("Concentration.Nakamoto metadata incomplete: %+v", got.Concentration.Nakamoto)
	}
	if math.Abs(got.Concentration.HHI.Value-1584.0) > 2.0 {
		t.Errorf("Concentration.HHI.Value = %.2f, want ~1584", got.Concentration.HHI.Value)
	}
}

func TestBitcoinMiningFeeShare(t *testing.T) {
	_, cleanup := newBtcMiningTestServer(t)
	defer cleanup()

	h := newTestBitcoinHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/mining?period=1y", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMining(rec, req)

	var got BtcMiningData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FeeSharePct == nil || got.Rewards144 == nil {
		t.Fatalf("FeeSharePct / Rewards144 nil: %+v", got)
	}
	// 303,000,000 / 45,303,000,000 = 0.66883% ≈ 0.669%
	if math.Abs(*got.FeeSharePct-0.669) > 0.002 {
		t.Errorf("FeeSharePct = %.4f%%, want ~0.669%%", *got.FeeSharePct)
	}
}

func TestBitcoinMiningChainDelta(t *testing.T) {
	_, cleanup := newBtcMiningTestServer(t)
	defer cleanup()

	h := newTestBitcoinHandler(t)
	store := openTestRiskStore(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	h.btc.read = newBtcReadStore(store, h.btc.settings, h.btc.now)
	if err := store.ensureBtcBQSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	db := store.db

	// Seed an A2/A1 onchain day on 2026-10-15 (index 63 in our 65-day fixture: mempool EH = 950 + 63*1.2 = 1025.6 EH/s).
	diff := 132_716_002_350_731.3
	blocks := int64(150)
	implied := impliedHashrateEHs(diff, blocks)
	oc := BtcOnchainDay{
		Date:                  "2026-10-15",
		Blocks:                blocks,
		EmptyBlocks:           1,
		AvgDifficulty:         diff,
		ImpliedHashrateEHs:    implied,
		ScheduledIssuanceSats: blocks * 312_500_000,
		ClaimedIssuanceSats:   blocks * 312_500_000,
		CoinbaseSats:          blocks*312_500_000 + 320_000_000,
		FeesSats:              320_000_000,
		FeeSharePct:           float64(320_000_000) / float64(blocks*312_500_000+320_000_000) * 100.0,
	}
	raw, _ := json.Marshal(oc)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO btc_onchain_daily (day, version, payload, synced_at) VALUES (?, 1, ?, '2026-10-16T06:30:00Z')`,
		oc.Date, string(raw)); err != nil {
		t.Fatalf("seed onchain day: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/mining?period=3m", nil)
	rec := httptest.NewRecorder()
	h.BitcoinMining(rec, req)

	var got BtcMiningData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Chain) != 1 {
		t.Fatalf("len(Chain) = %d, want 1", len(got.Chain))
	}
	ch := got.Chain[0]
	if ch.MempoolHashrateEHs == nil || ch.HashrateDeltaPct == nil {
		t.Fatalf("Chain[0] missing mempool comparison: %+v", ch)
	}
	wantDelta := (implied - 1025.6) / 1025.6 * 100.0
	if math.Abs(*ch.HashrateDeltaPct-wantDelta) > 0.01 {
		t.Errorf("HashrateDeltaPct = %.4f%%, want %.4f%%", *ch.HashrateDeltaPct, wantDelta)
	}
}

func TestComputeBtcMiningPoolsNakamotoDenominator(t *testing.T) {
	// Subcase 1: 1000 total blocks where A=300, B=199, C=250, D=231, Unknown=20 -> knownBlocks = 980.
	// A+B = 499 > 490 (50% of 980 known blocks), so Nakamoto Value = 2 and Denominator = 980
	// (whereas against totalBlocks=1000, 499 <= 500 would give Value=3 and Denominator=1000).
	_, conc980 := computeBtcMiningPools(&btcMempoolPoolsSummary{
		BlockCount: 1000,
		Pools: []btcMempoolPool{
			{Name: "A", BlockCount: 300},
			{Name: "B", BlockCount: 199},
			{Name: "C", BlockCount: 250},
			{Name: "D", BlockCount: 231},
			{Name: "Unknown", BlockCount: 20},
		},
	}, "1w")
	if conc980.Nakamoto.Value != 2 || conc980.Nakamoto.Denominator != 980 {
		t.Fatalf("conc980.Nakamoto = {Value:%v Denominator:%d}, want Value=2 Denominator=980", conc980.Nakamoto.Value, conc980.Nakamoto.Denominator)
	}

	// Subcase 2: knownBlocks = 499 (A=240, B=199, C=60, Unknown=20, BlockCount=1000).
	// A=240 <= 249.5, A+B=439 > 249.5 -> Value = 2, Denominator = 499.
	_, conc499 := computeBtcMiningPools(&btcMempoolPoolsSummary{
		BlockCount: 1000,
		Pools: []btcMempoolPool{
			{Name: "A", BlockCount: 240},
			{Name: "B", BlockCount: 199},
			{Name: "C", BlockCount: 60},
			{Name: "Unknown", BlockCount: 20},
		},
	}, "1w")
	if conc499.Nakamoto.Value != 2 || conc499.Nakamoto.Denominator != 499 {
		t.Fatalf("conc499.Nakamoto = {Value:%v Denominator:%d}, want Value=2 Denominator=499", conc499.Nakamoto.Value, conc499.Nakamoto.Denominator)
	}
}
