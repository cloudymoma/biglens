package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func TestParseCryptoDays(t *testing.T) {
	allowed := []int{7, 30, 90, 365}
	tests := []struct {
		name    string
		query   string
		def     int
		want    int
		wantErr bool
	}{
		{"default when absent", "", 90, 90, false},
		{"allowed value", "days=30", 90, 30, false},
		{"max value", "days=365", 90, 365, false},
		{"not on whitelist", "days=45", 90, 0, true},
		{"not an integer", "days=abc", 90, 0, true},
		{"negative", "days=-7", 90, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/opendata/crypto/pulse?"+tt.query, nil)
			got, err := parseCryptoDays(r, tt.def, allowed)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("days = %d, want %d", got, tt.want)
			}
		})
	}
}

// The window is half-open [start, end): end is the latest settled UTC day
// (accounting for cryptoIngestLag) so results cover complete days only and
// span exactly `days` days.
func TestCryptoWindow(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 10, 0, 0, time.UTC) // 00:10 UTC: within 20m ingest lag
	start, end := cryptoWindowAt(90, now)
	wantEnd := civil.Date{Year: 2026, Month: 10, Day: 4}
	if end != wantEnd {
		t.Errorf("end at 00:10 UTC = %s, want previous day %s", end, wantEnd)
	}
	if got := end.DaysSince(start); got != 90 {
		t.Errorf("window spans %d days, want 90", got)
	}

	later := time.Date(2026, 10, 5, 0, 25, 0, 0, time.UTC) // 00:25 UTC: past ingest lag
	_, end2 := cryptoWindowAt(90, later)
	wantEnd2 := civil.Date{Year: 2026, Month: 10, Day: 5}
	if end2 != wantEnd2 {
		t.Errorf("end at 00:25 UTC = %s, want %s", end2, wantEnd2)
	}
	ttl := cryptoTTL(end2, later)
	if ttl < 23*time.Hour || ttl > 24*time.Hour {
		t.Errorf("ttl at 00:25 UTC = %v, want ~23h55m", ttl)
	}
}

func TestRollupCryptoKpi(t *testing.T) {
	daily := []CryptoActivityRow{
		{Date: "2026-07-19", TxCount: 100, ValueSettled: 1.5, FeesTotal: 0.1},
		{Date: "2026-07-20", TxCount: 200, ValueSettled: 2.5, FeesTotal: 0.2},
	}
	blocks := []CryptoBlockRow{
		{Date: "2026-07-19", Blocks: 140, FullnessPct: 90},
		{Date: "2026-07-20", Blocks: 150, FullnessPct: 95.5},
	}
	kpi := rollupCryptoKpi(daily, blocks)
	if kpi.Date != "2026-07-20" || kpi.TxCount != 200 || kpi.Blocks != 150 || kpi.FullnessPct != 95.5 {
		t.Errorf("kpi = %+v, want latest day joined with its block stats", kpi)
	}
}

func TestRollupCryptoKpiEmpty(t *testing.T) {
	kpi := rollupCryptoKpi(nil, nil)
	if kpi.Date != "" || kpi.TxCount != 0 {
		t.Errorf("empty input must yield zero KPI, got %+v", kpi)
	}
}

// Subsidy is coinbase revenue minus fees, clamped at zero (a day where fee
// rows and coinbase rows disagree must never render a negative subsidy).
func TestMergeBtcFees(t *testing.T) {
	fees := []BtcFeeRow{
		{Date: "2026-07-19", MedianFeeVB: 12, TotalFeesBTC: 20},
		{Date: "2026-07-20", MedianFeeVB: 15, TotalFeesBTC: 30},
	}
	coinbase := []BtcCoinbaseRow{
		{Date: "2026-07-19", CoinbaseBTC: 470},
		{Date: "2026-07-20", CoinbaseBTC: 10}, // pathological: fees > revenue
	}
	got := mergeBtcFees(fees, coinbase)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].SubsidyBTC != 450 {
		t.Errorf("subsidy = %v, want 450", got[0].SubsidyBTC)
	}
	if got[1].SubsidyBTC != 0 {
		t.Errorf("clamped subsidy = %v, want 0", got[1].SubsidyBTC)
	}
	if fees[0].SubsidyBTC != 0 {
		t.Errorf("input slice was mutated")
	}
}

func TestMergeEthFees(t *testing.T) {
	fees := []EthFeeRow{{Date: "2026-07-20", AvgGasGwei: 8, TotalFeesETH: 900}}
	burn := []EthBurnRow{{Date: "2026-07-20", BurnedETH: 700, TipsETH: 200}}
	got := mergeEthFees(fees, burn)
	if got[0].BurnedETH != 700 || got[0].TipsETH != 200 {
		t.Errorf("merge = %+v, want burned 700 / tips 200", got[0])
	}
}

func TestParseCryptoChain(t *testing.T) {
	tests := []struct {
		query   string
		want    string
		wantErr bool
	}{
		{"", "btc", false}, // default
		{"chain=btc", "btc", false},
		{"chain=eth", "eth", false},
		{"chain=doge", "", true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/api/opendata/crypto/whales?"+tt.query, nil)
		got, err := parseCryptoChain(r)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", tt.query, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("%q: chain = %q, want %q", tt.query, got, tt.want)
		}
	}
}

// Explorer links are built only from rows whose hash matches the chain's
// canonical shape; anything else (NULL, truncated, injected) is dropped.
func TestFilterWhaleTxs(t *testing.T) {
	btcGood := "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b"
	ethGood := "0x" + btcGood
	tests := []struct {
		name  string
		chain string
		in    []WhaleTx
		want  int
	}{
		{"btc keeps valid", "btc", []WhaleTx{{Hash: btcGood}}, 1},
		{"btc drops 0x-prefixed", "btc", []WhaleTx{{Hash: ethGood}}, 0},
		{"btc drops empty and short", "btc", []WhaleTx{{Hash: ""}, {Hash: "abc123"}}, 0},
		{"eth keeps valid", "eth", []WhaleTx{{Hash: ethGood}}, 1},
		{"eth drops bare hex", "eth", []WhaleTx{{Hash: btcGood}}, 0},
		{"eth drops non-hex", "eth", []WhaleTx{{Hash: "0x" + strings.Repeat("g", 64)}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterWhaleTxs(tt.chain, tt.in); len(got) != tt.want {
				t.Errorf("kept %d rows, want %d", len(got), tt.want)
			}
		})
	}
}

// Token transfer counts and native tx counts come from two queries; the
// merged series must cover the union of dates with zeros, not drop days.
func TestMergeTokenDaily(t *testing.T) {
	transfers := []TokenDailyRow{
		{Date: "2026-07-19", Transfers: 500},
		{Date: "2026-07-20", Transfers: 700},
	}
	native := []CryptoActivityRow{
		{Date: "2026-07-20", TxCount: 1200},
		{Date: "2026-07-21", TxCount: 1300},
	}
	got := mergeTokenDaily(transfers, native)
	want := []TokenDailyRow{
		{Date: "2026-07-19", Transfers: 500, NativeTxs: 0},
		{Date: "2026-07-20", Transfers: 700, NativeTxs: 1200},
		{Date: "2026-07-21", Transfers: 0, NativeTxs: 1300},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Difficulty chosen so hashrate = difficulty * 2^32 * blocks / 86400 / 1e18
// is exactly 1 EH/s at 144 blocks/day — a wrong constant or a dropped
// blocks factor can't produce 1.0 by accident.
func TestMergeBtcMining(t *testing.T) {
	oneEhsDifficulty := 600.0 / 4294967296.0 * 1e18
	blocks := []BtcMiningBlockRow{
		{Date: "2026-07-19", Blocks: 144, Difficulty: oneEhsDifficulty},
		{Date: "2026-07-20", Blocks: 72, Difficulty: oneEhsDifficulty}, // half the blocks → half the hashrate
	}
	coinbase := []BtcCoinbaseRow{
		{Date: "2026-07-19", CoinbaseBTC: 460.5},
		// 2026-07-20 missing: revenue defaults to 0, day is kept
	}
	got := mergeBtcMining(blocks, coinbase)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if math.Abs(got[0].HashrateEhs-1.0) > 1e-9 {
		t.Errorf("hashrate = %v EH/s, want 1.0", got[0].HashrateEhs)
	}
	if math.Abs(got[1].HashrateEhs-0.5) > 1e-9 {
		t.Errorf("hashrate = %v EH/s, want 0.5", got[1].HashrateEhs)
	}
	if got[0].RevenueBTC != 460.5 {
		t.Errorf("revenue = %v, want 460.5", got[0].RevenueBTC)
	}
	if got[1].RevenueBTC != 0 {
		t.Errorf("missing coinbase day revenue = %v, want 0", got[1].RevenueBTC)
	}
}

func TestCryptoSpot(t *testing.T) {
	tests := []struct {
		name       string
		upstream   http.HandlerFunc
		wantStatus int
		wantPrice  float64
	}{
		{"success", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":{"amount":"117234.56","currency":"USD"}}`))
		}, http.StatusOK, 117234.56},
		{"upstream error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, http.StatusBadGateway, 0},
		{"malformed amount", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":{"amount":"","currency":"USD"}}`))
		}, http.StatusBadGateway, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(tt.upstream)
			defer upstream.Close()
			orig := btcSpotURL
			btcSpotURL = upstream.URL
			defer func() { btcSpotURL = orig }()

			h := &APIHandler{cache: NewCache(time.Minute)}
			rec := httptest.NewRecorder()
			h.CryptoSpot(rec, httptest.NewRequest("GET", "/api/opendata/crypto/spot", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var got CryptoSpotData
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.PriceUSD != tt.wantPrice {
				t.Errorf("price = %v, want %v", got.PriceUSD, tt.wantPrice)
			}
			if got.Source != "coinbase" {
				t.Errorf("source = %q, want coinbase", got.Source)
			}
		})
	}
}

// The live bar prices TRX and BTC through the same Coinbase endpoint; the
// base symbol must reach the URL and a bad amount must fail, not price at 0.
func TestFetchSpot(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if strings.Contains(r.URL.Path, "BAD") {
			w.Write([]byte(`{"data":{"amount":"0","currency":"USD"}}`))
			return
		}
		w.Write([]byte(`{"data":{"amount":"0.3346","currency":"USD"}}`))
	}))
	defer upstream.Close()
	orig := spotURLFormat
	spotURLFormat = upstream.URL + "/v2/prices/%s-USD/spot"
	defer func() { spotURLFormat = orig }()

	got, err := fetchSpot(context.Background(), "TRX")
	if err != nil {
		t.Fatalf("fetchSpot(TRX): %v", err)
	}
	if gotPath != "/v2/prices/TRX-USD/spot" {
		t.Errorf("requested %q, want /v2/prices/TRX-USD/spot", gotPath)
	}
	if got.PriceUSD != 0.3346 || got.Source != "coinbase" {
		t.Errorf("got %+v, want 0.3346 from coinbase", got)
	}
	if _, err := fetchSpot(context.Background(), "BAD"); err == nil {
		t.Error("a zero price must be an error, not a $0 quote")
	}
}

func TestEnrichTopTokensRiskAndMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	// 1. Seed scam_fake_tokens and scam_fake_token_deployers using the real store methods.
	if err := store.upsertFakeTokens(ctx, "eth", []fakeTokenRow{{
		Chain:      "eth",
		Contract:   "0x1111111111111111111111111111111111111111",
		Symbol:     "USDT",
		Creator:    "0xbad0000000000000000000000000000000000bad",
		Transfers:  900,
		Recipients: 450,
		FirstSeen:  "2026-10-10",
		LastSeen:   "2026-10-13",
	}}); err != nil {
		t.Fatalf("upsertFakeTokens: %v", err)
	}
	if err := store.applyScamBatchWithDeployers(ctx, scamEthSourceID, "eth", nil, []fakeTokenDeployerRow{{
		Address:   "0xbad0000000000000000000000000000000000bad",
		Contracts: 7,
		FirstSeen: "2026-10-01",
		LastSeen:  "2026-10-13",
	}}, "2026-10-13", "2026-10-13", now); err != nil {
		t.Fatalf("applyScamBatchWithDeployers: %v", err)
	}

	// 2. Seed token_contract_metadata.
	meta := []tokenContractMeta{
		{
			Address:        "0x1111111111111111111111111111111111111111",
			Symbol:         "USDT",
			Name:           "Tether USD",
			TokenStandard:  "ERC-20",
			Decimals:       6,
			CreatorAddress: "0xbad0000000000000000000000000000000000bad",
			CreatedAt:      "2026-10-10",
			Found:          true,
		},
		{
			Address:        "0xdac17f958d2ee523a2206206994597c13d831ec7",
			Symbol:         "USDT",
			Name:           "Tether USD",
			TokenStandard:  "ERC-20",
			Decimals:       6,
			CreatorAddress: "0x36928500bc1dcd7af6a2b4008875cc336b927d57",
			CreatedAt:      "2017-11-28",
			Found:          true,
		},
	}
	if err := store.upsertTokenMetadataBatch(ctx, nil, meta, now); err != nil {
		t.Fatalf("upsertTokenMetadataBatch: %v", err)
	}

	top := []TokenRow{
		{TokenAddress: "0x1111111111111111111111111111111111111111", Transfers: 50000, ZeroValueTransfers: 45000},
		{TokenAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7", Transfers: 100000, ZeroValueTransfers: 12000},
	}
	metaMap, err := store.loadTokenMetadata(ctx, []string{
		"0x1111111111111111111111111111111111111111",
		"0xdac17f958d2ee523a2206206994597c13d831ec7",
	}, now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("loadTokenMetadata: %v", err)
	}

	applyTokenMetadata(top, metaMap)
	enrichTopTokensRisk(ctx, store, top)

	if !slices.Contains(top[0].Risk, "counterfeit_token") {
		t.Errorf("fake USDT Risk = %v, want counterfeit_token", top[0].Risk)
	}
	if !slices.Contains(top[0].CreatorRisk, "fake_token_deployers") {
		t.Errorf("fake USDT CreatorRisk = %v, want fake_token_deployers", top[0].CreatorRisk)
	}
	if len(top[1].Risk) != 0 {
		t.Errorf("official USDT Risk = %v, want empty", top[1].Risk)
	}
}

func TestPersistSettledTokenEconomyDaysSkipsOnPartialFailure(t *testing.T) {
	ctx := context.Background()
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	// end is exclusive (< @end_date), so end = 2026-10-15 means the window's
	// last day is end-1 = 2026-10-14, while 2026-10-13 is strictly settled (< end-1).
	end := civil.Date{Year: 2026, Month: 10, Day: 15}
	bundleDaily := []tokenEconomyDailyRow{
		{Day: "2026-10-13", Transfers: 1000, ZeroValueTransfers: 100},
		{Day: "2026-10-14", Transfers: 1200, ZeroValueTransfers: 120},
	}
	bundleMajor := []tokenMajorDailyRow{
		{Day: "2026-10-13", TokenAddress: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Transfers: 500, VolumeToken: 1000},
		{Day: "2026-10-14", TokenAddress: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Transfers: 600, VolumeToken: 1200},
	}

	// 1. Simulate contracts query failing (canSaveDaily = false, canSaveMajor = true, lastDayComplete = false).
	// Neither daily row may be saved; only the settled 2026-10-13 major row (< end-1) may be saved.
	persistSettledTokenEconomyDays(ctx, store, end, false, false, true, bundleDaily, nil, nil, bundleMajor, now)

	gotDaily, err := store.loadTokenEconomyDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenEconomyDaily: %v", err)
	}
	if len(gotDaily) != 0 {
		t.Fatalf("expected 0 daily rows saved when a source query failed, got %+v", gotDaily)
	}
	gotMajor, err := store.loadTokenMajorDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenMajorDaily: %v", err)
	}
	if len(gotMajor) != 1 || gotMajor[0].Day != "2026-10-13" {
		t.Fatalf("expected only settled 2026-10-13 (< end-1) major row saved when lastDayComplete=false, got %+v", gotMajor)
	}

	// 2. All queries succeed, but lastDayComplete = false: 2026-10-13 (< end-1) is saved,
	// while in-progress 2026-10-14 (== end-1) must NOT be saved.
	persistSettledTokenEconomyDays(ctx, store, end, false, true, true, bundleDaily, nil, nil, bundleMajor, now)
	gotDaily, err = store.loadTokenEconomyDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenEconomyDaily: %v", err)
	}
	if len(gotDaily) != 1 || gotDaily[0].Day != "2026-10-13" {
		t.Fatalf("expected only 2026-10-13 (< end-1) daily row saved when lastDayComplete=false, got %+v", gotDaily)
	}

	// 3. Once lastDayComplete = true, 2026-10-14 (== end-1) is also saved.
	persistSettledTokenEconomyDays(ctx, store, end, true, true, true, bundleDaily, nil, nil, bundleMajor, now)
	gotDaily, err = store.loadTokenEconomyDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenEconomyDaily: %v", err)
	}
	if len(gotDaily) != 2 || gotDaily[1].Day != "2026-10-14" {
		t.Fatalf("expected both 2026-10-13 and 2026-10-14 saved when lastDayComplete=true, got %+v", gotDaily)
	}
}
