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

func TestCryptoFeeSQLPartitionFilters(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"btc fees", btcFeesSQL(), []string{
			"block_timestamp_month", "APPROX_QUANTILES", "virtual_size", "NOT is_coinbase"}},
		{"btc coinbase", btcCoinbaseSQL(), []string{
			"block_timestamp_month", "is_coinbase", "output_value"}},
		{"eth fees", ethFeesSQL(), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_gas_used", "SAFE_DIVIDE"}},
		{"eth burn", ethBurnSQL(), []string{
			"t.block_timestamp >= TIMESTAMP(@start_date)", "b.timestamp >= TIMESTAMP(@start_date)",
			"base_fee_per_gas", "JOIN"}},
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

func TestCryptoMiningSQLPartitionFilters(t *testing.T) {
	sql := btcMiningBlocksSQL()
	// Partition guard plus the bits→difficulty decode: exponent from the
	// first 2 hex chars, mantissa from the last 6, 65535/mantissa * 256^(29-e).
	for _, want := range []string{
		"timestamp_month", "timestamp >= TIMESTAMP(@start_date)", "crypto_bitcoin.blocks",
		"SUBSTR(bits, 1, 2)", "SUBSTR(bits, 3, 6)", "65535", "POW(256, 29",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("btc mining blocks: missing %q in SQL:\n%s", want, sql)
		}
	}
}

func TestCryptoWhaleSQLPartitionFilters(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"btc largest", whaleLargestSQL("btc"), []string{
			"block_timestamp_month", "NOT is_coinbase", "ORDER BY output_value DESC", "LIMIT 50"}},
		{"eth largest", whaleLargestSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "from_address", "to_address",
			"ORDER BY value DESC", "LIMIT 50"}},
		{"btc receivers", whaleReceiversSQL("btc"), []string{
			"block_timestamp_month", "UNNEST", "LIMIT 20"}},
		{"eth receivers", whaleReceiversSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "to_address", "LIMIT 20"}},
		{"btc trend", whaleTrendSQL("btc"), []string{
			"block_timestamp_month", "COUNTIF", "10000000000"}}, // 100 BTC in satoshi
		{"eth trend", whaleTrendSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "COUNTIF", "1000000000000000000000"}}, // 1000 ETH in wei
		{"btc concentration", whaleConcentrationSQL("btc"), []string{
			"block_timestamp_month", "APPROX_QUANTILES", "OFFSET(99)"}},
		{"eth concentration", whaleConcentrationSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "APPROX_QUANTILES", "OFFSET(99)"}},
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

func TestCryptoTokenSQLShape(t *testing.T) {
	sql := tokenTopSQL()
	for _, want := range []string{
		"WITH top AS", "block_timestamp >= TIMESTAMP(@start_date)",
		"APPROX_COUNT_DISTINCT(from_address)", "APPROX_COUNT_DISTINCT(to_address)",
		"LIMIT 25", "LEFT JOIN",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("tokenTopSQL missing %q:\n%s", want, sql)
		}
	}
	// Spec: aggregate token_transfers to 25 rows in a CTE BEFORE joining the
	// tokens table — the LIMIT must appear before the join.
	if strings.Index(sql, "LIMIT 25") > strings.Index(sql, "LEFT JOIN") {
		t.Errorf("tokenTopSQL joins tokens before aggregating transfers:\n%s", sql)
	}

	for name, s := range map[string]string{
		"tokenDailySQL":     tokenDailySQL(),
		"contractsDailySQL": contractsDailySQL(),
	} {
		if !strings.Contains(s, "block_timestamp >= TIMESTAMP(@start_date)") {
			t.Errorf("%s missing partition filter:\n%s", name, s)
		}
	}
	if !strings.Contains(contractsDailySQL(), "is_erc20") {
		t.Errorf("contractsDailySQL missing erc flags")
	}
}
