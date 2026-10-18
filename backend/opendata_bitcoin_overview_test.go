package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBtcRegimeIndicators(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }

	// 1. All 7 indicators in bullish/elevated/open/outflow states.
	sig1 := computeBtcRegimeSignals(btcRegimeInputs{
		CloseUSD:             f(90_000),
		SMA200:               f(75_000),
		WMA200:               f(55_000),
		CMMVRVPctlSince2011:  f(85.0),
		Ribbon:               BtcRibbonState{State: "normal", LastCrossUpDays: i(12)},
		FundingOIWeightedAPR: f(18.5),
		MajorDerivSpot:       f(5.2),
		DerivSpotMedian90d:   f(4.5),
		DVOL:                 f(58.0),
		RV30:                 f(48.0), // VRP = +10.0 > +5
		Consolidation:        BtcConsolidationSignal{Open: true, Reason: "Economy fee 2 sat/vB, backlog 8 blocks"},
		ExchangeNetFlow7dUSD: f(-420_000_000),
	})
	if len(sig1) != 7 {
		t.Fatalf("len(signals) = %d, want 7", len(sig1))
	}
	want1 := map[string]string{
		"trend":      "above_both",
		"valuation":  "high",
		"miners":     "recovery",
		"leverage":   "elevated",
		"volatility": "rich",
		"fee_market": "window_open",
		"flows":      "net_outflow",
	}
	for _, s := range sig1 {
		if want, ok := want1[s.ID]; !ok {
			t.Errorf("unexpected signal ID %q", s.ID)
		} else if s.State != want {
			t.Errorf("signal %s state = %q, want %q (summary: %s)", s.ID, s.State, want, s.Summary)
		}
	}

	// 2. Alternate states: between / mid / capitulation / overheated / cheap / closed / net_inflow.
	sig2 := computeBtcRegimeSignals(btcRegimeInputs{
		CloseUSD:             f(68_000),
		SMA200:               f(75_000),
		WMA200:               f(55_000),
		CMMVRVPctlSince2011:  f(52.0),
		Ribbon:               BtcRibbonState{State: "capitulation"},
		FundingOIWeightedAPR: f(34.0),
		MajorDerivSpot:       f(7.5),
		DerivSpotMedian90d:   f(4.5),
		DVOL:                 f(40.0),
		RV30:                 f(50.0), // VRP = -10.0 < -5
		Consolidation:        BtcConsolidationSignal{Open: false, Reason: "Economy fee 25 sat/vB > 10"},
		ExchangeNetFlow7dUSD: f(150_000_000),
	})
	want2 := map[string]string{
		"trend":      "between",
		"valuation":  "mid",
		"miners":     "capitulation",
		"leverage":   "overheated",
		"volatility": "cheap",
		"fee_market": "closed",
		"flows":      "net_inflow",
	}
	for _, s := range sig2 {
		if s.State != want2[s.ID] {
			t.Errorf("signal %s state = %q, want %q", s.ID, s.State, want2[s.ID])
		}
	}

	// 3. Remaining states: below_both / low / normal (>30d cross) / negative & neutral / fair.
	sig3 := computeBtcRegimeSignals(btcRegimeInputs{
		CloseUSD:             f(45_000),
		SMA200:               f(75_000),
		WMA200:               f(55_000),
		CMMVRVPctlSince2011:  f(14.0),
		Ribbon:               BtcRibbonState{State: "normal", LastCrossUpDays: i(45)},
		FundingOIWeightedAPR: f(-3.2),
		MajorDerivSpot:       f(4.0),
		DerivSpotMedian90d:   f(4.0),
		DVOL:                 f(48.0),
		RV30:                 f(46.0), // VRP = +2.0 -> fair
	})
	want3 := map[string]string{
		"trend":      "below_both",
		"valuation":  "low",
		"miners":     "normal",
		"leverage":   "negative",
		"volatility": "fair",
	}
	for _, s := range sig3 {
		if want, ok := want3[s.ID]; ok && s.State != want {
			t.Errorf("signal %s state = %q, want %q", s.ID, s.State, want)
		}
	}

	// Neutral leverage when funding is positive but < 15% and ratio < 1.25x median.
	sig4 := computeBtcRegimeSignals(btcRegimeInputs{
		FundingOIWeightedAPR: f(8.5),
		MajorDerivSpot:       f(4.2),
		DerivSpotMedian90d:   f(4.0),
	})
	for _, s := range sig4 {
		if s.ID == "leverage" && s.State != "neutral" {
			t.Errorf("leverage state = %q, want neutral", s.State)
		}
	}
}

func TestBtcVerificationRows(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i64 := func(v int64) *int64 { return &v }

	cmRow := &BtcCMMetricRow{
		Date:       "2026-10-15",
		TxCnt:      i64(520_000),
		AdrActCnt:  i64(780_000),
		IssTotNtv:  f(450.0),
		FeeTotNtv:  f(3.03128842),
		SplyCur:    f(20_099_037.5),
		CapMVRVCur: f(2.10),
	}
	ocDay := &BtcOnchainDay{
		Date:                  "2026-10-15",
		Blocks:                144,
		Txs:                   521_040,
		ActiveAddresses:       785_000,
		ScheduledIssuanceSats: 45_000_000_000,
		ClaimedIssuanceSats:   45_000_000_000,
		FeesSats:              303_128_842,
		CoinbaseSats:          45_303_128_842,
	}

	// Case A: M2 rows present, but no baseline exists yet -> supply & mvrv read "needs_baseline" / "needs baseline".
	rowsNoBase := computeBtcVerificationRows(btcVerificationInputs{
		Date:               "2026-10-15",
		MempoolHashrateEHs: f(945.0),
		ImpliedHashrateEHs: f(950.02),
		CMRow:              cmRow,
		MempoolRewardsBTC:  f(453.03128842),
		OnchainDay:         ocDay,
		HolderDay:          nil,
		BaselineAvailable:  false,
	})
	byID := make(map[string]BtcVerificationRow, len(rowsNoBase))
	for _, r := range rowsNoBase {
		byID[r.ID] = r
	}
	for _, id := range []string{"hashrate", "tx_count", "active_addresses", "issuance", "fees", "miner_revenue"} {
		r, ok := byID[id]
		if !ok {
			t.Fatalf("missing verification row %q", id)
		}
		if r.Status != "ok" {
			t.Errorf("row %s status = %q, want ok", id, r.Status)
		}
		if r.APIValue == nil || r.ChainValue == nil || r.DeltaPct == nil {
			t.Errorf("row %s expected non-nil APIValue, ChainValue, DeltaPct: %+v", id, r)
		}
	}
	if math.Abs(*byID["issuance"].DeltaPct) > 1e-9 {
		t.Errorf("issuance delta = %v, want 0", *byID["issuance"].DeltaPct)
	}
	if math.Abs(*byID["miner_revenue"].DeltaPct) > 1e-9 {
		t.Errorf("miner_revenue delta = %v, want 0", *byID["miner_revenue"].DeltaPct)
	}
	for _, id := range []string{"supply", "mvrv"} {
		r := byID[id]
		if r.Status != "needs_baseline" || r.Note != "needs baseline" {
			t.Errorf("row %s without baseline = status %q note %q, want needs_baseline / \"needs baseline\"", id, r.Status, r.Note)
		}
		if r.ChainValue != nil {
			t.Errorf("row %s without baseline must have nil ChainValue, got %v", id, *r.ChainValue)
		}
	}

	// Case B: Once a baseline and holder day exist, supply and mvrv flip to "ok" with chain values and delta.
	rowsWithBase := computeBtcVerificationRows(btcVerificationInputs{
		Date:               "2026-10-15",
		MempoolHashrateEHs: f(945.0),
		ImpliedHashrateEHs: f(950.02),
		CMRow:              cmRow,
		MempoolRewardsBTC:  f(453.03128842),
		OnchainDay:         ocDay,
		HolderDay: &BtcHolderDay{
			Date:       "2026-10-15",
			SupplySats: 2_009_903_750_000_000,
			MVRV:       f(2.142),
		},
		BaselineAvailable: true,
	})
	byID2 := make(map[string]BtcVerificationRow, len(rowsWithBase))
	for _, r := range rowsWithBase {
		byID2[r.ID] = r
	}
	for _, id := range []string{"supply", "mvrv"} {
		r := byID2[id]
		if r.Status != "ok" {
			t.Errorf("row %s with baseline status = %q, want ok", id, r.Status)
		}
		if r.ChainValue == nil || r.DeltaPct == nil {
			t.Errorf("row %s with baseline missing ChainValue or DeltaPct: %+v", id, r)
		}
	}
	if math.Abs(*byID2["mvrv"].DeltaPct-2.0) > 1e-6 {
		t.Errorf("mvrv delta = %v%%, want +2.0%%", *byID2["mvrv"].DeltaPct)
	}
}

func TestBitcoinOverviewPartial(t *testing.T) {
	poolsRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "mempool_pools_1w.json"))
	if err != nil {
		t.Fatalf("read mempool_pools_1w.json: %v", err)
	}
	bitnodesRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "bitnodes_latest.json"))
	if err != nil {
		t.Fatalf("read bitnodes_latest.json: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/mining/pools/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(poolsRaw)
		case strings.HasPrefix(r.URL.Path, "/api/v1/snapshots/latest"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bitnodesRaw)
		case strings.HasPrefix(r.URL.Path, "/api/v3/global"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"total_market_cap":{"usd":2800000000000},"total_volume":{"usd":95000000000},"market_cap_percentage":{"btc":57.84}}}`))
		case strings.HasPrefix(r.URL.Path, "/api/v3/coins/bitcoin/tickers"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tickers":[{"base":"BTC","target":"USDT","market":{"identifier":"binance"},"converted_volume":{"usd":1620000000},"bid_ask_spread_percentage":0.01,"cost_to_move_up_usd":25000000,"cost_to_move_down_usd":24000000,"is_anomaly":false,"is_stale":false}]}`))
		case strings.HasPrefix(r.URL.Path, "/fapi/v1/ticker/24hr"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","quoteVolume":"8100000000"}`))
		case strings.HasPrefix(r.URL.Path, "/fapi/v1/openInterest"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","openInterest":"85000"}`))
		case strings.HasPrefix(r.URL.Path, "/fapi/v1/premiumIndex"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"82064.5","indexPrice":"82050.0","lastFundingRate":"0.00010000"}`))
		case strings.HasPrefix(r.URL.Path, "/dapi/v1/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSD_PERP","volume":"0","openInterest":"0"}]`))
		default:
			// Simulate partial upstream failures on all other endpoints.
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	stubAllBtcUpstreams(t, srv.URL)
	btcBitnodesLatestURL = srv.URL + "/api/v1/snapshots/latest/"
	btcCGGlobalURL = srv.URL + "/api/v3/global"
	btcCGTickersBaseURL = srv.URL + "/api/v3/coins/bitcoin/tickers"

	h := newTestBitcoinHandler(t)
	// Seed 210 days of price history so SMA200 & Mayer compute cleanly.
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	h.btc.now = func() time.Time { return now }
	var priceSeed []btcPriceDay
	for i := 210; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		priceSeed = append(priceSeed, btcPriceDay{
			Day:    d,
			Close:  70_000 + float64(210-i)*50,
			Source: "coinbase",
		})
	}
	h.btc.prices.putRows(context.Background(), priceSeed)

	// Seed one Bitnodes snapshot into the store so node concentration is populated without network races.
	_ = saveBtcNodesSnapshot(context.Background(), h.btc.store, BtcNodes{
		AsOf:      now.Format(time.RFC3339),
		Reachable: 4,
		Clearnet:  3,
	})
	h.btc.nodes.mu.Lock()
	h.btc.nodes.lastSync = time.Time{}
	h.btc.nodes.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/overview", nil)
	rec := httptest.NewRecorder()
	h.BitcoinOverview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /overview status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got BtcOverviewData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode BtcOverviewData: %v", err)
	}
	if got.Cards.DominancePct == nil || math.Abs(*got.Cards.DominancePct-57.84) > 1e-6 {
		t.Errorf("DominancePct = %v, want 57.84", got.Cards.DominancePct)
	}
	if got.Cards.MajorDerivSpot == nil || math.Abs(*got.Cards.MajorDerivSpot-5.0) > 1e-6 {
		t.Errorf("MajorDerivSpot = %v, want 5.0 ($8.1B / $1.62B)", got.Cards.MajorDerivSpot)
	}
	if got.Cards.Concentration.PoolNakamoto.Value <= 0 || got.Cards.Concentration.PoolNakamoto.Denominator != 1130 {
		t.Errorf("PoolNakamoto = %+v, want value > 0 and denominator 1130", got.Cards.Concentration.PoolNakamoto)
	}
	if got.Cards.Concentration.NodeClearnet != 103 || got.Cards.Concentration.NodeReachable != 300 {
		t.Errorf("Node concentration clearnet/reachable = %d/%d, want 103/300", got.Cards.Concentration.NodeClearnet, got.Cards.Concentration.NodeReachable)
	}
	if len(got.Signals) != 7 {
		t.Errorf("len(Signals) = %d, want 7", len(got.Signals))
	}
	if len(got.Verification) != 8 {
		t.Errorf("len(Verification) = %d, want 8", len(got.Verification))
	}
	if got.Supply.ScheduledSupplyBTC < 19_000_000 {
		t.Errorf("ScheduledSupplyBTC = %f, want > 19M", got.Supply.ScheduledSupplyBTC)
	}
}
