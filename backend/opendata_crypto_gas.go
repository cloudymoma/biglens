package main

// BigQuery Open Data: Crypto Pulse 72h Gas Pulse (gas_fee_design.md §5).
//
// Seven chains share one hourly row shape, GasHourRow. Every 72h query is
// bounded by the half-open [@start_ts, @end_ts) window on its partition
// timestamp; BTC additionally prunes its MONTH partition column. Solana lives
// in us-central1 and must run with q.Location set. Column names are neutral
// (primary_val / band_low / band_high / load_val / total_fee); what they mean
// for each chain is declared once in gasChainMetas, and every query emits all
// seven columns (NULL where a chain has no such value).

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	gasChainBTC  = "btc"
	gasChainETH  = "eth"
	gasChainArb  = "arb"
	gasChainOP   = "op"
	gasChainPoly = "poly"
	gasChainTron = "tron"
	gasChainSol  = "sol"
)

// gasChainOrder is the display order of the chain selector.
var gasChainOrder = []string{gasChainBTC, gasChainETH, gasChainArb, gasChainOP, gasChainPoly, gasChainTron, gasChainSol}

const (
	arbBlocksTable    = "`bigquery-public-data.goog_blockchain_arbitrum_one_us.blocks`"
	opReceiptsTable   = "`bigquery-public-data.goog_blockchain_optimism_mainnet_us.receipts`"
	opBlocksTable     = "`bigquery-public-data.goog_blockchain_optimism_mainnet_us.blocks`"
	polyReceiptsTable = "`bigquery-public-data.goog_blockchain_polygon_mainnet_us.receipts`"
	polyBlocksTable   = "`bigquery-public-data.goog_blockchain_polygon_mainnet_us.blocks`"
	tronReceiptsTable = "`bigquery-public-data.goog_blockchain_tron_mainnet_us.receipts`"
	solBlocksTable    = "`bigquery-public-data.crypto_solana_mainnet_us.Blocks`"
	solLocation       = "us-central1"

	// cryptoDefaultMaxBytesBilled caps 72h Gas Pulse, all-time record delta,
	// and daily calibration queries (16 GiB).
	cryptoDefaultMaxBytesBilled int64 = 16 << 30
)

// WHERE fragments; tests assert their presence.
const (
	gasTsWindow       = `block_timestamp >= @start_ts AND block_timestamp < @end_ts`
	gasBtcTxWindow    = `block_timestamp_month >= DATE_TRUNC(DATE(@start_ts), MONTH) AND ` + gasTsWindow
	gasBtcBlockWindow = `timestamp_month >= DATE_TRUNC(DATE(@start_ts), MONTH)
		AND timestamp >= @start_ts AND timestamp < @end_ts`
)

// GasChainMeta tells the frontend how to label one chain's neutral columns.
// An empty BandLabel means the chain has no shaded band.
type GasChainMeta struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PrimaryLabel string `json:"primary_label"`
	PrimaryUnit  string `json:"primary_unit"`
	BandLabel    string `json:"band_label"`
	LoadLabel    string `json:"load_label"`
	LoadUnit     string `json:"load_unit"`
	FeeUnit      string `json:"fee_unit"`
	FeeLabel     string `json:"fee_label,omitempty"`
	Note         string `json:"note"`
}

var gasChainMetas = map[string]GasChainMeta{
	gasChainBTC: {ID: gasChainBTC, Name: "Bitcoin", PrimaryLabel: "Median fee rate", PrimaryUnit: "sat/vB",
		BandLabel: "P10–P90", LoadLabel: "Block weight utilization", LoadUnit: "%", FeeUnit: "BTC", FeeLabel: "72h total",
		Note: "Excludes coinbase transactions"},
	gasChainETH: {ID: gasChainETH, Name: "Ethereum", PrimaryLabel: "Median effective gas price", PrimaryUnit: "gwei",
		BandLabel: "P10–P90", LoadLabel: "Throughput", LoadUnit: "Mgas/s", FeeUnit: "ETH", FeeLabel: "72h total",
		Note: "Base fee + priority tip"},
	gasChainArb: {ID: gasChainArb, Name: "Arbitrum One", PrimaryLabel: "Avg base fee", PrimaryUnit: "gwei",
		BandLabel: "Min–Max", LoadLabel: "Throughput", LoadUnit: "Mgas/s", FeeUnit: "ETH", FeeLabel: "72h total",
		Note: "Band is the hour's min–max base fee; Arbitrum ignores tips"},
	gasChainOP: {ID: gasChainOP, Name: "Optimism", PrimaryLabel: "Median L2 execution gas price", PrimaryUnit: "gwei",
		BandLabel: "P10–P90", LoadLabel: "Throughput", LoadUnit: "Mgas/s", FeeUnit: "ETH", FeeLabel: "72h total",
		Note: "L2 execution fee only — excludes the L1 data fee"},
	gasChainPoly: {ID: gasChainPoly, Name: "Polygon PoS", PrimaryLabel: "Median effective gas price", PrimaryUnit: "gwei",
		BandLabel: "P10–P90", LoadLabel: "Throughput", LoadUnit: "Mgas/s", FeeUnit: "POL", FeeLabel: "72h total",
		Note: "Base fee + priority tip"},
	gasChainTron: {ID: gasChainTron, Name: "TRON", PrimaryLabel: "Energy consumed", PrimaryUnit: "Energy",
		BandLabel: "", LoadLabel: "Contract tx share", LoadUnit: "%", FeeUnit: "TRX", FeeLabel: "72h energy at burn price",
		Note: "Fee total is energy priced at the burn rate, not TRX actually burned"},
	gasChainSol: {ID: gasChainSol, Name: "Solana", PrimaryLabel: "Avg leader reward per tx", PrimaryUnit: "lamports",
		BandLabel: "", LoadLabel: "Transactions", LoadUnit: "tx/h", FeeUnit: "SOL", FeeLabel: "72h leader rewards",
		Note: "Includes validator vote transactions (~60%) · leader rewards = base fee burn (50%) + priority tips (100%)"},
}

// GasHourRow is one chain's hour; NULL columns marshal to JSON null.
type GasHourRow struct {
	HourUTC  string               `json:"hour_utc" bigquery:"hour_utc"`
	TxCount  bigquery.NullInt64   `json:"tx_count" bigquery:"tx_count"`
	BandLow  bigquery.NullFloat64 `json:"band_low" bigquery:"band_low"`
	Primary  bigquery.NullFloat64 `json:"primary_val" bigquery:"primary_val"`
	BandHigh bigquery.NullFloat64 `json:"band_high" bigquery:"band_high"`
	Load     bigquery.NullFloat64 `json:"load_val" bigquery:"load_val"`
	TotalFee bigquery.NullFloat64 `json:"total_fee" bigquery:"total_fee"`
}

// gasLoadRow is BTC's block-utilization hour, merged into GasHourRow.Load.
type gasLoadRow struct {
	HourUTC string               `bigquery:"hour_utc"`
	Load    bigquery.NullFloat64 `bigquery:"load_val"`
}

const gasHourCol = `FORMAT_TIMESTAMP('%%Y-%%m-%%d %%H:00', TIMESTAMP_TRUNC(%s, HOUR)) AS hour_utc`

// gasQuantileCols renders P10/P50/P90 of expr as band_low/primary_val/band_high.
func gasQuantileCols(expr string, digits int) string {
	return fmt.Sprintf(`ROUND(APPROX_QUANTILES(%[1]s, 100)[OFFSET(10)], %[2]d) AS band_low,
			ROUND(APPROX_QUANTILES(%[1]s, 100)[OFFSET(50)], %[2]d) AS primary_val,
			ROUND(APPROX_QUANTILES(%[1]s, 100)[OFFSET(90)], %[2]d) AS band_high`, expr, digits)
}

func gasHourlySQL(chain string) (string, error) {
	hour := fmt.Sprintf(gasHourCol, "block_timestamp")
	const ethPrice = "CAST(COALESCE(receipt_effective_gas_price, gas_price) AS FLOAT64)"
	// OP has one zero-priced L1-attributes tx per block; APPROX_QUANTILES
	// ignores NULLs, so only the quantiles skip them.
	const l2Price = "IF(effective_gas_price > 0, CAST(effective_gas_price AS FLOAT64) / 1e9, NULL)"
	const l2Receipts = `
		SELECT
			%s,
			COUNT(*) AS tx_count,
			%s,
			ROUND(SUM(CAST(gas_used AS FLOAT64)) / 3600.0 / 1e6, 3) AS load_val,
			ROUND(SUM(CAST(gas_used AS FLOAT64) * CAST(effective_gas_price AS FLOAT64)) / 1e18, 4) AS total_fee
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`

	switch chain {
	case gasChainBTC:
		return fmt.Sprintf(`
		SELECT
			%s,
			COUNT(*) AS tx_count,
			%s,
			CAST(NULL AS FLOAT64) AS load_val,
			ROUND(CAST(SUM(fee) AS FLOAT64) / 1e8, 4) AS total_fee
		FROM %s
		WHERE NOT is_coinbase AND %s
		GROUP BY hour_utc ORDER BY hour_utc`,
			hour, gasQuantileCols("SAFE_DIVIDE(CAST(fee AS FLOAT64), NULLIF(virtual_size, 0))", 2),
			btcTxTable, gasBtcTxWindow), nil
	case gasChainETH:
		return fmt.Sprintf(`
		SELECT
			%s,
			COUNT(*) AS tx_count,
			%s,
			ROUND(SUM(CAST(receipt_gas_used AS FLOAT64)) / 3600.0 / 1e6, 3) AS load_val,
			ROUND(SUM(receipt_gas_used * %s) / 1e18, 4) AS total_fee
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`,
			hour, gasQuantileCols(ethPrice+" / 1e9", 4), ethPrice, ethTxTable, gasTsWindow), nil
	case gasChainArb:
		// The blocks table has no transaction count; the band is min–max.
		return fmt.Sprintf(`
		SELECT
			%s,
			CAST(NULL AS INT64) AS tx_count,
			ROUND(MIN(CAST(base_fee_per_gas AS FLOAT64)) / 1e9, 4) AS band_low,
			ROUND(AVG(CAST(base_fee_per_gas AS FLOAT64)) / 1e9, 4) AS primary_val,
			ROUND(MAX(CAST(base_fee_per_gas AS FLOAT64)) / 1e9, 4) AS band_high,
			ROUND(SUM(CAST(gas_used AS FLOAT64)) / 3600.0 / 1e6, 3) AS load_val,
			ROUND(SUM(CAST(gas_used AS FLOAT64) * CAST(base_fee_per_gas AS FLOAT64)) / 1e18, 4) AS total_fee
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`, hour, arbBlocksTable, gasTsWindow), nil
	case gasChainOP:
		return fmt.Sprintf(l2Receipts, hour, gasQuantileCols(l2Price, 6), opReceiptsTable, gasTsWindow), nil
	case gasChainPoly:
		return fmt.Sprintf(l2Receipts, hour, gasQuantileCols(l2Price, 6), polyReceiptsTable, gasTsWindow), nil
	case gasChainTron:
		// total_fee is energy priced at the governance burn rate (100 sun/energy);
		// omitting effective_gas_price avoids scanning an extra INT64 column across ~25M rows.
		return fmt.Sprintf(`
		SELECT
			%s,
			COUNT(*) AS tx_count,
			CAST(NULL AS FLOAT64) AS band_low,
			ROUND(SUM(CAST(gas_used AS FLOAT64)), 0) AS primary_val,
			CAST(NULL AS FLOAT64) AS band_high,
			ROUND(SAFE_DIVIDE(COUNTIF(gas_used > 0), COUNT(*)) * 100, 2) AS load_val,
			ROUND(SUM(CAST(gas_used AS FLOAT64)) * 100.0 / 1e6, 2) AS total_fee
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`, hour, tronReceiptsTable, gasTsWindow), nil
	case gasChainSol:
		return fmt.Sprintf(`
		SELECT
			%s,
			SUM(transaction_count) AS tx_count,
			CAST(NULL AS FLOAT64) AS band_low,
			ROUND(SAFE_DIVIDE(CAST(SUM(leader_reward) AS FLOAT64), NULLIF(SUM(transaction_count), 0)), 1) AS primary_val,
			CAST(NULL AS FLOAT64) AS band_high,
			CAST(SUM(transaction_count) AS FLOAT64) AS load_val,
			ROUND(CAST(SUM(leader_reward) AS FLOAT64) / 1e9, 4) AS total_fee
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`, hour, solBlocksTable, gasTsWindow), nil
	}
	return "", fmt.Errorf("unknown gas chain %q", chain)
}

func btcGasLoadSQL() string {
	return fmt.Sprintf(`
		SELECT
			%s,
			ROUND(AVG(weight) / 4e6 * 100, 1) AS load_val
		FROM %s
		WHERE %s
		GROUP BY hour_utc ORDER BY hour_utc`, fmt.Sprintf(gasHourCol, "timestamp"), btcBlocksTable, gasBtcBlockWindow)
}

// mergeGasLoad copies BTC block utilization onto the fee rows by hour. An
// hour with blocks but no fee-paying transaction is kept with a NULL primary.
func mergeGasLoad(rows []GasHourRow, loads []gasLoadRow) []GasHourRow {
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		idx[r.HourUTC] = i
	}
	for _, l := range loads {
		if i, ok := idx[l.HourUTC]; ok {
			rows[i].Load = l.Load
			continue
		}
		rows = append(rows, GasHourRow{HourUTC: l.HourUTC, Load: l.Load})
	}
	return rows
}

func gasWindowParams(start, end time.Time) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_ts", Value: start},
		{Name: "end_ts", Value: end},
	}
}

func (b *BQClient) GetGasHourly(ctx context.Context, chain string, start, end time.Time) ([]GasHourRow, error) {
	sql, err := gasHourlySQL(chain)
	if err != nil {
		return nil, err
	}
	q := b.client.Query(sql)
	q.Parameters = gasWindowParams(start, end)
	q.MaxBytesBilled = cryptoDefaultMaxBytesBilled
	if chain == gasChainSol {
		q.Location = solLocation
	}
	rows, err := collectRows[GasHourRow](q, ctx)
	if err != nil {
		return nil, err
	}
	if chain != gasChainBTC {
		return rows, nil
	}
	lq := b.client.Query(btcGasLoadSQL())
	lq.Parameters = gasWindowParams(start, end)
	lq.MaxBytesBilled = cryptoDefaultMaxBytesBilled
	loads, err := collectRows[gasLoadRow](lq, ctx)
	if err != nil {
		return nil, err
	}
	return mergeGasLoad(rows, loads), nil
}

// --- All-time base-fee records (gas_fee_design.md §4) ---

// GasAllTime is one chain's all-time record card. Times are RFC3339 UTC;
// "" means genesis. CurrentValue is set only where a live price exists (TRON).
type GasAllTime struct {
	Unit         string   `json:"unit"`
	AthValue     float64  `json:"ath_value"`
	AthTime      string   `json:"ath_time"`
	AtlValue     float64  `json:"atl_value"`
	AtlTime      string   `json:"atl_time"`
	AtlIsFloor   bool     `json:"atl_is_floor"`
	CurrentValue *float64 `json:"current_value"`
	CurrentTime  string   `json:"current_time,omitempty"`
}

type gasFeePoint struct {
	Fee int64     `bigquery:"fee"`
	Ts  time.Time `bigquery:"ts"`
}

// gasRecordRow holds the first block at the maximum and the first two blocks
// at the minimum; two equal minimum fees mean the ATL is a protocol floor.
type gasRecordRow struct {
	Chain string        `bigquery:"chain"`
	Ath   gasFeePoint   `bigquery:"ath"`
	Atl   []gasFeePoint `bigquery:"atl"`
}

// gasAllTimeSeedTime is the watermark up to which gasAllTimeSeeds were
// pre-computed from the four public block tables, avoiding an 11.3 GB full
// history scan on cold start. Each refresh scans only blocks >= @since_ts.
var gasAllTimeSeedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func mustRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

var gasAllTimeSeeds = map[string]gasRecordRow{
	gasChainETH: {
		Chain: gasChainETH,
		Ath:   gasFeePoint{Fee: 8629051173688, Ts: mustRFC3339("2022-05-01T01:09:03Z")},
		Atl:   []gasFeePoint{{Fee: 8703865, Ts: mustRFC3339("2025-12-04T03:55:59Z")}, {Fee: 8800000, Ts: mustRFC3339("2025-12-04T03:56:11Z")}},
	},
	gasChainArb: {
		Chain: gasChainArb,
		Ath:   gasFeePoint{Fee: 41459759000, Ts: mustRFC3339("2025-10-10T21:20:47Z")},
		Atl:   []gasFeePoint{{Fee: 10000000, Ts: mustRFC3339("2024-03-19T07:31:10Z")}, {Fee: 10000000, Ts: mustRFC3339("2024-03-19T07:31:11Z")}},
	},
	gasChainOP: {
		Chain: gasChainOP,
		Ath:   gasFeePoint{Fee: 14732840665, Ts: mustRFC3339("2024-03-05T20:10:05Z")},
		Atl:   []gasFeePoint{{Fee: 1, Ts: mustRFC3339("2023-06-06T16:50:55Z")}, {Fee: 1, Ts: mustRFC3339("2023-06-06T16:50:57Z")}},
	},
	gasChainPoly: {
		Chain: gasChainPoly,
		Ath:   gasFeePoint{Fee: 6685884794976, Ts: mustRFC3339("2023-11-16T15:39:11Z")},
		Atl:   []gasFeePoint{{Fee: 30000000000, Ts: mustRFC3339("2022-01-17T06:05:06Z")}, {Fee: 30000000000, Ts: mustRFC3339("2022-01-17T06:05:08Z")}},
	},
}

var gasAllTimeState = struct {
	mu      sync.Mutex
	since   time.Time
	records map[string]gasRecordRow
}{
	since: gasAllTimeSeedTime,
}

// mergeGasRecordRow merges a delta window's ATH/ATL into cur, preserving
// deterministic earliest-timestamp tie-breaking and two-row floor detection.
func mergeGasRecordRow(cur, delta gasRecordRow) gasRecordRow {
	if cur.Chain == "" {
		return delta
	}
	if delta.Chain == "" {
		return cur
	}
	out := cur
	if delta.Ath.Fee > cur.Ath.Fee || (delta.Ath.Fee == cur.Ath.Fee && delta.Ath.Ts.Before(cur.Ath.Ts)) {
		out.Ath = delta.Ath
	}
	combined := make([]gasFeePoint, 0, len(cur.Atl)+len(delta.Atl))
	for _, p := range append(append([]gasFeePoint{}, cur.Atl...), delta.Atl...) {
		if p.Fee <= 0 {
			continue
		}
		dup := false
		for _, ex := range combined {
			if ex.Fee == p.Fee && ex.Ts.Equal(p.Ts) {
				dup = true
				break
			}
		}
		if !dup {
			combined = append(combined, p)
		}
	}
	sort.Slice(combined, func(i, j int) bool {
		if combined[i].Fee != combined[j].Fee {
			return combined[i].Fee < combined[j].Fee
		}
		return combined[i].Ts.Before(combined[j].Ts)
	})
	if len(combined) > 2 {
		combined = combined[:2]
	}
	out.Atl = combined
	return out
}

// gasAllTimeSQL scans base_fee_per_gas + timestamp of four block tables
// incrementally from @since_ts (with @eth_start_block cluster pruning on
// crypto_ethereum.blocks). ORDER BY (fee, ts) makes the "first reached" time
// deterministic despite ties.
func gasAllTimeSQL() string {
	return fmt.Sprintf(`
		WITH b AS (
			SELECT 'eth' AS chain, timestamp AS ts, base_fee_per_gas AS fee FROM %s WHERE number >= @eth_start_block AND timestamp >= @since_ts AND base_fee_per_gas > 0
			UNION ALL SELECT 'arb', block_timestamp, base_fee_per_gas FROM %s WHERE block_timestamp >= @since_ts AND base_fee_per_gas > 0
			UNION ALL SELECT 'op', block_timestamp, base_fee_per_gas FROM %s WHERE block_timestamp >= @since_ts AND base_fee_per_gas > 0
			UNION ALL SELECT 'poly', block_timestamp, base_fee_per_gas FROM %s WHERE block_timestamp >= @since_ts AND base_fee_per_gas > 0
		)
		SELECT
			chain,
			ARRAY_AGG(STRUCT(fee, ts) ORDER BY fee DESC, ts LIMIT 1)[OFFSET(0)] AS ath,
			ARRAY_AGG(STRUCT(fee, ts) ORDER BY fee, ts LIMIT 2) AS atl
		FROM b
		GROUP BY chain`, ethBlocksTable, arbBlocksTable, opBlocksTable, polyBlocksTable)
}

func (b *BQClient) GetGasAllTime(ctx context.Context) ([]gasRecordRow, error) {
	gasAllTimeState.mu.Lock()
	since := gasAllTimeState.since
	gasAllTimeState.mu.Unlock()

	q := b.client.Query(gasAllTimeSQL())
	q.Parameters = []bigquery.QueryParameter{
		{Name: "since_ts", Value: since},
		{Name: "eth_start_block", Value: ethStartBlockLB(civil.DateOf(since.UTC()))},
	}
	q.MaxBytesBilled = cryptoDefaultMaxBytesBilled
	rows, err := collectRows[gasRecordRow](q, ctx)
	if err != nil {
		return nil, err
	}

	gasAllTimeState.mu.Lock()
	defer gasAllTimeState.mu.Unlock()
	if gasAllTimeState.records == nil {
		gasAllTimeState.records = make(map[string]gasRecordRow, len(gasAllTimeSeeds))
		for k, v := range gasAllTimeSeeds {
			gasAllTimeState.records[k] = v
		}
	}
	for _, r := range rows {
		gasAllTimeState.records[r.Chain] = mergeGasRecordRow(gasAllTimeState.records[r.Chain], r)
	}
	if next := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour); next.After(gasAllTimeState.since) {
		gasAllTimeState.since = next
	}
	out := make([]gasRecordRow, 0, len(gasAllTimeState.records))
	for _, r := range gasAllTimeState.records {
		out = append(out, r)
	}
	return out, nil
}

func gasAllTimeFromRow(r gasRecordRow) GasAllTime {
	at := GasAllTime{
		Unit:     "gwei",
		AthValue: float64(r.Ath.Fee) / 1e9,
		AthTime:  r.Ath.Ts.UTC().Format(time.RFC3339),
	}
	if len(r.Atl) > 0 {
		at.AtlValue = float64(r.Atl[0].Fee) / 1e9
		at.AtlTime = r.Atl[0].Ts.UTC().Format(time.RFC3339)
		at.AtlIsFloor = len(r.Atl) > 1 && r.Atl[1].Fee == r.Atl[0].Fee
	}
	return at
}
