package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func uintHex32(v uint64) string {
	return "0x" + abiWord(int(v))
}

func TestFetchEVMBalancesThreeTags(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		var tag string
		_ = json.Unmarshal(req.Params[len(req.Params)-1], &tag)

		switch req.Method {
		case "eth_call":
			var callObj struct {
				To   string `json:"to"`
				Data string `json:"data"`
			}
			_ = json.Unmarshal(req.Params[0], &callObj)
			if !strings.HasPrefix(callObj.Data, "0x70a08231") {
				t.Errorf("unexpected calldata %q", callObj.Data)
			}
			// finalized = 100 USDT, safe = 150 USDT (+50), latest = 120 USDT (-30 from safe: pending outgoing).
			switch tag {
			case "finalized":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(100_000_000) + `"}`))
			case "safe":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(150_000_000) + `"}`))
			case "latest":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(120_000_000) + `"}`))
			}
		case "eth_getBalance":
			// finalized = 2 ETH, safe = 2 ETH (0), latest = 2.5 ETH (+0.5).
			switch tag {
			case "finalized", "safe":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1bc16d674ec80000"}`)) // 2e18
			case "latest":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x22b1c8c1227a0000"}`)) // 2.5e18
			}
		}
	}))
	defer rpc.Close()

	usdtRows := fetchEVMBalances(context.Background(), []string{rpc.URL}, "USDT", "eth", userAddr)
	if len(usdtRows) != 1 {
		t.Fatalf("eth USDT rows = %+v, want 1 row", usdtRows)
	}
	r0 := usdtRows[0]
	if r0.Error != "" || r0.Label != "USDT" || r0.Tier != tierNative ||
		r0.Finalized != "100" || r0.SafeDelta != "+50" || r0.LatestDelta != "-30" {
		t.Errorf("eth USDT row = %+v, want Finalized=100 SafeDelta=+50 LatestDelta=-30", r0)
	}

	ethRows := fetchEVMBalances(context.Background(), []string{rpc.URL}, "ETH", "eth", userAddr)
	if len(ethRows) != 1 {
		t.Fatalf("eth ETH rows = %+v, want 1 row", ethRows)
	}
	e0 := ethRows[0]
	if e0.Error != "" || e0.Label != "ETH" || e0.Contract != "" || e0.Tier != tierNative ||
		e0.Finalized != "2" || e0.SafeDelta != "0" || e0.LatestDelta != "+0.5" {
		t.Errorf("eth ETH row = %+v, want Finalized=2 SafeDelta=0 LatestDelta=+0.5", e0)
	}
}

func TestFetchEVMBalancesBridgedRow(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(strings.ToLower(string(b)), "0x01bff41798a0bcf287b996046ca68b395dbc1071") {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(25_000_000) + `"}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(10_500_000) + `"}`))
	}))
	defer rpc.Close()

	rows := fetchEVMBalances(context.Background(), []string{rpc.URL}, "USDT", "op", userAddr)
	if len(rows) != 2 {
		t.Fatalf("op USDT rows = %+v, want 2 rows (native USD₮0 + Bridged USDT)", rows)
	}
	if rows[0].Tier != tierNative || rows[0].Label != "USD₮0" || rows[0].Finalized != "25" {
		t.Errorf("row[0] = %+v, want native USD₮0 = 25", rows[0])
	}
	if rows[1].Tier != tierBridged || rows[1].Label != "Bridged USDT" || rows[1].Finalized != "10.5" {
		t.Errorf("row[1] = %+v, want Bridged USDT = 10.5", rows[1])
	}
}

func TestFetchEVMBalancesBridgedFailureKeepsNativeRow(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		// Bridged USDC.e on Arbitrum returns empty 0x (bad_response), while native USDC succeeds.
		if strings.Contains(strings.ToLower(string(b)), "0xff970a61a04b1ca14834a43f5de4533ebddb5cc8") {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x"}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(80_000_000) + `"}`))
	}))
	defer rpc.Close()

	rows := fetchEVMBalances(context.Background(), []string{rpc.URL}, "USDC", "arb", userAddr)
	if len(rows) != 2 {
		t.Fatalf("arb USDC rows = %+v, want 2 rows", rows)
	}
	if rows[0].Error != "" || rows[0].Finalized != "80" {
		t.Errorf("native row = %+v, want Finalized=80 and no error", rows[0])
	}
	if rows[1].Error != "bad_response" || rows[1].Finalized != "" {
		t.Errorf("bridged row = %+v, want Error=bad_response and empty Finalized", rows[1])
	}
}

func TestFetchEVMBalancesRPCFailover(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	var badCalls atomic.Int32
	badRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer badRPC.Close()

	goodRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + uintHex32(42_000_000) + `"}`))
	}))
	defer goodRPC.Close()

	rows := fetchEVMBalances(context.Background(), []string{badRPC.URL, goodRPC.URL}, "USDC", "base", userAddr)
	if len(rows) != 1 || rows[0].Error != "" || rows[0].Finalized != "42" {
		t.Fatalf("failover rows = %+v, want Finalized=42 without error", rows)
	}
	if badCalls.Load() < 3 {
		t.Errorf("badRPC calls = %d, want >= 3", badCalls.Load())
	}
}

func TestFetchTronBalancesInactiveAccount(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getaccount":
			// Activated only in latest block with 15 TRX (15,000,000 sun).
			w.Write([]byte(`{"address":"41a614f803b6fd780986a42c78ec9c7f77e6ded13c","balance":15000000}`))
		case "/walletsolidity/getaccount":
			// Unactivated at solidified block: TronGrid returns {} without "balance".
			w.Write([]byte(`{}`))
		case "/wallet/triggerconstantcontract":
			w.Write([]byte(`{"result":{"result":true},"constant_result":["` + abiWord(75_000_000) + `"]}`))
		case "/walletsolidity/triggerconstantcontract":
			w.Write([]byte(`{"result":{"result":true},"constant_result":["` + abiWord(50_000_000) + `"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	orig := tronGridBaseURL
	tronGridBaseURL = srv.URL
	defer func() { tronGridBaseURL = orig }()

	trxRows := fetchTronBalances(context.Background(), "TRX", tronAddr)
	if len(trxRows) != 1 {
		t.Fatalf("TRX rows = %+v, want 1 row", trxRows)
	}
	if trxRows[0].Error != "" || trxRows[0].Finalized != "0" || trxRows[0].SafeDelta != "" || trxRows[0].LatestDelta != "+15" {
		t.Errorf("TRX row = %+v, want Finalized=0 SafeDelta='' LatestDelta=+15", trxRows[0])
	}

	usdtRows := fetchTronBalances(context.Background(), "USDT", tronAddr)
	if len(usdtRows) != 1 {
		t.Fatalf("TRON USDT rows = %+v, want 1 row", usdtRows)
	}
	if usdtRows[0].Error != "" || usdtRows[0].Finalized != "50" || usdtRows[0].SafeDelta != "" || usdtRows[0].LatestDelta != "+25" {
		t.Errorf("TRON USDT row = %+v, want Finalized=50 SafeDelta='' LatestDelta=+25", usdtRows[0])
	}
}

func TestFetchBalancesAllRowsFail(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	badRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer badRPC.Close()

	rows := fetchEVMBalances(context.Background(), []string{badRPC.URL}, "USDT", "op", userAddr)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 rows", rows)
	}
	for i, r := range rows {
		if r.Error != "rate_limited" || r.Finalized != "" {
			t.Errorf("row[%d] = %+v, want Error=rate_limited and empty Finalized", i, r)
		}
	}
}
