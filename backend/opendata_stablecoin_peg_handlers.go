package main

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// /peg (stablecoin.md §9): the venue table, the consensus and dispersion
// series, the relative peg (exchange median and Uniswap), FX, and the depeg
// history with annotations. Served from the peg sampler's memory and SQLite;
// it never calls upstreams. Chainlink (oracle) and Curve are v1.1.

const (
	stablePegTTL = 30 * time.Second
	// stablePegStaleAfter: a sample older than 3 peg intervals is served
	// marked stale (§8.7), never refreshed by a request.
	stablePegStaleAfter = 3 * stablePegTick
	// stableDepegThresholdBps marks a day as off peg in the history.
	stableDepegThresholdBps = 30.0
	// stableDepegDaysMax caps the history list per asset (newest first).
	stableDepegDaysMax = 500
)

// stablePegRanges whitelists /peg ?range=; up to 30 days the series is the
// 5-minute samples, beyond that the daily rollup.
var stablePegRanges = map[string]struct {
	Span  time.Duration // 0: all
	Daily bool
}{
	"24h": {24 * time.Hour, false}, "7d": {7 * 24 * time.Hour, false}, "30d": {30 * 24 * time.Hour, false},
	"90d": {90 * 24 * time.Hour, true}, "1y": {365 * 24 * time.Hour, true}, "all": {0, true},
}

const stablePegDefaultRange = "7d"

type StablePegPoint struct {
	TS         string   `json:"ts"` // 5m: bucket start; 1d: the day
	State      string   `json:"state,omitempty"`
	Consensus  *float64 `json:"consensus"` // 1d: the day's close
	DevBps     *float64 `json:"dev_bps"`   // 1d: the close's deviation
	DispBps    *float64 `json:"disp_bps,omitempty"`
	VenuesUsed *int     `json:"venues_used,omitempty"`
	// Daily only.
	MinDevBps    *float64 `json:"min_dev_bps,omitempty"`
	MaxDevBps    *float64 `json:"max_dev_bps,omitempty"`
	MinutesGE30  *int     `json:"minutes_ge_30,omitempty"`
	MinutesGE100 *int     `json:"minutes_ge_100,omitempty"`
}

// StableDepegDay is a day at ≥ 30 bps off peg in one source, in the asset's
// numéraire.
type StableDepegDay struct {
	Day         string   `json:"day"`
	Source      string   `json:"source"` // bitstamp_ohlc | defillama | consensus
	Low         *float64 `json:"low,omitempty"`
	High        *float64 `json:"high,omitempty"`
	Close       *float64 `json:"close,omitempty"`
	MinDevBps   *float64 `json:"min_dev_bps"`
	MaxDevBps   *float64 `json:"max_dev_bps"`
	MinutesGE30 *int     `json:"minutes_ge_30,omitempty"` // consensus only
}

type StablePegAssetView struct {
	StablePeg
	SeriesResolution string           `json:"series_resolution"` // 5m | 1d
	Series           []StablePegPoint `json:"series"`
	// DepegDays lists every stored day at ≥ 30 bps off peg (newest first,
	// at most 500; DepegDaysTotal counts them all).
	DepegDays      []StableDepegDay   `json:"depeg_days"`
	DepegDaysTotal int                `json:"depeg_days_total"`
	Annotations    []stableAnnotation `json:"annotations"`
}

type StableRelativePegView struct {
	StableRelativePeg
	SeriesResolution string           `json:"series_resolution"`
	Series           []StablePegPoint `json:"series"`
}

type StablePegResponse struct {
	AsOf      string                `json:"as_of,omitempty"`
	Range     string                `json:"range"`
	WarmingUp bool                  `json:"warming_up"` // no peg sample yet
	Stale     bool                  `json:"stale"`      // the latest sample is older than 3 intervals
	Assets    []StablePegAssetView  `json:"assets"`
	Relative  StableRelativePegView `json:"relative"`
	FX        *StableFX             `json:"fx"`
	Markets   []StableQuote         `json:"markets"` // every venue market of the latest sample
	// Annotations covers every curated event, including coins outside the
	// core set (context).
	Annotations           []stableAnnotation `json:"annotations"`
	AnnotationsOverridden bool               `json:"annotations_overridden"`
	DepegThresholdBps     float64            `json:"depeg_threshold_bps"`
}

func (s *stablecoinService) pegLatest() *stablePegResult {
	if s.peg == nil {
		return nil
	}
	return s.peg.latest()
}

func (s *stablecoinService) buildPeg(r *http.Request, assets []string, rangeLabel string) StablePegResponse {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	now := s.now().UTC()
	res := s.pegLatest()
	rg := stablePegRanges[rangeLabel]
	out := StablePegResponse{Range: rangeLabel, WarmingUp: res == nil, Assets: []StablePegAssetView{}, Markets: []StableQuote{},
		Annotations: s.facts.Annotations, AnnotationsOverridden: s.facts.AnnotationsOverridden, DepegThresholdBps: stableDepegThresholdBps}
	if out.Annotations == nil {
		out.Annotations = []stableAnnotation{}
	}
	if res != nil {
		out.AsOf, out.Stale, out.Markets = stableFmt(res.AsOf), now.Sub(res.AsOf) > stablePegStaleAfter, res.Markets
		fx := res.FX
		out.FX = &fx
		out.Relative.StableRelativePeg = res.Relative
	} else {
		out.Relative.StableRelativePeg = StableRelativePeg{Pair: "USDC/USDT", Quotes: []StableQuote{}}
	}
	out.Relative.SeriesResolution, out.Relative.Series = s.pegSeries(ctx, stableRelativeAsset, rg.Span, rg.Daily, now)
	var fxHistory map[string]float64
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		v := StablePegAssetView{Annotations: []stableAnnotation{}}
		if res != nil {
			v.StablePeg = res.Assets[id]
		} else {
			v.StablePeg = StablePeg{Asset: id, Numeraire: def.Numeraire, State: "unknown", Venues: []string{}, Quotes: []StableQuote{}}
		}
		v.SeriesResolution, v.Series = s.pegSeries(ctx, id, rg.Span, rg.Daily, now)
		if def.Numeraire == "EUR" && fxHistory == nil {
			fxHistory = s.ecbHistory(ctx)
		}
		v.DepegDays, v.DepegDaysTotal = s.depegDays(ctx, def, fxHistory)
		for _, a := range out.Annotations {
			if strings.EqualFold(a.Asset, def.Symbol) {
				v.Annotations = append(v.Annotations, a)
			}
		}
		out.Assets = append(out.Assets, v)
	}
	return out
}

// pegSeries reads the 5-minute samples (spans up to 30 days) or the daily
// consensus rollup.
func (s *stablecoinService) pegSeries(ctx context.Context, asset string, span time.Duration, daily bool, now time.Time) (string, []StablePegPoint) {
	points := []StablePegPoint{}
	if s.store == nil {
		return map[bool]string{false: "5m", true: "1d"}[daily], points
	}
	if !daily {
		rows, err := s.store.stablePeg5mSeries(ctx, asset, now.Add(-span))
		if err != nil {
			slog.Warn("stablecoin peg series read", "asset", asset, "error", err)
		}
		for _, r := range rows {
			vu := r.VenuesUsed
			points = append(points, StablePegPoint{TS: stableFmt(r.Bucket), State: r.State, Consensus: r.Consensus, DevBps: r.DevBps,
				DispBps: r.DispBps, VenuesUsed: &vu})
		}
		return "5m", points
	}
	from := "0000-01-01"
	if span > 0 {
		from = now.Add(-span).Format(time.DateOnly)
	}
	rows, err := s.store.stablePegDailySeries(ctx, asset, stablePegSourceConsensus, from)
	if err != nil {
		slog.Warn("stablecoin peg daily read", "asset", asset, "error", err)
	}
	for _, r := range rows {
		p := StablePegPoint{TS: r.Day, Consensus: r.Close, MinDevBps: r.MinDevBps, MaxDevBps: r.MaxDevBps,
			MinutesGE30: r.MinutesGE30, MinutesGE100: r.MinutesGE100}
		if r.Close != nil {
			p.DevBps = stableF((*r.Close - 1) * 1e4)
		}
		points = append(points, p)
	}
	return "1d", points
}

func (s *stablecoinService) ecbHistory(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	if s.store == nil {
		return out
	}
	rows, err := s.store.stableRatesSeries(ctx, stableSeriesECBEURUSD, "0000-01-01")
	if err != nil {
		slog.Warn("stablecoin ecb history read", "error", err)
	}
	for _, r := range rows {
		out[r.Day] = r.Value
	}
	return out
}

// depegDays collects the days at ≥ 30 bps off peg from the Bitstamp candles,
// BigLens's own consensus rollup and DefiLlama's daily price (EURC through
// the ECB rate of the day).
func (s *stablecoinService) depegDays(ctx context.Context, def stableAssetDef, fx map[string]float64) ([]StableDepegDay, int) {
	days := []StableDepegDay{}
	if s.store == nil {
		return days, 0
	}
	off := func(lo, hi *float64) bool {
		return (lo != nil && stableBpsAtLeast(*lo, stableDepegThresholdBps)) || (hi != nil && stableBpsAtLeast(*hi, stableDepegThresholdBps))
	}
	for _, src := range []string{stablePegSourceBitstamp, stablePegSourceConsensus} {
		rows, err := s.store.stablePegDailySeries(ctx, def.ID, src, "0000-01-01")
		if err != nil {
			slog.Warn("stablecoin depeg history read", "asset", def.ID, "source", src, "error", err)
		}
		for _, r := range rows {
			if !off(r.MinDevBps, r.MaxDevBps) {
				continue
			}
			days = append(days, StableDepegDay{Day: r.Day, Source: src, Low: r.Low, High: r.High, Close: r.Close,
				MinDevBps: r.MinDevBps, MaxDevBps: r.MaxDevBps, MinutesGE30: r.MinutesGE30})
		}
	}
	if gecko, ok := stableGeckoIDs[def.ID]; ok {
		rows, err := s.store.stablePriceDailySeries(ctx, gecko, "0000-01-01")
		if err != nil {
			slog.Warn("stablecoin price history read", "asset", def.ID, "error", err)
		}
		for _, r := range rows {
			var dev *float64
			if def.Numeraire == "EUR" {
				dev = stableDevFromUSD(r.PriceUSD, r.Day, fx)
			} else {
				dev = stableF((r.PriceUSD - 1) * 1e4)
			}
			if dev == nil || !stableBpsAtLeast(*dev, stableDepegThresholdBps) {
				continue
			}
			days = append(days, StableDepegDay{Day: r.Day, Source: stablePriceSourceLlama, MinDevBps: dev, MaxDevBps: dev})
		}
	}
	sort.SliceStable(days, func(i, j int) bool {
		if days[i].Day != days[j].Day {
			return days[i].Day > days[j].Day
		}
		return days[i].Source < days[j].Source
	})
	total := len(days)
	if len(days) > stableDepegDaysMax {
		days = days[:stableDepegDaysMax]
	}
	return days, total
}

func (h *APIHandler) StablecoinPeg(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	q := r.URL.Query()
	asset := q.Get("asset")
	var assets []string
	switch asset {
	case "", "all":
		asset = "all"
		for _, a := range stableCoreAssets {
			assets = append(assets, a.ID)
		}
	default:
		if _, ok := stableAssetByID(asset); !ok {
			writeError(w, "invalid asset", http.StatusBadRequest)
			return
		}
		assets = []string{asset}
	}
	rangeLabel := q.Get("range")
	if _, ok := stablePegRanges[rangeLabel]; !ok {
		rangeLabel = stablePegDefaultRange
	}
	key := "stablecoin:peg:" + asset + ":" + rangeLabel
	v, _ := h.cachedFetch(key, stablePegTTL, func() (any, error) { return h.stable.buildPeg(r, assets, rangeLabel), nil })
	writeJSON(w, v)
}
