package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

var btcHalvingEpochs = []struct {
	Epoch int
	Date  string
	Label string
}{
	{1, "2012-11-28", "Epoch 1 (2012–2016)"},
	{2, "2016-07-09", "Epoch 2 (2016–2020)"},
	{3, "2020-05-11", "Epoch 3 (2020–2024)"},
	{4, "2024-04-20", "Epoch 4 (2024–)"},
}

type BtcHalvingCyclePoint struct {
	Day     int     `json:"day"`
	Date    string  `json:"date"`
	Indexed float64 `json:"indexed"`
	Price   float64 `json:"price"`
}

type BtcHalvingCycleSeries struct {
	Epoch       int                    `json:"epoch"`
	HalvingDate string                 `json:"halving_date"`
	Label       string                 `json:"label"`
	BasePrice   float64                `json:"base_price"`
	Points      []BtcHalvingCyclePoint `json:"points"`
}

type BtcRibbonState struct {
	State           string     `json:"state"` // capitulation | recovery | normal | warming_up
	SMA30           []*float64 `json:"sma30,omitempty"`
	SMA60           []*float64 `json:"sma60,omitempty"`
	LastCrossUpDays *int       `json:"last_cross_up_days,omitempty"`
}

// smaSeries computes the simple moving average over window; returns nil if len(vals) < window.
func smaSeries(vals []float64, window int) []*float64 {
	if window <= 0 || len(vals) < window {
		return nil
	}
	out := make([]*float64, len(vals))
	var sum float64
	for i, v := range vals {
		sum += v
		if i >= window {
			sum -= vals[i-window]
		}
		if i >= window-1 {
			avg := sum / float64(window)
			out[i] = &avg
		}
	}
	return out
}

// returnsPct computes percentage returns over lookback steps; returns nil if len(vals) <= lookback.
func returnsPct(vals []float64, lookback int) []*float64 {
	if lookback <= 0 || len(vals) <= lookback {
		return nil
	}
	out := make([]*float64, len(vals))
	for i := lookback; i < len(vals); i++ {
		prev := vals[i-lookback]
		if prev > 0 {
			r := (vals[i]/prev - 1) * 100
			out[i] = &r
		}
	}
	return out
}

// realizedVol computes the annualized realized volatility (in %) over window log-return steps.
// Requires at least window+1 closes; returns nil when history is shorter.
func realizedVol(closes []float64, window int) []*float64 {
	if window <= 1 || len(closes) < window+1 {
		return nil
	}
	logRet := make([]float64, len(closes))
	valid := make([]bool, len(closes))
	for i := 1; i < len(closes); i++ {
		if closes[i] > 0 && closes[i-1] > 0 {
			logRet[i] = math.Log(closes[i] / closes[i-1])
			valid[i] = true
		}
	}
	out := make([]*float64, len(closes))
	ann := math.Sqrt(365.0) * 100.0
	for i := window; i < len(closes); i++ {
		allOK := true
		var sum float64
		for j := i - window + 1; j <= i; j++ {
			if !valid[j] {
				allOK = false
				break
			}
			sum += logRet[j]
		}
		if !allOK {
			continue
		}
		mean := sum / float64(window)
		var sq float64
		for j := i - window + 1; j <= i; j++ {
			d := logRet[j] - mean
			sq += d * d
		}
		sd := math.Sqrt(sq / float64(window))
		rv := sd * ann
		out[i] = &rv
	}
	return out
}

// drawdownSeries computes the percentage drawdown from the running maximum.
func drawdownSeries(closes []float64) []float64 {
	if len(closes) == 0 {
		return nil
	}
	out := make([]float64, len(closes))
	var peak float64
	for i, c := range closes {
		if c > peak {
			peak = c
		}
		if peak > 0 {
			out[i] = (c/peak - 1) * 100
		}
	}
	return out
}

// mayerSeries computes close / SMA200; returns nil if len(closes) < 200.
func mayerSeries(closes []float64) []*float64 {
	sma := smaSeries(closes, 200)
	if sma == nil {
		return nil
	}
	out := make([]*float64, len(closes))
	for i, s := range sma {
		if s != nil && *s > 0 {
			m := closes[i] / *s
			out[i] = &m
		}
	}
	return out
}

// wma200Series computes the 200-week moving average (1,400 daily closes).
func wma200Series(closes []float64) []*float64 {
	return smaSeries(closes, 1400)
}

// percentileRank returns the percentage (0..100) of elements in vals that are <= x.
func percentileRank(vals []float64, x float64) float64 {
	var n, le int
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		n++
		if v <= x {
			le++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(le) / float64(n) * 100
}

// halvingOverlay indexes daily closes to 1.0 on each Bitcoin halving date.
func halvingOverlay(dates []string, closes []float64) []BtcHalvingCycleSeries {
	if len(dates) == 0 || len(dates) != len(closes) {
		return nil
	}
	priceByDate := make(map[string]float64, len(dates))
	for i, d := range dates {
		if closes[i] > 0 {
			priceByDate[d] = closes[i]
		}
	}
	var out []BtcHalvingCycleSeries
	for idx, ep := range btcHalvingEpochs {
		startT, err := time.Parse("2006-01-02", ep.Date)
		if err != nil {
			continue
		}
		var endDate string
		if idx+1 < len(btcHalvingEpochs) {
			endDate = btcHalvingEpochs[idx+1].Date
		}
		base := priceByDate[ep.Date]
		var pts []BtcHalvingCyclePoint
		for i, d := range dates {
			if d < ep.Date || (endDate != "" && d >= endDate) || closes[i] <= 0 {
				continue
			}
			t, err := time.Parse("2006-01-02", d)
			if err != nil {
				continue
			}
			if base <= 0 {
				base = closes[i]
			}
			day := int(math.Round(t.Sub(startT).Hours() / 24))
			pts = append(pts, BtcHalvingCyclePoint{
				Day:     day,
				Date:    d,
				Indexed: closes[i] / base,
				Price:   closes[i],
			})
		}
		if len(pts) > 0 {
			out = append(out, BtcHalvingCycleSeries{
				Epoch:       ep.Epoch,
				HalvingDate: ep.Date,
				Label:       ep.Label,
				BasePrice:   base,
				Points:      pts,
			})
		}
	}
	return out
}

// puellSeries computes daily issuance USD divided by its 365-day moving average.
func puellSeries(dailyIssuanceUSD []float64) []*float64 {
	sma := smaSeries(dailyIssuanceUSD, 365)
	if sma == nil {
		return nil
	}
	out := make([]*float64, len(dailyIssuanceUSD))
	for i, s := range sma {
		if s != nil && *s > 0 {
			p := dailyIssuanceUSD[i] / *s
			out[i] = &p
		}
	}
	return out
}

// hashpricePerPH computes daily miner revenue per PH/s in BTC and USD.
// Formula: rewardPerBlockBTC * 86,400 * 10^15 / (difficulty * 2^32).
func hashpricePerPH(rewardPerBlockBTC, difficulty float64, priceUSD *float64) (float64, *float64) {
	if difficulty <= 0 || rewardPerBlockBTC <= 0 {
		return 0, nil
	}
	btcPerPH := rewardPerBlockBTC * 86_400.0 * 1e15 / (difficulty * math.Pow(2, 32))
	if priceUSD == nil || *priceUSD <= 0 {
		return btcPerPH, nil
	}
	usd := btcPerPH * *priceUSD
	return btcPerPH, &usd
}

// impliedHashrateEHs computes daily implied hashrate in EH/s from average difficulty and block count.
func impliedHashrateEHs(avgDifficulty float64, blocks int64) float64 {
	if avgDifficulty <= 0 || blocks <= 0 {
		return 0
	}
	return avgDifficulty * math.Pow(2, 32) * float64(blocks) / 86_400.0 / 1e18
}

// ribbonState evaluates the 30d vs 60d simple moving average of hashrate.
func ribbonState(hashrate []float64) BtcRibbonState {
	sma30 := smaSeries(hashrate, 30)
	sma60 := smaSeries(hashrate, 60)
	if sma30 == nil || sma60 == nil {
		return BtcRibbonState{State: "warming_up"}
	}
	n := len(hashrate)
	s30, s60 := sma30[n-1], sma60[n-1]
	if s30 == nil || s60 == nil {
		return BtcRibbonState{State: "warming_up"}
	}
	if *s30 < *s60 {
		return BtcRibbonState{State: "capitulation", SMA30: sma30, SMA60: sma60}
	}
	var crossDays *int
	for i := n - 1; i >= 60; i-- {
		if sma30[i] != nil && sma60[i] != nil && sma30[i-1] != nil && sma60[i-1] != nil {
			if *sma30[i] >= *sma60[i] && *sma30[i-1] < *sma60[i-1] {
				d := (n - 1) - i
				crossDays = &d
				break
			}
		}
	}
	state := "normal"
	if crossDays != nil && *crossDays <= 30 {
		state = "recovery"
	}
	return BtcRibbonState{
		State:           state,
		SMA30:           sma30,
		SMA60:           sma60,
		LastCrossUpDays: crossDays,
	}
}

// nakamoto returns the minimum number of entities needed to exceed 50% of the
// total (excluding any labels listed in exclude).
func nakamoto(shares map[string]int64, exclude ...string) int {
	excl := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excl[strings.ToLower(strings.TrimSpace(e))] = true
	}
	var vals []int64
	var total int64
	for k, v := range shares {
		if v <= 0 {
			continue
		}
		if excl[strings.ToLower(strings.TrimSpace(k))] {
			continue
		}
		vals = append(vals, v)
		total += v
	}
	if total <= 0 {
		return 0
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] > vals[j] })
	var acc int64
	for i, v := range vals {
		acc += v
		if acc*2 > total {
			return i + 1
		}
	}
	return len(vals)
}

// hhi computes the Herfindahl-Hirschman Index: Σ (share_i * 100)^2.
// "Unknown" (an aggregate of unidentified miners) contributes to the denominator
// but is excluded from the sum of squares because it is not a single entity.
func hhi(shares map[string]int64) float64 {
	var total int64
	for _, v := range shares {
		if v > 0 {
			total += v
		}
	}
	if total <= 0 {
		return 0
	}
	var sumSq float64
	for k, v := range shares {
		if v <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "unknown") {
			continue
		}
		pct := float64(v) / float64(total) * 100.0
		sumSq += pct * pct
	}
	return sumSq
}

// fundingAPR annualizes a per-interval funding rate (decimal) to an APR percentage.
func fundingAPR(rate, intervalHours float64) float64 {
	if intervalHours <= 0 {
		return 0
	}
	return rate * (8760.0 / intervalHours) * 100.0
}

// basisAPR computes annualized futures basis in percent: (F/S - 1) * (365 / daysToExpiry) * 100.
func basisAPR(futurePrice, spotPrice, daysToExpiry float64) float64 {
	if spotPrice <= 0 || daysToExpiry <= 0 {
		return 0
	}
	return (futurePrice/spotPrice - 1.0) * (365.0 / daysToExpiry) * 100.0
}
