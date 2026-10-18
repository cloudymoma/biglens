package main

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	btcMiningHashrateKeyFm = "opendata:bitcoin:mining:hashrate:%s"
	btcMiningRewardsKeyFm  = "opendata:bitcoin:mining:rewards:%s"
	btcMiningPoolsKeyFm    = "opendata:bitcoin:mining:pools:%s"
	btcMiningReward144Key  = "opendata:bitcoin:mining:reward144"

	btcMiningTTL = 10 * time.Minute
)

// BtcConcentrationMetric carries a concentration figure together with its
// denominator, unit, coverage percentage and methodology (§10.2, Principle 10).
type BtcConcentrationMetric struct {
	Value       float64 `json:"value"`
	Denominator int64   `json:"denominator"`
	Unit        string  `json:"unit"`
	CoveragePct float64 `json:"coverage_pct"`
	Method      string  `json:"method"`
}

type BtcPoolConcentration struct {
	Period      string                 `json:"period"`
	TotalBlocks int64                  `json:"total_blocks"`
	KnownBlocks int64                  `json:"known_blocks"`
	Nakamoto    BtcConcentrationMetric `json:"nakamoto"`
	HHI         BtcConcentrationMetric `json:"hhi"`
}

type BtcMiningPoolRow struct {
	Rank         int     `json:"rank"`
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	BlockCount   int64   `json:"block_count"`
	SharePct     float64 `json:"share_pct"`
	EmptyBlocks  int64   `json:"empty_blocks"`
	AvgMatchRate float64 `json:"avg_match_rate"`
}

type BtcHashratePoint struct {
	Date               string   `json:"date"`
	TS                 string   `json:"ts"`
	HashrateEHs        float64  `json:"hashrate_ehs"`
	ImpliedHashrateEHs *float64 `json:"implied_hashrate_ehs,omitempty"`
	DeltaPct           *float64 `json:"delta_pct,omitempty"`
	SMA30EHs           *float64 `json:"sma30_ehs,omitempty"`
	SMA60EHs           *float64 `json:"sma60_ehs,omitempty"`
}

type BtcDifficultyHistoryPoint struct {
	Date          string  `json:"date"`
	TS            string  `json:"ts"`
	Height        int64   `json:"height"`
	Difficulty    float64 `json:"difficulty"`
	AdjustmentPct float64 `json:"adjustment_pct"`
}

type BtcHashpricePoint struct {
	Date        string   `json:"date"`
	BTCPerPHDay float64  `json:"btc_per_ph_day"`
	USDPerPHDay *float64 `json:"usd_per_ph_day"`
}

type BtcSecurityBudget struct {
	DailyMinerRevenueBTC float64  `json:"daily_miner_revenue_btc"`
	DailyMinerRevenueUSD *float64 `json:"daily_miner_revenue_usd"`
	AnnualizedRevenueUSD *float64 `json:"annualized_revenue_usd"`
	MarketCapUSD         *float64 `json:"market_cap_usd"`
	SecurityBudgetPct    *float64 `json:"security_budget_pct"`
}

type BtcMiningChainDay struct {
	Day                string   `json:"day"`
	Blocks             int64    `json:"blocks"`
	EmptyBlocks        int64    `json:"empty_blocks"`
	AvgDifficulty      float64  `json:"avg_difficulty"`
	ImpliedHashrateEHs float64  `json:"implied_hashrate_ehs"`
	MempoolHashrateEHs *float64 `json:"mempool_hashrate_ehs,omitempty"`
	HashrateDeltaPct   *float64 `json:"hashrate_delta_pct,omitempty"`
	ScheduledSats      int64    `json:"scheduled_sats"`
	ClaimedSats        int64    `json:"claimed_sats"`
	FeesSats           int64    `json:"fees_sats"`
	FeeSharePct        float64  `json:"fee_share_pct"`
}

type BtcNodeNetworkMix struct {
	IPv4  int64 `json:"ipv4"`
	IPv6  int64 `json:"ipv6"`
	Tor   int64 `json:"tor"`
	I2P   int64 `json:"i2p"`
	CJDNS int64 `json:"cjdns"`
}

type BtcNodeBucket struct {
	Label    string  `json:"label"`
	Count    int64   `json:"count"`
	SharePct float64 `json:"share_pct"`
}

type BtcNodeCountry struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Count    int64   `json:"count"`
	SharePct float64 `json:"share_pct"`
}

type BtcNodeASN struct {
	ASN      uint32  `json:"asn"`
	Org      string  `json:"org"`
	Count    int64   `json:"count"`
	SharePct float64 `json:"share_pct"`
	IsCloud  bool    `json:"is_cloud"`
}

type BtcNodes struct {
	AsOf            string                 `json:"as_of"`
	Reachable       int64                  `json:"reachable"`
	Clearnet        int64                  `json:"clearnet"`
	Network         BtcNodeNetworkMix      `json:"network"`
	Clients         []BtcNodeBucket        `json:"clients"`
	CoreVersions    []BtcNodeBucket        `json:"core_versions"`
	Countries       []BtcNodeCountry       `json:"countries"`
	TotalASNs       int64                  `json:"total_asns"`
	ASNs            []BtcNodeASN           `json:"asns"`
	CloudShare      BtcConcentrationMetric `json:"cloud_share"`
	ASNNakamoto     BtcConcentrationMetric `json:"asn_nakamoto"`
	CountryNakamoto BtcConcentrationMetric `json:"country_nakamoto"`
	ASNHHI          BtcConcentrationMetric `json:"asn_hhi"`
	Unresolved      int64                  `json:"unresolved"`
	Attribution     string                 `json:"attribution"`
}

type BtcMiningData struct {
	AsOf              string                      `json:"as_of"`
	Period            string                      `json:"period"`
	PriceUSD          *float64                    `json:"price_usd"`
	CurrentHashrateEH *float64                    `json:"current_hashrate_ehs"`
	CurrentDifficulty *float64                    `json:"current_difficulty"`
	Hashrate          []BtcHashratePoint          `json:"hashrate"`
	Difficulty        *BtcDifficultyAdj           `json:"difficulty"`
	Adjustments       []BtcDifficultyHistoryPoint `json:"adjustments"`
	Ribbon            BtcRibbonState              `json:"ribbon"`
	Hashprice         []BtcHashpricePoint         `json:"hashprice"`
	HashpriceNow      *BtcHashpricePoint          `json:"hashprice_now"`
	Pools             []BtcMiningPoolRow          `json:"pools"`
	Concentration     BtcPoolConcentration        `json:"concentration"`
	Rewards144        *BtcRewardStats144          `json:"rewards_144"`
	FeeSharePct       *float64                    `json:"fee_share_pct"`
	SecurityBudget    BtcSecurityBudget           `json:"security_budget"`
	Halving           BtcHalving                  `json:"halving"`
	Nodes             *BtcNodes                   `json:"nodes"`
	Chain             []BtcMiningChainDay         `json:"chain,omitempty"`
	Sources           []BtcSourceStatus           `json:"sources"`
}

func computeBtcMiningPools(summary *btcMempoolPoolsSummary, period string) ([]BtcMiningPoolRow, BtcPoolConcentration) {
	if period == "" {
		period = "1w"
	}
	unit := fmt.Sprintf("blocks (%s)", period)
	conc := BtcPoolConcentration{
		Period: period,
		Nakamoto: BtcConcentrationMetric{
			Unit:   unit,
			Method: "mempool.space pool attribution (Unknown excluded)",
		},
		HHI: BtcConcentrationMetric{
			Unit:   unit,
			Method: "Σ(share%²) over attributed pools (Unknown in denominator only)",
		},
	}
	if summary == nil || len(summary.Pools) == 0 {
		return nil, conc
	}

	var sumPoolBlocks int64
	var knownBlocks int64
	shares := make(map[string]int64, len(summary.Pools)+1)
	for _, p := range summary.Pools {
		if p.BlockCount <= 0 {
			continue
		}
		sumPoolBlocks += p.BlockCount
		shares[p.Name] += p.BlockCount
		if !strings.EqualFold(strings.TrimSpace(p.Name), "unknown") && !strings.EqualFold(strings.TrimSpace(p.Slug), "unknown") {
			knownBlocks += p.BlockCount
		}
	}
	totalBlocks := summary.BlockCount
	if totalBlocks < sumPoolBlocks {
		totalBlocks = sumPoolBlocks
	}
	if totalBlocks > sumPoolBlocks {
		shares["Unknown"] += totalBlocks - sumPoolBlocks
	}
	if totalBlocks <= 0 {
		return nil, conc
	}

	rows := make([]BtcMiningPoolRow, 0, len(summary.Pools))
	for _, p := range summary.Pools {
		if p.BlockCount <= 0 {
			continue
		}
		rows = append(rows, BtcMiningPoolRow{
			Rank:         p.Rank,
			Name:         p.Name,
			Slug:         p.Slug,
			BlockCount:   p.BlockCount,
			SharePct:     float64(p.BlockCount) / float64(totalBlocks) * 100.0,
			EmptyBlocks:  p.EmptyBlocks,
			AvgMatchRate: p.AvgMatchRate,
		})
	}
	slices.SortFunc(rows, func(a, b BtcMiningPoolRow) int {
		if a.BlockCount != b.BlockCount {
			if a.BlockCount > b.BlockCount {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	for i := range rows {
		if rows[i].Rank <= 0 {
			rows[i].Rank = i + 1
		}
	}

	covPct := float64(knownBlocks) / float64(totalBlocks) * 100.0
	conc.TotalBlocks = totalBlocks
	conc.KnownBlocks = knownBlocks
	conc.Nakamoto = BtcConcentrationMetric{
		Value:       float64(nakamoto(shares, "Unknown")),
		Denominator: totalBlocks,
		Unit:        unit,
		CoveragePct: covPct,
		Method:      "mempool.space pool attribution (Unknown excluded)",
	}
	conc.HHI = BtcConcentrationMetric{
		Value:       hhi(shares),
		Denominator: totalBlocks,
		Unit:        unit,
		CoveragePct: covPct,
		Method:      "Σ(share%²) over attributed pools (Unknown in denominator only)",
	}
	return rows, conc
}

func periodLookbackDays(period string) int {
	switch normalizeMempoolMiningPeriod(period) {
	case "3m":
		return 90
	case "3y":
		return 1095
	default:
		return 365
	}
}

func (h *APIHandler) buildBtcMiningData(ctx context.Context, r *http.Request, period string) BtcMiningData {
	s := h.btc
	period = normalizeMempoolMiningPeriod(period)
	now := s.now().UTC()
	nowStr := now.Format(time.RFC3339)

	if s.prices != nil {
		s.prices.touchAsync()
	}

	var (
		wg            sync.WaitGroup
		statsVal      any
		statsStatus   BtcSourceStatus
		diffVal       any
		diffStatus    BtcSourceStatus
		hrVal         any
		hrStatus      BtcSourceStatus
		rewVal        any
		rewStatus     BtcSourceStatus
		poolsVal      any
		poolsStatus   BtcSourceStatus
		rew144Val     any
		rew144Status  BtcSourceStatus
		fallbackPrice *float64
		fallbackSrc   BtcSourceStatus
	)

	wg.Add(6)
	go func() {
		defer wg.Done()
		statsVal, statsStatus = h.btcCachedSource(ctx, btcLiveStatsKey, btcLiveStatsTTL, "coinbase", func(c context.Context) (any, string) {
			return fetchCoinbaseBTCStats(c)
		})
		if statsVal == nil {
			v, st := h.btcCachedSource(ctx, "opendata:bitcoin:live:mempool_price", btcLiveStatsTTL, "mempool", func(c context.Context) (any, string) {
				return fetchMempoolBTCPrice(c)
			})
			fallbackSrc = st
			if p, ok := v.(*float64); ok {
				fallbackPrice = p
			}
		}
	}()
	go func() {
		defer wg.Done()
		diffVal, diffStatus = h.btcCachedSource(ctx, btcLiveDiffKey, btcLiveDiffTTL, "mempool", func(c context.Context) (any, string) {
			return fetchMempoolDifficultyAdj(c)
		})
	}()
	go func() {
		defer wg.Done()
		hrVal, hrStatus = h.btcCachedSource(ctx, fmt.Sprintf(btcMiningHashrateKeyFm, period), btcMiningTTL, "mempool", func(c context.Context) (any, string) {
			return fetchMempoolMiningHashrate(c, period)
		})
	}()
	go func() {
		defer wg.Done()
		rewVal, rewStatus = h.btcCachedSource(ctx, fmt.Sprintf(btcMiningRewardsKeyFm, period), btcMiningTTL, "mempool", func(c context.Context) (any, string) {
			return fetchMempoolBlockRewards(c, period)
		})
	}()
	go func() {
		defer wg.Done()
		poolsVal, poolsStatus = h.btcCachedSource(ctx, fmt.Sprintf(btcMiningPoolsKeyFm, "1w"), btcLivePools1wTTL, "mempool", func(c context.Context) (any, string) {
			return fetchMempoolPoolsSummary(c, "1w")
		})
	}()
	go func() {
		defer wg.Done()
		rew144Val, rew144Status = h.btcCachedSource(ctx, btcMiningReward144Key, btcMiningTTL, "mempool", func(c context.Context) (any, string) {
			return fetchMempoolRewardStats144(c)
		})
	}()
	wg.Wait()

	var priceUSD *float64
	if st, ok := statsVal.(*btcSpotStats); ok && st != nil && st.PriceUSD > 0 {
		p := st.PriceUSD
		priceUSD = &p
	} else if fallbackPrice != nil && *fallbackPrice > 0 {
		p := *fallbackPrice
		priceUSD = &p
	}

	priceByDay := make(map[string]float64)
	if s.prices != nil {
		rows := s.prices.rows("", "")
		for _, pr := range rows {
			if pr.Close > 0 {
				priceByDay[pr.Day] = pr.Close
			}
		}
		if priceUSD == nil && len(rows) > 0 {
			last := rows[len(rows)-1].Close
			if last > 0 {
				priceUSD = &last
			}
		}
	}

	var diffAdj *BtcDifficultyAdj
	if d, ok := diffVal.(*BtcDifficultyAdj); ok && d != nil {
		diffAdj = d
	}

	var tipHeight int64
	var rew144 *BtcRewardStats144
	var feeSharePct *float64
	if rw, ok := rew144Val.(*BtcRewardStats144); ok && rw != nil {
		rew144 = rw
		fs := rw.FeeSharePct
		feeSharePct = &fs
		if rw.EndBlock > 0 {
			tipHeight = rw.EndBlock
		}
	}
	avgBlockSec := btcDefaultBlockSec
	if diffAdj != nil && diffAdj.AvgBlockSeconds > 0 {
		avgBlockSec = diffAdj.AvgBlockSeconds
		if tipHeight == 0 && diffAdj.NextRetargetHeight > diffAdj.RemainingBlocks {
			tipHeight = diffAdj.NextRetargetHeight - diffAdj.RemainingBlocks
		}
	}
	halving := btcHalvingAt(tipHeight, now, avgBlockSec)

	var (
		hrPoints    []BtcHashratePoint
		adjustments []BtcDifficultyHistoryPoint
		ribbon      = BtcRibbonState{State: "warming_up"}
		currHR      *float64
		currDiff    *float64
	)

	if hrResp, ok := hrVal.(*btcMempoolHashrateResp); ok && hrResp != nil {
		if hrResp.CurrentHashrate > 0 {
			eh := hrResp.CurrentHashrate / 1e18
			currHR = &eh
		}
		if hrResp.CurrentDifficulty > 0 {
			cd := hrResp.CurrentDifficulty
			currDiff = &cd
		}

		rawHR := slices.Clone(hrResp.Hashrates)
		slices.SortFunc(rawHR, func(a, b btcMempoolHashratePoint) int {
			if a.Timestamp < b.Timestamp {
				return -1
			}
			if a.Timestamp > b.Timestamp {
				return 1
			}
			return 0
		})
		ehVals := make([]float64, 0, len(rawHR))
		hrPoints = make([]BtcHashratePoint, 0, len(rawHR))
		for _, pt := range rawHR {
			t := time.Unix(pt.Timestamp, 0).UTC()
			eh := pt.AvgHashrate / 1e18
			ehVals = append(ehVals, eh)
			hrPoints = append(hrPoints, BtcHashratePoint{
				Date:        t.Format("2006-01-02"),
				TS:          t.Format(time.RFC3339),
				HashrateEHs: eh,
			})
		}
		ribbon = ribbonState(ehVals)
		for i := range hrPoints {
			if i < len(ribbon.SMA30) {
				hrPoints[i].SMA30EHs = ribbon.SMA30[i]
			}
			if i < len(ribbon.SMA60) {
				hrPoints[i].SMA60EHs = ribbon.SMA60[i]
			}
		}
		if currHR == nil && len(hrPoints) > 0 {
			lastEH := hrPoints[len(hrPoints)-1].HashrateEHs
			currHR = &lastEH
		}

		rawDiff := slices.Clone(hrResp.Difficulty)
		slices.SortFunc(rawDiff, func(a, b btcMempoolDifficultyPoint) int {
			if a.Time < b.Time {
				return -1
			}
			if a.Time > b.Time {
				return 1
			}
			return 0
		})
		adjustments = make([]BtcDifficultyHistoryPoint, 0, len(rawDiff))
		for _, dp := range rawDiff {
			t := time.Unix(dp.Time, 0).UTC()
			adjustments = append(adjustments, BtcDifficultyHistoryPoint{
				Date:          t.Format("2006-01-02"),
				TS:            t.Format(time.RFC3339),
				Height:        dp.Height,
				Difficulty:    dp.Difficulty,
				AdjustmentPct: (dp.Adjustment - 1.0) * 100.0,
			})
		}
		if currDiff == nil && len(adjustments) > 0 {
			lastD := adjustments[len(adjustments)-1].Difficulty
			currDiff = &lastD
		}
	}

	var poolsSummary *btcMempoolPoolsSummary
	if ps, ok := poolsVal.(*btcMempoolPoolsSummary); ok && ps != nil {
		poolsSummary = ps
		if currHR == nil && ps.LastEstimatedHashrate1w > 0 {
			eh := ps.LastEstimatedHashrate1w / 1e18
			currHR = &eh
		}
	}
	pools, conc := computeBtcMiningPools(poolsSummary, "1w")

	rewardByDay := make(map[string]float64)
	if rewPts, ok := rewVal.([]btcMempoolBlockRewardPt); ok {
		for _, rp := range rewPts {
			if rp.Timestamp > 0 && rp.AvgRewards > 0 {
				d := time.Unix(rp.Timestamp, 0).UTC().Format("2006-01-02")
				rewardByDay[d] = rp.AvgRewards / 1e8
			}
		}
	}

	diffForDate := func(day string) float64 {
		var d float64
		for _, adj := range adjustments {
			if adj.Date <= day && adj.Difficulty > 0 {
				d = adj.Difficulty
			}
		}
		if d <= 0 && len(adjustments) > 0 {
			d = adjustments[0].Difficulty
		}
		if d <= 0 && currDiff != nil {
			d = *currDiff
		}
		return d
	}

	hashpriceSeries := make([]BtcHashpricePoint, 0, len(hrPoints))
	for _, hp := range hrPoints {
		diff := diffForDate(hp.Date)
		if diff <= 0 {
			continue
		}
		rewBTC := rewardByDay[hp.Date]
		if rewBTC <= 0 {
			rewBTC = halving.SubsidyNowBTC
			if hp.Date < "2024-04-20" {
				rewBTC = 6.25
			}
		}
		var dayPrice *float64
		if c, ok := priceByDay[hp.Date]; ok && c > 0 {
			p := c
			dayPrice = &p
		} else if priceUSD != nil {
			dayPrice = priceUSD
		}
		btcPH, usdPH := hashpricePerPH(rewBTC, diff, dayPrice)
		if btcPH > 0 {
			hashpriceSeries = append(hashpriceSeries, BtcHashpricePoint{
				Date:        hp.Date,
				BTCPerPHDay: btcPH,
				USDPerPHDay: usdPH,
			})
		}
	}

	var hashpriceNow *BtcHashpricePoint
	nowDiff := 0.0
	if currDiff != nil && *currDiff > 0 {
		nowDiff = *currDiff
	} else if len(adjustments) > 0 {
		nowDiff = adjustments[len(adjustments)-1].Difficulty
	}
	if nowDiff > 0 {
		rewNowBTC := halving.SubsidyNowBTC
		if rew144 != nil && rew144.TotalRewardBTC > 0 {
			blocks := rew144.EndBlock - rew144.StartBlock + 1
			if blocks <= 0 {
				blocks = 144
			}
			rewNowBTC = rew144.TotalRewardBTC / float64(blocks)
		} else if len(hrPoints) > 0 {
			if r, ok := rewardByDay[hrPoints[len(hrPoints)-1].Date]; ok && r > 0 {
				rewNowBTC = r
			}
		}
		btcPH, usdPH := hashpricePerPH(rewNowBTC, nowDiff, priceUSD)
		if btcPH > 0 {
			hashpriceNow = &BtcHashpricePoint{
				Date:        now.Format("2006-01-02"),
				BTCPerPHDay: btcPH,
				USDPerPHDay: usdPH,
			}
		}
	}

	dailyRevBTC := halving.SubsidyNowBTC * 144.0
	if rew144 != nil && rew144.TotalRewardBTC > 0 {
		dailyRevBTC = rew144.TotalRewardBTC
	}
	secBudget := BtcSecurityBudget{
		DailyMinerRevenueBTC: dailyRevBTC,
	}
	if priceUSD != nil && *priceUSD > 0 {
		dailyUSD := dailyRevBTC * (*priceUSD)
		annUSD := dailyUSD * 365.0
		secBudget.DailyMinerRevenueUSD = &dailyUSD
		secBudget.AnnualizedRevenueUSD = &annUSD
		if halving.ScheduledSupply > 0 {
			mcap := halving.ScheduledSupply * (*priceUSD)
			secBudget.MarketCapUSD = &mcap
			if mcap > 0 {
				pct := annUSD / mcap * 100.0
				secBudget.SecurityBudgetPct = &pct
			}
		}
	}

	var chainRows []BtcMiningChainDay
	var bqSource *BtcSourceStatus
	if s.read != nil {
		fromDay := now.AddDate(0, 0, -periodLookbackDays(period)).Format("2006-01-02")
		toDay := now.Format("2006-01-02")
		if onchain, err := s.read.onchainDays(ctx, fromDay, toDay); err == nil && len(onchain) > 0 {
			hrIdxByDate := make(map[string]int, len(hrPoints))
			for i, hp := range hrPoints {
				hrIdxByDate[hp.Date] = i
			}
			chainRows = make([]BtcMiningChainDay, 0, len(onchain))
			for _, oc := range onchain {
				implied := oc.ImpliedHashrateEHs
				if implied <= 0 && oc.AvgDifficulty > 0 && oc.Blocks > 0 {
					implied = impliedHashrateEHs(oc.AvgDifficulty, oc.Blocks)
				}
				claimed := oc.ClaimedIssuanceSats
				if claimed == 0 && oc.CoinbaseSats > oc.FeesSats {
					claimed = oc.CoinbaseSats - oc.FeesSats
				}
				feeShare := oc.FeeSharePct
				if feeShare == 0 && oc.CoinbaseSats > 0 {
					feeShare = float64(oc.FeesSats) / float64(oc.CoinbaseSats) * 100.0
				}
				row := BtcMiningChainDay{
					Day:                oc.Date,
					Blocks:             oc.Blocks,
					EmptyBlocks:        oc.EmptyBlocks,
					AvgDifficulty:      oc.AvgDifficulty,
					ImpliedHashrateEHs: implied,
					ScheduledSats:      oc.ScheduledIssuanceSats,
					ClaimedSats:        claimed,
					FeesSats:           oc.FeesSats,
					FeeSharePct:        feeShare,
				}
				if idx, ok := hrIdxByDate[oc.Date]; ok && hrPoints[idx].HashrateEHs > 0 && implied > 0 {
					memEH := hrPoints[idx].HashrateEHs
					delta := (implied - memEH) / memEH * 100.0
					row.MempoolHashrateEHs = &memEH
					row.HashrateDeltaPct = &delta
					impCopy := implied
					deltaCopy := delta
					hrPoints[idx].ImpliedHashrateEHs = &impCopy
					hrPoints[idx].DeltaPct = &deltaCopy
				}
				chainRows = append(chainRows, row)
			}
			meta := s.providerMeta("bigquery")
			bqSource = &BtcSourceStatus{
				ID:      meta.ID,
				Label:   meta.Label,
				Status:  "ok",
				AsOf:    onchain[len(onchain)-1].Date,
				License: meta.License,
			}
		}
	}

	var (
		nodesData   *BtcNodes
		bitnodesSrc BtcSourceStatus
		dbipSrc     BtcSourceStatus
	)
	if s.nodes != nil {
		nodesData, bitnodesSrc, dbipSrc = s.nodes.get(ctx, s)
	}

	mempoolStatus := mergeBtcSourceStatuses(s.providerMeta("mempool"), diffStatus, hrStatus, rewStatus, poolsStatus, rew144Status)
	sources := []BtcSourceStatus{mempoolStatus}
	if statsVal != nil || !s.providerEnabled("coinbase") {
		sources = append(sources, statsStatus)
	} else if fallbackSrc.ID != "" && fallbackSrc.ID != "mempool" {
		sources = append(sources, fallbackSrc)
	} else {
		sources = append(sources, statsStatus)
	}
	if bitnodesSrc.ID != "" {
		sources = append(sources, bitnodesSrc)
	}
	if dbipSrc.ID != "" {
		sources = append(sources, dbipSrc)
	}
	if bqSource != nil {
		sources = append(sources, *bqSource)
	}

	return BtcMiningData{
		AsOf:              nowStr,
		Period:            period,
		PriceUSD:          priceUSD,
		CurrentHashrateEH: currHR,
		CurrentDifficulty: currDiff,
		Hashrate:          hrPoints,
		Difficulty:        diffAdj,
		Adjustments:       adjustments,
		Ribbon:            ribbon,
		Hashprice:         hashpriceSeries,
		HashpriceNow:      hashpriceNow,
		Pools:             pools,
		Concentration:     conc,
		Rewards144:        rew144,
		FeeSharePct:       feeSharePct,
		SecurityBudget:    secBudget,
		Halving:           halving,
		Nodes:             nodesData,
		Chain:             chainRows,
		Sources:           sources,
	}
}

func mergeBtcSourceStatuses(meta btcProviderDef, statuses ...BtcSourceStatus) BtcSourceStatus {
	out := BtcSourceStatus{
		ID:      meta.ID,
		Label:   meta.Label,
		Status:  "disabled",
		License: meta.License,
	}
	for _, st := range statuses {
		if st.ID == "" {
			continue
		}
		if st.Status == "disabled" {
			out.Status = "disabled"
			continue
		}
		if st.Status == "ok" && out.Status != "stale" && out.Status != "error" && out.Status != "rate_limited" && out.Status != "geo_blocked" {
			out.Status = "ok"
			if st.AsOf > out.AsOf {
				out.AsOf = st.AsOf
			}
		} else if st.Status == "stale" && out.Status != "error" {
			out.Status = "stale"
			if st.AsOf != "" {
				out.AsOf = st.AsOf
			}
			if st.Error != "" {
				out.Error = st.Error
			}
		} else if st.Status == "error" || st.Status == "rate_limited" || st.Status == "geo_blocked" {
			if out.Status != "ok" && out.Status != "stale" {
				out.Status = st.Status
				out.Error = st.Error
			}
		}
	}
	return out
}

func (h *APIHandler) BitcoinMining(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	period := normalizeMempoolMiningPeriod(r.URL.Query().Get("period"))
	data := h.buildBtcMiningData(r.Context(), r, period)
	writeJSON(w, data)
}
