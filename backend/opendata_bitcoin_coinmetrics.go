package main

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var btcCoinMetricsBaseURL = "https://community-api.coinmetrics.io/v4/timeseries/asset-metrics"

const (
	btcCoinMetricsDefaultMetrics = "CapMVRVCur,SplyCur,FlowInExUSD,FlowOutExUSD,SplyExNtv,IssTotNtv,FeeTotNtv,AdrActCnt,TxCnt,PriceUSD"
	btcCoinMetricsMaxPages       = 8
)

// BtcCMMetricRow holds one daily row of Coin Metrics Community BTC metrics.
type BtcCMMetricRow struct {
	Date             string   `json:"date"`
	CapMVRVCur       *float64 `json:"cap_mvrv_cur,omitempty"`
	SplyCur          *float64 `json:"sply_cur,omitempty"`
	FlowInExUSD      *float64 `json:"flow_in_ex_usd,omitempty"`
	FlowOutExUSD     *float64 `json:"flow_out_ex_usd,omitempty"`
	NetFlowExUSD     *float64 `json:"net_flow_ex_usd,omitempty"`
	SplyExNtv        *float64 `json:"sply_ex_ntv,omitempty"`
	ExchangeSharePct *float64 `json:"exchange_share_pct,omitempty"`
	IssTotNtv        *float64 `json:"iss_tot_ntv,omitempty"`
	FeeTotNtv        *float64 `json:"fee_tot_ntv,omitempty"`
	AdrActCnt        *int64   `json:"adr_act_cnt,omitempty"`
	TxCnt            *int64   `json:"tx_cnt,omitempty"`
	PriceUSD         *float64 `json:"price_usd,omitempty"`
	Provisional      bool     `json:"provisional,omitempty"`
}

// BtcExchangeFlowPoint is the holder-tab projection of Coin Metrics exchange flow and balance metrics.
type BtcExchangeFlowPoint struct {
	Date             string   `json:"date"`
	InflowUSD        *float64 `json:"inflow_usd"`
	OutflowUSD       *float64 `json:"outflow_usd"`
	NetFlowUSD       *float64 `json:"net_flow_usd"`
	SupplyExBTC      *float64 `json:"supply_ex_btc"`
	SupplyCurBTC     *float64 `json:"supply_cur_btc"`
	ExchangeSharePct *float64 `json:"exchange_share_pct"`
	Provisional      bool     `json:"provisional,omitempty"`
}

type cmRawAssetRow struct {
	Asset         string `json:"asset"`
	Time          string `json:"time"`
	AdrActCnt     string `json:"AdrActCnt"`
	CapMVRVCur    string `json:"CapMVRVCur"`
	FeeTotNtv     string `json:"FeeTotNtv"`
	FlowInExUSD   string `json:"FlowInExUSD"`
	FlowInStatus  string `json:"FlowInExUSD-status"`
	FlowOutExUSD  string `json:"FlowOutExUSD"`
	FlowOutStatus string `json:"FlowOutExUSD-status"`
	IssTotNtv     string `json:"IssTotNtv"`
	PriceUSD      string `json:"PriceUSD"`
	SplyCur       string `json:"SplyCur"`
	SplyExNtv     string `json:"SplyExNtv"`
	SplyExStatus  string `json:"SplyExNtv-status"`
	TxCnt         string `json:"TxCnt"`
}

type cmRawAssetResponse struct {
	Data          []cmRawAssetRow `json:"data"`
	NextPageToken string          `json:"next_page_token"`
	NextPageURL   string          `json:"next_page_url"`
}

func parseCMFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseCMInt(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return &v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		iv := int64(f)
		return &iv
	}
	return nil
}

// fetchCMAssetMetrics fetches Coin Metrics Community daily BTC metrics starting at startDate (YYYY-MM-DD).
// It follows next_page_url pagination up to btcCoinMetricsMaxPages and marks "flash" status rows as provisional.
func fetchCMAssetMetrics(ctx context.Context, startDate string) ([]BtcCMMetricRow, string) {
	u, err := url.Parse(btcCoinMetricsBaseURL)
	if err != nil {
		return nil, "bad_response"
	}
	q := u.Query()
	q.Set("assets", "btc")
	q.Set("metrics", btcCoinMetricsDefaultMetrics)
	q.Set("frequency", "1d")
	q.Set("page_size", "1000")
	if startDate != "" {
		q.Set("start_time", startDate)
	}
	u.RawQuery = q.Encode()

	baseHost := strings.ToLower(u.Host)
	nextURL := u.String()
	byDate := make(map[string]BtcCMMetricRow)

	for page := 0; page < btcCoinMetricsMaxPages && nextURL != ""; page++ {
		var resp cmRawAssetResponse
		if code := btcGetJSON(ctx, nextURL, btcDefaultMaxBody, &resp); code != "" {
			if page == 0 {
				return nil, code
			}
			break
		}
		for _, r := range resp.Data {
			if len(r.Time) < 10 {
				continue
			}
			day := r.Time[:10]
			row := BtcCMMetricRow{
				Date:         day,
				CapMVRVCur:   parseCMFloat(r.CapMVRVCur),
				SplyCur:      parseCMFloat(r.SplyCur),
				FlowInExUSD:  parseCMFloat(r.FlowInExUSD),
				FlowOutExUSD: parseCMFloat(r.FlowOutExUSD),
				SplyExNtv:    parseCMFloat(r.SplyExNtv),
				IssTotNtv:    parseCMFloat(r.IssTotNtv),
				FeeTotNtv:    parseCMFloat(r.FeeTotNtv),
				AdrActCnt:    parseCMInt(r.AdrActCnt),
				TxCnt:        parseCMInt(r.TxCnt),
				PriceUSD:     parseCMFloat(r.PriceUSD),
				Provisional: strings.EqualFold(r.FlowInStatus, "flash") ||
					strings.EqualFold(r.FlowOutStatus, "flash") ||
					strings.EqualFold(r.SplyExStatus, "flash"),
			}
			if row.FlowInExUSD != nil && row.FlowOutExUSD != nil {
				net := *row.FlowInExUSD - *row.FlowOutExUSD
				row.NetFlowExUSD = &net
			}
			if row.SplyExNtv != nil && row.SplyCur != nil && *row.SplyCur > 0 {
				share := *row.SplyExNtv / *row.SplyCur * 100
				row.ExchangeSharePct = &share
			}
			byDate[day] = row
		}
		if resp.NextPageURL == "" {
			break
		}
		nu, err := url.Parse(resp.NextPageURL)
		if err != nil || strings.ToLower(nu.Host) != baseHost {
			break
		}
		nextURL = nu.String()
	}

	out := make([]BtcCMMetricRow, 0, len(byDate))
	for _, r := range byDate {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, ""
}

func cmRowsToExchangeFlows(rows []BtcCMMetricRow, days int, now time.Time) []BtcExchangeFlowPoint {
	cutoff := ""
	if days > 0 {
		cutoff = now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
	}
	out := make([]BtcExchangeFlowPoint, 0, len(rows))
	for _, r := range rows {
		if cutoff != "" && r.Date < cutoff {
			continue
		}
		if r.FlowInExUSD == nil && r.FlowOutExUSD == nil && r.SplyExNtv == nil {
			continue
		}
		out = append(out, BtcExchangeFlowPoint{
			Date:             r.Date,
			InflowUSD:        r.FlowInExUSD,
			OutflowUSD:       r.FlowOutExUSD,
			NetFlowUSD:       r.NetFlowExUSD,
			SupplyExBTC:      r.SplyExNtv,
			SupplyCurBTC:     r.SplyCur,
			ExchangeSharePct: r.ExchangeSharePct,
			Provisional:      r.Provisional,
		})
	}
	if days > 0 && len(out) > days {
		out = out[len(out)-days:]
	}
	return out
}
