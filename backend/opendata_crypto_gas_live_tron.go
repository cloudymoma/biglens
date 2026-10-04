package main

// TRC-20 USDT transfer burn cost for the TRON live bar (gas_fee_design.md §9).
// A sender with no staked or rented resources burns TRX at the governance
// prices for the energy and bandwidth the transfer consumes. Prices come from
// TronGrid (5-minute cache); the TRX→USD spot is applied at assembly.

import (
	"context"
)

const (
	// Modal USDT transfer energy in BigQuery receipts, stable for 90+ days:
	// recipient already holds USDT vs. a brand-new recipient.
	tronUSDTEnergyHolder = 64_285
	tronUSDTEnergyNew    = 130_285
	// tronUSDTBandwidth is the commonly cited transfer size (not re-measured;
	// ~5% of the cost), shown with "≈" in the UI.
	tronUSDTBandwidth = 345
)

type tronLiveRaw struct {
	EnergySun    int64
	BandwidthSun int64
	EnergySince  string
}

type TronTransferCost struct {
	Label     string   `json:"label"`
	Energy    int64    `json:"energy"`
	Bandwidth int64    `json:"bandwidth"`
	BurnTRX   float64  `json:"burn_trx"`
	BurnUSD   *float64 `json:"burn_usd"`
}

type TronLive struct {
	EnergyPriceSun    int64              `json:"energy_price_sun"`
	EnergyPriceSince  string             `json:"energy_price_since"`
	BandwidthPriceSun int64              `json:"bandwidth_price_sun"`
	Costs             []TronTransferCost `json:"costs"`
	Note              string             `json:"note"`
}

func fetchTronLiveRaw(ctx context.Context) (*tronLiveRaw, error) {
	energy, err := fetchTronEnergyPrices(ctx)
	if err != nil {
		return nil, err
	}
	bandwidth, err := fetchTronPrices(ctx, tronBandwidthPricesURL)
	if err != nil {
		return nil, err
	}
	cur := energy[len(energy)-1]
	return &tronLiveRaw{
		EnergySun:    cur.Sun,
		BandwidthSun: bandwidth[len(bandwidth)-1].Sun,
		EnergySince:  tronTime(cur.At),
	}, nil
}

func tronLiveFrom(raw tronLiveRaw, trxUSD *float64) TronLive {
	cost := func(label string, energy int64) TronTransferCost {
		burn := float64(energy*raw.EnergySun+tronUSDTBandwidth*raw.BandwidthSun) / 1e6
		c := TronTransferCost{Label: label, Energy: energy, Bandwidth: tronUSDTBandwidth, BurnTRX: burn}
		if trxUSD != nil {
			usd := burn * *trxUSD
			c.BurnUSD = &usd
		}
		return c
	}
	return TronLive{
		EnergyPriceSun:    raw.EnergySun,
		EnergyPriceSince:  raw.EnergySince,
		BandwidthPriceSun: raw.BandwidthSun,
		Costs: []TronTransferCost{
			cost("USDT to an existing holder", tronUSDTEnergyHolder),
			cost("USDT to a new address", tronUSDTEnergyNew),
		},
		Note: "Burned only when the sender has no staked or rented energy and bandwidth",
	}
}
