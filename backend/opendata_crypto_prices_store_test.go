package main

import (
	"context"
	"testing"
)

// The price table lives in the Address Risk SQLite file but is created
// lazily by its own methods; NULL prices survive the round trip, a re-synced
// day overwrites the old row, and as_of is the newest sync time.
func TestOnchainPricesStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openPricesTestStore(t)

	rows, asOf, err := store.loadOnchainPrices(ctx)
	if err != nil || len(rows) != 0 || !asOf.IsZero() {
		t.Fatalf("empty store = %v rows, as_of %v, err %v; want none", rows, asOf, err)
	}

	eth, btc := 2500.5, 83000.25
	if err := store.upsertOnchainPrices(ctx, []onchainPriceDay{
		{Day: "2026-10-10", EthUSD: &eth, EthSwaps: 4000},
		{Day: "2026-10-09"},
	}, pAt("2026-10-11T19:31:00Z")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, asOf, err = store.loadOnchainPrices(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("load = %d rows, %v; want 2", len(rows), err)
	}
	if rows[0].Day != "2026-10-09" || rows[0].EthUSD != nil || rows[0].BtcUSD != nil || rows[0].EthSwaps != 0 {
		t.Errorf("row 0 = %+v, want 2026-10-09 with NULL prices (ordered by day)", rows[0])
	}
	if rows[1].Day != "2026-10-10" || rows[1].EthUSD == nil || *rows[1].EthUSD != eth || rows[1].BtcUSD != nil || rows[1].EthSwaps != 4000 {
		t.Errorf("row 1 = %+v, want 2026-10-10 ETH %v, no BTC, 4000 swaps", rows[1], eth)
	}
	if !asOf.Equal(pAt("2026-10-11T19:31:00Z")) {
		t.Errorf("as_of = %v, want 2026-10-11T19:31:00Z", asOf)
	}

	if err := store.upsertOnchainPrices(ctx, []onchainPriceDay{
		{Day: "2026-10-09", EthUSD: &eth, BtcUSD: &btc, EthSwaps: 1, BtcSwaps: 2},
	}, pAt("2026-10-12T19:31:00Z")); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	rows, asOf, _ = store.loadOnchainPrices(ctx)
	if len(rows) != 2 || rows[0].BtcUSD == nil || *rows[0].BtcUSD != btc || rows[0].BtcSwaps != 2 {
		t.Errorf("after re-sync rows = %+v, want 2026-10-09 overwritten", rows)
	}
	if !asOf.Equal(pAt("2026-10-12T19:31:00Z")) {
		t.Errorf("as_of = %v, want the newest sync time", asOf)
	}
}
