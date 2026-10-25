package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

const (
	stableEthJobID  = "stable_eth"
	stableTronJobID = "stable_tron"

	stableEthUSDTMinDailyTransfers  = 10_000
	stableTronUSDTMinDailyTransfers = 100_000

	stableOnchainTTL = 5 * time.Minute

	stableOnchainCoverageNote = "On-chain transfer rollups, large transfers (top 20 per day) and supply events cover Ethereum (USDT, USDC, EURC) and TRON (USDT) settled UTC days via opt-in BigQuery jobs. Ordinary transfer counts and volume exclude mint/burn transfers from or to the zero address."

	stableAggregateExposureNote = "BigQuery monthly caps are enforced separately per feature (Bitcoin, Stablecoin, plus default-on Address Risk and Scam Radar jobs); worst-case monthly spend is their sum."
)

// Event topic hashes for opt-in BigQuery jobs (stablecoin.md Appendix D & E.4).
// Verified against legacy Keccak-256 in TestStableBQTopicsMatchKeccak.
const (
	topicERC20Transfer                = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" // Transfer(address,address,uint256)
	topicUSDTIssue                    = "0xcb8241adb0c3fdb35b70c24ce35c5eb0c17af7431c99f827d44a445ca624176a" // Issue(uint256)
	topicUSDTRedeem                   = "0x702d5967f45f6513a38ffc42d6ba9bf230bd40e8f53b16363c7eb4fd2deb9a44" // Redeem(uint256)
	topicUSDTDeprecate                = "0xcc358699805e9a8b7f77b522628c7cb9abd07d9efb86b6fb616af1609036a99e" // Deprecate(address)
	topicUSDTParams                   = "0xb044a1e409eac5c48e5af22d4af52670dd1a99059537a78b31b48c6500a6354e" // Params(uint256,uint256)
	topicStablePause                  = "0x6985a02210a168e66602d3235cb6db0e70f92b3ba4d376a33c0f3d9434bff625" // Pause()
	topicStableUnpause                = "0x7805862f689e2f13df9f062ff482ad3ad112aca9e0847911ed832e158c525b33" // Unpause()
	topicFiatTokenMint                = "0xab8530f87dc9b59234c4623bf917212bb2536d647574c8e7e5da92c2ede0c9f8" // Mint(address,address,uint256)
	topicFiatTokenBurn                = "0xcc16f5dbb4873280815c1ee09dbd06736cffcc184412cf7a71a0fdb75d397ca5" // Burn(address,uint256)
	topicFiatTokenMinterConfigured    = "0x46980fca912ef9bcdbd36877427b6b90e860769f604e89c0e67720cece530d20" // MinterConfigured(address,uint256)
	topicFiatTokenMinterRemoved       = "0xe94479a9f7e1952cc78f2d6baab678adc1b772d936c6583def489e524cb66692" // MinterRemoved(address)
	topicFiatTokenMasterMinterChanged = "0xdb66dfa9c6b8f5226fe9aac7e51897ae8ee94ac31dc70bb6c9900b2574b707e6" // MasterMinterChanged(address)
	topicFiatTokenBlacklisterChanged  = "0xc67398012c111ce95ecb7429b933096c977380ee6c421175a71a4a4c6c88c06e" // BlacklisterChanged(address)
	topicFiatTokenPauserChanged       = "0xb80482a293ca2e013eda8683c9bd7fc8347cfdaeea5ede58cba46df502c2a604" // PauserChanged(address)
	topicFiatTokenRescuerChanged      = "0xe475e580d85111348e40d8ca33cfdd74c30fe1655c2d8537a13abc10065ffa5a" // RescuerChanged(address)
	topicOwnershipTransferred         = "0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0" // OwnershipTransferred(address,address)
	topicProxyUpgraded                = "0xbc7cd75a20ee27fd9adebab32041f755214dbc6bffa90cc0225b39da2e5c2d3b" // Upgraded(address)
	topicProxyAdminChanged            = "0x7e644d79422f17c01e4894b5f4f588d331ebfa28653d42ae832dc59e38c9798f" // AdminChanged(address,address)
)

type stableEthTopTransferBQ struct {
	TxHash    string `bigquery:"tx_hash"`
	LogIndex  int64  `bigquery:"log_index"`
	TS        string `bigquery:"ts"`
	Src       string `bigquery:"src"`
	Dst       string `bigquery:"dst"`
	AmountRaw string `bigquery:"amount_raw"`
}

type stableEthTransferAggRow struct {
	Day           string                   `bigquery:"day"`
	Token         string                   `bigquery:"token"`
	Transfers     int64                    `bigquery:"transfers"`
	ZeroValue     int64                    `bigquery:"zero_value"`
	VolumeRaw     string                   `bigquery:"volume_raw"`
	Senders       int64                    `bigquery:"senders"`
	Receivers     int64                    `bigquery:"receivers"`
	Ge1MCount     int64                    `bigquery:"ge_1m_count"`
	Ge1MVolumeRaw string                   `bigquery:"ge_1m_volume_raw"`
	MintedRaw     string                   `bigquery:"minted_raw"`
	BurnedRaw     string                   `bigquery:"burned_raw"`
	TopTransfers  []stableEthTopTransferBQ `bigquery:"top_transfers"`
}

type stableEthLogRow struct {
	Day         string `bigquery:"day"`
	TxHash      string `bigquery:"tx_hash"`
	LogIndex    int64  `bigquery:"log_index"`
	BlockNumber int64  `bigquery:"block_number"`
	TS          string `bigquery:"ts"`
	Address     string `bigquery:"address"`
	Topic0      string `bigquery:"topic0"`
	Topic1      string `bigquery:"topic1"`
	Topic2      string `bigquery:"topic2"`
	Data        string `bigquery:"data"`
}

type stableTronTopTransferBQ struct {
	TxHash    string `bigquery:"tx_hash"`
	LogIndex  int64  `bigquery:"log_index"`
	TS        string `bigquery:"ts"`
	SrcHex20  string `bigquery:"src_hex20"`
	DstHex20  string `bigquery:"dst_hex20"`
	AmountRaw string `bigquery:"amount_raw"`
}

type stableTronTransferAggRow struct {
	Day           string                    `bigquery:"day"`
	Transfers     int64                     `bigquery:"transfers"`
	ZeroValue     int64                     `bigquery:"zero_value"`
	VolumeRaw     string                    `bigquery:"volume_raw"`
	Senders       int64                     `bigquery:"senders"`
	Receivers     int64                     `bigquery:"receivers"`
	Ge1MCount     int64                     `bigquery:"ge_1m_count"`
	Ge1MVolumeRaw string                    `bigquery:"ge_1m_volume_raw"`
	MintedRaw     string                    `bigquery:"minted_raw"`
	BurnedRaw     string                    `bigquery:"burned_raw"`
	OverflowRows  int64                     `bigquery:"overflow_rows"`
	TopTransfers  []stableTronTopTransferBQ `bigquery:"top_transfers"`
}

type stableTronLogRow struct {
	Day         string `bigquery:"day"`
	TxHash      string `bigquery:"tx_hash"`
	LogIndex    int64  `bigquery:"log_index"`
	BlockNumber int64  `bigquery:"block_number"`
	TS          string `bigquery:"ts"`
	Topic0      string `bigquery:"topic0"`
	Topic1      string `bigquery:"topic1"`
	Topic2      string `bigquery:"topic2"`
	Data        string `bigquery:"data"`
}

func stableEthTransfersSQL() string {
	return `
WITH t AS (
  SELECT DATE(block_timestamp) AS d,
         FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS ts,
         transaction_hash AS tx_hash,
         log_index,
         LOWER(address) AS token,
         LOWER(from_address) AS src,
         LOWER(to_address) AS dst,
         CAST(quantity AS BIGNUMERIC) AS q
  FROM ` + scamEthTokenTransfersTable + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
    AND address IN ('` + usdtContract + `', '` + usdcContract + `', '` + eurcContract + `')
    AND NOT removed
),
x AS (
  SELECT *, (src = '0x0000000000000000000000000000000000000000'
             OR dst = '0x0000000000000000000000000000000000000000') AS is_supply
  FROM t
)
SELECT FORMAT_DATE('%Y-%m-%d', d) AS day,
       token AS token,
       COUNTIF(NOT is_supply) AS transfers,
       COUNTIF(NOT is_supply AND q = 0) AS zero_value,
       CAST(SUM(IF(is_supply, 0, q)) AS STRING) AS volume_raw,
       APPROX_COUNT_DISTINCT(IF(NOT is_supply AND q > 0, src, NULL)) AS senders,
       APPROX_COUNT_DISTINCT(IF(NOT is_supply AND q > 0, dst, NULL)) AS receivers,
       COUNTIF(NOT is_supply AND q >= 1000000000000) AS ge_1m_count,
       CAST(SUM(IF(NOT is_supply AND q >= 1000000000000, q, 0)) AS STRING) AS ge_1m_volume_raw,
       CAST(SUM(IF(src = '0x0000000000000000000000000000000000000000', q, 0)) AS STRING) AS minted_raw,
       CAST(SUM(IF(dst = '0x0000000000000000000000000000000000000000', q, 0)) AS STRING) AS burned_raw,
       ARRAY_AGG(IF(is_supply, NULL, STRUCT(tx_hash, log_index, ts, src, dst, CAST(q AS STRING) AS amount_raw))
                 IGNORE NULLS ORDER BY q DESC LIMIT 20) AS top_transfers
FROM x
GROUP BY day, token`
}

func stableEthLogsSQL() string {
	topics := strings.Join([]string{
		"'" + topicUSDTIssue + "'",
		"'" + topicUSDTRedeem + "'",
		"'" + topicUSDTDeprecate + "'",
		"'" + topicUSDTParams + "'",
		"'" + topicStablePause + "'",
		"'" + topicStableUnpause + "'",
		"'" + topicFiatTokenMint + "'",
		"'" + topicFiatTokenBurn + "'",
		"'" + topicFiatTokenMinterConfigured + "'",
		"'" + topicFiatTokenMinterRemoved + "'",
		"'" + topicFiatTokenMasterMinterChanged + "'",
		"'" + topicFiatTokenBlacklisterChanged + "'",
		"'" + topicFiatTokenPauserChanged + "'",
		"'" + topicFiatTokenRescuerChanged + "'",
		"'" + topicOwnershipTransferred + "'",
		"'" + topicProxyUpgraded + "'",
		"'" + topicProxyAdminChanged + "'",
	}, ", ")
	return `
SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day,
       transaction_hash AS tx_hash,
       log_index AS log_index,
       block_number AS block_number,
       FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS ts,
       LOWER(address) AS address,
       LOWER(IFNULL(topics[SAFE_OFFSET(0)], '')) AS topic0,
       LOWER(IFNULL(topics[SAFE_OFFSET(1)], '')) AS topic1,
       LOWER(IFNULL(topics[SAFE_OFFSET(2)], '')) AS topic2,
       LOWER(IFNULL(data, '')) AS data
FROM ` + ethLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
  AND address IN ('` + usdtContract + `', '` + usdcContract + `', '` + eurcContract + `')
  AND topics[SAFE_OFFSET(0)] IN (` + topics + `)`
}

func stableTronTransfersSQL() string {
	return `
WITH raw AS (
  SELECT DATE(block_timestamp) AS d,
         FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS ts,
         transaction_hash AS tx_hash,
         log_index,
         LOWER(SUBSTR(topics[SAFE_OFFSET(1)], 27, 40)) AS src_hex20,
         LOWER(SUBSTR(topics[SAFE_OFFSET(2)], 27, 40)) AS dst_hex20,
         (LENGTH(data) >= 66 AND SUBSTR(data, 3, 48) = REPEAT('0', 48) AND SUBSTR(data, 51, 1) < '8') AS safe_u63,
         IF(LENGTH(data) >= 66 AND SUBSTR(data, 3, 48) = REPEAT('0', 48) AND SUBSTR(data, 51, 1) < '8',
            CAST(CAST(CONCAT('0x', SUBSTR(data, 51, 16)) AS INT64) AS BIGNUMERIC),
            NULL) AS q
  FROM ` + scamTronLogsTable + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
    AND address = '` + tronUSDTLogAddress + `'
    AND topics[SAFE_OFFSET(0)] = '` + topicERC20Transfer + `'
    AND NOT removed
),
x AS (
  SELECT *, (src_hex20 = REPEAT('0', 40) OR dst_hex20 = REPEAT('0', 40)) AS is_supply
  FROM raw
)
SELECT FORMAT_DATE('%Y-%m-%d', d) AS day,
       COUNTIF(NOT is_supply AND safe_u63) AS transfers,
       COUNTIF(NOT is_supply AND safe_u63 AND q = 0) AS zero_value,
       CAST(IFNULL(SUM(IF(is_supply OR NOT safe_u63, 0, q)), 0) AS STRING) AS volume_raw,
       APPROX_COUNT_DISTINCT(IF(NOT is_supply AND safe_u63 AND q > 0, src_hex20, NULL)) AS senders,
       APPROX_COUNT_DISTINCT(IF(NOT is_supply AND safe_u63 AND q > 0, dst_hex20, NULL)) AS receivers,
       COUNTIF(NOT is_supply AND safe_u63 AND q >= 1000000000000) AS ge_1m_count,
       CAST(IFNULL(SUM(IF(NOT is_supply AND safe_u63 AND q >= 1000000000000, q, 0)), 0) AS STRING) AS ge_1m_volume_raw,
       CAST(IFNULL(SUM(IF(src_hex20 = REPEAT('0', 40) AND safe_u63, q, 0)), 0) AS STRING) AS minted_raw,
       CAST(IFNULL(SUM(IF(dst_hex20 = REPEAT('0', 40) AND safe_u63, q, 0)), 0) AS STRING) AS burned_raw,
       COUNTIF(NOT safe_u63) AS overflow_rows,
       ARRAY_AGG(IF(is_supply OR NOT safe_u63, NULL, STRUCT(tx_hash, log_index, ts, src_hex20, dst_hex20, CAST(q AS STRING) AS amount_raw))
                 IGNORE NULLS ORDER BY q DESC LIMIT 20) AS top_transfers
FROM x
GROUP BY day`
}

func stableTronEventsSQL() string {
	topics := strings.Join([]string{
		"'" + topicUSDTIssue + "'",
		"'" + topicUSDTRedeem + "'",
		"'" + topicUSDTDeprecate + "'",
		"'" + topicUSDTParams + "'",
		"'" + topicStablePause + "'",
		"'" + topicStableUnpause + "'",
		"'" + topicOwnershipTransferred + "'",
	}, ", ")
	return `
SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day,
       transaction_hash AS tx_hash,
       log_index AS log_index,
       block_number AS block_number,
       FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', block_timestamp) AS ts,
       LOWER(IFNULL(topics[SAFE_OFFSET(0)], '')) AS topic0,
       LOWER(IFNULL(topics[SAFE_OFFSET(1)], '')) AS topic1,
       LOWER(IFNULL(topics[SAFE_OFFSET(2)], '')) AS topic2,
       LOWER(IFNULL(data, '')) AS data
FROM ` + scamTronLogsTable + `
WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
  AND address = '` + tronUSDTLogAddress + `'
  AND topics[SAFE_OFFSET(0)] IN (` + topics + `)
  AND NOT removed`
}

func stableApplyQueryLabels(q *bigquery.Query, jobLabel string) {
	applyQueryDefaults(q)
	if q.Labels == nil {
		q.Labels = make(map[string]string)
	}
	q.Labels["app"] = "biglens"
	q.Labels["feature"] = "stablecoin"
	if jobLabel != "" {
		q.Labels["job"] = jobLabel
	}
}

func stableDryRunQuery(ctx context.Context, client *bigquery.Client, sqlText, jobLabel string, params []bigquery.QueryParameter) (int64, error) {
	if client == nil {
		return 0, fmt.Errorf("nil bigquery client")
	}
	q := client.Query(sqlText)
	q.Parameters = params
	q.DryRun = true
	stableApplyQueryLabels(q, jobLabel)
	job, err := q.Run(ctx)
	if err != nil {
		return 0, err
	}
	return job.LastStatus().Statistics.TotalBytesProcessed, nil
}

func stableRunQuery[T any](ctx context.Context, client *bigquery.Client, sqlText, jobLabel string, params []bigquery.QueryParameter, maxBytes int64) ([]T, int64, error) {
	if client == nil {
		return nil, 0, fmt.Errorf("nil bigquery client")
	}
	q := client.Query(sqlText)
	q.Parameters = params
	q.MaxBytesBilled = maxBytes
	stableApplyQueryLabels(q, jobLabel)
	job, err := q.Run(ctx)
	if err != nil {
		return nil, 0, err
	}
	syncAttachJob(ctx, job.ID())
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
		return nil, billed, err
	}
	if err := status.Err(); err != nil {
		return nil, 0, err
	}
	var billed int64
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		billed = qs.TotalBytesBilled
	}
	it, err := job.Read(ctx)
	if err != nil {
		return nil, billed, err
	}
	var out []T
	for {
		var r T
		err := it.Next(&r)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, billed, err
		}
		out = append(out, r)
	}
	return out, billed, nil
}

// --- Sources and dailyJob implementations ---

type stableEthSource interface {
	Ready(ctx context.Context, end civil.Date) (bool, error)
	DryRunTransfers(ctx context.Context, start, end civil.Date) (int64, error)
	DryRunLogs(ctx context.Context, start, end civil.Date) (int64, error)
	FetchTransfers(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableEthTransferAggRow, int64, error)
	FetchLogs(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableEthLogRow, int64, error)
}

type bqStableEthSource struct {
	client *bigquery.Client
}

func (s bqStableEthSource) Ready(ctx context.Context, end civil.Date) (bool, error) {
	r := bqScamRunner{client: s.client}
	t1, err := r.MaxBlockTime(ctx, ethTokenTransfersReadySQL, end)
	if err != nil || t1.Before(dayStart(end).Add(stablecoinFinality)) {
		return false, err
	}
	t2, err := r.MaxBlockTime(ctx, blockCheckSQL, end)
	if err != nil {
		return false, err
	}
	return !t2.Before(dayStart(end).Add(stablecoinFinality)), nil
}

func (s bqStableEthSource) DryRunTransfers(ctx context.Context, start, end civil.Date) (int64, error) {
	return stableDryRunQuery(ctx, s.client, stableEthTransfersSQL(), stableEthJobID, windowParams(start, end))
}

func (s bqStableEthSource) DryRunLogs(ctx context.Context, start, end civil.Date) (int64, error) {
	return stableDryRunQuery(ctx, s.client, stableEthLogsSQL(), stableEthJobID, windowParams(start, end))
}

func (s bqStableEthSource) FetchTransfers(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableEthTransferAggRow, int64, error) {
	return stableRunQuery[stableEthTransferAggRow](ctx, s.client, stableEthTransfersSQL(), stableEthJobID, windowParams(start, end), maxBytes)
}

func (s bqStableEthSource) FetchLogs(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableEthLogRow, int64, error) {
	return stableRunQuery[stableEthLogRow](ctx, s.client, stableEthLogsSQL(), stableEthJobID, windowParams(start, end), maxBytes)
}

type stableTronSource interface {
	Ready(ctx context.Context, end civil.Date) (bool, error)
	DryRunTransfers(ctx context.Context, start, end civil.Date) (int64, error)
	DryRunEvents(ctx context.Context, start, end civil.Date) (int64, error)
	FetchTransfers(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableTronTransferAggRow, int64, error)
	FetchEvents(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableTronLogRow, int64, error)
}

type bqStableTronSource struct {
	client *bigquery.Client
}

func (s bqStableTronSource) Ready(ctx context.Context, end civil.Date) (bool, error) {
	t, err := (bqScamRunner{client: s.client}).MaxBlockTime(ctx, tronLogsReadySQL, end)
	if err != nil {
		return false, err
	}
	return !t.Before(dayStart(end).Add(stablecoinFinality)), nil
}

func (s bqStableTronSource) DryRunTransfers(ctx context.Context, start, end civil.Date) (int64, error) {
	return stableDryRunQuery(ctx, s.client, stableTronTransfersSQL(), stableTronJobID, windowParams(start, end))
}

func (s bqStableTronSource) DryRunEvents(ctx context.Context, start, end civil.Date) (int64, error) {
	return stableDryRunQuery(ctx, s.client, stableTronEventsSQL(), stableTronJobID, windowParams(start, end))
}

func (s bqStableTronSource) FetchTransfers(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableTronTransferAggRow, int64, error) {
	return stableRunQuery[stableTronTransferAggRow](ctx, s.client, stableTronTransfersSQL(), stableTronJobID, windowParams(start, end), maxBytes)
}

func (s bqStableTronSource) FetchEvents(ctx context.Context, start, end civil.Date, maxBytes int64) ([]stableTronLogRow, int64, error) {
	return stableRunQuery[stableTronLogRow](ctx, s.client, stableTronEventsSQL(), stableTronJobID, windowParams(start, end), maxBytes)
}

type stableJobPayload struct {
	chain          string
	daily          []stableOnchainDailyRow
	largeTransfers []StableLargeTransfer
	supplyEvents   []StableSupplyEvent
	controlEvents  []stableControlEvent
}

func stableEthContractAsset(addr string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case usdtContract:
		return "usdt", true
	case usdcContract:
		return "usdc", true
	case eurcContract:
		return "eurc", true
	default:
		return "", false
	}
}

func stableDecodeUint256Word(dataHex string, wordIdx int) (*big.Int, bool) {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(dataHex)), "0x")
	start := wordIdx * 64
	if len(h) < start+64 {
		return nil, false
	}
	w := h[start : start+64]
	if _, err := hex.DecodeString(w); err != nil {
		return nil, false
	}
	n, ok := new(big.Int).SetString(w, 16)
	return n, ok
}

func stableDecodeAddrWord(chain, wordOrTopic string) string {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(wordOrTopic)), "0x")
	if len(h) < 40 {
		return ""
	}
	hex20 := h[len(h)-40:]
	if _, err := hex.DecodeString(hex20); err != nil {
		return ""
	}
	if chain == "tron" {
		if b58, err := tronHexToBase58(hex20); err == nil {
			return b58
		}
		return "0x" + hex20
	}
	return "0x" + hex20
}

func stableDecodeControlLog(asset, chain, txHash, tsStr, topic0, topic1, topic2, data string) *stableControlEvent {
	firstWord := func() string {
		h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(data)), "0x")
		if len(h) >= 64 {
			return h[:64]
		}
		return ""
	}
	secondWord := func() string {
		h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(data)), "0x")
		if len(h) >= 128 {
			return h[64:128]
		}
		return ""
	}
	addrFromTopicOrData := func(topic string, w string) string {
		if topic != "" {
			return stableDecodeAddrWord(chain, topic)
		}
		return stableDecodeAddrWord(chain, w)
	}
	var field, oldVal, newVal string
	switch strings.ToLower(topic0) {
	case topicStablePause:
		field, oldVal, newVal = "paused", "false", "true"
	case topicStableUnpause:
		field, oldVal, newVal = "paused", "true", "false"
	case topicUSDTDeprecate:
		field = "upgraded_address"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicFiatTokenMasterMinterChanged:
		field = "master_minter"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicFiatTokenBlacklisterChanged:
		field = "blacklister"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicFiatTokenPauserChanged:
		field = "pauser"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicFiatTokenRescuerChanged:
		field = "rescuer"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicOwnershipTransferred:
		field = "owner"
		if topic1 != "" || topic2 != "" {
			oldVal = stableDecodeAddrWord(chain, topic1)
			newVal = stableDecodeAddrWord(chain, topic2)
		} else {
			oldVal = stableDecodeAddrWord(chain, firstWord())
			newVal = stableDecodeAddrWord(chain, secondWord())
		}
	case topicProxyUpgraded:
		field = "implementation"
		newVal = addrFromTopicOrData(topic1, firstWord())
	case topicProxyAdminChanged:
		field = "admin"
		if topic1 != "" || topic2 != "" {
			oldVal = stableDecodeAddrWord(chain, topic1)
			newVal = stableDecodeAddrWord(chain, topic2)
		} else {
			oldVal = stableDecodeAddrWord(chain, firstWord())
			newVal = stableDecodeAddrWord(chain, secondWord())
		}
	default:
		return nil
	}
	if field == "" || newVal == "" {
		return nil
	}
	ts := parseStableTS(tsStr)
	return &stableControlEvent{
		DetectedAt:  ts,
		Asset:       asset,
		Chain:       chain,
		Field:       field,
		Old:         oldVal,
		New:         newVal,
		FirstSeen:   ts,
		Source:      "bigquery",
		ConfirmedBy: "bigquery_log",
		TxHash:      txHash,
	}
}

func stableWindowDays(start, end civil.Date) []string {
	var out []string
	for d := start; d.Before(end); d = d.AddDays(1) {
		out = append(out, d.String())
	}
	return out
}

type stableEthJob struct {
	store            *riskStore
	src              stableEthSource
	minUSDTTransfers int64
}

func newStableEthJob(store *riskStore, client *BQClient) *stableEthJob {
	return &stableEthJob{
		store:            store,
		src:              bqStableEthSource{client: client.client},
		minUSDTTransfers: stableEthUSDTMinDailyTransfers,
	}
}

func (j *stableEthJob) ID() string { return stableEthJobID }

func (j *stableEthJob) Ready(ctx context.Context, end civil.Date) (bool, error) {
	return j.src.Ready(ctx, end)
}

func (j *stableEthJob) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	bA, err := j.src.DryRunTransfers(ctx, start, end)
	if err != nil {
		return 0, err
	}
	bB, err := j.src.DryRunLogs(ctx, start, end)
	if err != nil {
		return 0, err
	}
	return bA + bB, nil
}

func (j *stableEthJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	rowsA, billedA, err := j.src.FetchTransfers(ctx, start, end, maxBytes)
	if err != nil {
		return dailyResult{}, billedA, err
	}
	rem := maxBytes - billedA
	if rem < 1<<20 {
		rem = maxBytes
	}
	rowsB, billedB, err := j.src.FetchLogs(ctx, start, end, rem)
	totalBilled := billedA + billedB
	if err != nil {
		return dailyResult{}, totalBilled, err
	}

	days := stableWindowDays(start, end)
	perAssetDay := map[string]map[string]int64{
		"usdt": {},
		"usdc": {},
		"eurc": {},
	}
	dailyMap := map[string]*stableOnchainDailyRow{}
	var largeTransfers []StableLargeTransfer

	for _, r := range rowsA {
		asset, ok := stableEthContractAsset(r.Token)
		if !ok {
			return dailyResult{}, totalBilled, fmt.Errorf("stable_eth: unknown token %q", r.Token)
		}
		perAssetDay[asset][r.Day] = r.Transfers
		key := r.Day + "|" + asset
		row := &stableOnchainDailyRow{
			Day:           r.Day,
			Asset:         asset,
			Chain:         "eth",
			Transfers:     r.Transfers,
			ZeroValue:     r.ZeroValue,
			VolumeRaw:     r.VolumeRaw,
			Senders:       r.Senders,
			Receivers:     r.Receivers,
			Ge1MCount:     r.Ge1MCount,
			Ge1MVolumeRaw: r.Ge1MVolumeRaw,
			MintedRaw:     r.MintedRaw,
			BurnedRaw:     r.BurnedRaw,
		}
		if asset == "usdt" {
			// On Ethereum, USDT mints/burns are Issue/Redeem events without a Transfer from/to 0x0 (§10.2).
			row.MintedRaw = "0"
			row.BurnedRaw = "0"
		}
		dailyMap[key] = row

		for _, tt := range r.TopTransfers {
			amt, ok := stableRawToNative6(tt.AmountRaw)
			if !ok {
				return dailyResult{}, totalBilled, fmt.Errorf("stable_eth: bad top transfer amount %q", tt.AmountRaw)
			}
			largeTransfers = append(largeTransfers, StableLargeTransfer{
				Day:          r.Day,
				Asset:        asset,
				Chain:        "eth",
				TxHash:       tt.TxHash,
				LogIndex:     tt.LogIndex,
				TS:           tt.TS,
				From:         tt.Src,
				To:           tt.Dst,
				AmountRaw:    tt.AmountRaw,
				AmountNative: amt,
			})
		}
	}

	minUSDT := j.minUSDTTransfers
	if minUSDT <= 0 {
		minUSDT = stableEthUSDTMinDailyTransfers
	}
	if err := minPerDayGuard(days, perAssetDay["usdt"], minUSDT, "ETH USDT transfers"); err != nil {
		return dailyResult{}, totalBilled, err
	}
	if err := minPerDayGuard(days, perAssetDay["usdc"], 1, "ETH USDC transfers"); err != nil {
		return dailyResult{}, totalBilled, err
	}
	if err := minPerDayGuard(days, perAssetDay["eurc"], 1, "ETH EURC transfers"); err != nil {
		return dailyResult{}, totalBilled, err
	}

	usdtIssued := map[string]*big.Int{}
	usdtRedeemed := map[string]*big.Int{}
	var supplyEvents []StableSupplyEvent
	var controlEvents []stableControlEvent

	for _, lg := range rowsB {
		asset, ok := stableEthContractAsset(lg.Address)
		if !ok {
			return dailyResult{}, totalBilled, fmt.Errorf("stable_eth log: unknown contract %q", lg.Address)
		}
		t0 := strings.ToLower(lg.Topic0)
		switch t0 {
		case topicUSDTIssue, topicUSDTRedeem, topicFiatTokenMint, topicFiatTokenBurn:
			amtBig, ok := stableDecodeUint256Word(lg.Data, 0)
			if !ok {
				return dailyResult{}, totalBilled, fmt.Errorf("stable_eth log %s:%d: bad supply amount %q", lg.TxHash, lg.LogIndex, lg.Data)
			}
			amtRaw := amtBig.String()
			amtNat, _ := stableRawToNative6(amtRaw)
			var kind, actor string
			switch t0 {
			case topicUSDTIssue:
				kind = "issue"
				if usdtIssued[lg.Day] == nil {
					usdtIssued[lg.Day] = new(big.Int)
				}
				usdtIssued[lg.Day].Add(usdtIssued[lg.Day], amtBig)
			case topicUSDTRedeem:
				kind = "redeem"
				if usdtRedeemed[lg.Day] == nil {
					usdtRedeemed[lg.Day] = new(big.Int)
				}
				usdtRedeemed[lg.Day].Add(usdtRedeemed[lg.Day], amtBig)
			case topicFiatTokenMint:
				kind = "mint"
				actor = stableDecodeAddrWord("eth", lg.Topic2)
				if actor == "" {
					actor = stableDecodeAddrWord("eth", lg.Topic1)
				}
			case topicFiatTokenBurn:
				kind = "burn"
				actor = stableDecodeAddrWord("eth", lg.Topic1)
			}
			supplyEvents = append(supplyEvents, StableSupplyEvent{
				Day:          lg.Day,
				TS:           lg.TS,
				Asset:        asset,
				Chain:        "eth",
				TxHash:       lg.TxHash,
				LogIndex:     lg.LogIndex,
				Kind:         kind,
				AmountRaw:    amtRaw,
				AmountNative: amtNat,
				Actor:        actor,
			})
		default:
			if ce := stableDecodeControlLog(asset, "eth", lg.TxHash, lg.TS, t0, lg.Topic1, lg.Topic2, lg.Data); ce != nil {
				controlEvents = append(controlEvents, *ce)
			}
		}
	}

	for day, sum := range usdtIssued {
		if row, ok := dailyMap[day+"|usdt"]; ok {
			row.MintedRaw = sum.String()
		}
	}
	for day, sum := range usdtRedeemed {
		if row, ok := dailyMap[day+"|usdt"]; ok {
			row.BurnedRaw = sum.String()
		}
	}

	var daily []stableOnchainDailyRow
	for _, d := range days {
		for _, asset := range []string{"usdt", "usdc", "eurc"} {
			if r, ok := dailyMap[d+"|"+asset]; ok {
				daily = append(daily, *r)
			}
		}
	}

	return dailyResult{
		Rows: len(daily) + len(supplyEvents) + len(controlEvents),
		Payload: stableJobPayload{
			chain:          "eth",
			daily:          daily,
			largeTransfers: largeTransfers,
			supplyEvents:   supplyEvents,
			controlEvents:  controlEvents,
		},
	}, totalBilled, nil
}

func (j *stableEthJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	p, ok := r.Payload.(stableJobPayload)
	if !ok {
		return fmt.Errorf("stable_eth: unexpected payload %T", r.Payload)
	}
	for i := range p.daily {
		p.daily[i].SyncedAt = now
	}
	if err := s.upsertStableOnchainDaily(ctx, p.daily); err != nil {
		return err
	}
	if len(p.daily) > 0 {
		minDay, maxDay := p.daily[0].Day, p.daily[len(p.daily)-1].Day
		for _, d := range p.daily {
			if d.Day < minDay {
				minDay = d.Day
			}
			if d.Day > maxDay {
				maxDay = d.Day
			}
		}
		if err := s.replaceStableLargeTransfers(ctx, "eth", minDay, maxDay, p.largeTransfers); err != nil {
			return err
		}
	}
	if err := s.insertStableSupplyEvents(ctx, p.supplyEvents); err != nil {
		return err
	}
	if err := s.insertStableBQControlEvents(ctx, p.controlEvents); err != nil {
		return err
	}
	if err := ensureSyncRow(ctx, s.db, stableEthJobID); err != nil {
		return err
	}
	return updateSyncWatermarksTx(ctx, s.db, stableEthJobID, newFrom, cursor, r.Rows, now)
}

type stableTronJob struct {
	store        *riskStore
	src          stableTronSource
	minTransfers int64
}

func newStableTronJob(store *riskStore, client *BQClient) *stableTronJob {
	return &stableTronJob{
		store:        store,
		src:          bqStableTronSource{client: client.client},
		minTransfers: stableTronUSDTMinDailyTransfers,
	}
}

func (j *stableTronJob) ID() string { return stableTronJobID }

func (j *stableTronJob) Ready(ctx context.Context, end civil.Date) (bool, error) {
	return j.src.Ready(ctx, end)
}

func (j *stableTronJob) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	bA, err := j.src.DryRunTransfers(ctx, start, end)
	if err != nil {
		return 0, err
	}
	bB, err := j.src.DryRunEvents(ctx, start, end)
	if err != nil {
		return 0, err
	}
	return bA + bB, nil
}

func (j *stableTronJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	rowsA, billedA, err := j.src.FetchTransfers(ctx, start, end, maxBytes)
	if err != nil {
		return dailyResult{}, billedA, err
	}
	rem := maxBytes - billedA
	if rem < 1<<20 {
		rem = maxBytes
	}
	rowsB, billedB, err := j.src.FetchEvents(ctx, start, end, rem)
	totalBilled := billedA + billedB
	if err != nil {
		return dailyResult{}, totalBilled, err
	}

	days := stableWindowDays(start, end)
	perDay := map[string]int64{}
	dailyMap := map[string]*stableOnchainDailyRow{}
	var largeTransfers []StableLargeTransfer

	for _, r := range rowsA {
		if r.OverflowRows > 0 {
			return dailyResult{}, totalBilled, &suspiciousResultError{
				msg: fmt.Sprintf("amount_overflow on %s: %d TRON USDT transfer rows exceeded int64 safe bound", r.Day, r.OverflowRows),
			}
		}
		perDay[r.Day] = r.Transfers
		dailyMap[r.Day] = &stableOnchainDailyRow{
			Day:           r.Day,
			Asset:         "usdt",
			Chain:         "tron",
			Transfers:     r.Transfers,
			ZeroValue:     r.ZeroValue,
			VolumeRaw:     r.VolumeRaw,
			Senders:       r.Senders,
			Receivers:     r.Receivers,
			Ge1MCount:     r.Ge1MCount,
			Ge1MVolumeRaw: r.Ge1MVolumeRaw,
			MintedRaw:     r.MintedRaw,
			BurnedRaw:     r.BurnedRaw,
		}
		for _, tt := range r.TopTransfers {
			amt, ok := stableRawToNative6(tt.AmountRaw)
			if !ok {
				return dailyResult{}, totalBilled, fmt.Errorf("stable_tron: bad top transfer amount %q", tt.AmountRaw)
			}
			from58 := stableDecodeAddrWord("tron", tt.SrcHex20)
			to58 := stableDecodeAddrWord("tron", tt.DstHex20)
			largeTransfers = append(largeTransfers, StableLargeTransfer{
				Day:          r.Day,
				Asset:        "usdt",
				Chain:        "tron",
				TxHash:       tt.TxHash,
				LogIndex:     tt.LogIndex,
				TS:           tt.TS,
				From:         from58,
				To:           to58,
				AmountRaw:    tt.AmountRaw,
				AmountNative: amt,
			})
		}
	}

	minTransfers := j.minTransfers
	if minTransfers <= 0 {
		minTransfers = stableTronUSDTMinDailyTransfers
	}
	if err := minPerDayGuard(days, perDay, minTransfers, "TRON USDT transfers"); err != nil {
		return dailyResult{}, totalBilled, err
	}

	issuedByDay := map[string]*big.Int{}
	redeemedByDay := map[string]*big.Int{}
	var supplyEvents []StableSupplyEvent
	var controlEvents []stableControlEvent

	for _, lg := range rowsB {
		t0 := strings.ToLower(lg.Topic0)
		switch t0 {
		case topicUSDTIssue, topicUSDTRedeem:
			amtBig, ok := stableDecodeUint256Word(lg.Data, 0)
			if !ok {
				return dailyResult{}, totalBilled, fmt.Errorf("stable_tron log %s:%d: bad Issue/Redeem data %q", lg.TxHash, lg.LogIndex, lg.Data)
			}
			amtRaw := amtBig.String()
			amtNat, _ := stableRawToNative6(amtRaw)
			kind := "issue"
			if t0 == topicUSDTIssue {
				if issuedByDay[lg.Day] == nil {
					issuedByDay[lg.Day] = new(big.Int)
				}
				issuedByDay[lg.Day].Add(issuedByDay[lg.Day], amtBig)
			} else {
				kind = "redeem"
				if redeemedByDay[lg.Day] == nil {
					redeemedByDay[lg.Day] = new(big.Int)
				}
				redeemedByDay[lg.Day].Add(redeemedByDay[lg.Day], amtBig)
			}
			supplyEvents = append(supplyEvents, StableSupplyEvent{
				Day:          lg.Day,
				TS:           lg.TS,
				Asset:        "usdt",
				Chain:        "tron",
				TxHash:       lg.TxHash,
				LogIndex:     lg.LogIndex,
				Kind:         kind,
				AmountRaw:    amtRaw,
				AmountNative: amtNat,
			})
		default:
			if ce := stableDecodeControlLog("usdt", "tron", lg.TxHash, lg.TS, t0, lg.Topic1, lg.Topic2, lg.Data); ce != nil {
				controlEvents = append(controlEvents, *ce)
			}
		}
	}

	// Reconcile TRON Issue/Redeem events against Transfer(0x0) minted_raw/burned_raw (§10.3, Appendix E.2).
	parseBigZero := func(s string) *big.Int {
		s = strings.TrimSpace(s)
		if s == "" {
			return big.NewInt(0)
		}
		if z, ok := new(big.Int).SetString(s, 10); ok {
			return z
		}
		return big.NewInt(0)
	}
	var daily []stableOnchainDailyRow
	for _, d := range days {
		row := dailyMap[d]
		if row == nil {
			continue
		}
		mintedTransfer := parseBigZero(row.MintedRaw)
		burnedTransfer := parseBigZero(row.BurnedRaw)
		issuedLog := big.NewInt(0)
		if v := issuedByDay[d]; v != nil {
			issuedLog = v
		}
		redeemedLog := big.NewInt(0)
		if v := redeemedByDay[d]; v != nil {
			redeemedLog = v
		}
		if mintedTransfer.Cmp(issuedLog) != 0 || burnedTransfer.Cmp(redeemedLog) != 0 {
			return dailyResult{}, totalBilled, &suspiciousResultError{
				msg: fmt.Sprintf("supply_event_mismatch on %s: transfer minted=%s/burned=%s vs Issue=%s/Redeem=%s",
					d, mintedTransfer.String(), burnedTransfer.String(), issuedLog.String(), redeemedLog.String()),
			}
		}
		daily = append(daily, *row)
	}

	return dailyResult{
		Rows: len(daily) + len(supplyEvents) + len(controlEvents),
		Payload: stableJobPayload{
			chain:          "tron",
			daily:          daily,
			largeTransfers: largeTransfers,
			supplyEvents:   supplyEvents,
			controlEvents:  controlEvents,
		},
	}, totalBilled, nil
}

func (j *stableTronJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	p, ok := r.Payload.(stableJobPayload)
	if !ok {
		return fmt.Errorf("stable_tron: unexpected payload %T", r.Payload)
	}
	for i := range p.daily {
		p.daily[i].SyncedAt = now
	}
	if err := s.upsertStableOnchainDaily(ctx, p.daily); err != nil {
		return err
	}
	if len(p.daily) > 0 {
		minDay, maxDay := p.daily[0].Day, p.daily[len(p.daily)-1].Day
		for _, d := range p.daily {
			if d.Day < minDay {
				minDay = d.Day
			}
			if d.Day > maxDay {
				maxDay = d.Day
			}
		}
		if err := s.replaceStableLargeTransfers(ctx, "tron", minDay, maxDay, p.largeTransfers); err != nil {
			return err
		}
	}
	if err := s.insertStableSupplyEvents(ctx, p.supplyEvents); err != nil {
		return err
	}
	if err := s.insertStableBQControlEvents(ctx, p.controlEvents); err != nil {
		return err
	}
	if err := ensureSyncRow(ctx, s.db, stableTronJobID); err != nil {
		return err
	}
	return updateSyncWatermarksTx(ctx, s.db, stableTronJobID, newFrom, cursor, r.Rows, now)
}

// --- Stop flags, failure policy, CLI resume and job runner ---

type stableStopFlags struct {
	store *riskStore
}

func newStableStopFlags(store *riskStore) *stableStopFlags {
	return &stableStopFlags{store: store}
}

func (f *stableStopFlags) stopped(ctx context.Context, job string) (string, bool, error) {
	if f == nil || f.store == nil {
		return "", false, nil
	}
	return f.store.getStableKV(ctx, "stopped:"+job)
}

func (f *stableStopFlags) failures(ctx context.Context, job string) ([]syncFailureRecord, error) {
	if f == nil || f.store == nil {
		return nil, nil
	}
	raw, ok, err := f.store.getStableKV(ctx, "failures:"+job)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var recs []syncFailureRecord
	if err := json.Unmarshal([]byte(raw), &recs); err != nil {
		return nil, nil
	}
	return recs, nil
}

func (f *stableStopFlags) recordFailure(ctx context.Context, job string, rec syncFailureRecord) error {
	if f == nil || f.store == nil {
		return nil
	}
	recs, err := f.failures(ctx, job)
	if err != nil {
		return err
	}
	recs = append(recs, rec)
	b, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	return f.store.setStableKV(ctx, "failures:"+job, string(b))
}

func (f *stableStopFlags) stop(ctx context.Context, job, reason string) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.setStableKV(ctx, "stopped:"+job, reason)
}

func (f *stableStopFlags) clear(ctx context.Context, job string) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.deleteStableKV(ctx, "stopped:"+job, "failures:"+job)
}

func stableJobFailurePolicy() syncFailurePolicy {
	return syncFailurePolicy{
		maxBilledFailures: 4,
		within:            24 * time.Hour,
		backoff:           []time.Duration{3 * time.Hour, 6 * time.Hour, 12 * time.Hour},
	}
}

func stableResumeJob(ctx context.Context, store *riskStore, job string) error {
	job = strings.TrimSpace(job)
	if job != stableEthJobID && job != stableTronJobID {
		return fmt.Errorf("unknown Stablecoin BigQuery job %q (expected %s or %s)", job, stableEthJobID, stableTronJobID)
	}
	if store == nil {
		return fmt.Errorf("risk store unavailable")
	}
	flags := newStableStopFlags(store)
	if err := flags.clear(ctx, job); err != nil {
		return err
	}
	if st, err := store.getSyncState(ctx, job); err == nil && strings.HasPrefix(st.LastError, "stopped") {
		_ = store.setSyncError(ctx, job, "")
	}
	return nil
}

func runStableResumeCLI(ctx context.Context, cfg *Config, job string) int {
	var dbPath string
	if cfg != nil {
		dbPath = cfg.AddressRisk.dbPath()
	} else {
		dbPath = "data/security.db"
	}
	store, err := openRiskStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stablecoin-bq-resume: open store %s: %v\n", dbPath, err)
		return 1
	}
	defer store.db.Close()
	if err := stableResumeJob(ctx, store, job); err != nil {
		fmt.Fprintf(os.Stderr, "stablecoin-bq-resume: %v\n", err)
		return 1
	}
	fmt.Printf("resumed Stablecoin BigQuery job %s\n", job)
	return 0
}

func stableJobStatus(ctx context.Context, store *riskStore, settings stableSettings, flags *stableStopFlags, now time.Time, job string) BtcBQStatus {
	jobOn := settings.Jobs && (job != stableTronJobID || settings.Tron)
	if store == nil {
		return BtcBQStatus{Job: job, Jobs: jobOn, State: "off"}
	}
	st, err := store.getSyncState(ctx, job)
	if err != nil {
		return BtcBQStatus{Job: job, Jobs: jobOn, State: "off"}
	}
	out := BtcBQStatus{
		Job:           job,
		Jobs:          jobOn,
		SyncedThrough: st.Cursor,
		CoverageFrom:  st.CoverageFrom,
		LastError:     st.LastError,
	}
	reason, stopped, _ := flags.stopped(ctx, job)
	recs, _ := flags.failures(ctx, job)
	pol := stableJobFailurePolicy()
	var activeRecs []syncFailureRecord
	if pol.within > 0 {
		cutoff := now.UTC().Add(-pol.within)
		for _, f := range recs {
			if !f.At.Before(cutoff) {
				activeRecs = append(activeRecs, f)
			}
		}
	} else {
		activeRecs = recs
	}
	switch {
	case stopped:
		out.State = "stopped"
		out.StopReason = reason
		out.Failures = len(activeRecs)
	case !jobOn:
		out.State = "off"
	case st.LastError == "over_budget":
		out.State = "budget_paused"
	case len(activeRecs) > 0:
		out.State = "retrying"
		out.Failures = len(activeRecs)
		last := activeRecs[len(activeRecs)-1]
		idx := len(activeRecs) - 1
		if idx >= len(pol.backoff) {
			idx = len(pol.backoff) - 1
		}
		if idx >= 0 {
			out.NextRunAfter = last.At.Add(pol.backoff[idx]).UTC().Format(time.RFC3339)
		}
	case st.Cursor == "":
		out.State = "cold_start"
	default:
		out.State = "ready"
	}
	return out
}

func stablecoinSyncJobs(settings stableSettings, store *riskStore, client *BQClient) []func(context.Context) {
	if !settings.Enabled || !settings.Jobs || store == nil || client == nil {
		return nil
	}
	budget := newBqBudget(store, bqBudgetConfig{
		TablePrefix: bqPrefixStable,
		BudgetBytes: settings.BudgetBytes,
	}, time.Now)
	flags := newStableStopFlags(store)

	recoverStaleOnce := func(ctx context.Context) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		lookup := bqLookupJobBytes(rctx, client.client)
		if err := budget.recoverStale(rctx, time.Now().UTC(), lookup); err != nil {
			slog.Warn("stablecoin bigquery recover stale reservations", "error", err)
		}
	}

	ethSyncer := &dailyBQSyncer{
		store:          store,
		job:            newStableEthJob(store, client),
		now:            time.Now,
		initialDays:    settings.InitialDays,
		name:           stableEthJobID,
		hardCapBytes:   128 << 30,
		dryRunSizedCap: true,
		maxWindowDays:  31,
		chunkCatchUp:   true,
		timeout:        10 * time.Minute,
		prune:          func(context.Context, civil.Date) error { return nil },
		budget:         budget,
		failurePolicy:  stableJobFailurePolicy(),
		stopFlags:      flags,
	}
	out := []func(context.Context){recoverStaleOnce, ethSyncer.syncOnce}

	if settings.Tron {
		tronSyncer := &dailyBQSyncer{
			store:          store,
			job:            newStableTronJob(store, client),
			now:            time.Now,
			initialDays:    settings.InitialDays,
			name:           stableTronJobID,
			hardCapBytes:   128 << 30,
			dryRunSizedCap: true,
			maxWindowDays:  31,
			chunkCatchUp:   true,
			timeout:        10 * time.Minute,
			prune:          func(context.Context, civil.Date) error { return nil },
			budget:         budget,
			failurePolicy:  stableJobFailurePolicy(),
			stopFlags:      flags,
		}
		out = append(out, tronSyncer.syncOnce)
	}
	return out
}

// --- /onchain handler ---

type StableOnchainResponse struct {
	AsOf           string                 `json:"as_of"`
	Asset          string                 `json:"asset"`
	Days           int                    `json:"days"`
	DataAvailable  bool                   `json:"data_available"`
	Statuses       map[string]BtcBQStatus `json:"statuses"`
	Daily          []StableOnchainDay     `json:"daily"`
	LargeTransfers []StableLargeTransfer  `json:"large_transfers"`
	SupplyEvents   []StableSupplyEvent    `json:"supply_events"`
	CoverageNote   string                 `json:"coverage_note"`
}

func (s *stablecoinService) buildMetaBigQuery(ctx context.Context) StableMetaBigQuery {
	now := s.now().UTC()
	bq := StableMetaBigQuery{
		Jobs:                  s.settings.Jobs,
		Status:                "off",
		InitialDays:           s.settings.InitialDays,
		MonthlyBudgetGiB:      s.settings.BudgetBytes >> 30,
		Tron:                  s.settings.Tron,
		Statuses:              map[string]BtcBQStatus{},
		StoppedJobs:           []string{},
		AggregateExposureNote: stableAggregateExposureNote,
	}
	if s.store == nil {
		return bq
	}
	if has, err := s.store.stableHasOnchainData(ctx); err == nil {
		bq.DataAvailable = has
	}
	budget := newBqBudget(s.store, bqBudgetConfig{
		TablePrefix: bqPrefixStable,
		BudgetBytes: s.settings.BudgetBytes,
	}, s.now)
	if st, err := budget.status(ctx, now); err == nil {
		bq.MonthBilledGiB = st.MonthBilledGiB
		bq.ReservedGiB = st.ReservedGiB
		bq.RemainingGiB = float64(st.RemainingBytes) / float64(1<<30)
	}
	flags := newStableStopFlags(s.store)
	for _, jobID := range []string{stableEthJobID, stableTronJobID} {
		jst := stableJobStatus(ctx, s.store, s.settings, flags, now, jobID)
		bq.Statuses[jobID] = jst
		if jst.State == "stopped" {
			bq.StoppedJobs = append(bq.StoppedJobs, jobID)
		}
	}
	switch {
	case len(bq.StoppedJobs) > 0:
		bq.Status = "stopped"
	case bq.Statuses[stableEthJobID].State == "retrying" || bq.Statuses[stableTronJobID].State == "retrying":
		bq.Status = "retrying"
	case bq.Statuses[stableEthJobID].State == "budget_paused" || bq.Statuses[stableTronJobID].State == "budget_paused":
		bq.Status = "budget_paused"
	case bq.Statuses[stableEthJobID].State == "ready" || bq.Statuses[stableTronJobID].State == "ready":
		bq.Status = "ready"
	case s.settings.Jobs:
		bq.Status = "cold_start"
	case bq.DataAvailable:
		bq.Status = "cached"
	default:
		bq.Status = "off"
	}
	return bq
}

func (s *stablecoinService) buildOnchain(ctx context.Context, asset string, days int) StableOnchainResponse {
	now := s.now().UTC()
	fromDay := now.AddDate(0, 0, -days).Format(time.DateOnly)
	out := StableOnchainResponse{
		AsOf:           stableFmt(now),
		Asset:          asset,
		Days:           days,
		Statuses:       map[string]BtcBQStatus{},
		Daily:          []StableOnchainDay{},
		LargeTransfers: []StableLargeTransfer{},
		SupplyEvents:   []StableSupplyEvent{},
		CoverageNote:   stableOnchainCoverageNote,
	}
	if s.store == nil {
		return out
	}
	flags := newStableStopFlags(s.store)
	for _, jobID := range []string{stableEthJobID, stableTronJobID} {
		out.Statuses[jobID] = stableJobStatus(ctx, s.store, s.settings, flags, now, jobID)
	}
	daily, err := s.store.stableOnchainDaily(ctx, asset, fromDay)
	if err != nil {
		slog.Warn("stablecoin onchain daily read", "asset", asset, "error", err)
	} else {
		out.Daily = daily
	}
	if len(out.Daily) > 0 {
		out.DataAvailable = true
	} else if has, err := s.store.stableHasOnchainData(ctx); err == nil {
		out.DataAvailable = has
	}
	// Attach per-chain velocity (volume_native / circulating_native) where chain supply exists.
	chainSupply := map[string]float64{}
	assetsToLoad := []string{asset}
	if asset == "all" {
		assetsToLoad = []string{"usdt", "usdc", "eurc"}
	}
	for _, a := range assetsToLoad {
		for _, src := range []string{"issuer", "onchain", "aggregator"} {
			rows, err := s.store.stableChainDailySeries(ctx, a, src, fromDay)
			if err != nil {
				continue
			}
			for _, r := range rows {
				if r.CirculatingNative != nil && *r.CirculatingNative > 0 {
					k := r.Day + "|" + r.Asset + "|" + r.Chain
					if _, exists := chainSupply[k]; !exists {
						chainSupply[k] = *r.CirculatingNative
					}
				}
			}
		}
	}
	for i := range out.Daily {
		d := &out.Daily[i]
		if sup, ok := chainSupply[d.Day+"|"+d.Asset+"|"+d.Chain]; ok && sup > 0 {
			v := d.VolumeNative / sup
			d.Velocity = &v
		}
	}
	if lt, err := s.store.stableLargeTransfers(ctx, asset, fromDay, 200); err != nil {
		slog.Warn("stablecoin large transfers read", "asset", asset, "error", err)
	} else {
		out.LargeTransfers = lt
	}
	if se, err := s.store.stableSupplyEvents(ctx, asset, fromDay, 200); err != nil {
		slog.Warn("stablecoin supply events read", "asset", asset, "error", err)
	} else {
		out.SupplyEvents = se
	}
	return out
}

func (h *APIHandler) StablecoinOnchain(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	q := r.URL.Query()
	asset := q.Get("asset")
	switch asset {
	case "", "all":
		asset = "all"
	default:
		if _, ok := stableAssetByID(asset); !ok {
			writeError(w, "invalid asset", http.StatusBadRequest)
			return
		}
	}
	days := 90
	if rawDays := q.Get("days"); rawDays != "" {
		if n, err := strconv.Atoi(rawDays); err == nil {
			days = min(max(n, 7), 730)
		}
	}
	key := fmt.Sprintf("stablecoin:onchain:%s:%d", asset, days)
	v, _ := h.cachedFetch(key, stableOnchainTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		return h.stable.buildOnchain(ctx, asset, days), nil
	})
	writeJSON(w, v)
}
