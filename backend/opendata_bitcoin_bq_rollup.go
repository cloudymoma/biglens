package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	btcInversionSlack        = 6 * time.Hour
	btcInversionSlackHours   = 6
	btcInscriptionWBPerInput = 150

	btcRollupMinDailyTxs    int64 = 100_000
	btcRollupMinDailyInputs int64 = 100_000
	btcRollupSchemaVersion        = 1
)

func btcRollupSQL() string {
	return `WITH tx AS (
  SELECT
    DATE(block_timestamp) AS d, is_coinbase, size, virtual_size, input_count, output_count, fee,
    (SELECT COUNTIF(i.type = 'witness_v1_taproot') FROM UNNEST(inputs) i) AS tr_in,
    ARRAY(SELECT AS STRUCT o.type, o.value, DIV(LENGTH(o.script_hex), 2) AS script_bytes,
                           SUBSTR(o.script_hex, 1, 4) AS pfx
          FROM UNNEST(outputs) o) AS outs,
    ARRAY(SELECT i.type FROM UNNEST(inputs) i) AS in_types,
    ARRAY_CONCAT(ARRAY(SELECT a FROM UNNEST(inputs) i, UNNEST(i.addresses) a),
                 ARRAY(SELECT a FROM UNNEST(outputs) o, UNNEST(o.addresses) a)) AS addrs
  FROM ` + "`bigquery-public-data.crypto_bitcoin.transactions`" + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
), cls AS (
  SELECT *,
    4 * (size - virtual_size) / 3 AS witness_bytes,
    CASE
      WHEN is_coinbase THEN 'coinbase'
      WHEN EXISTS (SELECT 1 FROM UNNEST(outs) o WHERE o.type = 'nulldata' AND o.pfx = '6a5d') THEN 'runes'
      WHEN EXISTS (SELECT 1 FROM UNNEST(outs) o WHERE o.type = 'nulldata') THEN 'op_return'
      WHEN tr_in = input_count AND 4 * (size - virtual_size) / 3 / input_count >= @inscription_wb_per_input THEN 'inscription_like'
      WHEN input_count >= 5 AND output_count = 1 THEN 'consolidation'
      WHEN output_count >= 10 THEN 'batch'
      ELSE 'payment'
    END AS class
  FROM tx
), agg AS (
  SELECT d,
    COUNTIF(NOT is_coinbase) AS txs,
    COUNTIF(is_coinbase) AS coinbase_txs,
    SUM(virtual_size) AS vbytes, SUM(size) AS bytes, SUM(witness_bytes) AS witness_bytes,
    APPROX_QUANTILES(IF(is_coinbase, NULL, CAST(fee AS FLOAT64) / NULLIF(virtual_size, 0)), 100) AS fee_q,
    CAST(SUM(IF(is_coinbase, 0, fee)) AS INT64) AS fees_sats,
    CAST(SUM(IF(is_coinbase, (SELECT SUM(o.value) FROM UNNEST(outs) o), 0)) AS INT64) AS coinbase_sats,
    CAST(SUM((SELECT SUM(IF(o.type != 'nulldata', o.value, 0)) FROM UNNEST(outs) o)) AS INT64) AS created_sats,
    CAST(SUM((SELECT SUM(IF(o.type = 'nulldata', o.value, 0)) FROM UNNEST(outs) o)) AS INT64) AS opret_value_sats,
    SUM(IF(class = 'payment', virtual_size, 0))          AS vb_payment,
    SUM(IF(class = 'consolidation', virtual_size, 0))    AS vb_consolidation,
    SUM(IF(class = 'batch', virtual_size, 0))            AS vb_batch,
    SUM(IF(class = 'inscription_like', virtual_size, 0)) AS vb_inscription_like,
    SUM(IF(class = 'runes', virtual_size, 0))            AS vb_runes,
    SUM(IF(class = 'op_return', virtual_size, 0))        AS vb_op_return,
    SUM(IF(class = 'coinbase', virtual_size, 0))         AS vb_coinbase,
    SUM((SELECT COUNTIF(o.type = 'nulldata') FROM UNNEST(outs) o))                         AS opret_outputs,
    SUM((SELECT SUM(IF(o.type = 'nulldata', o.script_bytes, 0)) FROM UNNEST(outs) o))      AS opret_bytes,
    SUM((SELECT COUNTIF(o.type = 'nulldata' AND o.pfx = '6a5d') FROM UNNEST(outs) o))      AS runestones,
    SUM((SELECT COUNTIF(o.type = 'nulldata' AND o.script_bytes > 83) FROM UNNEST(outs) o)) AS opret_large,
    SUM((SELECT COUNTIF(o.type = 'witness_v1_taproot') FROM UNNEST(outs) o))    AS out_p2tr,
    SUM((SELECT COUNTIF(o.type = 'witness_v0_keyhash') FROM UNNEST(outs) o))    AS out_p2wpkh,
    SUM((SELECT COUNTIF(o.type = 'witness_v0_scripthash') FROM UNNEST(outs) o)) AS out_p2wsh,
    SUM((SELECT COUNTIF(o.type = 'scripthash') FROM UNNEST(outs) o))            AS out_p2sh,
    SUM((SELECT COUNTIF(o.type = 'pubkeyhash') FROM UNNEST(outs) o))            AS out_p2pkh,
    SUM((SELECT COUNTIF(o.type = 'anchor') FROM UNNEST(outs) o))                AS out_anchor,
    SUM(ARRAY_LENGTH(outs))                                                      AS out_all,
    SUM((SELECT COUNTIF(t = 'witness_v1_taproot') FROM UNNEST(in_types) t))     AS in_p2tr,
    SUM((SELECT COUNTIF(t = 'witness_v0_keyhash') FROM UNNEST(in_types) t))     AS in_p2wpkh,
    SUM((SELECT COUNTIF(t = 'witness_v0_scripthash') FROM UNNEST(in_types) t))  AS in_p2wsh,
    SUM((SELECT COUNTIF(t = 'scripthash') FROM UNNEST(in_types) t))             AS in_p2sh,
    SUM((SELECT COUNTIF(t = 'pubkeyhash') FROM UNNEST(in_types) t))             AS in_p2pkh,
    SUM((SELECT COUNTIF(t = 'anchor') FROM UNNEST(in_types) t))                 AS in_anchor,
    SUM((SELECT COUNTIF(t IS NOT NULL) FROM UNNEST(in_types) t))                AS in_all,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value < 100000) FROM UNNEST(outs) o)))                               AS band_lt_0_001,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 100000 AND o.value < 1000000) FROM UNNEST(outs) o)))         AS band_0_001_0_01,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 1000000 AND o.value < 10000000) FROM UNNEST(outs) o)))       AS band_0_01_0_1,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 10000000 AND o.value < 100000000) FROM UNNEST(outs) o)))     AS band_0_1_1,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 100000000 AND o.value < 1000000000) FROM UNNEST(outs) o)))   AS band_1_10,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 1000000000 AND o.value < 10000000000) FROM UNNEST(outs) o))) AS band_10_100,
    SUM(IF(is_coinbase, 0, (SELECT COUNTIF(o.type NOT IN ('nulldata', 'anchor') AND o.value >= 10000000000) FROM UNNEST(outs) o)))                         AS band_ge_100
  FROM cls GROUP BY d
), act AS (
  SELECT d, APPROX_COUNT_DISTINCT(a) AS active_addresses FROM tx, UNNEST(tx.addrs) AS a GROUP BY d
)
SELECT FORMAT_DATE('%Y-%m-%d', d) AS date, agg.* EXCEPT (d), act.active_addresses
FROM agg LEFT JOIN act USING (d)
ORDER BY date`
}

func btcBlocksSQL() string {
	return `SELECT
  number AS height,
  FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', timestamp) AS ts,
  FORMAT_DATE('%Y-%m-%d', DATE(timestamp)) AS date,
  bits,
  transaction_count,
  weight,
  IF(DIV(number, 210000) >= 64, 0, 5000000000 >> DIV(number, 210000)) AS subsidy_sats
FROM ` + "`bigquery-public-data.crypto_bitcoin.blocks`" + `
WHERE timestamp >= TIMESTAMP_SUB(TIMESTAMP(@start_date), INTERVAL @slack_hours HOUR)
  AND timestamp <  TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL @slack_hours HOUR)
ORDER BY height`
}

type btcBlockRow struct {
	Height           int64  `bigquery:"height" json:"height"`
	TS               string `bigquery:"ts" json:"ts"`
	Date             string `bigquery:"date" json:"date"`
	Bits             string `bigquery:"bits" json:"bits"`
	TransactionCount int64  `bigquery:"transaction_count" json:"transaction_count"`
	Weight           int64  `bigquery:"weight" json:"weight"`
	SubsidySats      int64  `bigquery:"subsidy_sats" json:"subsidy_sats"`
}

type btcRollupRawRow struct {
	Date              string    `bigquery:"date"`
	Txs               int64     `bigquery:"txs"`
	CoinbaseTxs       int64     `bigquery:"coinbase_txs"`
	Vbytes            int64     `bigquery:"vbytes"`
	Bytes             int64     `bigquery:"bytes"`
	WitnessBytes      int64     `bigquery:"witness_bytes"`
	FeeQ              []float64 `bigquery:"fee_q"`
	FeesSats          int64     `bigquery:"fees_sats"`
	CoinbaseSats      int64     `bigquery:"coinbase_sats"`
	CreatedSats       int64     `bigquery:"created_sats"`
	OpretValueSats    int64     `bigquery:"opret_value_sats"`
	VbPayment         int64     `bigquery:"vb_payment"`
	VbConsolidation   int64     `bigquery:"vb_consolidation"`
	VbBatch           int64     `bigquery:"vb_batch"`
	VbInscriptionLike int64     `bigquery:"vb_inscription_like"`
	VbRunes           int64     `bigquery:"vb_runes"`
	VbOpReturn        int64     `bigquery:"vb_op_return"`
	VbCoinbase        int64     `bigquery:"vb_coinbase"`
	OpretOutputs      int64     `bigquery:"opret_outputs"`
	OpretBytes        int64     `bigquery:"opret_bytes"`
	Runestones        int64     `bigquery:"runestones"`
	OpretLarge        int64     `bigquery:"opret_large"`
	OutP2TR           int64     `bigquery:"out_p2tr"`
	OutP2WPKH         int64     `bigquery:"out_p2wpkh"`
	OutP2WSH          int64     `bigquery:"out_p2wsh"`
	OutP2SH           int64     `bigquery:"out_p2sh"`
	OutP2PKH          int64     `bigquery:"out_p2pkh"`
	OutAnchor         int64     `bigquery:"out_anchor"`
	OutAll            int64     `bigquery:"out_all"`
	InP2TR            int64     `bigquery:"in_p2tr"`
	InP2WPKH          int64     `bigquery:"in_p2wpkh"`
	InP2WSH           int64     `bigquery:"in_p2wsh"`
	InP2SH            int64     `bigquery:"in_p2sh"`
	InP2PKH           int64     `bigquery:"in_p2pkh"`
	InAnchor          int64     `bigquery:"in_anchor"`
	InAll             int64     `bigquery:"in_all"`
	BandLt0001        int64     `bigquery:"band_lt_0_001"`
	Band0001To001     int64     `bigquery:"band_0_001_0_01"`
	Band001To01       int64     `bigquery:"band_0_01_0_1"`
	Band01To1         int64     `bigquery:"band_0_1_1"`
	Band1To10         int64     `bigquery:"band_1_10"`
	Band10To100       int64     `bigquery:"band_10_100"`
	BandGe100         int64     `bigquery:"band_ge_100"`
	ActiveAddresses   int64     `bigquery:"active_addresses"`
}

type btcRollupPayload struct {
	CoverageStart int64
	Blocks        []btcBlockRow
	Days          []BtcOnchainDay
}

// btcRollupQuerySource abstracts the BigQuery calls so unit tests can exercise
// A2-first contiguity failure, slack readiness, and per-day guards without BQ.
type btcRollupQuerySource interface {
	ReadyAt(ctx context.Context, at time.Time) (bool, error)
	DryRunBlocks(ctx context.Context, start, end civil.Date) (int64, error)
	DryRunRollup(ctx context.Context, start, end civil.Date) (int64, error)
	FetchBlocks(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcBlockRow, int64, error)
	FetchRollup(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcRollupRawRow, int64, error)
}

type bqRollupSource struct {
	bq *BQClient
}

func (s bqRollupSource) ReadyAt(ctx context.Context, at time.Time) (bool, error) {
	if s.bq == nil || s.bq.client == nil {
		return false, fmt.Errorf("nil bigquery client")
	}
	ts, err := s.bq.cryptoLatestRowTimeAt(ctx, "btc", at)
	if err != nil {
		return false, err
	}
	return cryptoTimestampComplete(ts, at), nil
}

func (s bqRollupSource) blocksParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
		{Name: "slack_hours", Value: int64(btcInversionSlackHours)},
	}
}

func (s bqRollupSource) rollupParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
		{Name: "inscription_wb_per_input", Value: int64(btcInscriptionWBPerInput)},
	}
}

func (s bqRollupSource) DryRunBlocks(ctx context.Context, start, end civil.Date) (int64, error) {
	return btcDryRunQuery(ctx, s.bq.client, btcBlocksSQL(), btcOnchainJobID, s.blocksParams(start, end))
}

func (s bqRollupSource) DryRunRollup(ctx context.Context, start, end civil.Date) (int64, error) {
	return btcDryRunQuery(ctx, s.bq.client, btcRollupSQL(), btcOnchainJobID, s.rollupParams(start, end))
}

func (s bqRollupSource) FetchBlocks(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcBlockRow, int64, error) {
	return btcRunQuery[btcBlockRow](ctx, s.bq.client, btcBlocksSQL(), btcOnchainJobID, s.blocksParams(start, end), maxBytes)
}

func (s bqRollupSource) FetchRollup(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcRollupRawRow, int64, error) {
	return btcRunQuery[btcRollupRawRow](ctx, s.bq.client, btcRollupSQL(), btcOnchainJobID, s.rollupParams(start, end), maxBytes)
}

type btcOnchainJob struct {
	store     *riskStore
	src       btcRollupQuerySource
	minTxs    int64
	minInputs int64
}

func newBtcOnchainJob(store *riskStore, bq *BQClient) *btcOnchainJob {
	return &btcOnchainJob{
		store:     store,
		src:       bqRollupSource{bq: bq},
		minTxs:    btcRollupMinDailyTxs,
		minInputs: btcRollupMinDailyInputs,
	}
}

func (j *btcOnchainJob) ID() string { return btcOnchainJobID }

// Ready probes transactions at end + 6h so forward-dated blocks straddling
// midnight cannot cause Ready to return true before earlier in-window blocks
// have been ingested.
func (j *btcOnchainJob) Ready(ctx context.Context, end civil.Date) (bool, error) {
	probeAt := dayStart(end).Add(btcInversionSlack)
	return j.src.ReadyAt(ctx, probeAt)
}

func (j *btcOnchainJob) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	b1, err := j.src.DryRunBlocks(ctx, start, end)
	if err != nil {
		return 0, err
	}
	b2, err := j.src.DryRunRollup(ctx, start, end)
	if err != nil {
		return 0, err
	}
	return b1 + b2, nil
}

// Run executes A2 (padded blocks) first, verifies exact height contiguity over
// [coverage_start, h_max] before billing A1, then executes A1 and runs all
// per-day guards.
func (j *btcOnchainJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	a2Rows, a2Billed, err := j.src.FetchBlocks(ctx, start, end, maxBytes)
	if err != nil {
		return dailyResult{}, a2Billed, err
	}

	var storedCov *int64
	var existing map[int64]btcBlockRow
	if j.store != nil {
		if cov, ok, cErr := j.store.getBtcBlocksCoverageStart(ctx); cErr != nil {
			return dailyResult{}, a2Billed, cErr
		} else if ok {
			storedCov = &cov
		}
		minH, maxH := btcBlockHeightBounds(a2Rows, storedCov)
		if maxH >= minH {
			existing, err = j.store.loadBtcBlocksRange(ctx, minH, maxH)
			if err != nil {
				return dailyResult{}, a2Billed, err
			}
		}
	}

	covStart, err := validateBtcBlocksA2(start, end, a2Rows, existing, storedCov)
	if err != nil {
		return dailyResult{}, a2Billed, err
	}

	remCap := maxBytes - a2Billed
	if maxBytes <= 0 {
		remCap = 1 << 30
	} else if remCap <= 0 {
		return dailyResult{}, a2Billed, errBtcOverBudget
	}
	a1Rows, a1Billed, err := j.src.FetchRollup(ctx, start, end, remCap)
	totalBilled := a2Billed + a1Billed
	if err != nil {
		return dailyResult{}, totalBilled, err
	}

	minTxs := j.minTxs
	if minTxs <= 0 {
		minTxs = btcRollupMinDailyTxs
	}
	minInputs := j.minInputs
	if minInputs <= 0 {
		minInputs = btcRollupMinDailyInputs
	}
	days, err := buildBtcOnchainDays(start, end, a2Rows, a1Rows, minTxs, minInputs)
	if err != nil {
		return dailyResult{}, totalBilled, err
	}

	return dailyResult{
		Rows: len(days),
		Payload: btcRollupPayload{
			CoverageStart: covStart,
			Blocks:        a2Rows,
			Days:          days,
		},
	}, totalBilled, nil
}

func (j *btcOnchainJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	payload, ok := r.Payload.(btcRollupPayload)
	if !ok {
		return fmt.Errorf("btc_onchain: unexpected payload %T", r.Payload)
	}
	return s.applyBtcRollupBatch(ctx, payload, newFrom, cursor, now)
}

func btcBlockHeightBounds(rows []btcBlockRow, storedCov *int64) (int64, int64) {
	if len(rows) == 0 {
		return 0, -1
	}
	minH := rows[0].Height
	maxH := rows[0].Height
	for _, r := range rows[1:] {
		if r.Height < minH {
			minH = r.Height
		}
		if r.Height > maxH {
			maxH = r.Height
		}
	}
	if storedCov != nil && *storedCov < minH {
		minH = *storedCov
	}
	return minH, maxH
}

// validateBtcBlocksA2 checks duplicate heights and exact contiguity over
// [coverage_start, h_max] across existing btc_blocks unioned with a2Rows, and
// logs a warning if any timestamp inversion spans > 12 heights or > 1 hour.
func validateBtcBlocksA2(start, end civil.Date, a2Rows []btcBlockRow, existing map[int64]btcBlockRow, storedCov *int64) (int64, error) {
	startStr := start.String()
	endStr := end.String()
	if len(a2Rows) == 0 {
		return 0, suspiciousEmpty(startStr, "blocks scanned in A2")
	}

	seenInA2 := make(map[int64]btcBlockRow, len(a2Rows))
	unionHeights := make(map[int64]bool, len(existing)+len(a2Rows))
	for h := range existing {
		unionHeights[h] = true
	}

	var (
		hasInWin  bool
		hMinInWin int64
		hMaxInWin int64
	)
	for _, r := range a2Rows {
		if _, dup := seenInA2[r.Height]; dup {
			return 0, &suspiciousResultError{
				msg: fmt.Sprintf("suspicious duplicate block height %d in A2", r.Height),
			}
		}
		if prev, ok := existing[r.Height]; ok {
			if prev.Date != r.Date || prev.Bits != r.Bits || prev.SubsidySats != r.SubsidySats {
				return 0, &suspiciousResultError{
					msg: fmt.Sprintf("suspicious conflicting block metadata at height %d", r.Height),
				}
			}
		}
		seenInA2[r.Height] = r
		unionHeights[r.Height] = true

		if r.Date >= startStr && r.Date < endStr {
			if !hasInWin {
				hasInWin = true
				hMinInWin = r.Height
				hMaxInWin = r.Height
			} else {
				if r.Height < hMinInWin {
					hMinInWin = r.Height
				}
				if r.Height > hMaxInWin {
					hMaxInWin = r.Height
				}
			}
		}
	}
	if !hasInWin {
		return 0, suspiciousEmpty(startStr, "in-window blocks in A2")
	}

	covStart := hMinInWin
	if storedCov != nil {
		covStart = *storedCov
	}
	for h := covStart; h <= hMaxInWin; h++ {
		if !unionHeights[h] {
			return 0, &suspiciousResultError{
				msg: fmt.Sprintf("suspicious block contiguity_hole at height %d in [%d, %d]", h, covStart, hMaxInWin),
			}
		}
	}

	warnLargeTimestampInversions(a2Rows)
	return covStart, nil
}

func warnLargeTimestampInversions(a2Rows []btcBlockRow) {
	sorted := make([]btcBlockRow, len(a2Rows))
	copy(sorted, a2Rows)
	slices.SortFunc(sorted, func(a, b btcBlockRow) int {
		if a.Height < b.Height {
			return -1
		}
		if a.Height > b.Height {
			return 1
		}
		return 0
	})
	var (
		maxTS       time.Time
		maxTSHeight int64
	)
	for _, b := range sorted {
		t, err := time.Parse(time.RFC3339, b.TS)
		if err != nil {
			continue
		}
		if !maxTS.IsZero() && t.Before(maxTS) {
			hSpan := b.Height - maxTSHeight
			tSpan := maxTS.Sub(t)
			if hSpan > 12 || tSpan > time.Hour {
				slog.Warn("btc_onchain: large block timestamp inversion",
					"height", b.Height,
					"earlier_than_height", maxTSHeight,
					"height_span", hSpan,
					"time_span", tSpan.String(),
				)
			}
		} else if maxTS.IsZero() || t.After(maxTS) {
			maxTS = t
			maxTSHeight = b.Height
		}
	}
}

func buildBtcOnchainDays(start, end civil.Date, a2Rows []btcBlockRow, a1Rows []btcRollupRawRow, minTxs, minInputs int64) ([]BtcOnchainDay, error) {
	startStr := start.String()
	endStr := end.String()

	blocksByDay := make(map[string][]btcBlockRow)
	for _, b := range a2Rows {
		if b.Date >= startStr && b.Date < endStr {
			blocksByDay[b.Date] = append(blocksByDay[b.Date], b)
		}
	}
	a1ByDay := make(map[string]btcRollupRawRow, len(a1Rows))
	for _, r := range a1Rows {
		a1ByDay[r.Date] = r
	}

	var out []BtcOnchainDay
	for cur := start; cur.Before(end); cur = cur.AddDays(1) {
		d := cur.String()
		bRows := blocksByDay[d]
		if len(bRows) == 0 {
			return nil, suspiciousEmpty(d, "blocks in A2")
		}
		a1, ok := a1ByDay[d]
		if !ok {
			return nil, suspiciousEmpty(d, "transactions in A1")
		}
		if a1.CoinbaseTxs != int64(len(bRows)) {
			return nil, &suspiciousResultError{
				msg: fmt.Sprintf("suspicious coinbase/block count mismatch for %s: coinbase_txs=%d blocks=%d", d, a1.CoinbaseTxs, len(bRows)),
			}
		}
		if a1.Txs < minTxs {
			return nil, suspiciousEmpty(d, "non-coinbase transactions")
		}
		if a1.InAll < minInputs {
			return nil, suspiciousEmpty(d, "non-coinbase transaction inputs")
		}
		if a1.OutP2TR < 1 {
			return nil, suspiciousEmpty(d, "out_p2tr taproot outputs")
		}
		if a1.OpretOutputs < 1 {
			return nil, suspiciousEmpty(d, "op_return outputs")
		}

		var (
			schedSats   int64
			emptyBlocks int64
			diffSum     float64
		)
		for _, b := range bRows {
			schedSats += b.SubsidySats
			if b.TransactionCount <= 1 {
				emptyBlocks++
			}
			diff, err := btcDifficultyFromBits(b.Bits)
			if err != nil {
				return nil, &suspiciousResultError{
					msg: fmt.Sprintf("suspicious block bits %q at height %d on %s", b.Bits, b.Height, d),
				}
			}
			diffSum += diff
		}
		blocks := int64(len(bRows))
		avgDiff := diffSum / float64(blocks)
		hashrate := impliedHashrateEHs(avgDiff, blocks)
		claimed := a1.CoinbaseSats - a1.FeesSats

		var witnessShare, feeShare float64
		if a1.Bytes > 0 {
			witnessShare = float64(a1.WitnessBytes) / float64(a1.Bytes) * 100.0
		}
		if a1.CoinbaseSats > 0 {
			feeShare = float64(a1.FeesSats) / float64(a1.CoinbaseSats) * 100.0
		}

		var p10, p25, p50, p75, p90 float64
		if len(a1.FeeQ) > 90 {
			p10 = a1.FeeQ[10]
			p25 = a1.FeeQ[25]
			p50 = a1.FeeQ[50]
			p75 = a1.FeeQ[75]
			p90 = a1.FeeQ[90]
		}

		out = append(out, BtcOnchainDay{
			Date:                  d,
			Blocks:                blocks,
			EmptyBlocks:           emptyBlocks,
			ScheduledIssuanceSats: schedSats,
			ClaimedIssuanceSats:   claimed,
			AvgDifficulty:         avgDiff,
			ImpliedHashrateEHs:    hashrate,
			Txs:                   a1.Txs,
			CoinbaseTxs:           a1.CoinbaseTxs,
			Vbytes:                a1.Vbytes,
			Bytes:                 a1.Bytes,
			WitnessBytes:          a1.WitnessBytes,
			WitnessSharePct:       witnessShare,
			FeesSats:              a1.FeesSats,
			CoinbaseSats:          a1.CoinbaseSats,
			CreatedSats:           a1.CreatedSats,
			OpretValueSats:        a1.OpretValueSats,
			FeeSharePct:           feeShare,
			FeeP10SatVB:           p10,
			FeeP25SatVB:           p25,
			FeeP50SatVB:           p50,
			FeeP75SatVB:           p75,
			FeeP90SatVB:           p90,
			VbPayment:             a1.VbPayment,
			VbConsolidation:       a1.VbConsolidation,
			VbBatch:               a1.VbBatch,
			VbInscriptionLike:     a1.VbInscriptionLike,
			VbRunes:               a1.VbRunes,
			VbOpReturn:            a1.VbOpReturn,
			VbCoinbase:            a1.VbCoinbase,
			OpretOutputs:          a1.OpretOutputs,
			OpretBytes:            a1.OpretBytes,
			Runestones:            a1.Runestones,
			OpretLarge:            a1.OpretLarge,
			OutP2TR:               a1.OutP2TR,
			OutP2WPKH:             a1.OutP2WPKH,
			OutP2WSH:              a1.OutP2WSH,
			OutP2SH:               a1.OutP2SH,
			OutP2PKH:              a1.OutP2PKH,
			OutAnchor:             a1.OutAnchor,
			OutAll:                a1.OutAll,
			InP2TR:                a1.InP2TR,
			InP2WPKH:              a1.InP2WPKH,
			InP2WSH:               a1.InP2WSH,
			InP2SH:                a1.InP2SH,
			InP2PKH:               a1.InP2PKH,
			InAnchor:              a1.InAnchor,
			InAll:                 a1.InAll,
			BandLt0001:            a1.BandLt0001,
			Band0001To001:         a1.Band0001To001,
			Band001To01:           a1.Band001To01,
			Band01To1:             a1.Band01To1,
			Band1To10:             a1.Band1To10,
			Band10To100:           a1.Band10To100,
			BandGe100:             a1.BandGe100,
			ActiveAddresses:       a1.ActiveAddresses,
		})
	}
	return out, nil
}
