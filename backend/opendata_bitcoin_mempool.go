package main

import (
	"context"
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
	Pools                  []btcMempoolPool `json:"pools"`
	BlockCount             int64            `json:"blockCount"`
	LastEstimatedHashrate  float64          `json:"lastEstimatedHashrate"`
	LastEstimatedHashrate3d float64         `json:"lastEstimatedHashrate3d"`
	LastEstimatedHashrate1w float64         `json:"lastEstimatedHashrate1w"`
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
