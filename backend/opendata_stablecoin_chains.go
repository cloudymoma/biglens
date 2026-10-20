package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// /chains (stablecoin.md §7.2 tab 5, §9): the chain table without holders
// (v1.1): tier, issuer supply and share, transfer cost, freeze mechanism,
// explorer link, and a Payment Check link for USDT and USDC. Served from the
// supply sampler's memory and the shared cache. Transfer costs are read from
// what the Gas page cached (gas-live); /chains never fetches them.

const stableChainsTTL = 10 * time.Minute

const stableChainsCostNote = "Transfer costs come from the Gas page's live cache, read on every controls run (15 min) and kept up to " +
	"24 h (stale after 1 h): BigLens prices USDT transfers on TRON and USDC transfers on Ethereum, Arbitrum, Optimism and Base. " +
	"Nothing is fetched for /chains; a cost the Gas page has not cached is not_cached. Other assets and chains are not priced."

type StableChainsResponse struct {
	AsOf      string             `json:"as_of,omitempty"` // the supply sample behind the issuer amounts
	WarmingUp bool               `json:"warming_up"`      // no supply sample yet
	Assets    []StableChainTable `json:"assets"`
	CostNote  string             `json:"cost_note"`
}

type StableChainTable struct {
	Asset                   string           `json:"asset"`
	Symbol                  string           `json:"symbol"`
	Numeraire               string           `json:"numeraire"`
	IssuerCirculatingNative *float64         `json:"issuer_circulating_native"`
	IssuerAsOf              string           `json:"issuer_as_of,omitempty"`
	Rows                    []StableChainRow `json:"rows"`
}

type StableChainRow struct {
	Chain      string `json:"chain"`
	ChainLabel string `json:"chain_label"`
	// Tier: native | bridged (registry deployments) | issuer (a chain the
	// issuer reports, outside the registry) | aggregator (only DefiLlama
	// reports it; native or bridged is not known).
	Tier                    string   `json:"tier"`
	TokenLabel              string   `json:"token_label,omitempty"`
	Verified                bool     `json:"verified"` // BigLens reads this deployment on-chain
	Contract                string   `json:"contract,omitempty"`
	IssuerCirculatingNative *float64 `json:"issuer_circulating_native"`
	IssuerSharePct          *float64 `json:"issuer_share_pct"`
	// AggregatorCirculatingNative is DefiLlama's amount, normalized to native
	// units (§5.1); the only amount on aggregator rows.
	AggregatorCirculatingNative *float64               `json:"aggregator_circulating_native"`
	FreezeMechanism             *StableFreezeMechanism `json:"freeze_mechanism"` // null when BigLens does not know it
	ExplorerURL                 string                 `json:"explorer_url,omitempty"`
	VerifyPayment               *StableVerifyLink      `json:"verify_payment"` // USDT and USDC on Payment Check networks
	TransferCost                *StableTransferCost    `json:"transfer_cost"`  // null where BigLens does not price transfers
}

type StableFreezeMechanism struct {
	Kind              string `json:"kind"`               // blacklist | blocklist | spl_freeze_authority
	Function          string `json:"function"`           // the check Address Risk and Payment Check run
	ControlsMonitored bool   `json:"controls_monitored"` // the controls sampler reads this deployment
}

// StableVerifyLink prefills Payment Check (Crypto Pulse, hash route).
type StableVerifyLink struct {
	Asset   string `json:"asset"`
	Network string `json:"network"`
	Hash    string `json:"hash"`
}

type StableTransferCost struct {
	// Status: ok (read from the gas-live cache within the hour) | stale (the
	// last good value, 1-24 h old) | not_cached (never filled, or older).
	Status     string   `json:"status"`
	Label      string   `json:"label"`
	Source     string   `json:"source"`
	NativeUnit string   `json:"native_unit"` // TRX | ETH
	Native     *float64 `json:"native"`
	USD        *float64 `json:"usd"`
	// USDNewAddress: TRON, a first transfer to an address without USDT.
	USDNewAddress *float64 `json:"usd_new_address,omitempty"`
	MeasuredAt    string   `json:"measured_at,omitempty"` // the calibration behind the transfer profile
	AsOf          string   `json:"as_of,omitempty"`       // when BigLens read the value from the gas-live cache
}

// stableFreezeKinds describes the registry freeze selectors.
var stableFreezeKinds = map[string]StableFreezeMechanism{
	"e47d6060":          {Kind: "blacklist", Function: "isBlackListed(address)"},
	"fe575a87":          {Kind: "blacklist", Function: "isBlacklisted(address)"},
	"fbac3951":          {Kind: "blocklist", Function: "isBlocked(address)"},
	"spl-account-state": {Kind: "spl_freeze_authority", Function: "SPL token account state (frozen)"},
}

// stableGasCosts holds the transfer costs readable from the gas-live cache.
type stableGasCosts struct {
	tronUSDT *StableTransferCost
	evmUSDC  map[string]*StableTransferCost
}

// The not_cached shapes of the two priced transfers.
func stableTronCostTemplate() *StableTransferCost {
	return &StableTransferCost{Status: "not_cached", Label: "USDT transfer to an existing holder", NativeUnit: "TRX",
		Source: "gas-live: TronGrid energy and bandwidth prices × today's USDT transfer profile, TRX at the Coinbase spot"}
}

func stableEVMCostTemplate() *StableTransferCost {
	return &StableTransferCost{Status: "not_cached", Label: "USDC transfer", NativeUnit: "ETH",
		Source: "gas-live: gas price × today's measured USDC transfer gas, plus the L1 data fee on L2s, ETH at the Coinbase spot"}
}

// Transfer costs are read from the gas-live cache on every controls run and
// kept as a last-good copy (§8.3): ok for an hour after the read, stale up to
// 24 h, then not_cached, as when the cache was never filled.
const (
	stableGasStaleAfter = time.Hour
	stableGasKeepFor    = 24 * time.Hour
)

type stableGasLastGood struct {
	mu   sync.Mutex
	tron *stableGasValue
	evm  map[string]*stableGasValue
}

type stableGasValue struct {
	cost StableTransferCost
	at   time.Time
}

func newStableGasLastGood() *stableGasLastGood {
	return &stableGasLastGood{evm: map[string]*stableGasValue{}}
}

// update keeps every cost the cache had (status ok) as the new copy; a cost
// the cache lacks leaves the previous copy alone.
func (g *stableGasLastGood) update(c stableGasCosts, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.tronUSDT != nil && c.tronUSDT.Status == "ok" {
		g.tron = &stableGasValue{cost: *c.tronUSDT, at: now}
	}
	for chain, cost := range c.evmUSDC {
		if cost != nil && cost.Status == "ok" {
			g.evm[chain] = &stableGasValue{cost: *cost, at: now}
		}
	}
}

// view returns the copy as /chains serves it.
func (g *stableGasLastGood) view(now time.Time) stableGasCosts {
	g.mu.Lock()
	defer g.mu.Unlock()
	at := func(v *stableGasValue, tmpl *StableTransferCost) *StableTransferCost {
		if v == nil || now.Sub(v.at) > stableGasKeepFor {
			return tmpl
		}
		c := v.cost
		c.Status, c.AsOf = "ok", stableFmt(v.at)
		if now.Sub(v.at) > stableGasStaleAfter {
			c.Status = "stale"
		}
		return &c
	}
	out := stableGasCosts{tronUSDT: at(g.tron, stableTronCostTemplate()), evmUSDC: map[string]*StableTransferCost{}}
	for _, chain := range l2Chains {
		out.evmUSDC[chain.ID] = at(g.evm[chain.ID], stableEVMCostTemplate())
	}
	return out
}

// refreshGasCosts stores what the gas-live cache holds now (controls run).
func (s *stablecoinService) refreshGasCosts(c stableGasCosts) {
	if s.gas != nil {
		s.gas.update(c, s.now().UTC())
	}
}

// gasView is the transfer-cost copy /chains serves.
func (s *stablecoinService) gasView() stableGasCosts {
	g := s.gas
	if g == nil {
		g = newStableGasLastGood()
	}
	return g.view(s.now().UTC())
}

// stableGasCosts reads what the Gas page cached; nothing is fetched.
func (h *APIHandler) stableGasCosts() stableGasCosts {
	get := func(key string) any {
		if h.cache == nil {
			return nil
		}
		v, _ := h.cache.Get(key)
		return v
	}
	spot := func(base string) *float64 {
		if s, ok := get(fmt.Sprintf(gasLiveSpotKey, base)).(*CryptoSpotData); ok && s != nil {
			v := s.PriceUSD
			return &v
		}
		return nil
	}
	cal, _ := get(gasLiveCalibrationKey).(*GasCalibration)
	out := stableGasCosts{evmUSDC: map[string]*StableTransferCost{}}
	out.tronUSDT = stableTronCostTemplate()
	if raw, ok := get(gasLiveTronKey).(*tronLiveRaw); ok && raw != nil && cal != nil {
		live := tronLiveFrom(*raw, *cal, spot("TRX"))
		if len(live.Costs) >= 2 {
			c := out.tronUSDT
			burn := live.Costs[0].BurnTRX
			c.Status, c.Native, c.USD, c.USDNewAddress, c.MeasuredAt = "ok", &burn, live.Costs[0].BurnUSD, live.Costs[1].BurnUSD, cal.MeasuredAt
		}
	}
	for _, chain := range l2Chains {
		c := stableEVMCostTemplate()
		out.evmUSDC[chain.ID] = c
		if cal == nil {
			continue
		}
		actions := l2ActionsFor(cal)
		q, ok := get(fmt.Sprintf(gasLiveL2Key, chain.ID, uint64(cal.USDCTransferGas))).(*l2Quote)
		if !ok || q == nil || len(q.L1Wei) < len(actions) {
			continue
		}
		ladder := l2LadderFrom(map[string]*l2Quote{chain.ID: q}, nil, actions, spot("ETH"))
		for _, row := range ladder.Rows {
			if row.ID != chain.ID {
				continue
			}
			for i, a := range actions {
				if a.Kind == l2ActionUSDC && i < len(row.Actions) {
					total := row.Actions[i].TotalETH
					c.Status, c.Native, c.USD, c.MeasuredAt = "ok", &total, row.Actions[i].TotalUSD, cal.MeasuredAt
				}
			}
		}
	}
	return out
}

// buildChains assembles the chain table of each asset.
func (s *stablecoinService) buildChains(assets []string, costs stableGasCosts) StableChainsResponse {
	state := s.snapshot()
	out := StableChainsResponse{WarmingUp: state == nil, Assets: []StableChainTable{}, CostNote: stableChainsCostNote}
	if state != nil {
		out.AsOf = stableFmt(state.AsOf)
	}
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		var a StableSupplyAsset
		if state != nil {
			a = state.Assets[id]
		} else {
			a = s.stableColdAsset(def)
		}
		tb := StableChainTable{Asset: id, Symbol: def.Symbol, Numeraire: def.Numeraire, IssuerCirculatingNative: a.Totals.IssuerCirculatingNative,
			IssuerAsOf: a.Totals.IssuerAsOf, Rows: []StableChainRow{}}
		for _, r := range a.Chains {
			row := StableChainRow{Chain: r.Chain, ChainLabel: r.ChainLabel, Tier: r.Tier, TokenLabel: r.TokenLabel, Verified: r.Verified,
				IssuerCirculatingNative: r.IssuerCirculatingNative, IssuerSharePct: r.IssuerSharePct, AggregatorCirculatingNative: r.AggregatorCirculatingNative}
			switch {
			case row.Tier != "":
			case r.IssuerCirculatingNative != nil:
				row.Tier = "issuer"
			default:
				row.Tier = "aggregator" // only DefiLlama reports this chain
			}
			if tok, ok := stableRegistryTokenFor(def.Symbol, r.Chain); ok && r.Verified {
				row.Contract = tok.Contract
				if fm, known := stableFreezeKinds[tok.FreezeSel]; known {
					_, monitored := stableControlDeploymentFor(id, tok.Network)
					fm.ControlsMonitored = monitored && tok.Tier == tierNative
					row.FreezeMechanism = &fm
				}
				if info, ok := chains[tok.Network]; ok && info.AddressURL != "" {
					row.ExplorerURL = fmt.Sprintf(info.AddressURL, tok.Contract)
				}
				for _, n := range payAssets[def.Symbol] {
					if def.Symbol != "USDT" && def.Symbol != "USDC" {
						break
					}
					if n == tok.Network {
						q := url.Values{}
						q.Set("asset", def.Symbol)
						q.Set("network", n)
						row.VerifyPayment = &StableVerifyLink{Asset: def.Symbol, Network: n, Hash: "#pay?" + q.Encode()}
					}
				}
				if tok.Tier == tierNative {
					switch {
					case def.Symbol == "USDT" && tok.Network == "tron":
						row.TransferCost = costs.tronUSDT
					case def.Symbol == "USDC":
						row.TransferCost = costs.evmUSDC[tok.Network]
					}
				}
			}
			tb.Rows = append(tb.Rows, row)
		}
		// Rows with an issuer amount first (largest first), then the rest by
		// DefiLlama's amount.
		amount := func(r StableChainRow) (bool, float64) {
			switch {
			case r.IssuerCirculatingNative != nil:
				return true, *r.IssuerCirculatingNative
			case r.AggregatorCirculatingNative != nil:
				return false, *r.AggregatorCirculatingNative
			}
			return false, -1
		}
		sort.SliceStable(tb.Rows, func(i, j int) bool {
			ii, a := amount(tb.Rows[i])
			ij, b := amount(tb.Rows[j])
			if ii != ij {
				return ii
			}
			return a > b
		})
		out.Assets = append(out.Assets, tb)
	}
	return out
}

// stableRegistryTokenFor maps a supply row key (network, or network +
// ".bridged") back to its registry deployment.
func stableRegistryTokenFor(symbol, key string) (registryToken, bool) {
	for _, tok := range registryTokens {
		if tok.Asset == symbol && stableOnchainKey(tok) == key {
			return tok, true
		}
	}
	return registryToken{}, false
}

func (h *APIHandler) StablecoinChains(w http.ResponseWriter, r *http.Request) {
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
	v, _ := h.cachedFetch("stablecoin:chains:"+asset, stableChainsTTL, func() (any, error) {
		return h.stable.buildChains(assets, h.stable.gasView()), nil
	})
	writeJSON(w, v)
}
