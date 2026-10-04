package main

// Daily calibration of the transfer profiles the live bars price
// (gas_fee_design.md §11), replacing Phase 2–3 constants:
//   - USDT (TRC-20) energy: the two modal values over the last 24h of TRON
//     receipts — updating an existing balance vs. writing a new one.
//   - USDT bandwidth: computed from one recent sample transaction of each
//     kind via TronGrid (java-tron's size rule, verified against net_usage).
//   - USDC transfer gas: median over the last 6h of Ethereum transactions,
//     applied to every EVM chain in the ladder.
// About 1.46 GB of BigQuery per run; callers cache the result for 24h.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
)

const (
	// TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t as stored in the BigQuery TRON dataset.
	tronUSDTReceiptAddress = "0xa614f803b6fd780986a42c78ec9c7f77e6ded13c"
	ethUSDCAddress         = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	// tronResultBytesPerContract is java-tron's MAX_RESULT_SIZE_IN_TX, charged
	// as bandwidth on top of the serialized transaction.
	tronResultBytesPerContract = 64
)

var tronTxByIDURL = "https://api.trongrid.io/wallet/gettransactionbyid"

func tronUSDTModesSQL() string {
	return fmt.Sprintf(`
		SELECT gas_used AS energy, COUNT(*) AS n
		FROM %s
		WHERE block_timestamp >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 24 HOUR)
			AND to_address = '%s' AND status = 1 AND gas_used > 0
		GROUP BY energy ORDER BY n DESC LIMIT 2`, tronReceiptsTable, tronUSDTReceiptAddress)
}

func tronUSDTSamplesSQL() string {
	return fmt.Sprintf(`
		SELECT gas_used AS energy, ANY_VALUE(transaction_hash) AS tx_hash
		FROM %s
		WHERE block_timestamp >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 15 MINUTE)
			AND to_address = '%s' AND status = 1 AND gas_used IN UNNEST(@energies)
		GROUP BY energy`, tronReceiptsTable, tronUSDTReceiptAddress)
}

func usdcTransferGasSQL() string {
	return fmt.Sprintf(`
		SELECT APPROX_QUANTILES(receipt_gas_used, 100)[OFFSET(50)] AS gas, COUNT(*) AS n
		FROM %s
		WHERE block_timestamp >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 6 HOUR)
			AND to_address = '%s' AND STARTS_WITH(input, '0xa9059cbb') AND receipt_status = 1`,
		ethTxTable, ethUSDCAddress)
}

type usdtEnergyMode struct {
	Energy int64 `bigquery:"energy"`
	N      int64 `bigquery:"n"`
}

type usdtSample struct {
	Energy int64  `bigquery:"energy"`
	TxHash string `bigquery:"tx_hash"`
}

type usdcGasRow struct {
	Gas bigquery.NullInt64 `bigquery:"gas"`
	N   int64              `bigquery:"n"`
}

type TransferProfile struct {
	Energy    int64   `json:"energy"`
	Bandwidth int64   `json:"bandwidth"`
	SharePct  float64 `json:"share_pct"`
}

type GasCalibration struct {
	USDTHolder      TransferProfile `json:"usdt_holder"`
	USDTNew         TransferProfile `json:"usdt_new"`
	USDCTransferGas int64           `json:"usdc_transfer_gas"`
	USDCSamples     int64           `json:"usdc_samples"`
	MeasuredAt      string          `json:"measured_at"`
	Windows         string          `json:"windows"`
}

// usdtProfilesFromModes splits the two modal energies by size: writing a new
// balance slot always costs more than updating one, whatever the day's mix.
func usdtProfilesFromModes(modes []usdtEnergyMode) (holder, newAddr TransferProfile, err error) {
	if len(modes) < 2 {
		return holder, newAddr, fmt.Errorf("usdt calibration: need two modal energies, got %d", len(modes))
	}
	two := []usdtEnergyMode{modes[0], modes[1]}
	sort.Slice(two, func(i, j int) bool { return two[i].Energy < two[j].Energy })
	total := float64(two[0].N + two[1].N)
	holder = TransferProfile{Energy: two[0].Energy, SharePct: float64(two[0].N) / total * 100}
	newAddr = TransferProfile{Energy: two[1].Energy, SharePct: float64(two[1].N) / total * 100}
	return holder, newAddr, nil
}

func protoVarintLen(n int) int {
	l := 1
	for n >= 0x80 {
		n >>= 7
		l++
	}
	return l
}

// tronTxBandwidth applies java-tron's rule: the serialized transaction without
// its ret field (raw_data = field 1, each signature = field 2) plus 64 bytes
// per contract. A TRC-20 transfer has exactly one contract.
func tronTxBandwidth(rawHex string, sigs []string) (int64, error) {
	raw, err := hex.DecodeString(rawHex)
	if err != nil || len(raw) == 0 {
		return 0, fmt.Errorf("tron tx: bad raw_data_hex")
	}
	size := 1 + protoVarintLen(len(raw)) + len(raw)
	for _, s := range sigs {
		sig, err := hex.DecodeString(s)
		if err != nil {
			return 0, fmt.Errorf("tron tx: bad signature hex")
		}
		size += 1 + protoVarintLen(len(sig)) + len(sig)
	}
	return int64(size + tronResultBytesPerContract), nil
}

func fetchTronTxBandwidth(ctx context.Context, txHash string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, tronFetchTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"value": strings.ToLower(strings.TrimPrefix(txHash, "0x"))})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tronTxByIDURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("tron tx request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := tronHTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("tron tx fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tron tx fetch: upstream status %d", resp.StatusCode)
	}
	var tx struct {
		RawDataHex string   `json:"raw_data_hex"`
		Signature  []string `json:"signature"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tx); err != nil {
		return 0, fmt.Errorf("tron tx decode: %w", err)
	}
	return tronTxBandwidth(tx.RawDataHex, tx.Signature)
}

func (b *BQClient) GetUSDTEnergyModes(ctx context.Context) ([]usdtEnergyMode, error) {
	return collectRows[usdtEnergyMode](b.client.Query(tronUSDTModesSQL()), ctx)
}

func (b *BQClient) GetUSDTSamples(ctx context.Context, energies []int64) ([]usdtSample, error) {
	q := b.client.Query(tronUSDTSamplesSQL())
	q.Parameters = []bigquery.QueryParameter{{Name: "energies", Value: energies}}
	return collectRows[usdtSample](q, ctx)
}

func (b *BQClient) GetUSDCTransferGas(ctx context.Context) (*usdcGasRow, error) {
	rows, err := collectRows[usdcGasRow](b.client.Query(usdcTransferGasSQL()), ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || !rows[0].Gas.Valid || rows[0].N == 0 {
		return nil, fmt.Errorf("usdc calibration: no transfers in the last 6h")
	}
	return &rows[0], nil
}

// fetchGasCalibration runs every step; any failure fails the whole run so the
// caller never mixes measured and missing values.
func fetchGasCalibration(ctx context.Context, b *BQClient, now time.Time) (*GasCalibration, error) {
	modes, err := b.GetUSDTEnergyModes(ctx)
	if err != nil {
		return nil, fmt.Errorf("usdt energy modes: %w", err)
	}
	holder, newAddr, err := usdtProfilesFromModes(modes)
	if err != nil {
		return nil, err
	}
	samples, err := b.GetUSDTSamples(ctx, []int64{holder.Energy, newAddr.Energy})
	if err != nil {
		return nil, fmt.Errorf("usdt samples: %w", err)
	}
	byEnergy := make(map[int64]string, len(samples))
	for _, s := range samples {
		byEnergy[s.Energy] = s.TxHash
	}
	for _, p := range []*TransferProfile{&holder, &newAddr} {
		hash, ok := byEnergy[p.Energy]
		if !ok {
			return nil, fmt.Errorf("usdt calibration: no recent sample with %d energy", p.Energy)
		}
		if p.Bandwidth, err = fetchTronTxBandwidth(ctx, hash); err != nil {
			return nil, err
		}
	}
	usdc, err := b.GetUSDCTransferGas(ctx)
	if err != nil {
		return nil, err
	}
	return &GasCalibration{
		USDTHolder:      holder,
		USDTNew:         newAddr,
		USDCTransferGas: usdc.Gas.Int64,
		USDCSamples:     usdc.N,
		MeasuredAt:      now.UTC().Format(time.RFC3339),
		Windows:         "USDT: last 24h of TRON receipts · USDC: median of the last 6h on Ethereum",
	}, nil
}
