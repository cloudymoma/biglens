package main

import (
	"context"
	"time"
)

// Reserves (stablecoin.md §5.5, §7.2 tab 4). Tether publishes total assets,
// liabilities and shareholder equity in its transparency JSON; the supply
// sampler stores one row per update (stable_tether_snapshots, keyed by the
// update id), and the coverage ratio is computed from it.

// stableTetherHistoryMax bounds the history served (about a year of daily
// updates).
const stableTetherHistoryMax = 400

type StableTetherPoint struct {
	UpdateID             int64    `json:"update_id"`
	AsOf                 string   `json:"as_of"` // the update time (Tether's id)
	TotalAssetsUSD       *float64 `json:"total_assets_usd"`
	TotalLiabilitiesUSD  *float64 `json:"total_liabilities_usd"`
	ShareholderEquityUSD *float64 `json:"shareholder_equity_usd"`
	Coverage             *float64 `json:"coverage"` // total assets ÷ total liabilities
}

type StableTetherReserves struct {
	Latest  *StableTetherPoint  `json:"latest"`
	History []StableTetherPoint `json:"history"` // oldest first
}

// stableTetherCoverage is assets ÷ liabilities, null when either is unknown
// or liabilities are not positive.
func stableTetherCoverage(assets, liabilities *float64) *float64 {
	if assets == nil || liabilities == nil || !(*liabilities > 0) {
		return nil
	}
	return stableF(*assets / *liabilities)
}

// stableTetherReserves returns the newest limit updates, oldest first.
func (s *riskStore) stableTetherReserves(ctx context.Context, limit int) (StableTetherReserves, error) {
	out := StableTetherReserves{History: []StableTetherPoint{}}
	snaps, err := s.stableTetherSnapshots(ctx, limit)
	if err != nil {
		return out, err
	}
	for i := len(snaps) - 1; i >= 0; i-- { // stored newest first
		r := snaps[i]
		out.History = append(out.History, StableTetherPoint{UpdateID: r.UpdateID, AsOf: stableFmt(time.Unix(r.UpdateID, 0)),
			TotalAssetsUSD: r.TotalAssetsUSD, TotalLiabilitiesUSD: r.TotalLiabilitiesUSD, ShareholderEquityUSD: r.ShareholderEquityUSD,
			Coverage: stableTetherCoverage(r.TotalAssetsUSD, r.TotalLiabilitiesUSD)})
	}
	if n := len(out.History); n > 0 {
		latest := out.History[n-1]
		out.Latest = &latest
	}
	return out, nil
}
