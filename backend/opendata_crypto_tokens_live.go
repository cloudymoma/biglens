package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	tokenSupplyCacheTTL  = 10 * time.Minute
	tokenMarketCacheTTL  = 5 * time.Minute
	tokenLiveInMemoryTTL = 2 * time.Minute
	tokenLiveTimeout     = 12 * time.Second

	selTotalSupply      = "0x18160ddd"                         // totalSupply()
	tronTriggerSelector = "totalSupply()"                      // TRC-20 totalSupply()
	tronDummyOwner      = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb" // TronGrid constant call owner_address
)

var defillamaCoinsURL = "https://coins.llama.fi/prices/current/"

type CryptoTokensLiveData struct {
	SupplyMatrix []TokenSupplyRow `json:"supply_matrix"`
	MarketPulse  []TokenMarketRow `json:"market_pulse"`
	AsOf         string           `json:"as_of"`
	Warnings     []string         `json:"warnings,omitempty"`
}

func scaleBigIntToFloat(raw *big.Int, decimals int) float64 {
	if raw == nil || raw.Sign() <= 0 {
		return 0
	}
	num := new(big.Float).SetPrec(256).SetInt(raw)
	denom := new(big.Float).SetPrec(256).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	f, _ := new(big.Float).Quo(num, denom).Float64()
	return f
}

func fetchEVMTokenSupply(ctx context.Context, rpcs []string, contract string, decimals int) (float64, string) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return evmRPCFailover(cctx, rpcs, func(actx context.Context, rpcURL string) (float64, string) {
		res, err := evmRPC(actx, rpcURL, "eth_call", map[string]string{"to": contract, "data": selTotalSupply}, "latest")
		if err != nil {
			return 0, evmRPCErrCode(err)
		}
		n, ok := parseHexBigInt(res)
		if !ok {
			return 0, "bad_response"
		}
		return scaleBigIntToFloat(n, decimals), ""
	})
}

func fetchTronTokenSupply(ctx context.Context, contract string, decimals int) (float64, string) {
	payload, err := json.Marshal(map[string]any{
		"owner_address":     tronDummyOwner,
		"contract_address":  contract,
		"function_selector": tronTriggerSelector,
		"visible":           true,
	})
	if err != nil {
		return 0, "bad_request"
	}
	raw, code := tronGridDo(ctx, http.MethodPost, "/walletsolidity/triggerconstantcontract", payload)
	if code != "" {
		return 0, code
	}
	var parsed struct {
		Result struct {
			Result bool `json:"result"`
		} `json:"result"`
		ConstantResult []string `json:"constant_result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || !parsed.Result.Result || len(parsed.ConstantResult) == 0 {
		return 0, "bad_response"
	}
	resHex := strings.TrimSpace(parsed.ConstantResult[0])
	if !strings.HasPrefix(strings.ToLower(resHex), "0x") {
		resHex = "0x" + resHex
	}
	n, ok := parseHexBigInt(resHex)
	if !ok {
		return 0, "bad_response"
	}
	return scaleBigIntToFloat(n, decimals), ""
}

// fetchSolanaMintSupply reads the SPL Token Mint account via getAccountInfo
// (jsonParsed) because public Solana nodes (e.g. publicnode) reject indexed
// getTokenSupply requests without a personal token, whereas getAccountInfo works keylessly.
func fetchSolanaMintSupply(ctx context.Context, rpcs []string, mint string, decimals int) (float64, string) {
	res, code := solanaRPCFailover(ctx, rpcs, "getAccountInfo", mint, map[string]any{
		"encoding":   "jsonParsed",
		"commitment": "finalized",
	})
	if code != "" {
		return 0, code
	}
	var out struct {
		Value *struct {
			Data struct {
				Parsed struct {
					Info struct {
						Supply string `json:"supply"`
					} `json:"info"`
				} `json:"parsed"`
			} `json:"data"`
		} `json:"value"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.Value == nil {
		return 0, "bad_response"
	}
	n, ok := new(big.Int).SetString(strings.TrimSpace(out.Value.Data.Parsed.Info.Supply), 10)
	if !ok {
		return 0, "bad_response"
	}
	return scaleBigIntToFloat(n, decimals), ""
}

func fetchDefiLlamaPrices(ctx context.Context) (map[string]float64, string) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	coins := make([]string, len(majorTokenCatalog))
	for i, m := range majorTokenCatalog {
		coins[i] = "ethereum:" + m.Address
	}
	u := strings.TrimRight(defillamaCoinsURL, "/") + "/" + strings.Join(coins, ",")
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "bad_request"
	}
	req.Header.Set("User-Agent", evmUserAgent)
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return nil, "bad_response"
	}
	var parsed struct {
		Coins map[string]struct {
			Price float64 `json:"price"`
		} `json:"coins"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "bad_response"
	}
	out := make(map[string]float64, len(parsed.Coins))
	for k, v := range parsed.Coins {
		addr := strings.ToLower(strings.TrimPrefix(k, "ethereum:"))
		if v.Price > 0 {
			out[addr] = v.Price
		}
	}
	return out, ""
}

// registrySupplyReading is one on-chain total supply read for a registry token.
type registrySupplyReading struct {
	Token  registryToken
	Supply float64 // native units; meaningful only when Code is ""
	Code   string  // "" on success, else an upstream error code
	ReadAt time.Time
}

// fetchRegistrySupplies reads totalSupply() (EVM, TRC-20) or the SPL mint
// supply (Solana) of each token over the configured RPCs, in the order given.
// It is shared by the Crypto Pulse supply matrix and the Stablecoins supply
// sampler, which keep their own caches (stablecoin.md §8.3).
func fetchRegistrySupplies(ctx context.Context, tokens []registryToken, rpcs func(network string) []string, now func() time.Time) []registrySupplyReading {
	out := make([]registrySupplyReading, len(tokens))
	var g errgroup.Group
	g.SetLimit(6)
	for i, tok := range tokens {
		g.Go(func() error {
			var (
				supply float64
				code   string
			)
			switch chains[tok.Network].Family {
			case familyEVM:
				supply, code = fetchEVMTokenSupply(ctx, rpcs(tok.Network), tok.Contract, tok.Decimals)
			case familyTron:
				supply, code = fetchTronTokenSupply(ctx, tok.Contract, tok.Decimals)
			case familySol:
				supply, code = fetchSolanaMintSupply(ctx, rpcs("sol"), tok.Contract, tok.Decimals)
			default:
				code = "unsupported_network"
			}
			out[i] = registrySupplyReading{Token: tok, Supply: supply, Code: code, ReadAt: now().UTC()}
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func (h *APIHandler) refreshSupplyMatrix(ctx context.Context, prev []TokenSupplyRow, now time.Time) ([]TokenSupplyRow, []string) {
	prevMap := make(map[string]TokenSupplyRow, len(prev))
	for _, r := range prev {
		prevMap[r.Asset+"|"+r.Network+"|"+r.Contract] = r
	}

	at := now.UTC().Format(time.RFC3339)
	readings := fetchRegistrySupplies(ctx, registryTokens, h.payRPCs, time.Now)
	rows := make([]TokenSupplyRow, len(readings))
	var warnings []string
	for i, rd := range readings {
		tok := rd.Token
		row := TokenSupplyRow{
			Asset:        tok.Asset,
			Network:      tok.Network,
			NetworkLabel: chains[tok.Network].Label,
			Contract:     tok.Contract,
			Tier:         string(tok.Tier),
			Label:        tok.Label,
			Supply:       rd.Supply,
			UpdatedAt:    at,
		}
		if rd.Code != "" {
			if old, ok := prevMap[tok.Asset+"|"+tok.Network+"|"+tok.Contract]; ok && old.Supply > 0 {
				row.Supply = old.Supply
				row.UpdatedAt = old.UpdatedAt
				row.Stale = true
			}
			row.Error = rd.Code
			warnings = append(warnings, fmt.Sprintf("supply %s (%s): %s", tok.Label, tok.Network, rd.Code))
		}
		rows[i] = row
	}
	return rows, warnings
}

func (h *APIHandler) refreshMarketPulse(ctx context.Context, prev []TokenMarketRow, now time.Time) ([]TokenMarketRow, []string) {
	prevMap := make(map[string]TokenMarketRow, len(prev))
	for _, r := range prev {
		prevMap[strings.ToLower(r.Address)] = r
	}

	var (
		llamaPrices map[string]float64
		llamaCode   string
		bsInfos     = make([]blockscoutAddressInfo, len(majorTokenCatalog))
		bsCodes     = make([]string, len(majorTokenCatalog))
		g           errgroup.Group
	)
	g.SetLimit(6)
	g.Go(func() error {
		llamaPrices, llamaCode = fetchDefiLlamaPrices(ctx)
		return nil
	})
	bsURL := h.payBlockscoutURL("eth")
	for i, m := range majorTokenCatalog {
		i, m := i, m
		g.Go(func() error {
			if bsURL == "" {
				bsCodes[i] = "not_configured"
				return nil
			}
			bctx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
			defer cancel()
			bsInfos[i], bsCodes[i] = fetchBlockscoutAddress(bctx, bsURL, m.Address)
			return nil
		})
	}
	_ = g.Wait()

	var warnings []string
	if llamaCode != "" {
		warnings = append(warnings, "defillama: "+llamaCode)
	}
	seenBSWarn := make(map[string]bool)
	for _, code := range bsCodes {
		if code != "" && !seenBSWarn[code] {
			seenBSWarn[code] = true
			warnings = append(warnings, "blockscout: "+code)
		}
	}
	at := now.UTC().Format(time.RFC3339)
	rows := make([]TokenMarketRow, len(majorTokenCatalog))
	for i, m := range majorTokenCatalog {
		old, hasOld := prevMap[m.Address]
		row := TokenMarketRow{
			Address:      m.Address,
			Symbol:       m.Symbol,
			HoldersCount: bsInfos[i].HoldersCount,
			Volume24hUSD: bsInfos[i].Volume24hUSD,
			MarketCapUSD: bsInfos[i].MarketCapUSD,
			UpdatedAt:    at,
		}
		if p := llamaPrices[m.Address]; p > 0 {
			row.PriceUSD = &p
		}
		// A missing price stays unknown (nil) — never an assumed $1 — and
		// marks the row stale so the last-good price below can apply.
		if bsCodes[i] != "" || llamaCode != "" || row.PriceUSD == nil {
			row.Stale = true
			if hasOld {
				if row.PriceUSD == nil && old.PriceUSD != nil {
					p := *old.PriceUSD
					row.PriceUSD = &p
				}
				if row.HoldersCount == 0 {
					row.HoldersCount = old.HoldersCount
				}
				if row.Volume24hUSD == 0 {
					row.Volume24hUSD = old.Volume24hUSD
				}
				if row.MarketCapUSD == 0 {
					row.MarketCapUSD = old.MarketCapUSD
				}
				row.UpdatedAt = old.UpdatedAt
			}
		}
		rows[i] = row
	}
	return rows, warnings
}

func (h *APIHandler) CryptoTokensLive(w http.ResponseWriter, r *http.Request) {
	const cacheKey = "opendata:crypto:tokens:live"
	if cached, ok := h.cache.Get(cacheKey); ok {
		writeJSON(w, cached)
		return
	}
	v, err, _ := cryptoFlight.Do(cacheKey, func() (any, error) {
		if cached, ok := h.cache.Get(cacheKey); ok {
			return cached, nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), tokenLiveTimeout)
		defer cancel()
		now := time.Now().UTC()
		store := h.payRiskStore()

		var (
			storedSupply []TokenSupplyRow
			supplyAsOf   time.Time
			storedMarket []TokenMarketRow
			marketAsOf   time.Time
		)
		if store != nil {
			storedSupply, supplyAsOf, _ = store.loadTokenSupplyCache(ctx)
			storedMarket, marketAsOf, _ = store.loadTokenMarketCache(ctx)
		}

		needSupply := len(storedSupply) < len(registryTokens) || now.Sub(supplyAsOf) > tokenSupplyCacheTTL
		needMarket := len(storedMarket) < len(majorTokenCatalog) || now.Sub(marketAsOf) > tokenMarketCacheTTL

		var (
			supplyRows = storedSupply
			marketRows = storedMarket
			supplyWarn []string
			marketWarn []string
			wg         sync.WaitGroup
		)
		if needSupply {
			wg.Add(1)
			go func() {
				defer wg.Done()
				supplyRows, supplyWarn = h.refreshSupplyMatrix(ctx, storedSupply, now)
				if store != nil {
					var fresh []TokenSupplyRow
					for _, r := range supplyRows {
						if !r.Stale && r.Error == "" && r.Supply > 0 {
							fresh = append(fresh, r)
						}
					}
					if len(fresh) > 0 {
						dbCtx, dbCancel := context.WithTimeout(context.Background(), 3*time.Second)
						_ = store.upsertTokenSupplyCache(dbCtx, fresh, now)
						dbCancel()
						if len(supplyWarn) == 0 {
							supplyAsOf = now
						}
					}
				}
			}()
		}
		if needMarket {
			wg.Add(1)
			go func() {
				defer wg.Done()
				marketRows, marketWarn = h.refreshMarketPulse(ctx, storedMarket, now)
				if store != nil {
					var fresh []TokenMarketRow
					for _, r := range marketRows {
						if !r.Stale && r.PriceUSD != nil && *r.PriceUSD > 0 {
							fresh = append(fresh, r)
						}
					}
					if len(fresh) > 0 {
						dbCtx, dbCancel := context.WithTimeout(context.Background(), 3*time.Second)
						_ = store.upsertTokenMarketCache(dbCtx, fresh, now)
						dbCancel()
						if len(marketWarn) == 0 {
							marketAsOf = now
						}
					}
				}
			}()
		}
		wg.Wait()

		var warnings []string
		warnings = append(warnings, supplyWarn...)
		warnings = append(warnings, marketWarn...)

		asOf := now
		if !supplyAsOf.IsZero() && supplyAsOf.Before(asOf) {
			asOf = supplyAsOf
		}
		data := &CryptoTokensLiveData{
			SupplyMatrix: nonNilSlice(supplyRows),
			MarketPulse:  nonNilSlice(marketRows),
			AsOf:         asOf.UTC().Format(time.RFC3339),
			Warnings:     warnings,
		}
		ttl := tokenLiveInMemoryTTL
		if len(data.Warnings) > 0 {
			ttl = 30 * time.Second
		}
		h.cache.SetWithTTL(cacheKey, data, ttl)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}
