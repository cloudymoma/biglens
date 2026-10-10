package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func buildMetaplexMetadataBytes(name, symbol string) []byte {
	var out []byte
	out = append(out, 4)                   // key = MetadataV1
	out = append(out, make([]byte, 32)...) // update_authority
	out = append(out, make([]byte, 32)...) // mint
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(name)))
	out = append(out, lenBuf...)
	out = append(out, []byte(name)...)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(symbol)))
	out = append(out, lenBuf...)
	out = append(out, []byte(symbol)...)
	return out
}

func TestSolanaSignaturesRequestUsesConfirmedCommitment(t *testing.T) {
	const walletAddr = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"

	var (
		mu          sync.Mutex
		commitments []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if req.Method == "getSignaturesForAddress" {
			var cfg struct {
				Commitment string `json:"commitment"`
			}
			_ = json.Unmarshal(req.Params[1], &cfg)
			mu.Lock()
			commitments = append(commitments, cfg.Commitment)
			mu.Unlock()
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := fetchSolanaRecent(context.Background(), nil, []string{srv.URL}, "USDC", walletAddr, payHeads{Finalized: 454800000})
	if err != nil {
		t.Fatalf("fetchSolanaRecent error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commitments) != 2 {
		t.Fatalf("getSignaturesForAddress calls = %d, want 2 (officialATA + wallet owner)", len(commitments))
	}
	for i, c := range commitments {
		if c != "confirmed" {
			t.Errorf("call[%d] commitment = %q, want \"confirmed\" (B1)", i, c)
		}
	}
}

func TestSolanaSPLCounterpartyMapsTokenAccountToWalletOwnerIncludingZeroPoisoning(t *testing.T) {
	const (
		victimOwner   = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		legitOwner    = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
		poisonerOwner = "9WzDXwBb1111111111111111111111111111111tAWWM"
		victimATA     = "ERbwSojYctddRjySVYiBw9TXgqAWw2nTeA2zQR9oB95i"
		legitATA      = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
		poisonerATA   = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
		usdcMint      = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSignaturesForAddress":
			var target string
			_ = json.Unmarshal(req.Params[0], &target)
			if target == victimATA {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
					{"signature":"sig_poison_zero","slot":454800200,"blockTime":1791447000,"err":null},
					{"signature":"sig_legit_in","slot":454800100,"blockTime":1791446400,"err":null}
				]}`))
			} else {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
			}
		case "getTransaction":
			var sig string
			_ = json.Unmarshal(req.Params[0], &sig)
			switch sig {
			case "sig_legit_in":
				// Legit 100 USDC transfer from legitATA (owner legitOwner) -> victimATA (owner victimOwner).
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
					"slot":454800100,
					"blockTime":1791446400,
					"transaction":{"message":{
						"accountKeys":["` + legitOwner + `","` + legitATA + `","` + victimATA + `","` + solanaTokenProgramID + `"],
						"instructions":[{
							"program":"spl-token",
							"parsed":{"type":"transferChecked","info":{
								"source":"` + legitATA + `",
								"destination":"` + victimATA + `",
								"authority":"` + legitOwner + `",
								"mint":"` + usdcMint + `",
								"tokenAmount":{"amount":"100000000","decimals":6}
							}}
						}]
					}},
					"meta":{
						"err":null,
						"preTokenBalances":[
							{"accountIndex":1,"mint":"` + usdcMint + `","owner":"` + legitOwner + `","uiTokenAmount":{"amount":"200000000","decimals":6}},
							{"accountIndex":2,"mint":"` + usdcMint + `","owner":"` + victimOwner + `","uiTokenAmount":{"amount":"0","decimals":6}}
						],
						"postTokenBalances":[
							{"accountIndex":1,"mint":"` + usdcMint + `","owner":"` + legitOwner + `","uiTokenAmount":{"amount":"100000000","decimals":6}},
							{"accountIndex":2,"mint":"` + usdcMint + `","owner":"` + victimOwner + `","uiTokenAmount":{"amount":"100000000","decimals":6}}
						]
					}
				}}`))
			case "sig_poison_zero":
				// 0 USDC transfer from poisonerATA (owner poisonerOwner) -> victimATA (owner victimOwner).
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
					"slot":454800200,
					"blockTime":1791447000,
					"transaction":{"message":{
						"accountKeys":["` + poisonerOwner + `","` + poisonerATA + `","` + victimATA + `","` + solanaTokenProgramID + `"],
						"instructions":[{
							"program":"spl-token",
							"parsed":{"type":"transferChecked","info":{
								"source":"` + poisonerATA + `",
								"destination":"` + victimATA + `",
								"authority":"` + poisonerOwner + `",
								"mint":"` + usdcMint + `",
								"tokenAmount":{"amount":"0","decimals":6}
							}}
						}]
					}},
					"meta":{
						"err":null,
						"preTokenBalances":[
							{"accountIndex":1,"mint":"` + usdcMint + `","owner":"` + poisonerOwner + `","uiTokenAmount":{"amount":"0","decimals":6}},
							{"accountIndex":2,"mint":"` + usdcMint + `","owner":"` + victimOwner + `","uiTokenAmount":{"amount":"100000000","decimals":6}}
						],
						"postTokenBalances":[
							{"accountIndex":1,"mint":"` + usdcMint + `","owner":"` + poisonerOwner + `","uiTokenAmount":{"amount":"0","decimals":6}},
							{"accountIndex":2,"mint":"` + usdcMint + `","owner":"` + victimOwner + `","uiTokenAmount":{"amount":"100000000","decimals":6}}
						]
					}
				}}`))
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	txs, err := fetchSolanaRecent(context.Background(), nil, []string{srv.URL}, "USDC", victimOwner, payHeads{Finalized: 454800300})
	if err != nil {
		t.Fatalf("fetchSolanaRecent error: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("len(txs) = %d, want 2: %+v", len(txs), txs)
	}
	// Newest (slot 454800200): 0 USDC poisoning -> Counterparty MUST be poisonerOwner (wallet owner, NOT poisonerATA, B6).
	if txs[0].TxHash != "sig_poison_zero" || txs[0].Amount != "0" || txs[0].Counterparty != poisonerOwner || txs[0].TokenTier != tierNative {
		t.Fatalf("txs[0] (0-val poison) = %+v, want Counterparty=%s Amount=0 tierNative", txs[0], poisonerOwner)
	}
	// Older (slot 454800100): 100 USDC legit -> Counterparty MUST be legitOwner (wallet owner, NOT legitATA, B6).
	if txs[1].TxHash != "sig_legit_in" || txs[1].Amount != "100" || txs[1].Counterparty != legitOwner || txs[1].TokenTier != tierNative {
		t.Fatalf("txs[1] (legit in) = %+v, want Counterparty=%s Amount=100 tierNative", txs[1], legitOwner)
	}
}

func TestSolanaSOLIgnoresSwapBalanceDeltaWithoutSystemTransfer(t *testing.T) {
	const (
		userAddr = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		peerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSignaturesForAddress":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
				{"signature":"sig_swap_only","slot":454800500,"blockTime":1791448000,"err":null},
				{"signature":"sig_sys_transfer","slot":454800400,"blockTime":1791447500,"err":null}
			]}`))
		case "getTransaction":
			var sig string
			_ = json.Unmarshal(req.Params[0], &sig)
			switch sig {
			case "sig_swap_only":
				// Swap program alters preBalances/postBalances without any system transfer instruction -> must emit 0 rows (S4).
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
					"slot":454800500,
					"blockTime":1791448000,
					"transaction":{"message":{
						"accountKeys":["` + userAddr + `","JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4"],
						"instructions":[{"programId":"JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4","data":"base58data"}]
					}},
					"meta":{"err":null,"fee":5000,"preBalances":[2000000000,0],"postBalances":[999995000,1000000000]}
				}}`))
			case "sig_sys_transfer":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
					"slot":454800400,
					"blockTime":1791447500,
					"transaction":{"message":{
						"accountKeys":["` + peerAddr + `","` + userAddr + `","` + solanaSystemProgramID + `"],
						"instructions":[{
							"program":"system",
							"parsed":{"type":"transfer","info":{"source":"` + peerAddr + `","destination":"` + userAddr + `","lamports":500000000}}
						}]
					}},
					"meta":{"err":null,"fee":5000,"preBalances":[1500005000,0,1],"postBalances":[1000000000,500000000,1]}
				}}`))
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	txs, err := fetchSolanaRecent(context.Background(), nil, []string{srv.URL}, "SOL", userAddr, payHeads{Finalized: 454800600})
	if err != nil {
		t.Fatalf("fetchSolanaRecent SOL error: %v", err)
	}
	if len(txs) != 1 || txs[0].TxHash != "sig_sys_transfer" || txs[0].Amount != "0.5" || txs[0].TokenTier != tierNative || txs[0].Counterparty != peerAddr {
		t.Fatalf("SOL txs = %+v, want only sig_sys_transfer (0.5 SOL native from peerAddr, swap ignored per S4)", txs)
	}
}

func TestSolanaCounterfeitDetectionViaMetaplexPDA(t *testing.T) {
	const (
		userAddr     = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		attackerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
		fakeMint     = "7vfCXTUXx5WJV5JADk17DUJ4ksgau7utNKj4b963voxs"
		attackerATA  = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
		userFakeATA  = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	)

	fakeMetaB64 := base64.StdEncoding.EncodeToString(buildMetaplexMetadataBytes("Tether USD\x00\x00", "USD₮\x00\x00"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSignaturesForAddress":
			var target string
			_ = json.Unmarshal(req.Params[0], &target)
			if target == userAddr {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[{"signature":"sig_fake_usdt","slot":454800700,"blockTime":1791448500,"err":null}]}`))
			} else {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
			}
		case "getTransaction":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"slot":454800700,
				"blockTime":1791448500,
				"transaction":{"message":{
					"accountKeys":["` + attackerAddr + `","` + attackerATA + `","` + userFakeATA + `","` + solanaTokenProgramID + `"],
					"instructions":[{
						"program":"spl-token",
						"parsed":{"type":"transferChecked","info":{
							"source":"` + attackerATA + `",
							"destination":"` + userFakeATA + `",
							"authority":"` + attackerAddr + `",
							"mint":"` + fakeMint + `",
							"tokenAmount":{"amount":"5000000000","decimals":6}
						}}
					}]
				}},
				"meta":{
					"err":null,
					"preTokenBalances":[
						{"accountIndex":1,"mint":"` + fakeMint + `","owner":"` + attackerAddr + `","uiTokenAmount":{"amount":"5000000000","decimals":6}},
						{"accountIndex":2,"mint":"` + fakeMint + `","owner":"` + userAddr + `","uiTokenAmount":{"amount":"0","decimals":6}}
					],
					"postTokenBalances":[
						{"accountIndex":1,"mint":"` + fakeMint + `","owner":"` + attackerAddr + `","uiTokenAmount":{"amount":"0","decimals":6}},
						{"accountIndex":2,"mint":"` + fakeMint + `","owner":"` + userAddr + `","uiTokenAmount":{"amount":"5000000000","decimals":6}}
					]
				}
			}}`))
		case "getMultipleAccounts":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454800800},"value":[{"data":["` + fakeMetaB64 + `","base64"]}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	txs, err := fetchSolanaRecent(context.Background(), NewCache(time.Minute), []string{srv.URL}, "USDT", userAddr, payHeads{Finalized: 454800800})
	if err != nil {
		t.Fatalf("fetchSolanaRecent fake token error: %v", err)
	}
	if len(txs) != 1 || txs[0].TokenTier != tierCounterfeit || txs[0].Amount != "5000" || txs[0].Counterparty != attackerAddr {
		t.Fatalf("fake token row = %+v, want tierCounterfeit 5000 from %s", txs, attackerAddr)
	}
}

func TestSolanaTxCacheTTLByFinality(t *testing.T) {
	const (
		userAddr = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		peerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	)

	var unfinTxCalls, finTxCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSignaturesForAddress":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
				{"signature":"sig_unfinalized","slot":454801050,"blockTime":1791449100,"err":null},
				{"signature":"sig_finalized","slot":454800950,"blockTime":1791449000,"err":null}
			]}`))
		case "getTransaction":
			var sig string
			_ = json.Unmarshal(req.Params[0], &sig)
			slot := uint64(454800950)
			if sig == "sig_unfinalized" {
				slot = 454801050
				unfinTxCalls.Add(1)
			} else {
				finTxCalls.Add(1)
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"slot":` + jsonNumber(slot) + `,
				"blockTime":1791449000,
				"transaction":{"message":{
					"accountKeys":["` + peerAddr + `","` + userAddr + `","` + solanaSystemProgramID + `"],
					"instructions":[{"program":"system","parsed":{"type":"transfer","info":{"source":"` + peerAddr + `","destination":"` + userAddr + `","lamports":1000000}}}]
				}},
				"meta":{"err":null}
			}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	cache := NewCache(time.Minute)
	heads := payHeads{Latest: 454801060, Safe: 454801055, Finalized: 454801000}

	// First call populates sol:tx:<sig> cache entries:
	// - sig_finalized (454800950 <= 454801000) -> 10m TTL
	// - sig_unfinalized (454801050 > 454801000) -> 4s TTL (payRecentCacheTTL)
	_, err := fetchSolanaRecent(context.Background(), cache, []string{srv.URL}, "SOL", userAddr, heads)
	if err != nil {
		t.Fatalf("first fetchSolanaRecent error: %v", err)
	}
	if unfinTxCalls.Load() != 1 || finTxCalls.Load() != 1 {
		t.Fatalf("initial calls: unfin=%d fin=%d, want 1 and 1", unfinTxCalls.Load(), finTxCalls.Load())
	}

	// Immediate repeat hits both cache entries.
	_, err = fetchSolanaRecent(context.Background(), cache, []string{srv.URL}, "SOL", userAddr, heads)
	if err != nil {
		t.Fatalf("second fetchSolanaRecent error: %v", err)
	}
	if unfinTxCalls.Load() != 1 || finTxCalls.Load() != 1 {
		t.Fatalf("immediate repeat calls: unfin=%d fin=%d, want still 1 and 1", unfinTxCalls.Load(), finTxCalls.Load())
	}

	// Simulate cache expiry beyond payRecentCacheTTL (4s): unfinalized expires, finalized remains cached!
	cache.store.Range(func(k, v any) bool {
		e := v.(*cacheEntry)
		cache.store.Store(k, &cacheEntry{
			data:      e.data,
			expiresAt: e.expiresAt.Add(-5 * time.Second),
		})
		return true
	})

	_, err = fetchSolanaRecent(context.Background(), cache, []string{srv.URL}, "SOL", userAddr, heads)
	if err != nil {
		t.Fatalf("post-4s fetchSolanaRecent error: %v", err)
	}
	if unfinTxCalls.Load() != 2 || finTxCalls.Load() != 1 {
		t.Fatalf("after 5s advance: unfin=%d fin=%d, want unfin=2 (4s expired) and fin=1 (10m cached, S1)", unfinTxCalls.Load(), finTxCalls.Load())
	}
}

func jsonNumber(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestSolanaConcurrentParseCachedTxNoRace(t *testing.T) {
	const (
		userAddr = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		peerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
		userATA  = "ERbwSojYctddRjySVYiBw9TXgqAWw2nTeA2zQR9oB95i"
		peerATA  = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
		usdcMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	)

	// PreTokenBalances intentionally has len=2, cap=4 so append(PreTokenBalances, PostTokenBalances...)
	// writes into shared backing storage if not cloned via slices.Concat!
	pre := make([]solTokenBalance, 2, 4)
	pre[0] = solTokenBalance{AccountIndex: 1, Mint: usdcMint, Owner: peerAddr}
	pre[1] = solTokenBalance{AccountIndex: 2, Mint: usdcMint, Owner: userAddr}
	post := []solTokenBalance{
		{AccountIndex: 1, Mint: usdcMint, Owner: peerAddr},
		{AccountIndex: 2, Mint: usdcMint, Owner: userAddr},
	}

	var tx solTxResult
	tx.Slot = 454800100
	bt := int64(1791446400)
	tx.BlockTime = &bt
	tx.Transaction.Message.AccountKeys = []solAccountKey{peerAddr, peerATA, userATA, solanaTokenProgramID}
	tx.Transaction.Message.Instructions = []solInstruction{{
		Program: "spl-token",
		Parsed:  json.RawMessage(`{"type":"transferChecked","info":{"source":"` + peerATA + `","destination":"` + userATA + `","authority":"` + peerAddr + `","mint":"` + usdcMint + `","tokenAmount":{"amount":"1000000","decimals":6}}}`),
	}}
	tx.Meta = &struct {
		Err               any               `json:"err"`
		Fee               uint64            `json:"fee"`
		PreBalances       []uint64          `json:"preBalances"`
		PostBalances      []uint64          `json:"postBalances"`
		PreTokenBalances  []solTokenBalance `json:"preTokenBalances"`
		PostTokenBalances []solTokenBalance `json:"postTokenBalances"`
		InnerInstructions []struct {
			Index        int              `json:"index"`
			Instructions []solInstruction `json:"instructions"`
		} `json:"innerInstructions"`
		LoadedAddresses *struct {
			Writable []string `json:"writable"`
			Readonly []string `json:"readonly"`
		} `json:"loadedAddresses"`
	}{
		PreTokenBalances:  pre,
		PostTokenBalances: post,
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				cp := tx // shallow struct copy just like cache retrieval
				rows := parseSolTxTransfers("sig_race", &cp, "USDC", userAddr)
				if len(rows) != 1 || rows[0].counterparty != peerAddr {
					t.Errorf("unexpected rows: %+v", rows)
				}
			}
		}()
	}
	wg.Wait()
}

// A lagging node behind a load balancer can return null for a signature that
// another node already confirmed; the incoming payment must not vanish from
// /live just because the first RPC was behind.
func TestSolanaTxDetailsRetriesNullOnNextRPC(t *testing.T) {
	const (
		userAddr = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		peerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	)
	sigsBody := `{"jsonrpc":"2.0","id":1,"result":[{"signature":"sig_new","slot":454801050,"blockTime":1791449100,"err":null}]}`
	rpcMethod := func(r *http.Request) string {
		var req struct {
			Method string `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		return req.Method
	}
	lagging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch rpcMethod(r) {
		case "getSignaturesForAddress":
			w.Write([]byte(sigsBody))
		case "getTransaction":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer lagging.Close()
	synced := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) != "getTransaction" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"slot":454801050,"blockTime":1791449100,
			"transaction":{"message":{
				"accountKeys":["` + peerAddr + `","` + userAddr + `","` + solanaSystemProgramID + `"],
				"instructions":[{"program":"system","parsed":{"type":"transfer","info":{"source":"` + peerAddr + `","destination":"` + userAddr + `","lamports":1000000}}}]
			}},
			"meta":{"err":null}
		}}`))
	}))
	defer synced.Close()

	heads := payHeads{Latest: 454801060, Safe: 454801055, Finalized: 454801000}
	txs, err := fetchSolanaRecent(context.Background(), NewCache(time.Minute), []string{lagging.URL, synced.URL}, "SOL", userAddr, heads)
	if err != nil {
		t.Fatalf("fetchSolanaRecent error: %v", err)
	}
	if len(txs) != 1 || txs[0].TxHash != "sig_new" || txs[0].Direction != "in" || txs[0].Amount != "0.001" {
		t.Fatalf("txs = %+v, want the incoming sig_new transfer fetched from the second RPC", txs)
	}
}

// Token-2022 mints keep name/symbol in the mint's tokenMetadata extension
// instead of a Metaplex PDA; a fake "USD₮" minted that way must still be
// flagged as counterfeit rather than filed under unrelated tokens.
func TestSolanaCounterfeitDetectionViaToken2022Metadata(t *testing.T) {
	const (
		userAddr     = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
		attackerAddr = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
		fakeMint     = "7vfCXTUXx5WJV5JADk17DUJ4ksgau7utNKj4b963voxs"
		attackerATA  = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
		userFakeATA  = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		switch req.Method {
		case "getSignaturesForAddress":
			var target string
			_ = json.Unmarshal(req.Params[0], &target)
			if target == userAddr {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[{"signature":"sig_fake_usdt","slot":454800700,"blockTime":1791448500,"err":null}]}`))
			} else {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
			}
		case "getTransaction":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"slot":454800700,
				"blockTime":1791448500,
				"transaction":{"message":{
					"accountKeys":["` + attackerAddr + `","` + attackerATA + `","` + userFakeATA + `","` + solanaTokenProgramID + `"],
					"instructions":[{
						"program":"spl-token",
						"parsed":{"type":"transferChecked","info":{
							"source":"` + attackerATA + `",
							"destination":"` + userFakeATA + `",
							"authority":"` + attackerAddr + `",
							"mint":"` + fakeMint + `",
							"tokenAmount":{"amount":"5000000000","decimals":6}
						}}
					}]
				}},
				"meta":{
					"err":null,
					"preTokenBalances":[
						{"accountIndex":1,"mint":"` + fakeMint + `","owner":"` + attackerAddr + `","uiTokenAmount":{"amount":"5000000000","decimals":6}},
						{"accountIndex":2,"mint":"` + fakeMint + `","owner":"` + userAddr + `","uiTokenAmount":{"amount":"0","decimals":6}}
					],
					"postTokenBalances":[
						{"accountIndex":1,"mint":"` + fakeMint + `","owner":"` + attackerAddr + `","uiTokenAmount":{"amount":"0","decimals":6}},
						{"accountIndex":2,"mint":"` + fakeMint + `","owner":"` + userAddr + `","uiTokenAmount":{"amount":"5000000000","decimals":6}}
					]
				}
			}}`))
		case "getMultipleAccounts":
			var cfg struct {
				Encoding string `json:"encoding"`
			}
			_ = json.Unmarshal(req.Params[1], &cfg)
			if cfg.Encoding == "base64" {
				// No Metaplex metadata PDA: the mint stores its symbol inline (Token-2022).
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454800800},"value":[null]}}`))
				return
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454800800},"value":[{"owner":"` + solanaToken2022ProgramID + `","data":{"program":"spl-token-2022","parsed":{"type":"mint","info":{"decimals":6,"extensions":[
				{"extension":"metadataPointer","state":{}},
				{"extension":"tokenMetadata","state":{"name":"Tether USD","symbol":"USD₮"}}
			]}}}}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	txs, err := fetchSolanaRecent(context.Background(), NewCache(time.Minute), []string{srv.URL}, "USDT", userAddr, payHeads{Finalized: 454800800})
	if err != nil {
		t.Fatalf("fetchSolanaRecent Token-2022 fake token error: %v", err)
	}
	if len(txs) != 1 || txs[0].TokenTier != tierCounterfeit || txs[0].Amount != "5000" || txs[0].Counterparty != attackerAddr {
		t.Fatalf("fake token row = %+v, want tierCounterfeit 5000 from %s", txs, attackerAddr)
	}
}
