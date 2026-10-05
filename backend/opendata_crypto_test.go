package main

import (
	"strings"
	"testing"

	"cloud.google.com/go/civil"
)

// Every BTC and ETH transactions query must bound the exact timestamp window,
// and ETH blocks / contracts queries must also carry the post-Merge block
// number lower bound (`number >= @start_block` / `block_number >= @start_block`)
// because the Analytics Hub views for blocks and contracts do not prune on
// timestamp alone.
func TestCryptoPulseSQLPartitionFilters(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"btc activity", cryptoActivitySQL("btc"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "block_timestamp < TIMESTAMP(@end_date)",
			"crypto_bitcoin.transactions"}},
		{"eth activity", cryptoActivitySQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "block_timestamp < TIMESTAMP(@end_date)",
			"receipt_status = 1", "receipt_blob_gas_used",
			"crypto_ethereum.transactions"}},
		{"btc addresses", cryptoAddressesSQL("btc"), []string{
			"t.block_timestamp >= TIMESTAMP(@start_date)", "APPROX_COUNT_DISTINCT", "UNNEST"}},
		{"eth addresses", cryptoAddressesSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "APPROX_COUNT_DISTINCT(from_address)"}},
		{"btc blocks", cryptoBlocksSQL("btc"), []string{
			"timestamp >= TIMESTAMP(@start_date)", "crypto_bitcoin.blocks", "weight"}},
		{"eth blocks", cryptoBlocksSQL("eth"), []string{
			"number >= @start_block", "timestamp >= TIMESTAMP(@start_date)", "crypto_ethereum.blocks", "gas_used"}},
		{"btc pulse combined 90d", cryptoPulseChainSQL("btc", true), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "APPROX_COUNT_DISTINCT", "UNNEST"}},
		{"eth pulse combined 90d", cryptoPulseChainSQL("eth", true), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "number >= @start_block",
			"receipt_status = 1", "APPROX_COUNT_DISTINCT(from_address)"}},
		{"eth pulse combined 365d", cryptoPulseChainSQL("eth", false), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "number >= @start_block"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, want := range tt.want {
				if !strings.Contains(tt.sql, want) {
					t.Errorf("%s: missing %q in SQL:\n%s", tt.name, want, tt.sql)
				}
			}
			if strings.Contains(tt.sql, "timestamp_month") {
				t.Errorf("%s: should not include redundant *_month filter in SQL:\n%s", tt.name, tt.sql)
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
		{"btc fees+coinbase", btcFeesSQL(), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "APPROX_QUANTILES", "virtual_size", "is_coinbase", "coinbase_btc"}},
		{"btc coinbase", btcCoinbaseSQL(), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "is_coinbase", "output_value"}},
		{"eth fees+burn+blocks", ethFeesSQL(), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "number >= @start_block",
			"receipt_gas_used", "receipt_blob_gas_used", "base_fee_per_gas", "SAFE_DIVIDE"}},
		{"eth burn", ethBurnSQL(), []string{
			"number >= @start_block", "timestamp >= TIMESTAMP(@start_date)",
			"base_fee_per_gas"}},
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
		"timestamp >= TIMESTAMP(@start_date)", "crypto_bitcoin.blocks",
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
			"block_timestamp >= TIMESTAMP(@start_date)", "NOT is_coinbase", "ORDER BY output_value DESC", "LIMIT 50"}},
		{"eth largest", whaleLargestSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_status = 1", "value > 0",
			"from_address", "to_address", "ORDER BY value DESC", "LIMIT 50"}},
		{"btc receivers", whaleReceiversSQL("btc"), []string{
			"t.block_timestamp >= TIMESTAMP(@start_date)", "NOT t.is_coinbase", "UNNEST", "LIMIT 20"}},
		{"eth receivers", whaleReceiversSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_status = 1", "value > 0", "to_address", "LIMIT 20"}},
		{"btc trend", whaleTrendSQL("btc"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "COUNTIF", "10000000000"}}, // 100 BTC in satoshi
		{"eth trend", whaleTrendSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_status = 1", "COUNTIF", "1000000000000000000000"}}, // 1000 ETH in wei
		{"btc concentration", whaleConcentrationSQL("btc"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "output_value > 0", "APPROX_QUANTILES", "OFFSET(99)"}},
		{"eth concentration", whaleConcentrationSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_status = 1", "value > 0", "APPROX_QUANTILES", "OFFSET(99)"}},
		{"btc bundle", whaleBundleSQL("btc"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "NOT is_coinbase", "LIMIT 50", "LIMIT 20", "10000000000", "OFFSET(99)"}},
		{"eth bundle", whaleBundleSQL("eth"), []string{
			"block_timestamp >= TIMESTAMP(@start_date)", "receipt_status = 1", "value > 0", "LIMIT 50", "LIMIT 20", "1000000000000000000000", "OFFSET(99)"}},
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
		"LIMIT 25", "LEFT JOIN", "amended_tokens",
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

	if !strings.Contains(tokenDailySQL(), "block_timestamp >= TIMESTAMP(@start_date)") {
		t.Errorf("tokenDailySQL missing partition filter:\n%s", tokenDailySQL())
	}
	for _, want := range []string{"block_number >= @start_block", "block_timestamp >= TIMESTAMP(@start_date)", "is_erc20"} {
		if !strings.Contains(contractsDailySQL(), want) {
			t.Errorf("contractsDailySQL missing %q:\n%s", want, contractsDailySQL())
		}
	}
	for _, want := range []string{"number >= @start_block", "timestamp >= TIMESTAMP(@start_date)", "SUM(transaction_count)"} {
		if !strings.Contains(ethNativeTxsSQL(), want) {
			t.Errorf("ethNativeTxsSQL missing %q:\n%s", want, ethNativeTxsSQL())
		}
	}
}

func TestEthStartBlockLB(t *testing.T) {
	// Pre-Merge date should return 0.
	if got := ethStartBlockLB(civil.Date{Year: 2022, Month: 9, Day: 1}); got != 0 {
		t.Errorf("pre-Merge ethStartBlockLB = %d, want 0", got)
	}
	// Known post-Merge checkpoints: lower bound must be > mergeBlock and safely below actual first block of that day.
	// 2024-01-01 00:00 UTC actual block was 18908895.
	got2024 := ethStartBlockLB(civil.Date{Year: 2024, Month: 1, Day: 1})
	if got2024 <= 15537394 || got2024 > 18908895 {
		t.Errorf("2024-01-01 ethStartBlockLB = %d, want in (15537394, 18908895]", got2024)
	}
}

