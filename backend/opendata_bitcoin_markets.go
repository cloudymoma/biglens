package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	btcMarketsVenueKeyFmt  = "opendata:bitcoin:markets:venue:%s"
	btcMarketsCGBitcoinKey = "opendata:bitcoin:markets:cg_bitcoin"
	btcMarketsCGSpotKey    = "opendata:bitcoin:markets:cg_spot"
	btcMarketsCGDerivKey   = "opendata:bitcoin:markets:cg_deriv"
	btcMarketsCFTCKey      = "opendata:bitcoin:markets:cftc"
	btcMarketsOKXRubikKey  = "opendata:bitcoin:markets:okx_rubik"

	btcMarketsVenueTTL     = 60 * time.Second
	btcMarketsCGBitcoinTTL = 5 * time.Minute
	btcMarketsCGSpotTTL    = 10 * time.Minute
	btcMarketsCGDerivTTL   = 30 * time.Minute
	btcMarketsOKXRubikTTL  = 1 * time.Hour
	btcMarketsCFTCTTL      = 12 * time.Hour
)

var btcMarketsDefaultCaveats = []string{
	"Reported exchange volumes include wash trading on some venues; the headline ratio uses a curated venue set while the all-reported CoinGecko figure is labeled unverified.",
	"Contract definitions differ across USD-M, COIN-M, inverse and nano contracts; all volumes and open interest are normalized to USD notional and BTC.",
	"CME open interest and positioning come from the weekly CFTC Traders in Financial Futures report (as of Tuesday).",
	"\"via CoinGecko\" fallback rows carry CoinGecko's reporting cadence and unit conventions; their funding rate is shown only where the venue's 8h interval was verified.",
}

// BtcVenueRatio holds the 24h derivatives-to-spot volume ratio for one venue.
type BtcVenueRatio struct {
	Venue       string   `json:"venue"`
	Spot24hUSD  float64  `json:"spot_24h_usd"`
	Deriv24hUSD float64  `json:"deriv_24h_usd"`
	Ratio       *float64 `json:"ratio"`
}

// BtcMarketsRatios holds the curated and all-reported derivatives/spot volume ratios.
type BtcMarketsRatios struct {
	CuratedSpot24hUSD      *float64        `json:"curated_spot_24h_usd"`
	CuratedDeriv24hUSD     *float64        `json:"curated_deriv_24h_usd"`
	MajorDerivSpot         *float64        `json:"major_deriv_spot"`
	AllReportedSpot24hUSD  *float64        `json:"all_reported_spot_24h_usd"`
	AllReportedDeriv24hUSD *float64        `json:"all_reported_deriv_24h_usd"`
	AllReportedDerivSpot   *float64        `json:"all_reported_deriv_spot"`
	AllReportedUnverified  bool            `json:"all_reported_unverified"`
	PerVenue               []BtcVenueRatio `json:"per_venue"`
	Note                   string          `json:"note,omitempty"`
}

// BtcOIShare represents one venue's share of curated BTC open interest.
type BtcOIShare struct {
	Venue    string  `json:"venue"`
	Kind     string  `json:"kind"`
	OIUSD    float64 `json:"oi_usd"`
	OIBTC    float64 `json:"oi_btc"`
	SharePct float64 `json:"share_pct"`
	Weekly   bool    `json:"weekly,omitempty"`
	Via      string  `json:"via,omitempty"`
}

// BtcMarketDailyPoint is one persisted daily market-structure snapshot row in btc_market_daily.
type BtcMarketDailyPoint struct {
	Date                 string   `json:"date"`
	MajorDerivSpot       *float64 `json:"major_deriv_spot"`
	AllReportedDerivSpot *float64 `json:"all_reported_deriv_spot"`
	TotalOIUSD           *float64 `json:"total_oi_usd"`
	OIToMcapPct          *float64 `json:"oi_to_mcap_pct"`
	DEXSharePct          *float64 `json:"dex_share_pct"`
	USRegulatedSharePct  *float64 `json:"us_regulated_share_pct"`
	FundingOIWeightedAPR *float64 `json:"funding_oi_weighted_apr"`
	DVOL                 *float64 `json:"dvol"`
	RV30                 *float64 `json:"rv30"`
	VRP                  *float64 `json:"vrp"`
}

// BtcMarketsData is the response payload for GET /api/opendata/bitcoin/markets.
type BtcMarketsData struct {
	AsOf                 string                `json:"as_of"`
	PriceUSD             *float64              `json:"price_usd"`
	MarketCapUSD         *float64              `json:"market_cap_usd"`
	TotalOIUSD           *float64              `json:"total_oi_usd"`
	Venues               []BtcVenueRow         `json:"venues"`
	Ratios               BtcMarketsRatios      `json:"ratios"`
	OIShares             []BtcOIShare          `json:"oi_shares"`
	OIToMcapPct          *float64              `json:"oi_to_mcap_pct"`
	DEXSharePct          *float64              `json:"dex_share_pct"`
	USRegulatedSharePct  *float64              `json:"us_regulated_share_pct"`
	FundingOIWeightedAPR *float64              `json:"funding_oi_weighted_apr"`
	Basis                []BtcBasisPoint       `json:"basis"`
	DVOL                 []BtcDVOLPoint        `json:"dvol"`
	DVOLLatest           *float64              `json:"dvol_latest"`
	RV30                 *float64              `json:"rv30"`
	VRP                  *float64              `json:"vrp"`
	COT                  *BtcCFTCSummary       `json:"cot"`
	History              []BtcMarketDailyPoint `json:"history"`
	OKXRubik             []BtcOKXRubikPoint    `json:"okx_rubik"`
	Caveats              []string              `json:"caveats"`
	Sources              []SourceStatus        `json:"sources"`
}

func computeMarketsRatios(
	curatedSpot *float64,
	curatedDeriv *float64,
	allSpot *float64,
	allDeriv *float64,
	perVenue []BtcVenueRatio,
	unavailable []string,
) BtcMarketsRatios {
	out := BtcMarketsRatios{
		CuratedSpot24hUSD:      curatedSpot,
		CuratedDeriv24hUSD:     curatedDeriv,
		AllReportedSpot24hUSD:  allSpot,
		AllReportedDeriv24hUSD: allDeriv,
		AllReportedUnverified:  true,
		PerVenue:               perVenue,
	}
	if curatedSpot != nil && *curatedSpot > 0 && curatedDeriv != nil && *curatedDeriv > 0 {
		out.MajorDerivSpot = btcFloatPtr(*curatedDeriv / *curatedSpot)
	}
	if allSpot != nil && *allSpot > 0 && allDeriv != nil && *allDeriv > 0 {
		out.AllReportedDerivSpot = btcFloatPtr(*allDeriv / *allSpot)
	}
	if len(unavailable) > 0 {
		out.Note = fmt.Sprintf("partial: computed excluding unavailable derivatives venues (%s)", strings.Join(unavailable, ", "))
	}
	return out
}

func (h *APIHandler) BitcoinMarkets(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	h.btc.prices.touchAsync()
	data := h.buildMarketsData(r.Context())
	writeJSON(w, data)
}

func (h *APIHandler) buildMarketsData(ctx context.Context) BtcMarketsData {
	s := h.btc
	now := s.now().UTC()
	asOf := now.Format(time.RFC3339)

	// Cap concurrent outbound fetches per request at 6 (§11.2); CoinGecko is further capped at 2 by btcCGSem.
	sem := make(chan struct{}, 6)
	runBounded := func(wg *sync.WaitGroup, fn func()) {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		})
	}

	var (
		wg          sync.WaitGroup
		cgBtcVal    any
		cgBtcSrc    SourceStatus
		cgSpotVal   any
		cgSpotSrc   SourceStatus
		cgDerivVal  any
		cgDerivSrc  SourceStatus
		cftcVal     any
		cftcSrc     SourceStatus
		okxRubikVal any
		okxRubikSrc SourceStatus
	)

	type directResult struct {
		deriv *btcVenueDeriv
		src   SourceStatus
	}
	directMap := make(map[string]directResult)
	var directMu sync.Mutex

	adapters := []btcVenueAdapter{
		s.getBinanceAdapter(),
		newOKXAdapter(),
		newCoinbaseAdapter(),
		newKrakenAdapter(s.now),
		newHyperliquidAdapter(),
		newDeribitAdapter(s.now),
	}

	for _, ad := range adapters {
		adapter := ad
		vID := adapter.ID()
		runBounded(&wg, func() {
			key := fmt.Sprintf(btcMarketsVenueKeyFmt, vID)
			val, src := h.btcCachedSource(ctx, key, btcMarketsVenueTTL, vID, func(c context.Context) (any, string) {
				d, code := adapter.Fetch(c)
				if code != "" {
					return nil, code
				}
				return &d, ""
			})
			var d *btcVenueDeriv
			if ptr, ok := val.(*btcVenueDeriv); ok {
				d = ptr
			}
			directMu.Lock()
			directMap[vID] = directResult{deriv: d, src: src}
			directMu.Unlock()
		})
	}

	runBounded(&wg, func() {
		cgBtcVal, cgBtcSrc = h.btcCachedSource(ctx, btcMarketsCGBitcoinKey, btcMarketsCGBitcoinTTL, "coingecko", func(c context.Context) (any, string) {
			return fetchCGBitcoin(c)
		})
	})
	runBounded(&wg, func() {
		cgSpotVal, cgSpotSrc = h.btcCachedSource(ctx, btcMarketsCGSpotKey, btcMarketsCGSpotTTL, "coingecko", func(c context.Context) (any, string) {
			return fetchCGCuratedSpot(c, btcCuratedSpotVenues)
		})
	})
	runBounded(&wg, func() {
		cgDerivVal, cgDerivSrc = h.btcCachedSource(ctx, btcMarketsCGDerivKey, btcMarketsCGDerivTTL, "coingecko", func(c context.Context) (any, string) {
			return fetchCGDerivativesBTC(c, now)
		})
	})
	runBounded(&wg, func() {
		cftcVal, cftcSrc = h.btcCachedSource(ctx, btcMarketsCFTCKey, btcMarketsCFTCTTL, "cftc", func(c context.Context) (any, string) {
			return fetchCFTCBitcoin(c, 52)
		})
	})
	runBounded(&wg, func() {
		okxRubikVal, okxRubikSrc = h.btcCachedSource(ctx, btcMarketsOKXRubikKey, btcMarketsOKXRubikTTL, "okx", func(c context.Context) (any, string) {
			pts, code := fetchOKXRubikHistory(c, btcOKXBaseURL)
			if code != "" {
				return nil, code
			}
			return pts, ""
		})
	})
	wg.Wait()

	cgBtc, _ := cgBtcVal.(*BtcCGBitcoin)
	cgSpot, _ := cgSpotVal.(*BtcCuratedSpotSummary)
	cgDeriv, _ := cgDerivVal.(*BtcCGDerivativesSummary)
	cot, _ := cftcVal.(*BtcCFTCSummary)
	okxRubik, _ := okxRubikVal.([]BtcOKXRubikPoint)

	// Determine reference BTC spot price and market cap.
	var refPriceUSD float64
	var marketCapUSD *float64
	if cgBtc != nil {
		if cgBtc.PriceUSD > 0 {
			refPriceUSD = cgBtc.PriceUSD
		}
		if cgBtc.MarketCapUSD > 0 {
			marketCapUSD = btcFloatPtr(cgBtc.MarketCapUSD)
		}
	}
	if refPriceUSD <= 0 {
		for _, vID := range []string{"deribit", "okx", "kraken", "hyperliquid", "coinbase", "binance"} {
			if dr := directMap[vID]; dr.deriv != nil && dr.deriv.MarkPrice > 0 {
				refPriceUSD = dr.deriv.MarkPrice
				break
			}
		}
	}
	if refPriceUSD <= 0 && s.prices != nil {
		if rows := s.prices.rows("", ""); len(rows) > 0 {
			refPriceUSD = rows[len(rows)-1].Close
		}
	}

	// Build venue board in canonical order: binance, okx, coinbase, kraken, hyperliquid, deribit, bybit, cme.
	boardOrder := []struct {
		id   string
		kind string
	}{
		{"binance", "cex_offshore"},
		{"okx", "cex_offshore"},
		{"coinbase", "cex_us"},
		{"kraken", "cex_us"},
		{"hyperliquid", "dex"},
		{"deribit", "cex_offshore"},
		{"bybit", "cex_offshore"},
		{"cme", "regulated_futures"},
	}

	var (
		venues             []BtcVenueRow
		curatedDerivSum    float64
		hasCuratedDeriv    bool
		unavailableVenues  []string
		perVenueRatios     []BtcVenueRatio
		basisPoints        []BtcBasisPoint
		dvolSeries         []BtcDVOLPoint
		fundingWeightedSum float64
		fundingWeightOI    float64
		cdeOIUSD           float64
		hasCDEOI           bool
		venueSourceBadges  []SourceStatus
	)

	for _, item := range boardOrder {
		vID := item.id
		row := BtcVenueRow{
			Venue: vID,
			Kind:  item.kind,
		}

		// Attach spot metrics if available in cgSpot.
		hasSpot := false
		if cgSpot != nil {
			if sp, ok := cgSpot.ByVenue[vID]; ok && sp.Spot24hUSD > 0 {
				row.Spot24hUSD = btcFloatPtr(sp.Spot24hUSD)
				row.Depth2pctUSD = sp.Depth2pctUSD
				row.SpreadPct = sp.SpreadPct
				hasSpot = true
			}
		}

		switch vID {
		case "cme":
			row.Weekly = true
			cmeSrc := cftcSrc
			if cot != nil && cot.AsOfReportDate != "" {
				cmeSrc.AsOf = cot.AsOfReportDate
			}
			if cot != nil && cot.TotalCMERegBTC > 0 {
				row.OIBTC = btcFloatPtr(cot.TotalCMERegBTC)
				if refPriceUSD > 0 {
					row.OIUSD = btcFloatPtr(cot.TotalCMERegBTC * refPriceUSD)
				}
			}
			row.Sources = []SourceStatus{cmeSrc}
			venueSourceBadges = append(venueSourceBadges, cmeSrc)

		case "bybit":
			// Bybit derivatives come from CoinGecko rows only (§5.3).
			bybitSrc := SourceStatus{
				ID:      "bybit",
				Label:   "Bybit",
				Via:     "coingecko",
				Status:  cgDerivSrc.Status,
				AsOf:    cgDerivSrc.AsOf,
				Error:   cgDerivSrc.Error,
				License: cgDerivSrc.License,
			}
			if cgDeriv != nil {
				if fb, ok := cgDeriv.FallbackVenues["bybit"]; ok {
					row.Deriv24hUSD = fb.Deriv24hUSD
					row.OIUSD = fb.OIUSD
					row.OIBTC = fb.OIBTC
					if row.OIBTC == nil && row.OIUSD != nil && refPriceUSD > 0 {
						row.OIBTC = btcFloatPtr(*row.OIUSD / refPriceUSD)
					}
					row.Funding8hPct = fb.Funding8hPct
					row.FundingAPRPct = fb.FundingAPRPct
				}
			}
			row.Sources = []SourceStatus{bybitSrc}
			if hasSpot && cgSpotSrc.Status != bybitSrc.Status {
				row.Sources = append(row.Sources, SourceStatus{
					ID:     "coingecko",
					Label:  "CoinGecko (Spot)",
					Status: cgSpotSrc.Status,
					AsOf:   cgSpotSrc.AsOf,
					Error:  cgSpotSrc.Error,
				})
			}

		default:
			dr := directMap[vID]
			src := dr.src
			usedFallback := false

			if dr.deriv != nil {
				d := dr.deriv
				row.Deriv24hUSD = d.Deriv24hUSD
				row.OIUSD = d.OIUSD
				row.OIBTC = d.OIBTC
				row.Funding8hPct = d.Funding8hPct
				row.FundingAPRPct = d.FundingAPRPct
				row.PremiumPct = d.PremiumPct
				if len(d.BasisPoints) > 0 {
					basisPoints = append(basisPoints, d.BasisPoints...)
				}
				if len(d.DVOLSeries) > 0 {
					dvolSeries = d.DVOLSeries
				}
				if vID == "coinbase" && d.CDEOIUSD != nil {
					cdeOIUSD = *d.CDEOIUSD
					hasCDEOI = true
				}
				// Direct sources contribute to OI-weighted funding (§10.1), weighted only by
				// the open interest of the perpetual contract that produced the funding rate.
				if d.FundingAPRPct != nil && d.PerpOIUSD != nil && *d.PerpOIUSD > 0 {
					fundingWeightedSum += (*d.FundingAPRPct) * (*d.PerpOIUSD)
					fundingWeightOI += *d.PerpOIUSD
				}
			} else if src.Status == "geo_blocked" && vID != "coinbase" && cgDeriv != nil {
				if fb, ok := cgDeriv.FallbackVenues[vID]; ok {
					usedFallback = true
					row.Deriv24hUSD = fb.Deriv24hUSD
					row.OIUSD = fb.OIUSD
					row.OIBTC = fb.OIBTC
					if row.OIBTC == nil && row.OIUSD != nil && refPriceUSD > 0 {
						row.OIBTC = btcFloatPtr(*row.OIUSD / refPriceUSD)
					}
					row.Funding8hPct = fb.Funding8hPct
					row.FundingAPRPct = fb.FundingAPRPct
					meta := s.providerMeta(vID)
					src = SourceStatus{
						ID:      vID,
						Label:   meta.Label,
						Via:     "coingecko",
						Status:  cgDerivSrc.Status,
						AsOf:    cgDerivSrc.AsOf,
						Error:   cgDerivSrc.Error,
						License: cgDerivSrc.License,
					}
				}
			}

			if dr.deriv == nil && !usedFallback && src.Status != "disabled" {
				unavailableVenues = append(unavailableVenues, vID)
			}

			row.Sources = []SourceStatus{src}
			if hasSpot {
				spotBadge := SourceStatus{
					ID:      "coingecko",
					Label:   "CoinGecko (Spot)",
					Status:  cgSpotSrc.Status,
					AsOf:    cgSpotSrc.AsOf,
					Error:   cgSpotSrc.Error,
					License: cgSpotSrc.License,
				}
				row.Sources = append(row.Sources, spotBadge)
			}
			venueSourceBadges = append(venueSourceBadges, src)
		}

		// Accumulate curated derivatives volume (excludes CME, which has no daily volume).
		if vID != "cme" && row.Deriv24hUSD != nil && *row.Deriv24hUSD > 0 {
			curatedDerivSum += *row.Deriv24hUSD
			hasCuratedDeriv = true
		}

		// Per-venue derivatives/spot ratio.
		if row.Spot24hUSD != nil && *row.Spot24hUSD > 0 && row.Deriv24hUSD != nil && *row.Deriv24hUSD > 0 {
			r := *row.Deriv24hUSD / *row.Spot24hUSD
			perVenueRatios = append(perVenueRatios, BtcVenueRatio{
				Venue:       vID,
				Spot24hUSD:  *row.Spot24hUSD,
				Deriv24hUSD: *row.Deriv24hUSD,
				Ratio:       btcFloatPtr(r),
			})
		}

		venues = append(venues, row)
	}

	// Compute OI totals, shares, DEX share, and US-regulated share.
	var totalOIUSD, perpOIExclCME, hyperliquidOIUSD, cmeOIUSD float64
	for _, r := range venues {
		if r.OIUSD == nil || *r.OIUSD <= 0 {
			continue
		}
		oi := *r.OIUSD
		totalOIUSD += oi
		if r.Venue == "cme" {
			cmeOIUSD = oi
		} else {
			perpOIExclCME += oi
		}
		if r.Venue == "hyperliquid" {
			hyperliquidOIUSD = oi
		}
	}
	if !hasCDEOI && cot != nil && cot.CDEOIBTC > 0 && refPriceUSD > 0 {
		cdeOIUSD = cot.CDEOIBTC * refPriceUSD
		hasCDEOI = true
		totalOIUSD += cdeOIUSD
		perpOIExclCME += cdeOIUSD
	}

	var oiShares []BtcOIShare
	if totalOIUSD > 0 {
		for _, r := range venues {
			if r.OIUSD == nil || *r.OIUSD <= 0 {
				continue
			}
			var oiBTC float64
			if r.OIBTC != nil {
				oiBTC = *r.OIBTC
			} else if refPriceUSD > 0 {
				oiBTC = *r.OIUSD / refPriceUSD
			}
			var via string
			if len(r.Sources) > 0 {
				via = r.Sources[0].Via
			}
			oiShares = append(oiShares, BtcOIShare{
				Venue:    r.Venue,
				Kind:     r.Kind,
				OIUSD:    *r.OIUSD,
				OIBTC:    oiBTC,
				SharePct: (*r.OIUSD / totalOIUSD) * 100.0,
				Weekly:   r.Weekly,
				Via:      via,
			})
		}
		sort.Slice(oiShares, func(i, j int) bool {
			return oiShares[i].OIUSD > oiShares[j].OIUSD
		})
	}

	var (
		totalOIPtr          *float64
		oiToMcapPct         *float64
		dexSharePct         *float64
		usRegulatedSharePct *float64
		fundingOIWeighted   *float64
	)
	if totalOIUSD > 0 {
		totalOIPtr = btcFloatPtr(totalOIUSD)
		if marketCapUSD != nil && *marketCapUSD > 0 {
			oiToMcapPct = btcFloatPtr((totalOIUSD / *marketCapUSD) * 100.0)
		}
		if cmeOIUSD > 0 || hasCDEOI {
			usRegulatedSharePct = btcFloatPtr(((cmeOIUSD + cdeOIUSD) / totalOIUSD) * 100.0)
		}
	}
	if perpOIExclCME > 0 && hyperliquidOIUSD > 0 {
		dexSharePct = btcFloatPtr((hyperliquidOIUSD / perpOIExclCME) * 100.0)
	}
	if fundingWeightOI > 0 {
		fundingOIWeighted = btcFloatPtr(fundingWeightedSum / fundingWeightOI)
	}

	// Curated and all-reported volume ratios.
	var curatedSpotPtr, curatedDerivPtr, allSpotPtr, allDerivPtr *float64
	if cgSpot != nil && cgSpot.TotalSpot24hUSD > 0 {
		curatedSpotPtr = btcFloatPtr(cgSpot.TotalSpot24hUSD)
	}
	if hasCuratedDeriv && curatedDerivSum > 0 {
		curatedDerivPtr = btcFloatPtr(curatedDerivSum)
	}
	if cgBtc != nil && cgBtc.AllReportedSpotVolume24hUSD > 0 {
		allSpotPtr = btcFloatPtr(cgBtc.AllReportedSpotVolume24hUSD)
	}
	if cgDeriv != nil && cgDeriv.AllReportedVolume24hUSD > 0 {
		allDerivPtr = btcFloatPtr(cgDeriv.AllReportedVolume24hUSD)
	}
	ratios := computeMarketsRatios(curatedSpotPtr, curatedDerivPtr, allSpotPtr, allDerivPtr, perVenueRatios, unavailableVenues)

	// Sort basis curve by days to expiry ascending.
	sort.Slice(basisPoints, func(i, j int) bool {
		return basisPoints[i].DaysToExpiry < basisPoints[j].DaysToExpiry
	})

	// DVOL latest, RV30 and VRP.
	var dvolLatest, rv30Ptr, vrpPtr *float64
	if len(dvolSeries) > 0 {
		dvolLatest = btcFloatPtr(dvolSeries[len(dvolSeries)-1].Close)
	}
	if s.prices != nil {
		priceRows := s.prices.rows("", "")
		if len(priceRows) >= 31 {
			closes := make([]float64, len(priceRows))
			for i, pr := range priceRows {
				closes[i] = pr.Close
			}
			if rvs := realizedVol(closes, 30); len(rvs) > 0 && rvs[len(rvs)-1] != nil {
				rv30Ptr = rvs[len(rvs)-1]
			}
		}
	}
	if dvolLatest != nil && rv30Ptr != nil {
		vrpPtr = btcFloatPtr(*dvolLatest - *rv30Ptr)
	}

	// Load persisted daily market history if store is available.
	var history []BtcMarketDailyPoint
	if s.store != nil {
		if hRows, err := s.store.loadBtcMarketDaily(ctx, 365); err == nil {
			history = hRows
		}
	}

	// Combine top-level Sources list.
	cgCombined := combineCGSourceStatus(cgBtcSrc, cgSpotSrc, cgDerivSrc)
	sources := make([]SourceStatus, 0, len(venueSourceBadges)+2)
	sources = append(sources, cgCombined)
	sources = append(sources, venueSourceBadges...)
	if okxRubikSrc.ID != "" && okxRubikSrc.Status != "ok" && okxRubikSrc.Status != "disabled" {
		// Surface rubik degradation if the primary OKX adapter was ok.
		for i := range sources {
			if sources[i].ID == "okx" && sources[i].Status == "ok" {
				sources[i].Status = okxRubikSrc.Status
				sources[i].Error = okxRubikSrc.Error
			}
		}
	}

	var pricePtr *float64
	if refPriceUSD > 0 {
		pricePtr = btcFloatPtr(refPriceUSD)
	}

	return BtcMarketsData{
		AsOf:                 asOf,
		PriceUSD:             pricePtr,
		MarketCapUSD:         marketCapUSD,
		TotalOIUSD:           totalOIPtr,
		Venues:               venues,
		Ratios:               ratios,
		OIShares:             oiShares,
		OIToMcapPct:          oiToMcapPct,
		DEXSharePct:          dexSharePct,
		USRegulatedSharePct:  usRegulatedSharePct,
		FundingOIWeightedAPR: fundingOIWeighted,
		Basis:                basisPoints,
		DVOL:                 dvolSeries,
		DVOLLatest:           dvolLatest,
		RV30:                 rv30Ptr,
		VRP:                  vrpPtr,
		COT:                  cot,
		History:              history,
		OKXRubik:             okxRubik,
		Caveats:              append([]string(nil), btcMarketsDefaultCaveats...),
		Sources:              sources,
	}
}

func combineCGSourceStatus(parts ...SourceStatus) SourceStatus {
	out := SourceStatus{
		ID:      "coingecko",
		Label:   "CoinGecko",
		Status:  "ok",
		License: "attribution required",
	}
	var anyOK, anyStale, allDisabled bool
	allDisabled = true
	for _, p := range parts {
		if p.ID == "" {
			continue
		}
		if p.Status != "disabled" {
			allDisabled = false
		}
		if p.AsOf != "" && (out.AsOf == "" || p.AsOf < out.AsOf) {
			out.AsOf = p.AsOf
		}
		switch p.Status {
		case "ok":
			anyOK = true
		case "stale":
			anyStale = true
			if out.Error == "" {
				out.Error = p.Error
			}
		case "rate_limited", "geo_blocked", "error":
			if out.Error == "" {
				out.Error = p.Error
			}
			if !anyOK && !anyStale {
				out.Status = p.Status
			}
		}
	}
	if allDisabled {
		out.Status = "disabled"
		return out
	}
	if anyStale {
		out.Status = "stale"
		return out
	}
	if anyOK && out.Error == "" {
		out.Status = "ok"
	}
	return out
}

// btcMarketSnapshotter captures one daily row in btc_market_daily per UTC day after 00:30 UTC (HTTP only).
type btcMarketSnapshotter struct {
	api   *APIHandler
	store *riskStore
	now   func() time.Time
	mu    sync.Mutex
}

func newBtcMarketSnapshotter(api *APIHandler, store *riskStore, now func() time.Time) *btcMarketSnapshotter {
	if now == nil {
		now = time.Now
	}
	return &btcMarketSnapshotter{
		api:   api,
		store: store,
		now:   now,
	}
}

func (ms *btcMarketSnapshotter) syncOnce(ctx context.Context) {
	if ms == nil || ms.api == nil || ms.api.btc == nil || ms.store == nil {
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()

	now := ms.now().UTC()
	// Wait until at least 00:30 UTC before writing the day's market snapshot (Task 4.3).
	if now.Hour() == 0 && now.Minute() < 30 {
		return
	}
	day := now.Format("2006-01-02")
	exists, err := ms.store.hasBtcMarketDaily(ctx, day)
	if err != nil {
		slog.Warn("btc market snapshot: check failed", "day", day, "error", err)
		return
	}
	if exists {
		return
	}

	data := ms.api.buildMarketsData(ctx)
	pt := BtcMarketDailyPoint{
		Date:                 day,
		MajorDerivSpot:       data.Ratios.MajorDerivSpot,
		AllReportedDerivSpot: data.Ratios.AllReportedDerivSpot,
		TotalOIUSD:           data.TotalOIUSD,
		OIToMcapPct:          data.OIToMcapPct,
		DEXSharePct:          data.DEXSharePct,
		USRegulatedSharePct:  data.USRegulatedSharePct,
		FundingOIWeightedAPR: data.FundingOIWeightedAPR,
		DVOL:                 data.DVOLLatest,
		RV30:                 data.RV30,
		VRP:                  data.VRP,
	}
	if pt.TotalOIUSD == nil && pt.MajorDerivSpot == nil && pt.DVOL == nil && pt.RV30 == nil {
		return
	}
	if _, err := ms.store.putBtcMarketDaily(ctx, pt, now); err != nil {
		slog.Warn("btc market snapshot: save failed", "day", day, "error", err)
	}
}
