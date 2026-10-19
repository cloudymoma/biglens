package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

var btcCFTCTFFBaseURL = "https://publicreporting.cftc.gov/resource/gpe5-46if.json"

// btcCFTCContractSizes maps CFTC market names and contract codes to BTC per contract (§6.2, Task 4.2).
var btcCFTCContractSizes = map[string]float64{
	"BITCOIN - CHICAGO MERCANTILE EXCHANGE":       5.0,
	"MICRO BITCOIN - CHICAGO MERCANTILE EXCHANGE": 0.1,
	"BITCOIN NANO - COINBASE DERIVATIVES, LLC":    0.01,
	"BITCOIN - COINBASE DERIVATIVES, LLC":         0.01,
	"133741":                                      5.0,
	"133742":                                      0.1,
	"133001":                                      0.01,
}

// BtcCFTCWeekPoint represents one weekly CFTC Traders in Financial Futures report row converted to BTC.
type BtcCFTCWeekPoint struct {
	ReportDate             string  `json:"report_date"`
	MarketName             string  `json:"market_name"`
	ContractCode           string  `json:"contract_code"`
	ContractSizeBTC        float64 `json:"contract_size_btc"`
	OpenInterestContracts  int64   `json:"open_interest_contracts"`
	OpenInterestBTC        float64 `json:"open_interest_btc"`
	AssetManagersNetBTC    float64 `json:"asset_managers_net_btc"`
	LeveragedFundsNetBTC   float64 `json:"leveraged_funds_net_btc"`
	DealersNetBTC          float64 `json:"dealers_net_btc"`
	OtherReportablesNetBTC float64 `json:"other_reportables_net_btc"`
}

// BtcCFTCSummary aggregates CME and US-regulated Bitcoin positioning from CFTC TFF.
type BtcCFTCSummary struct {
	AsOfReportDate string             `json:"as_of_report_date"`
	CMEOIBTC       float64            `json:"cme_oi_btc"`
	CMEMicroOIBTC  float64            `json:"cme_micro_oi_btc"`
	CDEOIBTC       float64            `json:"cde_oi_btc"`
	TotalCMERegBTC float64            `json:"total_cme_reg_btc"`
	CMEHistory     []BtcCFTCWeekPoint `json:"cme_history"`
	LatestByMarket []BtcCFTCWeekPoint `json:"latest_by_market"`
}

type cftcRawTFFRow struct {
	MarketAndExchangeNames string `json:"market_and_exchange_names"`
	ContractMarketCode     string `json:"cftc_contract_market_code"`
	ReportDate             string `json:"report_date_as_yyyy_mm_dd"`
	ContractUnits          string `json:"contract_units"`
	OpenInterestAll        string `json:"open_interest_all"`
	DealerLong             string `json:"dealer_positions_long_all"`
	DealerShort            string `json:"dealer_positions_short_all"`
	AssetMgrLong           string `json:"asset_mgr_positions_long_all"`
	AssetMgrShort          string `json:"asset_mgr_positions_short_all"`
	LevMoneyLong           string `json:"lev_money_positions_long_all"`
	LevMoneyShort          string `json:"lev_money_positions_short_all"`
	OtherReptLong          string `json:"other_rept_positions_long_all"`
	OtherReptShort         string `json:"other_rept_positions_short_all"`
}

func resolveCFTCContractSizeBTC(name, code, units string) (float64, bool) {
	upperName := strings.ToUpper(strings.TrimSpace(name))
	if sz, ok := btcCFTCContractSizes[upperName]; ok {
		return sz, true
	}
	trimmedCode := strings.TrimSpace(code)
	if sz, ok := btcCFTCContractSizes[trimmedCode]; ok {
		return sz, true
	}
	upperUnits := strings.ToUpper(units)
	switch {
	case strings.Contains(upperUnits, "5 BITCOIN"):
		return 5.0, true
	case strings.Contains(upperUnits, "0.10 BITCOIN"), strings.Contains(upperUnits, "0.1 BITCOIN"):
		return 0.1, true
	case strings.Contains(upperUnits, "0.01 BITCOIN"):
		return 0.01, true
	case strings.Contains(upperName, "NANO BITCOIN"), strings.Contains(upperName, "BITCOIN NANO"), strings.Contains(upperName, "COINBASE"):
		return 0.01, true
	default:
		return 0, false
	}
}

// fetchCFTCBitcoin fetches the last weeks of CFTC TFF rows for Bitcoin futures and converts all figures to BTC.
func fetchCFTCBitcoin(ctx context.Context, weeks int) (*BtcCFTCSummary, string) {
	if weeks <= 0 {
		weeks = 52
	}
	u := btcCFTCTFFBaseURL
	if !strings.Contains(u, "?") && strings.HasPrefix(u, "https://publicreporting.cftc.gov") {
		q := url.Values{}
		q.Set("$where", "upper(market_and_exchange_names) like '%BITCOIN%'")
		q.Set("$order", "report_date_as_yyyy_mm_dd DESC")
		q.Set("$limit", fmt.Sprintf("%d", weeks*12))
		u = u + "?" + q.Encode()
	}

	var rows []cftcRawTFFRow
	if code := btcGetJSON(ctx, u, btcDefaultMaxBody, &rows); code != "" {
		return nil, code
	}

	latestByMarket := make(map[string]BtcCFTCWeekPoint)
	var cmeHistory []BtcCFTCWeekPoint
	var asOfDate string

	for _, r := range rows {
		sz, ok := resolveCFTCContractSizeBTC(r.MarketAndExchangeNames, r.ContractMarketCode, r.ContractUnits)
		if !ok || sz <= 0 {
			continue
		}
		date := strings.TrimSpace(r.ReportDate)
		if len(date) >= 10 {
			date = date[:10]
		}
		if date == "" {
			continue
		}
		oiContractsF, okOI := parseBtcFloat(r.OpenInterestAll)
		if !okOI {
			continue
		}
		dLong, _ := parseBtcFloat(r.DealerLong)
		dShort, _ := parseBtcFloat(r.DealerShort)
		amLong, _ := parseBtcFloat(r.AssetMgrLong)
		amShort, _ := parseBtcFloat(r.AssetMgrShort)
		lfLong, _ := parseBtcFloat(r.LevMoneyLong)
		lfShort, _ := parseBtcFloat(r.LevMoneyShort)
		orLong, _ := parseBtcFloat(r.OtherReptLong)
		orShort, _ := parseBtcFloat(r.OtherReptShort)

		pt := BtcCFTCWeekPoint{
			ReportDate:             date,
			MarketName:             strings.TrimSpace(r.MarketAndExchangeNames),
			ContractCode:           strings.TrimSpace(r.ContractMarketCode),
			ContractSizeBTC:        sz,
			OpenInterestContracts:  int64(oiContractsF),
			OpenInterestBTC:        oiContractsF * sz,
			AssetManagersNetBTC:    (amLong - amShort) * sz,
			LeveragedFundsNetBTC:   (lfLong - lfShort) * sz,
			DealersNetBTC:          (dLong - dShort) * sz,
			OtherReportablesNetBTC: (orLong - orShort) * sz,
		}

		if date > asOfDate {
			asOfDate = date
		}
		key := strings.ToUpper(pt.MarketName)
		if prev, exists := latestByMarket[key]; !exists || pt.ReportDate > prev.ReportDate {
			latestByMarket[key] = pt
		}
		if sz == 5.0 {
			cmeHistory = append(cmeHistory, pt)
		}
	}

	sort.Slice(cmeHistory, func(i, j int) bool {
		return cmeHistory[i].ReportDate < cmeHistory[j].ReportDate
	})
	if len(cmeHistory) > weeks {
		cmeHistory = cmeHistory[len(cmeHistory)-weeks:]
	}

	var latestList []BtcCFTCWeekPoint
	var cmeOIBTC, microOIBTC, cdeOIBTC float64
	for _, pt := range latestByMarket {
		latestList = append(latestList, pt)
		switch pt.ContractSizeBTC {
		case 5.0:
			cmeOIBTC += pt.OpenInterestBTC
		case 0.1:
			microOIBTC += pt.OpenInterestBTC
		case 0.01:
			cdeOIBTC += pt.OpenInterestBTC
		}
	}
	sort.Slice(latestList, func(i, j int) bool {
		return latestList[i].OpenInterestBTC > latestList[j].OpenInterestBTC
	})

	return &BtcCFTCSummary{
		AsOfReportDate: asOfDate,
		CMEOIBTC:       cmeOIBTC,
		CMEMicroOIBTC:  microOIBTC,
		CDEOIBTC:       cdeOIBTC,
		TotalCMERegBTC: cmeOIBTC + microOIBTC,
		CMEHistory:     cmeHistory,
		LatestByMarket: latestList,
	}, ""
}
