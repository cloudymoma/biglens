package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var btcCFTCTFFBaseURL = "https://publicreporting.cftc.gov/resource/gpe5-46if.json"

var cftcUnitsBTCRe = regexp.MustCompile(`(?:^|[^0-9./])([0-9]+(?:\.[0-9]+)?)\s+BITCOINS?\b`)

// btcCFTCContractSizes maps CFTC market names and contract codes to BTC per contract (§6.2, Task 4.2).
var btcCFTCContractSizes = map[string]float64{
	"BITCOIN - CHICAGO MERCANTILE EXCHANGE":               5.0,
	"MICRO BITCOIN - CHICAGO MERCANTILE EXCHANGE":         0.1,
	"NANO BITCOIN - COINBASE DERIVATIVES, LLC":            0.01,
	"NANO BITCOIN PERP STYLE - COINBASE DERIVATIVES, LLC": 0.01,
	"BITCOIN NANO - COINBASE DERIVATIVES, LLC":            0.01,
	"BITCOIN - COINBASE DERIVATIVES, LLC":                 1.0,
	"133741":                                              5.0,
	"133742":                                              0.1,
	"133001":                                              0.01,
	"133LM1":                                              0.01,
	"133LM4":                                              0.01,
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
	normName := strings.Join(strings.Fields(strings.ToUpper(name)), " ")
	if strings.Contains(normName, "BITCOIN CASH") ||
		strings.Contains(normName, "BITCOIN SV") ||
		strings.Contains(normName, "BITCOIN GOLD") ||
		strings.Contains(normName, "BITCOIN DIAMOND") {
		return 0, false
	}
	upperUnits := strings.ToUpper(units)
	if m := cftcUnitsBTCRe.FindStringSubmatch(upperUnits); len(m) == 2 {
		if sz, ok := parseBtcFloat(m[1]); ok && sz > 0 {
			return sz, true
		}
	}
	trimmedCode := strings.TrimSpace(code)
	if sz, ok := btcCFTCContractSizes[trimmedCode]; ok {
		return sz, true
	}
	if sz, ok := btcCFTCContractSizes[normName]; ok {
		return sz, true
	}
	switch {
	case strings.Contains(normName, "NANO BITCOIN"), strings.Contains(normName, "BITCOIN NANO"):
		return 0.01, true
	case strings.Contains(normName, "MICRO BITCOIN"):
		return 0.1, true
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
		q.Set("$where", "upper(market_and_exchange_names) like '%BITCOIN%' AND upper(market_and_exchange_names) not like '%BITCOIN CASH%'")
		q.Set("$order", "report_date_as_yyyy_mm_dd DESC")
		q.Set("$limit", fmt.Sprintf("%d", weeks*12))
		u = u + "?" + q.Encode()
	}

	var rows []cftcRawTFFRow
	if code := btcGetJSON(ctx, u, btcDefaultMaxBody, &rows); code != "" {
		return nil, code
	}
	if len(rows) == 0 {
		return nil, "empty_response"
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
		upperMarket := strings.ToUpper(pt.MarketName)
		key := pt.ContractCode + "|" + upperMarket
		if prev, exists := latestByMarket[key]; !exists || pt.ReportDate > prev.ReportDate {
			latestByMarket[key] = pt
		}
		if pt.ContractCode == "133741" || (upperMarket == "BITCOIN - CHICAGO MERCANTILE EXCHANGE" && sz == 5.0) {
			cmeHistory = append(cmeHistory, pt)
		}
	}
	if asOfDate == "" {
		return nil, "empty_response"
	}

	sort.Slice(cmeHistory, func(i, j int) bool {
		return cmeHistory[i].ReportDate < cmeHistory[j].ReportDate
	})
	if len(cmeHistory) > weeks {
		cmeHistory = cmeHistory[len(cmeHistory)-weeks:]
	}

	var latestList []BtcCFTCWeekPoint
	var cmeOIBTC, microOIBTC, cdeOIBTC, totalRegBTC float64
	for _, pt := range latestByMarket {
		if pt.ReportDate != asOfDate {
			continue
		}
		latestList = append(latestList, pt)
		totalRegBTC += pt.OpenInterestBTC
		upperMarket := strings.ToUpper(pt.MarketName)
		switch {
		case strings.Contains(upperMarket, "COINBASE") || strings.Contains(upperMarket, "LMX LABS") || pt.ContractCode == "133001" || pt.ContractCode == "133LM1" || pt.ContractCode == "133LM4":
			cdeOIBTC += pt.OpenInterestBTC
		case pt.ContractCode == "133741" || (strings.Contains(upperMarket, "CHICAGO MERCANTILE EXCHANGE") && pt.ContractSizeBTC == 5.0):
			cmeOIBTC += pt.OpenInterestBTC
		case strings.Contains(upperMarket, "CHICAGO MERCANTILE EXCHANGE") || pt.ContractCode == "133742":
			microOIBTC += pt.OpenInterestBTC
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
		TotalCMERegBTC: totalRegBTC,
		CMEHistory:     cmeHistory,
		LatestByMarket: latestList,
	}, ""
}
