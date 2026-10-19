package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Supply sampler (stablecoin.md §5.1, §5.4, §6 S12, §7.4, §8.4).
//
// Every tick (10 min) fetches Circle and DefiLlama; Tether every 30 min and
// the on-chain totals hourly. At most one observation per source per UTC hour
// is stored, with the observation time as ts (Circle updateDate, Tether update
// time, RPC read time, DefiLlama fetch time). The latest computed view is
// kept in memory for /live and /supply, so handlers never call upstreams.

const (
	stableSupplyTick  = 10 * time.Minute
	stableTetherEvery = 30 * time.Minute
	stableRPCEvery    = time.Hour
	stableChartsEvery = 6 * time.Hour
	stableRatesEvery  = 24 * time.Hour
	// stableRetryAfterFail: a failed history or rates job is retried after
	// this delay instead of on every tick.
	stableRetryAfterFail = 30 * time.Minute

	// S12 inputs (§6.2). The on-chain comparison only pairs readings taken at
	// most stableAlignWindow apart (issuer updateDate vs RPC read time).
	stableAlignWindow               = 15 * time.Minute
	stableReconAggWatchPct          = 0.5
	stableReconAggWarningPct        = 2.0
	stableReconOnchainRelTolerance  = 0.001     // 0.1% of issuer circulating
	stableReconOnchainAbsToleranceN = 1_000_000 // native units

	// stableNetIssuanceTol: C(t−Δ) must be observed within ±4 h of t−Δ, so
	// the 24h metric pairs snapshots 20–28 h apart (§5.4).
	stableNetIssuanceTol = 4 * time.Hour
)

var stableNetIssuanceWindows = []struct {
	Label string
	D     time.Duration
}{{"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}}

// stableProviderRegistry labels every provider key with its terms (§4.6).
var stableProviderRegistry = []btcProviderDef{
	{ID: "defillama", Label: "DefiLlama stablecoins API", License: "personal, non-commercial use; no republishing (defillama.com/terms)"},
	{ID: "circle", Label: "Circle /v1/stablecoins", License: "public endpoint; personal use"},
	{ID: "tether", Label: "Tether transparency JSON", License: "personal viewing (tether.to/en/legal)"},
	{ID: "coinbase", Label: "Coinbase Exchange", License: "market data for personal use"},
	{ID: "kraken", Label: "Kraken", License: "market data for personal use"},
	{ID: "bitstamp", Label: "Bitstamp", License: "terms not confirmed (§4.6)"},
	{ID: "okx", Label: "OKX", License: "market data for personal use"},
	{ID: "binance", Label: "Binance", License: "market data for personal use"},
	{ID: "curve", Label: "Curve API", License: "no API terms found"},
	{ID: "blockscout", Label: "Blockscout", License: "hosted API; keyless access may end"},
	{ID: "ecb", Label: "European Central Bank", License: "reuse with attribution (Source: ECB)"},
	{ID: "us_treasury", Label: "US Treasury", License: "public domain"},
	{ID: "rpc_supply", Label: "On-chain supply (configured RPCs)", License: "public chain state"},
	{ID: "rpc_controls", Label: "On-chain controls (configured RPCs)", License: "public chain state"},
	{ID: "uniswap_rpc", Label: "Uniswap v3 pool (RPC)", License: "public chain state"},
	{ID: "chainlink_rpc", Label: "Chainlink feeds (RPC)", License: "public chain state"},
}

func stableProviderMeta(id string) btcProviderDef {
	for _, p := range stableProviderRegistry {
		if p.ID == id {
			return p
		}
	}
	return btcProviderDef{ID: id, Label: id}
}

// stableChainKeyMap maps each source's chain names onto BigLens network ids
// for the six chains with on-chain infrastructure; other chains keep a slug
// of the source's own name.
var stableChainKeyMap = map[string]map[string]string{
	"circle":    {"ETH": "eth", "ARB": "arb", "OP": "op", "BASE": "base", "SOL": "sol", "TRX": "tron"},
	"tether":    {"Ethereum": "eth", "Tron": "tron", "Solana": "sol"},
	"defillama": {"Ethereum": "eth", "Tron": "tron", "Solana": "sol", "Arbitrum": "arb", "OP Mainnet": "op", "Optimism": "op", "Base": "base"},
}

func stableChainKey(source, name string) string {
	if k, ok := stableChainKeyMap[source][name]; ok {
		return k
	}
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// stableOnchainKey is a registry deployment's chain key: the network for the
// native tier, network + ".bridged" for a bridged representation.
func stableOnchainKey(tok registryToken) string {
	if tok.Tier == tierBridged {
		return tok.Network + ".bridged"
	}
	return tok.Network
}

// stableOnchainReading is the latest on-chain total of one deployment.
type stableOnchainReading struct {
	Token        registryToken
	SupplyNative float64
	ReadAt       time.Time
	Stale        bool   // the latest read failed; this is the previous good one
	Code         string // error code of the latest read when Stale
}

// stableSupplyInputs is everything one computation of the supply view uses.
type stableSupplyInputs struct {
	Llama       stableResult[llamaStableSnapshot]
	Circle      stableResult[circleSnapshot]
	Tether      stableResult[tetherSnapshot]
	Onchain     map[string]stableOnchainReading // by network|contract
	OnchainCode string                          // "disabled", or the error of a read with no usable result
}

// --- payload types ---

type StableSupplyRow struct {
	Chain                       string   `json:"chain"`
	ChainLabel                  string   `json:"chain_label"`
	Tier                        string   `json:"tier,omitempty"` // native | bridged (registry deployments)
	TokenLabel                  string   `json:"token_label,omitempty"`
	Verified                    bool     `json:"verified"` // BigLens reads this deployment on-chain
	IssuerCirculatingNative     *float64 `json:"issuer_circulating_native"`
	IssuerUnissuedNative        *float64 `json:"issuer_unissued_native,omitempty"`    // Tether notIssued
	IssuerQuarantinedNative     *float64 `json:"issuer_quarantined_native,omitempty"` // Tether quarantined
	IssuerAsOf                  string   `json:"issuer_as_of,omitempty"`
	IssuerSharePct              *float64 `json:"issuer_share_pct"`
	OnchainTotalNative          *float64 `json:"onchain_total_native"`
	OnchainAsOf                 string   `json:"onchain_as_of,omitempty"`
	OnchainStale                bool     `json:"onchain_stale,omitempty"`
	InventoryNative             *float64 `json:"inventory_native"` // on-chain − issuer, when both exist
	AggregatorCirculatingNative *float64 `json:"aggregator_circulating_native"`
	Change1dPct                 *float64 `json:"change_1d_pct"`
	Change7dPct                 *float64 `json:"change_7d_pct"`
	Change30dPct                *float64 `json:"change_30d_pct"`
	ChangeSource                string   `json:"change_source,omitempty"` // aggregator
}

type StableSupplyTotals struct {
	IssuerCirculatingNative     *float64 `json:"issuer_circulating_native"`
	IssuerAsOf                  string   `json:"issuer_as_of,omitempty"`
	AggregatorCirculatingNative *float64 `json:"aggregator_circulating_native"`
	AggregatorCirculatingUSD    *float64 `json:"aggregator_circulating_usd"`
	AggregatorAsOf              string   `json:"aggregator_as_of,omitempty"`
	InventoryNative             *float64 `json:"inventory_native"` // Σ over chains with both values
	InventoryChains             int      `json:"inventory_chains"`
}

type StableNetIssuance struct {
	Window    string   `json:"window"` // 24h | 7d | 30d
	NetNative *float64 `json:"net_native"`
	NetPct    *float64 `json:"net_pct"`
	Source    string   `json:"source"`         // issuer | aggregator | "" (unknown)
	From      string   `json:"from,omitempty"` // observation time of C(t−Δ) (issuer only)
	To        string   `json:"to,omitempty"`   // observation time of C(t)
}

type StableReconAggregator struct {
	IssuerNative     *float64 `json:"issuer_native"`
	AggregatorNative *float64 `json:"aggregator_native"`
	DiffPct          *float64 `json:"diff_pct"` // (aggregator − issuer) / issuer × 100
	IssuerAsOf       string   `json:"issuer_as_of,omitempty"`
	AggregatorAsOf   string   `json:"aggregator_as_of,omitempty"`
	SkewSeconds      *int64   `json:"skew_seconds"`
	WatchPct         float64  `json:"watch_pct"`
	WarningPct       float64  `json:"warning_pct"`
	AboveWatch       bool     `json:"above_watch"`
	AboveWarning     bool     `json:"above_warning"`
	SkipReason       string   `json:"skip_reason,omitempty"`
}

type StableReconOnchain struct {
	Chain              string   `json:"chain"`
	IssuerNative       *float64 `json:"issuer_native"`
	OnchainNative      *float64 `json:"onchain_native"`
	ExcessNative       *float64 `json:"excess_native"` // issuer − on-chain; > 0: the issuer reports more than exists
	ToleranceNative    *float64 `json:"tolerance_native"`
	IssuerAsOf         string   `json:"issuer_as_of,omitempty"`
	OnchainAsOf        string   `json:"onchain_as_of,omitempty"`
	SkewSeconds        *int64   `json:"skew_seconds"`
	Aligned            bool     `json:"aligned"`
	ExceedsTolerance   bool     `json:"exceeds_tolerance"`
	ConsecutiveSamples int      `json:"consecutive_samples"`   // hourly samples in a row over the tolerance
	SkipReason         string   `json:"skip_reason,omitempty"` // not_aligned | missing_onchain
}

type StableReconcile struct {
	Aggregator  StableReconAggregator `json:"aggregator"`
	Onchain     []StableReconOnchain  `json:"onchain"`
	OnchainAsOf string                `json:"onchain_as_of,omitempty"`
}

type StableSupplyDay struct {
	Day                         string   `json:"day"`
	IssuerCirculatingNative     *float64 `json:"issuer_circulating_native"`
	AggregatorCirculatingNative *float64 `json:"aggregator_circulating_native"`
	AggregatorCirculatingUSD    *float64 `json:"aggregator_circulating_usd"`
	AggregatorChangeNative      *float64 `json:"aggregator_change_native"` // vs the previous day, when both exist
}

type StableSupplyAsset struct {
	Asset       string              `json:"asset"`
	Symbol      string              `json:"symbol"`
	Numeraire   string              `json:"numeraire"`
	Totals      StableSupplyTotals  `json:"totals"`
	Chains      []StableSupplyRow   `json:"chains"`
	NetIssuance []StableNetIssuance `json:"net_issuance"`
	Reconcile   StableReconcile     `json:"reconcile"`
	History     []StableSupplyDay   `json:"history"`
	Sources     []BtcSourceStatus   `json:"sources"`

	issuerAt time.Time
}

// stableSupplyState is the sampler's latest view.
type stableSupplyState struct {
	AsOf           time.Time
	Assets         map[string]StableSupplyAsset
	MarketTotalUSD *float64
	MarketAsOf     time.Time
}

// --- pure computation ---

func stableFmt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func stableF(v float64) *float64 { return &v }

func stablePct(cur, prev *float64) *float64 {
	if cur == nil || prev == nil || !(*prev > 0) {
		return nil
	}
	return stableF((*cur - *prev) / *prev * 100)
}

// stableToleranceNative is the S12 on-chain tolerance: max(0.1%, 1M units).
func stableToleranceNative(issuer float64) float64 {
	return math.Max(stableReconOnchainRelTolerance*issuer, stableReconOnchainAbsToleranceN)
}

func stableSourceStatus[T any](id string, r stableResult[T]) BtcSourceStatus {
	meta := stableProviderMeta(id)
	st := BtcSourceStatus{ID: id, Label: meta.Label, License: meta.License, AsOf: stableFmt(r.AsOf)}
	switch {
	case r.Code == "disabled":
		st.Status, st.AsOf = "disabled", ""
	case r.Has && !r.Stale:
		st.Status = "ok"
	case r.Has:
		st.Status, st.Error = "stale", r.Code
	default:
		st.Status, st.Error = "error", r.Code
		if st.Error == "" {
			st.Error = "no_data"
		}
	}
	return st
}

// buildStableSupplyAsset assembles one asset's chain table, totals,
// aggregator reconciliation and source badges (§5.1). Net issuance and the
// on-chain reconciliation are added by the sampler.
func buildStableSupplyAsset(def stableAssetDef, in stableSupplyInputs) StableSupplyAsset {
	a := StableSupplyAsset{Asset: def.ID, Symbol: def.Symbol, Numeraire: def.Numeraire,
		Chains: []StableSupplyRow{}, NetIssuance: []StableNetIssuance{}, History: []StableSupplyDay{},
		Reconcile: StableReconcile{Onchain: []StableReconOnchain{}}}
	rows := map[string]*StableSupplyRow{}
	row := func(key, label string) *StableSupplyRow {
		r, ok := rows[key]
		if !ok {
			r = &StableSupplyRow{Chain: key, ChainLabel: label}
			if info, mapped := chains[strings.TrimSuffix(key, ".bridged")]; mapped && !strings.HasSuffix(key, ".bridged") {
				r.ChainLabel = info.Label
			}
			rows[key] = r
		}
		return r
	}
	// Deployments BigLens reads on-chain.
	for _, tok := range registryTokens {
		if tok.Asset != def.Symbol {
			continue
		}
		key := stableOnchainKey(tok)
		label := chains[tok.Network].Label
		if tok.Tier == tierBridged {
			label += " (" + tok.Label + ")"
		}
		r := row(key, label)
		r.Verified, r.Tier, r.TokenLabel = true, string(tok.Tier), tok.Label
		if rd, ok := in.Onchain[tok.Network+"|"+tok.Contract]; ok {
			r.OnchainTotalNative = stableF(rd.SupplyNative)
			r.OnchainAsOf = stableFmt(rd.ReadAt)
			r.OnchainStale = rd.Stale
		}
	}
	// Issuer circulating.
	switch def.Issuer {
	case "circle":
		if in.Circle.Has {
			if coin, ok := in.Circle.Value.Coins[def.Symbol]; ok {
				var sum float64
				for _, c := range coin.Chains {
					r := row(stableChainKey("circle", c.Chain), c.Chain)
					r.IssuerCirculatingNative = stableF(c.AmountNative)
					r.IssuerAsOf = stableFmt(c.UpdatedAt)
					sum += c.AmountNative
				}
				total := coin.TotalNative
				if total == nil {
					total = stableF(sum)
				}
				a.Totals.IssuerCirculatingNative, a.issuerAt = total, coin.UpdatedAt
			}
		}
	case "tether":
		if in.Tether.Has {
			tz := in.Tether.Value
			for _, c := range tz.Chains {
				r := row(stableChainKey("tether", c.Name), c.Name)
				r.IssuerCirculatingNative = stableF(c.CirculatingNative)
				r.IssuerUnissuedNative = stableF(c.NotIssuedNative)
				r.IssuerQuarantinedNative = stableF(c.QuarantinedNative)
				r.IssuerAsOf = stableFmt(tz.UpdatedAt)
			}
			a.Totals.IssuerCirculatingNative, a.issuerAt = stableF(tz.CirculatingSumNative), tz.UpdatedAt
		}
	}
	a.Totals.IssuerAsOf = stableFmt(a.issuerAt)
	// Aggregator (DefiLlama, normalized to native units).
	var llama *llamaAsset
	if in.Llama.Has {
		if la, ok := in.Llama.Value.core(def.ID); ok {
			llama = &la
			a.Totals.AggregatorCirculatingNative = la.CirculatingNative
			a.Totals.AggregatorCirculatingUSD = la.CirculatingUSD
			a.Totals.AggregatorAsOf = stableFmt(in.Llama.AsOf)
			for _, c := range la.Chains {
				r := row(stableChainKey("defillama", c.Chain), c.Chain)
				r.AggregatorCirculatingNative = c.CurrentNative
				r.Change1dPct = stablePct(c.CurrentNative, c.PrevDayNative)
				r.Change7dPct = stablePct(c.CurrentNative, c.PrevWeekNative)
				r.Change30dPct = stablePct(c.CurrentNative, c.PrevMonthNative)
				if r.Change1dPct != nil || r.Change7dPct != nil || r.Change30dPct != nil {
					r.ChangeSource = "aggregator"
				}
			}
		}
	}
	// Inventory and shares.
	var inv float64
	for _, r := range rows {
		if r.Tier == string(tierNative) && r.IssuerCirculatingNative != nil && r.OnchainTotalNative != nil {
			r.InventoryNative = stableF(*r.OnchainTotalNative - *r.IssuerCirculatingNative)
			inv += *r.InventoryNative
			a.Totals.InventoryChains++
		}
		if r.IssuerCirculatingNative != nil && a.Totals.IssuerCirculatingNative != nil && *a.Totals.IssuerCirculatingNative > 0 {
			r.IssuerSharePct = stableF(*r.IssuerCirculatingNative / *a.Totals.IssuerCirculatingNative * 100)
		}
	}
	if a.Totals.InventoryChains > 0 {
		a.Totals.InventoryNative = stableF(inv)
	}
	for _, r := range rows {
		a.Chains = append(a.Chains, *r)
	}
	sort.Slice(a.Chains, func(i, j int) bool {
		x, y := a.Chains[i], a.Chains[j]
		for _, pair := range [][2]*float64{{x.IssuerCirculatingNative, y.IssuerCirculatingNative},
			{x.AggregatorCirculatingNative, y.AggregatorCirculatingNative}, {x.OnchainTotalNative, y.OnchainTotalNative}} {
			if (pair[0] == nil) != (pair[1] == nil) {
				return pair[0] != nil
			}
			if pair[0] != nil && *pair[0] != *pair[1] {
				return *pair[0] > *pair[1]
			}
		}
		return x.Chain < y.Chain
	})
	// Aggregator reconciliation: totals only (per-chain aggregator values
	// differ by bridge accounting, §5.1).
	ra := StableReconAggregator{IssuerNative: a.Totals.IssuerCirculatingNative, IssuerAsOf: a.Totals.IssuerAsOf,
		WatchPct: stableReconAggWatchPct, WarningPct: stableReconAggWarningPct}
	if llama != nil {
		ra.AggregatorNative, ra.AggregatorAsOf = llama.CirculatingNative, a.Totals.AggregatorAsOf
	}
	switch {
	case ra.IssuerNative == nil || !(*ra.IssuerNative > 0):
		ra.SkipReason = "missing_issuer"
	case ra.AggregatorNative == nil:
		ra.SkipReason = "missing_aggregator"
	default:
		ra.DiffPct = stableF((*ra.AggregatorNative - *ra.IssuerNative) / *ra.IssuerNative * 100)
		ra.AboveWatch = math.Abs(*ra.DiffPct) > stableReconAggWatchPct
		ra.AboveWarning = math.Abs(*ra.DiffPct) > stableReconAggWarningPct
		skew := int64(math.Abs(in.Llama.AsOf.Sub(a.issuerAt).Seconds()))
		ra.SkewSeconds = &skew
	}
	a.Reconcile.Aggregator = ra
	// Source badges.
	a.Sources = append(a.Sources, func() BtcSourceStatus {
		if def.Issuer == "tether" {
			return stableSourceStatus("tether", in.Tether)
		}
		return stableSourceStatus("circle", in.Circle)
	}(), stableSourceStatus("defillama", in.Llama), stableOnchainSourceStatus(def, in))
	return a
}

func stableOnchainSourceStatus(def stableAssetDef, in stableSupplyInputs) BtcSourceStatus {
	meta := stableProviderMeta("rpc_supply")
	st := BtcSourceStatus{ID: "rpc_supply", Label: meta.Label, License: meta.License}
	if in.OnchainCode == "disabled" {
		st.Status = "disabled"
		return st
	}
	var newest time.Time
	stale, seen := "", 0
	for _, rd := range in.Onchain {
		if rd.Token.Asset != def.Symbol {
			continue
		}
		seen++
		if rd.Stale && stale == "" {
			stale = rd.Code
		}
		if rd.ReadAt.After(newest) {
			newest = rd.ReadAt
		}
	}
	switch {
	case seen == 0:
		st.Status, st.Error = "error", in.OnchainCode
		if st.Error == "" {
			st.Error = "no_data"
		}
	case stale != "":
		st.Status, st.Error, st.AsOf = "stale", stale, stableFmt(newest)
	default:
		st.Status, st.AsOf = "ok", stableFmt(newest)
	}
	return st
}

// stableOnchainRecon compares issuer circulating with the on-chain total on
// every issuer-reported chain that maps to a native registry deployment
// (S12). streak counts consecutive hourly samples over the tolerance per
// asset|chain; a skipped or clean sample resets it.
func stableOnchainRecon(def stableAssetDef, in stableSupplyInputs, streak map[string]int) []StableReconOnchain {
	type issuerObs struct {
		amount float64
		at     time.Time
	}
	issuer := map[string]issuerObs{}
	switch def.Issuer {
	case "circle":
		if in.Circle.Has {
			for _, c := range in.Circle.Value.Coins[def.Symbol].Chains {
				issuer[stableChainKey("circle", c.Chain)] = issuerObs{c.AmountNative, c.UpdatedAt}
			}
		}
	case "tether":
		if in.Tether.Has {
			for _, c := range in.Tether.Value.Chains {
				issuer[stableChainKey("tether", c.Name)] = issuerObs{c.CirculatingNative, in.Tether.Value.UpdatedAt}
			}
		}
	}
	out := []StableReconOnchain{}
	for _, tok := range registryTokens {
		if tok.Asset != def.Symbol || tok.Tier != tierNative {
			continue
		}
		obs, ok := issuer[tok.Network]
		if !ok {
			continue
		}
		key := def.ID + "|" + tok.Network
		r := StableReconOnchain{Chain: tok.Network, IssuerNative: stableF(obs.amount), IssuerAsOf: stableFmt(obs.at),
			ToleranceNative: stableF(stableToleranceNative(obs.amount))}
		rd, has := in.Onchain[tok.Network+"|"+tok.Contract]
		if !has {
			r.SkipReason = "missing_onchain"
			streak[key] = 0
			out = append(out, r)
			continue
		}
		r.OnchainNative, r.OnchainAsOf = stableF(rd.SupplyNative), stableFmt(rd.ReadAt)
		skew := rd.ReadAt.Sub(obs.at)
		if skew < 0 {
			skew = -skew
		}
		secs := int64(skew.Seconds())
		r.SkewSeconds = &secs
		r.ExcessNative = stableF(obs.amount - rd.SupplyNative)
		if skew > stableAlignWindow || rd.Stale {
			r.SkipReason = "not_aligned"
			streak[key] = 0
			out = append(out, r)
			continue
		}
		r.Aligned = true
		if *r.ExcessNative > *r.ToleranceNative {
			r.ExceedsTolerance = true
			streak[key]++
		} else {
			streak[key] = 0
		}
		r.ConsecutiveSamples = streak[key]
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Chain < out[j].Chain })
	return out
}

// stableNetIssuance computes net issuance over 24h/7d/30d (§5.4): issuer
// circulating C(t) − C(t−Δ) when a stored issuer total lies within ±4 h of
// t−Δ, else DefiLlama's prev values (source "aggregator", §7.4), else null.
func stableNetIssuance(ctx context.Context, store *riskStore, asset string, issuerNow *float64, issuerAt time.Time,
	llama *llamaAsset, llamaAt time.Time) []StableNetIssuance {
	out := make([]StableNetIssuance, 0, len(stableNetIssuanceWindows))
	for _, w := range stableNetIssuanceWindows {
		n := StableNetIssuance{Window: w.Label}
		if store != nil && issuerNow != nil && !issuerAt.IsZero() {
			past, err := store.stableSupplyNear(ctx, asset, "issuer", "all", issuerAt.Add(-w.D), stableNetIssuanceTol)
			if err != nil {
				slog.Warn("stablecoin net issuance lookup", "asset", asset, "window", w.Label, "error", err)
			}
			if past != nil && past.CirculatingNative != nil && *past.CirculatingNative > 0 {
				n.NetNative = stableF(*issuerNow - *past.CirculatingNative)
				n.NetPct = stableF(*n.NetNative / *past.CirculatingNative * 100)
				n.Source, n.From, n.To = "issuer", stableFmt(past.TS), stableFmt(issuerAt)
				out = append(out, n)
				continue
			}
		}
		if llama != nil {
			var prev *float64
			switch w.Label {
			case "24h":
				prev = llama.PrevDayNative
			case "7d":
				prev = llama.PrevWeekNative
			case "30d":
				prev = llama.PrevMonthNative
			}
			if llama.CirculatingNative != nil && prev != nil && *prev > 0 {
				n.NetNative = stableF(*llama.CirculatingNative - *prev)
				n.NetPct = stableF(*n.NetNative / *prev * 100)
				n.Source, n.To = "aggregator", stableFmt(llamaAt)
			}
		}
		out = append(out, n)
	}
	return out
}

// --- sampler ---

type stableSupplySampler struct {
	settings stableSettings
	store    *riskStore
	ep       stableEndpoints
	rpcs     func(network string) []string
	now      func() time.Time
	// onFetch reports every upstream call (provider health, §8.7); may be nil.
	onFetch func(provider string, took time.Duration, code string)

	llama  stableLastGood[llamaStableSnapshot]
	circle stableLastGood[circleSnapshot]
	tether stableLastGood[tetherSnapshot]

	mu          sync.Mutex
	tetherRes   stableResult[tetherSnapshot]
	tetherAt    time.Time
	onchain     map[string]stableOnchainReading
	onchainCode string
	rpcAt       time.Time
	chartsDue   time.Time
	ratesDue    time.Time
	streak      map[string]int
	recon       map[string][]StableReconOnchain
	reconAsOf   time.Time
	state       *stableSupplyState
}

func newStableSupplySampler(settings stableSettings, store *riskStore, ep stableEndpoints, rpcs func(string) []string, now func() time.Time) *stableSupplySampler {
	return &stableSupplySampler{settings: settings, store: store, ep: ep, rpcs: rpcs, now: now,
		onchain: map[string]stableOnchainReading{}, streak: map[string]int{}, recon: map[string][]StableReconOnchain{}}
}

func (s *stableSupplySampler) enabled(key string) bool { return s.settings.Providers[key] }

func (s *stableSupplySampler) observe(provider string, start time.Time, code string) {
	if s.onFetch != nil {
		s.onFetch(provider, time.Since(start), code)
	}
}

// snapshot returns the latest view (nil before the first tick).
func (s *stableSupplySampler) snapshot() *stableSupplyState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// tick runs one supply sample: fetch what is due, store it, recompute the
// view. It reports whether every due upstream call succeeded.
func (s *stableSupplySampler) tick(ctx context.Context) bool {
	now := s.now().UTC()
	in, rpcFresh, ok := s.collect(ctx, now)
	s.persist(ctx, in)
	s.compute(ctx, in, rpcFresh, now)
	return ok
}

func (s *stableSupplySampler) collect(ctx context.Context, now time.Time) (stableSupplyInputs, bool, bool) {
	var in stableSupplyInputs
	var g errgroup.Group
	g.Go(func() error {
		if !s.enabled("defillama") {
			in.Llama = stableResult[llamaStableSnapshot]{Code: "disabled"}
			return nil
		}
		t0 := time.Now()
		v, code := fetchLlamaStablecoins(ctx, s.ep, now)
		s.observe("defillama", t0, code)
		in.Llama = s.llama.resolve(v, code, now)
		return nil
	})
	g.Go(func() error {
		if !s.enabled("circle") {
			in.Circle = stableResult[circleSnapshot]{Code: "disabled"}
			return nil
		}
		t0 := time.Now()
		v, code := fetchCircleStablecoins(ctx, s.ep, now)
		s.observe("circle", t0, code)
		in.Circle = s.circle.resolve(v, code, now)
		return nil
	})
	s.mu.Lock()
	tetherDue := s.enabled("tether") && (s.tetherAt.IsZero() || now.Sub(s.tetherAt) >= stableTetherEvery)
	rpcDue := s.enabled("rpc_supply") && s.rpcs != nil && (s.rpcAt.IsZero() || now.Sub(s.rpcAt) >= stableRPCEvery)
	if tetherDue {
		s.tetherAt = now
	}
	if rpcDue {
		s.rpcAt = now
	}
	s.mu.Unlock()
	var tetherRes stableResult[tetherSnapshot]
	if tetherDue {
		g.Go(func() error {
			t0 := time.Now()
			v, code := fetchTetherTransparency(ctx, s.ep, now)
			s.observe("tether", t0, code)
			tetherRes = s.tether.resolve(v, code, now)
			return nil
		})
	}
	var readings []registrySupplyReading
	if rpcDue {
		g.Go(func() error {
			var toks []registryToken
			for _, tok := range registryTokens {
				for _, def := range stableCoreAssets {
					if tok.Asset == def.Symbol {
						toks = append(toks, tok)
					}
				}
			}
			t0 := time.Now()
			readings = fetchRegistrySupplies(ctx, toks, s.rpcs, s.now)
			code := ""
			for _, rd := range readings {
				if rd.Code != "" {
					code = rd.Code
				}
			}
			s.observe("rpc_supply", t0, code)
			return nil
		})
	}
	_ = g.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.enabled("tether"):
		s.tetherRes = stableResult[tetherSnapshot]{Code: "disabled"}
	case tetherDue:
		s.tetherRes = tetherRes
	}
	in.Tether = s.tetherRes
	switch {
	case !s.enabled("rpc_supply"):
		s.onchain, s.onchainCode = map[string]stableOnchainReading{}, "disabled"
	case rpcDue:
		next := map[string]stableOnchainReading{}
		lastCode := ""
		for _, rd := range readings {
			key := rd.Token.Network + "|" + rd.Token.Contract
			if rd.Code == "" {
				next[key] = stableOnchainReading{Token: rd.Token, SupplyNative: rd.Supply, ReadAt: rd.ReadAt}
				continue
			}
			lastCode = rd.Code
			if old, ok := s.onchain[key]; ok {
				old.Stale, old.Code = true, rd.Code
				next[key] = old
			}
		}
		s.onchain, s.onchainCode = next, lastCode
	}
	in.Onchain = make(map[string]stableOnchainReading, len(s.onchain))
	for k, v := range s.onchain {
		in.Onchain[k] = v
	}
	in.OnchainCode = s.onchainCode
	ok := (!s.enabled("defillama") || in.Llama.Code == "") && (!s.enabled("circle") || in.Circle.Code == "") &&
		(!tetherDue || tetherRes.Code == "") && (!rpcDue || s.onchainCode == "")
	return in, rpcDue, ok
}

// persist stores at most one observation per source per UTC hour, plus the
// daily rollups and the Tether update (idempotent by update id).
func (s *stableSupplySampler) persist(ctx context.Context, in stableSupplyInputs) {
	if s.store == nil {
		return
	}
	var rows []stableSupplySnapshot
	var daily []stableSupplyDaily
	var chainDaily []stableChainDaily
	add := func(asset, source string, at time.Time, batch []stableSupplySnapshot) {
		if len(batch) == 0 || at.IsZero() {
			return
		}
		has, err := s.store.hasStableSnapshotInHour(ctx, asset, source, at)
		if err != nil {
			slog.Warn("stablecoin supply persist", "asset", asset, "source", source, "error", err)
			return
		}
		if has {
			return
		}
		rows = append(rows, batch...)
		for _, r := range batch {
			day := r.TS.UTC().Format(time.DateOnly)
			if r.Chain == "all" {
				if source == "issuer" {
					daily = append(daily, stableSupplyDaily{Day: day, Asset: asset, Source: source, CirculatingNative: r.CirculatingNative, UpdatedAt: r.TS})
				}
				continue
			}
			v := r.CirculatingNative
			if source == "onchain" {
				v = r.OnchainTotalNative
			}
			chainDaily = append(chainDaily, stableChainDaily{Day: day, Asset: asset, Chain: r.Chain, Source: source, CirculatingNative: v, UpdatedAt: r.TS})
		}
	}
	for _, def := range stableCoreAssets {
		// Issuer.
		switch {
		case def.Issuer == "circle" && in.Circle.Has:
			if coin, ok := in.Circle.Value.Coins[def.Symbol]; ok {
				fetched := in.Circle.Value.FetchedAt
				var batch []stableSupplySnapshot
				var sum float64
				for _, c := range coin.Chains {
					batch = append(batch, stableSupplySnapshot{TS: c.UpdatedAt.Truncate(time.Second), Asset: def.ID,
						Chain: stableChainKey("circle", c.Chain), Source: "issuer", CirculatingNative: stableF(c.AmountNative), FetchedAt: fetched})
					sum += c.AmountNative
				}
				total := coin.TotalNative
				if total == nil {
					total = stableF(sum)
				}
				batch = append(batch, stableSupplySnapshot{TS: coin.UpdatedAt.Truncate(time.Second), Asset: def.ID, Chain: "all",
					Source: "issuer", CirculatingNative: total, FetchedAt: fetched})
				add(def.ID, "issuer", coin.UpdatedAt, batch)
			}
		case def.Issuer == "tether" && in.Tether.Has:
			tz := in.Tether.Value
			chainsJSON := tetherChainsJSON(tz)
			if err := s.store.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: tz.UpdateID,
				TotalAssetsUSD: tz.TotalAssetsUSD, TotalLiabilitiesUSD: tz.TotalLiabilitiesUSD,
				ShareholderEquityUSD: tz.ShareholderEquityUSD, ChainsJSON: chainsJSON, FetchedAt: tz.FetchedAt}); err != nil {
				slog.Warn("stablecoin tether snapshot", "error", err)
			}
			var batch []stableSupplySnapshot
			for _, c := range tz.Chains {
				batch = append(batch, stableSupplySnapshot{TS: tz.UpdatedAt, Asset: def.ID, Chain: stableChainKey("tether", c.Name),
					Source: "issuer", CirculatingNative: stableF(c.CirculatingNative), UnissuedNative: stableF(c.NotIssuedNative),
					QuarantinedNative: stableF(c.QuarantinedNative), FetchedAt: tz.FetchedAt})
			}
			batch = append(batch, stableSupplySnapshot{TS: tz.UpdatedAt, Asset: def.ID, Chain: "all", Source: "issuer",
				CirculatingNative: stableF(tz.CirculatingSumNative), FetchedAt: tz.FetchedAt})
			add(def.ID, "issuer", tz.UpdatedAt, batch)
		}
		// On-chain (fresh readings only).
		var batch []stableSupplySnapshot
		var newest time.Time
		for _, rd := range in.Onchain {
			if rd.Token.Asset != def.Symbol || rd.Stale {
				continue
			}
			batch = append(batch, stableSupplySnapshot{TS: rd.ReadAt.Truncate(time.Second), Asset: def.ID, Chain: stableOnchainKey(rd.Token),
				Source: "onchain", OnchainTotalNative: stableF(rd.SupplyNative), FetchedAt: rd.ReadAt})
			if rd.ReadAt.After(newest) {
				newest = rd.ReadAt
			}
		}
		add(def.ID, "onchain", newest, batch)
		// Aggregator (fresh fetch only): the total and the monitored chains.
		if in.Llama.Has && !in.Llama.Stale {
			if la, ok := in.Llama.Value.core(def.ID); ok {
				at := in.Llama.Value.FetchedAt.Truncate(time.Second)
				agg := []stableSupplySnapshot{{TS: at, Asset: def.ID, Chain: "all", Source: "aggregator",
					CirculatingNative: la.CirculatingNative, FetchedAt: at}}
				for _, c := range la.Chains {
					key := stableChainKey("defillama", c.Chain)
					if _, monitored := chains[key]; !monitored {
						continue
					}
					agg = append(agg, stableSupplySnapshot{TS: at, Asset: def.ID, Chain: key, Source: "aggregator",
						CirculatingNative: c.CurrentNative, FetchedAt: at})
				}
				add(def.ID, "aggregator", at, agg)
			}
		}
	}
	if err := s.store.insertStableSupplySnapshots(ctx, rows); err != nil {
		slog.Warn("stablecoin supply snapshots", "error", err)
		return
	}
	if err := s.store.upsertStableSupplyDaily(ctx, daily); err != nil {
		slog.Warn("stablecoin supply daily", "error", err)
	}
	if err := s.store.upsertStableChainDaily(ctx, chainDaily); err != nil {
		slog.Warn("stablecoin chain daily", "error", err)
	}
}

// tetherChainsJSON serializes the per-chain breakdown kept with each Tether
// update (stable_tether_snapshots.chains_json); units are native tokens.
func tetherChainsJSON(tz tetherSnapshot) string {
	type chainJSON struct {
		Name                  string  `json:"name"`
		TotalAuthorizedNative float64 `json:"total_authorized_native"`
		NotIssuedNative       float64 `json:"not_issued_native"`
		QuarantinedNative     float64 `json:"quarantined_native"`
		CirculatingNative     float64 `json:"circulating_native"`
	}
	out := make([]chainJSON, 0, len(tz.Chains))
	for _, c := range tz.Chains {
		out = append(out, chainJSON{Name: c.Name, TotalAuthorizedNative: c.TotalAuthorizedNative, NotIssuedNative: c.NotIssuedNative,
			QuarantinedNative: c.QuarantinedNative, CirculatingNative: c.CirculatingNative})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *stableSupplySampler) compute(ctx context.Context, in stableSupplyInputs, rpcFresh bool, now time.Time) {
	state := &stableSupplyState{AsOf: now, Assets: map[string]StableSupplyAsset{}}
	if in.Llama.Has {
		var total float64
		for _, a := range in.Llama.Value.Assets {
			if a.CirculatingUSD != nil {
				total += *a.CirculatingUSD
			}
		}
		state.MarketTotalUSD, state.MarketAsOf = stableF(total), in.Llama.AsOf
	}
	s.mu.Lock()
	if rpcFresh {
		for _, def := range stableCoreAssets {
			s.recon[def.ID] = stableOnchainRecon(def, in, s.streak)
		}
		s.reconAsOf = now
	}
	recon := make(map[string][]StableReconOnchain, len(s.recon))
	for k, v := range s.recon {
		recon[k] = v
	}
	reconAsOf := s.reconAsOf
	s.mu.Unlock()
	for _, def := range stableCoreAssets {
		a := buildStableSupplyAsset(def, in)
		if r, ok := recon[def.ID]; ok {
			a.Reconcile.Onchain, a.Reconcile.OnchainAsOf = r, stableFmt(reconAsOf)
		}
		var llama *llamaAsset
		if in.Llama.Has {
			if la, ok := in.Llama.Value.core(def.ID); ok {
				llama = &la
			}
		}
		a.NetIssuance = stableNetIssuance(ctx, s.store, def.ID, a.Totals.IssuerCirculatingNative, a.issuerAt, llama, in.Llama.AsOf)
		state.Assets[def.ID] = a
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

// syncHistory refreshes the DefiLlama daily supply history every 6 h
// (stable_supply_daily, source "aggregator"): totalCirculating is native,
// totalCirculatingUSD is USD (§5.1).
func (s *stableSupplySampler) syncHistory(ctx context.Context) (ran, ok bool) {
	now := s.now().UTC()
	s.mu.Lock()
	due := s.enabled("defillama") && !now.Before(s.chartsDue)
	s.mu.Unlock()
	if !due || s.store == nil {
		return false, false
	}
	ok = true
	for _, def := range stableCoreAssets {
		t0 := time.Now()
		rows, code := fetchLlamaChart(ctx, s.ep, def.LlamaID, def.LlamaPeg)
		s.observe("defillama", t0, code)
		if code != "" {
			ok = false
			slog.Warn("stablecoin supply history", "asset", def.ID, "error", code)
			continue
		}
		daily := make([]stableSupplyDaily, 0, len(rows))
		for _, r := range rows {
			at, err := time.Parse(time.DateOnly, r.Day)
			if err != nil {
				continue
			}
			daily = append(daily, stableSupplyDaily{Day: r.Day, Asset: def.ID, Source: "aggregator",
				CirculatingNative: r.CirculatingNative, CirculatingUSD: r.CirculatingUSD, UpdatedAt: at})
		}
		if err := s.store.upsertStableSupplyDaily(ctx, daily); err != nil {
			ok = false
			slog.Warn("stablecoin supply history store", "asset", def.ID, "error", err)
		}
	}
	s.mu.Lock()
	if ok {
		s.chartsDue = now.Add(stableChartsEvery)
	} else {
		s.chartsDue = now.Add(stableRetryAfterFail)
	}
	s.mu.Unlock()
	return true, ok
}

func (s *stableSupplySampler) historyNextDue() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chartsDue
}

func (s *stableSupplySampler) ratesNextDue() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ratesDue
}

// syncRates stores the ECB EUR/USD and €STR fixings and the 13-week T-bill
// yield once a day (stable_rates_daily).
func (s *stableSupplySampler) syncRates(ctx context.Context) (ran, ok bool) {
	now := s.now().UTC()
	s.mu.Lock()
	due := !now.Before(s.ratesDue)
	s.mu.Unlock()
	if !due || s.store == nil || (!s.enabled("ecb") && !s.enabled("us_treasury")) {
		return false, false
	}
	ok = true
	var rows []stableRateDaily
	addObs := func(series string, obs []stableRateObs) {
		for _, o := range obs {
			rows = append(rows, stableRateDaily{Day: o.Day, Series: series, Value: o.Value, FetchedAt: now})
		}
	}
	if s.enabled("ecb") {
		for _, f := range []struct {
			series string
			fetch  func(context.Context, stableEndpoints, int) ([]stableRateObs, string)
		}{{stableSeriesECBEURUSD, fetchECBEURUSD}, {stableSeriesECBESTRPct, fetchECBESTR}} {
			t0 := time.Now()
			obs, code := f.fetch(ctx, s.ep, 10)
			s.observe("ecb", t0, code)
			if code != "" {
				ok = false
				continue
			}
			addObs(f.series, obs)
		}
	}
	if s.enabled("us_treasury") {
		t0 := time.Now()
		obs, code := fetchTreasury13W(ctx, s.ep, now.Year())
		if code != "" && now.Month() == time.January {
			obs, code = fetchTreasury13W(ctx, s.ep, now.Year()-1) // the new year's file may still be empty
		}
		s.observe("us_treasury", t0, code)
		if code != "" {
			ok = false
		} else {
			addObs(stableSeriesUST13WPct, obs)
		}
	}
	if err := s.store.upsertStableRatesDaily(ctx, rows); err != nil {
		ok = false
		slog.Warn("stablecoin rates store", "error", err)
	}
	s.mu.Lock()
	if ok {
		s.ratesDue = now.Add(stableRatesEvery)
	} else {
		s.ratesDue = now.Add(stableRetryAfterFail)
	}
	s.mu.Unlock()
	return true, ok
}

// stableSupplyHistory merges the issuer and aggregator daily series from
// fromDay on, oldest first.
func stableSupplyHistory(ctx context.Context, store *riskStore, asset, fromDay string) ([]StableSupplyDay, error) {
	out := []StableSupplyDay{}
	if store == nil {
		return out, nil
	}
	issuer, err := store.stableSupplyDailySeries(ctx, asset, "issuer", fromDay)
	if err != nil {
		return nil, err
	}
	agg, err := store.stableSupplyDailySeries(ctx, asset, "aggregator", fromDay)
	if err != nil {
		return nil, err
	}
	byDay := map[string]*StableSupplyDay{}
	get := func(day string) *StableSupplyDay {
		if d, ok := byDay[day]; ok {
			return d
		}
		d := &StableSupplyDay{Day: day}
		byDay[day] = d
		return d
	}
	for _, r := range issuer {
		get(r.Day).IssuerCirculatingNative = r.CirculatingNative
	}
	for _, r := range agg {
		d := get(r.Day)
		d.AggregatorCirculatingNative, d.AggregatorCirculatingUSD = r.CirculatingNative, r.CirculatingUSD
	}
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	for i := 1; i < len(out); i++ {
		prev, cur := out[i-1], &out[i]
		pd, err1 := time.Parse(time.DateOnly, prev.Day)
		cd, err2 := time.Parse(time.DateOnly, cur.Day)
		if err1 == nil && err2 == nil && cd.Sub(pd) == 24*time.Hour &&
			prev.AggregatorCirculatingNative != nil && cur.AggregatorCirculatingNative != nil {
			cur.AggregatorChangeNative = stableF(*cur.AggregatorCirculatingNative - *prev.AggregatorCirculatingNative)
		}
	}
	return out, nil
}
