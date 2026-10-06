package main

// USDT/USDC blacklist events from bigquery-public-data.crypto_ethereum.logs.
// BigQuery is used only by the background sync and the backfill CLI, never by
// a lookup (spec D3). The logs view is DAY-partitioned on block_timestamp, so
// the half-open date window prunes partitions; the address filter does not.

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

const (
	usdtContract = "0xdac17f958d2ee523a2206206994597c13d831ec7"
	usdcContract = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"

	topicUSDTAddedBlackList      = "0x42e160154868087d6bfdc0ca23d96a1c1cfa32f1b72ba9ba27b69b98a0d819dc" // AddedBlackList(address)
	topicUSDTRemovedBlackList    = "0xd7e9ec6e6ecd65492dce6bf513cd6867560d49544421d0783ddf06e76c24470c" // RemovedBlackList(address)
	topicUSDTDestroyedBlackFunds = "0x61e6e66b0d6339b2980aecc6ccc0039736791f0ccde9ed512e789a7fbdd698c6" // DestroyedBlackFunds(address,uint256)
	topicUSDCBlacklisted         = "0xffa4e6181777692565cf28528fc88fd1516ea86b56da075235fa575af6a4b855" // Blacklisted(address)
	topicUSDCUnBlacklisted       = "0x117e3210bb9aa7d9baff172026820255c6f6c30ba8999d1c2fd88e2848137c4e" // UnBlacklisted(address)

	// stablecoinFirstDay is the earliest USDT freeze; coverage from this day
	// on means the full history is local (spec §7.2).
	stablecoinFirstDay = "2017-11-28"

	ethLogsTable = "`bigquery-public-data.crypto_ethereum.logs`"
)

var stablecoinFirstDate = civil.Date{Year: 2017, Month: time.November, Day: 28}

// stablecoinSQL: USDT puts the target address in data (not indexed), USDC in
// topics[1]. ELSE 'unfreeze' is safe because WHERE admits only the 5 topics.
// The destroy amount is returned as hex and parsed with big.Int in Go.
const stablecoinSQL = `
SELECT
  transaction_hash AS tx_hash,
  log_index,
  block_number,
  FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS block_time,
  IF(address = '` + usdtContract + `', 'USDT', 'USDC') AS token,
  CASE topics[SAFE_OFFSET(0)]
    WHEN '` + topicUSDTAddedBlackList + `' THEN 'freeze'
    WHEN '` + topicUSDCBlacklisted + `' THEN 'freeze'
    WHEN '` + topicUSDTDestroyedBlackFunds + `' THEN 'destroy'
    ELSE 'unfreeze'
  END AS action,
  LOWER(CONCAT('0x', SUBSTR(
    IF(address = '` + usdtContract + `', data, topics[SAFE_OFFSET(1)]), 27, 40))) AS address,
  IF(topics[SAFE_OFFSET(0)] = '` + topicUSDTDestroyedBlackFunds + `',
     SUBSTR(data, 67, 64), '') AS amount_hex
FROM ` + ethLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
  AND address IN ('` + usdtContract + `', '` + usdcContract + `')
  AND topics[SAFE_OFFSET(0)] IN ('` + topicUSDTAddedBlackList + `', '` + topicUSDTRemovedBlackList + `',
    '` + topicUSDTDestroyedBlackFunds + `', '` + topicUSDCBlacklisted + `', '` + topicUSDCUnBlacklisted + `')`

// blockCheckSQL finds the newest exported log in the 2 hours after
// end_date; the sync only trusts a day once logs past end_date 00:30 exist
// (spec §7.1). Querying logs directly (DAY-partitioned on block_timestamp)
// prunes to a single day partition (~25 MB instead of scanning 209 MB of
// unpartitioned blocks.timestamp) and verifies the exact table being synced.
const blockCheckSQL = `
SELECT MAX(block_timestamp) AS t FROM ` + ethLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@end_date) AND block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)`

// stablecoinSource isolates BigQuery so the syncer and the backfill CLI can
// be tested with a fake (spec §15). Windows are half-open [start, end).
type stablecoinSource interface {
	Fetch(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stablecoinEvent, int64, error)
	DryRun(ctx context.Context, start, end civil.Date) (int64, error)
	MaxBlockTime(ctx context.Context, end civil.Date) (time.Time, error)
}

type stablecoinRow struct {
	TxHash      string `bigquery:"tx_hash"`
	LogIndex    int64  `bigquery:"log_index"`
	BlockNumber int64  `bigquery:"block_number"`
	BlockTime   string `bigquery:"block_time"`
	Token       string `bigquery:"token"`
	Action      string `bigquery:"action"`
	Address     string `bigquery:"address"`
	AmountHex   string `bigquery:"amount_hex"`
}

// decodeStablecoinRow validates one row. Any surprise fails the whole batch,
// so the cursor does not move past data that was not understood.
func decodeStablecoinRow(r stablecoinRow) (stablecoinEvent, error) {
	if !listAddressRe.MatchString(r.Address) {
		return stablecoinEvent{}, fmt.Errorf("row %s:%d: bad address %q", r.TxHash, r.LogIndex, r.Address)
	}
	if r.Token != "USDT" && r.Token != "USDC" {
		return stablecoinEvent{}, fmt.Errorf("row %s:%d: bad token %q", r.TxHash, r.LogIndex, r.Token)
	}
	amount := ""
	switch r.Action {
	case "freeze", "unfreeze":
	case "destroy":
		n, ok := new(big.Int).SetString(r.AmountHex, 16)
		if !ok {
			return stablecoinEvent{}, fmt.Errorf("row %s:%d: bad destroy amount %q", r.TxHash, r.LogIndex, r.AmountHex)
		}
		amount = n.String()
	default:
		return stablecoinEvent{}, fmt.Errorf("row %s:%d: bad action %q", r.TxHash, r.LogIndex, r.Action)
	}
	return stablecoinEvent{TxHash: r.TxHash, LogIndex: r.LogIndex, Token: r.Token, Action: r.Action,
		Address: r.Address, Amount: amount, BlockNumber: r.BlockNumber, BlockTime: r.BlockTime}, nil
}

// formatTokenAmount renders a smallest-unit integer with 2 decimals,
// e.g. 1640000459211 with 6 decimals → "1640000.46".
func formatTokenAmount(n *big.Int, decimals int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Rat).SetFrac(n, scale).FloatString(2)
}

type bqStablecoinSource struct{ client *bigquery.Client }

func windowParams(start, end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{{Name: "start_date", Value: start}, {Name: "end_date", Value: end}}
}

// Fetch runs the sync query with a hard MaxBytesBilled cap enforced by
// BigQuery and returns the events plus the bytes actually billed.
func (b bqStablecoinSource) Fetch(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stablecoinEvent, int64, error) {
	q := b.client.Query(stablecoinSQL)
	q.Parameters = windowParams(start, end)
	q.MaxBytesBilled = maxBytes
	job, err := q.Run(ctx)
	if err != nil {
		return nil, 0, err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		// If the local context timed out after the BigQuery job was already
		// submitted, try to cancel the server-side job and check whether it
		// already billed bytes (or assume conservatively that an orphaned
		// timed-out job may still finish and bill on the server) so the
		// caller applies the 6h billed-retry backoff instead of re-submitting
		// the same query every hour.
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = job.Cancel(cctx)
		var billed int64
		if st, sErr := job.Status(cctx); sErr == nil && st != nil && st.Statistics != nil {
			if qs, ok := st.Statistics.Details.(*bigquery.QueryStatistics); ok {
				billed = qs.TotalBytesBilled
			}
		}
		cancel()
		return nil, billed, err
	}
	if err := status.Err(); err != nil {
		return nil, 0, err
	}
	// The job is done and billed from here on; errors below still report the
	// billed bytes so the caller can avoid re-billing the same query hourly.
	var billed int64
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		billed = qs.TotalBytesBilled
	}
	it, err := job.Read(ctx)
	if err != nil {
		return nil, billed, err
	}
	var events []stablecoinEvent
	for {
		var r stablecoinRow
		err := it.Next(&r)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, billed, err
		}
		e, err := decodeStablecoinRow(r)
		if err != nil {
			return nil, billed, err
		}
		events = append(events, e)
	}
	return events, billed, nil
}

// DryRun returns the bytes the sync query would process; it is free.
func (b bqStablecoinSource) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	q := b.client.Query(stablecoinSQL)
	q.Parameters = windowParams(start, end)
	q.DryRun = true
	job, err := q.Run(ctx)
	if err != nil {
		return 0, err
	}
	return job.LastStatus().Statistics.TotalBytesProcessed, nil
}

type blockMaxRow struct {
	T bigquery.NullTimestamp `bigquery:"t"`
}

// MaxBlockTime returns the zero time when no block exists yet in the window.
func (b bqStablecoinSource) MaxBlockTime(ctx context.Context, end civil.Date) (time.Time, error) {
	q := b.client.Query(blockCheckSQL)
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
