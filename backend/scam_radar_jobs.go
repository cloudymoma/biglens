package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

const (
	scamEthTokenTransfersTable = "`bigquery-public-data.goog_blockchain_ethereum_mainnet_us.token_transfers`"
	scamEthTokensTable         = "`bigquery-public-data.crypto_ethereum.tokens`"
	scamTronLogsTable          = "`bigquery-public-data.goog_blockchain_tron_mainnet_us.logs`"
	scamBTCInputsTable         = "`bigquery-public-data.crypto_bitcoin.inputs`"

	tronUSDTLogAddress = "0xa614f803b6fd780986a42c78ec9c7f77e6ded13c"

	ethTokenTransfersReadySQL = `
SELECT MAX(block_timestamp) AS t FROM ` + scamEthTokenTransfersTable + `
WHERE block_timestamp >= TIMESTAMP(@end_date) AND block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)`

	tronLogsReadySQL = `
SELECT MAX(block_timestamp) AS t FROM ` + scamTronLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@end_date) AND block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)`

	btcInputsReadySQL = `
SELECT MAX(block_timestamp) AS t FROM ` + scamBTCInputsTable + `
WHERE block_timestamp >= TIMESTAMP(@end_date) AND block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)`

	// R1: ETH zero-value address poisoning on official USDT/USDC (spec P2.2).
	scamEthPoisonSQL = `
WITH t AS (
  SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day,
         LOWER(from_address) AS src,
         LOWER(to_address) AS dst,
         quantity AS q
  FROM ` + scamEthTokenTransfersTable + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
    AND address IN ('` + usdtContract + `', '` + usdcContract + `')
    AND NOT removed
),
real AS (
  SELECT DISTINCT day, src, dst FROM t WHERE q != '0'
)
SELECT z.day AS day,
       z.dst AS lookalike,
       r.dst AS imitated,
       COUNT(*) AS hits,
       COUNT(DISTINCT z.src) AS victims
FROM t z
JOIN real r
  ON z.day = r.day AND z.src = r.src AND z.dst != r.dst
 AND SUBSTR(z.dst, 3, 4) = SUBSTR(r.dst, 3, 4)
 AND RIGHT(z.dst, 4) = RIGHT(r.dst, 4)
WHERE z.q = '0'
GROUP BY 1, 2, 3`

	// R2: ETH counterfeit USDT/USDC token contracts (spec P2.2).
	scamEthFakeTokenSQL = `
WITH tok AS (
  SELECT LOWER(address) AS address, ANY_VALUE(symbol) AS symbol
  FROM ` + scamEthTokensTable + `
  WHERE REGEXP_CONTAINS(UPPER(symbol), r'^(USDT|USDC|USD₮)[^A-Z0-9]?$')
  GROUP BY 1
)
SELECT FORMAT_DATE('%Y-%m-%d', DATE(tt.block_timestamp)) AS day,
       LOWER(tt.address) AS contract,
       tok.symbol AS symbol,
       COUNT(*) AS transfers,
       COUNT(DISTINCT tt.to_address) AS recipients
FROM ` + scamEthTokenTransfersTable + ` tt
JOIN tok ON tt.address = tok.address
WHERE tt.block_timestamp >= TIMESTAMP(@start_date) AND tt.block_timestamp < TIMESTAMP(@end_date)
  AND tt.address NOT IN ('` + usdtContract + `', '` + usdcContract + `')
  AND NOT tt.removed
GROUP BY 1, 2, 3`

	// R3: TRON USDT dust poisoning with in-BigQuery Base58 encoding and
	// 4+4 prefix/suffix matching (spec P2.1, P2.2).
	scamTronPoisonSQL = `
CREATE TEMP FUNCTION b58(h STRING) RETURNS STRING LANGUAGE js AS r"""
  const A = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
  let n = BigInt('0x' + h), s = '';
  while (n > 0n) { s = A[Number(n % 58n)] + s; n /= 58n; }
  for (let i = 0; i + 1 < h.length && h.slice(i, i + 2) === '00'; i += 2) { s = '1' + s; }
  return s;
""";
CREATE TEMP FUNCTION tron(h STRING) AS (
  b58(TO_HEX((SELECT CONCAT(b, SUBSTR(SHA256(SHA256(b)), 1, 4)) FROM (SELECT FROM_HEX(CONCAT('41', h)) AS b))))
);
WITH t AS (
  SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day,
         SUBSTR(topics[SAFE_OFFSET(1)], 27, 40) AS src,
         SUBSTR(topics[SAFE_OFFSET(2)], 27, 40) AS dst,
         LENGTH(LTRIM(SUBSTR(data, 3), '0')) <= 5 AS is_dust
  FROM ` + scamTronLogsTable + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
    AND address = '` + tronUSDTLogAddress + `'
    AND topics[SAFE_OFFSET(0)] = '` + erc20TransferTopic + `'
    AND NOT removed
),
real AS (
  SELECT DISTINCT day, src, dst FROM t WHERE NOT is_dust
),
cand AS (
  SELECT d.day, d.src AS victim, d.dst AS lookalike_hex, r.dst AS imitated_hex
  FROM t d
  JOIN real r ON d.day = r.day AND d.src = r.src AND d.dst != r.dst
  WHERE d.is_dust
  UNION ALL
  SELECT d.day, d.dst AS victim, d.src AS lookalike_hex, r.dst AS imitated_hex
  FROM t d
  JOIN real r ON d.day = r.day AND d.dst = r.src AND d.src != r.dst
  WHERE d.is_dust
),
day_cands AS (
  SELECT day, COUNT(*) AS candidates FROM cand GROUP BY day
),
uniq_addrs AS (
  SELECT hex, tron(hex) AS addr58
  FROM (
    SELECT DISTINCT lookalike_hex AS hex FROM cand
    UNION DISTINCT
    SELECT DISTINCT imitated_hex AS hex FROM cand
  )
),
matched AS (
  SELECT c.day,
         lk.addr58 AS lookalike58,
         im.addr58 AS imitated58,
         COUNT(*) AS hits,
         COUNT(DISTINCT c.victim) AS victims
  FROM cand c
  JOIN uniq_addrs lk ON c.lookalike_hex = lk.hex
  JOIN uniq_addrs im ON c.imitated_hex = im.hex
  WHERE lk.addr58 != im.addr58
    AND SUBSTR(lk.addr58, 2, 4) = SUBSTR(im.addr58, 2, 4)
    AND RIGHT(lk.addr58, 4) = RIGHT(im.addr58, 4)
  GROUP BY 1, 2, 3
)
SELECT dc.day AS day,
       IFNULL(m.lookalike58, '') AS lookalike58,
       IFNULL(m.imitated58, '') AS imitated58,
       IFNULL(m.hits, 0) AS hits,
       IFNULL(m.victims, 0) AS victims,
       dc.candidates AS candidates
FROM day_cands dc
LEFT JOIN matched m USING (day)`

	// R4: TRON USDT freeze / unfreeze / destroy logs (spec P2.2). Reuses the
	// topicUSDT* constants from address_risk_stablecoin.go; on TRON the target
	// address is indexed in topics[1] and destroy amount is in the first 32B of data.
	tronStablecoinSQL = `
SELECT transaction_hash AS tx_hash,
       log_index,
       block_number,
       FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS block_time,
       CASE topics[SAFE_OFFSET(0)]
         WHEN '` + topicUSDTAddedBlackList + `' THEN 'freeze'
         WHEN '` + topicUSDTDestroyedBlackFunds + `' THEN 'destroy'
         ELSE 'unfreeze'
       END AS action,
       SUBSTR(topics[SAFE_OFFSET(1)], 27, 40) AS address_hex20,
       IF(topics[SAFE_OFFSET(0)] = '` + topicUSDTDestroyedBlackFunds + `', SUBSTR(data, 3, 64), '') AS amount_hex
FROM ` + scamTronLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
  AND address = '` + tronUSDTLogAddress + `' AND NOT removed
  AND topics[SAFE_OFFSET(0)] IN ('` + topicUSDTAddedBlackList + `', '` + topicUSDTRemovedBlackList + `', '` + topicUSDTDestroyedBlackFunds + `')`

	// R5: BTC explicit RBF share per UTC day (spec P2.2).
	scamBTCRBFSQL = `
SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day,
       COUNT(DISTINCT transaction_hash) AS txs,
       COUNT(DISTINCT IF(sequence < 4294967294, transaction_hash, NULL)) AS rbf_signal_txs
FROM ` + scamBTCInputsTable + `
WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
GROUP BY 1
ORDER BY 1`
)

type ethPoisonRow struct {
	Day       string `bigquery:"day"`
	Lookalike string `bigquery:"lookalike"`
	Imitated  string `bigquery:"imitated"`
	Hits      int64  `bigquery:"hits"`
	Victims   int64  `bigquery:"victims"`
}

type ethFakeTokenRow struct {
	Day        string `bigquery:"day"`
	Contract   string `bigquery:"contract"`
	Symbol     string `bigquery:"symbol"`
	Transfers  int64  `bigquery:"transfers"`
	Recipients int64  `bigquery:"recipients"`
}

type tronPoisonRow struct {
	Day         string `bigquery:"day"`
	Lookalike58 string `bigquery:"lookalike58"`
	Imitated58  string `bigquery:"imitated58"`
	Hits        int64  `bigquery:"hits"`
	Victims     int64  `bigquery:"victims"`
	Candidates  int64  `bigquery:"candidates"`
}

type tronStablecoinRow struct {
	TxHash       string `bigquery:"tx_hash"`
	LogIndex     int64  `bigquery:"log_index"`
	BlockNumber  int64  `bigquery:"block_number"`
	BlockTime    string `bigquery:"block_time"`
	Action       string `bigquery:"action"`
	AddressHex20 string `bigquery:"address_hex20"`
	AmountHex    string `bigquery:"amount_hex"`
}

type btcRBFRow struct {
	Day          string `bigquery:"day"`
	Txs          int64  `bigquery:"txs"`
	RBFSignalTxs int64  `bigquery:"rbf_signal_txs"`
}

// decodeTronStablecoinRow validates one TRON USDT blacklist log row, converting
// the 20-byte hex address into TRON Base58Check and parsing hex destroy amounts.
func decodeTronStablecoinRow(r tronStablecoinRow) (stablecoinEvent, error) {
	if r.TxHash == "" {
		return stablecoinEvent{}, fmt.Errorf("tron row: empty tx_hash")
	}
	addr, err := tronHexToBase58(r.AddressHex20)
	if err != nil {
		return stablecoinEvent{}, fmt.Errorf("tron row %s:%d: bad address %q: %w", r.TxHash, r.LogIndex, r.AddressHex20, err)
	}
	amount := ""
	switch r.Action {
	case "freeze", "unfreeze":
	case "destroy":
		hexStr := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(r.AmountHex), "0x"), "0X")
		if hexStr == "" {
			return stablecoinEvent{}, fmt.Errorf("tron row %s:%d: empty destroy amount", r.TxHash, r.LogIndex)
		}
		n, ok := new(big.Int).SetString(hexStr, 16)
		if !ok {
			return stablecoinEvent{}, fmt.Errorf("tron row %s:%d: bad destroy amount %q", r.TxHash, r.LogIndex, r.AmountHex)
		}
		amount = n.String()
	default:
		return stablecoinEvent{}, fmt.Errorf("tron row %s:%d: bad action %q", r.TxHash, r.LogIndex, r.Action)
	}
	return stablecoinEvent{
		TxHash:      r.TxHash,
		LogIndex:    r.LogIndex,
		Token:       "USDT",
		Action:      r.Action,
		Address:     addr,
		Amount:      amount,
		BlockNumber: r.BlockNumber,
		BlockTime:   r.BlockTime,
	}, nil
}

type scamBQRunner interface {
	MaxBlockTime(ctx context.Context, checkSQL string, end civil.Date) (time.Time, error)
	DryRun(ctx context.Context, sql string, start, end civil.Date) (int64, error)
	Query(ctx context.Context, sql string, start, end civil.Date, maxBytes int64, scan func(next func(dst any) bool) error) (int64, error)
}

type bqScamRunner struct{ client *bigquery.Client }

func (b bqScamRunner) MaxBlockTime(ctx context.Context, checkSQL string, end civil.Date) (time.Time, error) {
	q := b.client.Query(checkSQL)
	q.Parameters = []bigquery.QueryParameter{{Name: "end_date", Value: end}}
	q.MaxBytesBilled = 256 << 20
	rows, err := collectRows[blockMaxRow](q, ctx)
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) == 0 || !rows[0].T.Valid {
		return time.Time{}, nil
	}
	return rows[0].T.Timestamp, nil
}

func (b bqScamRunner) DryRun(ctx context.Context, sql string, start, end civil.Date) (int64, error) {
	q := b.client.Query(sql)
	q.Parameters = windowParams(start, end)
	q.DryRun = true
	job, err := q.Run(ctx)
	if err != nil {
		return 0, err
	}
	return job.LastStatus().Statistics.TotalBytesProcessed, nil
}

func (b bqScamRunner) Query(ctx context.Context, sql string, start, end civil.Date, maxBytes int64, scan func(next func(dst any) bool) error) (int64, error) {
	q := b.client.Query(sql)
	q.Parameters = windowParams(start, end)
	q.MaxBytesBilled = maxBytes
	job, err := q.Run(ctx)
	if err != nil {
		return 0, err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = job.Cancel(cctx)
		var billed int64
		if st, sErr := job.Status(cctx); sErr == nil && st != nil && st.Statistics != nil {
			if qs, ok := st.Statistics.Details.(*bigquery.QueryStatistics); ok {
				billed = qs.TotalBytesBilled
			}
		}
		cancel()
		return billed, err
	}
	if err := status.Err(); err != nil {
		return 0, err
	}
	var billed int64
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		billed = qs.TotalBytesBilled
	}
	it, err := job.Read(ctx)
	if err != nil {
		return billed, err
	}
	var iterErr error
	sErr := scan(func(dst any) bool {
		err := it.Next(dst)
		if err == iterator.Done {
			return false
		}
		if err != nil {
			iterErr = err
			return false
		}
		return true
	})
	if iterErr != nil {
		return billed, iterErr
	}
	return billed, sErr
}

type bqDailyJob struct {
	id       string
	readySQL string
	runner   scamBQRunner
}

func (j *bqDailyJob) ID() string { return j.id }

func (j *bqDailyJob) Ready(ctx context.Context, end civil.Date) (bool, error) {
	t, err := j.runner.MaxBlockTime(ctx, j.readySQL, end)
	if err != nil {
		return false, err
	}
	return !t.Before(dayStart(end).Add(stablecoinFinality)), nil
}

func (j *bqDailyJob) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	switch j.id {
	case scamEthSourceID:
		b1, err := j.runner.DryRun(ctx, scamEthPoisonSQL, start, end)
		if err != nil {
			return 0, err
		}
		b2, err := j.runner.DryRun(ctx, scamEthFakeTokenSQL, start, end)
		if err != nil {
			return 0, err
		}
		return b1 + b2, nil
	case scamTronSourceID:
		return j.runner.DryRun(ctx, scamTronPoisonSQL, start, end)
	case tronStablecoinSourceID:
		return j.runner.DryRun(ctx, tronStablecoinSQL, start, end)
	case scamBTCSourceID:
		return j.runner.DryRun(ctx, scamBTCRBFSQL, start, end)
	default:
		return 0, fmt.Errorf("unknown daily job %s", j.id)
	}
}

func initWindowDays(start, end civil.Date, defaultMetrics map[string]float64) ([]string, map[string]*scamDayBatch) {
	var order []string
	byDay := map[string]*scamDayBatch{}
	for d := start; d.Before(end); d = d.AddDays(1) {
		ds := d.String()
		stats := make(map[string]float64, len(defaultMetrics))
		for k, v := range defaultMetrics {
			stats[k] = v
		}
		order = append(order, ds)
		byDay[ds] = &scamDayBatch{Day: ds, Stats: stats}
	}
	return order, byDay
}

func collectDays(order []string, byDay map[string]*scamDayBatch) []scamDayBatch {
	out := make([]scamDayBatch, 0, len(order))
	for _, ds := range order {
		out = append(out, *byDay[ds])
	}
	return out
}

func (j *bqDailyJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	switch j.id {
	case scamEthSourceID:
		return j.runEth(ctx, start, end, maxBytes)
	case scamTronSourceID:
		return j.runTronPoison(ctx, start, end, maxBytes)
	case tronStablecoinSourceID:
		return j.runTronStablecoin(ctx, start, end, maxBytes)
	case scamBTCSourceID:
		return j.runBTC(ctx, start, end, maxBytes)
	default:
		return dailyResult{}, 0, fmt.Errorf("unknown daily job %s", j.id)
	}
}

func (j *bqDailyJob) runEth(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	order, byDay := initWindowDays(start, end, map[string]float64{
		"eth_poison_hits":    0,
		"eth_poison_victims": 0,
		"eth_lookalikes":     0,
		"eth_fake_transfers": 0,
		"eth_fake_contracts": 0,
	})
	seenLookalikes := map[string]map[string]bool{}
	seenContracts := map[string]map[string]bool{}
	totalRows := 0

	billed1, err := j.runner.Query(ctx, scamEthPoisonSQL, start, end, maxBytes, func(next func(dst any) bool) error {
		var r ethPoisonRow
		for next(&r) {
			b := byDay[r.Day]
			if b == nil {
				return fmt.Errorf("eth poison row day %q outside window", r.Day)
			}
			if !ethAddressRe.MatchString(r.Lookalike) || !ethAddressRe.MatchString(r.Imitated) {
				return fmt.Errorf("eth poison row bad address %q / %q", r.Lookalike, r.Imitated)
			}
			lk := strings.ToLower(r.Lookalike)
			im := strings.ToLower(r.Imitated)
			b.Lookalikes = append(b.Lookalikes, lookalikeRow{
				Chain:     "eth",
				Lookalike: lk,
				Imitated:  im,
				Hits:      int(r.Hits),
				Victims:   int(r.Victims),
				FirstSeen: r.Day,
				LastSeen:  r.Day,
			})
			b.Stats["eth_poison_hits"] += float64(r.Hits)
			b.Stats["eth_poison_victims"] += float64(r.Victims)
			if seenLookalikes[r.Day] == nil {
				seenLookalikes[r.Day] = map[string]bool{}
			}
			seenLookalikes[r.Day][lk] = true
			b.Stats["eth_lookalikes"] = float64(len(seenLookalikes[r.Day]))
			totalRows++
		}
		return nil
	})
	if err != nil {
		return dailyResult{}, billed1, err
	}

	billed2, err := j.runner.Query(ctx, scamEthFakeTokenSQL, start, end, maxBytes, func(next func(dst any) bool) error {
		var r ethFakeTokenRow
		for next(&r) {
			b := byDay[r.Day]
			if b == nil {
				return fmt.Errorf("eth fake token row day %q outside window", r.Day)
			}
			if !ethAddressRe.MatchString(r.Contract) {
				return fmt.Errorf("eth fake token bad contract %q", r.Contract)
			}
			c := strings.ToLower(r.Contract)
			b.FakeTokens = append(b.FakeTokens, fakeTokenRow{
				Chain:      "eth",
				Contract:   c,
				Symbol:     strings.TrimSpace(r.Symbol),
				Transfers:  int(r.Transfers),
				Recipients: int(r.Recipients),
				FirstSeen:  r.Day,
				LastSeen:   r.Day,
			})
			b.Stats["eth_fake_transfers"] += float64(r.Transfers)
			if seenContracts[r.Day] == nil {
				seenContracts[r.Day] = map[string]bool{}
			}
			seenContracts[r.Day][c] = true
			b.Stats["eth_fake_contracts"] = float64(len(seenContracts[r.Day]))
			totalRows++
		}
		return nil
	})
	if err != nil {
		return dailyResult{}, billed1 + billed2, err
	}

	return dailyResult{Rows: totalRows, Days: collectDays(order, byDay)}, billed1 + billed2, nil
}

func (j *bqDailyJob) runTronPoison(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	order, byDay := initWindowDays(start, end, map[string]float64{
		"tron_poison_hits":    0,
		"tron_poison_victims": 0,
		"tron_lookalikes":     0,
		"tron_candidates":     0,
	})
	seenLookalikes := map[string]map[string]bool{}
	totalRows := 0

	billed, err := j.runner.Query(ctx, scamTronPoisonSQL, start, end, maxBytes, func(next func(dst any) bool) error {
		var r tronPoisonRow
		for next(&r) {
			b := byDay[r.Day]
			if b == nil {
				return fmt.Errorf("tron poison row day %q outside window", r.Day)
			}
			b.Stats["tron_candidates"] = float64(r.Candidates)
			if r.Lookalike58 == "" || r.Hits == 0 {
				continue
			}
			if !tronShapeRe.MatchString(r.Lookalike58) || !tronShapeRe.MatchString(r.Imitated58) {
				return fmt.Errorf("tron poison row bad base58 %q / %q", r.Lookalike58, r.Imitated58)
			}
			b.Lookalikes = append(b.Lookalikes, lookalikeRow{
				Chain:     "tron",
				Lookalike: r.Lookalike58,
				Imitated:  r.Imitated58,
				Hits:      int(r.Hits),
				Victims:   int(r.Victims),
				FirstSeen: r.Day,
				LastSeen:  r.Day,
			})
			b.Stats["tron_poison_hits"] += float64(r.Hits)
			b.Stats["tron_poison_victims"] += float64(r.Victims)
			if seenLookalikes[r.Day] == nil {
				seenLookalikes[r.Day] = map[string]bool{}
			}
			seenLookalikes[r.Day][r.Lookalike58] = true
			b.Stats["tron_lookalikes"] = float64(len(seenLookalikes[r.Day]))
			totalRows++
		}
		return nil
	})
	if err != nil {
		return dailyResult{}, billed, err
	}
	return dailyResult{Rows: totalRows, Days: collectDays(order, byDay)}, billed, nil
}

func (j *bqDailyJob) runTronStablecoin(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	order, byDay := initWindowDays(start, end, map[string]float64{
		"tron_usdt_freezes":   0,
		"tron_usdt_unfreezes": 0,
		"tron_usdt_destroys":  0,
	})
	var evs []stablecoinEvent
	billed, err := j.runner.Query(ctx, tronStablecoinSQL, start, end, maxBytes, func(next func(dst any) bool) error {
		var r tronStablecoinRow
		for next(&r) {
			ev, err := decodeTronStablecoinRow(r)
			if err != nil {
				return err
			}
			if len(ev.BlockTime) >= 10 {
				if b := byDay[ev.BlockTime[:10]]; b != nil {
					switch ev.Action {
					case "freeze":
						b.Stats["tron_usdt_freezes"]++
					case "unfreeze":
						b.Stats["tron_usdt_unfreezes"]++
					case "destroy":
						b.Stats["tron_usdt_destroys"]++
					}
				}
			}
			evs = append(evs, ev)
		}
		return nil
	})
	if err != nil {
		return dailyResult{}, billed, err
	}
	return dailyResult{Rows: len(evs), TronEvents: evs, Days: collectDays(order, byDay)}, billed, nil
}

func (j *bqDailyJob) runBTC(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	order, byDay := initWindowDays(start, end, map[string]float64{
		"btc_txs":     0,
		"btc_rbf_txs": 0,
	})
	totalRows := 0
	billed, err := j.runner.Query(ctx, scamBTCRBFSQL, start, end, maxBytes, func(next func(dst any) bool) error {
		var r btcRBFRow
		for next(&r) {
			b := byDay[r.Day]
			if b == nil {
				return fmt.Errorf("btc rbf row day %q outside window", r.Day)
			}
			b.Stats["btc_txs"] = float64(r.Txs)
			b.Stats["btc_rbf_txs"] = float64(r.RBFSignalTxs)
			totalRows++
		}
		return nil
	})
	if err != nil {
		return dailyResult{}, billed, err
	}
	return dailyResult{Rows: totalRows, Days: collectDays(order, byDay)}, billed, nil
}

func (j *bqDailyJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	switch j.id {
	case scamEthSourceID:
		return s.applyScamBatch(ctx, scamEthSourceID, "eth", r.Days, newFrom, cursor, now)
	case scamTronSourceID:
		return s.applyScamBatch(ctx, scamTronSourceID, "tron", r.Days, newFrom, cursor, now)
	case scamBTCSourceID:
		return s.applyScamBatch(ctx, scamBTCSourceID, "btc", r.Days, newFrom, cursor, now)
	case tronStablecoinSourceID:
		return s.insertTronStablecoinEvents(ctx, r.TronEvents, newFrom, cursor, now)
	default:
		return fmt.Errorf("unknown daily job %s", j.id)
	}
}

func newScamRadarJobsWithRunner(runner scamBQRunner) []*bqDailyJob {
	return []*bqDailyJob{
		{id: scamEthSourceID, readySQL: ethTokenTransfersReadySQL, runner: runner},
		{id: scamTronSourceID, readySQL: tronLogsReadySQL, runner: runner},
		{id: scamBTCSourceID, readySQL: btcInputsReadySQL, runner: runner},
		{id: tronStablecoinSourceID, readySQL: tronLogsReadySQL, runner: runner},
	}
}

func newScamRadarJobs(client *bigquery.Client) []*bqDailyJob {
	return newScamRadarJobsWithRunner(bqScamRunner{client: client})
}
