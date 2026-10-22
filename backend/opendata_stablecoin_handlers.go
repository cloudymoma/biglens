package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Stablecoins dashboard HTTP API (stablecoin.md §9): /meta, /live, /supply
// and /peg here; /overview, /reserves, /chains and /controls in their own
// files. Handlers read memory and SQLite only and never call upstreams.
// Upstream failures show as source badges and nulls with HTTP 200; a disabled
// dashboard answers 404.

const (
	stableMetaTTL   = 60 * time.Second
	stableLiveTTL   = 30 * time.Second
	stableSupplyTTL = 5 * time.Minute
)

// stableRanges whitelists /supply ?range= (days of daily history; 0 = all).
var stableRanges = map[string]int{"30d": 30, "90d": 90, "1y": 365, "all": 0}

const stableDefaultRange = "90d"

type stablecoinRoute struct {
	Pattern string
	Handler http.HandlerFunc
}

func stablecoinRoutes(h *APIHandler) []stablecoinRoute {
	return []stablecoinRoute{
		{Pattern: "/api/opendata/stablecoin/meta", Handler: h.StablecoinMeta},
		{Pattern: "/api/opendata/stablecoin/live", Handler: h.StablecoinLive},
		{Pattern: "/api/opendata/stablecoin/overview", Handler: h.StablecoinOverview},
		{Pattern: "/api/opendata/stablecoin/supply", Handler: h.StablecoinSupply},
		{Pattern: "/api/opendata/stablecoin/peg", Handler: h.StablecoinPeg},
		{Pattern: "/api/opendata/stablecoin/reserves", Handler: h.StablecoinReserves},
		{Pattern: "/api/opendata/stablecoin/chains", Handler: h.StablecoinChains},
		{Pattern: "/api/opendata/stablecoin/controls", Handler: h.StablecoinControls},
	}
}

// registerStablecoinRoutes mounts the routes when the dashboard is enabled;
// a disabled dashboard has no routes (404), and each handler also checks.
func registerStablecoinRoutes(mux *http.ServeMux, h *APIHandler, gzWrap func(http.HandlerFunc) http.Handler) {
	if h == nil || h.stable == nil {
		return
	}
	for _, rt := range stablecoinRoutes(h) {
		mux.Handle(rt.Pattern, gzWrap(rt.Handler))
	}
}

func stableGETOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// --- /meta ---

type StableMetaAsset struct {
	ID        string `json:"id"`
	Symbol    string `json:"symbol"`
	Numeraire string `json:"numeraire"`
	Issuer    string `json:"issuer"`
}

type StableMetaFacts struct {
	Version         string              `json:"version"`
	OverrideStatus  string              `json:"override_status"` // none | applied | invalid | unreadable
	OverrideVersion string              `json:"override_version,omitempty"`
	Overridden      map[string][]string `json:"overridden,omitempty"` // asset → sections (operator override badge)
	AssetStatus     map[string]string   `json:"asset_status"`         // unknown | curated
}

type StableMetaBigQuery struct {
	Jobs             bool   `json:"jobs"`
	Status           string `json:"status"` // not_in_v1: the opt-in jobs ship in v1.1
	InitialDays      int    `json:"initial_days"`
	MonthlyBudgetGiB int64  `json:"monthly_budget_gib"`
	Tron             bool   `json:"tron"`
}

type StableMeta struct {
	AsOf          string                 `json:"as_of"`
	Assets        []StableMetaAsset      `json:"assets"`
	Providers     []StableProviderHealth `json:"providers"`
	Samplers      []StableSamplerHealth  `json:"samplers"`
	Facts         StableMetaFacts        `json:"facts"`
	BigQuery      StableMetaBigQuery     `json:"bigquery"`
	CoverageNotes []string               `json:"coverage_notes"`
	// DataHealth merges every asset's S13 inputs (pre-M4: the Tether sum check).
	DataHealth StableDataHealth `json:"data_health"`
}

var stableCoverageNotes = []string{
	"On-chain totals are read on eth, arb, op, base, tron and sol only; other chains are issuer- or aggregator-reported.",
	"DefiLlama list values are USD (native × DefiLlama price); they are converted to native units with the price from the same response.",
	"USDT per-chain issuer figures come from Tether's transparency JSON, which updates about once a day; the issuer vs on-chain check compares the latest Tether update with the latest on-chain total and needs 2 consecutive Tether updates. USDC and EURC pairs are compared only when the readings are at most 15 minutes apart.",
}

func (s *stablecoinService) buildMeta() StableMeta {
	m := StableMeta{AsOf: stableFmt(s.now()), CoverageNotes: stableCoverageNotes}
	for _, a := range stableCoreAssets {
		m.Assets = append(m.Assets, StableMetaAsset{ID: a.ID, Symbol: a.Symbol, Numeraire: a.Numeraire, Issuer: a.Issuer})
	}
	health := s.health
	if health == nil {
		health = newStableHealth(s.settings, s.now)
	}
	m.Providers, m.Samplers = health.providers(), health.samplers()
	m.Facts = StableMetaFacts{Version: s.facts.Version, OverrideStatus: s.facts.OverrideStatus,
		OverrideVersion: s.facts.OverrideVersion, Overridden: s.facts.Overridden, AssetStatus: map[string]string{}}
	for _, a := range stableCoreAssets {
		status := "unknown"
		if f, ok := s.facts.Assets[a.ID]; ok && f.Status != stableFactsUnknown {
			status = "curated"
		}
		m.Facts.AssetStatus[a.ID] = status
	}
	m.BigQuery = StableMetaBigQuery{Jobs: s.settings.Jobs, Status: "not_in_v1", InitialDays: s.settings.InitialDays,
		MonthlyBudgetGiB: s.settings.BudgetBytes >> 30, Tron: s.settings.Tron}
	m.DataHealth = newStableDataHealth()
	if state := s.snapshot(); state != nil {
		for _, def := range stableCoreAssets {
			m.DataHealth.merge(state.Assets[def.ID].DataHealth)
		}
	}
	return m
}

func (h *APIHandler) StablecoinMeta(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	v, _ := h.cachedFetch("stablecoin:meta", stableMetaTTL, func() (any, error) { return h.stable.buildMeta(), nil })
	writeJSON(w, v)
}

// --- /live ---

type StableLiveAsset struct {
	Asset     string `json:"asset"`
	Symbol    string `json:"symbol"`
	Numeraire string `json:"numeraire"`
	// Peg fields: the latest 5-minute consensus in the asset's numéraire.
	Consensus  *float64 `json:"consensus"`
	DevBps     *float64 `json:"dev_bps"`
	VenuesUsed *int     `json:"venues_used"`
	PegState   string   `json:"peg_state,omitempty"` // ok | single_venue | unknown
	// PegSignal is S1's state, which colors the deviation chip (§7.1); null
	// when the signal engine is not running.
	PegSignal    *string  `json:"peg_signal"`
	DevMin24hBps *float64 `json:"dev_min_24h_bps"` // over the stored samples of the last 24 h
	DevMax24hBps *float64 `json:"dev_max_24h_bps"`
	// DevRangeFrom is the oldest sample in that range; DevRangePartial says
	// the samples do not span 24 h yet (cold start: "since HH:MM", §7.4).
	DevRangeFrom    string `json:"dev_range_from,omitempty"`
	DevRangePartial bool   `json:"dev_range_partial"`
	PriceAsOf       string `json:"price_as_of,omitempty"`
	PriceStale      bool   `json:"price_stale,omitempty"` // the sample is older than 3 intervals
	// Supply.
	CirculatingNative    *float64 `json:"circulating_native"`
	CirculatingSource    string   `json:"circulating_source,omitempty"` // issuer | aggregator
	CirculatingAsOf      string   `json:"circulating_as_of,omitempty"`
	CirculatingStale     bool     `json:"circulating_stale,omitempty"`
	CirculatingDegraded  bool     `json:"circulating_degraded,omitempty"` // the issuer source is degraded (Tether sum check)
	NetIssuance24hNative *float64 `json:"net_issuance_24h_native"`
	NetIssuance24hPct    *float64 `json:"net_issuance_24h_pct"`
	NetIssuance24hSource string   `json:"net_issuance_24h_source,omitempty"`
	// WorstSignal is the asset's summary state (§6.1): the worst state across
	// its signals; null when the signal engine is not running.
	WorstSignal *string `json:"worst_signal"`
}

type StableLive struct {
	AsOf           string            `json:"as_of,omitempty"`
	WarmingUp      bool              `json:"warming_up"`     // no supply sample yet
	PegWarmingUp   bool              `json:"peg_warming_up"` // no peg sample yet
	Assets         []StableLiveAsset `json:"assets"`
	MarketTotalUSD *float64          `json:"market_total_usd"`
	MarketAsOf     string            `json:"market_as_of,omitempty"`
	// MarketTotal30dChangePct is the total's change over 30 days in USD, over
	// the coins DefiLlama reports both values for (§7.1).
	MarketTotal30dChangePct *float64 `json:"market_total_30d_change_pct"`
	// Alerts counts signals at critical, warning and watch across the core
	// assets (§7.1); null when the signal engine is not running.
	Alerts map[string]int `json:"alerts"`
}

func (s *stablecoinService) snapshot() *stableSupplyState {
	if s.supply == nil {
		return nil
	}
	return s.supply.snapshot()
}

func (s *stablecoinService) buildLive() StableLive {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state := s.snapshot()
	peg := s.pegLatest()
	now := s.now().UTC()
	out := StableLive{WarmingUp: state == nil, PegWarmingUp: peg == nil, Assets: []StableLiveAsset{}}
	if state != nil {
		out.AsOf, out.MarketTotalUSD, out.MarketAsOf = stableFmt(state.AsOf), state.MarketTotalUSD, stableFmt(state.MarketAsOf)
		out.MarketTotal30dChangePct = stableMarket30dChange(state.Llama)
	}
	for _, def := range stableCoreAssets {
		la := StableLiveAsset{Asset: def.ID, Symbol: def.Symbol, Numeraire: def.Numeraire}
		if peg != nil {
			p := peg.Assets[def.ID]
			vu := p.VenuesUsed
			la.Consensus, la.DevBps, la.VenuesUsed, la.PegState = p.Consensus, p.DevBps, &vu, p.State
			la.PriceAsOf, la.PriceStale = stableFmt(peg.AsOf), now.Sub(peg.AsOf) > stablePegStaleAfter
			var from time.Time
			la.DevMin24hBps, la.DevMax24hBps, from = s.devRange24h(ctx, def.ID, now)
			if !from.IsZero() {
				la.DevRangeFrom, la.DevRangePartial = stableFmt(from), from.After(now.Add(-24*time.Hour+stablePegTick))
			}
		}
		if state != nil {
			a := state.Assets[def.ID]
			issuerStale, issuerDegraded := false, false
			for _, src := range a.Sources {
				if src.ID == def.Issuer {
					issuerStale, issuerDegraded = src.Status == "stale", src.Status == "degraded"
				}
			}
			switch {
			case a.Totals.IssuerCirculatingNative != nil:
				la.CirculatingNative, la.CirculatingSource, la.CirculatingAsOf = a.Totals.IssuerCirculatingNative, "issuer", a.Totals.IssuerAsOf
				la.CirculatingStale, la.CirculatingDegraded = issuerStale, issuerDegraded
			case a.Totals.AggregatorCirculatingNative != nil:
				la.CirculatingNative, la.CirculatingSource, la.CirculatingAsOf = a.Totals.AggregatorCirculatingNative, "aggregator", a.Totals.AggregatorAsOf
			}
			for _, n := range a.NetIssuance {
				if n.Window == "24h" {
					la.NetIssuance24hNative, la.NetIssuance24hPct, la.NetIssuance24hSource = n.NetNative, n.NetPct, n.Source
				}
			}
		}
		if s.signals != nil {
			w := s.signals.worst(def.ID)
			la.WorstSignal = &w
			for _, sg := range s.signals.snapshot(def.ID) {
				if sg.ID == "S1" {
					st := sg.State
					la.PegSignal = &st
				}
			}
		}
		out.Assets = append(out.Assets, la)
	}
	if s.signals != nil {
		out.Alerts = stableAlertCounts(s.signalStates())
	}
	return out
}

// devRange24h is the range of the stored consensus deviations over 24 h and
// the oldest sample with a deviation in it (zero without one).
func (s *stablecoinService) devRange24h(ctx context.Context, asset string, now time.Time) (*float64, *float64, time.Time) {
	if s.store == nil {
		return nil, nil, time.Time{}
	}
	rows, err := s.store.stablePeg5mSeries(ctx, asset, now.Add(-24*time.Hour))
	if err != nil {
		slog.Warn("stablecoin 24h peg range read", "asset", asset, "error", err)
		return nil, nil, time.Time{}
	}
	var lo, hi *float64
	var from time.Time
	for _, r := range rows {
		if r.DevBps == nil {
			continue
		}
		if from.IsZero() {
			from = r.Bucket
		}
		if lo == nil || *r.DevBps < *lo {
			lo = stableF(*r.DevBps)
		}
		if hi == nil || *r.DevBps > *hi {
			hi = stableF(*r.DevBps)
		}
	}
	return lo, hi, from
}

// stableMarket30dChange is the DefiLlama total's 30-day change in percent,
// over the coins with both a current and a month-ago USD value; nil without
// any.
func stableMarket30dChange(snap *llamaStableSnapshot) *float64 {
	if snap == nil {
		return nil
	}
	var cur, prev float64
	for _, a := range snap.Assets {
		if a.CirculatingUSD != nil && a.PrevMonthUSD != nil {
			cur, prev = cur+*a.CirculatingUSD, prev+*a.PrevMonthUSD
		}
	}
	if prev <= 0 {
		return nil
	}
	return stableF((cur/prev - 1) * 100)
}

func (h *APIHandler) StablecoinLive(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	v, _ := h.cachedFetch("stablecoin:live", stableLiveTTL, func() (any, error) { return h.stable.buildLive(), nil })
	writeJSON(w, v)
}

// --- /supply ---

type StableSupplyResponse struct {
	AsOf         string              `json:"as_of,omitempty"`
	Range        string              `json:"range"`
	WarmingUp    bool                `json:"warming_up"`
	HistoryError bool                `json:"history_error,omitempty"`
	Assets       []StableSupplyAsset `json:"assets"`
}

// stableColdAsset is an asset's view before the first sample: every value
// unknown, sources pending (or disabled).
func (s *stablecoinService) stableColdAsset(def stableAssetDef) StableSupplyAsset {
	in := stableSupplyInputs{}
	a := buildStableSupplyAsset(def, in)
	for i := range a.Sources {
		if s.settings.Providers[a.Sources[i].ID] {
			a.Sources[i].Status, a.Sources[i].Error = "pending", ""
		} else {
			a.Sources[i].Status, a.Sources[i].Error = "disabled", ""
		}
	}
	return a
}

func (s *stablecoinService) buildSupply(r *http.Request, assets []string, rangeLabel string) StableSupplyResponse {
	// Shared by singleflight and cached: a cancelled first request must not
	// cancel (and cache) everyone's history read.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	state := s.snapshot()
	out := StableSupplyResponse{Range: rangeLabel, WarmingUp: state == nil, Assets: []StableSupplyAsset{}}
	if state != nil {
		out.AsOf = stableFmt(state.AsOf)
	}
	fromDay := "0000-01-01"
	if days := stableRanges[rangeLabel]; days > 0 {
		fromDay = s.now().UTC().AddDate(0, 0, -days).Format(time.DateOnly)
	}
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		var a StableSupplyAsset
		if state != nil {
			a = state.Assets[id]
		} else {
			a = s.stableColdAsset(def)
		}
		hist, err := stableSupplyHistory(ctx, s.store, id, fromDay)
		if err != nil {
			slog.Warn("stablecoin supply history read", "asset", id, "error", err)
			out.HistoryError = true
			hist = []StableSupplyDay{}
		}
		a.History = hist
		out.Assets = append(out.Assets, a)
	}
	return out
}

var errStableHistoryRead = errors.New("stablecoin history read failed")

func (h *APIHandler) StablecoinSupply(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := stableRanges[rangeLabel]; !ok {
		rangeLabel = stableDefaultRange
	}
	key := "stablecoin:supply:" + asset + ":" + rangeLabel
	v, _ := h.cachedFetch(key, stableSupplyTTL, func() (any, error) {
		res := h.stable.buildSupply(r, assets, rangeLabel)
		if res.HistoryError {
			return res, errStableHistoryRead
		}
		return res, nil
	})
	writeJSON(w, v)
}
