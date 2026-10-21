package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	btcBinanceFapiBaseURL   = "https://fapi.binance.com"
	btcBinanceDapiBaseURL   = "https://dapi.binance.com"
	btcOKXBaseURL           = "https://www.okx.com"
	btcCoinbaseINTXPerpURL  = "https://api.international.coinbase.com/api/v1/instruments/BTC-PERP"
	btcCoinbaseCDEBIPURL    = "https://api.coinbase.com/api/v3/brokerage/market/products/BIP-20DEC30-CDE"
	btcKrakenFuturesTickURL = "https://futures.kraken.com/derivatives/api/v3/tickers"
	btcHyperliquidInfoURL   = "https://api.hyperliquid.xyz/info"
	btcDeribitBaseURL       = "https://www.deribit.com/api/v2"
)

const (
	btcBinanceGeoBlockTTL   = 24 * time.Hour
	btcBinanceGeoBlockedKey = "binance_geo_blocked_until"
	btcBinanceCoinMSizeUSD  = 100.0
	btcDefaultCDESizeBTC    = 0.01
	btcMinBasisDaysToExpiry = 2.0
)

// BtcVenueRow is one line of the Markets venue board. Amounts in USD notional unless noted.
type BtcVenueRow struct {
	Venue         string         `json:"venue"` // binance | okx | coinbase | kraken | hyperliquid | deribit | bybit | cme
	Kind          string         `json:"kind"`  // cex_offshore | cex_us | dex | regulated_futures
	Spot24hUSD    *float64       `json:"spot_24h_usd"`
	Depth2pctUSD  *float64       `json:"depth_2pct_usd"` // (+2 % ask side + −2 % bid side) / 2
	SpreadPct     *float64       `json:"spread_pct"`
	Deriv24hUSD   *float64       `json:"deriv_24h_usd"`
	OIUSD         *float64       `json:"oi_usd"`
	OIBTC         *float64       `json:"oi_btc"`
	Funding8hPct  *float64       `json:"funding_8h_pct"`
	FundingAPRPct *float64       `json:"funding_apr_pct"`
	PremiumPct    *float64       `json:"premium_pct"`
	Weekly        bool           `json:"weekly,omitempty"` // CME (CFTC)
	Sources       []SourceStatus `json:"sources"`
}

// BtcBasisPoint represents one dated futures contract on the annualized basis curve.
type BtcBasisPoint struct {
	Venue        string  `json:"venue"`
	Instrument   string  `json:"instrument"`
	Expiry       string  `json:"expiry"`
	DaysToExpiry float64 `json:"days_to_expiry"`
	FuturePrice  float64 `json:"future_price"`
	SpotIndex    float64 `json:"spot_index"`
	BasisAPRPct  float64 `json:"basis_apr_pct"`
	OIUSD        float64 `json:"oi_usd"`
	Volume24hUSD float64 `json:"volume_24h_usd"`
}

// BtcDVOLPoint is one daily Deribit volatility index (DVOL) candle.
type BtcDVOLPoint struct {
	Date  string  `json:"date"`
	Open  float64 `json:"open"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

// BtcOKXRubikPoint is one daily point from OKX's 180-day contract OI/volume series.
type BtcOKXRubikPoint struct {
	Date         string  `json:"date"`
	OIUSD        float64 `json:"oi_usd"`
	Volume24hUSD float64 `json:"volume_24h_usd"`
}

// btcVenueDeriv holds normalized derivatives telemetry for a single venue.
type btcVenueDeriv struct {
	Venue         string
	Kind          string
	MarkPrice     float64
	IndexPrice    float64
	Deriv24hUSD   *float64
	OIUSD         *float64
	OIBTC         *float64
	PerpOIUSD     *float64
	Funding8hPct  *float64
	FundingAPRPct *float64
	PremiumPct    *float64
	IntervalHours float64

	// Sub-venue breakdown fields for verification and US-regulated share calculation.
	USDMVol24hUSD      *float64
	USDMOIUSD          *float64
	USDMOIBTC          *float64
	CoinMVol24hUSD     *float64
	CoinMOIUSD         *float64
	CoinMOIBTC         *float64
	INTXNotional24hUSD *float64
	INTXOIBTC          *float64
	INTXOIUSD          *float64
	CDEVol24hUSD       *float64
	CDEOIBTC           *float64
	CDEOIUSD           *float64

	BasisPoints []BtcBasisPoint
	DVOLSeries  []BtcDVOLPoint
}

func (d btcVenueDeriv) toVenueRow(sources []SourceStatus) BtcVenueRow {
	return BtcVenueRow{
		Venue:         d.Venue,
		Kind:          d.Kind,
		Deriv24hUSD:   d.Deriv24hUSD,
		OIUSD:         d.OIUSD,
		OIBTC:         d.OIBTC,
		Funding8hPct:  d.Funding8hPct,
		FundingAPRPct: d.FundingAPRPct,
		PremiumPct:    d.PremiumPct,
		Sources:       sources,
	}
}

type btcVenueAdapter interface {
	ID() string
	Fetch(ctx context.Context) (btcVenueDeriv, string)
}

func btcFloatPtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	x := v
	return &x
}

func parseBtcFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// -----------------------------------------------------------------------------
// 1. Binance adapter (USD-M + COIN-M, auto geo_blocked 24h memory + SQLite)
// -----------------------------------------------------------------------------

type binanceAdapter struct {
	fapiBaseURL string
	dapiBaseURL string
	store       *riskStore
	now         func() time.Time

	mu              sync.Mutex
	geoBlockedUntil time.Time
}

func newBinanceAdapter(store *riskStore, now func() time.Time) *binanceAdapter {
	if now == nil {
		now = time.Now
	}
	return &binanceAdapter{
		store: store,
		now:   now,
	}
}

func (a *binanceAdapter) setDeps(store *riskStore, now func() time.Time) {
	a.mu.Lock()
	if store != nil {
		a.store = store
	}
	if now != nil {
		a.now = now
	}
	a.mu.Unlock()
}

func (a *binanceAdapter) getDeps() (*riskStore, func() time.Time) {
	a.mu.Lock()
	st := a.store
	nw := a.now
	a.mu.Unlock()
	if nw == nil {
		nw = time.Now
	}
	return st, nw
}

func (a *binanceAdapter) ID() string { return "binance" }

func (a *binanceAdapter) isGeoBlocked(ctx context.Context, now time.Time) bool {
	a.mu.Lock()
	until := a.geoBlockedUntil
	st := a.store
	a.mu.Unlock()
	if now.Before(until) {
		return true
	}
	if st != nil {
		if raw, ok, err := st.getBtcKV(ctx, btcBinanceGeoBlockedKey); err == nil && ok && raw != "" {
			if t, pErr := time.Parse(time.RFC3339, raw); pErr == nil && now.Before(t) {
				a.mu.Lock()
				a.geoBlockedUntil = t
				a.mu.Unlock()
				return true
			}
		}
	}
	return false
}

func (a *binanceAdapter) markGeoBlocked(ctx context.Context, now time.Time) {
	until := now.UTC().Add(btcBinanceGeoBlockTTL)
	a.mu.Lock()
	a.geoBlockedUntil = until
	st := a.store
	a.mu.Unlock()
	if st != nil {
		_ = st.setBtcKV(ctx, btcBinanceGeoBlockedKey, until.Format(time.RFC3339))
	}
}

func (a *binanceAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	_, nowFn := a.getDeps()
	now := nowFn().UTC()
	out := btcVenueDeriv{Venue: "binance", Kind: "cex_offshore", IntervalHours: 8}
	if a.isGeoBlocked(ctx, now) {
		return out, "geo_blocked"
	}

	fapi := strings.TrimRight(a.fapiBaseURL, "/")
	dapi := strings.TrimRight(a.dapiBaseURL, "/")
	if fapi == "" {
		fapi = btcBinanceFapiBaseURL
	}
	if dapi == "" {
		dapi = btcBinanceDapiBaseURL
	}

	var fTicker struct {
		LastPrice   string `json:"lastPrice"`
		Volume      string `json:"volume"`
		QuoteVolume string `json:"quoteVolume"`
	}
	if code := btcGetJSON(ctx, fapi+"/fapi/v1/ticker/24hr?symbol=BTCUSDT", btcDefaultMaxBody, &fTicker); code != "" {
		if code == "geo_blocked" {
			a.markGeoBlocked(ctx, now)
		}
		return out, code
	}

	var fOI struct {
		OpenInterest string `json:"openInterest"`
	}
	if code := btcGetJSON(ctx, fapi+"/fapi/v1/openInterest?symbol=BTCUSDT", btcDefaultMaxBody, &fOI); code != "" {
		if code == "geo_blocked" {
			a.markGeoBlocked(ctx, now)
		}
		return out, code
	}

	var fPrem struct {
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
	}
	if code := btcGetJSON(ctx, fapi+"/fapi/v1/premiumIndex?symbol=BTCUSDT", btcDefaultMaxBody, &fPrem); code != "" {
		if code == "geo_blocked" {
			a.markGeoBlocked(ctx, now)
		}
		return out, code
	}

	mark, _ := parseBtcFloat(fPrem.MarkPrice)
	if mark <= 0 {
		mark, _ = parseBtcFloat(fTicker.LastPrice)
	}
	idxPx, _ := parseBtcFloat(fPrem.IndexPrice)
	out.MarkPrice = mark
	out.IndexPrice = idxPx

	usdmVol, okVol := parseBtcFloat(fTicker.QuoteVolume)
	usdmOIBTC, okOI := parseBtcFloat(fOI.OpenInterest)
	if !okVol || !okOI || mark <= 0 {
		return out, "bad_response"
	}
	usdmOIUSD := usdmOIBTC * mark
	out.USDMVol24hUSD = btcFloatPtr(usdmVol)
	out.USDMOIBTC = btcFloatPtr(usdmOIBTC)
	out.USDMOIUSD = btcFloatPtr(usdmOIUSD)
	out.PerpOIUSD = btcFloatPtr(usdmOIUSD)

	if rate, ok := parseBtcFloat(fPrem.LastFundingRate); ok {
		out.Funding8hPct = btcFloatPtr(rate * 100.0)
		out.FundingAPRPct = btcFloatPtr(fundingAPR(rate, 8.0))
	}
	if idxPx > 0 && mark > 0 {
		out.PremiumPct = btcFloatPtr((mark/idxPx - 1.0) * 100.0)
	}

	totalVolUSD := usdmVol
	totalOIUSD := usdmOIUSD
	totalOIBTC := usdmOIBTC

	// COIN-M BTCUSD_PERP ($100 contract size)
	var dTickers []struct {
		Volume string `json:"volume"`
	}
	if code := btcGetJSON(ctx, dapi+"/dapi/v1/ticker/24hr?symbol=BTCUSD_PERP", btcDefaultMaxBody, &dTickers); code == "" && len(dTickers) > 0 {
		if contracts, ok := parseBtcFloat(dTickers[0].Volume); ok && contracts >= 0 {
			cVolUSD := contracts * btcBinanceCoinMSizeUSD
			out.CoinMVol24hUSD = btcFloatPtr(cVolUSD)
			totalVolUSD += cVolUSD
		}
	} else if code == "geo_blocked" {
		a.markGeoBlocked(ctx, now)
		return out, "geo_blocked"
	}

	var dOI struct {
		OpenInterest string `json:"openInterest"`
	}
	if code := btcGetJSON(ctx, dapi+"/dapi/v1/openInterest?symbol=BTCUSD_PERP", btcDefaultMaxBody, &dOI); code == "" {
		if contracts, ok := parseBtcFloat(dOI.OpenInterest); ok && contracts >= 0 {
			cOIUSD := contracts * btcBinanceCoinMSizeUSD
			cOIBTC := cOIUSD / mark
			out.CoinMOIUSD = btcFloatPtr(cOIUSD)
			out.CoinMOIBTC = btcFloatPtr(cOIBTC)
			totalOIUSD += cOIUSD
			totalOIBTC += cOIBTC
		}
	} else if code == "geo_blocked" {
		a.markGeoBlocked(ctx, now)
		return out, "geo_blocked"
	}

	out.Deriv24hUSD = btcFloatPtr(totalVolUSD)
	out.OIUSD = btcFloatPtr(totalOIUSD)
	out.OIBTC = btcFloatPtr(totalOIBTC)
	return out, ""
}

// -----------------------------------------------------------------------------
// 2. OKX adapter (BTC-USDT-SWAP ticker, open-interest, funding-rate, rubik)
// -----------------------------------------------------------------------------

type okxAdapter struct {
	baseURL string
}

func newOKXAdapter() *okxAdapter {
	return &okxAdapter{baseURL: btcOKXBaseURL}
}

func (a *okxAdapter) ID() string { return "okx" }

func (a *okxAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	out := btcVenueDeriv{Venue: "okx", Kind: "cex_offshore", IntervalHours: 8}
	base := strings.TrimRight(a.baseURL, "/")
	if base == "" {
		base = btcOKXBaseURL
	}

	var tickerResp struct {
		Code string `json:"code"`
		Data []struct {
			Last      string `json:"last"`
			VolCcy24h string `json:"volCcy24h"`
		} `json:"data"`
	}
	if code := btcGetJSON(ctx, base+"/api/v5/market/ticker?instId=BTC-USDT-SWAP", btcDefaultMaxBody, &tickerResp); code != "" {
		return out, code
	}
	if len(tickerResp.Data) == 0 {
		return out, "bad_response"
	}

	var oiResp struct {
		Code string `json:"code"`
		Data []struct {
			OICcy string `json:"oiCcy"`
			OIUsd string `json:"oiUsd"`
		} `json:"data"`
	}
	if code := btcGetJSON(ctx, base+"/api/v5/public/open-interest?instType=SWAP&instId=BTC-USDT-SWAP", btcDefaultMaxBody, &oiResp); code != "" {
		return out, code
	}
	if len(oiResp.Data) == 0 {
		return out, "bad_response"
	}

	var fundResp struct {
		Code string `json:"code"`
		Data []struct {
			FundingRate     string `json:"fundingRate"`
			FundingTime     string `json:"fundingTime"`
			NextFundingTime string `json:"nextFundingTime"`
		} `json:"data"`
	}
	if code := btcGetJSON(ctx, base+"/api/v5/public/funding-rate?instId=BTC-USDT-SWAP", btcDefaultMaxBody, &fundResp); code != "" {
		return out, code
	}
	if len(fundResp.Data) == 0 {
		return out, "bad_response"
	}

	mark, okMark := parseBtcFloat(tickerResp.Data[0].Last)
	volBTC, okVol := parseBtcFloat(tickerResp.Data[0].VolCcy24h)
	oiUSD, okOIUSD := parseBtcFloat(oiResp.Data[0].OIUsd)
	oiBTC, okOIBTC := parseBtcFloat(oiResp.Data[0].OICcy)
	if !okMark || mark <= 0 || !okVol || !okOIUSD {
		return out, "bad_response"
	}
	if !okOIBTC || oiBTC <= 0 {
		oiBTC = oiUSD / mark
	}

	out.MarkPrice = mark
	out.Deriv24hUSD = btcFloatPtr(volBTC * mark)
	out.OIUSD = btcFloatPtr(oiUSD)
	out.OIBTC = btcFloatPtr(oiBTC)
	out.PerpOIUSD = btcFloatPtr(oiUSD)

	intervalHours := 8.0
	ft, err1 := strconv.ParseInt(strings.TrimSpace(fundResp.Data[0].FundingTime), 10, 64)
	nft, err2 := strconv.ParseInt(strings.TrimSpace(fundResp.Data[0].NextFundingTime), 10, 64)
	if err1 == nil && err2 == nil && nft > ft {
		h := float64(nft-ft) / 3_600_000.0
		if h >= 1 && h <= 24 {
			intervalHours = h
		}
	}
	out.IntervalHours = intervalHours

	if rate, ok := parseBtcFloat(fundResp.Data[0].FundingRate); ok {
		out.Funding8hPct = btcFloatPtr(rate * (8.0 / intervalHours) * 100.0)
		out.FundingAPRPct = btcFloatPtr(fundingAPR(rate, intervalHours))
	}
	return out, ""
}

// fetchOKXRubikHistory fetches OKX's 180-day daily contract OI and volume series.
func fetchOKXRubikHistory(ctx context.Context, baseURL string) ([]BtcOKXRubikPoint, string) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = btcOKXBaseURL
	}
	var raw struct {
		Code string     `json:"code"`
		Data [][]string `json:"data"`
	}
	if code := btcGetJSON(ctx, base+"/api/v5/rubik/stat/contracts/open-interest-volume?ccy=BTC&period=1D", btcDefaultMaxBody, &raw); code != "" {
		return nil, code
	}
	out := make([]BtcOKXRubikPoint, 0, len(raw.Data))
	for _, row := range raw.Data {
		if len(row) < 3 {
			continue
		}
		tsMs, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		oiUSD, ok1 := parseBtcFloat(row[1])
		volUSD, ok2 := parseBtcFloat(row[2])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, BtcOKXRubikPoint{
			Date:         time.UnixMilli(tsMs).UTC().Format("2006-01-02"),
			OIUSD:        oiUSD,
			Volume24hUSD: volUSD,
		})
	}
	return out, ""
}

// -----------------------------------------------------------------------------
// 3. Coinbase adapter (INTX BTC-PERP + US CDE BIP-20DEC30-CDE)
// -----------------------------------------------------------------------------

type coinbaseAdapter struct {
	intxURL string
	cdeURL  string
}

func newCoinbaseAdapter() *coinbaseAdapter {
	return &coinbaseAdapter{
		intxURL: btcCoinbaseINTXPerpURL,
		cdeURL:  btcCoinbaseCDEBIPURL,
	}
}

func (a *coinbaseAdapter) ID() string { return "coinbase" }

func (a *coinbaseAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	out := btcVenueDeriv{Venue: "coinbase", Kind: "cex_us", IntervalHours: 1}
	intxURL := a.intxURL
	if intxURL == "" {
		intxURL = btcCoinbaseINTXPerpURL
	}
	cdeURL := a.cdeURL
	if cdeURL == "" {
		cdeURL = btcCoinbaseCDEBIPURL
	}

	var intxRaw struct {
		OpenInterest string `json:"open_interest"`
		Notional24hr string `json:"notional_24hr"`
		Qty24hr      string `json:"qty_24hr"`
		Quote        struct {
			MarkPrice        string `json:"mark_price"`
			IndexPrice       string `json:"index_price"`
			PredictedFunding string `json:"predicted_funding"`
		} `json:"quote"`
	}
	intxCode := btcGetJSON(ctx, intxURL, btcDefaultMaxBody, &intxRaw)

	var cdeRaw struct {
		ProductID            string `json:"product_id"`
		Price                string `json:"price"`
		Volume24h            string `json:"volume_24h"`
		FutureProductDetails struct {
			ContractSize    string `json:"contract_size"`
			OpenInterest    string `json:"open_interest"`
			FundingRate     string `json:"funding_rate"`
			FundingInterval string `json:"funding_interval"`
		} `json:"future_product_details"`
	}
	cdeCode := btcGetJSON(ctx, cdeURL, btcDefaultMaxBody, &cdeRaw)

	if intxCode != "" && cdeCode != "" {
		return out, cdeCode
	}

	var totalOIBTC, totalOIUSD float64
	var hasOI bool
	var totalVolUSD float64
	var hasVol bool

	if intxCode == "" {
		mark, _ := parseBtcFloat(intxRaw.Quote.MarkPrice)
		idxPx, _ := parseBtcFloat(intxRaw.Quote.IndexPrice)
		if mark > 0 {
			out.MarkPrice = mark
		}
		if idxPx > 0 {
			out.IndexPrice = idxPx
		}
		if oiBTC, ok := parseBtcFloat(intxRaw.OpenInterest); ok && oiBTC >= 0 && mark > 0 {
			oiUSD := oiBTC * mark
			out.INTXOIBTC = btcFloatPtr(oiBTC)
			out.INTXOIUSD = btcFloatPtr(oiUSD)
			totalOIBTC += oiBTC
			totalOIUSD += oiUSD
			hasOI = true
		}
		// Never substitute avg_daily_notional when notional_24hr is 0.
		if notional, ok := parseBtcFloat(intxRaw.Notional24hr); ok && notional > 0 {
			out.INTXNotional24hUSD = btcFloatPtr(notional)
			totalVolUSD += notional
			hasVol = true
		}
	}

	if cdeCode == "" {
		px, _ := parseBtcFloat(cdeRaw.Price)
		if px <= 0 {
			px = out.MarkPrice
		} else if out.MarkPrice <= 0 {
			out.MarkPrice = px
		}
		contractSize, okSize := parseBtcFloat(cdeRaw.FutureProductDetails.ContractSize)
		if !okSize || contractSize <= 0 {
			contractSize = btcDefaultCDESizeBTC
		}
		if oiContracts, ok := parseBtcFloat(cdeRaw.FutureProductDetails.OpenInterest); ok && oiContracts >= 0 {
			cdeOIBTC := oiContracts * contractSize
			out.CDEOIBTC = btcFloatPtr(cdeOIBTC)
			totalOIBTC += cdeOIBTC
			hasOI = true
			if px > 0 {
				cdeOIUSD := cdeOIBTC * px
				out.CDEOIUSD = btcFloatPtr(cdeOIUSD)
				totalOIUSD += cdeOIUSD
			}
		}
		if volContracts, ok := parseBtcFloat(cdeRaw.Volume24h); ok && volContracts > 0 && px > 0 {
			cdeVolUSD := volContracts * contractSize * px
			out.CDEVol24hUSD = btcFloatPtr(cdeVolUSD)
			totalVolUSD += cdeVolUSD
			hasVol = true
		}
		intervalHours := parseCoinbaseFundingIntervalHours(cdeRaw.FutureProductDetails.FundingInterval)
		out.IntervalHours = intervalHours
		if rate, ok := parseBtcFloat(cdeRaw.FutureProductDetails.FundingRate); ok {
			out.Funding8hPct = btcFloatPtr(rate * (8.0 / intervalHours) * 100.0)
			out.FundingAPRPct = btcFloatPtr(fundingAPR(rate, intervalHours))
			if out.CDEOIUSD != nil && *out.CDEOIUSD > 0 {
				out.PerpOIUSD = out.CDEOIUSD
			}
		}
	}

	if out.FundingAPRPct == nil && intxCode == "" {
		if rate, ok := parseBtcFloat(intxRaw.Quote.PredictedFunding); ok {
			out.Funding8hPct = btcFloatPtr(rate * 8.0 * 100.0)
			out.FundingAPRPct = btcFloatPtr(fundingAPR(rate, 1.0))
		}
	}
	if out.MarkPrice > 0 && out.IndexPrice > 0 {
		out.PremiumPct = btcFloatPtr((out.MarkPrice/out.IndexPrice - 1.0) * 100.0)
	}
	if hasOI {
		out.OIBTC = btcFloatPtr(totalOIBTC)
		if totalOIUSD > 0 {
			out.OIUSD = btcFloatPtr(totalOIUSD)
		}
	}
	if hasVol {
		out.Deriv24hUSD = btcFloatPtr(totalVolUSD)
	}
	return out, ""
}

func parseCoinbaseFundingIntervalHours(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1.0
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d.Hours()
	}
	if secs, err := strconv.ParseFloat(strings.TrimSuffix(raw, "s"), 64); err == nil && secs > 0 {
		return secs / 3600.0
	}
	return 1.0
}

// -----------------------------------------------------------------------------
// 4. Kraken Futures adapter (PF_XBTUSD perpetual + FF_XBTUSD_* dated futures)
// -----------------------------------------------------------------------------

type krakenAdapter struct {
	tickersURL string
	now        func() time.Time
}

func newKrakenAdapter(now func() time.Time) *krakenAdapter {
	if now == nil {
		now = time.Now
	}
	return &krakenAdapter{
		tickersURL: btcKrakenFuturesTickURL,
		now:        now,
	}
}

func (a *krakenAdapter) ID() string { return "kraken" }

func (a *krakenAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	out := btcVenueDeriv{Venue: "kraken", Kind: "cex_us", IntervalHours: 1}
	u := a.tickersURL
	if u == "" {
		u = btcKrakenFuturesTickURL
	}
	var resp struct {
		Result  string `json:"result"`
		Tickers []struct {
			Symbol              string  `json:"symbol"`
			Tag                 string  `json:"tag"`
			Pair                string  `json:"pair"`
			MarkPrice           float64 `json:"markPrice"`
			IndexPrice          float64 `json:"indexPrice"`
			Vol24h              float64 `json:"vol24h"`
			VolumeQuote         float64 `json:"volumeQuote"`
			OpenInterest        float64 `json:"openInterest"`
			FundingRate         float64 `json:"fundingRate"`
			RelativeFundingRate float64 `json:"relativeFundingRate"`
			MaturityTime        string  `json:"maturityTime"`
			Suspended           bool    `json:"suspended"`
		} `json:"tickers"`
	}
	if code := btcGetJSON(ctx, u, btcDefaultMaxBody, &resp); code != "" {
		return out, code
	}

	now := a.now().UTC()
	var totalVolUSD, totalOIUSD, totalOIBTC float64
	var foundPerp bool

	for _, tk := range resp.Tickers {
		if tk.Suspended || tk.MarkPrice <= 0 {
			continue
		}
		sym := strings.ToUpper(strings.TrimSpace(tk.Symbol))
		if sym != "PF_XBTUSD" && !strings.HasPrefix(sym, "FF_XBTUSD_") {
			continue
		}
		volUSD := tk.VolumeQuote
		if volUSD <= 0 && tk.Vol24h > 0 {
			volUSD = tk.Vol24h * tk.MarkPrice
		}
		oiBTC := tk.OpenInterest
		oiUSD := oiBTC * tk.MarkPrice

		totalVolUSD += volUSD
		totalOIBTC += oiBTC
		totalOIUSD += oiUSD

		if sym == "PF_XBTUSD" {
			foundPerp = true
			out.MarkPrice = tk.MarkPrice
			out.IndexPrice = tk.IndexPrice
			out.PerpOIUSD = btcFloatPtr(oiUSD)
			out.Funding8hPct = btcFloatPtr(tk.RelativeFundingRate * 8.0 * 100.0)
			out.FundingAPRPct = btcFloatPtr(fundingAPR(tk.RelativeFundingRate, 1.0))
			if tk.IndexPrice > 0 {
				out.PremiumPct = btcFloatPtr((tk.MarkPrice/tk.IndexPrice - 1.0) * 100.0)
			}
			continue
		}

		if exp, ok := parseKrakenFutureExpiry(sym, tk.MaturityTime); ok && exp.After(now) && tk.IndexPrice > 0 {
			days := exp.Sub(now).Hours() / 24.0
			if days >= btcMinBasisDaysToExpiry {
				out.BasisPoints = append(out.BasisPoints, BtcBasisPoint{
					Venue:        "kraken",
					Instrument:   sym,
					Expiry:       exp.Format(time.RFC3339),
					DaysToExpiry: days,
					FuturePrice:  tk.MarkPrice,
					SpotIndex:    tk.IndexPrice,
					BasisAPRPct:  basisAPR(tk.MarkPrice, tk.IndexPrice, days),
					OIUSD:        oiUSD,
					Volume24hUSD: volUSD,
				})
			}
		}
	}

	if !foundPerp {
		return out, "bad_response"
	}
	out.Deriv24hUSD = btcFloatPtr(totalVolUSD)
	out.OIUSD = btcFloatPtr(totalOIUSD)
	out.OIBTC = btcFloatPtr(totalOIBTC)
	return out, ""
}

func parseKrakenFutureExpiry(symbol, maturityTime string) (time.Time, bool) {
	if maturityTime != "" {
		if t, err := time.Parse(time.RFC3339, maturityTime); err == nil {
			return t.UTC(), true
		}
	}
	// FF_XBTUSD_YYMMDD settles at 16:00 UTC on 20YY-MM-DD.
	parts := strings.Split(symbol, "_")
	if len(parts) == 3 && len(parts[2]) == 6 {
		if d, err := time.Parse("060102", parts[2]); err == nil {
			return time.Date(d.Year(), d.Month(), d.Day(), 16, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

// -----------------------------------------------------------------------------
// 5. Hyperliquid adapter (POST /info metaAndAssetCtxs)
// -----------------------------------------------------------------------------

type hyperliquidAdapter struct {
	infoURL string
}

func newHyperliquidAdapter() *hyperliquidAdapter {
	return &hyperliquidAdapter{infoURL: btcHyperliquidInfoURL}
}

func (a *hyperliquidAdapter) ID() string { return "hyperliquid" }

func (a *hyperliquidAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	out := btcVenueDeriv{Venue: "hyperliquid", Kind: "dex", IntervalHours: 1}
	u := a.infoURL
	if u == "" {
		u = btcHyperliquidInfoURL
	}

	var raw []json.RawMessage
	if code := btcPostJSON(ctx, u, map[string]string{"type": "metaAndAssetCtxs"}, btcDefaultMaxBody, &raw); code != "" {
		return out, code
	}
	if len(raw) < 2 {
		return out, "bad_response"
	}

	var meta struct {
		Universe []struct {
			Name string `json:"name"`
		} `json:"universe"`
	}
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return out, "bad_response"
	}

	var ctxs []struct {
		Funding      string `json:"funding"`
		OpenInterest string `json:"openInterest"`
		DayNtlVlm    string `json:"dayNtlVlm"`
		Premium      string `json:"premium"`
		OraclePx     string `json:"oraclePx"`
		MarkPx       string `json:"markPx"`
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return out, "bad_response"
	}

	btcIdx := -1
	for i, u := range meta.Universe {
		if strings.EqualFold(u.Name, "BTC") {
			btcIdx = i
			break
		}
	}
	if btcIdx < 0 || btcIdx >= len(ctxs) {
		return out, "bad_response"
	}

	c := ctxs[btcIdx]
	mark, okMark := parseBtcFloat(c.MarkPx)
	oracle, _ := parseBtcFloat(c.OraclePx)
	oiBTC, okOI := parseBtcFloat(c.OpenInterest)
	volUSD, okVol := parseBtcFloat(c.DayNtlVlm)
	if !okMark || mark <= 0 || !okOI || !okVol {
		return out, "bad_response"
	}

	out.MarkPrice = mark
	out.IndexPrice = oracle
	out.Deriv24hUSD = btcFloatPtr(volUSD)
	out.OIBTC = btcFloatPtr(oiBTC)
	out.OIUSD = btcFloatPtr(oiBTC * mark)
	out.PerpOIUSD = btcFloatPtr(oiBTC * mark)

	if rate, ok := parseBtcFloat(c.Funding); ok {
		out.Funding8hPct = btcFloatPtr(rate * 8.0 * 100.0)
		out.FundingAPRPct = btcFloatPtr(fundingAPR(rate, 1.0))
	}
	if prem, ok := parseBtcFloat(c.Premium); ok {
		out.PremiumPct = btcFloatPtr(prem * 100.0)
	} else if oracle > 0 {
		out.PremiumPct = btcFloatPtr((mark/oracle - 1.0) * 100.0)
	}
	return out, ""
}

// -----------------------------------------------------------------------------
// 6. Deribit adapter (BTC-PERPETUAL, dated futures, DVOL)
// -----------------------------------------------------------------------------

type deribitAdapter struct {
	baseURL string
	now     func() time.Time
}

func newDeribitAdapter(now func() time.Time) *deribitAdapter {
	if now == nil {
		now = time.Now
	}
	return &deribitAdapter{
		baseURL: btcDeribitBaseURL,
		now:     now,
	}
}

func (a *deribitAdapter) ID() string { return "deribit" }

// parseDeribitExpiry parses instrument names like "BTC-25DEC26" or "BTC-5NOV26"
// into their 08:00 UTC settlement timestamp.
func parseDeribitExpiry(instrument string) (time.Time, bool) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(instrument)), "-")
	if len(parts) != 2 || parts[0] != "BTC" || parts[1] == "PERPETUAL" {
		return time.Time{}, false
	}
	code := parts[1]
	if len(code) == 6 {
		code = "0" + code
	}
	if len(code) != 7 {
		return time.Time{}, false
	}
	day, err1 := strconv.Atoi(code[:2])
	monStr := code[2:5]
	yr, err2 := strconv.Atoi(code[5:7])
	if err1 != nil || err2 != nil {
		return time.Time{}, false
	}
	months := map[string]time.Month{
		"JAN": time.January, "FEB": time.February, "MAR": time.March,
		"APR": time.April, "MAY": time.May, "JUN": time.June,
		"JUL": time.July, "AUG": time.August, "SEP": time.September,
		"OCT": time.October, "NOV": time.November, "DEC": time.December,
	}
	mon, ok := months[monStr]
	if !ok {
		return time.Time{}, false
	}
	t := time.Date(2000+yr, mon, day, 8, 0, 0, 0, time.UTC)
	if t.Day() != day || t.Month() != mon {
		return time.Time{}, false
	}
	return t, true
}

func (a *deribitAdapter) Fetch(ctx context.Context) (btcVenueDeriv, string) {
	out := btcVenueDeriv{Venue: "deribit", Kind: "cex_offshore", IntervalHours: 8}
	base := strings.TrimRight(a.baseURL, "/")
	if base == "" {
		base = btcDeribitBaseURL
	}

	var perpResp struct {
		Result struct {
			InstrumentName string  `json:"instrument_name"`
			MarkPrice      float64 `json:"mark_price"`
			IndexPrice     float64 `json:"index_price"`
			OpenInterest   float64 `json:"open_interest"`
			Funding8h      float64 `json:"funding_8h"`
			Stats          struct {
				VolumeUSD float64 `json:"volume_usd"`
			} `json:"stats"`
		} `json:"result"`
	}
	if code := btcGetJSON(ctx, base+"/public/ticker?instrument_name=BTC-PERPETUAL", btcDefaultMaxBody, &perpResp); code != "" {
		return out, code
	}
	r := perpResp.Result
	if r.MarkPrice <= 0 {
		return out, "bad_response"
	}

	out.MarkPrice = r.MarkPrice
	out.IndexPrice = r.IndexPrice
	out.Funding8hPct = btcFloatPtr(r.Funding8h * 100.0)
	out.FundingAPRPct = btcFloatPtr(fundingAPR(r.Funding8h, 8.0))
	if r.IndexPrice > 0 {
		out.PremiumPct = btcFloatPtr((r.MarkPrice/r.IndexPrice - 1.0) * 100.0)
	}

	totalVolUSD := r.Stats.VolumeUSD
	totalOIUSD := r.OpenInterest
	out.PerpOIUSD = btcFloatPtr(r.OpenInterest)

	var bookResp struct {
		Result []struct {
			InstrumentName         string  `json:"instrument_name"`
			MarkPrice              float64 `json:"mark_price"`
			EstimatedDeliveryPrice float64 `json:"estimated_delivery_price"`
			OpenInterest           float64 `json:"open_interest"`
			VolumeUSD              float64 `json:"volume_usd"`
		} `json:"result"`
	}
	now := a.now().UTC()
	if code := btcGetJSON(ctx, base+"/public/get_book_summary_by_currency?currency=BTC&kind=future", btcDefaultMaxBody, &bookResp); code == "" {
		for _, item := range bookResp.Result {
			if strings.EqualFold(item.InstrumentName, "BTC-PERPETUAL") {
				continue
			}
			exp, ok := parseDeribitExpiry(item.InstrumentName)
			if !ok || !exp.After(now) || item.MarkPrice <= 0 {
				continue
			}
			spot := item.EstimatedDeliveryPrice
			if spot <= 0 {
				spot = r.IndexPrice
			}
			if spot <= 0 {
				continue
			}
			days := exp.Sub(now).Hours() / 24.0
			if days <= 0 {
				continue
			}
			totalOIUSD += item.OpenInterest
			totalVolUSD += item.VolumeUSD
			if days < btcMinBasisDaysToExpiry {
				continue
			}
			out.BasisPoints = append(out.BasisPoints, BtcBasisPoint{
				Venue:        "deribit",
				Instrument:   item.InstrumentName,
				Expiry:       exp.Format(time.RFC3339),
				DaysToExpiry: days,
				FuturePrice:  item.MarkPrice,
				SpotIndex:    spot,
				BasisAPRPct:  basisAPR(item.MarkPrice, spot, days),
				OIUSD:        item.OpenInterest,
				Volume24hUSD: item.VolumeUSD,
			})
		}
	}

	out.Deriv24hUSD = btcFloatPtr(totalVolUSD)
	out.OIUSD = btcFloatPtr(totalOIUSD)
	out.OIBTC = btcFloatPtr(totalOIUSD / r.MarkPrice)
	return out, ""
}

// fetchDeribitDVOL fetches daily DVOL candles over the last days window.
func fetchDeribitDVOL(ctx context.Context, baseURL string, now time.Time, days int) ([]BtcDVOLPoint, string) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = btcDeribitBaseURL
	}
	if days <= 0 {
		days = 365
	}
	endMs := now.UTC().UnixMilli()
	startMs := now.UTC().AddDate(0, 0, -days).UnixMilli()
	u := fmt.Sprintf("%s/public/get_volatility_index_data?currency=BTC&resolution=86400&start_timestamp=%d&end_timestamp=%d", base, startMs, endMs)

	var resp struct {
		Result struct {
			Data [][]float64 `json:"data"`
		} `json:"result"`
	}
	if code := btcGetJSON(ctx, u, btcDefaultMaxBody, &resp); code != "" {
		return nil, code
	}
	out := make([]BtcDVOLPoint, 0, len(resp.Result.Data))
	for _, row := range resp.Result.Data {
		if len(row) < 5 {
			continue
		}
		ts := time.UnixMilli(int64(row[0])).UTC().Format("2006-01-02")
		out = append(out, BtcDVOLPoint{
			Date:  ts,
			Open:  row[1],
			High:  row[2],
			Low:   row[3],
			Close: row[4],
		})
	}
	return out, ""
}
