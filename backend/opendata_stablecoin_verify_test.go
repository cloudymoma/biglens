package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func abiString(s string) string {
	b := make([]byte, 64+((len(s)+31)/32)*32)
	b[31] = 0x20
	b[63] = byte(len(s))
	copy(b[64:], s)
	return hex.EncodeToString(b)
}

// stableVerifyRPCStub answers symbol(), decimals() and totalSupply() for EVM,
// TRON constant calls and Solana mint accounts. symbols overrides a
// contract's symbol (lowercase address or TRON base58).
func stableVerifyRPCStub(t *testing.T, symbols map[string]string, solDecimals int) func(string) []string {
	t.Helper()
	symbolFor := func(contract string) string {
		if s, ok := symbols[contract]; ok {
			return s
		}
		for _, tok := range registryTokens {
			if strings.EqualFold(tok.Contract, contract) {
				return tok.Asset
			}
		}
		return "UNKNOWN"
	}
	evm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		var call struct{ To, Data string }
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &call)
		}
		var res string
		pool := map[string]string{ // the Uniswap v3 USDC/USDT 0.01% pool
			"0x0dfe1681": "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(stableUniswapToken0, "0x"),
			"0xd21220a7": "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(stableUniswapToken1, "0x"),
			"0xddca3f43": fmt.Sprintf("0x%064x", stableUniswapFee),
			"0x3850c7bd": uniswapSlot0Fixture(t),
		}
		switch call.Data {
		case "0x0dfe1681", "0xd21220a7", "0xddca3f43", "0x3850c7bd":
			res = pool[call.Data]
		case "0x95d89b41":
			res = "0x" + abiString(symbolFor(strings.ToLower(call.To)))
		case "0x313ce567":
			res = fmt.Sprintf("0x%064x", 6)
		default:
			res = fmt.Sprintf("0x%064x", 1_000_000_000_000)
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"%s"}`, res)
	}))
	t.Cleanup(evm.Close)
	sol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"value":{"data":{"parsed":{"type":"mint","info":{"decimals":%d,"supply":"1000000"}}}}}}`, solDecimals)
	}))
	t.Cleanup(sol.Close)
	tron := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Contract string `json:"contract_address"`
			Selector string `json:"function_selector"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		res := fmt.Sprintf("%064x", 6)
		if req.Selector == "symbol()" {
			res = abiString(symbolFor(req.Contract))
		}
		fmt.Fprintf(w, `{"result":{"result":true},"constant_result":["%s"]}`, res)
	}))
	t.Cleanup(tron.Close)
	orig := tronGridBaseURL
	tronGridBaseURL = tron.URL
	t.Cleanup(func() { tronGridBaseURL = orig })
	return func(network string) []string {
		if network == "sol" {
			return []string{sol.URL}
		}
		return []string{evm.URL}
	}
}

// uniswapSlot0Fixture is the recorded slot0() result.
func uniswapSlot0Fixture(t *testing.T) string {
	var recorded []struct {
		Request struct {
			Data string `json:"data"`
		} `json:"request"`
		Response struct {
			Result string `json:"result"`
		} `json:"response"`
	}
	_ = json.Unmarshal(readStableFixture(t, "uniswap_v3_usdc_usdt_rpc.json"), &recorded)
	for _, r := range recorded {
		if r.Request.Data == selUniswapSlot0 {
			return r.Response.Result
		}
	}
	return "0x"
}

// stableVerifyEndpoints serves every verify check: the supply stub (issuers,
// DefiLlama, ECB, Treasury) and the venue stub (exchanges, Bitstamp candles).
func stableVerifyEndpoints(t *testing.T) (*stableStub, *stableVenueStub, stableEndpoints) {
	t.Helper()
	stub, venues := newStableStub(t), newStableVenueStub(t)
	ep := stub.endpoints()
	v := venues.endpoints()
	ep.CoinbaseBase, ep.KrakenBase, ep.BitstampBase, ep.OKXBase, ep.BinanceBase = v.CoinbaseBase, v.KrakenBase, v.BitstampBase, v.OKXBase, v.BinanceBase
	return stub, venues, ep
}

func runVerifyForTest(t *testing.T, settings stableSettings, ep stableEndpoints, rpcs func(string) []string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := runStableVerify(context.Background(), settings, ep, rpcs, time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC), &out)
	return code, out.String()
}

func TestStableVerifyPass(t *testing.T) {
	_, venues, ep := stableVerifyEndpoints(t)
	rpcs := stableVerifyRPCStub(t, map[string]string{
		"0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9": "USD₮0", "0x01bff41798a0bcf287b996046ca68b395dbc1071": "USD₮0",
		"0xff970a61a04b1ca14834a43f5de4533ebddb5cc8": "USDC.e",
	}, 6)
	code, out := runVerifyForTest(t, StablecoinConfig{}.settings(), ep, rpcs)
	if code != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("verify exit %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{"PASS", "defillama", "circle", "tether", "ecb", "us_treasury", "rpc_supply", "EURC sol",
		// M2 providers: every venue pair, the traps, the candles, DefiLlama prices and the pool.
		"coinbase", "USDT-USDC", "EURC-USDC", "USDC-USD", "kraken", "USDTZUSD", "ZEURZUSD", "bitstamp", "eurceur", "eurcusd",
		"daily OHLC usdcusd", "okx", "USDC-USDT", "binance", "USDCUSDT", "/stablecoinprices", "uniswap_rpc", "slot0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Bridged and renamed deployments may report the asset symbol or the label.
	if !strings.Contains(out, "0 of ") {
		t.Errorf("summary line missing:\n%s", out)
	}
	// eurceur candles are not used (§7.2), so they are not verified either.
	if strings.Contains(out, "daily OHLC eurceur") {
		t.Errorf("eurceur candles verified:\n%s", out)
	}
	if venues.count("/kraken/0/public/Ticker") != 1 {
		t.Errorf("kraken calls = %d, want one batched call", venues.count("/kraken/0/public/Ticker"))
	}
}

func TestStableVerifyFailures(t *testing.T) {
	rpcs := stableVerifyRPCStub(t, nil, 6)
	cases := []struct {
		name     string
		mutate   func(*stableStub, *stableVenueStub, stableEndpoints) stableEndpoints
		provider string
		detail   string // expected in the FAIL line, when set
	}{
		{"defillama id drift", func(s *stableStub, _ *stableVenueStub, ep stableEndpoints) stableEndpoints {
			drift := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"peggedAssets":[{"id":"1","symbol":"USDX","pegType":"peggedUSD"},{"id":"2","symbol":"USDC","pegType":"peggedUSD"},{"id":"50","symbol":"EURC","pegType":"peggedEUR"}]}`))
			}))
			t.Cleanup(drift.Close)
			ep.LlamaBase = drift.URL
			return ep
		}, "defillama", ""},
		{"tether sum mismatch", func(s *stableStub, _ *stableVenueStub, ep stableEndpoints) stableEndpoints {
			body := strings.Replace(string(readStableFixture(t, "tether_transparency.json")),
				`"total_liabilities": "`+fxTetherLiabilitiesStr+`"`, `"total_liabilities": "1`+fxTetherLiabilitiesStr+`"`, 1)
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			t.Cleanup(bad.Close)
			ep.TetherURL = bad.URL
			return ep
		}, "tether", "sum_mismatch"},
		{"circle outage", func(s *stableStub, _ *stableVenueStub, ep stableEndpoints) stableEndpoints {
			s.setFail("/circle", http.StatusServiceUnavailable)
			return ep
		}, "circle", ""},
		{"ecb html", func(s *stableStub, _ *stableVenueStub, ep stableEndpoints) stableEndpoints {
			html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>maintenance</html>")) }))
			t.Cleanup(html.Close)
			ep.ECBBase = html.URL
			return ep
		}, "ecb", ""},
		{"binance geo-blocked", func(_ *stableStub, v *stableVenueStub, ep stableEndpoints) stableEndpoints {
			v.setStatus("/binance/api/v3/ticker/24hr", http.StatusUnavailableForLegalReasons)
			return ep
		}, "binance", "geo_blocked"},
		{"kraken error list", func(_ *stableStub, _ *stableVenueStub, ep stableEndpoints) stableEndpoints {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"error":["EQuery:Unknown asset pair"]}`))
			}))
			t.Cleanup(bad.Close)
			ep.KrakenBase = bad.URL
			return ep
		}, "kraken", "bad_response"},
		{"bitstamp list for a known pair", func(_ *stableStub, v *stableVenueStub, ep stableEndpoints) stableEndpoints {
			v.serveList("usdtusd")
			return ep
		}, "bitstamp", "bad_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub, venues, ep := stableVerifyEndpoints(t)
			code, out := runVerifyForTest(t, StablecoinConfig{}.settings(), tc.mutate(stub, venues, ep), rpcs)
			if code == 0 {
				t.Fatalf("verify exit 0, want non-zero:\n%s", out)
			}
			found := false
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "FAIL") && strings.Contains(line, tc.provider) && strings.Contains(line, tc.detail) {
					found = true
				}
			}
			if !found {
				t.Errorf("no FAIL line for %s (%q):\n%s", tc.provider, tc.detail, out)
			}
		})
	}
	// The Uniswap pool must still be USDC/USDT at the 0.01% tier.
	_, _, ep := stableVerifyEndpoints(t)
	wrongPool, _ := newUniswapRPCStub(t, map[string]string{"0xddca3f43": fmt.Sprintf("0x%064x", 500)})
	poolRPCs := func(n string) []string {
		if n == "eth" {
			return []string{wrongPool}
		}
		return rpcs(n)
	}
	cfg := StablecoinConfig{Providers: map[string]bool{"rpc_supply": false}}
	if code, out := runVerifyForTest(t, cfg.settings(), ep, poolRPCs); code == 0 || !strings.Contains(out, "FAIL") || !strings.Contains(out, "uniswap_rpc") {
		t.Errorf("verify with a 0.05%% pool: exit %d\n%s", code, out)
	}
}

func TestStableVerifyRegistryDrift(t *testing.T) {
	_, _, ep := stableVerifyEndpoints(t)
	rpcs := stableVerifyRPCStub(t, map[string]string{"0x1abaea1f7c830bd89acc67ec4af516284b1bc33c": "XYZ"}, 9)
	code, out := runVerifyForTest(t, StablecoinConfig{}.settings(), ep, rpcs)
	if code == 0 {
		t.Fatalf("verify exit 0 with a wrong symbol and wrong Solana decimals:\n%s", out)
	}
	var symbolFail, decimalsFail bool
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FAIL") && strings.Contains(line, "EURC eth") && strings.Contains(line, "XYZ") {
			symbolFail = true
		}
		if strings.HasPrefix(line, "FAIL") && strings.Contains(line, "sol") && strings.Contains(line, "decimals 9") {
			decimalsFail = true
		}
	}
	if !symbolFail || !decimalsFail {
		t.Errorf("symbol fail %v, decimals fail %v:\n%s", symbolFail, decimalsFail, out)
	}
}

func TestStableVerifySkipsDisabledProviders(t *testing.T) {
	stub, venues, ep := stableVerifyEndpoints(t)
	stub.setFail("/circle", http.StatusServiceUnavailable)
	cfg := StablecoinConfig{Providers: map[string]bool{"circle": false, "rpc_supply": false, "binance": false, "uniswap_rpc": false}}
	code, out := runVerifyForTest(t, cfg.settings(), ep, nil)
	if code != 0 || stub.count("/circle") != 0 || venues.count("/binance/api/v3/ticker/24hr") != 0 {
		t.Fatalf("exit %d, circle calls %d, binance calls %d; disabled providers must be skipped:\n%s", code, stub.count("/circle"),
			venues.count("/binance/api/v3/ticker/24hr"), out)
	}
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "binance") {
		t.Errorf("no SKIP line for the disabled providers:\n%s", out)
	}
}
