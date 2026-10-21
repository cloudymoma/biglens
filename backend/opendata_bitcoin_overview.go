package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	btcOverviewGlobalKey = "opendata:bitcoin:overview:cg_global"
	btcOverviewGlobalTTL = 10 * time.Minute

	// Named regime-indicator thresholds (§10.4).
	btcRegimeValuationLowPctl         = 20.0
	btcRegimeValuationHighPctl        = 80.0
	btcRegimeMinerRecoveryDays        = 30
	btcRegimeFundingNegativeAPR       = 0.0
	btcRegimeFundingElevatedAPR       = 15.0
	btcRegimeFundingOverheatedAPR     = 30.0
	btcRegimeDerivSpotElevatedRatio   = 1.25
	btcRegimeDerivSpotOverheatedRatio = 1.50
	btcRegimeVRPRichThreshold         = 5.0
	btcRegimeVRPCheapThreshold        = -5.0
)

// BtcOverviewConcentration presents pool and node concentration in separate
// labelled rows with each layer's denominator, unit, coverage and methodology (§5.2, §10.2).
// No field combines or compares pool concentration and node concentration.
type BtcOverviewConcentration struct {
	PoolPeriod           string                  `json:"pool_period"`
	PoolNakamoto         BtcConcentrationMetric  `json:"pool_nakamoto"`
	PoolHHI              BtcConcentrationMetric  `json:"pool_hhi"`
	NodeReachable        int64                   `json:"node_reachable"`
	NodeClearnet         int64                   `json:"node_clearnet"`
	NodeClearnetSharePct *float64                `json:"node_clearnet_share_pct"`
	NodeCountryNakamoto  *BtcConcentrationMetric `json:"node_country_nakamoto,omitempty"`
	NodeASNNakamoto      *BtcConcentrationMetric `json:"node_asn_nakamoto,omitempty"`
	NodeASNHHI           *BtcConcentrationMetric `json:"node_asn_hhi,omitempty"`
	NodeCloudShare       *BtcConcentrationMetric `json:"node_cloud_share,omitempty"`
}

// BtcOverviewCards holds the headline KPI cards on the Overview tab (§5.2).
type BtcOverviewCards struct {
	CloseUSD              *float64                 `json:"close_usd"`
	MarketCapUSD          *float64                 `json:"market_cap_usd"`
	DominancePct          *float64                 `json:"dominance_pct"`
	DrawdownPct           *float64                 `json:"drawdown_pct"`
	ATHUSD                *float64                 `json:"ath_usd"`
	ATHDate               string                   `json:"ath_date,omitempty"`
	RV30                  *float64                 `json:"rv30"`
	DVOL                  *float64                 `json:"dvol"`
	VRP                   *float64                 `json:"vrp"`
	FundingOIWeightedAPR  *float64                 `json:"funding_oi_weighted_apr"`
	TotalOIUSD            *float64                 `json:"total_oi_usd"`
	MajorDerivSpot        *float64                 `json:"major_deriv_spot"`
	AllReportedDerivSpot  *float64                 `json:"all_reported_deriv_spot"`
	AllReportedUnverified bool                     `json:"all_reported_unverified"`
	Mayer                 *float64                 `json:"mayer"`
	MayerPctlSince2011    *float64                 `json:"mayer_pctl_since_2011"`
	CMMVRV                *float64                 `json:"cm_mvrv"`
	CMMVRVPctlSince2011   *float64                 `json:"cm_mvrv_pctl_since_2011"`
	BigLensMVRV           *float64                 `json:"biglens_mvrv,omitempty"`
	MVRVDeltaPct          *float64                 `json:"mvrv_delta_pct,omitempty"`
	RealizedPriceBlocked  string                   `json:"realized_price_blocked,omitempty"`
	HashpriceUSDPerPHDay  *float64                 `json:"hashprice_usd_per_ph_day"`
	HashpriceBTCPerPHDay  *float64                 `json:"hashprice_btc_per_ph_day"`
	ExchangeNetFlow7dUSD  *float64                 `json:"exchange_net_flow_7d_usd"`
	ExchangeNetFlow7dBTC  *float64                 `json:"exchange_net_flow_7d_btc"`
	ExchangeFlowFlash     bool                     `json:"exchange_flow_flash,omitempty"`
	Concentration         BtcOverviewConcentration `json:"concentration"`
}

// BtcRegimeSignal represents one descriptive regime indicator (§10.4).
type BtcRegimeSignal struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Rule    string `json:"rule"`
}

// BtcOverviewSupply summarizes Bitcoin issuance, scheduled supply, exchange balances, and public treasuries (§5.2).
type BtcOverviewSupply struct {
	TipHeight           *int64   `json:"tip_height"`
	ScheduledSupplyBTC  *float64 `json:"scheduled_supply_btc"`
	MaxSupplyBTC        float64  `json:"max_supply_btc"`
	MinedPct            *float64 `json:"mined_pct"`
	SubsidyBTC          *float64 `json:"subsidy_btc"`
	AnnualIssuanceBTC   *float64 `json:"annual_issuance_btc"`
	AnnualInflationPct  *float64 `json:"annual_inflation_pct"`
	ExchangeBalanceBTC  *float64 `json:"exchange_balance_btc"`
	ExchangeBalancePct  *float64 `json:"exchange_balance_pct"`
	TreasuriesBTC       *float64 `json:"treasuries_btc"`
	TreasuriesPct       *float64 `json:"treasuries_pct"`
	TreasuriesCompanies int      `json:"treasuries_companies"`
}

// BtcOverviewSentiment holds the Fear & Greed Index summary and recent history (§5.2).
type BtcOverviewSentiment struct {
	FearGreedValue   *int          `json:"fear_greed_value,omitempty"`
	FearGreedClass   string        `json:"fear_greed_class,omitempty"`
	FearGreedHistory []BtcFnGPoint `json:"fear_greed_history"`
}

// BtcVerificationRow compares a public-API metric against its independent on-chain BigQuery counterpart (§10.3).
type BtcVerificationRow struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Date       string   `json:"date,omitempty"`
	Unit       string   `json:"unit"`
	APILabel   string   `json:"api_label"`
	APIValue   *float64 `json:"api_value"`
	ChainLabel string   `json:"chain_label"`
	ChainValue *float64 `json:"chain_value"`
	DeltaPct   *float64 `json:"delta_pct,omitempty"`
	Status     string   `json:"status"` // ok | needs_bigquery | needs_baseline | price_history_incomplete
	Note       string   `json:"note,omitempty"`
}

// BtcOverviewData is the response payload for GET /api/opendata/bitcoin/overview.
type BtcOverviewData struct {
	AsOf         string               `json:"as_of"`
	Cards        BtcOverviewCards     `json:"cards"`
	PriceChart   []BtcValuationPoint  `json:"price_chart"`
	Signals      []BtcRegimeSignal    `json:"signals"`
	Supply       BtcOverviewSupply    `json:"supply"`
	Sentiment    BtcOverviewSentiment `json:"sentiment"`
	Verification []BtcVerificationRow `json:"verification"`
	Sources      []SourceStatus       `json:"sources"`
}

type btcRegimeInputs struct {
	CloseUSD             *float64
	SMA200               *float64
	WMA200               *float64
	CMMVRVPctlSince2011  *float64
	Ribbon               BtcRibbonState
	FundingOIWeightedAPR *float64
	MajorDerivSpot       *float64
	DerivSpotMedian90d   *float64
	DVOL                 *float64
	RV30                 *float64
	Consolidation        BtcConsolidationSignal
	ExchangeNetFlow7dUSD *float64
}

func computeBtcRegimeSignals(in btcRegimeInputs) []BtcRegimeSignal {
	// 1. Trend: close vs SMA200 and WMA200 -> above_both | between | below_both.
	trend := BtcRegimeSignal{
		ID:    "trend",
		Label: "Trend",
		State: "unavailable",
		Rule:  "Close vs 200-day SMA and 200-week MA (1,400d SMA)",
	}
	if in.CloseUSD != nil && in.SMA200 != nil && in.WMA200 != nil {
		c, s200, w200 := *in.CloseUSD, *in.SMA200, *in.WMA200
		switch {
		case c > s200 && c > w200:
			trend.State = "above_both"
			trend.Summary = fmt.Sprintf("Close $%.0f above both 200DMA ($%.0f) and 200WMA ($%.0f)", c, s200, w200)
		case c < s200 && c < w200:
			trend.State = "below_both"
			trend.Summary = fmt.Sprintf("Close $%.0f below both 200DMA ($%.0f) and 200WMA ($%.0f)", c, s200, w200)
		default:
			trend.State = "between"
			trend.Summary = fmt.Sprintf("Close $%.0f between 200DMA ($%.0f) and 200WMA ($%.0f)", c, s200, w200)
		}
	} else {
		trend.Summary = "Waiting for 200-week price history"
	}

	// 2. Valuation: Coin Metrics MVRV percentile since 2011 -> low (< 20) | mid (20-80) | high (> 80).
	valuation := BtcRegimeSignal{
		ID:    "valuation",
		Label: "Valuation",
		State: "unavailable",
		Rule:  fmt.Sprintf("Coin Metrics MVRV percentile since 2011 (< %.0f low · %.0f–%.0f mid · > %.0f high)", btcRegimeValuationLowPctl, btcRegimeValuationLowPctl, btcRegimeValuationHighPctl, btcRegimeValuationHighPctl),
	}
	if in.CMMVRVPctlSince2011 != nil {
		p := *in.CMMVRVPctlSince2011
		switch {
		case p < btcRegimeValuationLowPctl:
			valuation.State = "low"
		case p > btcRegimeValuationHighPctl:
			valuation.State = "high"
		default:
			valuation.State = "mid"
		}
		valuation.Summary = fmt.Sprintf("MVRV at %.1fth percentile since 2011 (%s)", p, valuation.State)
	} else {
		valuation.Summary = "MVRV percentile unavailable"
	}

	// 3. Miners: hash ribbon -> capitulation | recovery (cross <= 30d ago) | normal.
	miners := BtcRegimeSignal{
		ID:    "miners",
		Label: "Miners",
		State: "unavailable",
		Rule:  fmt.Sprintf("Hash ribbon (SMA30 vs SMA60 of daily hashrate; recovery cross ≤ %dd ago)", btcRegimeMinerRecoveryDays),
	}
	switch in.Ribbon.State {
	case "capitulation":
		miners.State = "capitulation"
		miners.Summary = "SMA30 < SMA60 (miner capitulation)"
	case "recovery":
		miners.State = "recovery"
		if in.Ribbon.LastCrossUpDays != nil {
			miners.Summary = fmt.Sprintf("SMA30 ≥ SMA60 (recovery cross %dd ago)", *in.Ribbon.LastCrossUpDays)
		} else {
			miners.Summary = "SMA30 ≥ SMA60 (hashrate recovery)"
		}
	case "normal":
		if in.Ribbon.LastCrossUpDays != nil && *in.Ribbon.LastCrossUpDays <= btcRegimeMinerRecoveryDays {
			miners.State = "recovery"
			miners.Summary = fmt.Sprintf("SMA30 ≥ SMA60 (recovery cross %dd ago)", *in.Ribbon.LastCrossUpDays)
		} else {
			miners.State = "normal"
			miners.Summary = "SMA30 ≥ SMA60 (normal expansion)"
		}
	default:
		miners.Summary = "Hashrate history warming up"
	}

	// 4. Leverage: OI-weighted funding APR and derivatives/spot ratio vs 90-day median -> negative | neutral | elevated | overheated.
	leverage := BtcRegimeSignal{
		ID:    "leverage",
		Label: "Leverage",
		State: "unavailable",
		Rule:  fmt.Sprintf("OI-weighted funding APR (< %.0f%% negative · ≥ %.0f%% elevated · ≥ %.0f%% overheated) and deriv/spot vs 90d median", btcRegimeFundingNegativeAPR, btcRegimeFundingElevatedAPR, btcRegimeFundingOverheatedAPR),
	}
	if in.FundingOIWeightedAPR != nil || in.MajorDerivSpot != nil {
		var ratioVsMed float64
		hasRatioVsMed := false
		if in.MajorDerivSpot != nil && in.DerivSpotMedian90d != nil && *in.DerivSpotMedian90d > 0 {
			ratioVsMed = *in.MajorDerivSpot / *in.DerivSpotMedian90d
			hasRatioVsMed = true
		}
		switch {
		case in.FundingOIWeightedAPR != nil && *in.FundingOIWeightedAPR < btcRegimeFundingNegativeAPR:
			leverage.State = "negative"
		case (in.FundingOIWeightedAPR != nil && *in.FundingOIWeightedAPR >= btcRegimeFundingOverheatedAPR) ||
			(hasRatioVsMed && ratioVsMed >= btcRegimeDerivSpotOverheatedRatio):
			leverage.State = "overheated"
		case (in.FundingOIWeightedAPR != nil && *in.FundingOIWeightedAPR >= btcRegimeFundingElevatedAPR) ||
			(hasRatioVsMed && ratioVsMed >= btcRegimeDerivSpotElevatedRatio):
			leverage.State = "elevated"
		default:
			leverage.State = "neutral"
		}
		switch {
		case in.FundingOIWeightedAPR != nil && in.MajorDerivSpot != nil:
			leverage.Summary = fmt.Sprintf("Funding %+.2f%% APR · deriv/spot %.2f×", *in.FundingOIWeightedAPR, *in.MajorDerivSpot)
		case in.FundingOIWeightedAPR != nil:
			leverage.Summary = fmt.Sprintf("Funding %+.2f%% APR", *in.FundingOIWeightedAPR)
		default:
			leverage.Summary = fmt.Sprintf("Deriv/spot %.2f×", *in.MajorDerivSpot)
		}
	} else {
		leverage.Summary = "Funding and ratio unavailable"
	}

	// 5. Volatility: DVOL - RV30 -> rich (> +5) | fair | cheap (< -5).
	volatility := BtcRegimeSignal{
		ID:    "volatility",
		Label: "Volatility",
		State: "unavailable",
		Rule:  fmt.Sprintf("VRP = DVOL − RV30 (> %+.0f rich · %+.0f to %+.0f fair · < %+.0f cheap)", btcRegimeVRPRichThreshold, btcRegimeVRPCheapThreshold, btcRegimeVRPRichThreshold, btcRegimeVRPCheapThreshold),
	}
	if in.DVOL != nil && in.RV30 != nil {
		vrp := *in.DVOL - *in.RV30
		switch {
		case vrp > btcRegimeVRPRichThreshold:
			volatility.State = "rich"
		case vrp < btcRegimeVRPCheapThreshold:
			volatility.State = "cheap"
		default:
			volatility.State = "fair"
		}
		volatility.Summary = fmt.Sprintf("DVOL %.1f%% vs RV30 %.1f%% (VRP %+.1f pts)", *in.DVOL, *in.RV30, vrp)
	} else {
		volatility.Summary = "DVOL or RV30 unavailable"
	}

	// 6. Fee market: consolidation rule -> window_open | closed | unavailable.
	feeMarket := BtcRegimeSignal{
		ID:      "fee_market",
		Label:   "Fee Market",
		State:   "unavailable",
		Rule:    fmt.Sprintf("Consolidation window: economy ≤ %.0f sat/vB and backlog ≤ %.0f blocks", btcConsolidationMaxSatVB, btcConsolidationMaxBlocks),
		Summary: in.Consolidation.Reason,
	}
	if in.Consolidation.EconomySatVB <= 0 {
		feeMarket.State = "unavailable"
		if feeMarket.Summary == "" {
			feeMarket.Summary = "Mempool fee telemetry unavailable"
		}
	} else if in.Consolidation.Open {
		feeMarket.State = "window_open"
	} else {
		feeMarket.State = "closed"
	}

	// 7. Flows: 7d CM exchange net flow -> net_outflow | net_inflow.
	flows := BtcRegimeSignal{
		ID:    "flows",
		Label: "Flows",
		State: "unavailable",
		Rule:  "7-day Coin Metrics exchange net flow (FlowInExUSD − FlowOutExUSD)",
	}
	if in.ExchangeNetFlow7dUSD != nil {
		net := *in.ExchangeNetFlow7dUSD
		if net < 0 {
			flows.State = "net_outflow"
			flows.Summary = fmt.Sprintf("7d net outflow $%.1fM", math.Abs(net)/1e6)
		} else {
			flows.State = "net_inflow"
			flows.Summary = fmt.Sprintf("7d net inflow +$%.1fM", net/1e6)
		}
	} else {
		flows.Summary = "Exchange net flow unavailable"
	}

	return []BtcRegimeSignal{trend, valuation, miners, leverage, volatility, feeMarket, flows}
}

type btcVerificationInputs struct {
	Date               string
	MempoolHashrateEHs *float64
	ImpliedHashrateEHs *float64
	CMRow              *BtcCMMetricRow
	MempoolRewardsBTC  *float64
	OnchainDay         *BtcOnchainDay
	HolderDay          *BtcHolderDay
	BaselineAvailable  bool
	RealizedBlocked    string
}

func computeVerificationDeltaPct(chainVal, apiVal *float64) *float64 {
	if chainVal == nil || apiVal == nil || *apiVal == 0 {
		return nil
	}
	d := (*chainVal - *apiVal) / *apiVal * 100.0
	return &d
}

func computeBtcVerificationRows(in btcVerificationInputs) []BtcVerificationRow {
	var (
		cmTxCnt, cmAdrAct, cmIssBTC, cmFeeBTC, cmSplyBTC, cmMVRV *float64
		cmDate                                                   string
	)
	if in.CMRow != nil {
		cmDate = in.CMRow.Date
		if in.CMRow.TxCnt != nil {
			v := float64(*in.CMRow.TxCnt)
			cmTxCnt = &v
		}
		if in.CMRow.AdrActCnt != nil {
			v := float64(*in.CMRow.AdrActCnt)
			cmAdrAct = &v
		}
		cmIssBTC = in.CMRow.IssTotNtv
		cmFeeBTC = in.CMRow.FeeTotNtv
		cmSplyBTC = in.CMRow.SplyCur
		cmMVRV = in.CMRow.CapMVRVCur
	}

	var (
		chainTxs, chainActAddrs, chainIssBTC, chainFeeBTC, chainRevBTC *float64
		ocDate                                                         string
	)
	if in.OnchainDay != nil {
		ocDate = in.OnchainDay.Date
		txs := float64(in.OnchainDay.Txs)
		act := float64(in.OnchainDay.ActiveAddresses)
		iss := float64(in.OnchainDay.ScheduledIssuanceSats) / 1e8
		fees := float64(in.OnchainDay.FeesSats) / 1e8
		rev := float64(in.OnchainDay.CoinbaseSats) / 1e8
		chainTxs = &txs
		chainActAddrs = &act
		chainIssBTC = &iss
		chainFeeBTC = &fees
		chainRevBTC = &rev
	}

	rowDate := in.Date
	if rowDate == "" {
		if ocDate != "" {
			rowDate = ocDate
		} else {
			rowDate = cmDate
		}
	}

	var apiMinerRev *float64
	apiMinerRevLabel := "CM IssTotNtv + FeeTotNtv"
	if cmIssBTC != nil && cmFeeBTC != nil && (ocDate == "" || cmDate == "" || cmDate == ocDate) {
		sum := *cmIssBTC + *cmFeeBTC
		apiMinerRev = &sum
	}

	m2Status := func(apiVal, chainVal *float64) (string, string) {
		if chainVal == nil {
			return "needs_bigquery", "needs BigQuery rollup"
		}
		if apiVal == nil {
			return "partial", "API metric unavailable"
		}
		return "ok", ""
	}

	hrStatus, hrNote := m2Status(in.MempoolHashrateEHs, in.ImpliedHashrateEHs)
	txStatus, txNote := m2Status(cmTxCnt, chainTxs)
	adrStatus, adrNote := m2Status(cmAdrAct, chainActAddrs)
	issStatus, issNote := m2Status(cmIssBTC, chainIssBTC)
	feeStatus, feeNote := m2Status(cmFeeBTC, chainFeeBTC)
	revStatus, revNote := m2Status(apiMinerRev, chainRevBTC)
	if chainRevBTC != nil && apiMinerRev == nil {
		revStatus = "unavailable"
		revNote = "same-day Coin Metrics revenue unavailable"
	}

	var (
		chainSupplyBTC *float64
		chainMVRV      *float64
		supplyStatus   = "needs_baseline"
		supplyNote     = "needs baseline"
		mvrvStatus     = "needs_baseline"
		mvrvNote       = "needs baseline"
	)
	if in.BaselineAvailable && in.HolderDay != nil {
		if in.HolderDay.SupplySats > 0 {
			sp := float64(in.HolderDay.SupplySats) / 1e8
			chainSupplyBTC = &sp
			supplyStatus = "ok"
			supplyNote = ""
		}
		if in.HolderDay.Blocked != "" {
			mvrvStatus = in.HolderDay.Blocked
			mvrvNote = in.HolderDay.Blocked
		} else if in.RealizedBlocked != "" {
			mvrvStatus = in.RealizedBlocked
			mvrvNote = in.RealizedBlocked
		} else if in.HolderDay.MVRV != nil {
			v := *in.HolderDay.MVRV
			chainMVRV = &v
			mvrvStatus = "ok"
			mvrvNote = ""
		}
	} else if in.BaselineAvailable && in.RealizedBlocked != "" {
		mvrvStatus = in.RealizedBlocked
		mvrvNote = in.RealizedBlocked
	}

	return []BtcVerificationRow{
		{
			ID:         "hashrate",
			Label:      "Hashrate",
			Date:       rowDate,
			Unit:       "EH/s",
			APILabel:   "mempool.space",
			APIValue:   in.MempoolHashrateEHs,
			ChainLabel: "Implied (A2 difficulty × blocks)",
			ChainValue: in.ImpliedHashrateEHs,
			DeltaPct:   computeVerificationDeltaPct(in.ImpliedHashrateEHs, in.MempoolHashrateEHs),
			Status:     hrStatus,
			Note:       hrNote,
		},
		{
			ID:         "tx_count",
			Label:      "Transactions (excl. coinbase)",
			Date:       rowDate,
			Unit:       "txs",
			APILabel:   "Coin Metrics TxCnt",
			APIValue:   cmTxCnt,
			ChainLabel: "BigQuery A1 txs",
			ChainValue: chainTxs,
			DeltaPct:   computeVerificationDeltaPct(chainTxs, cmTxCnt),
			Status:     txStatus,
			Note:       txNote,
		},
		{
			ID:         "active_addresses",
			Label:      "Active Addresses",
			Date:       rowDate,
			Unit:       "addrs",
			APILabel:   "Coin Metrics AdrActCnt",
			APIValue:   cmAdrAct,
			ChainLabel: "BigQuery A1 active_addresses",
			ChainValue: chainActAddrs,
			DeltaPct:   computeVerificationDeltaPct(chainActAddrs, cmAdrAct),
			Status:     adrStatus,
			Note:       adrNote,
		},
		{
			ID:         "issuance",
			Label:      "Scheduled Issuance",
			Date:       rowDate,
			Unit:       "BTC",
			APILabel:   "Coin Metrics IssTotNtv",
			APIValue:   cmIssBTC,
			ChainLabel: "BigQuery A2 Σ subsidy_sats",
			ChainValue: chainIssBTC,
			DeltaPct:   computeVerificationDeltaPct(chainIssBTC, cmIssBTC),
			Status:     issStatus,
			Note:       issNote,
		},
		{
			ID:         "fees",
			Label:      "Total Fees",
			Date:       rowDate,
			Unit:       "BTC",
			APILabel:   "Coin Metrics FeeTotNtv",
			APIValue:   cmFeeBTC,
			ChainLabel: "BigQuery A1 fees_sats",
			ChainValue: chainFeeBTC,
			DeltaPct:   computeVerificationDeltaPct(chainFeeBTC, cmFeeBTC),
			Status:     feeStatus,
			Note:       feeNote,
		},
		{
			ID:         "miner_revenue",
			Label:      "Miner Revenue",
			Date:       rowDate,
			Unit:       "BTC",
			APILabel:   apiMinerRevLabel,
			APIValue:   apiMinerRev,
			ChainLabel: "BigQuery A1 coinbase_sats",
			ChainValue: chainRevBTC,
			DeltaPct:   computeVerificationDeltaPct(chainRevBTC, apiMinerRev),
			Status:     revStatus,
			Note:       revNote,
		},
		{
			ID:         "supply",
			Label:      "Circulating Supply",
			Date:       rowDate,
			Unit:       "BTC",
			APILabel:   "Coin Metrics SplyCur",
			APIValue:   cmSplyBTC,
			ChainLabel: "BigQuery Holder ΣU_t",
			ChainValue: chainSupplyBTC,
			DeltaPct:   computeVerificationDeltaPct(chainSupplyBTC, cmSplyBTC),
			Status:     supplyStatus,
			Note:       supplyNote,
		},
		{
			ID:         "mvrv",
			Label:      "MVRV Ratio",
			Date:       rowDate,
			Unit:       "ratio",
			APILabel:   "Coin Metrics CapMVRVCur",
			APIValue:   cmMVRV,
			ChainLabel: "BigLens MVRV (supply × close / realized cap)",
			ChainValue: chainMVRV,
			DeltaPct:   computeVerificationDeltaPct(chainMVRV, cmMVRV),
			Status:     mvrvStatus,
			Note:       mvrvNote,
		},
	}
}

func medianFloat64(vals []float64) *float64 {
	if len(vals) == 0 {
		return nil
	}
	cp := slices.Clone(vals)
	slices.Sort(cp)
	mid := len(cp) / 2
	var m float64
	if len(cp)%2 == 1 {
		m = cp[mid]
	} else {
		m = (cp[mid-1] + cp[mid]) / 2.0
	}
	return &m
}

func (h *APIHandler) BitcoinOverview(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	s := h.btc
	ctx := r.Context()
	now := s.now().UTC()
	nowStr := now.Format(time.RFC3339)

	var (
		wg            sync.WaitGroup
		markets       BtcMarketsData
		valuation     BtcValuationData
		mining        BtcMiningData
		globalVal     any
		globalSrc     SourceStatus
		treasuriesVal any
		treasuriesSrc SourceStatus
		memRaw        *btcLiveRaw
		memErr        error
	)

	wg.Go(func() { markets = h.buildMarketsData(ctx) })
	wg.Go(func() { valuation = h.buildBitcoinValuation(ctx, "1y", 365) })
	wg.Go(func() { mining = h.buildBtcMiningData(ctx, r, "1y") })
	wg.Go(func() {
		globalVal, globalSrc = h.btcCachedSource(ctx, btcOverviewGlobalKey, btcOverviewGlobalTTL, "coingecko", func(c context.Context) (any, string) {
			return fetchCGGlobal(c)
		})
	})
	wg.Go(func() {
		treasuriesVal, treasuriesSrc = h.btcCachedSource(ctx, btcHoldersTreasuriesKey, btcTreasuriesTTL, "coingecko", func(c context.Context) (any, string) {
			return fetchCGTreasuries(c)
		})
	})
	if s.providerEnabled("mempool") {
		wg.Go(func() { memRaw, memErr = h.gasLiveBtc(r) })
	}
	wg.Wait()

	// Consolidation signal from live mempool fee & backlog.
	if memRaw != nil && memErr == nil {
		s.lastGood.Set("opendata:bitcoin:live:memraw", memRaw, nowStr)
	} else if prev, _, ok := s.lastGood.Get("opendata:bitcoin:live:memraw"); ok {
		memRaw, _ = prev.(*btcLiveRaw)
	}
	var economy, blocksToClear float64
	var tipHeight int64
	if memRaw != nil {
		economy = memRaw.Fees.Economy
		blocksToClear = float64(memRaw.Mempool.Vsize) / btcBlockVB
		if len(memRaw.Recent) > 0 {
			tipHeight = memRaw.Recent[0].Height
		}
	}
	if tipHeight == 0 && mining.Halving.NextHeight != nil && mining.Halving.BlocksRemaining != nil && *mining.Halving.NextHeight > *mining.Halving.BlocksRemaining {
		tipHeight = *mining.Halving.NextHeight - *mining.Halving.BlocksRemaining
	}
	consSignal := consolidationSignal(economy, blocksToClear)

	// Extract 7d exchange net flow & latest exchange balance from cached Coin Metrics rows.
	var (
		cmRows               []BtcCMMetricRow
		exchangeNetFlow7dUSD *float64
		exchangeNetFlow7dBTC *float64
		exchangeFlowFlash    bool
		exchangeBalanceBTC   *float64
		exchangeBalancePct   *float64
	)
	if v, _, ok := s.lastGood.Get(btcValuationCMKey); ok {
		cmRows, _ = v.([]BtcCMMetricRow)
	} else if v, _, ok := s.lastGood.Get(btcHoldersCMKey); ok {
		cmRows, _ = v.([]BtcCMMetricRow)
	}
	if len(cmRows) > 0 {
		flows := cmRowsToExchangeFlows(cmRows, 30, now)
		if len(flows) > 0 {
			last := flows[len(flows)-1]
			exchangeFlowFlash = last.Provisional
			if last.SupplyExBTC != nil {
				exchangeBalanceBTC = last.SupplyExBTC
			}
			if last.ExchangeSharePct != nil {
				exchangeBalancePct = last.ExchangeSharePct
			}
			start := max(0, len(flows)-7)
			var sumUSD float64
			var cntUSD int
			for i := start; i < len(flows); i++ {
				if flows[i].NetFlowUSD != nil {
					sumUSD += *flows[i].NetFlowUSD
					cntUSD++
				}
			}
			if cntUSD > 0 {
				exchangeNetFlow7dUSD = &sumUSD
			}
		}
	}

	// Dominance from CoinGecko /global (or fallback to treasuries response).
	var dominancePct *float64
	if g, ok := globalVal.(*BtcCGGlobal); ok && g != nil && g.BTCDominancePct > 0 {
		d := g.BTCDominancePct
		dominancePct = &d
	}
	var treasuriesBTC, treasuriesPct *float64
	var treasuriesCompanies int
	if tr, ok := treasuriesVal.(*BtcTreasuriesSummary); ok && tr != nil {
		if tr.TotalHoldingsBTC > 0 {
			tb := tr.TotalHoldingsBTC
			treasuriesBTC = &tb
			pct := tb / 21_000_000.0 * 100.0
			treasuriesPct = &pct
		}
		treasuriesCompanies = len(tr.Companies)
		if dominancePct == nil && tr.MarketCapDominancePct > 0 {
			d := tr.MarketCapDominancePct
			dominancePct = &d
		}
	}

	// Concentration card: pool row + node row, never merged (§5.2, §10.2).
	conc := BtcOverviewConcentration{
		PoolPeriod:   mining.Concentration.Period,
		PoolNakamoto: mining.Concentration.Nakamoto,
		PoolHHI:      mining.Concentration.HHI,
	}
	if mining.Nodes != nil {
		conc.NodeReachable = mining.Nodes.Reachable
		conc.NodeClearnet = mining.Nodes.Clearnet
		if mining.Nodes.Reachable > 0 {
			pct := float64(mining.Nodes.Clearnet) / float64(mining.Nodes.Reachable) * 100.0
			conc.NodeClearnetSharePct = &pct
		}
		cn := mining.Nodes.CountryNakamoto
		an := mining.Nodes.ASNNakamoto
		ah := mining.Nodes.ASNHHI
		cs := mining.Nodes.CloudShare
		conc.NodeCountryNakamoto = &cn
		conc.NodeASNNakamoto = &an
		conc.NodeASNHHI = &ah
		conc.NodeCloudShare = &cs
	}

	closeUSD := valuation.Latest.CloseUSD
	if closeUSD == nil {
		closeUSD = markets.PriceUSD
	}
	if closeUSD == nil {
		closeUSD = mining.PriceUSD
	}

	var hpUSD, hpBTC *float64
	if mining.HashpriceNow != nil {
		b := mining.HashpriceNow.BTCPerPHDay
		hpBTC = &b
		hpUSD = mining.HashpriceNow.USDPerPHDay
	}

	cards := BtcOverviewCards{
		CloseUSD:              closeUSD,
		MarketCapUSD:          markets.MarketCapUSD,
		DominancePct:          dominancePct,
		DrawdownPct:           valuation.Latest.DrawdownPct,
		ATHUSD:                valuation.Latest.ATHUSD,
		ATHDate:               valuation.Latest.ATHDate,
		RV30:                  markets.RV30,
		DVOL:                  markets.DVOLLatest,
		VRP:                   markets.VRP,
		FundingOIWeightedAPR:  markets.FundingOIWeightedAPR,
		TotalOIUSD:            markets.TotalOIUSD,
		MajorDerivSpot:        markets.Ratios.MajorDerivSpot,
		AllReportedDerivSpot:  markets.Ratios.AllReportedDerivSpot,
		AllReportedUnverified: markets.Ratios.AllReportedUnverified,
		Mayer:                 valuation.Latest.Mayer,
		MayerPctlSince2011:    valuation.Latest.MayerPctlSince2011,
		CMMVRV:                valuation.Latest.CMMVRV,
		CMMVRVPctlSince2011:   valuation.Latest.CMMVRVPctlSince2011,
		BigLensMVRV:           valuation.Latest.BigLensMVRV,
		MVRVDeltaPct:          valuation.Latest.MVRVDeltaPct,
		RealizedPriceBlocked:  valuation.Latest.RealizedPriceBlocked,
		HashpriceUSDPerPHDay:  hpUSD,
		HashpriceBTCPerPHDay:  hpBTC,
		ExchangeNetFlow7dUSD:  exchangeNetFlow7dUSD,
		ExchangeNetFlow7dBTC:  exchangeNetFlow7dBTC,
		ExchangeFlowFlash:     exchangeFlowFlash,
		Concentration:         conc,
	}

	// Compute 90d median of MajorDerivSpot if market history exists.
	var derivSpotMedian90d *float64
	if len(markets.History) > 0 {
		start := max(0, len(markets.History)-90)
		var vals []float64
		for i := start; i < len(markets.History); i++ {
			if markets.History[i].MajorDerivSpot != nil && *markets.History[i].MajorDerivSpot > 0 {
				vals = append(vals, *markets.History[i].MajorDerivSpot)
			}
		}
		derivSpotMedian90d = medianFloat64(vals)
	}

	signals := computeBtcRegimeSignals(btcRegimeInputs{
		CloseUSD:             closeUSD,
		SMA200:               valuation.Latest.SMA200,
		WMA200:               valuation.Latest.WMA200,
		CMMVRVPctlSince2011:  valuation.Latest.CMMVRVPctlSince2011,
		Ribbon:               mining.Ribbon,
		FundingOIWeightedAPR: markets.FundingOIWeightedAPR,
		MajorDerivSpot:       markets.Ratios.MajorDerivSpot,
		DerivSpotMedian90d:   derivSpotMedian90d,
		DVOL:                 markets.DVOLLatest,
		RV30:                 markets.RV30,
		Consolidation:        consSignal,
		ExchangeNetFlow7dUSD: exchangeNetFlow7dUSD,
	})

	// Build verification table by aligning on-chain rollup, holder day, and Coin Metrics to the same UTC date.
	var (
		ocRows          []BtcOnchainDay
		hRows           []BtcHolderDay
		latestOnchain   *BtcOnchainDay
		latestHolder    *BtcHolderDay
		baselineAvail   bool
		impliedHR       *float64
		mempoolHRForDay *float64
		mempoolRevBTC   *float64
		matchedCMRow    *BtcCMMetricRow
	)
	if s.read != nil {
		fromDay := now.AddDate(0, 0, -30).Format("2006-01-02")
		toDay := now.Format("2006-01-02")
		if rows, err := s.read.onchainDays(ctx, fromDay, toDay); err == nil && len(rows) > 0 {
			ocRows = rows
			last := ocRows[len(ocRows)-1]
			latestOnchain = &last
		}
		if bs, err := s.read.baselineStatus(ctx); err == nil {
			baselineAvail = bs.Available
		}
		if rows, err := s.read.holderDays(ctx, fromDay, toDay); err == nil && len(rows) > 0 {
			hRows = rows
		}
	}

	// Align latestOnchain and matchedCMRow to the newest common UTC date when both are present.
	if len(ocRows) > 0 && len(cmRows) > 0 {
		cmByDate := make(map[string]BtcCMMetricRow, len(cmRows))
		for _, r := range cmRows {
			cmByDate[r.Date] = r
		}
		for i := len(ocRows) - 1; i >= 0; i-- {
			if cm, ok := cmByDate[ocRows[i].Date]; ok {
				oc := ocRows[i]
				latestOnchain = &oc
				matchedCMRow = &cm
				break
			}
		}
	} else if len(cmRows) > 0 {
		row := cmRows[len(cmRows)-1]
		matchedCMRow = &row
	}

	if len(hRows) > 0 {
		if latestOnchain != nil {
			for i := len(hRows) - 1; i >= 0; i-- {
				if hRows[i].Date == latestOnchain.Date {
					h := hRows[i]
					latestHolder = &h
					break
				}
			}
		} else {
			last := hRows[len(hRows)-1]
			latestHolder = &last
		}
	}

	if len(mining.Chain) > 0 {
		var matchedChain *BtcMiningChainDay
		if latestOnchain != nil {
			for i := len(mining.Chain) - 1; i >= 0; i-- {
				if mining.Chain[i].Day == latestOnchain.Date {
					ch := mining.Chain[i]
					matchedChain = &ch
					break
				}
			}
		}
		if matchedChain == nil {
			ch := mining.Chain[len(mining.Chain)-1]
			matchedChain = &ch
		}
		ih := matchedChain.ImpliedHashrateEHs
		impliedHR = &ih
		mempoolHRForDay = matchedChain.MempoolHashrateEHs
	}
	if mempoolHRForDay == nil {
		mempoolHRForDay = mining.CurrentHashrateEH
	}
	if mining.Rewards144 != nil && mining.Rewards144.TotalRewardSats > 0 {
		rBTC := float64(mining.Rewards144.TotalRewardSats) / 1e8
		mempoolRevBTC = &rBTC
	}

	// Supply summary (§5.2). When tipHeight is unknown, height-dependent fields are nil.
	supply := BtcOverviewSupply{
		MaxSupplyBTC:        21_000_000,
		ExchangeBalanceBTC:  exchangeBalanceBTC,
		ExchangeBalancePct:  exchangeBalancePct,
		TreasuriesBTC:       treasuriesBTC,
		TreasuriesPct:       treasuriesPct,
		TreasuriesCompanies: treasuriesCompanies,
	}
	if tipHeight > 0 {
		th := tipHeight
		schedSats := btcScheduledSupply(tipHeight)
		schedBTC := float64(schedSats) / 1e8
		minedPct := schedBTC / 21_000_000.0 * 100.0
		subBTC := float64(btcSubsidySats(tipHeight)) / 1e8
		annBTC := subBTC * 144.0 * 365.0
		var annInfPct float64
		if schedBTC > 0 {
			annInfPct = annBTC / schedBTC * 100.0
		}
		supply.TipHeight = &th
		supply.ScheduledSupplyBTC = &schedBTC
		supply.MinedPct = &minedPct
		supply.SubsidyBTC = &subBTC
		supply.AnnualIssuanceBTC = &annBTC
		supply.AnnualInflationPct = &annInfPct
	}

	fngHist := valuation.FearGreed
	if len(fngHist) > 90 {
		fngHist = fngHist[len(fngHist)-90:]
	}
	sentiment := BtcOverviewSentiment{
		FearGreedValue:   valuation.Latest.FearGreedValue,
		FearGreedClass:   valuation.Latest.FearGreedClass,
		FearGreedHistory: fngHist,
	}

	verifDate := ""
	if latestOnchain != nil {
		verifDate = latestOnchain.Date
	} else if matchedCMRow != nil {
		verifDate = matchedCMRow.Date
	}

	verification := computeBtcVerificationRows(btcVerificationInputs{
		Date:               verifDate,
		MempoolHashrateEHs: mempoolHRForDay,
		ImpliedHashrateEHs: impliedHR,
		CMRow:              matchedCMRow,
		MempoolRewardsBTC:  mempoolRevBTC,
		OnchainDay:         latestOnchain,
		HolderDay:          latestHolder,
		BaselineAvailable:  baselineAvail,
		RealizedBlocked:    valuation.Latest.RealizedPriceBlocked,
	})

	// Deduplicate source badges in canonical order.
	seenSrc := make(map[string]bool)
	var sources []SourceStatus
	addSrc := func(list ...SourceStatus) {
		for _, st := range list {
			if st.ID == "" {
				continue
			}
			key := st.ID + ":" + st.Via
			if seenSrc[key] {
				continue
			}
			seenSrc[key] = true
			sources = append(sources, st)
		}
	}
	addSrc(valuation.Sources...)
	addSrc(mining.Sources...)
	addSrc(markets.Sources...)
	addSrc(globalSrc, treasuriesSrc)

	writeJSON(w, BtcOverviewData{
		AsOf:         nowStr,
		Cards:        cards,
		PriceChart:   valuation.Series,
		Signals:      signals,
		Supply:       supply,
		Sentiment:    sentiment,
		Verification: verification,
		Sources:      sources,
	})
}
