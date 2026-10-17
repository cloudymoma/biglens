package main

import (
	"context"
	"sort"
)

var btcCGTreasuriesURL = "https://api.coingecko.com/api/v3/companies/public_treasury/bitcoin"

const btcMaxTreasuryCompanies = 20

// BtcTreasuryCompany represents one public company holding BTC in its treasury.
type BtcTreasuryCompany struct {
	Name                 string  `json:"name"`
	Symbol               string  `json:"symbol"`
	Country              string  `json:"country"`
	TotalHoldingsBTC     float64 `json:"total_holdings_btc"`
	TotalEntryValueUSD   float64 `json:"total_entry_value_usd"`
	TotalCurrentValueUSD float64 `json:"total_current_value_usd"`
	PctOfTotalSupply     float64 `json:"pct_of_total_supply"`
}

// BtcTreasuriesSummary aggregates CoinGecko's public BTC treasuries response (top 20 companies).
type BtcTreasuriesSummary struct {
	TotalHoldingsBTC      float64              `json:"total_holdings_btc"`
	TotalValueUSD         float64              `json:"total_value_usd"`
	MarketCapDominancePct float64              `json:"market_cap_dominance_pct"`
	Companies             []BtcTreasuryCompany `json:"companies"`
}

type cgRawTreasuriesResponse struct {
	TotalHoldings      float64 `json:"total_holdings"`
	TotalValueUSD      float64 `json:"total_value_usd"`
	MarketCapDominance float64 `json:"market_cap_dominance"`
	Companies          []struct {
		Name                    string  `json:"name"`
		Symbol                  string  `json:"symbol"`
		Country                 string  `json:"country"`
		TotalHoldings           float64 `json:"total_holdings"`
		TotalEntryValueUSD      float64 `json:"total_entry_value_usd"`
		TotalCurrentValueUSD    float64 `json:"total_current_value_usd"`
		PercentageOfTotalSupply float64 `json:"percentage_of_total_supply"`
	} `json:"companies"`
}

// fetchCGTreasuries fetches CoinGecko's public BTC treasury holdings and keeps the top 20 companies by BTC held.
func fetchCGTreasuries(ctx context.Context) (*BtcTreasuriesSummary, string) {
	var raw cgRawTreasuriesResponse
	if code := btcGetJSON(ctx, btcCGTreasuriesURL, btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	companies := make([]BtcTreasuryCompany, 0, len(raw.Companies))
	for _, c := range raw.Companies {
		if c.TotalHoldings <= 0 {
			continue
		}
		companies = append(companies, BtcTreasuryCompany{
			Name:                 c.Name,
			Symbol:               c.Symbol,
			Country:              c.Country,
			TotalHoldingsBTC:     c.TotalHoldings,
			TotalEntryValueUSD:   c.TotalEntryValueUSD,
			TotalCurrentValueUSD: c.TotalCurrentValueUSD,
			PctOfTotalSupply:     c.PercentageOfTotalSupply,
		})
	}
	sort.Slice(companies, func(i, j int) bool {
		return companies[i].TotalHoldingsBTC > companies[j].TotalHoldingsBTC
	})
	if len(companies) > btcMaxTreasuryCompanies {
		companies = companies[:btcMaxTreasuryCompanies]
	}
	return &BtcTreasuriesSummary{
		TotalHoldingsBTC:      raw.TotalHoldings,
		TotalValueUSD:         raw.TotalValueUSD,
		MarketCapDominancePct: raw.MarketCapDominance,
		Companies:             companies,
	}, ""
}
