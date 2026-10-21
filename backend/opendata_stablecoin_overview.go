package main

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// /overview and /reserves (stablecoin.md §7.2 tabs 1 and 4, §9). Both read
// memory, SQLite and the curated facts only; they never call upstreams.

const (
	stableOverviewTTL = 60 * time.Second
	stableReservesTTL = 10 * time.Minute
	// Watchlist size (§2.1): the 15 largest USD coins by circulating
	// supply; every EUR coin is listed.
	stableWatchUSDRows = 15
	// Watchlist coins with |dev| >= 1,000 bps (10%) have unreliable aggregator
	// prices (for example EURR at $0.0758 -> -9,325 bps and a bogus 108M EURR
	// native supply = value / price); hide their native supply and deviation
	// behind nil ("—") and attach the unreliable-price note.
	stableWatchMaxAbsDevBps   = 1000.0
	stableWatchUnreliableNote = "price unreliable (aggregator)"
	stableDisclaimer          = "Informational only, not financial advice. Signals are rule outputs, not ratings."
)

// stableV1Regimes are the regulatory matrix columns in v1 (HK, SG and JP are
// v1.1).
var stableV1Regimes = []string{"EU-MiCA", "US-GENIUS"}

// --- watchlist (§2.1, §7.2) ---

type StableWatchRow struct {
	LlamaID           string   `json:"llama_id"`
	Symbol            string   `json:"symbol"`
	Name              string   `json:"name"`
	PegType           string   `json:"peg_type"`  // peggedUSD | peggedEUR
	Numeraire         string   `json:"numeraire"` // USD | EUR
	PegMechanism      string   `json:"peg_mechanism"`
	Core              string   `json:"core,omitempty"` // usdt | usdc | eurc
	CirculatingNative *float64 `json:"circulating_native"`
	CirculatingUSD    *float64 `json:"circulating_usd"`
	Change1dPct       *float64 `json:"change_1d_pct"`
	Change7dPct       *float64 `json:"change_7d_pct"`
	Change30dPct      *float64 `json:"change_30d_pct"`
	PriceUSD          *float64 `json:"price_usd"`
	// DevBps is in the coin's own currency (§2.1): peggedUSD (price − 1) ×
	// 1e4; peggedEUR (price ÷ EUR/USD − 1) × 1e4 with the FX consensus, null
	// when the rate or the price is unknown, or when |dev| ≥ 1,000 bps.
	DevBps   *float64 `json:"dev_bps"`
	SharePct *float64 `json:"share_pct"` // of its peg type's total
	Note     string   `json:"note,omitempty"`
}

type StableWatchlist struct {
	AsOf        string           `json:"as_of,omitempty"`
	Stale       bool             `json:"stale"`
	Source      string           `json:"source"` // defillama
	FXEURUSD    *float64         `json:"fx_eur_usd"`
	FXAsOf      string           `json:"fx_as_of,omitempty"`
	USDTotalUSD *float64         `json:"usd_total_usd"` // every USD-pegged coin in the response
	EURTotalUSD *float64         `json:"eur_total_usd"`
	EURSharePct *float64         `json:"eur_share_pct"` // EUR coins' share of USD + EUR, in USD
	Rows        []StableWatchRow `json:"rows"`
	Note        string           `json:"note"`
}

// stableWatchlist builds the market table from a DefiLlama snapshot: the
// largest USD-pegged coins and every EUR-pegged coin (other peg types are
// left out), with native supply (value ÷ price, §5.1) and deviation in each
// coin's own currency.
func stableWatchlist(snap *llamaStableSnapshot, asOf time.Time, stale bool, fx *float64, fxAsOf time.Time) StableWatchlist {
	w := StableWatchlist{Source: "defillama", Rows: []StableWatchRow{},
		Note: "The 15 largest USD-pegged coins and every EUR-pegged coin; other peg types are not listed. Deviations are in each coin's own currency (EUR coins through the EUR/USD consensus)."}
	if snap == nil {
		return w
	}
	w.AsOf, w.Stale, w.FXEURUSD, w.FXAsOf = stableFmt(asOf), stale, fx, stableFmt(fxAsOf)
	core := map[string]string{}
	for _, d := range stableCoreAssets {
		core[d.LlamaID] = d.ID
	}
	var usd, eur []llamaAsset
	var usdTotal, eurTotal float64
	for _, a := range snap.Assets {
		if a.CirculatingUSD == nil {
			continue
		}
		switch a.PegType {
		case "peggedUSD":
			usd, usdTotal = append(usd, a), usdTotal+*a.CirculatingUSD
		case "peggedEUR":
			eur, eurTotal = append(eur, a), eurTotal+*a.CirculatingUSD
		}
	}
	w.USDTotalUSD, w.EURTotalUSD = stableF(usdTotal), stableF(eurTotal)
	if usdTotal+eurTotal > 0 {
		w.EURSharePct = stableF(eurTotal / (usdTotal + eurTotal) * 100)
	}
	add := func(list []llamaAsset, n int, total float64, numeraire string) {
		sort.SliceStable(list, func(i, j int) bool { return *list[i].CirculatingUSD > *list[j].CirculatingUSD })
		for i, a := range list {
			if i >= n {
				break
			}
			r := StableWatchRow{LlamaID: a.LlamaID, Symbol: a.Symbol, Name: a.Name, PegType: a.PegType, Numeraire: numeraire,
				PegMechanism: a.PegMechanism, Core: core[a.LlamaID], CirculatingNative: a.CirculatingNative, CirculatingUSD: a.CirculatingUSD,
				PriceUSD: a.PriceUSD, Change1dPct: stablePct(a.CirculatingNative, a.PrevDayNative),
				Change7dPct: stablePct(a.CirculatingNative, a.PrevWeekNative), Change30dPct: stablePct(a.CirculatingNative, a.PrevMonthNative)}
			if total > 0 {
				r.SharePct = stableF(*a.CirculatingUSD / total * 100)
			}
			if a.PriceUSD != nil {
				switch {
				case numeraire == "USD":
					r.DevBps = stableF((*a.PriceUSD - 1) * 1e4)
				case fx != nil && *fx > 0:
					r.DevBps = stableF((*a.PriceUSD / *fx - 1) * 1e4)
				}
			}
			if r.DevBps != nil && stableBpsAtLeast(*r.DevBps, stableWatchMaxAbsDevBps) {
				r.CirculatingNative = nil
				r.DevBps = nil
				r.Note = stableWatchUnreliableNote
			}
			w.Rows = append(w.Rows, r)
		}
	}
	add(usd, stableWatchUSDRows, usdTotal, "USD")
	add(eur, len(eur), eurTotal, "EUR")
	return w
}

// --- /overview ---

// StableAttestationAge is the latest attestation recorded by BigLens (S6).
type StableAttestationAge struct {
	PeriodEnd           string `json:"period_end"`
	Published           string `json:"published"`
	Firm                string `json:"firm"`
	AgeDays             int    `json:"age_days"`
	CadenceDays         int    `json:"cadence_days"`
	AttestationsChecked string `json:"attestations_checked,omitempty"`
}

type StableOverviewCard struct {
	Asset             string              `json:"asset"`
	Symbol            string              `json:"symbol"`
	Numeraire         string              `json:"numeraire"`
	Consensus         *float64            `json:"consensus"`
	DevBps            *float64            `json:"dev_bps"`
	PriceAsOf         string              `json:"price_as_of,omitempty"`
	PegSignal         string              `json:"peg_signal"` // S1 state
	CirculatingNative *float64            `json:"circulating_native"`
	CirculatingSource string              `json:"circulating_source,omitempty"` // issuer | aggregator
	CirculatingAsOf   string              `json:"circulating_as_of,omitempty"`
	NetIssuance       []StableNetIssuance `json:"net_issuance"` // 24h and 7d
	// Coverage is Tether's total assets ÷ liabilities (USDT only).
	Coverage     *float64              `json:"coverage"`
	CoverageAsOf string                `json:"coverage_as_of,omitempty"`
	Attestation  *StableAttestationAge `json:"attestation"`
	Regulatory   map[string]string     `json:"regulatory"` // regime → status
	// ControlState and ControlSummary come from S8 ("no changes in 7 days on
	// …", or the changes).
	ControlState   string `json:"control_state"`
	ControlSummary string `json:"control_summary"`
	// Freezes30d sums the 30-day freezes over the synced chains; null when no
	// chain of the asset is synced.
	Freezes30d  *int   `json:"freezes_30d"`
	WorstSignal string `json:"worst_signal"`
}

type StableMatrixRow struct {
	Asset   string         `json:"asset"`
	Worst   string         `json:"worst"`
	Signals []StableSignal `json:"signals"`
}

type StableOverviewSeries struct {
	Asset string            `json:"asset"`
	Days  []StableSupplyDay `json:"days"`
}

type StableOverviewResponse struct {
	AsOf          string                 `json:"as_of"`
	Range         string                 `json:"range"`
	WarmingUp     bool                   `json:"warming_up"` // no supply sample yet
	Cards         []StableOverviewCard   `json:"cards"`
	Matrix        []StableMatrixRow      `json:"matrix"`
	Alerts        map[string]int         `json:"alerts"`
	Events        []StableEvent          `json:"events"`
	SupplyHistory []StableOverviewSeries `json:"supply_history"`
	Watchlist     StableWatchlist        `json:"watchlist"`
	Disclaimer    string                 `json:"disclaimer"`
}

// signalsFor is an asset's signal row; without a running engine every
// signal is unknown (warming up).
func (s *stablecoinService) signalsFor(asset string) []StableSignal {
	if s.signals != nil {
		return s.signals.snapshot(asset)
	}
	return newStableSignalEngine(nil, s.now).snapshot(asset)
}

func stableLatestAttestation(f stableAssetFacts, now time.Time) *StableAttestationAge {
	if len(f.Attestations) == 0 {
		return nil
	}
	latest := f.Attestations[0]
	for _, a := range f.Attestations[1:] {
		if a.PeriodEnd > latest.PeriodEnd {
			latest = a
		}
	}
	pe, err := time.Parse(time.DateOnly, latest.PeriodEnd)
	if err != nil {
		return nil
	}
	return &StableAttestationAge{PeriodEnd: latest.PeriodEnd, Published: latest.Published, Firm: latest.Firm,
		AgeDays: int(now.Sub(pe).Hours() / 24), CadenceDays: f.CadenceDays, AttestationsChecked: f.AttestationsChecked}
}

func (s *stablecoinService) buildOverview(ctx context.Context, rangeLabel string) StableOverviewResponse {
	now := s.now().UTC()
	state, peg := s.snapshot(), s.pegLatest()
	out := StableOverviewResponse{AsOf: stableFmt(now), Range: rangeLabel, WarmingUp: state == nil, Cards: []StableOverviewCard{},
		Matrix: []StableMatrixRow{}, Events: []StableEvent{}, SupplyHistory: []StableOverviewSeries{}, Disclaimer: stableDisclaimer}
	var tether *StableTetherPoint
	if s.store != nil {
		if tr, err := s.store.stableTetherReserves(ctx, 1); err != nil {
			slog.Warn("stablecoin overview: tether snapshot", "error", err)
		} else {
			tether = tr.Latest
		}
	}
	var assets, states []string
	for _, def := range stableCoreAssets {
		assets = append(assets, def.ID)
		sigs := s.signalsFor(def.ID)
		var st []string
		byID := map[string]StableSignal{}
		for _, sg := range sigs {
			st, byID[sg.ID] = append(st, sg.State), sg
		}
		states = append(states, st...)
		worst := stableWorstState(st)
		out.Matrix = append(out.Matrix, StableMatrixRow{Asset: def.ID, Worst: worst, Signals: sigs})
		card := StableOverviewCard{Asset: def.ID, Symbol: def.Symbol, Numeraire: def.Numeraire, NetIssuance: []StableNetIssuance{},
			PegSignal: byID["S1"].State, ControlState: byID["S8"].State, ControlSummary: byID["S8"].Summary, WorstSignal: worst,
			Regulatory: map[string]string{}}
		if peg != nil {
			p := peg.Assets[def.ID]
			card.Consensus, card.DevBps, card.PriceAsOf = p.Consensus, p.DevBps, stableFmt(peg.AsOf)
		}
		if state != nil {
			a := state.Assets[def.ID]
			switch {
			case a.Totals.IssuerCirculatingNative != nil:
				card.CirculatingNative, card.CirculatingSource, card.CirculatingAsOf = a.Totals.IssuerCirculatingNative, "issuer", a.Totals.IssuerAsOf
			case a.Totals.AggregatorCirculatingNative != nil:
				card.CirculatingNative, card.CirculatingSource, card.CirculatingAsOf = a.Totals.AggregatorCirculatingNative, "aggregator", a.Totals.AggregatorAsOf
			}
			for _, n := range a.NetIssuance {
				if n.Window == "24h" || n.Window == "7d" {
					card.NetIssuance = append(card.NetIssuance, n)
				}
			}
		}
		if def.ID == "usdt" && tether != nil {
			card.Coverage, card.CoverageAsOf = tether.Coverage, tether.AsOf
		}
		f := s.facts.Assets[def.ID]
		card.Attestation = stableLatestAttestation(f, now)
		for _, r := range stableV1Regimes {
			card.Regulatory[r] = stableStateUnknown
		}
		for _, r := range f.Regulatory {
			card.Regulatory[r.Regime] = r.Status
		}
		if s.store != nil {
			card.Freezes30d = s.freezes30d(ctx, def.ID)
		}
		out.Cards = append(out.Cards, card)
	}
	out.Alerts = stableAlertCounts(states)
	out.Events = s.buildEvents(ctx, assets, now, stableEventsLimit)
	if s.store != nil {
		from := ""
		if days := stableRanges[rangeLabel]; days > 0 {
			from = now.AddDate(0, 0, -days).Format(time.DateOnly)
		}
		for _, a := range assets {
			days, err := stableSupplyHistory(ctx, s.store, a, from)
			if err != nil {
				slog.Warn("stablecoin overview: supply history", "asset", a, "error", err)
				days = []StableSupplyDay{}
			}
			out.SupplyHistory = append(out.SupplyHistory, StableOverviewSeries{Asset: a, Days: days})
		}
	}
	var fx *float64
	var fxAt time.Time
	if peg != nil {
		fx, fxAt = peg.FX.EURUSD, peg.AsOf
	}
	if state != nil {
		out.Watchlist = stableWatchlist(state.Llama, state.LlamaAsOf, state.LlamaStale, fx, fxAt)
	} else {
		out.Watchlist = stableWatchlist(nil, time.Time{}, false, fx, fxAt)
	}
	return out
}

// freezes30d sums the 30-day freeze counts over an asset's synced chains.
func (s *stablecoinService) freezes30d(ctx context.Context, asset string) *int {
	stats, err := s.store.stableFreezeStats(ctx, asset)
	if err != nil {
		slog.Warn("stablecoin overview: freeze stats", "asset", asset, "error", err)
		return nil
	}
	var n *int
	for _, st := range stats {
		for _, w := range st.Windows {
			if st.Status == "ok" && w.Days == 30 {
				if n == nil {
					n = new(int)
				}
				*n += w.Freezes
			}
		}
	}
	return n
}

func (h *APIHandler) StablecoinOverview(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	rangeLabel := r.URL.Query().Get("range")
	if rangeLabel == "" {
		rangeLabel = stableDefaultRange
	}
	if _, ok := stableRanges[rangeLabel]; !ok {
		writeError(w, "invalid range", http.StatusBadRequest)
		return
	}
	v, _ := h.cachedFetch("stablecoin:overview:"+rangeLabel, stableOverviewTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		return h.stable.buildOverview(ctx, rangeLabel), nil
	})
	writeJSON(w, v)
}

// --- /reserves ---

type StableAttestationView struct {
	stableAttestation
	Ratio   *float64 `json:"ratio"`    // reserves ÷ liabilities, when both are stated
	AgeDays int      `json:"age_days"` // since period_end
}

type StableKeyDateView struct {
	Date      string `json:"date"`
	Label     string `json:"label"`
	SourceURL string `json:"source_url,omitempty"`
	Past      bool   `json:"past"`
}

type StableRegimeView struct {
	Regime    string              `json:"regime"`
	KeyDates  []StableKeyDateView `json:"key_dates"`
	Note      string              `json:"note,omitempty"`
	AsOf      string              `json:"as_of"`
	SourceURL string              `json:"source_url"`
}

type StableReservesAsset struct {
	Asset       string   `json:"asset"`
	Symbol      string   `json:"symbol"`
	Numeraire   string   `json:"numeraire"`
	FactsStatus string   `json:"facts_status"` // curated | unknown
	Overridden  []string `json:"overridden"`   // sections set by the operator override
	// Tether is BigLens's history of Tether's balance sheet (USDT only).
	Tether              *StableTetherReserves   `json:"tether,omitempty"`
	Signals             []StableSignal          `json:"signals"` // S5, S6, S11
	CadenceDays         int                     `json:"cadence_days"`
	AttestationsChecked string                  `json:"attestations_checked,omitempty"`
	Attestations        []StableAttestationView `json:"attestations"` // newest first
	ReserveMix          []stableReserveMix      `json:"reserve_mix"`  // newest first
	Issuer              *stableIssuerFacts      `json:"issuer"`
	Redemption          []stableRedemption      `json:"redemption"`
	Regulatory          []stableRegulatory      `json:"regulatory"` // v1 regimes first; missing ones unknown
	Events              []stableFactEvent       `json:"events"`
}

type StableReservesResponse struct {
	AsOf              string                `json:"as_of"`
	Asset             string                `json:"asset"`
	FactsVersion      string                `json:"facts_version"`
	OverrideStatus    string                `json:"override_status"`
	OverrideVersion   string                `json:"override_version,omitempty"`
	Assets            []StableReservesAsset `json:"assets"`
	Regimes           []StableRegimeView    `json:"regimes"`
	RegimesOverridden bool                  `json:"regimes_overridden"`
	Disclaimer        string                `json:"disclaimer"`
}

func (s *stablecoinService) buildReserves(ctx context.Context, asset string, assets []string) StableReservesResponse {
	now := s.now().UTC()
	today := now.Format(time.DateOnly)
	out := StableReservesResponse{AsOf: stableFmt(now), Asset: asset, FactsVersion: s.facts.Version, OverrideStatus: s.facts.OverrideStatus,
		OverrideVersion: s.facts.OverrideVersion, Assets: []StableReservesAsset{}, Regimes: []StableRegimeView{},
		RegimesOverridden: s.facts.RegimesOverridden, Disclaimer: stableDisclaimer}
	for _, g := range s.facts.Regimes {
		v := StableRegimeView{Regime: g.Regime, KeyDates: []StableKeyDateView{}, Note: g.Note, AsOf: g.AsOf, SourceURL: g.SourceURL}
		for _, k := range g.KeyDates {
			v.KeyDates = append(v.KeyDates, StableKeyDateView{Date: k.Date, Label: k.Label, SourceURL: k.SourceURL, Past: k.Date <= today})
		}
		out.Regimes = append(out.Regimes, v)
	}
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		f := s.facts.Assets[id]
		a := StableReservesAsset{Asset: id, Symbol: def.Symbol, Numeraire: def.Numeraire, FactsStatus: "curated",
			Overridden: s.facts.Overridden[id], Signals: []StableSignal{}, CadenceDays: f.CadenceDays, AttestationsChecked: f.AttestationsChecked,
			Attestations: []StableAttestationView{}, ReserveMix: []stableReserveMix{}, Issuer: f.Issuer, Redemption: []stableRedemption{},
			Regulatory: []stableRegulatory{}, Events: []stableFactEvent{}}
		if f.Status == stableFactsUnknown || (f.Issuer == nil && len(f.Attestations) == 0 && len(f.Regulatory) == 0) {
			a.FactsStatus = "unknown"
		}
		if a.Overridden == nil {
			a.Overridden = []string{}
		}
		for _, sg := range s.signalsFor(id) {
			if sg.ID == "S5" || sg.ID == "S6" || sg.ID == "S11" {
				a.Signals = append(a.Signals, sg)
			}
		}
		if id == "usdt" && s.store != nil {
			if tr, err := s.store.stableTetherReserves(ctx, stableTetherHistoryMax); err != nil {
				slog.Warn("stablecoin reserves: tether history", "error", err)
			} else {
				a.Tether = &tr
			}
		}
		for _, at := range f.Attestations {
			v := StableAttestationView{stableAttestation: at}
			if pe, err := time.Parse(time.DateOnly, at.PeriodEnd); err == nil {
				v.AgeDays = int(now.Sub(pe).Hours() / 24)
			}
			res, liab := stableParseDecimal(at.ReservesNative), stableParseDecimal(at.LiabilitiesNative)
			if res != nil && liab != nil && *liab > 0 {
				v.Ratio = stableF(*res / *liab)
			}
			a.Attestations = append(a.Attestations, v)
		}
		sort.SliceStable(a.Attestations, func(i, j int) bool { return a.Attestations[i].PeriodEnd > a.Attestations[j].PeriodEnd })
		a.ReserveMix = append(a.ReserveMix, f.ReserveMix...)
		sort.SliceStable(a.ReserveMix, func(i, j int) bool { return a.ReserveMix[i].AsOf > a.ReserveMix[j].AsOf })
		a.Redemption = append(a.Redemption, f.Redemption...)
		a.Events = append(a.Events, f.Events...)
		rows := map[string]bool{}
		for _, r := range stableV1Regimes {
			found := false
			for _, reg := range f.Regulatory {
				if reg.Regime == r {
					a.Regulatory, found = append(a.Regulatory, reg), true
				}
			}
			if !found {
				a.Regulatory = append(a.Regulatory, stableRegulatory{Regime: r, Status: stableStateUnknown})
			}
			rows[r] = true
		}
		for _, reg := range f.Regulatory {
			if !rows[reg.Regime] {
				a.Regulatory = append(a.Regulatory, reg)
			}
		}
		out.Assets = append(out.Assets, a)
	}
	return out
}

func stableParseDecimal(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (h *APIHandler) StablecoinReserves(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	asset := r.URL.Query().Get("asset")
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
	v, _ := h.cachedFetch("stablecoin:reserves:"+asset, stableReservesTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		return h.stable.buildReserves(ctx, asset, assets), nil
	})
	writeJSON(w, v)
}
