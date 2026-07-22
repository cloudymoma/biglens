package main

// BigQuery Open Data: Crypto Pulse (Bitcoin + Ethereum on-chain metrics).
//
// Queries `bigquery-public-data.crypto_bitcoin` and `crypto_ethereum`
// directly. Partitioning differs per dataset and both bounds are always
// applied:
//   - crypto_bitcoin tables are MONTH-partitioned on block_timestamp_month /
//     timestamp_month (DATE) — filtering block_timestamp alone would scan the
//     whole table, so every BTC WHERE carries the month bound AND the exact
//     timestamp window.
//   - crypto_ethereum tables are DAY-partitioned on block_timestamp (blocks:
//     timestamp) and are filtered on it directly.
// Windows are half-open [start, end) with end = today UTC, so rows cover
// complete days only. Money columns are CAST(... AS FLOAT64) in SQL (sat/1e8,
// wei/1e18) so rows scan into float64, never *big.Rat.

import (
	"context"
	"fmt"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	btcTxTable     = "`bigquery-public-data.crypto_bitcoin.transactions`"
	btcBlocksTable = "`bigquery-public-data.crypto_bitcoin.blocks`"
	ethTxTable     = "`bigquery-public-data.crypto_ethereum.transactions`"
	ethBlocksTable = "`bigquery-public-data.crypto_ethereum.blocks`"
)

// WHERE fragments shared by every builder; tests assert their presence.
const (
	btcTxWindow = `block_timestamp_month BETWEEN DATE_TRUNC(@start_date, MONTH) AND @end_date
		AND block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)`
	btcBlockWindow = `timestamp_month BETWEEN DATE_TRUNC(@start_date, MONTH) AND @end_date
		AND timestamp >= TIMESTAMP(@start_date) AND timestamp < TIMESTAMP(@end_date)`
	ethTxWindow    = `block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)`
	ethBlockWindow = `timestamp >= TIMESTAMP(@start_date) AND timestamp < TIMESTAMP(@end_date)`
)

func cryptoDateParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
	}
}

// --- /pulse: daily activity, active addresses, block stats ---

// CryptoActivityRow is one day's transaction activity for one chain.
// ValueSettled for BTC includes change outputs returning to the sender
// (an upper bound on economic volume — surfaced as a UI caveat).
type CryptoActivityRow struct {
	Date         string  `json:"date" bigquery:"date"`
	TxCount      int64   `json:"tx_count" bigquery:"tx_count"`
	ValueSettled float64 `json:"value_settled" bigquery:"value_settled"`
	FeesTotal    float64 `json:"fees_total" bigquery:"fees_total"`
}

func cryptoActivitySQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNT(*) AS tx_count,
			ROUND(CAST(SUM(output_value) AS FLOAT64) / 1e8, 2) AS value_settled,
			ROUND(CAST(SUM(fee) AS FLOAT64) / 1e8, 4) AS fees_total
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
	}
	// receipt_effective_gas_price is NULL pre-London; gas_price is the legacy
	// fallback. The product is computed in FLOAT64 to avoid INT64 overflow.
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNT(*) AS tx_count,
			ROUND(CAST(SUM(value) AS FLOAT64) / 1e18, 2) AS value_settled,
			ROUND(SUM(receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64)) / 1e18, 4) AS fees_total
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoActivity(ctx context.Context, chain string, start, end civil.Date) ([]CryptoActivityRow, error) {
	q := b.client.Query(cryptoActivitySQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[CryptoActivityRow](q, ctx)
}

// CryptoAddressRow is one day's approximate distinct active senders.
type CryptoAddressRow struct {
	Date            string `json:"date" bigquery:"date"`
	ActiveAddresses int64  `json:"active_addresses" bigquery:"active_addresses"`
}

func cryptoAddressesSQL(chain string) string {
	if chain == "btc" {
		// Input addresses live in a nested array; APPROX_COUNT_DISTINCT (HLL)
		// avoids the exact-distinct shuffle — ~1% error is invisible on a
		// trend chart and billed bytes are identical either way.
		return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(t.block_timestamp)) AS date,
			APPROX_COUNT_DISTINCT(addr) AS active_addresses
		FROM %s t, UNNEST(t.inputs) AS i, UNNEST(i.addresses) AS addr
		WHERE t.%s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindowAliased)
	}
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			APPROX_COUNT_DISTINCT(from_address) AS active_addresses
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

// btcTxWindowAliased is btcTxWindow with the `t.` alias for UNNEST queries.
const btcTxWindowAliased = `block_timestamp_month BETWEEN DATE_TRUNC(@start_date, MONTH) AND @end_date
		AND t.block_timestamp >= TIMESTAMP(@start_date) AND t.block_timestamp < TIMESTAMP(@end_date)`

func (b *BQClient) GetCryptoActiveAddresses(ctx context.Context, chain string, start, end civil.Date) ([]CryptoAddressRow, error) {
	q := b.client.Query(cryptoAddressesSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[CryptoAddressRow](q, ctx)
}

// CryptoBlockRow is one day's block production and fullness. FullnessPct is
// BTC avg weight vs the 4M-weight-unit limit, ETH avg gas_used/gas_limit.
type CryptoBlockRow struct {
	Date        string  `json:"date" bigquery:"date"`
	Blocks      int64   `json:"blocks" bigquery:"blocks"`
	FullnessPct float64 `json:"fullness_pct" bigquery:"fullness_pct"`
}

func cryptoBlocksSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(timestamp)) AS date,
			COUNT(*) AS blocks,
			ROUND(AVG(weight) / 4e6 * 100, 1) AS fullness_pct
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, btcBlocksTable, btcBlockWindow)
	}
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(timestamp)) AS date,
			COUNT(*) AS blocks,
			ROUND(AVG(SAFE_DIVIDE(gas_used, gas_limit)) * 100, 1) AS fullness_pct
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethBlocksTable, ethBlockWindow)
}

func (b *BQClient) GetCryptoBlockStats(ctx context.Context, chain string, start, end civil.Date) ([]CryptoBlockRow, error) {
	q := b.client.Query(cryptoBlocksSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[CryptoBlockRow](q, ctx)
}
