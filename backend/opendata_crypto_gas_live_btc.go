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
	"sync"
	"time"
)

var (
	mempoolFeesURL      = "https://mempool.space/api/v1/fees/precise"
	mempoolStatsURL     = "https://mempool.space/api/mempool"
	mempoolProjectedURL = "https://mempool.space/api/v1/fees/mempool-blocks"
	mempoolRecentURL    = "https://mempool.space/api/v1/blocks"
	mempoolHTTPClient   = &http.Client{Timeout: 5 * time.Second}
)

const (
	mempoolFetchTimeout = 4 * time.Second
	// btcStandardTxVB prices a 1-input/2-output native SegWit transfer.
	btcStandardTxVB      = 140
	btcBlockVB           = 1_000_000
	btcConveyorProjected = 3
	btcConveyorRecent    = 5
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

// mempoolProjectedBlock is one block mempool.space expects to be mined next.
type mempoolProjectedBlock struct {
	BlockVSize float64   `json:"blockVSize"`
	NTx        int64     `json:"nTx"`
	TotalFees  int64     `json:"totalFees"`
	MedianFee  float64   `json:"medianFee"`
	FeeRange   []float64 `json:"feeRange"`
}

// mempoolMinedBlock is one recently mined block from /api/v1/blocks.
type mempoolMinedBlock struct {
	Height    int64 `json:"height"`
	Timestamp int64 `json:"timestamp"`
	TxCount   int64 `json:"tx_count"`
	Size      int64 `json:"size"`
	Weight    int64 `json:"weight"`
	Extras    struct {
		MedianFee float64 `json:"medianFee"`
		TotalFees int64   `json:"totalFees"`
	} `json:"extras"`
}

type btcLiveRaw struct {
	Fees      btcPreciseFees
	Mempool   btcMempoolStats
	Projected []mempoolProjectedBlock
	Recent    []mempoolMinedBlock
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

type BtcProjectedBlock struct {
	MedianFee   float64 `json:"median_fee"`
	MinFee      float64 `json:"min_fee"`
	MaxFee      float64 `json:"max_fee"`
	VsizeMB     float64 `json:"vsize_mb"`
	TxCount     int64   `json:"tx_count"`
	TotalFeeBTC float64 `json:"total_fee_btc"`
}

type BtcMinedBlock struct {
	Height      int64   `json:"height"`
	MinedAt     string  `json:"mined_at"`
	MedianFee   float64 `json:"median_fee"`
	SizeMB      float64 `json:"size_mb"`
	FullnessPct float64 `json:"fullness_pct"`
	TxCount     int64   `json:"tx_count"`
	TotalFeeBTC float64 `json:"total_fee_btc"`
}

type BtcLive struct {
	Tiers         []BtcFeeTier        `json:"tiers"`
	MinimumSatVB  float64             `json:"minimum_sat_vb"`
	TxCount       int64               `json:"tx_count"`
	VsizeMB       float64             `json:"vsize_mb"`
	BlocksToClear float64             `json:"blocks_to_clear"`
	TotalFeeBTC   float64             `json:"total_fee_btc"`
	Bands         []BtcFeeBand        `json:"bands"`
	StandardTxVB  int                 `json:"standard_tx_vb"`
	Projected     []BtcProjectedBlock `json:"projected"`
	Recent        []BtcMinedBlock     `json:"recent"`
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
	var (
		raw                btcLiveRaw
		feesErr, statsErr  error
		projErr, recentErr error
		wg                 sync.WaitGroup
	)
	wg.Go(func() { feesErr = mempoolGetJSON(ctx, mempoolFeesURL, &raw.Fees) })
	wg.Go(func() { statsErr = mempoolGetJSON(ctx, mempoolStatsURL, &raw.Mempool) })
	wg.Go(func() { projErr = mempoolGetJSON(ctx, mempoolProjectedURL, &raw.Projected) })
	wg.Go(func() { recentErr = mempoolGetJSON(ctx, mempoolRecentURL, &raw.Recent) })
	wg.Wait()
	if feesErr != nil {
		return nil, feesErr
	}
	if statsErr != nil {
		return nil, statsErr
	}
	// Conveyor endpoints are optional: if either fails, keep the fee tiers and
	// mempool backlog intact and hide only the missing half of the conveyor.
	if projErr != nil {
		raw.Projected = nil
	}
	if recentErr != nil {
		raw.Recent = nil
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

// btcConveyor keeps the next three projected blocks and the five newest
// mined blocks, in mempool.space's order.
func btcConveyor(projected []mempoolProjectedBlock, recent []mempoolMinedBlock) ([]BtcProjectedBlock, []BtcMinedBlock) {
	p := make([]BtcProjectedBlock, 0, btcConveyorProjected)
	for _, b := range projected[:min(len(projected), btcConveyorProjected)] {
		pb := BtcProjectedBlock{
			MedianFee:   b.MedianFee,
			VsizeMB:     b.BlockVSize / 1e6,
			TxCount:     b.NTx,
			TotalFeeBTC: float64(b.TotalFees) / 1e8,
		}
		if n := len(b.FeeRange); n > 0 {
			pb.MinFee, pb.MaxFee = b.FeeRange[0], b.FeeRange[n-1]
		}
		p = append(p, pb)
	}
	r := make([]BtcMinedBlock, 0, btcConveyorRecent)
	for _, b := range recent[:min(len(recent), btcConveyorRecent)] {
		r = append(r, BtcMinedBlock{
			Height:      b.Height,
			MinedAt:     time.Unix(b.Timestamp, 0).UTC().Format(time.RFC3339),
			MedianFee:   b.Extras.MedianFee,
			SizeMB:      float64(b.Size) / 1e6,
			FullnessPct: float64(b.Weight) / 4e6 * 100,
			TxCount:     b.TxCount,
			TotalFeeBTC: float64(b.Extras.TotalFees) / 1e8,
		})
	}
	return p, r
}

func btcLiveFrom(raw btcLiveRaw, btcUSD *float64) BtcLive {
	m := raw.Mempool
	projected, recent := btcConveyor(raw.Projected, raw.Recent)
	return BtcLive{
		Tiers:         btcFeeTiers(raw.Fees, btcUSD),
		MinimumSatVB:  raw.Fees.Minimum,
		TxCount:       m.Count,
		VsizeMB:       float64(m.Vsize) / 1e6,
		BlocksToClear: float64(m.Vsize) / btcBlockVB,
		TotalFeeBTC:   float64(m.TotalFee) / 1e8,
		Bands:         bandBtcMempool(m.FeeHistogram),
		StandardTxVB:  btcStandardTxVB,
		Projected:     projected,
		Recent:        recent,
	}
}
