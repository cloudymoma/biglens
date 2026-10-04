package main

// Daily calibration of the transfer profiles the live bars price
// (gas_fee_design.md §11), replacing Phase 2–3 constants:
//   - USDT (TRC-20) energy: the two modal values over the last 24h of TRON
//     receipts — updating an existing balance vs. writing a new one.
//   - USDT bandwidth: the most common size among up to five recent sample
//     transactions of each kind via TronGrid (java-tron's size rule, verified
//     against net_usage); one sample alone can be an unusually short encoding.
//   - USDC transfer gas: median over the last 6h of Ethereum transactions,
//     applied to every EVM chain in the ladder.
// About 1.3 GB of BigQuery per day. The BigQuery and TronGrid halves are
// cached separately (24h), and a failed half is retried only after a back-off.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// usdtSamplesPerCase is how many candidate hashes BigQuery returns per
	// transfer case; usdtSampleTarget is how many are actually measured.
	usdtSamplesPerCase = 5
	usdtSampleTarget   = 3
)

// usdtSamplePause spaces TronGrid lookups: its keyless API answers bursts
// with 429 (seen live during Phase 4 verification).
var usdtSamplePause = 250 * time.Millisecond

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
		SELECT gas_used AS energy, ARRAY_AGG(transaction_hash LIMIT %d) AS tx_hashes
		FROM %s
		WHERE block_timestamp >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 15 MINUTE)
			AND to_address = '%s' AND status = 1 AND gas_used IN UNNEST(@energies)
		GROUP BY energy`, usdtSamplesPerCase, tronReceiptsTable, tronUSDTReceiptAddress)
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
	Energy   int64    `bigquery:"energy"`
	TxHashes []string `bigquery:"tx_hashes"`
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

// modeBandwidth returns the most common value; a tie goes to the larger,
// so the burn cost is never understated.
func modeBandwidth(values []int64) int64 {
	counts := make(map[int64]int, len(values))
	var best int64
	for _, v := range values {
		counts[v]++
		if c, bc := counts[v], counts[best]; c > bc || (c == bc && v > best) {
			best = v
		}
	}
	return best
}

// sampleBandwidth measures sample transactions one at a time, pausing between
// TronGrid lookups and stopping once usdtSampleTarget have succeeded; failed
// lookups are skipped. It fails only if none succeed.
func sampleBandwidth(ctx context.Context, hashes []string) (int64, error) {
	var sizes []int64
	var errs []error
	for i, h := range hashes {
		if len(sizes) == usdtSampleTarget {
			break
		}
		if i > 0 && usdtSamplePause > 0 {
			select {
			case <-time.After(usdtSamplePause):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		bw, err := fetchTronTxBandwidth(ctx, h)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sizes = append(sizes, bw)
	}
	if len(sizes) == 0 {
		return 0, fmt.Errorf("usdt bandwidth: no sample could be measured (%d tried): %w", len(errs), errors.Join(errs...))
	}
	return modeBandwidth(sizes), nil
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

// gasCalibrationSamples is the BigQuery half of the calibration (~1.3 GB):
// the modal USDT energies with candidate sample hashes, and the USDC transfer
// gas median. Cached on its own so a TronGrid failure never re-runs it.
type gasCalibrationSamples struct {
	Holder, New             TransferProfile
	HolderHashes, NewHashes []string
	USDCGas, USDCSamples    int64
}

func fetchCalibrationSamples(ctx context.Context, b *BQClient) (*gasCalibrationSamples, error) {
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
	byEnergy := make(map[int64][]string, len(samples))
	for _, s := range samples {
		byEnergy[s.Energy] = s.TxHashes
	}
	for _, e := range []int64{holder.Energy, newAddr.Energy} {
		if len(byEnergy[e]) == 0 {
			return nil, fmt.Errorf("usdt calibration: no recent sample with %d energy", e)
		}
	}
	usdc, err := b.GetUSDCTransferGas(ctx)
	if err != nil {
		return nil, err
	}
	return &gasCalibrationSamples{
		Holder: holder, New: newAddr,
		HolderHashes: byEnergy[holder.Energy], NewHashes: byEnergy[newAddr.Energy],
		USDCGas: usdc.Gas.Int64, USDCSamples: usdc.N,
	}, nil
}

// measureCalibration is the TronGrid half: it measures each case's bandwidth
// from its sample transactions; any failure fails the whole calibration so the
// caller never mixes measured and missing values.
func measureCalibration(ctx context.Context, s gasCalibrationSamples, now time.Time) (*GasCalibration, error) {
	holder, newAddr := s.Holder, s.New
	var err error
	if holder.Bandwidth, err = sampleBandwidth(ctx, s.HolderHashes); err != nil {
		return nil, err
	}
	if newAddr.Bandwidth, err = sampleBandwidth(ctx, s.NewHashes); err != nil {
		return nil, err
	}
	return &GasCalibration{
		USDTHolder:      holder,
		USDTNew:         newAddr,
		USDCTransferGas: s.USDCGas,
		USDCSamples:     s.USDCSamples,
		MeasuredAt:      now.UTC().Format(time.RFC3339),
		Windows:         "USDT: last 24h of TRON receipts · USDC: median of the last 6h on Ethereum",
	}, nil
}
