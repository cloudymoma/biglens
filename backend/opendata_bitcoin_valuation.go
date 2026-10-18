package main

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var btcAlternativeMeFnGURL = "https://api.alternative.me/fng/?limit=365"

const (
	btcValuationFnGKey = "opendata:bitcoin:valuation:fng"
	btcValuationCMKey  = "opendata:bitcoin:valuation:cm"
	btcValuationFnGTTL = 1 * time.Hour
)

// BtcFnGPoint holds one daily observation from the alternative.me Fear & Greed Index.
type BtcFnGPoint struct {
	Date           string `json:"date"`
	Value          int    `json:"value"`
	Classification string `json:"classification"`
}

// BtcValuationPoint holds one daily row of price, trend, and valuation multiples.
type BtcValuationPoint struct {
	Date                string   `json:"date"`
	CloseUSD            float64  `json:"close_usd"`
	SMA200              *float64 `json:"sma200,omitempty"`
	WMA200              *float64 `json:"wma200,omitempty"`
	WMA200MonthlyPct    *float64 `json:"wma200_monthly_pct,omitempty"`
	Mayer               *float64 `json:"mayer,omitempty"`
	DrawdownPct         float64  `json:"drawdown_pct"`
	CMMVRV              *float64 `json:"cm_mvrv,omitempty"`
	BigLensMVRV         *float64 `json:"biglens_mvrv,omitempty"`
	MVRVDeltaPct        *float64 `json:"mvrv_delta_pct,omitempty"`
	PuellCM             *float64 `json:"puell_cm,omitempty"`
	PuellChain          *float64 `json:"puell_chain,omitempty"`
	RealizedPriceUSD    *float64 `json:"realized_price_usd,omitempty"`
	STHRealizedPriceUSD *float64 `json:"sth_realized_price_usd,omitempty"`
	LTHRealizedPriceUSD *float64 `json:"lth_realized_price_usd,omitempty"`
}

// BtcValuationLatest summarizes the most recent valuation state and percentiles.
type BtcValuationLatest struct {
	Date                 string   `json:"date"`
	CloseUSD             *float64 `json:"close_usd"`
	ATHUSD               *float64 `json:"ath_usd"`
	ATHDate              string   `json:"ath_date,omitempty"`
	DrawdownPct          *float64 `json:"drawdown_pct"`
	SMA200               *float64 `json:"sma200"`
	WMA200               *float64 `json:"wma200"`
	WMA200MonthlyPct     *float64 `json:"wma200_monthly_pct"`
	Mayer                *float64 `json:"mayer"`
	MayerPctlSince2011   *float64 `json:"mayer_pctl_since_2011"`
	CMMVRV               *float64 `json:"cm_mvrv"`
	CMMVRVPctlSince2011  *float64 `json:"cm_mvrv_pctl_since_2011"`
	BigLensMVRV          *float64 `json:"biglens_mvrv,omitempty"`
	MVRVDeltaPct         *float64 `json:"mvrv_delta_pct,omitempty"`
	PuellCM              *float64 `json:"puell_cm"`
	PuellChain           *float64 `json:"puell_chain,omitempty"`
	RealizedPriceUSD     *float64 `json:"realized_price_usd,omitempty"`
	STHRealizedPriceUSD  *float64 `json:"sth_realized_price_usd,omitempty"`
	LTHRealizedPriceUSD  *float64 `json:"lth_realized_price_usd,omitempty"`
	RealizedPriceBlocked string   `json:"realized_price_blocked,omitempty"`
	HalvingCycleDay      *int     `json:"halving_cycle_day,omitempty"`
	FearGreedValue       *int     `json:"fear_greed_value,omitempty"`
	FearGreedClass       string   `json:"fear_greed_class,omitempty"`
}

// BtcValuationData is the payload returned by GET /api/opendata/bitcoin/valuation.
type BtcValuationData struct {
	AsOf          string                  `json:"as_of"`
	Range         string                  `json:"range"` // 1y | 4y | all
	WarmingUp     bool                    `json:"warming_up"`
	PriceCoverage BtcPriceCoverage        `json:"price_coverage"`
	Latest        BtcValuationLatest      `json:"latest"`
	Series        []BtcValuationPoint     `json:"series"`
	Cycles        []BtcHalvingCycleSeries `json:"cycles"`
	FearGreed     []BtcFnGPoint           `json:"fear_greed"`
	Sources       []BtcSourceStatus       `json:"sources"`
}

func fetchAlternativeMeFnG(ctx context.Context) ([]BtcFnGPoint, string) {
	var resp struct {
		Data []struct {
			Value          string `json:"value"`
			Classification string `json:"value_classification"`
			Timestamp      string `json:"timestamp"`
		} `json:"data"`
	}
	if code := btcGetJSON(ctx, btcAlternativeMeFnGURL, btcDefaultMaxBody, &resp); code != "" {
		return nil, code
	}
	byDate := make(map[string]BtcFnGPoint, len(resp.Data))
	for _, item := range resp.Data {
		v, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil {
			continue
		}
		ts, err := strconv.ParseInt(strings.TrimSpace(item.Timestamp), 10, 64)
		if err != nil || ts <= 0 {
			continue
		}
		day := time.Unix(ts, 0).UTC().Format("2006-01-02")
		byDate[day] = BtcFnGPoint{
			Date:           day,
			Value:          v,
			Classification: strings.TrimSpace(item.Classification),
		}
	}
	out := make([]BtcFnGPoint, 0, len(byDate))
	for _, pt := range byDate {
		out = append(out, pt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, ""
}

func normalizeValuationRange(raw string) (string, int) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "4y":
		return "4y", 1460
	case "all":
		return "all", 0
	default:
		return "1y", 365
	}
}

func (h *APIHandler) BitcoinValuation(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	rangeKey, limitDays := normalizeValuationRange(r.URL.Query().Get("range"))
	data := h.buildBitcoinValuation(r.Context(), rangeKey, limitDays)
	writeJSON(w, data)
}

func (h *APIHandler) buildBitcoinValuation(ctx context.Context, rangeKey string, limitDays int) BtcValuationData {
	s := h.btc
	s.prices.touchAsync()

	nowStr := s.now().UTC().Format(time.RFC3339)
	priceRows := s.prices.rows("", "")
	cov := s.prices.priceCoverage()

	cmVal, cmSrc := h.btcCachedSource(ctx, btcValuationCMKey, btcCoinMetricsTTL, "coinmetrics", func(ctx context.Context) (any, string) {
		return fetchCMAssetMetrics(ctx, btcPriceCoverageStart)
	})
	fngVal, fngSrc := h.btcCachedSource(ctx, btcValuationFnGKey, btcValuationFnGTTL, "alternative_me", func(ctx context.Context) (any, string) {
		return fetchAlternativeMeFnG(ctx)
	})

	var cmRows []BtcCMMetricRow
	if v, ok := cmVal.([]BtcCMMetricRow); ok {
		cmRows = v
	}
	var fngRows []BtcFnGPoint
	if v, ok := fngVal.([]BtcFnGPoint); ok {
		fngRows = v
	}
	if fngRows == nil {
		fngRows = []BtcFnGPoint{}
	}

	priceSrc := BtcSourceStatus{
		ID:      "prices",
		Label:   "BTC Daily Price History (blockchain.com / Coinbase / Kraken)",
		Status:  "ok",
		AsOf:    cov.Latest,
		License: "public chart & market data",
	}
	if len(priceRows) == 0 {
		priceSrc.Status = "stale"
		priceSrc.Error = "warming_up"
	}
	sources := []BtcSourceStatus{priceSrc, cmSrc, fngSrc}

	n := len(priceRows)
	dates := make([]string, n)
	closes := make([]float64, n)
	var athUSD float64
	var athDate string
	for i, r := range priceRows {
		dates[i] = r.Day
		closes[i] = r.Close
		if r.Close > athUSD {
			athUSD = r.Close
			athDate = r.Day
		}
	}

	sma200 := smaSeries(closes, 200)
	wma200 := wma200Series(closes)
	mayer := mayerSeries(closes)
	drawdown := drawdownSeries(closes)
	cycles := halvingOverlay(dates, closes)
	if cycles == nil {
		cycles = []BtcHalvingCycleSeries{}
	}

	wma200Monthly := make([]*float64, n)
	if wma200 != nil {
		for i := 28; i < n; i++ {
			if wma200[i] != nil && wma200[i-28] != nil && *wma200[i-28] > 0 {
				pct := (*wma200[i]/(*wma200[i-28]) - 1.0) * 100.0
				wma200Monthly[i] = &pct
			}
		}
	}

	cmByDate := make(map[string]BtcCMMetricRow, len(cmRows))
	var cmMVRVSince2011 []float64
	for _, r := range cmRows {
		cmByDate[r.Date] = r
		if r.Date >= "2011-01-01" && r.CapMVRVCur != nil {
			cmMVRVSince2011 = append(cmMVRVSince2011, *r.CapMVRVCur)
		}
	}

	// Build full series before slicing to the requested range.
	fullSeries := make([]BtcValuationPoint, n)
	var mayerSince2011 []float64
	var cmIssIdx []int
	var cmIssUSD []float64

	for i := 0; i < n; i++ {
		pt := BtcValuationPoint{
			Date:     dates[i],
			CloseUSD: closes[i],
		}
		if drawdown != nil {
			pt.DrawdownPct = drawdown[i]
		}
		if sma200 != nil {
			pt.SMA200 = sma200[i]
		}
		if wma200 != nil {
			pt.WMA200 = wma200[i]
			pt.WMA200MonthlyPct = wma200Monthly[i]
		}
		if mayer != nil && mayer[i] != nil {
			pt.Mayer = mayer[i]
			if dates[i] >= "2011-01-01" {
				mayerSince2011 = append(mayerSince2011, *mayer[i])
			}
		}
		if cm, ok := cmByDate[dates[i]]; ok {
			pt.CMMVRV = cm.CapMVRVCur
			if cm.IssTotNtv != nil && *cm.IssTotNtv > 0 {
				cmIssIdx = append(cmIssIdx, i)
				cmIssUSD = append(cmIssUSD, *cm.IssTotNtv*closes[i])
			}
		}
		fullSeries[i] = pt
	}

	if puellCM := puellSeries(cmIssUSD); puellCM != nil {
		for k, idx := range cmIssIdx {
			fullSeries[idx].PuellCM = puellCM[k]
		}
	}

	// Attach optional BigQuery chain issuance (PuellChain) and price-gated realized prices / MVRV.
	var realizedBlocked string
	if !cov.Complete {
		realizedBlocked = "price_history_incomplete"
	}
	if s.read != nil && n > 0 {
		fromDay, toDay := dates[0], dates[n-1]
		if onchainRows, err := s.read.onchainDays(ctx, fromDay, toDay); err == nil && len(onchainRows) > 0 {
			ocByDate := make(map[string]BtcOnchainDay, len(onchainRows))
			for _, oc := range onchainRows {
				ocByDate[oc.Date] = oc
			}
			var chainIdx []int
			var chainIssUSD []float64
			for i, d := range dates {
				if oc, ok := ocByDate[d]; ok && oc.ScheduledIssuanceSats > 0 {
					chainIdx = append(chainIdx, i)
					chainIssUSD = append(chainIssUSD, (float64(oc.ScheduledIssuanceSats)/1e8)*closes[i])
				}
			}
			if puellChain := puellSeries(chainIssUSD); puellChain != nil {
				for k, idx := range chainIdx {
					fullSeries[idx].PuellChain = puellChain[k]
				}
			}
		}

		holderRows, _ := s.read.holderDays(ctx, fromDay, toDay)
		if cov.Complete {
			if len(holderRows) > 0 {
				hByDate := make(map[string]BtcHolderDay, len(holderRows))
				for _, hr := range holderRows {
					hByDate[hr.Date] = hr
				}
				for i, d := range dates {
					hr, ok := hByDate[d]
					if !ok {
						continue
					}
					if hr.Blocked != "" {
						realizedBlocked = hr.Blocked
						continue
					}
					fullSeries[i].RealizedPriceUSD = hr.RealizedPriceUSD
					fullSeries[i].STHRealizedPriceUSD = hr.STHRealizedPrice
					fullSeries[i].LTHRealizedPriceUSD = hr.LTHRealizedPrice
					fullSeries[i].BigLensMVRV = hr.MVRV
					if hr.MVRV != nil && fullSeries[i].CMMVRV != nil && *fullSeries[i].CMMVRV > 0 {
						delta := (*hr.MVRV - *fullSeries[i].CMMVRV) / *fullSeries[i].CMMVRV * 100.0
						fullSeries[i].MVRVDeltaPct = &delta
					}
				}
			} else if bs, err := s.read.baselineStatus(ctx); err == nil && !bs.Available {
				realizedBlocked = "needs_baseline"
			}
		}
	}

	var latest BtcValuationLatest
	latest.RealizedPriceBlocked = realizedBlocked
	if n > 0 {
		last := fullSeries[n-1]
		closeCopy := last.CloseUSD
		ddCopy := last.DrawdownPct
		athCopy := athUSD
		latest.Date = last.Date
		latest.CloseUSD = &closeCopy
		latest.ATHUSD = &athCopy
		latest.ATHDate = athDate
		latest.DrawdownPct = &ddCopy
		latest.SMA200 = last.SMA200
		latest.WMA200 = last.WMA200
		latest.WMA200MonthlyPct = last.WMA200MonthlyPct
		latest.Mayer = last.Mayer
		if last.Mayer != nil && len(mayerSince2011) > 0 {
			pctl := percentileRank(mayerSince2011, *last.Mayer)
			latest.MayerPctlSince2011 = &pctl
		}
		latest.CMMVRV = last.CMMVRV
		if latest.CMMVRV == nil && len(cmRows) > 0 {
			for i := len(cmRows) - 1; i >= 0; i-- {
				if cmRows[i].CapMVRVCur != nil {
					latest.CMMVRV = cmRows[i].CapMVRVCur
					break
				}
			}
		}
		if latest.CMMVRV != nil && len(cmMVRVSince2011) > 0 {
			pctl := percentileRank(cmMVRVSince2011, *latest.CMMVRV)
			latest.CMMVRVPctlSince2011 = &pctl
		}
		latest.BigLensMVRV = last.BigLensMVRV
		latest.MVRVDeltaPct = last.MVRVDeltaPct
		latest.PuellCM = last.PuellCM
		if latest.PuellCM == nil {
			for i := n - 1; i >= 0; i-- {
				if fullSeries[i].PuellCM != nil {
					latest.PuellCM = fullSeries[i].PuellCM
					break
				}
			}
		}
		latest.PuellChain = last.PuellChain
		if latest.PuellChain == nil {
			for i := n - 1; i >= 0; i-- {
				if fullSeries[i].PuellChain != nil {
					latest.PuellChain = fullSeries[i].PuellChain
					break
				}
			}
		}
		if cov.Complete {
			for i := n - 1; i >= 0; i-- {
				if fullSeries[i].RealizedPriceUSD != nil {
					latest.RealizedPriceUSD = fullSeries[i].RealizedPriceUSD
					latest.STHRealizedPriceUSD = fullSeries[i].STHRealizedPriceUSD
					latest.LTHRealizedPriceUSD = fullSeries[i].LTHRealizedPriceUSD
					if latest.BigLensMVRV == nil {
						latest.BigLensMVRV = fullSeries[i].BigLensMVRV
						latest.MVRVDeltaPct = fullSeries[i].MVRVDeltaPct
					}
					break
				}
			}
		}
	}
	if len(cycles) > 0 {
		lastCycle := cycles[len(cycles)-1]
		if len(lastCycle.Points) > 0 {
			day := lastCycle.Points[len(lastCycle.Points)-1].Day
			latest.HalvingCycleDay = &day
		}
	}
	if len(fngRows) > 0 {
		lastFnG := fngRows[len(fngRows)-1]
		val := lastFnG.Value
		latest.FearGreedValue = &val
		latest.FearGreedClass = lastFnG.Classification
	}

	sliced := fullSeries
	if limitDays > 0 && len(sliced) > limitDays {
		sliced = sliced[len(sliced)-limitDays:]
	}
	if sliced == nil {
		sliced = []BtcValuationPoint{}
	}

	return BtcValuationData{
		AsOf:          nowStr,
		Range:         rangeKey,
		WarmingUp:     n < 200,
		PriceCoverage: cov,
		Latest:        latest,
		Series:        sliced,
		Cycles:        cycles,
		FearGreed:     fngRows,
		Sources:       sources,
	}
}
