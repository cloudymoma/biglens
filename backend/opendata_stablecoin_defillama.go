package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefiLlama stablecoins client (stablecoin.md §4.1, §5.1).
//
// Units: on /stablecoins every list value (circulating, circulatingPrev*,
// chainCirculating.*) is the native amount times DefiLlama's own price field,
// i.e. a USD value, under the asset's pegType key. M0 showed this for
// peggedEUR (EURC); the fixtures show the same for peggedUSD: USDT
// circulatingPrevDay ÷ price equals the charts' native total for that day to
// the unit. The parser therefore normalizes every peg type at parse time:
// _native = value ÷ same-response price (null when the price is missing or
// zero) and _usd = value. Chart totalCirculating is already native and
// totalCirculatingUSD is USD; both are used as-is.

// llamaStableSnapshot is one parsed /stablecoins response.
type llamaStableSnapshot struct {
	FetchedAt time.Time
	Assets    []llamaAsset
}

type llamaAsset struct {
	LlamaID      string
	Symbol       string
	Name         string
	GeckoID      string
	PegType      string
	PegMechanism string
	PriceUSD     *float64

	CirculatingNative *float64
	PrevDayNative     *float64
	PrevWeekNative    *float64
	PrevMonthNative   *float64
	CirculatingUSD    *float64
	PrevDayUSD        *float64
	PrevWeekUSD       *float64
	PrevMonthUSD      *float64

	Chains []llamaChain // sorted by current supply, largest first
}

type llamaChain struct {
	Chain           string // DefiLlama chain name
	CurrentNative   *float64
	PrevDayNative   *float64
	PrevWeekNative  *float64
	PrevMonthNative *float64
	CurrentUSD      *float64
}

func (s llamaStableSnapshot) byID(id string) (llamaAsset, bool) {
	for _, a := range s.Assets {
		if a.LlamaID == id {
			return a, true
		}
	}
	return llamaAsset{}, false
}

// core returns a core asset (usdt, usdc, eurc) by its dashboard id.
func (s llamaStableSnapshot) core(asset string) (llamaAsset, bool) {
	def, ok := stableAssetByID(asset)
	if !ok {
		return llamaAsset{}, false
	}
	return s.byID(def.LlamaID)
}

// llamaNativeFromUSD converts a DefiLlama list value (USD) into native units
// with the same-response price; unknown inputs give null, never a guess.
func llamaNativeFromUSD(valueUSD, priceUSD *float64) *float64 {
	if valueUSD == nil || priceUSD == nil || !(*priceUSD > 0) {
		return nil
	}
	v := *valueUSD / *priceUSD
	return &v
}

type llamaRawAmounts map[string]*float64

func (m llamaRawAmounts) get(key string) *float64 {
	if m == nil {
		return nil
	}
	return m[key]
}

type llamaRawAsset struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Symbol           string          `json:"symbol"`
	GeckoID          *string         `json:"gecko_id"`
	PegType          string          `json:"pegType"`
	PegMechanism     string          `json:"pegMechanism"`
	Price            json.RawMessage `json:"price"`
	Circulating      llamaRawAmounts `json:"circulating"`
	PrevDay          llamaRawAmounts `json:"circulatingPrevDay"`
	PrevWeek         llamaRawAmounts `json:"circulatingPrevWeek"`
	PrevMonth        llamaRawAmounts `json:"circulatingPrevMonth"`
	ChainCirculating map[string]struct {
		Current   llamaRawAmounts `json:"current"`
		PrevDay   llamaRawAmounts `json:"circulatingPrevDay"`
		PrevWeek  llamaRawAmounts `json:"circulatingPrevWeek"`
		PrevMonth llamaRawAmounts `json:"circulatingPrevMonth"`
	} `json:"chainCirculating"`
}

// llamaPrice accepts a positive JSON number; null (DefiLlama sends it for dead
// assets), zero, strings and anything else are unknown.
func llamaPrice(raw json.RawMessage) *float64 {
	var p *float64
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil || p == nil || !(*p > 0) {
		return nil
	}
	return p
}

// parseLlamaStablecoins parses /stablecoins?includePrices=true. The response
// is rejected as a whole when a core id no longer maps to its symbol and peg
// type (§8.7), because nothing else in it can then be trusted.
func parseLlamaStablecoins(body []byte, fetchedAt time.Time) (llamaStableSnapshot, error) {
	var raw struct {
		PeggedAssets []llamaRawAsset `json:"peggedAssets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return llamaStableSnapshot{}, fmt.Errorf("decode: %w", err)
	}
	if len(raw.PeggedAssets) == 0 {
		return llamaStableSnapshot{}, errors.New("no peggedAssets")
	}
	out := llamaStableSnapshot{FetchedAt: fetchedAt, Assets: make([]llamaAsset, 0, len(raw.PeggedAssets))}
	for _, r := range raw.PeggedAssets {
		if r.ID == "" || r.PegType == "" {
			continue
		}
		price := llamaPrice(r.Price)
		key := r.PegType
		a := llamaAsset{
			LlamaID: r.ID, Symbol: r.Symbol, Name: r.Name, PegType: r.PegType, PegMechanism: r.PegMechanism,
			PriceUSD:       price,
			CirculatingUSD: r.Circulating.get(key), PrevDayUSD: r.PrevDay.get(key),
			PrevWeekUSD: r.PrevWeek.get(key), PrevMonthUSD: r.PrevMonth.get(key),
		}
		if r.GeckoID != nil {
			a.GeckoID = *r.GeckoID
		}
		a.CirculatingNative = llamaNativeFromUSD(a.CirculatingUSD, price)
		a.PrevDayNative = llamaNativeFromUSD(a.PrevDayUSD, price)
		a.PrevWeekNative = llamaNativeFromUSD(a.PrevWeekUSD, price)
		a.PrevMonthNative = llamaNativeFromUSD(a.PrevMonthUSD, price)
		for name, c := range r.ChainCirculating {
			cur := c.Current.get(key)
			a.Chains = append(a.Chains, llamaChain{
				Chain:           name,
				CurrentUSD:      cur,
				CurrentNative:   llamaNativeFromUSD(cur, price),
				PrevDayNative:   llamaNativeFromUSD(c.PrevDay.get(key), price),
				PrevWeekNative:  llamaNativeFromUSD(c.PrevWeek.get(key), price),
				PrevMonthNative: llamaNativeFromUSD(c.PrevMonth.get(key), price),
			})
		}
		sort.Slice(a.Chains, func(i, j int) bool {
			vi, vj := a.Chains[i].CurrentUSD, a.Chains[j].CurrentUSD
			switch {
			case vi == nil || vj == nil:
				if (vi == nil) != (vj == nil) {
					return vj == nil
				}
				return a.Chains[i].Chain < a.Chains[j].Chain
			case *vi != *vj:
				return *vi > *vj
			default:
				return a.Chains[i].Chain < a.Chains[j].Chain
			}
		})
		out.Assets = append(out.Assets, a)
	}
	for _, def := range stableCoreAssets {
		a, ok := out.byID(def.LlamaID)
		if !ok {
			return llamaStableSnapshot{}, fmt.Errorf("core id %s (%s) missing", def.LlamaID, def.Symbol)
		}
		if a.Symbol != def.Symbol || a.PegType != def.LlamaPeg {
			return llamaStableSnapshot{}, fmt.Errorf("core id %s maps to %s/%s, want %s/%s", def.LlamaID, a.Symbol, a.PegType, def.Symbol, def.LlamaPeg)
		}
	}
	return out, nil
}

// llamaChartRow is one day of /stablecoincharts/all for one asset.
type llamaChartRow struct {
	Day               string // YYYY-MM-DD, UTC
	CirculatingNative *float64
	CirculatingUSD    *float64
	UnreleasedNative  *float64
}

// llamaChartDate accepts a unix-seconds date as a JSON string or number.
func llamaChartDate(raw json.RawMessage) (string, error) {
	s := strings.Trim(string(raw), `"`)
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || sec <= 0 {
		return "", fmt.Errorf("bad date %s", raw)
	}
	return time.Unix(sec, 0).UTC().Format(time.DateOnly), nil
}

// parseLlamaChart parses /stablecoincharts/all?stablecoin={id}; pegType is
// the asset's key in every amount object. Rows are returned oldest first.
func parseLlamaChart(body []byte, pegType string) ([]llamaChartRow, error) {
	var raw []struct {
		Date                json.RawMessage `json:"date"`
		TotalCirculating    llamaRawAmounts `json:"totalCirculating"`
		TotalUnreleased     llamaRawAmounts `json:"totalUnreleased"`
		TotalCirculatingUSD llamaRawAmounts `json:"totalCirculatingUSD"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("empty chart")
	}
	byDay := make(map[string]llamaChartRow, len(raw))
	withPeg := 0
	for _, r := range raw {
		day, err := llamaChartDate(r.Date)
		if err != nil {
			return nil, err
		}
		row := llamaChartRow{Day: day, CirculatingNative: r.TotalCirculating.get(pegType),
			CirculatingUSD: r.TotalCirculatingUSD.get(pegType), UnreleasedNative: r.TotalUnreleased.get(pegType)}
		if row.CirculatingNative != nil {
			withPeg++
		}
		byDay[day] = row
	}
	if withPeg == 0 {
		return nil, fmt.Errorf("no %s values in chart", pegType)
	}
	out := make([]llamaChartRow, 0, len(byDay))
	for _, r := range byDay {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

func fetchLlamaStablecoins(ctx context.Context, ep stableEndpoints, now time.Time) (llamaStableSnapshot, string) {
	body, code := stableFetch(ctx, ep.LlamaBase+"/stablecoins?includePrices=true", stableTimeoutLarge, stableMaxBodyLarge)
	if code != "" {
		return llamaStableSnapshot{}, code
	}
	snap, err := parseLlamaStablecoins(body, now)
	if err != nil {
		return llamaStableSnapshot{}, "bad_response"
	}
	return snap, ""
}

func fetchLlamaChart(ctx context.Context, ep stableEndpoints, llamaID, pegType string) ([]llamaChartRow, string) {
	body, code := stableFetch(ctx, ep.LlamaBase+"/stablecoincharts/all?stablecoin="+url.QueryEscape(llamaID), stableTimeoutLarge, stableMaxBodyLarge)
	if code != "" {
		return nil, code
	}
	rows, err := parseLlamaChart(body, pegType)
	if err != nil {
		return nil, "bad_response"
	}
	return rows, ""
}
