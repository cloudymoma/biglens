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

// --- /fees: fee rates, miner revenue, EIP-1559 burn split ---

// BtcFeeRow is one day's BTC fee economics. SubsidyBTC is filled by
// mergeBtcFees (coinbase revenue − fees), not by SQL.
type BtcFeeRow struct {
	Date         string  `json:"date" bigquery:"date"`
	MedianFeeVB  float64 `json:"median_fee_vb" bigquery:"median_fee_vb"`
	TotalFeesBTC float64 `json:"total_fees_btc" bigquery:"total_fees_btc"`
	SubsidyBTC   float64 `json:"subsidy_btc" bigquery:"-"`
}

func btcFeesSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			ROUND(APPROX_QUANTILES(CAST(fee AS FLOAT64) / NULLIF(virtual_size, 0), 100)[OFFSET(50)], 2) AS median_fee_vb,
			ROUND(CAST(SUM(fee) AS FLOAT64) / 1e8, 4) AS total_fees_btc
		FROM %s
		WHERE NOT is_coinbase AND %s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
}

func (b *BQClient) GetBtcFees(ctx context.Context, start, end civil.Date) ([]BtcFeeRow, error) {
	q := b.client.Query(btcFeesSQL())
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[BtcFeeRow](q, ctx)
}

// BtcCoinbaseRow is one day's total miner revenue (subsidy + fees), read
// from the coinbase transactions' outputs.
type BtcCoinbaseRow struct {
	Date        string  `json:"date" bigquery:"date"`
	CoinbaseBTC float64 `json:"coinbase_btc" bigquery:"coinbase_btc"`
}

func btcCoinbaseSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			ROUND(CAST(SUM(output_value) AS FLOAT64) / 1e8, 4) AS coinbase_btc
		FROM %s
		WHERE is_coinbase AND %s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
}

func (b *BQClient) GetBtcCoinbase(ctx context.Context, start, end civil.Date) ([]BtcCoinbaseRow, error) {
	q := b.client.Query(btcCoinbaseSQL())
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[BtcCoinbaseRow](q, ctx)
}

// EthFeeRow is one day's ETH fee economics; the gas price average is
// gas-weighted. BurnedETH/TipsETH are filled by mergeEthFees.
type EthFeeRow struct {
	Date         string  `json:"date" bigquery:"date"`
	AvgGasGwei   float64 `json:"avg_gas_gwei" bigquery:"avg_gas_gwei"`
	TotalFeesETH float64 `json:"total_fees_eth" bigquery:"total_fees_eth"`
	BurnedETH    float64 `json:"burned_eth" bigquery:"-"`
	TipsETH      float64 `json:"tips_eth" bigquery:"-"`
}

func ethFeesSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			ROUND(SAFE_DIVIDE(
				SUM(receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64)),
				SUM(receipt_gas_used)) / 1e9, 2) AS avg_gas_gwei,
			ROUND(SUM(receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64)) / 1e18, 4) AS total_fees_eth
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetEthFees(ctx context.Context, start, end civil.Date) ([]EthFeeRow, error) {
	q := b.client.Query(ethFeesSQL())
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[EthFeeRow](q, ctx)
}

// EthBurnRow splits a day's fees into base fee burned vs priority tips
// (EIP-1559). base_fee_per_gas is NULL pre-London, coalesced to 0 so those
// days report everything as tips — historically accurate.
type EthBurnRow struct {
	Date      string  `json:"date" bigquery:"date"`
	BurnedETH float64 `json:"burned_eth" bigquery:"burned_eth"`
	TipsETH   float64 `json:"tips_eth" bigquery:"tips_eth"`
}

func ethBurnSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(t.block_timestamp)) AS date,
			ROUND(SUM(t.receipt_gas_used * CAST(COALESCE(b.base_fee_per_gas, 0) AS FLOAT64)) / 1e18, 4) AS burned_eth,
			ROUND(SUM(t.receipt_gas_used * GREATEST(
				CAST(COALESCE(t.receipt_effective_gas_price, t.gas_price) AS FLOAT64) - CAST(COALESCE(b.base_fee_per_gas, 0) AS FLOAT64),
				0)) / 1e18, 4) AS tips_eth
		FROM %s t
		JOIN %s b ON b.number = t.block_number
		WHERE t.block_timestamp >= TIMESTAMP(@start_date) AND t.block_timestamp < TIMESTAMP(@end_date)
			AND b.timestamp >= TIMESTAMP(@start_date) AND b.timestamp < TIMESTAMP(@end_date)
		GROUP BY date ORDER BY date`, ethTxTable, ethBlocksTable)
}

func (b *BQClient) GetEthBurn(ctx context.Context, start, end civil.Date) ([]EthBurnRow, error) {
	q := b.client.Query(ethBurnSQL())
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[EthBurnRow](q, ctx)
}

// --- /whales: largest transfers, top receivers, whale trend, concentration ---

// Whale thresholds in native units (no USD rate exists in these datasets);
// the satoshi/wei literals appear verbatim in SQL so tests can assert them.
const (
	whaleThresholdBTC = 100.0  // BTC;  10000000000 satoshi
	whaleThresholdETH = 1000.0 // ETH;  1000000000000000000000 wei (NUMERIC literal)
)

// WhaleTx is one large transaction. From/To are empty on BTC: with multiple
// inputs and outputs there is no single sender/receiver to name.
type WhaleTx struct {
	Hash   string  `json:"hash" bigquery:"hash"`
	Time   string  `json:"time" bigquery:"time"`
	From   string  `json:"from" bigquery:"from_address"`
	To     string  `json:"to" bigquery:"to_address"`
	Amount float64 `json:"amount" bigquery:"amount"`
}

func whaleLargestSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			`+"`hash`"+`,
			FORMAT_TIMESTAMP('%%Y-%%m-%%d %%H:%%M', block_timestamp) AS time,
			'' AS from_address,
			'' AS to_address,
			ROUND(CAST(output_value AS FLOAT64) / 1e8, 2) AS amount
		FROM %s
		WHERE NOT is_coinbase AND %s
		ORDER BY output_value DESC
		LIMIT 50`, btcTxTable, btcTxWindow)
	}
	return fmt.Sprintf(`
		SELECT
			`+"`hash`"+`,
			FORMAT_TIMESTAMP('%%Y-%%m-%%d %%H:%%M', block_timestamp) AS time,
			COALESCE(from_address, '') AS from_address,
			COALESCE(to_address, '') AS to_address,
			ROUND(CAST(value AS FLOAT64) / 1e18, 2) AS amount
		FROM %s
		WHERE %s
		ORDER BY value DESC
		LIMIT 50`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoLargestTxs(ctx context.Context, chain string, start, end civil.Date) ([]WhaleTx, error) {
	q := b.client.Query(whaleLargestSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[WhaleTx](q, ctx)
}

// WhaleAddress is one of the window's top value receivers.
type WhaleAddress struct {
	Address string  `json:"address" bigquery:"address"`
	Total   float64 `json:"total" bigquery:"total"`
	TxCount int64   `json:"tx_count" bigquery:"tx_count"`
}

func whaleReceiversSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			addr AS address,
			ROUND(CAST(SUM(o.value) AS FLOAT64) / 1e8, 2) AS total,
			COUNT(*) AS tx_count
		FROM %s t, UNNEST(t.outputs) AS o, UNNEST(o.addresses) AS addr
		WHERE t.%s
		GROUP BY addr
		ORDER BY total DESC
		LIMIT 20`, btcTxTable, btcTxWindowAliased)
	}
	return fmt.Sprintf(`
		SELECT
			to_address AS address,
			ROUND(CAST(SUM(value) AS FLOAT64) / 1e18, 2) AS total,
			COUNT(*) AS tx_count
		FROM %s
		WHERE to_address IS NOT NULL AND %s
		GROUP BY to_address
		ORDER BY total DESC
		LIMIT 20`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoTopReceivers(ctx context.Context, chain string, start, end civil.Date) ([]WhaleAddress, error) {
	q := b.client.Query(whaleReceiversSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[WhaleAddress](q, ctx)
}

// WhaleTrendRow counts whale-sized transactions per day.
type WhaleTrendRow struct {
	Date       string `json:"date" bigquery:"date"`
	WhaleCount int64  `json:"whale_count" bigquery:"whale_count"`
}

func whaleTrendSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNTIF(output_value >= 10000000000) AS whale_count
		FROM %s
		WHERE NOT is_coinbase AND %s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
	}
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNTIF(value >= NUMERIC '1000000000000000000000') AS whale_count
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoWhaleTrend(ctx context.Context, chain string, start, end civil.Date) ([]WhaleTrendRow, error) {
	q := b.client.Query(whaleTrendSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[WhaleTrendRow](q, ctx)
}

// ConcentrationRow is the share of a day's moved value carried by its top 1%
// largest transactions.
type ConcentrationRow struct {
	Date         string  `json:"date" bigquery:"date"`
	Top1PctShare float64 `json:"top1pct_share" bigquery:"top1pct_share"`
}

// whaleConcentrationSQL computes per-day p99 with APPROX_QUANTILES, then the
// value share at-or-above it. The CTE reads only the value column twice —
// far cheaper than an exact PERCENTILE_CONT window shuffle.
func whaleConcentrationSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		WITH t AS (
			SELECT DATE(block_timestamp) AS d, CAST(output_value AS FLOAT64) AS v
			FROM %s
			WHERE NOT is_coinbase AND %s
		), q AS (
			SELECT d, APPROX_QUANTILES(v, 100)[OFFSET(99)] AS p99 FROM t GROUP BY d
		)
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', t.d) AS date,
			ROUND(SAFE_DIVIDE(SUM(IF(t.v >= q.p99, t.v, 0)), SUM(t.v)) * 100, 1) AS top1pct_share
		FROM t JOIN q USING (d)
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
	}
	return fmt.Sprintf(`
		WITH t AS (
			SELECT DATE(block_timestamp) AS d, CAST(value AS FLOAT64) AS v
			FROM %s
			WHERE %s
		), q AS (
			SELECT d, APPROX_QUANTILES(v, 100)[OFFSET(99)] AS p99 FROM t GROUP BY d
		)
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', t.d) AS date,
			ROUND(SAFE_DIVIDE(SUM(IF(t.v >= q.p99, t.v, 0)), SUM(t.v)) * 100, 1) AS top1pct_share
		FROM t JOIN q USING (d)
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoConcentration(ctx context.Context, chain string, start, end civil.Date) ([]ConcentrationRow, error) {
	q := b.client.Query(whaleConcentrationSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	return collectRows[ConcentrationRow](q, ctx)
}
