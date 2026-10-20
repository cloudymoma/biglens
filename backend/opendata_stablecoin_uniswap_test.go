package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newUniswapRPCStub answers eth_call from the recorded pool calls; override
// maps a selector to a different result (or "error" for a JSON-RPC error).
func newUniswapRPCStub(t *testing.T, override map[string]string) (string, *int) {
	t.Helper()
	var recorded []struct {
		Request struct {
			To   string `json:"to"`
			Data string `json:"data"`
		} `json:"request"`
		Response struct {
			Result string `json:"result"`
		} `json:"response"`
	}
	if err := json.Unmarshal(readStableFixture(t, "uniswap_v3_usdc_usdt_rpc.json"), &recorded); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		var call struct {
			To   string `json:"to"`
			Data string `json:"data"`
		}
		if json.Unmarshal(body, &req) != nil || req.Method != "eth_call" || len(req.Params) == 0 || json.Unmarshal(req.Params[0], &call) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if res, ok := override[call.Data]; ok {
			if res == "error" {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"execution reverted"}}`))
				return
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + res + `"}`))
			return
		}
		for _, rc := range recorded {
			if strings.EqualFold(rc.Request.To, call.To) && rc.Request.Data == call.Data {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + rc.Response.Result + `"}`))
				return
			}
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unknown call"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

func TestStableUniswapPrice(t *testing.T) {
	sqrt, ok := new(big.Int).SetString(fxUniswapSqrtPriceX96, 10)
	if !ok {
		t.Fatal("bad fixture constant")
	}
	// P = (sqrtPriceX96 / 2^96)^2 × 10^(dec0 − dec1) = token1 per token0.
	if p := stableUniswapPrice(sqrt, 6, 6); math.Abs(p-fxUniswapPrice) > 1e-12 {
		t.Errorf("price = %.12f, want %.12f", p, fxUniswapPrice)
	}
	q96 := new(big.Int).Lsh(big.NewInt(1), 96)
	if p := stableUniswapPrice(q96, 6, 6); p != 1 {
		t.Errorf("sqrtPriceX96 = 2^96 -> %v, want 1", p)
	}
	// Decimal scaling: an 18-decimal token0 against a 6-decimal token1.
	if p := stableUniswapPrice(q96, 18, 6); math.Abs(p-1e12)/1e12 > 1e-12 {
		t.Errorf("dec0 18, dec1 6 -> %v, want 1e12", p)
	}
	if p := stableUniswapPrice(new(big.Int).Lsh(big.NewInt(1), 97), 6, 6); p != 4 {
		t.Errorf("sqrtPriceX96 = 2^97 -> %v, want 4", p)
	}
}

func TestParseUniswapSlot0(t *testing.T) {
	word := func(hex string) string { return strings.Repeat("0", 64-len(hex)) + hex }
	sqrtHex := new(big.Int).Lsh(big.NewInt(1), 96).Text(16)
	tail := strings.Repeat(word("0"), 5) + word("1")
	sqrt, tick, err := parseUniswapSlot0("0x" + word(sqrtHex) + word("6") + tail)
	if err != nil || sqrt.Cmp(new(big.Int).Lsh(big.NewInt(1), 96)) != 0 || tick != 6 {
		t.Errorf("slot0 = %v, %d, %v", sqrt, tick, err)
	}
	// int24 tick, sign-extended to 256 bits.
	if _, tick, err := parseUniswapSlot0("0x" + word(sqrtHex) + strings.Repeat("f", 64) + tail); err != nil || tick != -1 {
		t.Errorf("negative tick = %d, %v, want -1", tick, err)
	}
	for name, res := range map[string]string{
		"short":        "0x" + word(sqrtHex),
		"zero price":   "0x" + word("0") + word("6") + tail,
		"not hex":      "0x" + strings.Repeat("z", 64*7),
		"no prefix":    word(sqrtHex) + word("6") + tail,
		"tick too big": "0x" + word(sqrtHex) + word("800000") + tail,
	} {
		if _, _, err := parseUniswapSlot0(res); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadUniswapPoolFromStub(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	rpc, calls := newUniswapRPCStub(t, nil)
	rpcs := func(string) []string { return []string{rpc} }
	r := readStableUniswap(ctx, rpcs, at)
	if r.Code != "" || math.Abs(r.Price-fxUniswapPrice) > 1e-12 || r.Tick != fxUniswapTick || r.SqrtPriceX96 != fxUniswapSqrtPriceX96 || !r.ReadAt.Equal(at) {
		t.Errorf("reading = %+v", r)
	}
	if *calls != 1 {
		t.Errorf("slot0 read made %d RPC calls, want 1", *calls)
	}
	q := r.quote()
	if q.Venue != "uniswap" || q.Pair != "USDC-USDT-100" || q.Base != "USDC" || q.Quote != "USDT" || q.Bid != r.Price || q.Ask != r.Price ||
		!q.TS.Equal(at) || q.Code != "" {
		t.Errorf("quote = %+v", q)
	}
	// Pool identity: token0 USDC, token1 USDT, fee tier 100 (0.01%).
	if code := checkStableUniswapPool(ctx, rpcs); code != "" {
		t.Errorf("pool check = %q", code)
	}
	wrong, _ := newUniswapRPCStub(t, map[string]string{"0x0dfe1681": "0x" + strings.Repeat("0", 24) + strings.Repeat("1", 40)})
	if code := checkStableUniswapPool(ctx, func(string) []string { return []string{wrong} }); code != "bad_response" {
		t.Errorf("pool with another token0 = %q, want bad_response", code)
	}
	fee, _ := newUniswapRPCStub(t, map[string]string{"0xddca3f43": "0x" + strings.Repeat("0", 61) + "1f4"})
	if code := checkStableUniswapPool(ctx, func(string) []string { return []string{fee} }); code != "bad_response" {
		t.Errorf("pool with fee 500 = %q, want bad_response", code)
	}
	// RPC failures carry the shared codes; no RPC configured is unavailable.
	reverted, _ := newUniswapRPCStub(t, map[string]string{"0x3850c7bd": "error"})
	if r := readStableUniswap(ctx, func(string) []string { return []string{reverted} }, at); r.Code != "bad_response" || r.Price != 0 {
		t.Errorf("reverted slot0 = %+v", r)
	}
	if r := readStableUniswap(ctx, func(string) []string { return nil }, at); r.Code != "unavailable" {
		t.Errorf("no RPC = %+v", r)
	}
	if q := (stableUniswapReading{Code: "timeout", ReadAt: at}).quote(); q.Code != "timeout" || q.Venue != "uniswap" {
		t.Errorf("failed reading quote = %+v", q)
	}
}
