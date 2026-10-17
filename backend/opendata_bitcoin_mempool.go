package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

var btcCoinbaseStatsURL = "https://api.exchange.coinbase.com/products/BTC-USD/stats"

type btcSpotStats struct {
	PriceUSD     float64  `json:"price_usd"`
	Change24hPct *float64 `json:"change_24h_pct"`
}

type btcMempoolPool struct {
	PoolID       int64   `json:"poolId"`
	Name         string  `json:"name"`
	Link         string  `json:"link"`
	BlockCount   int64   `json:"blockCount"`
	Rank         int     `json:"rank"`
	EmptyBlocks  int64   `json:"emptyBlocks"`
	Slug         string  `json:"slug"`
	AvgMatchRate float64 `json:"avgMatchRate"`
	AvgFeeDelta  string  `json:"avgFeeDelta"`
}

type btcMempoolPoolsSummary struct {
	Pools                   []btcMempoolPool `json:"pools"`
	BlockCount              int64            `json:"blockCount"`
	LastEstimatedHashrate   float64          `json:"lastEstimatedHashrate"`
	LastEstimatedHashrate3d float64          `json:"lastEstimatedHashrate3d"`
	LastEstimatedHashrate1w float64          `json:"lastEstimatedHashrate1w"`
}

func btcMempoolEndpoint(path string) string {
	base := strings.TrimRight(mempoolBaseURL, "/")
	if base == "" {
		base = defaultMempoolBaseURL
	}
	return base + path
}

func fetchMempoolDifficultyAdj(ctx context.Context) (*BtcDifficultyAdj, string) {
	var raw struct {
		ProgressPercent       float64 `json:"progressPercent"`
		DifficultyChange      float64 `json:"difficultyChange"`
		EstimatedRetargetDate int64   `json:"estimatedRetargetDate"`
		RemainingBlocks       int64   `json:"remainingBlocks"`
		PreviousRetarget      float64 `json:"previousRetarget"`
		NextRetargetHeight    int64   `json:"nextRetargetHeight"`
		TimeAvg               float64 `json:"timeAvg"`
		AdjustedTimeAvg       float64 `json:"adjustedTimeAvg"`
	}
	if code := btcGetJSON(ctx, btcMempoolEndpoint("/api/v1/difficulty-adjustment"), btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	avgMs := raw.AdjustedTimeAvg
	if avgMs <= 0 {
		avgMs = raw.TimeAvg
	}
	avgSec := avgMs / 1000.0
	if avgSec <= 0 {
		avgSec = btcDefaultBlockSec
	}
	var retargetAt string
	if raw.EstimatedRetargetDate > 0 {
		retargetAt = time.UnixMilli(raw.EstimatedRetargetDate).UTC().Format(time.RFC3339)
	}
	return &BtcDifficultyAdj{
		ProgressPct:        raw.ProgressPercent,
		EstChangePct:       raw.DifficultyChange,
		PrevChangePct:      raw.PreviousRetarget,
		RemainingBlocks:    raw.RemainingBlocks,
		EstRetargetAt:      retargetAt,
		NextRetargetHeight: raw.NextRetargetHeight,
		AvgBlockSeconds:    avgSec,
	}, ""
}

func fetchMempoolPoolsSummary(ctx context.Context, period string) (*btcMempoolPoolsSummary, string) {
	if period == "" {
		period = "1w"
	}
	var out btcMempoolPoolsSummary
	if code := btcGetJSON(ctx, btcMempoolEndpoint("/api/v1/mining/pools/"+period), btcDefaultMaxBody, &out); code != "" {
		return nil, code
	}
	return &out, ""
}

func fetchCoinbaseBTCStats(ctx context.Context) (*btcSpotStats, string) {
	var raw struct {
		Open string `json:"open"`
		Last string `json:"last"`
	}
	if code := btcGetJSON(ctx, btcCoinbaseStatsURL, btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	last, err := strconv.ParseFloat(raw.Last, 64)
	if err != nil || last <= 0 {
		return nil, "bad_response"
	}
	out := &btcSpotStats{PriceUSD: last}
	if open, err := strconv.ParseFloat(raw.Open, 64); err == nil && open > 0 {
		chg := (last/open - 1.0) * 100.0
		out.Change24hPct = &chg
	}
	return out, ""
}

func fetchMempoolBTCPrice(ctx context.Context) (*float64, string) {
	var raw struct {
		USD float64 `json:"USD"`
	}
	if code := btcGetJSON(ctx, btcMempoolEndpoint("/api/v1/prices"), btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	if raw.USD <= 0 {
		return nil, "bad_response"
	}
	v := raw.USD
	return &v, ""
}

// mempoolStatsFeeLevels is the 39-bucket fee-rate boundary list (sat/vB) from
// mempool.space's frontend/src/app/app.constants.ts (`feeLevels`).
var mempoolStatsFeeLevels = []int{
	0, 1, 2, 3, 4, 5, 6, 8, 10, 12,
	15, 20, 30, 40, 50, 60, 70, 80, 90, 100,
	125, 150, 175, 200, 250, 300, 350, 400, 500, 600,
	700, 800, 900, 1000, 1200, 1400, 1600, 1800, 2000,
}

const (
	btcConsolidationMaxSatVB  = 5.0
	btcConsolidationMaxBlocks = 3.0
)

type BtcTxTemplateCost struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Vbytes        int64    `json:"vbytes"`
	NextBlockSats int64    `json:"next_block_sats"`
	NextBlockUSD  *float64 `json:"next_block_usd"`
	HalfHourSats  int64    `json:"half_hour_sats"`
	HalfHourUSD   *float64 `json:"half_hour_usd"`
	EconomySats   int64    `json:"economy_sats"`
	EconomyUSD    *float64 `json:"economy_usd"`
}

type BtcConsolidationSignal struct {
	Open             bool    `json:"open"`
	EconomySatVB     float64 `json:"economy_sat_vb"`
	ThresholdSatVB   float64 `json:"threshold_sat_vb"`
	BlocksToClear    float64 `json:"blocks_to_clear"`
	MaxBlocksToClear float64 `json:"max_blocks_to_clear"`
	Reason           string  `json:"reason"`
}

type BtcMempoolStatPoint struct {
	TS            string  `json:"ts"`
	TotalVMB      float64 `json:"total_vmb"`
	Band0To1VMB   float64 `json:"band_0_1_vmb"`
	Band1To2VMB   float64 `json:"band_1_2_vmb"`
	Band2To5VMB   float64 `json:"band_2_5_vmb"`
	Band5To10VMB  float64 `json:"band_5_10_vmb"`
	Band10To50VMB float64 `json:"band_10_50_vmb"`
	Band50PlusVMB float64 `json:"band_50_plus_vmb"`
}

type BtcBlockFeeRatePoint struct {
	TS        string  `json:"ts"`
	AvgHeight int64   `json:"avg_height"`
	P10SatVB  float64 `json:"p10_sat_vb"`
	P25SatVB  float64 `json:"p25_sat_vb"`
	P50SatVB  float64 `json:"p50_sat_vb"`
	P75SatVB  float64 `json:"p75_sat_vb"`
	P90SatVB  float64 `json:"p90_sat_vb"`
}

var btcTxTemplates = []struct {
	ID     string
	Label  string
	Vbytes int64
}{
	{ID: "standard_p2wpkh", Label: "Standard transfer (P2WPKH)", Vbytes: 140},
	{ID: "p2tr_keypath", Label: "P2TR key-path, 1-in / 2-out", Vbytes: 154},
	{ID: "p2wsh_multisig", Label: "P2WSH 2-of-3 multisig, 1-in / 2-out", Vbytes: 201},
	{ID: "p2pkh_legacy", Label: "P2PKH legacy, 1-in / 2-out", Vbytes: 226},
	{ID: "exchange_batch", Label: "Exchange batch payout, 1-in / 50-out P2WPKH", Vbytes: 1629},
	{ID: "utxo_consolidation", Label: "UTXO consolidation, 50-in P2WPKH / 1-out", Vbytes: 3442},
}

func computeBtcTxTemplateCosts(fastest, halfHour, economy float64, priceUSD *float64) []BtcTxTemplateCost {
	satsUSD := func(sats int64) *float64 {
		if priceUSD == nil || *priceUSD <= 0 {
			return nil
		}
		v := (float64(sats) / 1e8) * (*priceUSD)
		return &v
	}
	out := make([]BtcTxTemplateCost, 0, len(btcTxTemplates))
	for _, tpl := range btcTxTemplates {
		vb := float64(tpl.Vbytes)
		nbSats := int64(math.Round(fastest * vb))
		hhSats := int64(math.Round(halfHour * vb))
		ecSats := int64(math.Round(economy * vb))
		out = append(out, BtcTxTemplateCost{
			ID:            tpl.ID,
			Label:         tpl.Label,
			Vbytes:        tpl.Vbytes,
			NextBlockSats: nbSats,
			NextBlockUSD:  satsUSD(nbSats),
			HalfHourSats:  hhSats,
			HalfHourUSD:   satsUSD(hhSats),
			EconomySats:   ecSats,
			EconomyUSD:    satsUSD(ecSats),
		})
	}
	return out
}

func consolidationSignal(economySatVB, blocksToClear float64) BtcConsolidationSignal {
	sig := BtcConsolidationSignal{
		EconomySatVB:     economySatVB,
		ThresholdSatVB:   btcConsolidationMaxSatVB,
		BlocksToClear:    blocksToClear,
		MaxBlocksToClear: btcConsolidationMaxBlocks,
	}
	if economySatVB <= 0 {
		sig.Reason = "Mempool fee telemetry unavailable"
		return sig
	}
	if economySatVB <= btcConsolidationMaxSatVB && blocksToClear <= btcConsolidationMaxBlocks {
		sig.Open = true
		sig.Reason = fmt.Sprintf("Economy fee %.2f sat/vB ≤ %.0f sat/vB and backlog %.1f blocks ≤ %.0f blocks",
			economySatVB, btcConsolidationMaxSatVB, blocksToClear, btcConsolidationMaxBlocks)
		return sig
	}
	if economySatVB > btcConsolidationMaxSatVB {
		sig.Reason = fmt.Sprintf("Economy fee %.2f sat/vB exceeds %.0f sat/vB threshold",
			economySatVB, btcConsolidationMaxSatVB)
		return sig
	}
	sig.Reason = fmt.Sprintf("Mempool backlog %.1f blocks exceeds %.0f-block threshold",
		blocksToClear, btcConsolidationMaxBlocks)
	return sig
}

// foldMempoolStats folds mempool.space's 39-bucket vsizes array (in vbytes)
// into 6 display bands in vMB: [0-1), [1-2), [2-5), [5-10), [10-50), [50+).
func foldMempoolStats(ts string, vsizes []float64) (BtcMempoolStatPoint, error) {
	if len(vsizes) != len(mempoolStatsFeeLevels) {
		return BtcMempoolStatPoint{}, fmt.Errorf("vsizes len = %d, want %d", len(vsizes), len(mempoolStatsFeeLevels))
	}
	pt := BtcMempoolStatPoint{TS: ts}
	for i, vb := range vsizes {
		if vb <= 0 {
			continue
		}
		vmb := vb / 1e6
		pt.TotalVMB += vmb
		lvl := mempoolStatsFeeLevels[i]
		switch {
		case lvl < 1:
			pt.Band0To1VMB += vmb
		case lvl < 2:
			pt.Band1To2VMB += vmb
		case lvl < 5:
			pt.Band2To5VMB += vmb
		case lvl < 10:
			pt.Band5To10VMB += vmb
		case lvl < 50:
			pt.Band10To50VMB += vmb
		default:
			pt.Band50PlusVMB += vmb
		}
	}
	return pt, nil
}

func fetchMempoolStats(ctx context.Context, period string) ([]BtcMempoolStatPoint, string) {
	switch period {
	case "24h", "1w", "1m":
	default:
		period = "24h"
	}
	var raw []struct {
		Added  int64     `json:"added"`
		Vsizes []float64 `json:"vsizes"`
	}
	if code := btcGetJSON(ctx, btcMempoolEndpoint("/api/v1/statistics/"+period), btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	if len(raw) == 0 {
		return nil, "bad_response"
	}
	slices.SortFunc(raw, func(a, b struct {
		Added  int64     `json:"added"`
		Vsizes []float64 `json:"vsizes"`
	}) int {
		if a.Added < b.Added {
			return -1
		}
		if a.Added > b.Added {
			return 1
		}
		return 0
	})
	out := make([]BtcMempoolStatPoint, 0, len(raw))
	for _, r := range raw {
		ts := time.Unix(r.Added, 0).UTC().Format(time.RFC3339)
		pt, err := foldMempoolStats(ts, r.Vsizes)
		if err != nil {
			return nil, "bad_response"
		}
		out = append(out, pt)
	}
	return out, ""
}

func fetchBlockFeeRates(ctx context.Context, period string) ([]BtcBlockFeeRatePoint, string) {
	reqPeriod := period
	switch reqPeriod {
	case "24h", "1w", "1m", "3m":
	default:
		reqPeriod = "1w"
	}
	var raw []struct {
		AvgHeight int64   `json:"avgHeight"`
		Timestamp int64   `json:"timestamp"`
		AvgFee10  float64 `json:"avgFee_10"`
		AvgFee25  float64 `json:"avgFee_25"`
		AvgFee50  float64 `json:"avgFee_50"`
		AvgFee75  float64 `json:"avgFee_75"`
		AvgFee90  float64 `json:"avgFee_90"`
	}
	if code := btcGetJSON(ctx, btcMempoolEndpoint("/api/v1/mining/blocks/fee-rates/"+reqPeriod), btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	if len(raw) == 0 {
		return nil, "bad_response"
	}
	slices.SortFunc(raw, func(a, b struct {
		AvgHeight int64   `json:"avgHeight"`
		Timestamp int64   `json:"timestamp"`
		AvgFee10  float64 `json:"avgFee_10"`
		AvgFee25  float64 `json:"avgFee_25"`
		AvgFee50  float64 `json:"avgFee_50"`
		AvgFee75  float64 `json:"avgFee_75"`
		AvgFee90  float64 `json:"avgFee_90"`
	}) int {
		if a.Timestamp < b.Timestamp {
			return -1
		}
		if a.Timestamp > b.Timestamp {
			return 1
		}
		return 0
	})
	out := make([]BtcBlockFeeRatePoint, 0, len(raw))
	for _, r := range raw {
		out = append(out, BtcBlockFeeRatePoint{
			TS:        time.Unix(r.Timestamp, 0).UTC().Format(time.RFC3339),
			AvgHeight: r.AvgHeight,
			P10SatVB:  r.AvgFee10,
			P25SatVB:  r.AvgFee25,
			P50SatVB:  r.AvgFee50,
			P75SatVB:  r.AvgFee75,
			P90SatVB:  r.AvgFee90,
		})
	}
	return out, ""
}
