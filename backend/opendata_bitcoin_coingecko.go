package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	btcCGTreasuriesURL  = "https://api.coingecko.com/api/v3/companies/public_treasury/bitcoin"
	btcCGGlobalURL      = "https://api.coingecko.com/api/v3/global"
	btcCGBitcoinURL     = "https://api.coingecko.com/api/v3/coins/bitcoin?localization=false&tickers=false&market_data=true&community_data=false&developer_data=false"
	btcCGTickersBaseURL = "https://api.coingecko.com/api/v3/coins/bitcoin/tickers"
	btcCGDerivativesURL = "https://api.coingecko.com/api/v3/derivatives"
)

const (
	btcMaxTreasuryCompanies = 20
	btcCG429Backoff         = 2 * time.Minute
)

// btcCuratedSpotVenues is the CoinGecko exchange_id list for the curated spot denominator (D14, §6.2).
var btcCuratedSpotVenues = []string{
	"binance",
	"gdax",
	"okex",
	"kraken",
	"bybit_spot",
	"bitstamp",
	"bitfinex",
	"upbit",
}

// btcCuratedDerivVenues is the canonical venue key list for the curated derivatives numerator.
var btcCuratedDerivVenues = []string{
	"binance",
	"okx",
	"coinbase",
	"kraken",
	"hyperliquid",
	"deribit",
	"bybit",
}

// btcCGSem caps concurrent outbound CoinGecko requests at 2 (§11.2).
var btcCGSem = make(chan struct{}, 2)

var (
	btcCGRateMu         sync.Mutex
	btcCGRateLimitUntil time.Time
)

func btcCGAcquire(ctx context.Context, now time.Time) string {
	btcCGRateMu.Lock()
	until := btcCGRateLimitUntil
	btcCGRateMu.Unlock()
	if now.Before(until) {
		return "rate_limited"
	}
	select {
	case btcCGSem <- struct{}{}:
		return ""
	case <-ctx.Done():
		return "timeout"
	}
}

func btcCGRelease(code string, now time.Time) {
	<-btcCGSem
	if code == "rate_limited" {
		btcCGRateMu.Lock()
		btcCGRateLimitUntil = now.Add(btcCG429Backoff)
		btcCGRateMu.Unlock()
	}
}

func resetBtcCGRateLimitForTest() {
	btcCGRateMu.Lock()
	btcCGRateLimitUntil = time.Time{}
	btcCGRateMu.Unlock()
}

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
	now := time.Now().UTC()
	if code := btcCGAcquire(ctx, now); code != "" {
		return nil, code
	}
	var raw cgRawTreasuriesResponse
	code := btcGetJSON(ctx, btcCGTreasuriesURL, btcDefaultMaxBody, &raw)
	btcCGRelease(code, now)
	if code != "" {
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

// BtcCGGlobal holds global crypto market cap and BTC dominance from CoinGecko /global.
type BtcCGGlobal struct {
	TotalMarketCapUSD float64 `json:"total_market_cap_usd"`
	TotalVolume24hUSD float64 `json:"total_volume_24h_usd"`
	BTCDominancePct   float64 `json:"btc_dominance_pct"`
}

func fetchCGGlobal(ctx context.Context) (*BtcCGGlobal, string) {
	now := time.Now().UTC()
	if code := btcCGAcquire(ctx, now); code != "" {
		return nil, code
	}
	var raw struct {
		Data struct {
			TotalMarketCap      map[string]float64 `json:"total_market_cap"`
			TotalVolume         map[string]float64 `json:"total_volume"`
			MarketCapPercentage map[string]float64 `json:"market_cap_percentage"`
		} `json:"data"`
	}
	code := btcGetJSON(ctx, btcCGGlobalURL, btcDefaultMaxBody, &raw)
	btcCGRelease(code, now)
	if code != "" {
		return nil, code
	}
	return &BtcCGGlobal{
		TotalMarketCapUSD: raw.Data.TotalMarketCap["usd"],
		TotalVolume24hUSD: raw.Data.TotalVolume["usd"],
		BTCDominancePct:   raw.Data.MarketCapPercentage["btc"],
	}, ""
}

// BtcCGBitcoin holds all-reported BTC spot volume and market cap from CoinGecko /coins/bitcoin.
type BtcCGBitcoin struct {
	PriceUSD                    float64 `json:"price_usd"`
	MarketCapUSD                float64 `json:"market_cap_usd"`
	AllReportedSpotVolume24hUSD float64 `json:"all_reported_spot_volume_24h_usd"`
	Change24hPct                float64 `json:"change_24h_pct"`
}

func fetchCGBitcoin(ctx context.Context) (*BtcCGBitcoin, string) {
	now := time.Now().UTC()
	if code := btcCGAcquire(ctx, now); code != "" {
		return nil, code
	}
	var raw struct {
		MarketData struct {
			CurrentPrice             map[string]float64 `json:"current_price"`
			MarketCap                map[string]float64 `json:"market_cap"`
			TotalVolume              map[string]float64 `json:"total_volume"`
			PriceChangePercentage24h float64            `json:"price_change_percentage_24h"`
		} `json:"market_data"`
	}
	code := btcGetJSON(ctx, btcCGBitcoinURL, btcDefaultMaxBody, &raw)
	btcCGRelease(code, now)
	if code != "" {
		return nil, code
	}
	return &BtcCGBitcoin{
		PriceUSD:                    raw.MarketData.CurrentPrice["usd"],
		MarketCapUSD:                raw.MarketData.MarketCap["usd"],
		AllReportedSpotVolume24hUSD: raw.MarketData.TotalVolume["usd"],
		Change24hPct:                raw.MarketData.PriceChangePercentage24h,
	}, ""
}

// BtcSpotVenueAgg aggregates spot 24h volume, ±2% depth, and spread for a single venue.
type BtcSpotVenueAgg struct {
	Venue        string   `json:"venue"`
	Spot24hUSD   float64  `json:"spot_24h_usd"`
	Depth2pctUSD *float64 `json:"depth_2pct_usd"`
	SpreadPct    *float64 `json:"spread_pct"`
	Pairs        int      `json:"pairs"`
}

type BtcCuratedSpotSummary struct {
	TotalSpot24hUSD float64                    `json:"total_spot_24h_usd"`
	ByVenue         map[string]BtcSpotVenueAgg `json:"by_venue"`
}

func normalizeCGSpotVenueID(identifier string) string {
	switch strings.ToLower(strings.TrimSpace(identifier)) {
	case "binance":
		return "binance"
	case "gdax", "coinbase", "coinbase_exchange":
		return "coinbase"
	case "okex", "okx":
		return "okx"
	case "kraken":
		return "kraken"
	case "bybit_spot", "bybit":
		return "bybit"
	case "bitstamp":
		return "bitstamp"
	case "bitfinex":
		return "bitfinex"
	case "upbit":
		return "upbit"
	default:
		return strings.ToLower(strings.TrimSpace(identifier))
	}
}

// fetchCGCuratedSpot fetches CoinGecko's /coins/bitcoin/tickers with depth=true for the curated spot venues.
func fetchCGCuratedSpot(ctx context.Context, exchangeIDs []string) (*BtcCuratedSpotSummary, string) {
	if len(exchangeIDs) == 0 {
		exchangeIDs = btcCuratedSpotVenues
	}
	now := time.Now().UTC()
	if code := btcCGAcquire(ctx, now); code != "" {
		return nil, code
	}
	u := btcCGTickersBaseURL
	if !strings.Contains(u, "?") {
		u = fmt.Sprintf("%s?exchange_ids=%s&depth=true", strings.TrimRight(u, "/"), strings.Join(exchangeIDs, ","))
	}
	var raw struct {
		Tickers []struct {
			Base   string `json:"base"`
			Target string `json:"target"`
			Market struct {
				Name       string `json:"name"`
				Identifier string `json:"identifier"`
			} `json:"market"`
			ConvertedVolume struct {
				USD float64 `json:"usd"`
			} `json:"converted_volume"`
			BidAskSpreadPercentage *float64 `json:"bid_ask_spread_percentage"`
			CostToMoveUpUSD        *float64 `json:"cost_to_move_up_usd"`
			CostToMoveDownUSD      *float64 `json:"cost_to_move_down_usd"`
			IsAnomaly              bool     `json:"is_anomaly"`
			IsStale                bool     `json:"is_stale"`
		} `json:"tickers"`
	}
	code := btcGetJSON(ctx, u, btcDefaultMaxBody, &raw)
	btcCGRelease(code, now)
	if code != "" {
		return nil, code
	}

	type accum struct {
		volUSD         float64
		upUSD          float64
		downUSD        float64
		hasDepth       bool
		spreadWeighted float64
		spreadVol      float64
		pairs          int
	}
	accMap := make(map[string]*accum)
	var totalSpot float64

	for _, tk := range raw.Tickers {
		if tk.IsAnomaly || tk.IsStale {
			continue
		}
		if !isBTCBaseSymbol(tk.Base) {
			continue
		}
		vol := tk.ConvertedVolume.USD
		if vol <= 0 {
			continue
		}
		vID := normalizeCGSpotVenueID(tk.Market.Identifier)
		if vID == "" {
			continue
		}
		a := accMap[vID]
		if a == nil {
			a = &accum{}
			accMap[vID] = a
		}
		a.volUSD += vol
		a.pairs++
		totalSpot += vol
		if tk.CostToMoveUpUSD != nil && tk.CostToMoveDownUSD != nil && (*tk.CostToMoveUpUSD > 0 || *tk.CostToMoveDownUSD > 0) {
			a.upUSD += *tk.CostToMoveUpUSD
			a.downUSD += *tk.CostToMoveDownUSD
			a.hasDepth = true
		}
		if tk.BidAskSpreadPercentage != nil && *tk.BidAskSpreadPercentage > 0 {
			a.spreadWeighted += *tk.BidAskSpreadPercentage * vol
			a.spreadVol += vol
		}
	}

	byVenue := make(map[string]BtcSpotVenueAgg, len(accMap))
	for vID, a := range accMap {
		agg := BtcSpotVenueAgg{
			Venue:      vID,
			Spot24hUSD: a.volUSD,
			Pairs:      a.pairs,
		}
		if a.hasDepth {
			agg.Depth2pctUSD = btcFloatPtr((a.upUSD + a.downUSD) / 2.0)
		}
		if a.spreadVol > 0 {
			agg.SpreadPct = btcFloatPtr(a.spreadWeighted / a.spreadVol)
		}
		byVenue[vID] = agg
	}
	return &BtcCuratedSpotSummary{
		TotalSpot24hUSD: totalSpot,
		ByVenue:         byVenue,
	}, ""
}

// BtcCGDerivativesSummary holds the stream-decoded BTC-index derivatives aggregates and per-venue fallbacks.
type BtcCGDerivativesSummary struct {
	AllReportedVolume24hUSD float64                `json:"all_reported_volume_24h_usd"`
	AllReportedOIUSD        float64                `json:"all_reported_oi_usd"`
	BTCIndexRows            int                    `json:"btc_index_rows"`
	VenuesCount             int                    `json:"venues_count"`
	FallbackVenues          map[string]BtcVenueRow `json:"fallback_venues"`
}

type cgRawDerivativeTicker struct {
	Market          string   `json:"market"`
	Symbol          string   `json:"symbol"`
	IndexID         string   `json:"index_id"`
	Price           string   `json:"price"`
	ContractType    string   `json:"contract_type"`
	Index           *float64 `json:"index"`
	Basis           *float64 `json:"basis"`
	Spread          *float64 `json:"spread"`
	FundingRate     *float64 `json:"funding_rate"`
	OpenInterest    *float64 `json:"open_interest"`
	Volume24h       any      `json:"volume_24h"`
	ConvertedVolume struct {
		USD any `json:"usd"`
	} `json:"converted_volume"`
	ExpiredAt *int64 `json:"expired_at"`
}

func isBTCBaseSymbol(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s == "BTC" || s == "XBT"
}

func mapCGDerivativeMarketToVenue(market string) (venueID, kind string, verifiedFunding8h bool) {
	switch strings.ToLower(strings.TrimSpace(market)) {
	case "binance (futures)":
		return "binance", "cex_offshore", true
	case "bybit (futures)", "bybit":
		return "bybit", "cex_offshore", true
	case "okx (futures)":
		return "okx", "cex_offshore", true
	case "deribit":
		return "deribit", "cex_offshore", true
	case "kraken (futures)":
		return "kraken", "cex_us", false
	case "hyperliquid (futures)", "hyperliquid":
		return "hyperliquid", "dex", false
	case "coinbase international exchange (derivatives)", "coinbase (derivatives)":
		return "coinbase", "cex_us", false
	default:
		return "", "", false
	}
}

func parseCGAnyFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		return parseBtcFloat(x)
	case nil:
		return 0, false
	default:
		return parseBtcFloat(fmt.Sprint(x))
	}
}

// fetchCGDerivativesBTC stream-decodes CoinGecko's /derivatives endpoint through
// a 32 MiB limit, keeping only unexpired BTC-index rows.
func fetchCGDerivativesBTC(ctx context.Context, now time.Time) (*BtcCGDerivativesSummary, string) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if code := btcCGAcquire(ctx, now); code != "" {
		return nil, code
	}

	type venueAcc struct {
		venueID           string
		kind              string
		verifiedFunding8h bool
		volUSD            float64
		oiUSD             float64
		markPx            float64
		funding8hPct      *float64
		fundingOIWeight   float64
	}
	venuesSeen := make(map[string]bool)
	byVenue := make(map[string]*venueAcc)
	var allVolUSD, allOIUSD float64
	var btcRows int
	nowUnix := now.UTC().Unix()

	code := btcStreamJSONArray(ctx, btcCGDerivativesURL, btcStreamMaxBody, func(tk cgRawDerivativeTicker) error {
		if !isBTCBaseSymbol(tk.IndexID) {
			return nil
		}
		if tk.ExpiredAt != nil && *tk.ExpiredAt > 0 && *tk.ExpiredAt <= nowUnix {
			return nil
		}
		btcRows++
		mkt := strings.TrimSpace(tk.Market)
		if mkt != "" {
			venuesSeen[strings.ToLower(mkt)] = true
		}

		volUSD, ok := parseCGAnyFloat(tk.Volume24h)
		if !ok || volUSD <= 0 {
			volUSD, _ = parseCGAnyFloat(tk.ConvertedVolume.USD)
		}
		var oiUSD float64
		if tk.OpenInterest != nil && *tk.OpenInterest > 0 {
			oiUSD = *tk.OpenInterest
		}
		if volUSD > 0 {
			allVolUSD += volUSD
		}
		if oiUSD > 0 {
			allOIUSD += oiUSD
		}

		vID, kind, verified8h := mapCGDerivativeMarketToVenue(mkt)
		if vID == "" {
			return nil
		}
		acc := byVenue[vID]
		if acc == nil {
			acc = &venueAcc{
				venueID:           vID,
				kind:              kind,
				verifiedFunding8h: verified8h,
			}
			byVenue[vID] = acc
		}
		acc.volUSD += volUSD
		acc.oiUSD += oiUSD
		if px, ok := parseBtcFloat(tk.Price); ok && px > 0 && acc.markPx <= 0 {
			acc.markPx = px
		} else if tk.Index != nil && *tk.Index > 0 && acc.markPx <= 0 {
			acc.markPx = *tk.Index
		}
		if verified8h && strings.EqualFold(tk.ContractType, "perpetual") && tk.FundingRate != nil {
			// CoinGecko /derivatives reports funding_rate in percentage points per 8h interval on verified venues.
			if oiUSD > acc.fundingOIWeight || acc.funding8hPct == nil {
				acc.funding8hPct = btcFloatPtr(*tk.FundingRate)
				acc.fundingOIWeight = oiUSD
			}
		}
		return nil
	})
	btcCGRelease(code, now)
	if code != "" {
		return nil, code
	}

	asOf := now.UTC().Format(time.RFC3339)
	fallbacks := make(map[string]BtcVenueRow, len(byVenue))
	for vID, acc := range byVenue {
		row := BtcVenueRow{
			Venue:       vID,
			Kind:        acc.kind,
			Deriv24hUSD: btcFloatPtr(acc.volUSD),
			OIUSD:       btcFloatPtr(acc.oiUSD),
			Sources: []SourceStatus{
				{
					ID:      vID,
					Label:   vID + " (via CoinGecko)",
					Status:  "ok",
					Via:     "coingecko",
					AsOf:    asOf,
					License: "attribution required",
				},
			},
		}
		if acc.markPx > 0 && acc.oiUSD > 0 {
			row.OIBTC = btcFloatPtr(acc.oiUSD / acc.markPx)
		}
		if acc.verifiedFunding8h && acc.funding8hPct != nil {
			row.Funding8hPct = acc.funding8hPct
			// funding_8h_pct is already a percentage (e.g. 0.005447% per 8h -> * 1095 = 5.96% APR).
			row.FundingAPRPct = btcFloatPtr(*acc.funding8hPct * (8760.0 / 8.0))
		}
		fallbacks[vID] = row
	}

	return &BtcCGDerivativesSummary{
		AllReportedVolume24hUSD: allVolUSD,
		AllReportedOIUSD:        allOIUSD,
		BTCIndexRows:            btcRows,
		VenuesCount:             len(venuesSeen),
		FallbackVenues:          fallbacks,
	}, ""
}

func formatUnixSecToDate(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02")
}

var _ = strconv.Itoa
