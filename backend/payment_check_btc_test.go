package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConvertBTCTxIncomingOutgoingAndChange(t *testing.T) {
	const (
		selfAddr   = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
		senderA    = "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"
		senderB    = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
		recipient1 = "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
		recipient2 = "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"
	)

	// 1. Incoming tx with two inputs: senderB has larger input (80M > 30M) -> counterparty is senderB.
	inTx := esploraTx{
		TxID: "aa01",
		Status: esploraTxStatus{
			Confirmed:   true,
			BlockHeight: 970598,
			BlockTime:   1791446400,
		},
		Vin: []esploraVin{
			{Sequence: 0xffffffff, Prevout: &esploraPrevout{ScriptpubkeyAddress: senderA, Value: 30_000_000}},
			{Sequence: 0xffffffff, Prevout: &esploraPrevout{ScriptpubkeyAddress: senderB, Value: 80_000_000}},
		},
		Vout: []esploraVout{
			{ScriptpubkeyAddress: selfAddr, Value: 100_000_000}, // 1 BTC
			{ScriptpubkeyAddress: senderB, Value: 9_990_000},    // sender change ignored
		},
	}
	inRows := convertBTCTx(inTx, selfAddr)
	if len(inRows) != 1 {
		t.Fatalf("incoming rows = %+v, want 1", inRows)
	}
	if inRows[0].Direction != "in" || inRows[0].Amount != "1" || inRows[0].Symbol != "BTC" ||
		inRows[0].TokenTier != tierNative || inRows[0].Counterparty != senderB || inRows[0].Block != 970598 {
		t.Fatalf("unexpected incoming row: %+v", inRows[0])
	}

	// 2. Outgoing tx with 2 outputs to recipient1, 1 output to recipient2, and change back to selfAddr.
	outTx := esploraTx{
		TxID: "bb02",
		Status: esploraTxStatus{
			Confirmed:   true,
			BlockHeight: 970599,
			BlockTime:   1791447000,
		},
		Vin: []esploraVin{
			{Sequence: 0xfffffffe, Prevout: &esploraPrevout{ScriptpubkeyAddress: selfAddr, Value: 200_000_000}},
		},
		Vout: []esploraVout{
			{ScriptpubkeyAddress: recipient1, Value: 25_000_000},
			{ScriptpubkeyAddress: selfAddr, Value: 140_000_000},  // change filtered out
			{ScriptpubkeyAddress: recipient1, Value: 15_000_000}, // merged into recipient1 -> 0.4 BTC
			{ScriptpubkeyAddress: recipient2, Value: 19_980_000}, // 0.1998 BTC
		},
	}
	outRows := convertBTCTx(outTx, selfAddr)
	if len(outRows) != 2 {
		t.Fatalf("outgoing rows = %+v, want 2 (merged recipients, no change)", outRows)
	}
	if outRows[0].Direction != "out" || outRows[0].Counterparty != recipient1 || outRows[0].Amount != "0.4" || outRows[0].TokenTier != tierNative {
		t.Fatalf("outgoing[0] = %+v, want recipient1 0.4 BTC native", outRows[0])
	}
	if outRows[1].Direction != "out" || outRows[1].Counterparty != recipient2 || outRows[1].Amount != "0.1998" || outRows[1].TokenTier != tierNative {
		t.Fatalf("outgoing[1] = %+v, want recipient2 0.1998 BTC native", outRows[1])
	}

	// 3. Pure UTXO consolidation (self -> self only).
	consolTx := esploraTx{
		TxID: "cc03",
		Status: esploraTxStatus{
			Confirmed:   true,
			BlockHeight: 970600,
			BlockTime:   1791447600,
		},
		Vin: []esploraVin{
			{Sequence: 0xffffffff, Prevout: &esploraPrevout{ScriptpubkeyAddress: selfAddr, Value: 50_000_000}},
			{Sequence: 0xffffffff, Prevout: &esploraPrevout{ScriptpubkeyAddress: selfAddr, Value: 50_000_000}},
		},
		Vout: []esploraVout{
			{ScriptpubkeyAddress: selfAddr, Value: 99_990_000},
		},
	}
	consolRows := convertBTCTx(consolTx, selfAddr)
	if len(consolRows) != 1 || consolRows[0].Direction != "out" || consolRows[0].Counterparty != selfAddr || consolRows[0].Amount != "0.9999" {
		t.Fatalf("consolidation rows = %+v, want 1 self out row of 0.9999 BTC", consolRows)
	}
}

func TestConvertBTCTxRBFSignaled(t *testing.T) {
	const (
		selfAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
		sender   = "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"
	)

	mempoolRBF := esploraTx{
		TxID:   "mempool_rbf_01",
		Status: esploraTxStatus{Confirmed: false},
		Vin: []esploraVin{
			{Sequence: 0xfffffffd, Prevout: &esploraPrevout{ScriptpubkeyAddress: sender, Value: 50_000_000}},
		},
		Vout: []esploraVout{
			{ScriptpubkeyAddress: selfAddr, Value: 49_990_000},
		},
	}
	rows := convertBTCTx(mempoolRBF, selfAddr)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	if rows[0].Block != 0 || rows[0].Timestamp != "" {
		t.Errorf("unconfirmed tx got Block=%d Timestamp=%q, want 0 and empty", rows[0].Block, rows[0].Timestamp)
	}
	if !slices.Contains(rows[0].Flags, "rbf_signaled") {
		t.Errorf("Flags = %v, want rbf_signaled", rows[0].Flags)
	}
}

func TestBTCHistoryPaginationStopsAtSinceAndCapsPages(t *testing.T) {
	const selfAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	const sender = "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"
	origMempool, origDefault := mempoolBaseURL, defaultMempoolURLForFailover
	defer func() {
		mempoolBaseURL = origMempool
		defaultMempoolURLForFailover = origDefault
	}()

	now := time.Unix(1791500000, 0).UTC()
	since := now.Add(-payHistoryWindow)

	// Subcase A: stops at page 2 because tx timestamp crosses below since.
	var pageCallsA atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/address/" + selfAddr + "/txs":
			pageCallsA.Add(1)
			w.Write([]byte(`[{"txid":"p1","status":{"confirmed":true,"block_height":970600,"block_time":` + strconv.FormatInt(now.Unix()-3600, 10) + `},"vin":[{"sequence":4294967295,"prevout":{"scriptpubkey_address":"` + sender + `","value":10000000}}],"vout":[{"scriptpubkey_address":"` + selfAddr + `","value":10000000}]}]`))
		case "/api/address/" + selfAddr + "/txs/chain/p1":
			pageCallsA.Add(1)
			w.Write([]byte(`[{"txid":"p2_old","status":{"confirmed":true,"block_height":969000,"block_time":` + strconv.FormatInt(since.Unix()-10, 10) + `},"vin":[{"sequence":4294967295,"prevout":{"scriptpubkey_address":"` + sender + `","value":10000000}}],"vout":[{"scriptpubkey_address":"` + selfAddr + `","value":10000000}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srvA.Close()
	mempoolBaseURL = srvA.URL
	defaultMempoolURLForFailover = "https://mempool.space"

	txsA, scopeA, err := fetchBTCHistory(context.Background(), selfAddr, since)
	if err != nil {
		t.Fatalf("fetchBTCHistory A error: %v", err)
	}
	if len(txsA) != 1 || scopeA.Truncated || pageCallsA.Load() != 2 {
		t.Fatalf("subcase A: len=%d truncated=%v calls=%d, want len=1 truncated=false calls=2", len(txsA), scopeA.Truncated, pageCallsA.Load())
	}

	// Subcase B: hits payBTCHistoryMaxPages (8) with all txs >= since -> scope.Truncated = true (S6).
	var pageCallsB atomic.Int32
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/address/"+selfAddr+"/txs") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := pageCallsB.Add(1)
		txid := "tx_" + strconv.Itoa(int(n))
		ts := now.Unix() - int64(n)*600
		w.Write([]byte(`[{"txid":"` + txid + `","status":{"confirmed":true,"block_height":` + strconv.Itoa(970600-int(n)) + `,"block_time":` + strconv.FormatInt(ts, 10) + `},"vin":[{"sequence":4294967295,"prevout":{"scriptpubkey_address":"` + sender + `","value":10000000}}],"vout":[{"scriptpubkey_address":"` + selfAddr + `","value":10000000}]}]`))
	}))
	defer srvB.Close()
	mempoolBaseURL = srvB.URL

	txsB, scopeB, err := fetchBTCHistory(context.Background(), selfAddr, since)
	if err != nil {
		t.Fatalf("fetchBTCHistory B error: %v", err)
	}
	if len(txsB) != 8 || !scopeB.Truncated || pageCallsB.Load() != 8 {
		t.Fatalf("subcase B: len=%d truncated=%v calls=%d, want len=8 truncated=true calls=8", len(txsB), scopeB.Truncated, pageCallsB.Load())
	}
}

func TestSortPayTxsDescUnconfirmedFirstAndRegression(t *testing.T) {
	// 1. BTC mix: unconfirmed mempool tx (Block=0, Timestamp="") must sort before confirmed txs.
	btcTxs := []payTx{
		{TxHash: "conf_old", Block: 970590, Timestamp: "2026-10-08T10:00:00Z"},
		{TxHash: "mempool_0conf", Block: 0, Timestamp: ""},
		{TxHash: "conf_new", Block: 970600, Timestamp: "2026-10-08T12:00:00Z"},
	}
	sortPayTxsDesc(btcTxs)
	if btcTxs[0].TxHash != "mempool_0conf" || btcTxs[1].TxHash != "conf_new" || btcTxs[2].TxHash != "conf_old" {
		t.Fatalf("btcTxs sorted order = [%s, %s, %s], want [mempool_0conf, conf_new, conf_old]",
			btcTxs[0].TxHash, btcTxs[1].TxHash, btcTxs[2].TxHash)
	}

	// 2. TRON regression: pending TRC-20 tx has Block=0 with non-empty Timestamp -> sorted strictly by Timestamp DESC.
	tronTxs := []payTx{
		{TxHash: "tron_older_pending", Block: 0, Timestamp: "2026-10-08T11:00:00Z"},
		{TxHash: "tron_newer_confirmed", Block: 65000000, Timestamp: "2026-10-08T12:00:00Z"},
	}
	sortPayTxsDesc(tronTxs)
	if tronTxs[0].TxHash != "tron_newer_confirmed" || tronTxs[1].TxHash != "tron_older_pending" {
		t.Fatalf("tronTxs sorted order = [%s, %s], want [tron_newer_confirmed, tron_older_pending]",
			tronTxs[0].TxHash, tronTxs[1].TxHash)
	}

	// 3. EVM regression: all have Block > 0 -> sorted strictly by Block DESC.
	evmTxs := []payTx{
		{TxHash: "evm_low", Block: 21000001, Timestamp: "2026-10-08T12:00:00Z"},
		{TxHash: "evm_high", Block: 21000005, Timestamp: "2026-10-08T12:01:00Z"},
	}
	sortPayTxsDesc(evmTxs)
	if evmTxs[0].TxHash != "evm_high" || evmTxs[1].TxHash != "evm_low" {
		t.Fatalf("evmTxs sorted order = [%s, %s], want [evm_high, evm_low]",
			evmTxs[0].TxHash, evmTxs[1].TxHash)
	}
}
