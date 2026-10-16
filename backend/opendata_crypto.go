package main

// BigQuery Open Data: Crypto Pulse (Bitcoin + Ethereum on-chain metrics).
//
// Queries `bigquery-public-data.crypto_bitcoin` and `crypto_ethereum`
// directly. Since the 2026 re-platforming (SQD pipelines) both are Analytics
// Hub linked datasets of views. Observed pruning behaviour on those views:
//   - crypto_bitcoin.transactions and crypto_ethereum.transactions /
//     token_transfers prune on block_timestamp (even below a day). (Passing
//     block_timestamp_month does not improve pruning and scans an extra
//     8 B/row.)
//   - crypto_ethereum.blocks and contracts carry a conservative post-Merge
//     block-number lower bound (`number >= @start_block` /
//     `block_number >= @start_block`) alongside the exact timestamp window:
//     dry runs show the literal block-number bound pruning these views, which
//     a timestamp-only predicate was not observed to do.
// Windows are half-open [start, end) with an ingest lag buffer so rows cover
// settled UTC days only. Money columns are CAST(... AS FLOAT64) in SQL
// (sat/1e8, wei/1e18) so rows scan into float64, never *big.Rat.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	btcTxTable     = "`bigquery-public-data.crypto_bitcoin.transactions`"
	btcBlocksTable = "`bigquery-public-data.crypto_bitcoin.blocks`"
	ethTxTable     = "`bigquery-public-data.crypto_ethereum.transactions`"
	ethBlocksTable = "`bigquery-public-data.crypto_ethereum.blocks`"
)

// Per-query MaxBytesBilled guardrails (~2x worst-case window dry-run bytes).
const (
	cryptoPulseMaxBytesBilled         int64 = 100 << 30 // 100 GiB (365d ETH pulse ~45 GiB)
	cryptoFeesMaxBytesBilled          int64 = 64 << 30  // 64 GiB  (365d ETH fees ~22.5 GiB)
	cryptoWhalesMaxBytesBilled        int64 = 80 << 30  // 80 GiB  (90d ETH whales ~36.7 GiB)
	cryptoTokensMaxBytesBilled        int64 = 40 << 30  // 40 GiB  (30d token_top ~15.1 GiB)
	cryptoTokenMetadataMaxBytesBilled int64 = 4 << 30   // 4 GiB   (unpartitioned tokens_latest ~1.70 GiB)
	cryptoMiningMaxBytesBilled        int64 = 16 << 30  // 16 GiB  (365d BTC mining ~4.7 GiB)
	cryptoBlocksMaxBytesBilled        int64 = 2 << 30   // 2 GiB   (pruned blocks/contracts < 500 MiB)
)

// WHERE fragments shared by every builder; tests assert their presence.
const (
	btcTxWindow        = `block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)`
	btcTxWindowAliased = `t.block_timestamp >= TIMESTAMP(@start_date) AND t.block_timestamp < TIMESTAMP(@end_date)`
	btcBlockWindow     = `timestamp >= TIMESTAMP(@start_date) AND timestamp < TIMESTAMP(@end_date)`
	ethTxWindow        = `block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)`
	ethBlockWindow     = `number >= @start_block
		AND timestamp >= TIMESTAMP(@start_date) AND timestamp < TIMESTAMP(@end_date)`
	ethContractWindow = `block_number >= @start_block
		AND block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)`
)

// ethStartBlockLB returns a conservative lower bound on the Ethereum block
// number at `start` (00:00 UTC) so queries against `crypto_ethereum.blocks`
// and `contracts` prune historical partitions. Post-Merge slots are 12s; a
// 0.97 factor tolerates up to 3% missed slots (~28 days of safety margin today).
func ethStartBlockLB(start civil.Date) int64 {
	const (
		mergeBlock = 15537394
		mergeUnix  = 1663224179 // 2022-09-15T06:42:59Z
	)
	t := start.In(time.UTC).Unix()
	if t <= mergeUnix {
		return 0
	}
	return mergeBlock + int64(float64(t-mergeUnix)/12.0*0.97)
}

func cryptoDateParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
	}
}

func cryptoEthBlockParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
		{Name: "start_block", Value: ethStartBlockLB(start)},
	}
}

// --- /pulse: daily activity, active addresses, block stats ---

// CryptoActivityRow is one day's transaction activity for one chain.
// ValueSettled for BTC includes change outputs returning to the sender
// (an upper bound on economic volume — surfaced as a UI caveat).
// On ETH, ValueSettled sums top-level value on succeeded transactions
// (receipt_status = 1), while FeesTotal includes both execution gas fees
// (across all transactions) and EIP-4844 blob gas fees.
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
	// fallback. Blob fees (EIP-4844) are added when present. Only succeeded
	// transactions (receipt_status = 1) transfer value.
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNT(*) AS tx_count,
			ROUND(SUM(IF(receipt_status = 1, CAST(value AS FLOAT64), 0)) / 1e18, 2) AS value_settled,
			ROUND(SUM(
				receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64) +
				CAST(IFNULL(receipt_blob_gas_used, 0) AS FLOAT64) * CAST(IFNULL(receipt_blob_gas_price, 0) AS FLOAT64)
			) / 1e18, 4) AS fees_total
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoActivity(ctx context.Context, chain string, start, end civil.Date) ([]CryptoActivityRow, error) {
	q := b.client.Query(cryptoActivitySQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoPulseMaxBytesBilled
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
		WHERE %s
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

func (b *BQClient) GetCryptoActiveAddresses(ctx context.Context, chain string, start, end civil.Date) ([]CryptoAddressRow, error) {
	q := b.client.Query(cryptoAddressesSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoPulseMaxBytesBilled
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
	if chain == "eth" {
		q.Parameters = cryptoEthBlockParams(start, end)
	} else {
		q.Parameters = cryptoDateParams(start, end)
	}
	q.MaxBytesBilled = cryptoBlocksMaxBytesBilled
	return collectRows[CryptoBlockRow](q, ctx)
}

// cryptoPulseRow is a combined per-day row used by /pulse to scan each chain's
// transactions table once (instead of separate activity + addresses jobs).
type cryptoPulseRow struct {
	Date            string  `bigquery:"date"`
	TxCount         int64   `bigquery:"tx_count"`
	ValueSettled    float64 `bigquery:"value_settled"`
	FeesTotal       float64 `bigquery:"fees_total"`
	ActiveAddresses int64   `bigquery:"active_addresses"`
	Blocks          int64   `bigquery:"blocks"`
	FullnessPct     float64 `bigquery:"fullness_pct"`
}

func cryptoPulseChainSQL(chain string, includeAddresses bool) string {
	if chain == "btc" {
		if !includeAddresses {
			return fmt.Sprintf(`
			SELECT
				FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
				COUNT(*) AS tx_count,
				ROUND(CAST(SUM(output_value) AS FLOAT64) / 1e8, 2) AS value_settled,
				ROUND(CAST(SUM(fee) AS FLOAT64) / 1e8, 4) AS fees_total,
				0 AS active_addresses,
				0 AS blocks,
				0.0 AS fullness_pct
			FROM %s
			WHERE %s
			GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
		}
		return fmt.Sprintf(`
		WITH tx AS (
			SELECT DATE(block_timestamp) AS d, output_value, fee, inputs
			FROM %s
			WHERE %s
		), act AS (
			SELECT
				d,
				COUNT(*) AS tx_count,
				ROUND(CAST(SUM(output_value) AS FLOAT64) / 1e8, 2) AS value_settled,
				ROUND(CAST(SUM(fee) AS FLOAT64) / 1e8, 4) AS fees_total
			FROM tx
			GROUP BY d
		), addr AS (
			SELECT
				d,
				APPROX_COUNT_DISTINCT(a) AS active_addresses
			FROM tx, UNNEST(tx.inputs) AS i, UNNEST(i.addresses) AS a
			GROUP BY d
		)
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', act.d) AS date,
			act.tx_count,
			act.value_settled,
			act.fees_total,
			COALESCE(addr.active_addresses, 0) AS active_addresses,
			0 AS blocks,
			0.0 AS fullness_pct
		FROM act
		LEFT JOIN addr USING (d)
		ORDER BY date`, btcTxTable, btcTxWindow)
	}
	addrExpr := "0"
	if includeAddresses {
		addrExpr = "APPROX_COUNT_DISTINCT(from_address)"
	}
	return fmt.Sprintf(`
		WITH tx AS (
			SELECT
				DATE(block_timestamp) AS d,
				COUNT(*) AS tx_count,
				ROUND(SUM(IF(receipt_status = 1, CAST(value AS FLOAT64), 0)) / 1e18, 2) AS value_settled,
				ROUND(SUM(
					receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64) +
					CAST(IFNULL(receipt_blob_gas_used, 0) AS FLOAT64) * CAST(IFNULL(receipt_blob_gas_price, 0) AS FLOAT64)
				) / 1e18, 4) AS fees_total,
				%s AS active_addresses
			FROM %s
			WHERE %s
			GROUP BY d
		), b AS (
			SELECT
				DATE(timestamp) AS d,
				COUNT(*) AS blocks,
				ROUND(AVG(SAFE_DIVIDE(gas_used, gas_limit)) * 100, 1) AS fullness_pct
			FROM %s
			WHERE %s
			GROUP BY d
		)
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', COALESCE(tx.d, b.d)) AS date,
			COALESCE(tx.tx_count, 0) AS tx_count,
			COALESCE(tx.value_settled, 0) AS value_settled,
			COALESCE(tx.fees_total, 0) AS fees_total,
			COALESCE(tx.active_addresses, 0) AS active_addresses,
			COALESCE(b.blocks, 0) AS blocks,
			COALESCE(b.fullness_pct, 0) AS fullness_pct
		FROM tx FULL JOIN b USING (d)
		ORDER BY date`, addrExpr, ethTxTable, ethTxWindow, ethBlocksTable, ethBlockWindow)
}

func (b *BQClient) getCryptoPulseChain(ctx context.Context, chain string, start, end civil.Date, includeAddresses bool) ([]cryptoPulseRow, error) {
	q := b.client.Query(cryptoPulseChainSQL(chain, includeAddresses))
	if chain == "eth" {
		q.Parameters = cryptoEthBlockParams(start, end)
	} else {
		q.Parameters = cryptoDateParams(start, end)
	}
	q.MaxBytesBilled = cryptoPulseMaxBytesBilled
	return collectRows[cryptoPulseRow](q, ctx)
}

// --- /fees: fee rates, miner revenue, EIP-1559 burn split ---

// BtcFeeRow is one day's BTC fee economics. SubsidyBTC is filled by
// mergeBtcFees (coinbase revenue − fees), not by SQL.
type BtcFeeRow struct {
	Date         string  `json:"date" bigquery:"date"`
	MedianFeeVB  float64 `json:"median_fee_vb" bigquery:"median_fee_vb"`
	TotalFeesBTC float64 `json:"total_fees_btc" bigquery:"total_fees_btc"`
	CoinbaseBTC  float64 `json:"-" bigquery:"coinbase_btc"`
	SubsidyBTC   float64 `json:"subsidy_btc" bigquery:"-"`
}

// btcFeesSQL computes non-coinbase fee metrics AND coinbase revenue in a
// single pass over crypto_bitcoin.transactions via conditional aggregation.
func btcFeesSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			ROUND(APPROX_QUANTILES(IF(is_coinbase, NULL, CAST(fee AS FLOAT64) / NULLIF(virtual_size, 0)), 100)[OFFSET(50)], 2) AS median_fee_vb,
			ROUND(CAST(SUM(IF(is_coinbase, 0, fee)) AS FLOAT64) / 1e8, 4) AS total_fees_btc,
			ROUND(CAST(SUM(IF(is_coinbase, output_value, 0)) AS FLOAT64) / 1e8, 4) AS coinbase_btc
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, btcTxTable, btcTxWindow)
}

func (b *BQClient) GetBtcFees(ctx context.Context, start, end civil.Date) ([]BtcFeeRow, error) {
	q := b.client.Query(btcFeesSQL())
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoFeesMaxBytesBilled
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
	q.MaxBytesBilled = cryptoMiningMaxBytesBilled
	return collectRows[BtcCoinbaseRow](q, ctx)
}

// EthFeeRow is one day's ETH fee economics; the gas price average is
// gas-weighted. BurnedETH/TipsETH are computed alongside blocks in ethFeesSQL
// (or filled by mergeEthFees when called separately).
type EthFeeRow struct {
	Date         string  `json:"date" bigquery:"date"`
	AvgGasGwei   float64 `json:"avg_gas_gwei" bigquery:"avg_gas_gwei"`
	TotalFeesETH float64 `json:"total_fees_eth" bigquery:"total_fees_eth"`
	BurnedETH    float64 `json:"burned_eth" bigquery:"burned_eth"`
	TipsETH      float64 `json:"tips_eth" bigquery:"tips_eth"`
	Blocks       int64   `json:"-" bigquery:"blocks"`
	FullnessPct  float64 `json:"-" bigquery:"fullness_pct"`
}

// ethFeesSQL combines transaction fee aggregation (including EIP-4844 blob
// fees) with block-level EIP-1559 base fee burn and fullness in a single job,
// avoiding a row-level transactions × blocks JOIN.
func ethFeesSQL() string {
	return fmt.Sprintf(`
		WITH tx AS (
			SELECT
				DATE(block_timestamp) AS d,
				SUM(receipt_gas_used * CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64)) AS exec_fee_wei,
				SUM(CAST(IFNULL(receipt_blob_gas_used, 0) AS FLOAT64) * CAST(IFNULL(receipt_blob_gas_price, 0) AS FLOAT64)) AS blob_fee_wei,
				SUM(receipt_gas_used) AS gas
			FROM %s
			WHERE %s
			GROUP BY d
		), b AS (
			SELECT
				DATE(timestamp) AS d,
				SUM(CAST(gas_used AS FLOAT64) * CAST(COALESCE(base_fee_per_gas, 0) AS FLOAT64)) AS base_burn_wei,
				COUNT(*) AS blocks,
				AVG(SAFE_DIVIDE(gas_used, gas_limit)) AS fullness
			FROM %s
			WHERE %s
			GROUP BY d
		)
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', tx.d) AS date,
			ROUND(SAFE_DIVIDE(tx.exec_fee_wei, tx.gas) / 1e9, 2) AS avg_gas_gwei,
			ROUND((tx.exec_fee_wei + tx.blob_fee_wei) / 1e18, 4) AS total_fees_eth,
			ROUND((b.base_burn_wei + tx.blob_fee_wei) / 1e18, 4) AS burned_eth,
			ROUND(GREATEST(tx.exec_fee_wei - b.base_burn_wei, 0) / 1e18, 4) AS tips_eth,
			b.blocks AS blocks,
			ROUND(b.fullness * 100, 1) AS fullness_pct
		FROM tx
		JOIN b USING (d)
		ORDER BY date`, ethTxTable, ethTxWindow, ethBlocksTable, ethBlockWindow)
}

func (b *BQClient) GetEthFees(ctx context.Context, start, end civil.Date) ([]EthFeeRow, error) {
	q := b.client.Query(ethFeesSQL())
	q.Parameters = cryptoEthBlockParams(start, end)
	q.MaxBytesBilled = cryptoFeesMaxBytesBilled
	return collectRows[EthFeeRow](q, ctx)
}

// EthBurnRow splits a day's fees into base fee burned vs priority tips
// (EIP-1559). Retained for compatibility; CryptoFees uses the single-pass
// ethFeesSQL query above.
type EthBurnRow struct {
	Date      string  `json:"date" bigquery:"date"`
	BurnedETH float64 `json:"burned_eth" bigquery:"burned_eth"`
	TipsETH   float64 `json:"tips_eth" bigquery:"tips_eth"`
}

func ethBurnSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(timestamp)) AS date,
			ROUND(SUM(CAST(gas_used AS FLOAT64) * CAST(COALESCE(base_fee_per_gas, 0) AS FLOAT64)) / 1e18, 4) AS burned_eth,
			0.0 AS tips_eth
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethBlocksTable, ethBlockWindow)
}

func (b *BQClient) GetEthBurn(ctx context.Context, start, end civil.Date) ([]EthBurnRow, error) {
	q := b.client.Query(ethBurnSQL())
	q.Parameters = cryptoEthBlockParams(start, end)
	q.MaxBytesBilled = cryptoBlocksMaxBytesBilled
	return collectRows[EthBurnRow](q, ctx)
}

// --- /mining: daily block production, difficulty, network hashrate ---

// BtcMiningBlockRow is one day's block count and average difficulty. The
// blocks table has no difficulty column; it is decoded in SQL from the
// compact `bits` header (8 hex chars, no 0x prefix): exponent = first 2
// chars, mantissa = last 6, difficulty = 65535/mantissa * 256^(29-exponent).
type BtcMiningBlockRow struct {
	Date       string  `json:"date" bigquery:"date"`
	Blocks     int64   `json:"blocks" bigquery:"blocks"`
	Difficulty float64 `json:"difficulty" bigquery:"difficulty"`
}

func btcMiningBlocksSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(timestamp)) AS date,
			COUNT(*) AS blocks,
			AVG(SAFE_DIVIDE(65535, CAST(CONCAT('0x', SUBSTR(bits, 3, 6)) AS INT64))
				* POW(256, 29 - CAST(CONCAT('0x', SUBSTR(bits, 1, 2)) AS INT64))) AS difficulty
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, btcBlocksTable, btcBlockWindow)
}

func (b *BQClient) GetBtcMiningBlocks(ctx context.Context, start, end civil.Date) ([]BtcMiningBlockRow, error) {
	q := b.client.Query(btcMiningBlocksSQL())
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoBlocksMaxBytesBilled
	return collectRows[BtcMiningBlockRow](q, ctx)
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
	// FromRisk/ToRisk: local Address Risk lists the address is on (ETH only,
	// tagged per request on a copy; never cached, never from BigQuery).
	FromRisk []string `json:"from_risk,omitempty" bigquery:"-"`
	ToRisk   []string `json:"to_risk,omitempty" bigquery:"-"`
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
		WHERE receipt_status = 1 AND value > 0 AND %s
		ORDER BY value DESC
		LIMIT 50`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoLargestTxs(ctx context.Context, chain string, start, end civil.Date) ([]WhaleTx, error) {
	q := b.client.Query(whaleLargestSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoWhalesMaxBytesBilled
	return collectRows[WhaleTx](q, ctx)
}

// WhaleAddress is one of the window's top value receivers.
type WhaleAddress struct {
	Address string   `json:"address" bigquery:"address"`
	Total   float64  `json:"total" bigquery:"total"`
	TxCount int64    `json:"tx_count" bigquery:"tx_count"`
	Risk    []string `json:"risk,omitempty" bigquery:"-"` // see WhaleTx.FromRisk
}

func whaleReceiversSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		SELECT
			addr AS address,
			ROUND(CAST(SUM(o.value) AS FLOAT64) / 1e8, 2) AS total,
			COUNT(*) AS tx_count
		FROM %s t, UNNEST(t.outputs) AS o, UNNEST(o.addresses) AS addr
		WHERE NOT t.is_coinbase AND %s
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
		WHERE receipt_status = 1 AND value > 0 AND to_address IS NOT NULL AND %s
		GROUP BY to_address
		ORDER BY total DESC
		LIMIT 20`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoTopReceivers(ctx context.Context, chain string, start, end civil.Date) ([]WhaleAddress, error) {
	q := b.client.Query(whaleReceiversSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoWhalesMaxBytesBilled
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
		WHERE receipt_status = 1 AND %s
		GROUP BY date ORDER BY date`, ethTxTable, ethTxWindow)
}

func (b *BQClient) GetCryptoWhaleTrend(ctx context.Context, chain string, start, end civil.Date) ([]WhaleTrendRow, error) {
	q := b.client.Query(whaleTrendSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoWhalesMaxBytesBilled
	return collectRows[WhaleTrendRow](q, ctx)
}

// ConcentrationRow is the share of a day's moved value carried by its top 1%
// largest value-bearing transactions.
type ConcentrationRow struct {
	Date         string  `json:"date" bigquery:"date"`
	Top1PctShare float64 `json:"top1pct_share" bigquery:"top1pct_share"`
}

// whaleConcentrationSQL computes per-day p99 across value-bearing transactions
// with APPROX_QUANTILES, then the value share at-or-above it.
func whaleConcentrationSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		WITH t AS (
			SELECT DATE(block_timestamp) AS d, CAST(output_value AS FLOAT64) AS v
			FROM %s
			WHERE NOT is_coinbase AND output_value > 0 AND %s
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
			WHERE receipt_status = 1 AND value > 0 AND %s
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
	q.MaxBytesBilled = cryptoWhalesMaxBytesBilled
	return collectRows[ConcentrationRow](q, ctx)
}

type whaleDailyBundleRow struct {
	Date         string  `bigquery:"date"`
	WhaleCount   int64   `bigquery:"whale_count"`
	Top1PctShare float64 `bigquery:"top1pct_share"`
}

type whaleBundleRow struct {
	Largest      []WhaleTx             `bigquery:"largest"`
	TopReceivers []WhaleAddress        `bigquery:"top_receivers"`
	Daily        []whaleDailyBundleRow `bigquery:"daily"`
}

// whaleBundleSQL consolidates all 4 Whales widgets (Largest, TopReceivers,
// WhaleTrend, Concentration) into a single BigQuery scan of the transactions
// table, cutting billed bytes by 31% (BTC) to 46% (ETH).
func whaleBundleSQL(chain string) string {
	if chain == "btc" {
		return fmt.Sprintf(`
		WITH t AS (
			SELECT `+"`hash`"+`, block_timestamp, output_value, outputs
			FROM %s
			WHERE NOT is_coinbase AND %s
		), q AS (
			SELECT DATE(block_timestamp) AS d, APPROX_QUANTILES(CAST(output_value AS FLOAT64), 100)[OFFSET(99)] AS p99
			FROM t
			WHERE output_value > 0
			GROUP BY d
		)
		SELECT
			ARRAY(
				SELECT AS STRUCT
					`+"`hash`"+`,
					FORMAT_TIMESTAMP('%%Y-%%m-%%d %%H:%%M', block_timestamp) AS time,
					'' AS from_address,
					'' AS to_address,
					ROUND(CAST(output_value AS FLOAT64) / 1e8, 2) AS amount
				FROM t
				ORDER BY output_value DESC
				LIMIT 50
			) AS largest,
			ARRAY(
				SELECT AS STRUCT
					addr AS address,
					ROUND(CAST(SUM(o.value) AS FLOAT64) / 1e8, 2) AS total,
					COUNT(*) AS tx_count
				FROM t, UNNEST(t.outputs) AS o, UNNEST(o.addresses) AS addr
				GROUP BY addr
				ORDER BY total DESC
				LIMIT 20
			) AS top_receivers,
			ARRAY(
				SELECT AS STRUCT
					FORMAT_DATE('%%Y-%%m-%%d', d) AS date,
					COUNTIF(output_value >= 10000000000) AS whale_count,
					ROUND(SAFE_DIVIDE(
						SUM(IF(output_value > 0 AND CAST(output_value AS FLOAT64) >= p99, CAST(output_value AS FLOAT64), 0)),
						SUM(IF(output_value > 0, CAST(output_value AS FLOAT64), 0))
					) * 100, 1) AS top1pct_share
				FROM (SELECT DATE(block_timestamp) AS d, output_value FROM t)
				LEFT JOIN q USING (d)
				GROUP BY d
				ORDER BY d
			) AS daily`, btcTxTable, btcTxWindow)
	}
	return fmt.Sprintf(`
		WITH t AS (
			SELECT `+"`hash`"+`, block_timestamp, from_address, to_address, value
			FROM %s
			WHERE receipt_status = 1 AND %s
		), q AS (
			SELECT DATE(block_timestamp) AS d, APPROX_QUANTILES(CAST(value AS FLOAT64), 100)[OFFSET(99)] AS p99
			FROM t
			WHERE value > 0
			GROUP BY d
		)
		SELECT
			ARRAY(
				SELECT AS STRUCT
					`+"`hash`"+`,
					FORMAT_TIMESTAMP('%%Y-%%m-%%d %%H:%%M', block_timestamp) AS time,
					COALESCE(from_address, '') AS from_address,
					COALESCE(to_address, '') AS to_address,
					ROUND(CAST(value AS FLOAT64) / 1e18, 2) AS amount
				FROM t
				WHERE value > 0
				ORDER BY value DESC
				LIMIT 50
			) AS largest,
			ARRAY(
				SELECT AS STRUCT
					to_address AS address,
					ROUND(CAST(SUM(value) AS FLOAT64) / 1e18, 2) AS total,
					COUNT(*) AS tx_count
				FROM t
				WHERE to_address IS NOT NULL AND value > 0
				GROUP BY to_address
				ORDER BY total DESC
				LIMIT 20
			) AS top_receivers,
			ARRAY(
				SELECT AS STRUCT
					FORMAT_DATE('%%Y-%%m-%%d', d) AS date,
					COUNTIF(value >= NUMERIC '1000000000000000000000') AS whale_count,
					ROUND(SAFE_DIVIDE(
						SUM(IF(value > 0 AND CAST(value AS FLOAT64) >= p99, CAST(value AS FLOAT64), 0)),
						SUM(IF(value > 0, CAST(value AS FLOAT64), 0))
					) * 100, 1) AS top1pct_share
				FROM (SELECT DATE(block_timestamp) AS d, value FROM t)
				LEFT JOIN q USING (d)
				GROUP BY d
				ORDER BY d
			) AS daily`, ethTxTable, ethTxWindow)
}

func (b *BQClient) getCryptoWhalesBundle(ctx context.Context, chain string, start, end civil.Date) (*whaleBundleRow, error) {
	q := b.client.Query(whaleBundleSQL(chain))
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoWhalesMaxBytesBilled
	rows, err := collectRows[whaleBundleRow](q, ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &whaleBundleRow{}, nil
	}
	return &rows[0], nil
}

// --- /tokens: token Transfer activity & 12-token USD / mint-burn fundamentals ---

const (
	ethTokenTransfersTable = "`bigquery-public-data.crypto_ethereum.token_transfers`"
	ethTokensLatestTable   = "`bigquery-public-data.crypto_ethereum.tokens_latest`"
	ethContractsTable      = "`bigquery-public-data.crypto_ethereum.contracts`"
)

// TokenRow is one of the window's most-moved token contracts.
type TokenRow struct {
	TokenAddress       string   `json:"token_address" bigquery:"token_address"`
	Symbol             string   `json:"symbol" bigquery:"-"`
	Name               string   `json:"name" bigquery:"-"`
	TokenStandard      string   `json:"token_standard,omitempty" bigquery:"-"`
	Decimals           int      `json:"decimals,omitempty" bigquery:"-"`
	ProxyStandard      string   `json:"proxy_standard,omitempty" bigquery:"-"`
	CreatorAddress     string   `json:"creator_address,omitempty" bigquery:"-"`
	CreatedAt          string   `json:"created_at,omitempty" bigquery:"-"`
	Transfers          int64    `json:"transfers" bigquery:"transfers"`
	ZeroValueTransfers int64    `json:"zero_value_transfers" bigquery:"zero_value_transfers"`
	Senders            int64    `json:"senders" bigquery:"senders"`
	Receivers          int64    `json:"receivers" bigquery:"receivers"`
	Risk               []string `json:"risk,omitempty" bigquery:"-"`
	CreatorRisk        []string `json:"creator_risk,omitempty" bigquery:"-"`
}

// TokenDailyRow compares token Transfer events with total ETH transactions and
// tracks daily USD volume across allowlisted stablecoin, ETH, and BTC tokens.
type TokenDailyRow struct {
	Date               string  `json:"date" bigquery:"date"`
	Transfers          int64   `json:"transfers" bigquery:"transfers"`
	ZeroValueTransfers int64   `json:"zero_value_transfers" bigquery:"-"`
	NativeTxs          int64   `json:"native_txs" bigquery:"-"`
	StablecoinUSD      float64 `json:"stablecoin_usd" bigquery:"-"`
	EthTokenUSD        float64 `json:"eth_token_usd" bigquery:"-"`
	BtcTokenUSD        float64 `json:"btc_token_usd" bigquery:"-"`
}

type MajorTokenSummary struct {
	TokenAddress      string  `json:"token_address"`
	Symbol            string  `json:"symbol"`
	Name              string  `json:"name"`
	Category          string  `json:"category"` // "stablecoin" | "eth" | "btc"
	Transfers         int64   `json:"transfers"`
	ZeroValue         int64   `json:"zero_value"`
	Senders           int64   `json:"senders"`
	Receivers         int64   `json:"receivers"`
	VolumeToken       float64 `json:"volume_token"`
	VolumeUSD         float64 `json:"volume_usd"`
	MintedToken       float64 `json:"minted_token"`
	BurnedToken       float64 `json:"burned_token"`
	NetIssuedToken    float64 `json:"net_issued_token"`
	NetIssuedUSD      float64 `json:"net_issued_usd"`
	MintBurnSupported bool    `json:"mint_burn_supported"`
}

type majorTokenMeta struct {
	Address           string
	Symbol            string
	Name              string
	Decimals          int
	Category          string // "stablecoin" | "eth" | "btc"
	MintBurnSupported bool   // false for USDT (Issue/Redeem) and WETH (Deposit/Withdrawal)
}

var majorTokenExtraMeta = map[string]struct {
	Name              string
	Category          string
	MintBurnSupported bool
}{
	"USDT":   {"Tether USD", "stablecoin", false},
	"USDC":   {"USD Coin", "stablecoin", true},
	"DAI":    {"Dai Stablecoin", "stablecoin", true},
	"USDS":   {"Sky USD", "stablecoin", true},
	"USDe":   {"Ethena USDe", "stablecoin", true},
	"PYUSD":  {"PayPal USD", "stablecoin", true},
	"FDUSD":  {"First Digital USD", "stablecoin", true},
	"WETH":   {"Wrapped Ether", "eth", false},
	"stETH":  {"Lido Staked Ether", "eth", true},
	"wstETH": {"Wrapped liquid staked Ether 2.0", "eth", true},
	"WBTC":   {"Wrapped BTC", "btc", true},
	"cbBTC":  {"Coinbase Wrapped BTC", "btc", true},
}

// majorTokenCatalog is derived at init time from riskTokenAllowlist +
// riskTokenAllowlistSymbols (address_risk_association.go) so addresses and
// decimals have a single source of truth.
var (
	majorTokenCatalog []majorTokenMeta
	majorTokenByAddr  map[string]majorTokenMeta
)

func init() {
	addrBySym := make(map[string]string, len(riskTokenAllowlist))
	for addr, tok := range riskTokenAllowlist {
		addrBySym[tok.Symbol] = addr
	}
	majorTokenCatalog = make([]majorTokenMeta, 0, len(riskTokenAllowlistSymbols))
	majorTokenByAddr = make(map[string]majorTokenMeta, len(riskTokenAllowlistSymbols))
	for _, sym := range riskTokenAllowlistSymbols {
		addr := addrBySym[sym]
		allow := riskTokenAllowlist[addr]
		extra := majorTokenExtraMeta[sym]
		m := majorTokenMeta{
			Address:           addr,
			Symbol:            sym,
			Name:              extra.Name,
			Decimals:          allow.Decimals,
			Category:          extra.Category,
			MintBurnSupported: extra.MintBurnSupported,
		}
		majorTokenCatalog = append(majorTokenCatalog, m)
		majorTokenByAddr[addr] = m
	}
}

func tokenTopTransfersOnlySQL() string {
	return fmt.Sprintf(`
SELECT
  LOWER(token_address) AS token_address,
  COUNT(1) AS transfers,
  COUNTIF(value = '0') AS zero_value_transfers,
  APPROX_COUNT_DISTINCT(from_address) AS senders,
  APPROX_COUNT_DISTINCT(to_address) AS receivers
FROM %s
WHERE block_timestamp >= TIMESTAMP(@start_date)
  AND block_timestamp < TIMESTAMP(@end_date)
GROUP BY token_address
ORDER BY transfers DESC
LIMIT 25`, ethTokenTransfersTable)
}

func tokenMetadataBatchSQL() string {
	return fmt.Sprintf(`
SELECT
  LOWER(address) AS address,
  IFNULL(symbol, '') AS symbol,
  IFNULL(name, '') AS name,
  IFNULL(token_standard, '') AS token_standard,
  IFNULL(SAFE_CAST(decimals_int AS INT64), 0) AS decimals,
  IFNULL(proxy_standard, '') AS proxy_standard,
  IFNULL(LOWER(creator_address), '') AS creator_address,
  IFNULL(FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)), '') AS created_at
FROM %s
WHERE LOWER(address) IN UNNEST(@addrs)`, ethTokensLatestTable)
}

func tokenDailyBundleSQL() string {
	var inList []string
	var scaleCases []string
	var mintBurnAllowed []string
	for _, m := range majorTokenCatalog {
		inList = append(inList, fmt.Sprintf("'%s'", m.Address))
		scaleCases = append(scaleCases, fmt.Sprintf("WHEN '%s' THEN 1e%d", m.Address, m.Decimals))
		if m.MintBurnSupported {
			mintBurnAllowed = append(mintBurnAllowed, fmt.Sprintf("'%s'", m.Address))
		}
	}
	inClause := strings.Join(inList, ", ")
	caseClause := strings.Join(scaleCases, " ")
	mintBurnClause := strings.Join(mintBurnAllowed, ", ")

	return fmt.Sprintf(`
WITH filtered AS (
  SELECT
    FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS day,
    LOWER(token_address) AS token_address,
    LOWER(from_address) AS from_address,
    LOWER(to_address) AS to_address,
    value,
    CASE LOWER(token_address) %s ELSE 0 END AS scale
  FROM %s
  WHERE block_timestamp >= TIMESTAMP(@start_date)
    AND block_timestamp < TIMESTAMP(@end_date)
),
daily_all AS (
  SELECT
    day,
    COUNT(1) AS transfers,
    COUNTIF(value = '0') AS zero_value_transfers
  FROM filtered
  GROUP BY day
),
major_daily AS (
  SELECT
    day,
    token_address,
    COUNT(1) AS transfers,
    COUNTIF(value = '0') AS zero_value,
    APPROX_COUNT_DISTINCT(from_address) AS senders,
    APPROX_COUNT_DISTINCT(to_address) AS receivers,
    IFNULL(CAST(SUM(SAFE_CAST(value AS BIGNUMERIC) / scale) AS FLOAT64), 0) AS volume_token,
    IFNULL(CAST(SUM(IF(token_address IN (%s) AND from_address = '%s', SAFE_CAST(value AS BIGNUMERIC) / scale, 0)) AS FLOAT64), 0) AS minted_token,
    IFNULL(CAST(SUM(IF(token_address IN (%s) AND to_address = '%s', SAFE_CAST(value AS BIGNUMERIC) / scale, 0)) AS FLOAT64), 0) AS burned_token
  FROM filtered
  WHERE token_address IN (%s)
  GROUP BY day, token_address
)
SELECT
  'daily' AS row_type,
  day,
  '' AS token_address,
  transfers,
  zero_value_transfers AS zero_value,
  0 AS senders,
  0 AS receivers,
  0.0 AS volume_token,
  0.0 AS minted_token,
  0.0 AS burned_token
FROM daily_all
UNION ALL
SELECT
  'major' AS row_type,
  day,
  token_address,
  transfers,
  zero_value,
  senders,
  receivers,
  volume_token,
  minted_token,
  burned_token
FROM major_daily
ORDER BY day ASC, row_type ASC, token_address ASC`,
		caseClause, ethTokenTransfersTable, mintBurnClause, riskZeroAddress, mintBurnClause, riskZeroAddress, inClause)
}

func (b *BQClient) GetTokenTopTransfers(ctx context.Context, start, end civil.Date) ([]TokenRow, error) {
	q := b.client.Query(tokenTopTransfersOnlySQL())
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoTokensMaxBytesBilled
	return collectRows[TokenRow](q, ctx)
}

type tokenMetadataQueryRow struct {
	Address        string `bigquery:"address"`
	Symbol         string `bigquery:"symbol"`
	Name           string `bigquery:"name"`
	TokenStandard  string `bigquery:"token_standard"`
	Decimals       int64  `bigquery:"decimals"`
	ProxyStandard  string `bigquery:"proxy_standard"`
	CreatorAddress string `bigquery:"creator_address"`
	CreatedAt      string `bigquery:"created_at"`
}

func (b *BQClient) GetTokenMetadataBatch(ctx context.Context, addrs []string) ([]tokenContractMeta, error) {
	if len(addrs) == 0 {
		return nil, nil
	}
	q := b.client.Query(tokenMetadataBatchSQL())
	q.Parameters = []bigquery.QueryParameter{{Name: "addrs", Value: addrs}}
	q.MaxBytesBilled = cryptoTokenMetadataMaxBytesBilled
	rows, err := collectRows[tokenMetadataQueryRow](q, ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tokenContractMeta, len(rows))
	for i, r := range rows {
		out[i] = tokenContractMeta{
			Address:        r.Address,
			Symbol:         r.Symbol,
			Name:           r.Name,
			TokenStandard:  r.TokenStandard,
			Decimals:       int(r.Decimals),
			ProxyStandard:  r.ProxyStandard,
			CreatorAddress: r.CreatorAddress,
			CreatedAt:      r.CreatedAt,
			Found:          true,
		}
	}
	return out, nil
}

type tokenBundleQueryRow struct {
	RowType      string  `bigquery:"row_type"`
	Day          string  `bigquery:"day"`
	TokenAddress string  `bigquery:"token_address"`
	Transfers    int64   `bigquery:"transfers"`
	ZeroValue    int64   `bigquery:"zero_value"`
	Senders      int64   `bigquery:"senders"`
	Receivers    int64   `bigquery:"receivers"`
	VolumeToken  float64 `bigquery:"volume_token"`
	MintedToken  float64 `bigquery:"minted_token"`
	BurnedToken  float64 `bigquery:"burned_token"`
}

func (b *BQClient) GetTokenDailyBundle(ctx context.Context, start, end civil.Date) ([]tokenEconomyDailyRow, []tokenMajorDailyRow, error) {
	q := b.client.Query(tokenDailyBundleSQL())
	q.Parameters = cryptoDateParams(start, end)
	q.MaxBytesBilled = cryptoTokensMaxBytesBilled
	rows, err := collectRows[tokenBundleQueryRow](q, ctx)
	if err != nil {
		return nil, nil, err
	}
	var (
		daily []tokenEconomyDailyRow
		major []tokenMajorDailyRow
	)
	for _, r := range rows {
		if r.RowType == "daily" {
			daily = append(daily, tokenEconomyDailyRow{
				Day:                r.Day,
				Transfers:          r.Transfers,
				ZeroValueTransfers: r.ZeroValue,
			})
		} else if r.RowType == "major" {
			major = append(major, tokenMajorDailyRow{
				Day:          r.Day,
				TokenAddress: r.TokenAddress,
				Transfers:    r.Transfers,
				ZeroValue:    r.ZeroValue,
				Senders:      r.Senders,
				Receivers:    r.Receivers,
				VolumeToken:  r.VolumeToken,
				MintedToken:  r.MintedToken,
				BurnedToken:  r.BurnedToken,
			})
		}
	}
	return daily, major, nil
}

// ethNativeTxsSQL reads daily Ethereum transaction counts from blocks.transaction_count
// (10.6 MiB for 30d with @start_block pruning, vs 3.37 GiB for scanning transactions).
func ethNativeTxsSQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(timestamp)) AS date,
			SUM(transaction_count) AS tx_count,
			0.0 AS value_settled,
			0.0 AS fees_total
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethBlocksTable, ethBlockWindow)
}

func (b *BQClient) GetEthNativeTxs(ctx context.Context, start, end civil.Date) ([]CryptoActivityRow, error) {
	q := b.client.Query(ethNativeTxsSQL())
	q.Parameters = cryptoEthBlockParams(start, end)
	q.MaxBytesBilled = cryptoBlocksMaxBytesBilled
	return collectRows[CryptoActivityRow](q, ctx)
}

// ContractRow is one day's contract deployments with ERC flags.
type ContractRow struct {
	Date      string `json:"date" bigquery:"date"`
	Contracts int64  `json:"contracts" bigquery:"contracts"`
	Erc20     int64  `json:"erc20" bigquery:"erc20"`
	Erc721    int64  `json:"erc721" bigquery:"erc721"`
}

func contractsDailySQL() string {
	return fmt.Sprintf(`
		SELECT
			FORMAT_DATE('%%Y-%%m-%%d', DATE(block_timestamp)) AS date,
			COUNT(*) AS contracts,
			COUNTIF(is_erc20) AS erc20,
			COUNTIF(is_erc721) AS erc721
		FROM %s
		WHERE %s
		GROUP BY date ORDER BY date`, ethContractsTable, ethContractWindow)
}

func (b *BQClient) GetContractsDaily(ctx context.Context, start, end civil.Date) ([]ContractRow, error) {
	q := b.client.Query(contractsDailySQL())
	q.Parameters = cryptoEthBlockParams(start, end)
	q.MaxBytesBilled = cryptoBlocksMaxBytesBilled
	return collectRows[ContractRow](q, ctx)
}
