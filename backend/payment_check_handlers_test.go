package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	handlerTestEVMAddr  = "0x1111111111111111111111111111111111111111"
	handlerTestEVMPayer = "0x222200000000000000000000000000000000abcd"
	handlerTestEVMMimic = "0x222299999999999999999999999999999999abcd"
	handlerTestTronAddr = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	handlerTestTronPay  = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
)

func newTestPaymentAPIHandler(t *testing.T, rpcs map[string][]string, blockscouts map[string]string) *APIHandler {
	t.Helper()
	origLim := tronGridLimiter
	tronGridLimiter = rateLimiterUnlimited()
	t.Cleanup(func() { tronGridLimiter = origLim })
	store := newTestRiskStore(t)
	svc := newAddressRiskService(store, rpcs["eth"])
	svc.chainRPCs = rpcs
	svc.blockscoutURLs = blockscouts
	return &APIHandler{
		cache: NewCache(10 * time.Minute),
		risk:  svc,
	}
}

func TestPaymentCheckValidation(t *testing.T) {
	h := newTestPaymentAPIHandler(t, nil, nil)

	cases := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "USDC on tron rejected",
			method:     http.MethodGet,
			target:     "/api/opendata/crypto/payment-check/live?asset=USDC&network=tron&address=" + handlerTestTronAddr,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "unsupported asset/network",
		},
		{
			name:       "TRON address on eth rejected",
			method:     http.MethodGet,
			target:     "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address=" + handlerTestTronAddr,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "enter a 0x address (42 characters)",
		},
		{
			name:       "missing asset rejected",
			method:     http.MethodGet,
			target:     "/api/opendata/crypto/payment-check/history?network=eth&address=" + handlerTestEVMAddr,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "unsupported asset/network",
		},
		{
			name:       "POST rejected",
			method:     http.MethodPost,
			target:     "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address=" + handlerTestEVMAddr,
			wantStatus: http.StatusMethodNotAllowed,
			wantSubstr: "method not allowed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			rec := httptest.NewRecorder()
			if strings.Contains(tc.target, "/history") {
				h.PaymentCheckHistory(rec, req)
			} else {
				h.PaymentCheckLive(rec, req)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
			if strings.Contains(rec.Body.String(), handlerTestTronAddr) || strings.Contains(rec.Body.String(), handlerTestEVMAddr) {
				t.Fatalf("error body must never echo user address: %q", rec.Body.String())
			}
		})
	}
}

func TestPaymentCheckLiveLatestSkipsDust(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			var tag string
			_ = json.Unmarshal(req.Params[0], &tag)
			switch tag {
			case "latest":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
			case "safe":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x5a","timestamp":"0x%x"}}`, nowSec-300)
			case "finalized":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x50","timestamp":"0x%x"}}`, nowSec-900)
			}
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			// Two incoming transfers: older 250 USDT from real payer at block 92, newer 0.05 USDT dust from lookalike at block 98
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x000000000000000000000000000000000000000000000000000000000ee6b280",
					"blockNumber":"0x5c",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xreal100"
				},
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x000000000000000000000000000000000000000000000000000000000000c350",
					"blockNumber":"0x62",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xdust005"
				}
			]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-120,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMMimic),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-10,
			)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil)
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Latest == nil {
		t.Fatalf("expected non-nil Latest")
	}
	if resp.Latest.TxHash != "0xreal100" || resp.Latest.Amount != "250" {
		t.Fatalf("Latest = %+v, want 0xreal100 (250 USDT, skipping dust)", resp.Latest)
	}
	foundPoisonAlert := false
	for _, a := range resp.Alerts {
		if a.Code == "poisoning_received" && a.TxHash == "0xdust005" {
			foundPoisonAlert = true
		}
	}
	if !foundPoisonAlert {
		t.Fatalf("Alerts = %+v, expected poisoning_received alert for 0xdust005", resp.Alerts)
	}
}

func TestPaymentCheckLiveOwnAddressFrozen(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			var callObj struct {
				Data string `json:"data"`
			}
			_ = json.Unmarshal(req.Params[0], &callObj)
			if strings.HasPrefix(strings.TrimPrefix(callObj.Data, "0x"), "e47d6060") {
				// Own address is frozen by USDT contract
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000001"}`)
				return
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000005f5e100"}`)
		case "eth_getLogs":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
					"blockNumber":"0x60",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xincoming1"
				}
			]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-30,
			)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil)
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Latest == nil || resp.Latest.TxHash != "0xincoming1" {
		t.Fatalf("Latest = %+v, want 0xincoming1 even when own address is frozen", resp.Latest)
	}
	foundFrozen := false
	for _, a := range resp.Alerts {
		if a.Severity == "critical" && a.Code == "own_address_frozen" {
			foundFrozen = true
		}
	}
	if !foundFrozen {
		t.Fatalf("Alerts = %+v, expected critical own_address_frozen alert", resp.Alerts)
	}
}

func TestPaymentCheckLiveBalanceErrorIsolated(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			var callObj struct {
				Data string `json:"data"`
			}
			_ = json.Unmarshal(req.Params[0], &callObj)
			if strings.HasPrefix(strings.TrimPrefix(callObj.Data, "0x"), "70a08231") {
				// Fail balanceOf calls
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
					"blockNumber":"0x60",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xincoming2"
				}
			]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-20,
			)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil)
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.BalanceError == "" {
		t.Fatalf("expected non-empty BalanceError when balanceOf fails")
	}
	if resp.Heads.Latest != 100 {
		t.Fatalf("Heads.Latest = %d, want 100", resp.Heads.Latest)
	}
	if resp.Latest == nil || resp.Latest.TxHash != "0xincoming2" {
		t.Fatalf("Latest = %+v, want 0xincoming2", resp.Latest)
	}
}

func TestPaymentCheckHistoryNotCachedOnError(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
	}))
	defer rpcSrv.Close()

	var bsCalls atomic.Int32
	var shouldFail atomic.Bool
	shouldFail.Store(true)

	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bsCalls.Add(1)
		if shouldFail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		action := r.URL.Query().Get("action")
		if action == "tokentx" {
			fmt.Fprintf(w, `{"status":"1","message":"OK","result":[
				{
					"hash":"0xhist1",
					"from":%q,
					"to":%q,
					"value":"100000000",
					"timeStamp":"%d",
					"contractAddress":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"blockNumber":"90",
					"tokenSymbol":"USDT",
					"tokenDecimal":"6"
				}
			]}`, handlerTestEVMPayer, handlerTestEVMAddr, nowSec-60)
			return
		}
		fmt.Fprint(w, `{"status":"1","message":"OK","result":[]}`)
	}))
	defer bsSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, map[string]string{"eth": bsSrv.URL})

	// 1st call fails
	rec1 := httptest.NewRecorder()
	h.PaymentCheckHistory(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("1st status = %d", rec1.Code)
	}
	var resp1 payHistoryResponse
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	if len(resp1.Txs) != 0 {
		t.Fatalf("expected 0 txs on error, got %d", len(resp1.Txs))
	}

	// 2nd call succeeds because error was not cached
	shouldFail.Store(false)
	rec2 := httptest.NewRecorder()
	h.PaymentCheckHistory(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	var resp2 payHistoryResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if len(resp2.Txs) != 1 || resp2.Txs[0].TxHash != "0xhist1" {
		t.Fatalf("2nd response Txs = %+v, want [0xhist1]", resp2.Txs)
	}
}

func TestPaymentCheckLiveSeesNewTronIncomingDespiteHistoryCache(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	nowSec := nowMs / 1000

	// Subtest 1: TRON USDT
	t.Run("tron_usdt", func(t *testing.T) {
		var serveNewTx atomic.Bool
		tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/wallet/getnowblock":
				fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":1000,"timestamp":%d}}}`, nowMs)
			case r.URL.Path == "/walletsolidity/getnowblock":
				fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":980,"timestamp":%d}}}`, nowMs-60000)
			case r.URL.Path == "/wallet/triggerconstantcontract" || r.URL.Path == "/walletsolidity/triggerconstantcontract":
				fmt.Fprint(w, `{"result":{"result":true},"constant_result":["0000000000000000000000000000000000000000000000000000000000000000"]}`)
			case strings.HasSuffix(r.URL.Path, "/transactions/trc20"):
				if serveNewTx.Load() && r.URL.Query().Get("limit") == "20" {
					fmt.Fprintf(w, `{"data":[{
						"transaction_id":"tron_new_tx",
						"block_timestamp":%d,
						"from":%q,
						"to":%q,
						"type":"Transfer",
						"value":"50000000",
						"token_info":{"symbol":"USDT","address":"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t","decimals":6}
					}],"meta":{}}`, nowMs-3000, handlerTestTronPay, handlerTestTronAddr)
					return
				}
				fmt.Fprint(w, `{"data":[],"meta":{}}`)
			case strings.HasSuffix(r.URL.Path, "/transactions"):
				fmt.Fprint(w, `{"data":[],"meta":{}}`)
			case r.URL.Path == "/wallet/gettransactioninfobyid":
				fmt.Fprint(w, `{"blockNumber":998}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer tronSrv.Close()

		origTronURL := tronGridBaseURL
		tronGridBaseURL = tronSrv.URL
		defer func() { tronGridBaseURL = origTronURL }()

		h := newTestPaymentAPIHandler(t, nil, nil)

		// 1st /live (history cache empty, recent empty)
		rec1 := httptest.NewRecorder()
		h.PaymentCheckLive(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=tron&address="+handlerTestTronAddr, nil))
		var resp1 payLiveResponse
		_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
		if resp1.Latest != nil {
			t.Fatalf("1st Latest = %+v, want nil", resp1.Latest)
		}

		// Also populate 60s history cache with empty history
		recHist := httptest.NewRecorder()
		h.PaymentCheckHistory(recHist, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=tron&address="+handlerTestTronAddr, nil))

		// Now recent endpoint returns a new incoming transfer within the 60s history cache TTL
		serveNewTx.Store(true)
		rec2 := httptest.NewRecorder()
		h.PaymentCheckLive(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=tron&address="+handlerTestTronAddr, nil))
		var resp2 payLiveResponse
		_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
		if resp2.Latest == nil || resp2.Latest.TxHash != "tron_new_tx" {
			t.Fatalf("2nd Latest = %+v, want tron_new_tx", resp2.Latest)
		}
	})

	// Subtest 2: EVM native ETH
	t.Run("evm_eth", func(t *testing.T) {
		rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Method {
			case "eth_getBlockByNumber":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
			case "eth_getBalance":
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`)
			}
		}))
		defer rpcSrv.Close()

		var serveNewETH atomic.Bool
		bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			action := r.URL.Query().Get("action")
			offset := r.URL.Query().Get("offset")
			if serveNewETH.Load() && action == "txlist" && offset == "20" {
				fmt.Fprintf(w, `{"status":"1","message":"OK","result":[{
					"hash":"0xeth_new_tx",
					"from":%q,
					"to":%q,
					"value":"1000000000000000000",
					"isError":"0",
					"timeStamp":"%d",
					"blockNumber":"99"
				}]}`, handlerTestEVMPayer, handlerTestEVMAddr, nowSec-5)
				return
			}
			fmt.Fprint(w, `{"status":"1","message":"OK","result":[]}`)
		}))
		defer bsSrv.Close()

		h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, map[string]string{"eth": bsSrv.URL})

		rec1 := httptest.NewRecorder()
		h.PaymentCheckLive(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=ETH&network=eth&address="+handlerTestEVMAddr, nil))
		var resp1 payLiveResponse
		_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
		if resp1.Latest != nil {
			t.Fatalf("1st ETH Latest = %+v, want nil", resp1.Latest)
		}

		// Populate 60s history cache
		recHist := httptest.NewRecorder()
		h.PaymentCheckHistory(recHist, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=ETH&network=eth&address="+handlerTestEVMAddr, nil))

		serveNewETH.Store(true)
		rec2 := httptest.NewRecorder()
		h.PaymentCheckLive(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=ETH&network=eth&address="+handlerTestEVMAddr, nil))
		var resp2 payLiveResponse
		_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
		if resp2.Latest == nil || resp2.Latest.TxHash != "0xeth_new_tx" {
			t.Fatalf("2nd ETH Latest = %+v, want 0xeth_new_tx", resp2.Latest)
		}
	})
}

func TestPaymentCheckLevelsRecomputedFromCache(t *testing.T) {
	nowSec := time.Now().Unix()
	var safeHex, finalizedHex atomic.Value
	safeHex.Store("0x5a")      // 90 initially
	finalizedHex.Store("0x50") // 80 initially

	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "eth_getBlockByNumber" {
			var tag string
			_ = json.Unmarshal(req.Params[0], &tag)
			switch tag {
			case "latest":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
			case "safe":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":%q,"timestamp":"0x%x"}}`, safeHex.Load().(string), nowSec-120)
			case "finalized":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":%q,"timestamp":"0x%x"}}`, finalizedHex.Load().(string), nowSec-600)
			}
		}
	}))
	defer rpcSrv.Close()

	var bsCalls atomic.Int32
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bsCalls.Add(1)
		if r.URL.Query().Get("action") == "tokentx" {
			// Transfer at block 95 (0x5f)
			fmt.Fprintf(w, `{"status":"1","message":"OK","result":[{
				"hash":"0xrecomp",
				"from":%q,
				"to":%q,
				"value":"100000000",
				"timeStamp":"%d",
				"contractAddress":"0xdac17f958d2ee523a2206206994597c13d831ec7",
				"blockNumber":"95",
				"tokenSymbol":"USDT",
				"tokenDecimal":"6"
			}]}`, handlerTestEVMPayer, handlerTestEVMAddr, nowSec-30)
			return
		}
		fmt.Fprint(w, `{"status":"1","message":"OK","result":[]}`)
	}))
	defer bsSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, map[string]string{"eth": bsSrv.URL})

	// 1st /history: block 95 > safe (90) -> SOFT
	rec1 := httptest.NewRecorder()
	h.PaymentCheckHistory(rec1, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	var resp1 payHistoryResponse
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	if len(resp1.Txs) != 1 || resp1.Txs[0].Level != levelSoft {
		t.Fatalf("1st /history Txs = %+v, want level SOFT", resp1.Txs)
	}
	if bsCalls.Load() != 2 { // tokentx + txlist
		t.Fatalf("expected 2 Blockscout calls on cache miss, got %d", bsCalls.Load())
	}

	// Advance safe and finalized heads to 100 (0x64), >= block 95, and expire 4s heads cache
	safeHex.Store("0x64")
	finalizedHex.Store("0x64")
	h.cache.Delete("pay:heads:eth")

	// 2nd /history: hits 60s history cache (no new Blockscout calls!), but recomputes level as FINALIZED
	rec2 := httptest.NewRecorder()
	h.PaymentCheckHistory(rec2, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	var resp2 payHistoryResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if bsCalls.Load() != 2 {
		t.Fatalf("expected history cache hit (still 2 Blockscout calls), got %d", bsCalls.Load())
	}
	if len(resp2.Txs) != 1 || resp2.Txs[0].Level != levelFinalized || resp2.Txs[0].Progress != 100 {
		t.Fatalf("2nd /history Txs = %+v, want level FINALIZED (100%%)", resp2.Txs)
	}
}

func TestPaymentCheckLiveNeverFetchesHistory(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			var tag string
			_ = json.Unmarshal(req.Params[0], &tag)
			switch tag {
			case "latest":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
			case "safe":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x5a","timestamp":"0x%x"}}`, nowSec-540)
			case "finalized":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x50","timestamp":"0x%x"}}`, nowSec-960)
			}
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			// Return an unsettled log WITHOUT blockTimestamp (Timestamp == "") at block 99 (0x63 > safe)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[{
				"address":"0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9",
				"topics":[%q,%q,%q],
				"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
				"blockNumber":"0x63",
				"transactionHash":"0xarblog1"
			}]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
			)
		}
	}))
	defer rpcSrv.Close()

	var historyOffset1000Calls atomic.Int32
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "1000" {
			historyOffset1000Calls.Add(1)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
	defer bsSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"arb": {rpcSrv.URL}}, map[string]string{"arb": bsSrv.URL})

	start := time.Now()
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=arb&address="+handlerTestEVMAddr, nil))
	elapsed := time.Since(start)

	if elapsed >= 1*time.Second {
		t.Fatalf("/live took %v, expected < 1s when Blockscout hangs", elapsed)
	}
	if got := historyOffset1000Calls.Load(); got != 0 {
		t.Fatalf("offset=1000 history calls = %d, want 0", got)
	}
	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Latest == nil || resp.Latest.TxHash != "0xarblog1" {
		t.Fatalf("Latest = %+v, want 0xarblog1", resp.Latest)
	}
	// Verify unknown timestamp (blockTime == 0) handling on unsettled log:
	if resp.Latest.Level != levelSoft || resp.Latest.Progress != 10 {
		t.Fatalf("Latest with unknown blockTime: level=%s progress=%d, want SOFT / 10", resp.Latest.Level, resp.Latest.Progress)
	}
}

func TestPaymentCheckAccessLogOmitsAddress(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := newTestPaymentAPIHandler(t, nil, nil)
	logMW := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
			)
		})
	}

	secretAddr := "0xdeadbeef1234567890abcdef1234567890abcdef"
	for _, path := range []string{
		"/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address=" + secretAddr,
		"/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address=" + secretAddr,
		"/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address=bad_" + secretAddr,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if strings.Contains(path, "/history") {
			logMW(h.PaymentCheckHistory).ServeHTTP(rec, req)
		} else {
			logMW(h.PaymentCheckLive).ServeHTTP(rec, req)
		}
	}

	logs := buf.String()
	if !strings.Contains(logs, "/api/opendata/crypto/payment-check/live") || !strings.Contains(logs, "/api/opendata/crypto/payment-check/history") {
		t.Fatalf("expected request paths in access log, got %s", logs)
	}
	if strings.Contains(strings.ToLower(logs), "deadbeef") {
		t.Fatalf("access log leaked queried address: %s", logs)
	}
}

func TestPaymentCheckHeadsUseLagSampler(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			var tag string
			_ = json.Unmarshal(req.Params[0], &tag)
			switch tag {
			case "latest":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
			case "safe":
				// Raw safeLag = 1000s
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x5a","timestamp":"0x%x"}}`, nowSec-1000)
			case "finalized":
				// Raw finalLag = 2000s
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x50","timestamp":"0x%x"}}`, nowSec-2000)
			}
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
		}
	}))
	defer rpcSrv.Close()

	// Seed the "base" sampler with two earlier samples (60s/900s and 80s/950s)
	// so the 3-sample median with (1000s/2000s) is 80s / 950s.
	origSampler := payLagSamplers["base"]
	s := &lagSampler{}
	now := time.Now()
	s.observe(now.Add(-2*time.Minute), 60, 900)
	s.observe(now.Add(-1*time.Minute), 80, 950)
	payLagSamplers["base"] = s
	defer func() { payLagSamplers["base"] = origSampler }()

	h := newTestPaymentAPIHandler(t, map[string][]string{"base": {rpcSrv.URL}}, nil)
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDC&network=base&address="+handlerTestEVMAddr, nil))

	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Heads.SafeLagSec != 80 || resp.Heads.FinalLagSec != 950 {
		t.Fatalf("Heads lag = (%d, %d), want sampler median (80, 950)", resp.Heads.SafeLagSec, resp.Heads.FinalLagSec)
	}
}

func TestPaymentCheckLocalErrorFixedCodeAndLiveMarksError(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
					"blockNumber":"0x60",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xincoming_local_err"
				}
			]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-20,
			)
		}
	}))
	defer rpcSrv.Close()

	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "tokentx" {
			fmt.Fprintf(w, `{"status":"1","message":"OK","result":[{
				"hash":"0xincoming_local_err",
				"from":%q,
				"to":%q,
				"value":"100000000",
				"timeStamp":"%d",
				"contractAddress":"0xdac17f958d2ee523a2206206994597c13d831ec7",
				"blockNumber":"96",
				"tokenSymbol":"USDT",
				"tokenDecimal":"6"
			}]}`, handlerTestEVMPayer, handlerTestEVMAddr, nowSec-20)
			return
		}
		fmt.Fprint(w, `{"status":"1","message":"OK","result":[]}`)
	}))
	defer bsSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, map[string]string{"eth": bsSrv.URL})
	// Drop stablecoin_events so store.listHits(ownAddr) succeeds on address_risk_hits,
	// while applyLocalHits -> store.riskPoolForAddresses fails with a raw SQLite error.
	if _, err := h.risk.store.db.Exec(`DROP TABLE stablecoin_events`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	// 1. /history must report local source status="error" with fixed error="unavailable" (never raw SQLite text)
	recHist := httptest.NewRecorder()
	h.PaymentCheckHistory(recHist, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	var histResp payHistoryResponse
	if err := json.Unmarshal(recHist.Body.Bytes(), &histResp); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	var histLocal *riskSource
	for i := range histResp.Sources {
		if histResp.Sources[i].ID == "local" {
			histLocal = &histResp.Sources[i]
		}
	}
	if histLocal == nil || histLocal.Status != "error" || histLocal.Error != "unavailable" {
		t.Fatalf("/history local source = %+v, want status=error error=unavailable", histLocal)
	}

	// 2. /live must also mark local source status="error" error="unavailable" when applyLocalHits fails
	recLive := httptest.NewRecorder()
	h.PaymentCheckLive(recLive, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))
	var liveResp payLiveResponse
	if err := json.Unmarshal(recLive.Body.Bytes(), &liveResp); err != nil {
		t.Fatalf("unmarshal live: %v", err)
	}
	var liveLocal *riskSource
	for i := range liveResp.Sources {
		if liveResp.Sources[i].ID == "local" {
			liveLocal = &liveResp.Sources[i]
		}
	}
	if liveLocal == nil || liveLocal.Status != "error" || liveLocal.Error != "unavailable" {
		t.Fatalf("/live local source = %+v, want status=error error=unavailable", liveLocal)
	}
}

func TestPaymentCheckLiveOneUSDTLookalikeAlert(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			// 1st log at block 95: 100 USDT from real counterparty
			// 2nd log at block 96: 1 USDT (non-dust!) from lookalike counterparty (G1)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
					"blockNumber":"0x5f",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xreal100"
				},
				{
					"address":"0xdac17f958d2ee523a2206206994597c13d831ec7",
					"topics":[%q,%q,%q],
					"data":"0x00000000000000000000000000000000000000000000000000000000000f4240",
					"blockNumber":"0x60",
					"blockTimestamp":"0x%x",
					"transactionHash":"0xlookalike1usdt"
				}
			]}`,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMPayer),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-120,
				erc20TransferTopic,
				"0x"+pad32EVMAddr(handlerTestEVMMimic),
				"0x"+pad32EVMAddr(handlerTestEVMAddr),
				nowSec-30,
			)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))

	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Settlement card selection rule is unchanged: 1 USDT is non-dust so Latest is 0xlookalike1usdt
	if resp.Latest == nil || resp.Latest.TxHash != "0xlookalike1usdt" {
		t.Fatalf("Latest = %+v, want 0xlookalike1usdt", resp.Latest)
	}
	foundAlert := false
	for _, a := range resp.Alerts {
		if a.Severity == "warning" && a.Code == "poisoning_received" && a.TxHash == "0xlookalike1usdt" {
			foundAlert = true
		}
	}
	if !foundAlert {
		t.Fatalf("Alerts = %+v, expected warning poisoning_received for 1 USDT lookalike transfer", resp.Alerts)
	}
}

func TestPaymentCheckLiveSentToLookalikeAlert(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	// Seed history cache with:
	// 1. Earlier incoming from real counterparty (block 90)
	// 2. Recent outgoing of 500 USDT to lookalike counterparty (block 95, within 24h)
	histKey := "pay:hist:eth:USDT:" + handlerTestEVMAddr
	h.cache.SetWithTTL(histKey, &cachedPayHistory{
		txs: []payTx{
			{
				TxHash:        "0xsent_wrong",
				Direction:     "out",
				Timestamp:     time.Unix(nowSec-60, 0).UTC().Format(time.RFC3339),
				Amount:        "500",
				Symbol:        "USDT",
				TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
				TokenTier:     tierNative,
				Counterparty:  handlerTestEVMMimic,
				Block:         95,
				rawValue:      "500000000",
			},
			{
				TxHash:        "0xreal_in",
				Direction:     "in",
				Timestamp:     time.Unix(nowSec-3600, 0).UTC().Format(time.RFC3339),
				Amount:        "500",
				Symbol:        "USDT",
				TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
				TokenTier:     tierNative,
				Counterparty:  handlerTestEVMPayer,
				Block:         90,
				rawValue:      "500000000",
			},
		},
	}, payHistoryCacheTTL)

	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))

	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	foundSentAlert := false
	for _, a := range resp.Alerts {
		if a.Severity == "critical" && a.Code == "sent_to_lookalike" && a.TxHash == "0xsent_wrong" {
			foundSentAlert = true
		}
	}
	if !foundSentAlert {
		t.Fatalf("Alerts = %+v, expected critical sent_to_lookalike alert for 0xsent_wrong", resp.Alerts)
	}
}

func TestPaymentCheckLiveDedupesAlertPerTxHash(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	// Seed history cache with:
	// 1. Earlier incoming from real counterparty (block 90)
	// 2. Counterfeit USDT transfer from lookalike counterparty (block 95, within 24h)
	//    -> carries BOTH counterfeit_token and lookalike flags on the same tx_hash.
	histKey := "pay:hist:eth:USDT:" + handlerTestEVMAddr
	h.cache.SetWithTTL(histKey, &cachedPayHistory{
		txs: []payTx{
			{
				TxHash:        "0xfake_and_lookalike",
				Direction:     "in",
				Timestamp:     time.Unix(nowSec-60, 0).UTC().Format(time.RFC3339),
				Amount:        "1000",
				Symbol:        "USDT",
				TokenContract: "0x1111111111111111111111111111111111111111",
				TokenTier:     tierCounterfeit,
				Counterparty:  handlerTestEVMMimic,
				Block:         95,
				rawValue:      "1000000000",
			},
			{
				TxHash:        "0xreal_in",
				Direction:     "in",
				Timestamp:     time.Unix(nowSec-3600, 0).UTC().Format(time.RFC3339),
				Amount:        "500",
				Symbol:        "USDT",
				TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
				TokenTier:     tierNative,
				Counterparty:  handlerTestEVMPayer,
				Block:         90,
				rawValue:      "500000000",
			},
		},
	}, payHistoryCacheTTL)

	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))

	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var matching []payAlert
	for _, a := range resp.Alerts {
		if a.TxHash == "0xfake_and_lookalike" {
			matching = append(matching, a)
		}
	}
	if len(matching) != 1 || matching[0].Code != "counterfeit_received" || matching[0].Severity != "critical" {
		t.Fatalf("Alerts for 0xfake_and_lookalike = %+v, want exactly 1 critical counterfeit_received alert", matching)
	}
}

func TestLiveAlertForLookalikeKnown(t *testing.T) {
	nowSec := time.Now().Unix()
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x64","timestamp":"0x%x"}}`, nowSec)
		case "eth_call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000000"}`)
		case "eth_getLogs":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
		}
	}))
	defer rpcSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"eth": {rpcSrv.URL}}, nil)
	const knownPoisoner = "0xdead00000000000000000000000000000000beef"
	if err := h.risk.store.upsertLookalikes(context.Background(), "eth", []lookalikeRow{{
		Lookalike: knownPoisoner,
		Imitated:  handlerTestEVMPayer,
		Hits:      10,
		Victims:   5,
	}}, "2026-10-06"); err != nil {
		t.Fatalf("upsertLookalikes: %v", err)
	}

	// Seed history cache with:
	// 1. Incoming 0-value transfer from knownPoisoner (no prior real counterparty in cache!) -> poisoning_received (warning)
	// 2. Real outgoing transfer to knownPoisoner -> sent_to_lookalike (critical)
	histKey := "pay:hist:eth:USDT:" + handlerTestEVMAddr
	h.cache.SetWithTTL(histKey, &cachedPayHistory{
		txs: []payTx{
			{
				TxHash:        "0xknown_poison_out",
				Direction:     "out",
				Timestamp:     time.Unix(nowSec-30, 0).UTC().Format(time.RFC3339),
				Amount:        "200",
				Symbol:        "USDT",
				TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
				TokenTier:     tierNative,
				Counterparty:  knownPoisoner,
				Block:         96,
				rawValue:      "200000000",
			},
			{
				TxHash:        "0xknown_poison_in",
				Direction:     "in",
				Timestamp:     time.Unix(nowSec-60, 0).UTC().Format(time.RFC3339),
				Amount:        "0",
				Symbol:        "USDT",
				TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
				TokenTier:     tierNative,
				Counterparty:  knownPoisoner,
				Block:         95,
				rawValue:      "0",
			},
		},
	}, payHistoryCacheTTL)

	rec := httptest.NewRecorder()
	h.PaymentCheckLive(rec, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDT&network=eth&address="+handlerTestEVMAddr, nil))

	var resp payLiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var gotIn, gotOut bool
	for _, a := range resp.Alerts {
		if a.TxHash == "0xknown_poison_in" && a.Code == "poisoning_received" && a.Severity == "warning" {
			gotIn = true
		}
		if a.TxHash == "0xknown_poison_out" && a.Code == "sent_to_lookalike" && a.Severity == "critical" {
			gotOut = true
		}
	}
	if !gotIn || !gotOut {
		t.Fatalf("Alerts = %+v, want poisoning_received (warning) on 0xknown_poison_in and sent_to_lookalike (critical) on 0xknown_poison_out", resp.Alerts)
	}
}

func TestHandlePaymentCheckLiveAndHistoryBTCAndSolana(t *testing.T) {
	const (
		btcSelf   = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
		btcSender = "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"
		solSelf   = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		solSender = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	)
	nowSec := time.Now().Unix()

	origMempool, origDefault := mempoolBaseURL, defaultMempoolURLForFailover
	defer func() {
		mempoolBaseURL = origMempool
		defaultMempoolURLForFailover = origDefault
	}()

	// Mock Esplora server as BOTH mempoolBaseURL and defaultMempoolURLForFailover so default failover host list is active!
	btcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/blocks":
			w.Write([]byte(`[
				{"height":970600,"timestamp":` + fmt.Sprintf("%d", nowSec) + `},
				{"height":970599,"timestamp":` + fmt.Sprintf("%d", nowSec-600) + `},
				{"height":970598,"timestamp":` + fmt.Sprintf("%d", nowSec-1200) + `},
				{"height":970597,"timestamp":` + fmt.Sprintf("%d", nowSec-1800) + `},
				{"height":970596,"timestamp":` + fmt.Sprintf("%d", nowSec-2400) + `},
				{"height":970595,"timestamp":` + fmt.Sprintf("%d", nowSec-3000) + `}
			]`))
		case "/api/address/" + btcSelf:
			w.Write([]byte(`{
				"chain_stats":{"funded_txo_sum":100000000,"spent_txo_sum":0},
				"mempool_stats":{"funded_txo_sum":25000000,"spent_txo_sum":0}
			}`))
		case "/api/address/" + btcSelf + "/txs":
			// 0-conf incoming with RBF + 6-conf incoming
			w.Write([]byte(`[
				{"txid":"tx_0conf","status":{"confirmed":false},"vin":[{"sequence":4294967293,"prevout":{"scriptpubkey_address":"` + btcSender + `","value":25000000}}],"vout":[{"scriptpubkey_address":"` + btcSelf + `","value":25000000}]},
				{"txid":"tx_6conf","status":{"confirmed":true,"block_height":970595,"block_time":` + fmt.Sprintf("%d", nowSec-3000) + `},"vin":[{"sequence":4294967295,"prevout":{"scriptpubkey_address":"` + btcSender + `","value":100000000}}],"vout":[{"scriptpubkey_address":"` + btcSelf + `","value":100000000}]}
			]`))
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer btcSrv.Close()
	mempoolBaseURL = btcSrv.URL
	defaultMempoolURLForFailover = btcSrv.URL

	// Mock Solana JSON-RPC server serving SOL + frozen USDC ATA + recent system transfer
	solSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getSlot":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":454805000}`))
		case "getBlockTime":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + fmt.Sprintf("%d", nowSec) + `}`))
		case "getBalance":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454805000},"value":2000000000}}`))
		case "getAccountInfo":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454805000},"value":{"data":{"parsed":{"info":{"tokenAmount":{"amount":"50000000","decimals":6}}}}}}}`))
		case "getMultipleAccounts":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454805000},"value":[{"data":{"parsed":{"info":{"state":"frozen"}}}}]}}`))
		case "getSignaturesForAddress":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[{"signature":"sol_sig_1","slot":454804999,"blockTime":` + fmt.Sprintf("%d", nowSec-10) + `,"err":null}]}`))
		case "getTransaction":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"slot":454804999,
				"blockTime":` + fmt.Sprintf("%d", nowSec-10) + `,
				"transaction":{"message":{
					"accountKeys":["` + solSender + `","` + solSelf + `","` + solanaSystemProgramID + `"],
					"instructions":[{"program":"system","parsed":{"type":"transfer","info":{"source":"` + solSender + `","destination":"` + solSelf + `","lamports":2000000000}}}]
				}},
				"meta":{"err":null}
			}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer solSrv.Close()

	h := newTestPaymentAPIHandler(t, map[string][]string{"sol": {solSrv.URL}}, nil)

	// 1. BTC /live: Latest selects unconfirmed 0-conf incoming (DANGER, rbf_signaled), Balance = Finalized 1, SafeDelta 0, LatestDelta +0.25
	recBTCLive := httptest.NewRecorder()
	h.PaymentCheckLive(recBTCLive, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=BTC&network=btc&address="+btcSelf, nil))
	if recBTCLive.Code != http.StatusOK {
		t.Fatalf("BTC /live code = %d, body = %s", recBTCLive.Code, recBTCLive.Body.String())
	}
	var btcLive payLiveResponse
	if err := json.Unmarshal(recBTCLive.Body.Bytes(), &btcLive); err != nil {
		t.Fatalf("unmarshal BTC /live: %v", err)
	}
	if btcLive.Latest == nil || btcLive.Latest.TxHash != "tx_0conf" || btcLive.Latest.Level != levelDanger || btcLive.Latest.TokenTier != tierNative {
		t.Fatalf("BTC Latest = %+v, want tx_0conf DANGER tierNative (S5)", btcLive.Latest)
	}

	// 2. BTC /history on default mempool URL: Scope.Hosts and Sources[0].Hosts must BOTH include blockstream.info!
	recBTCHist := httptest.NewRecorder()
	h.PaymentCheckHistory(recBTCHist, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/history?asset=BTC&network=btc&address="+btcSelf, nil))
	if recBTCHist.Code != http.StatusOK {
		t.Fatalf("BTC /history code = %d, body = %s", recBTCHist.Code, recBTCHist.Body.String())
	}
	var btcHist payHistoryResponse
	if err := json.Unmarshal(recBTCHist.Body.Bytes(), &btcHist); err != nil {
		t.Fatalf("unmarshal BTC /history: %v", err)
	}
	hasBlockstream := false
	for _, host := range btcHist.Scope.Hosts {
		if host == "blockstream.info" {
			hasBlockstream = true
		}
	}
	if !hasBlockstream {
		t.Fatalf("BTC /history Scope.Hosts = %v, want blockstream.info included on default mempool config", btcHist.Scope.Hosts)
	}

	// 3. Solana SOL /live: Latest selects native SOL transfer (SAFE)
	recSOLLive := httptest.NewRecorder()
	h.PaymentCheckLive(recSOLLive, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=SOL&network=sol&address="+solSelf, nil))
	if recSOLLive.Code != http.StatusOK {
		t.Fatalf("SOL /live code = %d, body = %s", recSOLLive.Code, recSOLLive.Body.String())
	}
	var solLive payLiveResponse
	if err := json.Unmarshal(recSOLLive.Body.Bytes(), &solLive); err != nil {
		t.Fatalf("unmarshal SOL /live: %v", err)
	}
	if solLive.Latest == nil || solLive.Latest.TxHash != "sol_sig_1" || solLive.Latest.Amount != "2" || solLive.Latest.TokenTier != tierNative {
		t.Fatalf("SOL Latest = %+v, want sol_sig_1 (2 SOL native)", solLive.Latest)
	}

	// 4. Solana USDC /live: frozen ATA triggers critical own_address_frozen alert!
	recUSDCLive := httptest.NewRecorder()
	h.PaymentCheckLive(recUSDCLive, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/payment-check/live?asset=USDC&network=sol&address="+solSelf, nil))
	if recUSDCLive.Code != http.StatusOK {
		t.Fatalf("Solana USDC /live code = %d, body = %s", recUSDCLive.Code, recUSDCLive.Body.String())
	}
	var usdcLive payLiveResponse
	if err := json.Unmarshal(recUSDCLive.Body.Bytes(), &usdcLive); err != nil {
		t.Fatalf("unmarshal Solana USDC /live: %v", err)
	}
	foundFrozen := false
	for _, a := range usdcLive.Alerts {
		if a.Code == "own_address_frozen" && a.Severity == "critical" {
			foundFrozen = true
		}
	}
	if !foundFrozen {
		t.Fatalf("Solana USDC Alerts = %+v, expected critical own_address_frozen alert", usdcLive.Alerts)
	}
}
