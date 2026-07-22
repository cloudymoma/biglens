package main

import (
	"strings"
	"testing"
)

// Every BTC SQL must prune the MONTH partition column AND bound the exact
// timestamp window; every ETH SQL must bound its DAY-partitioned timestamp.
// This is the cost guardrail from the spec: no query without a partition
// filter can ship.
func TestCryptoPulseSQLPartitionFilters(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"btc activity", cryptoActivitySQL("btc"), []string{
			"block_timestamp_month", "DATE_TRUNC(@start_date, MONTH)",
			"block_timestamp >= TIMESTAMP(@start_date)", "block_timestamp < TIMESTAMP(@end_date)",
			"crypto_bitcoin.transactions"}},
		{"eth activity", cryptoActivitySQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "block_timestamp < TIMESTAMP(@end_date)",
			"crypto_ethereum.transactions"}},
		{"btc addresses", cryptoAddressesSQL("btc"), []string{
			"block_timestamp_month", "APPROX_COUNT_DISTINCT", "UNNEST"}},
		{"eth addresses", cryptoAddressesSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "APPROX_COUNT_DISTINCT(from_address)"}},
		{"btc blocks", cryptoBlocksSQL("btc"), []string{
			"timestamp_month", "crypto_bitcoin.blocks", "weight"}},
		{"eth blocks", cryptoBlocksSQL("eth"), []string{
			"timestamp >= TIMESTAMP(@start_date)", "crypto_ethereum.blocks", "gas_used"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, want := range tt.want {
				if !strings.Contains(tt.sql, want) {
					t.Errorf("%s: missing %q in SQL:\n%s", tt.name, want, tt.sql)
				}
			}
		})
	}
}
