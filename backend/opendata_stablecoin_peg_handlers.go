package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// /peg (stablecoin.md §9): the venue table, the consensus and dispersion
// series, the per-venue deviation heatmap, the relative peg (exchange median
// and Uniswap), FX and the ECB reference rate, and the depeg history with
// annotations. Served from the peg sampler's memory and SQLite; it never
// calls upstreams. Chainlink (oracle) and Curve are v1.1.

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

// StableDepegDay, StableDepegMarker and StableLastDeviation are defined with
// the classification rules in opendata_stablecoin_depeg.go.

type StablePegAssetView struct {
	StablePeg
	SeriesResolution string           `json:"series_resolution"` // 5m | 1d
	Series           []StablePegPoint `json:"series"`
	// DepegDays lists the stored days off peg by their daily close (newest
	// first, at most 500; DepegDaysTotal counts them all). LowMarkers are
	// the Bitstamp intraday lows shown on the history chart.
	DepegDays      []StableDepegDay    `json:"depeg_days"`
	DepegDaysTotal int                 `json:"depeg_days_total"`
	LowMarkers     []StableDepegMarker `json:"low_markers"`
	// LastGE100 is the newest ≥ 100 bps deviation in the stored history;
	// null when there is none since HistoryFrom.
	LastGE100   *StableLastDeviation `json:"last_ge_100"`
	HistoryFrom string               `json:"history_from,omitempty"`
	Annotations []stableAnnotation   `json:"annotations"`
	// DailyCloses is the depeg-history chart: every stored day's close by
	// the same rule, oldest first.
	DailyCloses []StableDepegClose `json:"daily_closes"`
	// Heatmap is the per-venue deviation heatmap; null for ranges longer
	// than the 30 days of stored inputs.
	Heatmap *StablePegHeatmap `json:"heatmap"`
}

type StableRelativePegView struct {
	StableRelativePeg
	SeriesResolution string            `json:"series_resolution"`
	Series           []StablePegPoint  `json:"series"`
	Heatmap          *StablePegHeatmap `json:"heatmap"`
	// UniswapSeries is the DEX panel: the stored 10-minute readings up to
	// 30 days, beyond that the last reading of each day.
	UniswapSeries []StableDexPoint `json:"uniswap_series"`
}

type StablePegResponse struct {
	AsOf      string                `json:"as_of,omitempty"`
	Range     string                `json:"range"`
	WarmingUp bool                  `json:"warming_up"` // no peg sample yet
	Stale     bool                  `json:"stale"`      // the latest sample is older than 3 intervals
	Assets    []StablePegAssetView  `json:"assets"`
	Relative  StableRelativePegView `json:"relative"`
	FX        *StableFX             `json:"fx"`
	// FXSingleVenue: the EUR/USD consensus behind EURC's derived inputs
	// comes from one venue (Kraken or Bitstamp) in the latest sample.
	FXSingleVenue bool          `json:"fx_single_venue"`
	Markets       []StableQuote `json:"markets"` // every venue market of the latest sample
	// Annotations covers every curated event, including coins outside the
	// core set (context).
	Annotations           []stableAnnotation `json:"annotations"`
	AnnotationsOverridden bool               `json:"annotations_overridden"`
	DepegThresholdBps     float64            `json:"depeg_threshold_bps"` // a day's close; EURC on DefiLlama ÷ ECB uses each day's threshold_bps
	LowMarkerBps          float64            `json:"low_marker_bps"`
	// ECB is the newest stored ECB reference rate, for EURC mode; null when
	// no EUR asset is requested or none is stored.
	ECB *StableECBRate `json:"ecb"`
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
		Annotations: s.facts.Annotations, AnnotationsOverridden: s.facts.AnnotationsOverridden, DepegThresholdBps: stableDepegThresholdBps,
		LowMarkerBps: stableLowMarkerBps}
	if out.Annotations == nil {
		out.Annotations = []stableAnnotation{}
	}
	if res != nil {
		out.AsOf, out.Stale, out.Markets = stableFmt(res.AsOf), now.Sub(res.AsOf) > stablePegStaleAfter, res.Markets
		fx := res.FX
		out.FX, out.FXSingleVenue = &fx, fx.VenuesUsed == 1
		out.Relative.StableRelativePeg = res.Relative
	} else {
		out.Relative.StableRelativePeg = StableRelativePeg{Pair: "USDC/USDT", Quotes: []StableQuote{}}
	}
	out.Relative.SeriesResolution, out.Relative.Series = s.pegSeries(ctx, stableRelativeAsset, rg.Span, rg.Daily, now)
	out.Relative.Heatmap = s.pegHeatmap(ctx, stableRelativeAsset, rg.Span, now)
	out.Relative.UniswapSeries = s.pegDexSeries(ctx, rg.Span, rg.Daily, now)
	var fxHistory map[string]float64
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		v := StablePegAssetView{Annotations: []stableAnnotation{}, DepegDays: []StableDepegDay{}, LowMarkers: []StableDepegMarker{},
			DailyCloses: []StableDepegClose{}}
		if res != nil {
			v.StablePeg = res.Assets[id]
		} else {
			v.StablePeg = StablePeg{Asset: id, Numeraire: def.Numeraire, State: "unknown", Venues: []string{}, Quotes: []StableQuote{}}
		}
		v.SeriesResolution, v.Series = s.pegSeries(ctx, id, rg.Span, rg.Daily, now)
		v.Heatmap = s.pegHeatmap(ctx, id, rg.Span, now)
		if def.Numeraire == "EUR" && fxHistory == nil {
			fxHistory = s.ecbHistory(ctx)
			out.ECB = stableLatestECB(fxHistory)
		}
		if hist, ok := s.depegHistory(ctx, def, fxHistory, now); ok {
			v.DepegDays, v.DepegDaysTotal, v.LowMarkers, v.LastGE100, v.HistoryFrom = hist.Days, hist.Total, hist.Markers, hist.LastGE100, hist.HistoryFrom
			v.DailyCloses = hist.Closes
		}
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

// depegHistory reads one asset's stored history (consensus rollup, Bitstamp
// candles for the pairs in stableOHLCPairs, DefiLlama prices) and applies
// the §7.2 classification. ok is false without a store.
func (s *stablecoinService) depegHistory(ctx context.Context, def stableAssetDef, fx map[string]float64, now time.Time) (stableDepegHistory, bool) {
	if s.store == nil {
		return stableDepegHistory{}, false
	}
	in := stableDepegInputs{Numeraire: def.Numeraire, ECB: fx, Today: now.Format(time.DateOnly)}
	var err error
	if in.Rollup, err = s.store.stablePegDailySeries(ctx, def.ID, stablePegSourceConsensus, "0000-01-01"); err != nil {
		slog.Warn("stablecoin depeg history read", "asset", def.ID, "source", stablePegSourceConsensus, "error", err)
	}
	for _, p := range stableOHLCPairs {
		if p.Asset != def.ID {
			continue
		}
		if in.Candles, err = s.store.stablePegDailySeries(ctx, def.ID, stablePegSourceBitstamp, "0000-01-01"); err != nil {
			slog.Warn("stablecoin depeg history read", "asset", def.ID, "source", stablePegSourceBitstamp, "error", err)
		}
	}
	if def.Numeraire == "EUR" {
		if in.Kraken, err = s.store.stablePegDailySeries(ctx, def.ID, stablePegSourceKraken, "0000-01-01"); err != nil {
			slog.Warn("stablecoin depeg history read", "asset", def.ID, "source", stablePegSourceKraken, "error", err)
		}
	}
	if gecko, ok := stableGeckoIDs[def.ID]; ok {
		if in.Prices, err = s.store.stablePriceDailySeries(ctx, gecko, "0000-01-01"); err != nil {
			slog.Warn("stablecoin price history read", "asset", def.ID, "error", err)
		}
	}
	h := stableClassifyDepeg(in)
	if len(h.Days) > stableDepegDaysMax {
		h.Days = h.Days[:stableDepegDaysMax]
	}
	return h, true
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
