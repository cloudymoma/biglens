package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyEVM(t *testing.T) {
	heads := payHeads{
		Latest:      1000,
		Safe:        960,
		Finalized:   920,
		LatestTime:  2000,
		SafeTime:    1500,
		FinalTime:   1100,
		SafeLagSec:  500,
		FinalLagSec: 900,
	}
	now := int64(2000)

	// 1. Failed tx is always DANGER / 0 / 0.
	if lvl, prog, eta := classifyEVM(990, 1950, true, heads, now); lvl != levelDanger || prog != 0 || eta != 0 {
		t.Errorf("failed tx = (%s, %d, %d), want (DANGER, 0, 0)", lvl, prog, eta)
	}

	// 2. B <= F is FINALIZED / 100 / 0.
	for _, b := range []uint64{900, 920} {
		if lvl, prog, eta := classifyEVM(b, 1000, false, heads, now); lvl != levelFinalized || prog != 100 || eta != 0 {
			t.Errorf("B=%d = (%s, %d, %d), want (FINALIZED, 100, 0)", b, lvl, prog, eta)
		}
	}

	// 3. B == S is SAFE with progress 85.
	lvlS, progS, etaS := classifyEVM(960, 1500, false, heads, now)
	if lvlS != levelSafe || progS != 85 || etaS != 400 {
		t.Errorf("B=S = (%s, %d, %d), want (SAFE, 85, 400)", lvlS, progS, etaS)
	}

	// 4. F < B < S: progress in [85, 95] and monotonically increases as B approaches F.
	_, prog950, _ := classifyEVM(950, 1400, false, heads, now)
	_, prog930, _ := classifyEVM(930, 1200, false, heads, now)
	if prog950 <= progS || prog930 <= prog950 || prog930 > 95 {
		t.Errorf("SAFE progress monotonicity: B=960→%d, B=950→%d, B=930→%d (want 85 < p950 < p930 <= 95)", progS, prog950, prog930)
	}

	// 5. B > S with age = 0, half safeLag, and > safeLag (capped at 75); ETA never negative.
	lvl0, prog0, eta0 := classifyEVM(1000, 2000, false, heads, now)
	if lvl0 != levelSoft || prog0 != 10 || eta0 != 900 {
		t.Errorf("B>S age=0 = (%s, %d, %d), want (SOFT, 10, 900)", lvl0, prog0, eta0)
	}
	lvlHalf, progHalf, etaHalf := classifyEVM(980, 1750, false, heads, now) // age = 250 = safeLag/2
	if lvlHalf != levelSoft || progHalf != 43 || etaHalf != 650 {
		t.Errorf("B>S age=half = (%s, %d, %d), want (SAFE/SOFT=SOFT, 43, 650)", lvlHalf, progHalf, etaHalf)
	}
	lvlOld, progOld, etaOld := classifyEVM(970, 500, false, heads, now) // age = 1500 > finalLag
	if lvlOld != levelSoft || progOld != 75 || etaOld != 0 {
		t.Errorf("B>S age>finalLag = (%s, %d, %d), want (SOFT, 75, 0)", lvlOld, progOld, etaOld)
	}
}

func TestClassifyEVMUnknownBlockIsNeverFinal(t *testing.T) {
	heads := payHeads{
		Latest:      1000,
		Safe:        960,
		Finalized:   920,
		SafeLagSec:  500,
		FinalLagSec: 900,
	}
	lvl, prog, eta := classifyEVM(0, 2000, false, heads, 2000)
	if lvl != levelSoft || prog != 0 || eta != 900 {
		t.Fatalf("block=0 = (%s, %d, %d), want (SOFT, 0, 900) — must never be FINALIZED", lvl, prog, eta)
	}
}

func TestClassifyEVMNegativeAgeAndZeroLag(t *testing.T) {
	heads := payHeads{
		Latest:      100,
		Safe:        90,
		Finalized:   80,
		SafeLagSec:  0,
		FinalLagSec: 0,
	}
	// blockTime > now (clock skew) and safeLag = 0 must not panic or drop below 10.
	lvl, prog, eta := classifyEVM(95, 3000, false, heads, 2000)
	if lvl != levelSoft || prog < 10 || prog > 75 || eta < 0 {
		t.Errorf("negative age & zero lag = (%s, %d, %d), want SOFT, prog in [10,75], eta >= 0", lvl, prog, eta)
	}
}

func TestClassifyEVMUnknownBlockTimeIsNotNearlyFinal(t *testing.T) {
	heads := payHeads{
		Latest:      1000,
		Safe:        960,
		Finalized:   920,
		LatestTime:  1759600000,
		SafeTime:    1759599500,
		FinalTime:   1759599100,
		SafeLagSec:  500,
		FinalLagSec: 900,
	}
	// When an RPC log has a known block number (995 > Safe) but no blockTimestamp
	// (blockTime <= 0), age must be treated as 0 rather than now - 0 = 1.75e9s,
	// so progress is 10 (not 75) and ETA is FinalLagSec (900, not 0).
	lvl, prog, eta := classifyEVM(995, 0, false, heads, 1759600000)
	if lvl != levelSoft || prog != 10 || eta != 900 {
		t.Fatalf("unknown blockTime = (%s, %d, %d), want (SOFT, 10, 900)", lvl, prog, eta)
	}
}

func TestClassifyTron(t *testing.T) {
	heads := payHeads{
		Latest:      118,
		Finalized:   100,
		LatestTime:  2054,
		FinalTime:   2000,
		FinalLagSec: 54,
	}

	// Failed tx -> DANGER.
	if lvl, prog, eta := classifyTron(105, 2015, true, heads); lvl != levelDanger || prog != 0 || eta != 0 {
		t.Errorf("failed tron tx = (%s, %d, %d), want (DANGER, 0, 0)", lvl, prog, eta)
	}

	// B <= solid -> FINALIZED / 100 / 0.
	for _, b := range []uint64{95, 100} {
		if lvl, prog, eta := classifyTron(b, 1990, false, heads); lvl != levelFinalized || prog != 100 || eta != 0 {
			t.Errorf("B=%d = (%s, %d, %d), want (FINALIZED, 100, 0)", b, lvl, prog, eta)
		}
	}

	// B = solid + 1 -> SOFT, high progress, ETA = 3s.
	lvlNext, progNext, etaNext := classifyTron(101, 2003, false, heads)
	if lvlNext != levelSoft || progNext != 90 || etaNext != 3 {
		t.Errorf("B=solid+1 = (%s, %d, %d), want (SOFT, 90, 3)", lvlNext, progNext, etaNext)
	}

	// B = latest -> SOFT, progress near 0, ETA = 18 * 3 = 54s.
	lvlLat, progLat, etaLat := classifyTron(118, 2054, false, heads)
	if lvlLat != levelSoft || progLat != 5 || etaLat != 54 {
		t.Errorf("B=latest = (%s, %d, %d), want (SOFT, 5, 54)", lvlLat, progLat, etaLat)
	}
}

func TestClassifyTronUnknownBlock(t *testing.T) {
	heads := payHeads{
		Latest:      118,
		Finalized:   100,
		LatestTime:  2054,
		FinalTime:   2000,
		FinalLagSec: 54,
	}

	// block == 0 and blockTime > FinalTime -> SOFT / 0.
	if lvl, prog, eta := classifyTron(0, 2010, false, heads); lvl != levelSoft || prog != 0 || eta != 54 {
		t.Errorf("block=0 newer than FinalTime = (%s, %d, %d), want (SOFT, 0, 54)", lvl, prog, eta)
	}

	// block == 0 and blockTime <= FinalTime -> FINALIZED / 100 / 0.
	if lvl, prog, eta := classifyTron(0, 2000, false, heads); lvl != levelFinalized || prog != 100 || eta != 0 {
		t.Errorf("block=0 at FinalTime = (%s, %d, %d), want (FINALIZED, 100, 0)", lvl, prog, eta)
	}
	if lvl, prog, eta := classifyTron(0, 1950, false, heads); lvl != levelFinalized || prog != 100 || eta != 0 {
		t.Errorf("block=0 older than FinalTime = (%s, %d, %d), want (FINALIZED, 100, 0)", lvl, prog, eta)
	}
}

func TestClassifyTronBlockAheadOfCachedHeads(t *testing.T) {
	heads := payHeads{
		Latest:      118,
		Finalized:   100,
		LatestTime:  2054,
		FinalTime:   2000,
		FinalLagSec: 54,
	}
	// Block 120 was mined after heads were cached at 118.
	lvl, prog, eta := classifyTron(120, 2060, false, heads)
	if lvl != levelSoft || prog < 0 || prog > 95 || eta != 60 {
		t.Errorf("B > Latest = (%s, %d, %d), want SOFT, prog in [0,95], eta=60", lvl, prog, eta)
	}
}

func TestClampHeads(t *testing.T) {
	// EVM: Safe > Latest and Finalized > Safe are clamped monotonically.
	evm := clampHeads(payHeads{
		Latest:     100,
		Safe:       105,
		Finalized:  110,
		LatestTime: 1000,
		SafeTime:   1050,
		FinalTime:  1100,
	}, false)
	if evm.Safe != 100 || evm.Finalized != 100 || evm.SafeTime != 1000 || evm.FinalTime != 1000 {
		t.Errorf("clamped EVM heads = %+v, want all 100 / 1000", evm)
	}

	// EVM: Finalized > Safe while Safe <= Latest.
	evm2 := clampHeads(payHeads{
		Latest:    100,
		Safe:      90,
		Finalized: 95,
	}, false)
	if evm2.Safe != 90 || evm2.Finalized != 90 {
		t.Errorf("clamped EVM2 heads = %+v, want Safe=90 Finalized=90", evm2)
	}

	// TRON: solid (Finalized) > Latest is clamped to Latest, and Safe is 0.
	tr := clampHeads(payHeads{
		Latest:     200,
		Safe:       195,
		Finalized:  205,
		LatestTime: 2000,
		FinalTime:  2015,
	}, true)
	if tr.Safe != 0 || tr.Finalized != 200 || tr.FinalTime != 2000 {
		t.Errorf("clamped TRON heads = %+v, want Safe=0 Finalized=200 FinalTime=2000", tr)
	}
}

func TestLagSamplerMedian(t *testing.T) {
	var s lagSampler
	t0 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

	// Empty buffer returns fallback / current values; median() before any sample returns (0, 0).
	if ms, mf := s.median(); ms != 0 || mf != 0 {
		t.Fatalf("empty median() = (%d, %d), want (0, 0)", ms, mf)
	}

	// First observation is recorded.
	if ms, mf := s.observe(t0, 500, 900); ms != 500 || mf != 900 {
		t.Fatalf("first observe = (%d, %d), want (500, 900)", ms, mf)
	}

	// Observation < 30s later is ignored.
	if ms, mf := s.observe(t0.Add(15*time.Second), 9999, 9999); ms != 500 || mf != 900 {
		t.Fatalf("within 30s observe = (%d, %d), want (500, 900) (ignored)", ms, mf)
	}

	// Two more observations >= 30s apart -> median of [500, 600, 520] = 520.
	s.observe(t0.Add(30*time.Second), 600, 1000)
	if ms, mf := s.observe(t0.Add(60*time.Second), 520, 940); ms != 520 || mf != 940 {
		t.Fatalf("3-sample median = (%d, %d), want (520, 940)", ms, mf)
	}

	// Push 120 more samples of (100, 200) -> old samples (500..600) are evicted.
	for i := 0; i < 120; i++ {
		s.observe(t0.Add(time.Duration(i+3)*30*time.Second), 100, 200)
	}
	if ms, mf := s.median(); ms != 100 || mf != 200 {
		t.Fatalf("after 120 new samples median() = (%d, %d), want (100, 200)", ms, mf)
	}
}

func TestFetchEVMHeads(t *testing.T) {
	var badHits atomic.Int32
	badRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badRPC.Close()

	goodRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != evmUserAgent {
			t.Errorf("User-Agent = %q, want %q", r.Header.Get("User-Agent"), evmUserAgent)
		}
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if req.Method != "eth_getBlockByNumber" || len(req.Params) < 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tag, _ := req.Params[0].(string)
		switch tag {
		case "latest":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"number":"0x3e8","timestamp":"0x7d0"}}`)) // 1000, 2000
		case "safe":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"number":"0x3c0","timestamp":"0x5dc"}}`)) // 960, 1500
		case "finalized":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"number":"0x398","timestamp":"0x44c"}}`)) // 920, 1100
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer goodRPC.Close()

	h, err := fetchEVMHeads(context.Background(), []string{badRPC.URL, goodRPC.URL})
	if err != nil {
		t.Fatalf("fetchEVMHeads error = %v", err)
	}
	if badHits.Load() < 3 {
		t.Errorf("badRPC hits = %d, want >= 3 (each tag tries badRPC first)", badHits.Load())
	}
	if h.Latest != 1000 || h.Safe != 960 || h.Finalized != 920 ||
		h.LatestTime != 2000 || h.SafeTime != 1500 || h.FinalTime != 1100 ||
		h.SafeLagSec != 500 || h.FinalLagSec != 900 {
		t.Errorf("heads = %+v", h)
	}
}

func TestFetchTronHeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844320,"timestamp":1727352054000}}}`))
		case "/walletsolidity/getnowblock":
			w.Write([]byte(`{"block_header":{"raw_data":{"number":86844302,"timestamp":1727352000000}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	orig := tronGridBaseURL
	tronGridBaseURL = srv.URL
	defer func() { tronGridBaseURL = orig }()

	h, err := fetchTronHeads(context.Background())
	if err != nil {
		t.Fatalf("fetchTronHeads error = %v", err)
	}
	if h.Latest != 86844320 || h.Safe != 0 || h.Finalized != 86844302 ||
		h.LatestTime != 1727352054 || h.FinalTime != 1727352000 || h.FinalLagSec != 54 {
		t.Errorf("tron heads = %+v", h)
	}
}

func TestClassifyBTCZeroToSixConfirmations(t *testing.T) {
	h := payHeads{
		Latest:    970600,
		Safe:      970598, // 3 confirmations
		Finalized: 970595, // 6 confirmations
	}

	cases := []struct {
		name     string
		block    uint64
		wantLvl  payLevel
		wantProg int
		wantETA  int
	}{
		{"0 conf mempool", 0, levelDanger, 0, 6 * btcAvgBlockSec},
		{"block ahead of cached Latest (B2 underflow guard)", 970601, levelSoft, 20, 5 * btcAvgBlockSec},
		{"1 conf (Latest)", 970600, levelSoft, 20, 5 * btcAvgBlockSec},
		{"2 conf", 970599, levelSoft, 40, 4 * btcAvgBlockSec},
		{"3 conf (Safe)", 970598, levelSafe, 70, 3 * btcAvgBlockSec},
		{"4 conf", 970597, levelSafe, 80, 2 * btcAvgBlockSec},
		{"5 conf", 970596, levelSafe, 90, 1 * btcAvgBlockSec},
		{"6 conf (Finalized)", 970595, levelFinalized, 100, 0},
		{"10 conf (>6)", 970591, levelFinalized, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lvl, prog, eta := classifyBTC(tc.block, h)
			if lvl != tc.wantLvl || prog != tc.wantProg || eta != tc.wantETA {
				t.Errorf("classifyBTC(%d) = (%s, %d, %d), want (%s, %d, %d)",
					tc.block, lvl, prog, eta, tc.wantLvl, tc.wantProg, tc.wantETA)
			}
		})
	}
}

func TestFetchBTCHeadsFailoverAndPrivacy(t *testing.T) {
	origMempool, origFallback := mempoolBaseURL, esploraFallbackAPI
	defer func() {
		mempoolBaseURL = origMempool
		esploraFallbackAPI = origFallback
	}()

	var fallbackHits atomic.Int32
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		if r.URL.Path != "/blocks" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`[
			{"height":970600,"timestamp":1760003600},
			{"height":970599,"timestamp":1760003000},
			{"height":970598,"timestamp":1760002400},
			{"height":970597,"timestamp":1760001800},
			{"height":970596,"timestamp":1760001200},
			{"height":970595,"timestamp":1760000600}
		]`))
	}))
	defer fallbackSrv.Close()
	esploraFallbackAPI = fallbackSrv.URL

	brokenPrimary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer brokenPrimary.Close()

	// 1. Custom self-hosted mempoolBaseURL MUST NOT leak requests to fallback (S10).
	mempoolBaseURL = brokenPrimary.URL
	if _, err := fetchBTCHeads(context.Background()); err == nil {
		t.Fatal("expected error when custom mempoolBaseURL fails without third-party fallback")
	}
	if fallbackHits.Load() != 0 {
		t.Fatalf("custom mempoolBaseURL leaked %d request(s) to fallback", fallbackHits.Load())
	}

	// 2. Default public mempoolBaseURL (simulated via esploraDefaultURLForTest or default flag)
	// fails over cleanly to esploraFallbackAPI.
	origDefault := defaultMempoolURLForFailover
	defaultMempoolURLForFailover = brokenPrimary.URL
	defer func() { defaultMempoolURLForFailover = origDefault }()

	h, err := fetchBTCHeads(context.Background())
	if err != nil {
		t.Fatalf("fetchBTCHeads failover error = %v", err)
	}
	if h.Latest != 970600 || h.Safe != 970598 || h.Finalized != 970595 {
		t.Fatalf("btc heads = %+v, want Latest=970600 Safe=970598 Finalized=970595", h)
	}
	if h.SafeLagSec != 1200 || h.FinalLagSec != 3000 {
		t.Errorf("btc lags = (%d, %d), want (1200, 3000)", h.SafeLagSec, h.FinalLagSec)
	}
}

func TestClassifySolanaSlotLevels(t *testing.T) {
	h := payHeads{
		Latest:      454804685,
		Safe:        454804680,
		Finalized:   454804650,
		SafeTime:    1760000100,
		FinalTime:   1760000088,
		FinalLagSec: 12,
	}
	now := int64(1760000102)

	if lvl, prog, eta := classifySolana(454804660, 1760000095, true, h, now); lvl != levelDanger || prog != 0 || eta != 0 {
		t.Errorf("failed solana = (%s, %d, %d), want (DANGER, 0, 0)", lvl, prog, eta)
	}
	if lvl, prog, eta := classifySolana(0, 0, false, h, now); lvl != levelSoft || prog != 0 || eta != 12 {
		t.Errorf("slot=0 = (%s, %d, %d), want (SOFT, 0, 12)", lvl, prog, eta)
	}
	if lvl, prog, eta := classifySolana(454804682, 0, false, h, now); lvl != levelSoft || prog != 25 || eta != 12 {
		t.Errorf("slot>Safe = (%s, %d, %d), want (SOFT, 25, 12)", lvl, prog, eta)
	}
	lvlSafe, progSafe, etaSafe := classifySolana(454804665, 1760000098, false, h, now)
	if lvlSafe != levelSafe || progSafe != 90 || etaSafe != 8 {
		t.Errorf("confirmed slot = (%s, %d, %d), want (SAFE, 90, 8)", lvlSafe, progSafe, etaSafe)
	}
	if lvl, prog, eta := classifySolana(454804650, 1760000088, false, h, now); lvl != levelFinalized || prog != 100 || eta != 0 {
		t.Errorf("finalized slot = (%s, %d, %d), want (FINALIZED, 100, 0)", lvl, prog, eta)
	}
}

func TestFetchSolanaHeadsToleratesMissingBlockTime(t *testing.T) {
	var blockTimeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSlot":
			var cfg map[string]string
			_ = json.Unmarshal(req.Params[0], &cfg)
			switch cfg["commitment"] {
			case "processed":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":454804685}`))
			case "confirmed":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":454804680}`))
			case "finalized":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":454804650}`))
			}
		case "getBlockTime":
			blockTimeCalls.Add(1)
			// Return JSON null (skipped/pruned slot) — MUST NOT fail fetchSolanaHeads (S2).
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	h, err := fetchSolanaHeads(context.Background(), []string{srv.URL})
	if err != nil {
		t.Fatalf("fetchSolanaHeads error = %v", err)
	}
	if blockTimeCalls.Load() != 2 {
		t.Errorf("getBlockTime calls = %d, want 2 (only confirmed & finalized, S2)", blockTimeCalls.Load())
	}
	if h.Latest != 454804685 || h.Safe != 454804680 || h.Finalized != 454804650 || h.FinalLagSec <= 0 {
		t.Errorf("solana heads = %+v", h)
	}
}
