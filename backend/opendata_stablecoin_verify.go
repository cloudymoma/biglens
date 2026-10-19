package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// --stablecoin-verify (stablecoin.md §8.7): re-runs the M0 source checks
// against the live endpoints, prints a pass/fail table and exits non-zero on
// any failure. It makes network calls, so tests only run it against stubs.
// Venue, DEX and control checks join with their adapters (M2, M3).

type stableVerifyResult struct {
	Status   string // PASS | FAIL | SKIP
	Provider string
	Check    string
	Detail   string // short; never an upstream message or URL
}

// runStableVerifyCLI wires the configured endpoints and RPC lists.
func runStableVerifyCLI(ctx context.Context, cfg *Config) int {
	settings := cfg.Stablecoin.settings()
	if !settings.Enabled {
		fmt.Println("stablecoin dashboard is disabled (stablecoin.enabled: false); nothing to verify")
		return 0
	}
	applyCryptoGasConfig(cfg.CryptoGas) // TRON base URL
	gas := cfg.CryptoGas.withDefaults()
	lists := map[string][]string{
		"eth": cfg.AddressRisk.rpcURLs(), "arb": gas.ArbitrumRPCURLs, "op": gas.OptimismRPCURLs,
		"base": gas.BaseRPCURLs, "sol": gas.SolanaRPCURLs,
	}
	rpcs := func(network string) []string { return lists[network] }
	return runStableVerify(ctx, settings, defaultStableEndpoints(), rpcs, time.Now().UTC(), os.Stdout)
}

func runStableVerify(ctx context.Context, settings stableSettings, ep stableEndpoints, rpcs func(string) []string, now time.Time, out io.Writer) int {
	results := stableVerifyChecks(ctx, settings, ep, rpcs, now)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tPROVIDER\tCHECK\tDETAIL")
	failed := 0
	for _, r := range results {
		if r.Status == "FAIL" {
			failed++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Status, r.Provider, r.Check, r.Detail)
	}
	tw.Flush()
	fmt.Fprintf(out, "%d of %d checks failed\n", failed, len(results))
	if failed > 0 {
		return 1
	}
	return 0
}

func stableVerifyChecks(ctx context.Context, settings stableSettings, ep stableEndpoints, rpcs func(string) []string, now time.Time) []stableVerifyResult {
	var res []stableVerifyResult
	add := func(provider, check string, err string, ok string) {
		if err != "" {
			res = append(res, stableVerifyResult{"FAIL", provider, check, err})
			return
		}
		res = append(res, stableVerifyResult{"PASS", provider, check, ok})
	}
	skip := func(provider string) {
		res = append(res, stableVerifyResult{"SKIP", provider, "all checks", "disabled in conf.yaml"})
	}

	if settings.Providers["defillama"] {
		snap, code := fetchLlamaStablecoins(ctx, ep, now)
		add("defillama", "/stablecoins core ids 1/2/50 = USDT/USDC/EURC", code, fmt.Sprintf("%d assets", len(snap.Assets)))
		for _, def := range stableCoreAssets {
			rows, code := fetchLlamaChart(ctx, ep, def.LlamaID, def.LlamaPeg)
			detail := ""
			if code == "" {
				detail = fmt.Sprintf("%d daily rows, last %s", len(rows), rows[len(rows)-1].Day)
			}
			add("defillama", "/stablecoincharts "+def.Symbol+" (native totalCirculating)", code, detail)
		}
	} else {
		skip("defillama")
	}

	if settings.Providers["circle"] {
		snap, code := fetchCircleStablecoins(ctx, ep, now)
		if code == "" && len(snap.Missing) > 0 {
			code = "missing " + strings.Join(snap.Missing, ",")
		}
		detail := ""
		if code == "" {
			detail = fmt.Sprintf("USDC %d chains, EURC %d chains", len(snap.Coins["USDC"].Chains), len(snap.Coins["EURC"].Chains))
		}
		add("circle", "/v1/stablecoins USDC and EURC per-chain amounts", code, detail)
	} else {
		skip("circle")
	}

	if settings.Providers["tether"] {
		snap, code := fetchTetherTransparency(ctx, ep, now)
		detail := ""
		if code == "" {
			detail = fmt.Sprintf("%d chains, sum = total_liabilities within 0.01%%, update %s", len(snap.Chains), snap.UpdatedAt.Format("2006-01-02 15:04Z"))
		}
		add("tether", "transparency.json usdt per-chain formula", code, detail)
	} else {
		skip("tether")
	}

	if settings.Providers["ecb"] {
		for _, f := range []struct {
			check string
			fetch func(context.Context, stableEndpoints, int) ([]stableRateObs, string)
		}{{"EXR D.USD.EUR.SP00.A (EUR/USD)", fetchECBEURUSD}, {"EST B.EU000A2X2A25.WT (€STR)", fetchECBESTR}} {
			obs, code := f.fetch(ctx, ep, 3)
			detail := ""
			if code == "" {
				last := obs[len(obs)-1]
				detail = fmt.Sprintf("%s = %g", last.Day, last.Value)
			}
			add("ecb", f.check, code, detail)
		}
	} else {
		skip("ecb")
	}

	if settings.Providers["us_treasury"] {
		obs, code := fetchTreasury13W(ctx, ep, now.Year())
		if code != "" && now.Month() == time.January {
			obs, code = fetchTreasury13W(ctx, ep, now.Year()-1)
		}
		detail := ""
		if code == "" {
			last := obs[len(obs)-1]
			detail = fmt.Sprintf("%s = %g%%", last.Day, last.Value)
		}
		add("us_treasury", "daily bill rates CSV (13-week coupon equivalent)", code, detail)
	} else {
		skip("us_treasury")
	}

	if settings.Providers["rpc_supply"] && rpcs != nil {
		for _, def := range stableCoreAssets {
			for _, tok := range registryTokens {
				if tok.Asset != def.Symbol {
					continue
				}
				symbol, decimals, code := stableReadTokenMeta(ctx, tok, rpcs)
				check := fmt.Sprintf("%s %s symbol/decimals", tok.Asset, stableOnchainKey(tok))
				if code == "" && decimals != tok.Decimals {
					code = fmt.Sprintf("decimals %d (want %d)", decimals, tok.Decimals)
				}
				if code == "" && chains[tok.Network].Family != familySol && symbol != tok.Label && symbol != tok.Asset {
					code = fmt.Sprintf("symbol %q (want %s or %s)", symbol, tok.Asset, tok.Label)
				}
				detail := fmt.Sprintf("decimals %d", decimals)
				if symbol != "" {
					detail = fmt.Sprintf("%s, decimals %d", symbol, decimals)
				}
				add("rpc_supply", check, code, detail)
			}
		}
	} else {
		skip("rpc_supply")
	}
	return res
}

// stableReadTokenMeta reads symbol() and decimals() (EVM, TRC-20) or the SPL
// mint's decimals (Solana: no on-chain symbol).
func stableReadTokenMeta(ctx context.Context, tok registryToken, rpcs func(string) []string) (string, int, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch chains[tok.Network].Family {
	case familyEVM:
		call := func(sel string) (string, string) {
			return evmRPCFailover(ctx, rpcs(tok.Network), func(actx context.Context, u string) (string, string) {
				res, err := evmRPC(actx, u, "eth_call", map[string]string{"to": tok.Contract, "data": sel}, "latest")
				if err != nil {
					return "", evmRPCErrCode(err)
				}
				return res, ""
			})
		}
		symHex, code := call("0x95d89b41")
		if code != "" {
			return "", 0, code
		}
		decHex, code := call("0x313ce567")
		if code != "" {
			return "", 0, code
		}
		return stableDecodeMeta(symHex, decHex)
	case familyTron:
		symHex, code := stableTronConstant(ctx, tok.Contract, "symbol()")
		if code != "" {
			return "", 0, code
		}
		decHex, code := stableTronConstant(ctx, tok.Contract, "decimals()")
		if code != "" {
			return "", 0, code
		}
		return stableDecodeMeta(symHex, decHex)
	case familySol:
		raw, code := solanaRPCFailover(ctx, rpcs("sol"), "getAccountInfo", tok.Contract, map[string]any{"encoding": "jsonParsed", "commitment": "finalized"})
		if code != "" {
			return "", 0, code
		}
		var out struct {
			Value *struct {
				Data struct {
					Parsed struct {
						Type string `json:"type"`
						Info struct {
							Decimals *int `json:"decimals"`
						} `json:"info"`
					} `json:"parsed"`
				} `json:"data"`
			} `json:"value"`
		}
		if json.Unmarshal(raw, &out) != nil || out.Value == nil || out.Value.Data.Parsed.Type != "mint" || out.Value.Data.Parsed.Info.Decimals == nil {
			return "", 0, "bad_response"
		}
		return "", *out.Value.Data.Parsed.Info.Decimals, ""
	}
	return "", 0, "unsupported_network"
}

func stableDecodeMeta(symHex, decHex string) (string, int, string) {
	sym, ok := decodeABIString(symHex)
	if !ok {
		return "", 0, "bad_response"
	}
	d, ok := parseHexBigInt(decHex)
	if !ok || !d.IsInt64() || d.Int64() > 255 {
		return "", 0, "bad_response"
	}
	return sym, int(d.Int64()), ""
}

// stableTronConstant runs a constant (view) call on a TRC-20 contract.
func stableTronConstant(ctx context.Context, contract, selector string) (string, string) {
	payload, err := json.Marshal(map[string]any{
		"owner_address": tronDummyOwner, "contract_address": contract, "function_selector": selector, "visible": true,
	})
	if err != nil {
		return "", "bad_request"
	}
	raw, code := tronGridDo(ctx, http.MethodPost, "/walletsolidity/triggerconstantcontract", payload)
	if code != "" {
		return "", code
	}
	var parsed struct {
		Result struct {
			Result bool `json:"result"`
		} `json:"result"`
		ConstantResult []string `json:"constant_result"`
	}
	if json.Unmarshal(raw, &parsed) != nil || !parsed.Result.Result || len(parsed.ConstantResult) == 0 {
		return "", "bad_response"
	}
	return "0x" + strings.TrimPrefix(strings.TrimSpace(parsed.ConstantResult[0]), "0x"), ""
}

// decodeABIString decodes an ABI-encoded string return value (or a legacy
// bytes32 symbol).
func decodeABIString(h string) (string, bool) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(h), "0x"))
	if err != nil {
		return "", false
	}
	if len(b) == 32 { // bytes32
		return strings.TrimRight(string(b), "\x00"), utf8.Valid(b)
	}
	if len(b) < 64 {
		return "", false
	}
	off := new(big.Int).SetBytes(b[:32])
	if !off.IsInt64() || off.Int64()+32 > int64(len(b)) {
		return "", false
	}
	o := off.Int64()
	n := new(big.Int).SetBytes(b[o : o+32])
	if !n.IsInt64() || o+32+n.Int64() > int64(len(b)) {
		return "", false
	}
	s := b[o+32 : o+32+n.Int64()]
	if !utf8.Valid(s) {
		return "", false
	}
	return string(s), true
}
