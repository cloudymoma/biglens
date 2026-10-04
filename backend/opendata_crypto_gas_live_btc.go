package main

// Bitcoin live fee quotes and mempool backlog from mempool.space's keyless
// API (gas_fee_design.md §9). /fees/precise keeps sub-1 sat/vB rates that
// /fees/recommended rounds up to 1. Callers cache the raw payload for 30s;
// USD conversion happens at assembly because the spot price has its own TTL.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var (
	mempoolFeesURL    = "https://mempool.space/api/v1/fees/precise"
	mempoolStatsURL   = "https://mempool.space/api/mempool"
	mempoolHTTPClient = &http.Client{Timeout: 5 * time.Second}
)

const (
	mempoolFetchTimeout = 4 * time.Second
	// btcStandardTxVB prices a 1-input/2-output native SegWit transfer.
	btcStandardTxVB = 140
	btcBlockVB      = 1_000_000
)

type btcPreciseFees struct {
	Fastest  float64 `json:"fastestFee"`
	HalfHour float64 `json:"halfHourFee"`
	Hour     float64 `json:"hourFee"`
	Economy  float64 `json:"economyFee"`
	Minimum  float64 `json:"minimumFee"`
}

// btcMempoolStats mirrors /api/mempool; FeeHistogram holds [fee-rate lower
// bound in sat/vB, vsize] pairs in descending fee order.
type btcMempoolStats struct {
	Count        int64        `json:"count"`
	Vsize        int64        `json:"vsize"`
	TotalFee     int64        `json:"total_fee"`
	FeeHistogram [][2]float64 `json:"fee_histogram"`
}

type btcLiveRaw struct {
	Fees    btcPreciseFees
	Mempool btcMempoolStats
}

type BtcFeeTier struct {
	Label string   `json:"label"`
	SatVB float64  `json:"sat_vb"`
	USD   *float64 `json:"usd"`
}

type BtcFeeBand struct {
	Label    string  `json:"label"`
	MinSatVB float64 `json:"min_sat_vb"`
	VsizeMB  float64 `json:"vsize_mb"`
}

type BtcLive struct {
	Tiers         []BtcFeeTier `json:"tiers"`
	MinimumSatVB  float64      `json:"minimum_sat_vb"`
	TxCount       int64        `json:"tx_count"`
	VsizeMB       float64      `json:"vsize_mb"`
	BlocksToClear float64      `json:"blocks_to_clear"`
	TotalFeeBTC   float64      `json:"total_fee_btc"`
	Bands         []BtcFeeBand `json:"bands"`
	StandardTxVB  int          `json:"standard_tx_vb"`
}

func mempoolGetJSON(ctx context.Context, url string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, mempoolFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("mempool.space request: %w", err)
	}
	resp, err := mempoolHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("mempool.space fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mempool.space fetch %s: upstream status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("mempool.space decode %s: %w", url, err)
	}
	return nil
}

func fetchBtcLiveRaw(ctx context.Context) (*btcLiveRaw, error) {
	var raw btcLiveRaw
	if err := mempoolGetJSON(ctx, mempoolFeesURL, &raw.Fees); err != nil {
		return nil, err
	}
	if err := mempoolGetJSON(ctx, mempoolStatsURL, &raw.Mempool); err != nil {
		return nil, err
	}
	return &raw, nil
}

// btcTxUSD prices a standard transfer at rate; nil when the spot is unknown.
func btcTxUSD(rate float64, btcUSD *float64) *float64 {
	if btcUSD == nil {
		return nil
	}
	usd := rate * btcStandardTxVB * *btcUSD / 1e8
	return &usd
}

func btcFeeTiers(f btcPreciseFees, btcUSD *float64) []BtcFeeTier {
	return []BtcFeeTier{
		{Label: "Next block", SatVB: f.Fastest, USD: btcTxUSD(f.Fastest, btcUSD)},
		{Label: "~30 min", SatVB: f.HalfHour, USD: btcTxUSD(f.HalfHour, btcUSD)},
		{Label: "Economy", SatVB: f.Economy, USD: btcTxUSD(f.Economy, btcUSD)},
	}
}

var btcBandFloors = []struct {
	label string
	floor float64
}{{"<1", 0}, {"1–2", 1}, {"2–5", 2}, {"5–10", 5}, {"10–20", 10}, {"20+", 20}}

// bandBtcMempool folds the ~200-step histogram into six ascending fee bands.
func bandBtcMempool(hist [][2]float64) []BtcFeeBand {
	if len(hist) == 0 {
		return []BtcFeeBand{}
	}
	bands := make([]BtcFeeBand, len(btcBandFloors))
	for i, b := range btcBandFloors {
		bands[i] = BtcFeeBand{Label: b.label, MinSatVB: b.floor}
	}
	for _, step := range hist {
		rate, vsize := step[0], step[1]
		i := len(btcBandFloors) - 1
		for i > 0 && rate < btcBandFloors[i].floor {
			i--
		}
		bands[i].VsizeMB += vsize / 1e6
	}
	return bands
}

func btcLiveFrom(raw btcLiveRaw, btcUSD *float64) BtcLive {
	m := raw.Mempool
	return BtcLive{
		Tiers:         btcFeeTiers(raw.Fees, btcUSD),
		MinimumSatVB:  raw.Fees.Minimum,
		TxCount:       m.Count,
		VsizeMB:       float64(m.Vsize) / 1e6,
		BlocksToClear: float64(m.Vsize) / btcBlockVB,
		TotalFeeBTC:   float64(m.TotalFee) / 1e8,
		Bands:         bandBtcMempool(m.FeeHistogram),
		StandardTxVB:  btcStandardTxVB,
	}
}
