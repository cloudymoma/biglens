package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func rateLimiterUnlimited() *rate.Limiter {
	return rate.NewLimiter(rate.Inf, 100)
}

func loadPaymentFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "payment_check", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestFetchEVMHistoryTiers(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	tokentxFixture := loadPaymentFixture(t, "evm_tokentx_op_usdt.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "tokentx":
			w.Write(tokentxFixture)
		default:
			w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
		}
	}))
	defer srv.Close()

	since := time.Unix(1759500000, 0).UTC()
	txs, scope, err := fetchEVMHistory(context.Background(), srv.URL, "USDT", "op", userAddr, since)
	if err != nil {
		t.Fatalf("fetchEVMHistory: %v", err)
	}
	if scope.TokenTx != 5 {
		t.Errorf("scope.TokenTx = %d, want 5", scope.TokenTx)
	}
	if len(txs) != 5 {
		t.Fatalf("len(txs) = %d, want 5", len(txs))
	}

	// Row 0: native USD₮0 on Optimism
	if txs[0].TokenTier != tierNative || txs[0].Symbol != "USD₮0" || txs[0].Amount != "50" || txs[0].Direction != "in" {
		t.Errorf("txs[0] = %+v, want native USD₮0 50 in", txs[0])
	}
	// Row 1: bridged USDT on Optimism
	if txs[1].TokenTier != tierBridged || txs[1].Symbol != "Bridged USDT" || txs[1].Amount != "12.5" || txs[1].Direction != "in" {
		t.Errorf("txs[1] = %+v, want bridged Bridged USDT 12.5 in", txs[1])
	}
	// Row 2: counterfeit "USDT" from non-registry contract
	if txs[2].TokenTier != tierCounterfeit || txs[2].Symbol != `"USDT"` || txs[2].Amount != "99" {
		t.Errorf("txs[2] = %+v, want counterfeit \"USDT\" 99", txs[2])
	}
	// Row 4: unrelated DAI token
	if txs[4].TokenTier != tierOther || txs[4].Symbol != "DAI" || txs[4].Amount != "1" {
		t.Errorf("txs[4] = %+v, want other DAI 1", txs[4])
	}
}

func TestFetchEVMHistoryWindow(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	baseTS := int64(1759600000)
	since := time.Unix(baseTS-7*24*3600, 0).UTC()

	// Case 1: 2 rows in window + 1 row older than 7 days -> 2 kept, Truncated=false.
	srvSmall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "tokentx" {
			w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
			return
		}
		rows := []map[string]string{
			{"blockNumber": "100", "timeStamp": strconv.FormatInt(baseTS-60, 10), "hash": "0x01", "from": "0x2222222222222222222222222222222222222222", "to": userAddr, "contractAddress": usdtContract, "value": "1000000", "tokenSymbol": "USDT", "tokenDecimal": "6"},
			{"blockNumber": "99", "timeStamp": strconv.FormatInt(since.Unix(), 10), "hash": "0x02", "from": "0x2222222222222222222222222222222222222222", "to": userAddr, "contractAddress": usdtContract, "value": "2000000", "tokenSymbol": "USDT", "tokenDecimal": "6"},
			{"blockNumber": "98", "timeStamp": strconv.FormatInt(since.Unix()-1, 10), "hash": "0x03", "from": "0x2222222222222222222222222222222222222222", "to": userAddr, "contractAddress": usdtContract, "value": "3000000", "tokenSymbol": "USDT", "tokenDecimal": "6"},
		}
		res, _ := json.Marshal(rows)
		w.Write([]byte(`{"status":"1","message":"OK","result":` + string(res) + `}`))
	}))
	defer srvSmall.Close()

	txs, scope, err := fetchEVMHistory(context.Background(), srvSmall.URL, "USDT", "eth", userAddr, since)
	if err != nil {
		t.Fatalf("fetchEVMHistory small: %v", err)
	}
	if len(txs) != 2 || scope.Truncated {
		t.Errorf("small window: len=%d truncated=%v, want len=2 truncated=false", len(txs), scope.Truncated)
	}

	// Case 2: 1000 rows all within the 7-day window -> Truncated=true.
	srvFull := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "tokentx" {
			w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
			return
		}
		rows := make([]map[string]string, payHistoryMaxRows)
		for i := range rows {
			rows[i] = map[string]string{
				"blockNumber":     strconv.Itoa(2000 - i),
				"timeStamp":       strconv.FormatInt(baseTS-int64(i), 10),
				"hash":            fmt.Sprintf("0x%064x", i+1),
				"from":            "0x2222222222222222222222222222222222222222",
				"to":              userAddr,
				"contractAddress": usdtContract,
				"value":           "1000000",
				"tokenSymbol":     "USDT",
				"tokenDecimal":    "6",
			}
		}
		res, _ := json.Marshal(rows)
		w.Write([]byte(`{"status":"1","message":"OK","result":` + string(res) + `}`))
	}))
	defer srvFull.Close()

	txsFull, scopeFull, err := fetchEVMHistory(context.Background(), srvFull.URL, "USDT", "eth", userAddr, since)
	if err != nil {
		t.Fatalf("fetchEVMHistory full: %v", err)
	}
	if len(txsFull) != payHistoryMaxRows || !scopeFull.Truncated {
		t.Errorf("full window: len=%d truncated=%v, want len=%d truncated=true", len(txsFull), scopeFull.Truncated, payHistoryMaxRows)
	}
}

func TestFetchEVMHistoryFailedOutgoing(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	txlistFixture := loadPaymentFixture(t, "evm_txlist_failed_usdt.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "txlist":
			w.Write(txlistFixture)
		default:
			w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
		}
	}))
	defer srv.Close()

	since := time.Unix(1759500000, 0).UTC()
	txs, _, err := fetchEVMHistory(context.Background(), srv.URL, "USDT", "eth", userAddr, since)
	if err != nil {
		t.Fatalf("fetchEVMHistory: %v", err)
	}
	// Only the valid failed outgoing transfer to registry USDT contract is kept;
	// truncated calldata (row 2) and non-registry contract (row 3) are skipped.
	if len(txs) != 1 {
		t.Fatalf("len(txs) = %d (%+v), want 1", len(txs), txs)
	}
	got := txs[0]
	// 0x07735940 = 125,000,000 -> 125 USDT
	if !got.Failed || got.Direction != "out" || got.Counterparty != "0x7777777777777777777777777777777777777777" ||
		got.Amount != "125" || got.TokenTier != tierNative || got.Symbol != "USDT" {
		t.Errorf("failed outgoing tx = %+v, want Failed=true out 125 USDT to 0x7777...", got)
	}
}

func TestFetchEVMHistoryKeepsForgedOutgoing(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	tokentxFixture := loadPaymentFixture(t, "evm_tokentx_op_usdt.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "tokentx" {
			w.Write(tokentxFixture)
			return
		}
		w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
	}))
	defer srv.Close()

	txs, _, err := fetchEVMHistory(context.Background(), srv.URL, "USDT", "op", userAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchEVMHistory: %v", err)
	}
	var sawCounterfeitOut, sawZeroValueOut bool
	for _, tx := range txs {
		if tx.Direction == "out" && tx.TokenTier == tierCounterfeit && tx.Counterparty == "0x4444444444444444444444444444444444444444" {
			sawCounterfeitOut = true
		}
		if tx.Direction == "out" && tx.TokenTier == tierNative && tx.Amount == "0" && tx.Counterparty == "0x5555555555555555555555555555555555555555" {
			sawZeroValueOut = true
		}
	}
	if !sawCounterfeitOut || !sawZeroValueOut {
		t.Errorf("sawCounterfeitOut=%v sawZeroValueOut=%v in %+v", sawCounterfeitOut, sawZeroValueOut, txs)
	}
}

func TestFetchEVMUnsettledUsesRegistryAddresses(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	var gotAddresses []string
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []struct {
				Address []string `json:"address"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if len(req.Params) > 0 {
			gotAddresses = req.Params[0].Address
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	}))
	defer rpc.Close()

	_, err := fetchEVMUnsettled(context.Background(), []string{rpc.URL}, "USDT", "op", userAddr, 100, 200)
	if err != nil {
		t.Fatalf("fetchEVMUnsettled: %v", err)
	}
	want := []string{
		"0x01bff41798a0bcf287b996046ca68b395dbc1071",
		"0x94b008aa00579c1307b0ef2c499ad98a8ce58e58",
	}
	if !slices.Equal(gotAddresses, want) {
		t.Errorf("eth_getLogs address = %v, want %v", gotAddresses, want)
	}
}

func TestFetchEVMUnsettledSplitsRange(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	origSpan := payLogsMaxSpan
	payLogsMaxSpan = 10000
	defer func() { payLogsMaxSpan = origSpan }()

	type seg struct{ from, to uint64 }
	var segs []seg
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []struct {
				FromBlock string `json:"fromBlock"`
				ToBlock   string `json:"toBlock"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		fb, _ := parseHexUint64(req.Params[0].FromBlock)
		tb, _ := parseHexUint64(req.Params[0].ToBlock)
		segs = append(segs, seg{from: fb, to: tb})
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	}))
	defer rpc.Close()

	_, err := fetchEVMUnsettled(context.Background(), []string{rpc.URL}, "USDC", "arb", userAddr, 1001, 26000)
	if err != nil {
		t.Fatalf("fetchEVMUnsettled: %v", err)
	}
	want := []seg{
		{from: 1001, to: 11000},
		{from: 11001, to: 21000},
		{from: 21001, to: 26000},
	}
	if !slices.Equal(segs, want) {
		t.Errorf("segments = %+v, want %+v", segs, want)
	}
}

func TestFetchEVMHistoryLowercasesAddresses(t *testing.T) {
	const mixedUser = "0x1111111111111111111111111111111111111111"
	tokentxFixture := loadPaymentFixture(t, "evm_tokentx_op_usdt.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "tokentx" {
			w.Write(tokentxFixture)
			return
		}
		w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
	}))
	defer srv.Close()

	txs, _, err := fetchEVMHistory(context.Background(), srv.URL, "USDT", "op", mixedUser, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchEVMHistory: %v", err)
	}
	if len(txs) == 0 {
		t.Fatal("expected non-empty txs")
	}
	if txs[0].TxHash != strings.ToLower(txs[0].TxHash) ||
		txs[0].TokenContract != strings.ToLower(txs[0].TokenContract) ||
		txs[0].Counterparty != strings.ToLower(txs[0].Counterparty) {
		t.Errorf("addresses not lowercased: %+v", txs[0])
	}
}

func TestMergeTxsDedup(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	const senderAddr = "0x2222222222222222222222222222222222222222"
	const txHash = "0x9999999999999999999999999999999999999999999999999999999999999999"

	// RPC returns log with hex value 0x05f5e100 (100,000,000 -> 100 USDT) at block 21000500.
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[{
			"address":"` + usdtContract + `",
			"topics":[
				"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
				"0x0000000000000000000000002222222222222222222222222222222222222222",
				"0x0000000000000000000000001111111111111111111111111111111111111111"
			],
			"data":"0x0000000000000000000000000000000000000000000000000000000005f5e100",
			"blockNumber":"0x1406f44",
			"transactionHash":"` + txHash + `"
		}]}`))
	}))
	defer rpc.Close()

	unsettled, err := fetchEVMUnsettled(context.Background(), []string{rpc.URL}, "USDT", "eth", userAddr, 21000000, 21000600)
	if err != nil || len(unsettled) != 1 {
		t.Fatalf("fetchEVMUnsettled: len=%d err=%v", len(unsettled), err)
	}

	// Blockscout returns the same transfer with decimal value "100000000".
	bsRow := payTx{
		TxHash:        txHash,
		ExplorerURL:   "https://etherscan.io/tx/" + txHash,
		Direction:     "in",
		Timestamp:     "2026-10-05T12:00:00Z",
		Amount:        "100",
		Symbol:        "USDT",
		TokenContract: usdtContract,
		TokenTier:     tierNative,
		Counterparty:  senderAddr,
		Block:         21000500,
		rawValue:      "100000000",
	}

	merged := mergeTxs(unsettled, []payTx{bsRow})
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1: %+v", len(merged), merged)
	}
	if merged[0].Block != 0x1406f44 || merged[0].Timestamp != "2026-10-05T12:00:00Z" || merged[0].Amount != "100" {
		t.Errorf("merged[0] = %+v, want Block=21000004 Timestamp=2026-10-05T12:00:00Z Amount=100", merged[0])
	}
}

func TestMergeTxsKeepsDuplicateTransfersInOneTx(t *testing.T) {
	tx1 := payTx{
		TxHash:        "0xabc",
		Direction:     "in",
		Timestamp:     "2026-10-05T12:00:00Z",
		Amount:        "50",
		Symbol:        "USDT",
		TokenContract: usdtContract,
		TokenTier:     tierNative,
		Counterparty:  "0x2222222222222222222222222222222222222222",
		Block:         100,
		rawValue:      "50000000",
	}
	// Batch transfer inside one tx sends 50 USDT twice to the same recipient.
	fromRPC := []payTx{tx1, tx1}
	fromBlockscout := []payTx{tx1, tx1}

	merged := mergeTxs(fromRPC, fromBlockscout)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want 2 (duplicate transfers in same tx preserved)", len(merged))
	}
}

func TestDecodeTransferCalldataRejectsTruncated(t *testing.T) {
	cases := []string{
		"",
		"0x",
		"0xa9059cbb",
		"0xa9059cbb" + strings.Repeat("0", 127), // 8 + 127 = 135 hex chars
	}
	for _, tc := range cases {
		to20, amt, ok := decodeTransferCalldata(tc)
		if ok || to20 != "" || amt != nil {
			t.Errorf("decodeTransferCalldata(len=%d) = (%q, %v, %v), want ok=false", len(strings.TrimPrefix(tc, "0x")), to20, amt, ok)
		}
	}
}

func TestFetchTronHistoryPaging(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	page1 := loadPaymentFixture(t, "tron_trc20_page1.json")
	page2 := loadPaymentFixture(t, "tron_trc20_page2.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") == "" {
				w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
				return
			}
			if r.URL.Query().Get("fingerprint") == "fp_page_2" {
				w.Write(page2)
				return
			}
			w.Write(page1)
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600100000}}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, scope, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if len(txs) != 2 || scope.TRC20 != 2 {
		t.Fatalf("txs=%+v scope=%+v, want 2 rows across 2 pages", txs, scope)
	}
	if txs[0].TxHash != "tron_tx_page1_newest" || txs[1].TxHash != "tron_tx_page2_older" {
		t.Errorf("unexpected tx order: %+v", txs)
	}
}

func TestFetchTronHistoryFinalizedByTimestamp(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	page1 := loadPaymentFixture(t, "tron_trc20_page2.json") // timestamp 1759590000000
	var txInfoCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") != "" {
				w.Write(page1)
				return
			}
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			// Solidified timestamp is newer than the row's block_timestamp.
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600000000}}}`))
		case "/wallet/gettransactioninfobyid":
			txInfoCalls.Add(1)
			w.Write([]byte(`{"blockNumber":86840000}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	_, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if txInfoCalls.Load() != 0 {
		t.Errorf("gettransactioninfobyid called %d times for old finalized row, want 0", txInfoCalls.Load())
	}
}

func TestFetchTronHistoryCounterfeit(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	unfiltered := loadPaymentFixture(t, "tron_trc20_unfiltered.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") != "" {
				w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
				return
			}
			w.Write(unfiltered)
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600100000}}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("len(txs) = %d (%+v), want 1 counterfeit row", len(txs), txs)
	}
	if txs[0].TokenTier != tierCounterfeit || txs[0].Symbol != `"USD₮"` || txs[0].Amount != "88" {
		t.Errorf("counterfeit row = %+v, want tierCounterfeit Symbol=\"USD₮\" Amount=88", txs[0])
	}
}

func TestFetchTronHistoryFailedTRX(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	failedFixture := loadPaymentFixture(t, "tron_transactions_failed.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts/"+tronAddr+"/transactions" {
			w.Write(failedFixture)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, _, err := fetchTronHistory(context.Background(), "TRX", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory TRX: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("len(txs) = %d (%+v), want 1 TransferContract row", len(txs), txs)
	}
	if !txs[0].Failed || txs[0].TxHash != "tron_failed_trx_transfer" || txs[0].Amount != "25" || txs[0].Symbol != "TRX" {
		t.Errorf("failed TRX row = %+v, want Failed=true Amount=25 TRX", txs[0])
	}
}

func TestFetchTronHistoryUnfilteredPageKeepsOnlyNonRegistry(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	page1 := loadPaymentFixture(t, "tron_trc20_page2.json")
	unfiltered := loadPaymentFixture(t, "tron_trc20_unfiltered.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") != "" {
				w.Write(page1)
				return
			}
			// Unfiltered response contains BOTH an official USDT row ("tron_tx_page1_newest")
			// and a counterfeit row ("tron_tx_fake_usdt").
			w.Write(unfiltered)
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600100000}}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	for _, tx := range txs {
		if tx.TxHash == "tron_tx_page1_newest" {
			t.Fatalf("official USDT row from unfiltered request was not filtered out: %+v", txs)
		}
	}
	if len(txs) != 2 {
		t.Fatalf("len(txs) = %d (%+v), want 2 (1 from contract-filtered + 1 counterfeit from unfiltered)", len(txs), txs)
	}
}

func TestFetchTronHistoryFailedTRC20Outgoing(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	failedFixture := loadPaymentFixture(t, "tron_transactions_failed.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write(failedFixture)
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600100000}}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	// Only the failed TriggerSmartContract on the registry USDT contract is kept;
	// the failed call on a non-registry contract and the TRX transfer are ignored.
	if len(txs) != 1 {
		t.Fatalf("len(txs) = %d (%+v), want 1", len(txs), txs)
	}
	wantRecipient, _ := tronHexToBase58("74472e7d35395a6b50427e975f55e2e076457211")
	got := txs[0]
	// 0x02faf080 = 50,000,000 -> 50 USDT
	if !got.Failed || got.Direction != "out" || got.Counterparty != wantRecipient ||
		got.Amount != "50" || got.Block != 86844320 || got.TokenTier != tierNative {
		t.Errorf("failed TRC20 row = %+v, want Failed=true out 50 USDT to %s at block 86844320", got, wantRecipient)
	}
}

func TestFetchTronHistoryTxInfoEmptyKeepsBlockZero(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	page1 := loadPaymentFixture(t, "tron_trc20_page1.json") // timestamp 1759600000000

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") != "" && r.URL.Query().Get("fingerprint") == "" {
				// Return single row without next fingerprint
				w.Write([]byte(strings.ReplaceAll(string(page1), `"fingerprint": "fp_page_2",`, "")))
				return
			}
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			// Solidified timestamp is older than page1 row -> triggers gettransactioninfobyid.
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759599900000}}}`))
		case "/wallet/gettransactioninfobyid":
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if len(txs) != 1 || txs[0].Block != 0 {
		t.Errorf("txs = %+v, want 1 row with Block=0 when gettransactioninfobyid returns {}", txs)
	}
}

func TestFetchTronHistoryTxInfoCappedAtFive(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	origLimiter := tronGridLimiter
	tronGridLimiter = rateLimiterUnlimited()
	defer func() { tronGridLimiter = origLimiter }()

	var txInfoCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") == "" {
				w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
				return
			}
			// 8 unsolidified rows
			rows := make([]map[string]any, 8)
			for i := range rows {
				rows[i] = map[string]any{
					"transaction_id":  fmt.Sprintf("tx_unsolid_%d", i),
					"block_timestamp": int64(1759600000000 - i*1000),
					"from":            "TFp3Ls4mH7cjP2c9mG3tZ6uX8vW1nK2jR9",
					"to":              tronAddr,
					"type":            "Transfer",
					"value":           "1000000",
					"token_info": map[string]any{
						"symbol":   "USDT",
						"address":  tronAddr,
						"decimals": 6,
					},
				}
			}
			b, _ := json.Marshal(map[string]any{"data": rows, "success": true, "meta": map[string]any{}})
			w.Write(b)
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759599900000}}}`))
		case "/wallet/gettransactioninfobyid":
			n := txInfoCalls.Add(1)
			w.Write([]byte(fmt.Sprintf(`{"blockNumber":%d}`, 86844310-n)))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig := tronGridBaseURL
	tronGridBaseURL = srv.URL
	defer func() { tronGridBaseURL = orig }()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if txInfoCalls.Load() != 5 {
		t.Fatalf("gettransactioninfobyid calls = %d, want 5", txInfoCalls.Load())
	}
	for i := 0; i < 5; i++ {
		if txs[i].Block == 0 {
			t.Errorf("txs[%d].Block = 0, want > 0", i)
		}
	}
	for i := 5; i < 8; i++ {
		if txs[i].Block != 0 {
			t.Errorf("txs[%d].Block = %d, want 0 (capped at 5)", i, txs[i].Block)
		}
	}
}

func TestFetchTronHistoryFailedOutgoingSinglePage(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	var onlyFromCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/v1/accounts/" + tronAddr + "/transactions":
			if r.URL.Query().Get("only_from") == "true" {
				onlyFromCalls.Add(1)
			}
			// Return a full page of 200 rows with a fingerprint -> must NOT page further,
			// and must set scope.TruncatedFailedOutgoing = true.
			rows := make([]map[string]any, 200)
			for i := range rows {
				rows[i] = map[string]any{
					"txID":            fmt.Sprintf("tx_%d", i),
					"blockNumber":     86844300,
					"block_timestamp": int64(1759600000000),
					"ret":             []map[string]string{{"contractRet": "SUCCESS"}},
				}
			}
			b, _ := json.Marshal(map[string]any{
				"data":    rows,
				"success": true,
				"meta":    map[string]any{"fingerprint": "more_pages"},
			})
			w.Write(b)
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759600100000}}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	_, scope, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory: %v", err)
	}
	if onlyFromCalls.Load() != 1 {
		t.Errorf("only_from calls = %d, want 1", onlyFromCalls.Load())
	}
	if !scope.TruncatedFailedOutgoing {
		t.Errorf("scope.TruncatedFailedOutgoing = false, want true when 200 rows returned")
	}
}

func TestFetchEVMRecent(t *testing.T) {
	const userAddr = "0x1111111111111111111111111111111111111111"
	tokentxFixture := loadPaymentFixture(t, "evm_tokentx_op_usdt.json")
	var gotOffset string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotOffset = r.URL.Query().Get("offset")
		w.Write(tokentxFixture)
	}))
	defer srv.Close()

	txs, err := fetchEVMRecent(context.Background(), srv.URL, "USDT", "op", userAddr)
	if err != nil {
		t.Fatalf("fetchEVMRecent: %v", err)
	}
	if calls.Load() != 1 || gotOffset != "20" {
		t.Errorf("calls=%d offset=%q, want calls=1 offset=\"20\"", calls.Load(), gotOffset)
	}
	if len(txs) == 0 {
		t.Errorf("expected non-empty recent txs")
	}
}

func TestFetchTronRecent(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	page1 := loadPaymentFixture(t, "tron_trc20_page1.json")
	var trc20Calls, txCalls atomic.Int32
	var gotLimit string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			trc20Calls.Add(1)
			gotLimit = r.URL.Query().Get("limit")
			w.Write(page1) // has fingerprint, but fetchTronRecent must NOT page
		case "/v1/accounts/" + tronAddr + "/transactions":
			txCalls.Add(1)
			w.Write([]byte(`{"data":[],"success":true,"meta":{"fingerprint":"unused"}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL = srv.URL
	tronGridLimiter = rateLimiterUnlimited()
	defer func() {
		tronGridBaseURL = orig
		tronGridLimiter = origLim
	}()

	txs, err := fetchTronRecent(context.Background(), "USDT", tronAddr)
	if err != nil {
		t.Fatalf("fetchTronRecent: %v", err)
	}
	if trc20Calls.Load() != 1 || gotLimit != "20" {
		t.Errorf("trc20Calls=%d limit=%q, want 1 and \"20\"", trc20Calls.Load(), gotLimit)
	}
	if len(txs) != 1 {
		t.Errorf("len(txs) = %d, want 1", len(txs))
	}
}

func TestFetchTronHistoryNineSlowRequestsSucceed(t *testing.T) {
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	origLimiter := tronGridLimiter
	tronGridLimiter = rateLimiterUnlimited()
	defer func() { tronGridLimiter = origLimiter }()

	var totalReqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalReqs.Add(1)
		// Every request takes 1 second; 9 requests total (1 registry trc20 page +
		// 1 unfiltered trc20 page + 1 only_from page + 1 solidified block +
		// 5 gettransactioninfobyid calls). With per-request 8s timeout, an overall
		// 20s timeout, and concurrency, this must succeed well within budget.
		time.Sleep(time.Second)

		switch r.URL.Path {
		case "/v1/accounts/" + tronAddr + "/transactions/trc20":
			if r.URL.Query().Get("contract_address") == "" {
				w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
				return
			}
			rows := make([]map[string]any, 5)
			for i := range rows {
				rows[i] = map[string]any{
					"transaction_id":  fmt.Sprintf("tx_slow_%d", i),
					"block_timestamp": int64(1759600000000 - i*1000),
					"from":            "TFp3Ls4mH7cjP2c9mG3tZ6uX8vW1nK2jR9",
					"to":              tronAddr,
					"type":            "Transfer",
					"value":           "1000000",
					"token_info": map[string]any{
						"symbol":   "USDT",
						"address":  tronAddr,
						"decimals": 6,
					},
				}
			}
			b, _ := json.Marshal(map[string]any{"data": rows, "success": true, "meta": map[string]any{}})
			w.Write(b)
		case "/v1/accounts/" + tronAddr + "/transactions":
			w.Write([]byte(`{"data":[],"success":true,"meta":{}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844300,"timestamp":1759599900000}}}`))
		case "/wallet/gettransactioninfobyid":
			w.Write([]byte(`{"blockNumber":86844305}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	orig := tronGridBaseURL
	tronGridBaseURL = srv.URL
	defer func() { tronGridBaseURL = orig }()

	txs, _, err := fetchTronHistory(context.Background(), "USDT", tronAddr, time.Unix(1759500000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchTronHistory with 9 x 1s requests failed: %v", err)
	}
	if totalReqs.Load() != 9 {
		t.Errorf("totalReqs = %d, want 9", totalReqs.Load())
	}
	if len(txs) != 5 {
		t.Fatalf("len(txs) = %d, want 5", len(txs))
	}
	for i, tx := range txs {
		if tx.Block != 86844305 {
			t.Errorf("txs[%d].Block = %d, want 86844305", i, tx.Block)
		}
	}
}
