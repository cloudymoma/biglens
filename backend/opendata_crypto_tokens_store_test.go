package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenEconomyStoreDailyAndMajor(t *testing.T) {
	ctx := context.Background()
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	daily := []tokenEconomyDailyRow{
		{Day: "2026-10-13", Transfers: 1000, ZeroValueTransfers: 250, NativeTxs: 500, Contracts: 40, ERC20: 15, ERC721: 5},
		{Day: "2026-10-14", Transfers: 1200, ZeroValueTransfers: 300, NativeTxs: 600, Contracts: 50, ERC20: 20, ERC721: 8},
	}
	if err := store.upsertTokenEconomyDaily(ctx, daily, now); err != nil {
		t.Fatalf("upsertTokenEconomyDaily: %v", err)
	}

	// Settled days use INSERT OR IGNORE, so a later write with zeros must not overwrite.
	overwriteAttempt := []tokenEconomyDailyRow{
		{Day: "2026-10-13", Transfers: 0, ZeroValueTransfers: 0, NativeTxs: 0, Contracts: 0, ERC20: 0, ERC721: 0},
	}
	if err := store.upsertTokenEconomyDaily(ctx, overwriteAttempt, now); err != nil {
		t.Fatalf("upsertTokenEconomyDaily overwrite: %v", err)
	}

	gotDaily, err := store.loadTokenEconomyDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenEconomyDaily: %v", err)
	}
	if len(gotDaily) != 2 {
		t.Fatalf("len(gotDaily) = %d, want 2", len(gotDaily))
	}
	if gotDaily[0].Transfers != 1000 || gotDaily[0].ZeroValueTransfers != 250 || gotDaily[0].ERC20 != 15 {
		t.Errorf("gotDaily[0] = %+v, want original settled values preserved", gotDaily[0])
	}

	major := []tokenMajorDailyRow{
		{
			Day:          "2026-10-13",
			TokenAddress: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
			Transfers:    400,
			ZeroValue:    40,
			Senders:      200,
			Receivers:    210,
			VolumeToken:  5000000.5,
			MintedToken:  100000.0,
			BurnedToken:  50000.0,
		},
	}
	if err := store.upsertTokenMajorDaily(ctx, major, now); err != nil {
		t.Fatalf("upsertTokenMajorDaily: %v", err)
	}
	gotMajor, err := store.loadTokenMajorDaily(ctx, "2026-10-13", "2026-10-15")
	if err != nil {
		t.Fatalf("loadTokenMajorDaily: %v", err)
	}
	if len(gotMajor) != 1 || gotMajor[0].VolumeToken != 5000000.5 || gotMajor[0].MintedToken != 100000.0 {
		t.Errorf("gotMajor = %+v", gotMajor)
	}
}

func TestTokenMetadataCacheWithNegativeCaching(t *testing.T) {
	ctx := context.Background()
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	requested := []string{
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		"0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", // missing from tokens_latest
	}
	found := []tokenContractMeta{
		{
			Address:        "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
			Symbol:         "USDC",
			Name:           "USD Coin",
			TokenStandard:  "ERC-20",
			Decimals:       6,
			ProxyStandard:  "EIP-897",
			CreatorAddress: "0x95ba4cf87d6723ad9c0db21737d862be80e93911",
			CreatedAt:      "2018-08-03",
			Found:          true,
		},
	}
	if err := store.upsertTokenMetadataBatch(ctx, requested, found, now); err != nil {
		t.Fatalf("upsertTokenMetadataBatch: %v", err)
	}

	// Both found and not-found addresses must be returned as fresh within 7 days.
	fresh, err := store.loadTokenMetadata(ctx, requested, now.Add(24*time.Hour), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("loadTokenMetadata fresh: %v", err)
	}
	if len(fresh) != 2 {
		t.Fatalf("expected both found and negative-cached addresses in fresh map, got %d", len(fresh))
	}
	if !fresh["0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"].Found || fresh["0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"].ProxyStandard != "EIP-897" {
		t.Errorf("unexpected found meta: %+v", fresh["0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"])
	}
	if fresh["0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"].Found {
		t.Errorf("expected negative-cached entry to have Found=false")
	}

	// Expired (> 7 days): must return empty so caller re-queries.
	expired, err := store.loadTokenMetadata(ctx, requested, now.Add(8*24*time.Hour), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("loadTokenMetadata expired: %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("expected expired map to be empty, got %+v", expired)
	}
}

func TestTokenSupplyAndMarketCache(t *testing.T) {
	ctx := context.Background()
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	supplies := []TokenSupplyRow{
		{Asset: "USDT", Network: "tron", NetworkLabel: "TRON", Contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Tier: "native", Label: "USDT", Supply: 85000000000, UpdatedAt: now.Format(time.RFC3339)},
	}
	if err := store.upsertTokenSupplyCache(ctx, supplies, now); err != nil {
		t.Fatalf("upsertTokenSupplyCache: %v", err)
	}
	gotSupplies, asOf, err := store.loadTokenSupplyCache(ctx)
	if err != nil || len(gotSupplies) != 1 || gotSupplies[0].Supply != 85000000000 || asOf.IsZero() {
		t.Fatalf("loadTokenSupplyCache = %+v, asOf=%v, err=%v", gotSupplies, asOf, err)
	}

	markets := []TokenMarketRow{
		{Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Symbol: "USDC", PriceUSD: btcFloatPtr(1.0), HoldersCount: 9495898, Volume24hUSD: 21491450944, MarketCapUSD: 72642407980, UpdatedAt: now.Format(time.RFC3339)},
	}
	if err := store.upsertTokenMarketCache(ctx, markets, now); err != nil {
		t.Fatalf("upsertTokenMarketCache: %v", err)
	}
	gotMarkets, mAsOf, err := store.loadTokenMarketCache(ctx)
	if err != nil || len(gotMarkets) != 1 || gotMarkets[0].HoldersCount != 9495898 || mAsOf.IsZero() {
		t.Fatalf("loadTokenMarketCache = %+v, asOf=%v, err=%v", gotMarkets, mAsOf, err)
	}
}
