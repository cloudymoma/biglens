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
	"time"

	"golang.org/x/time/rate"
)

func TestParseBool32Response(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantHit  bool
		wantCode string
	}{
		{"true", `{"jsonrpc":"2.0","id":1,"result":"` + oracleTrue + `"}`, true, ""},
		{"false", `{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`, false, ""},
		{"missing 0x prefix", `{"result":"` + oracleTrue[2:] + `"}`, false, "bad_response"},
		{"empty 0x (no contract / no function)", `{"result":"0x"}`, false, "bad_response"},
		{"json-rpc error object", `{"error":{"code":3,"message":"execution reverted"}}`, false, "bad_response"},
		{"result is an object inside HTTP 200", `{"result":{"code":503}}`, false, "bad_response"},
		{"not json", `<html>`, false, "bad_response"},
		{"other non-zero value", `{"result":"0x0000000000000000000000000000000000000000000000000000000000000002"}`, false, "bad_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, code := parseBool32Response([]byte(tt.body))
			if hit != tt.wantHit || code != tt.wantCode {
				t.Errorf("parseBool32Response got (%v, %q), want (%v, %q)", hit, code, tt.wantHit, tt.wantCode)
			}
			// parseOracleResponse must remain identical.
			oHit, oCode := parseOracleResponse([]byte(tt.body))
			if oHit != tt.wantHit || oCode != tt.wantCode {
				t.Errorf("parseOracleResponse got (%v, %q), want (%v, %q)", oHit, oCode, tt.wantHit, tt.wantCode)
			}
		})
	}
}

func TestCheckIssuerFreezeEVM(t *testing.T) {
	orig := riskSourceTimeout
	riskSourceTimeout = 300 * time.Millisecond
	defer func() { riskSourceTimeout = orig }()

	const addr = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

	t.Run("true and false across contracts", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(strings.ToLower(string(b)), "0xdac17f958d2ee523a2206206994597c13d831ec7") {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleTrue + `"}`))
				return
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
		}))
		defer srv.Close()

		states, code := checkIssuerFreeze(context.Background(), "eth", addr, "", []string{srv.URL})
		if code != "" || len(states) != 2 {
			t.Fatalf("got (%+v, %q), want 2 states and empty code", states, code)
		}
		if states[0].Token != "USDT" || !states[0].Frozen || states[1].Token != "USDC" || states[1].Frozen {
			t.Errorf("unexpected states: %+v", states)
		}
	})

	t.Run("0x empty result is bad_response, never unfrozen", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x"}`))
		}))
		defer srv.Close()

		states, code := checkIssuerFreeze(context.Background(), "base", addr, "USDC", []string{srv.URL})
		if code != "bad_response" || len(states) != 0 {
			t.Errorf("got (%+v, %q), want (nil, bad_response)", states, code)
		}
	})

	t.Run("HTTP 429 maps to rate_limited", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		_, code := checkIssuerFreeze(context.Background(), "base", addr, "USDC", []string{srv.URL})
		if code != "rate_limited" {
			t.Errorf("code = %q, want rate_limited", code)
		}
	})

	t.Run("failover after first RPC times out", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Second)
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
		}))
		defer slow.Close()
		good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleTrue + `"}`))
		}))
		defer good.Close()

		states, code := checkIssuerFreeze(context.Background(), "base", addr, "USDC", []string{slow.URL, good.URL})
		if code != "" || len(states) != 1 || !states[0].Frozen {
			t.Errorf("failover got (%+v, %q), want frozen state", states, code)
		}
	})
}

func TestCheckIssuerFreezeConcurrentContracts(t *testing.T) {
	orig := riskSourceTimeout
	riskSourceTimeout = 3 * time.Second
	defer func() { riskSourceTimeout = orig }()

	// Arbitrum has 3 contracts with FreezeSel (USD₮0, USDC, USDC.e).
	// If each takes 250ms, sequential execution takes >=750ms while concurrent takes ~250ms.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
	}))
	defer srv.Close()

	start := time.Now()
	states, code := checkIssuerFreeze(context.Background(), "arb", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", "", []string{srv.URL})
	elapsed := time.Since(start)
	if code != "" || len(states) != 3 {
		t.Fatalf("got (%+v, %q), want 3 states", states, code)
	}
	if elapsed >= 600*time.Millisecond {
		t.Errorf("3 contracts took %v; expected concurrent execution (< 600ms)", elapsed)
	}
}

func TestEVMCallsSendUserAgent(t *testing.T) {
	var freezeUA, oracleUA atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), riskOracleContract) {
			oracleUA.Store(r.Header.Get("User-Agent"))
		} else {
			freezeUA.Store(r.Header.Get("User-Agent"))
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
	}))
	defer srv.Close()

	const addr = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	if _, code := checkIssuerFreeze(context.Background(), "base", addr, "USDC", []string{srv.URL}); code != "" {
		t.Fatalf("checkIssuerFreeze code = %q", code)
	}
	if _, code := checkOracle(context.Background(), []string{srv.URL}, addr); code != "" {
		t.Fatalf("checkOracle code = %q", code)
	}
	if got, _ := freezeUA.Load().(string); got != "biglens/1.0" {
		t.Errorf("freeze User-Agent = %q, want biglens/1.0", got)
	}
	if got, _ := oracleUA.Load().(string); got != "biglens/1.0" {
		t.Errorf("oracle User-Agent = %q, want biglens/1.0", got)
	}
}

func TestTronGridDoRateLimited(t *testing.T) {
	origLim, origV1Lim := tronGridLimiter, tronGridV1Limiter
	// 1 token per hour, burst 1, pre-exhausted.
	lim := rate.NewLimiter(rate.Every(time.Hour), 1)
	lim.Allow()
	tronGridLimiter = lim
	defer func() {
		tronGridLimiter = origLim
		tronGridV1Limiter = origV1Lim
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, code := tronGridDo(ctx, http.MethodGet, "/wallet/getnowblock", nil)
	if code != "rate_limited_local" {
		t.Errorf("tronGridDo code = %q, want rate_limited_local", code)
	}

	// Verify /v1/ limiter also rate-limits when exhausted even if general limiter has tokens.
	tronGridLimiter = rate.NewLimiter(100, 100)
	v1Lim := rate.NewLimiter(rate.Every(time.Hour), 1)
	v1Lim.Allow()
	tronGridV1Limiter = v1Lim
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	_, code2 := tronGridDo(ctx2, http.MethodGet, "/v1/accounts/TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7/transactions/trc20", nil)
	if code2 != "rate_limited_local" {
		t.Errorf("tronGridDo(/v1/...) code = %q, want rate_limited_local", code2)
	}
}

func TestCheckIssuerFreezeTron(t *testing.T) {
	origURL, origLim := tronGridBaseURL, tronGridLimiter
	tronGridLimiter = rate.NewLimiter(rate.Inf, 10)
	defer func() {
		tronGridBaseURL = origURL
		tronGridLimiter = origLim
	}()

	const addr = "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7"

	tests := []struct {
		name       string
		status     int
		body       string
		wantFrozen bool
		wantCode   string
	}{
		{
			name:       "frozen (1)",
			status:     200,
			body:       `{"result":{"result":true},"constant_result":["` + oracleTrue[2:] + `"]}`,
			wantFrozen: true,
		},
		{
			name:       "not frozen (0)",
			status:     200,
			body:       `{"result":{"result":true},"constant_result":["` + oracleFalse[2:] + `"]}`,
			wantFrozen: false,
		},
		{
			name:     "result.result is false",
			status:   200,
			body:     `{"result":{"result":false},"constant_result":["` + oracleFalse[2:] + `"]}`,
			wantCode: "bad_response",
		},
		{
			name:     "malformed constant_result",
			status:   200,
			body:     `{"result":{"result":true},"constant_result":["0x"]}`,
			wantCode: "bad_response",
		},
		{
			name:     "http 429",
			status:   429,
			body:     ``,
			wantCode: "rate_limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/wallet/triggerconstantcontract" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if r.Header.Get("User-Agent") != "biglens/1.0" {
					t.Errorf("User-Agent = %q, want biglens/1.0", r.Header.Get("User-Agent"))
				}
				var req struct {
					ContractAddress  string `json:"contract_address"`
					FunctionSelector string `json:"function_selector"`
					Parameter        string `json:"parameter"`
					Visible          bool   `json:"visible"`
				}
				b, _ := io.ReadAll(r.Body)
				json.Unmarshal(b, &req)
				if req.ContractAddress != "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" ||
					req.FunctionSelector != "isBlackListed(address)" ||
					len(req.Parameter) != 64 || !req.Visible {
					t.Errorf("unexpected request payload: %s", b)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			tronGridBaseURL = srv.URL

			states, code := checkIssuerFreeze(context.Background(), "tron", addr, "", nil)
			if tt.wantCode != "" {
				if code != tt.wantCode || len(states) != 0 {
					t.Errorf("got (%+v, %q), want (nil, %q)", states, code, tt.wantCode)
				}
				return
			}
			if code != "" || len(states) != 1 || states[0].Frozen != tt.wantFrozen || states[0].Token != "USDT" {
				t.Errorf("got (%+v, %q), want Frozen=%v", states, code, tt.wantFrozen)
			}
		})
	}
}

func TestIssuerFreezeDedupWithLocal(t *testing.T) {
	live := []issuerFreezeState{
		{Token: "USDT", Contract: "0xdac17f958d2ee523a2206206994597c13d831ec7", Frozen: true},
		{Token: "USDC", Contract: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Frozen: false},
	}

	// Local already shows USDT frozen → no duplicate live clue.
	localFrozen := []stablecoinState{{Token: "USDT", Action: "freeze"}}
	if got := issuerFreezeClues("eth", live, localFrozen, "2026-10-05T12:00:00Z"); len(got) != 0 {
		t.Errorf("expected dedup when local already frozen, got %+v", got)
	}

	// Local shows USDT unfrozen (or has no record), but live says frozen → emit live clue.
	localUnfrozen := []stablecoinState{{Token: "USDT", Action: "unfreeze"}}
	got := issuerFreezeClues("eth", live, localUnfrozen, "2026-10-05T12:00:00Z")
	if len(got) != 1 {
		t.Fatalf("got %d clues, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Severity != sevCritical || c.Source != "issuer_freeze" || c.Code != "issuer_frozen_live" ||
		c.Token != "USDT" || c.Title != "Currently frozen by the USDT contract (live check)" ||
		c.RefURL != "https://etherscan.io/address/0xdac17f958d2ee523a2206206994597c13d831ec7" {
		t.Errorf("unexpected clue: %+v", c)
	}
}

func TestCheckIssuerFreezeSolana(t *testing.T) {
	const holder = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
	usdtATA, err := deriveSolanaATA(holder, "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB")
	if err != nil {
		t.Fatal(err)
	}
	usdcATA, err := deriveSolanaATA(holder, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("indexed call refused (publicnode 403) falls back to canonical ATAs via one getMultipleAccounts", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &req); err != nil {
				t.Fatalf("unmarshal rpc req: %v", err)
			}
			if req.Method == "getTokenAccountsByOwner" {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Indexed requests require a personal token."},"id":1}`))
				return
			}
			if req.Method != "getMultipleAccounts" || len(req.Params) != 2 {
				t.Fatalf("unexpected RPC method/params: %s", b)
			}
			var keys []string
			json.Unmarshal(req.Params[0], &keys)
			if len(keys) != 2 || keys[0] != usdtATA || keys[1] != usdcATA {
				t.Fatalf("keys = %v, want [%s, %s]", keys, usdtATA, usdcATA)
			}
			var cfg map[string]string
			json.Unmarshal(req.Params[1], &cfg)
			if cfg["encoding"] != "jsonParsed" || cfg["commitment"] != "confirmed" {
				t.Fatalf("cfg = %v, want encoding=jsonParsed commitment=confirmed", cfg)
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454804650},"value":[null,{"data":{"parsed":{"info":{"state":"frozen"},"type":"account"},"program":"spl-token","space":165}}]}}`))
		}))
		defer srv.Close()

		states, code := checkIssuerFreeze(context.Background(), "sol", holder, "", []string{srv.URL})
		if code != "" || len(states) != 2 {
			t.Fatalf("got (%+v, %q), want 2 states and empty error code", states, code)
		}
		if states[0].Token != "USDT" || states[0].Frozen {
			t.Errorf("USDT state = %+v, want unfrozen", states[0])
		}
		if states[1].Token != "USDC" || !states[1].Frozen {
			t.Errorf("USDC state = %+v, want frozen", states[1])
		}
	})

	// A wallet can hold USDC in a token account that is not its canonical ATA;
	// a freeze on that account must still be reported (issuers freeze accounts,
	// not owners).
	t.Run("frozen non-canonical token account found via getTokenAccountsByOwner", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &req)
			if req.Method != "getTokenAccountsByOwner" || len(req.Params) != 3 {
				t.Errorf("unexpected RPC call: %s", b)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var owner string
			var filter map[string]string
			_ = json.Unmarshal(req.Params[0], &owner)
			_ = json.Unmarshal(req.Params[1], &filter)
			if owner != holder {
				t.Errorf("owner = %q, want %q", owner, holder)
			}
			switch filter["mint"] {
			case "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":[]}}`))
			case "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v":
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":[
					{"pubkey":"` + usdcATA + `","account":{"data":{"program":"spl-token","parsed":{"type":"account","info":{"state":"initialized"}}}}},
					{"pubkey":"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM","account":{"data":{"program":"spl-token","parsed":{"type":"account","info":{"state":"frozen"}}}}}
				]}}`))
			default:
				t.Errorf("unexpected mint filter %v", filter)
			}
		}))
		defer srv.Close()

		states, code := checkIssuerFreeze(context.Background(), "sol", holder, "", []string{srv.URL})
		if code != "" || len(states) != 2 {
			t.Fatalf("got (%+v, %q), want 2 states and empty error code", states, code)
		}
		if states[0].Token != "USDT" || states[0].Frozen {
			t.Errorf("USDT state = %+v, want unfrozen (no accounts)", states[0])
		}
		if states[1].Token != "USDC" || !states[1].Frozen {
			t.Errorf("USDC state = %+v, want frozen via the non-canonical account", states[1])
		}
	})

	t.Run("RPC failover on HTTP error", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer bad.Close()
		good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454804650},"value":[{"data":{"parsed":{"info":{"state":"initialized"}}}}]}}`))
		}))
		defer good.Close()

		states, code := checkIssuerFreeze(context.Background(), "sol", holder, "USDC", []string{bad.URL, good.URL})
		if code != "" || len(states) != 1 || states[0].Frozen {
			t.Fatalf("failover got (%+v, %q), want 1 unfrozen state", states, code)
		}
	})
}

func TestLookupSolanaSources(t *testing.T) {
	defaults := (CryptoGasConfig{}).withDefaults()
	if len(defaults.SolanaRPCURLs) != 2 ||
		defaults.SolanaRPCURLs[0] != "https://solana-rpc.publicnode.com" ||
		defaults.SolanaRPCURLs[1] != "https://api.mainnet-beta.solana.com" {
		t.Fatalf("default SolanaRPCURLs = %v", defaults.SolanaRPCURLs)
	}

	if got := strings.Join(riskSourcesFor("sol"), ","); got != "ofac,issuer_freeze" {
		t.Fatalf("riskSourcesFor(sol) = %q, want ofac,issuer_freeze (must not include goplus)", got)
	}

	const addr = "42RLPACwZPx3vYYmxSueqsogfynBDqXK298EDsNoyoHi"
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := store.replaceList(context.Background(), "ofac", []listEntry{
		{Address: addr, Label: "tagged SOL"},
	}, "hash-sol", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	solRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v") {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454804650},"value":[]}}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":454804650},"value":[{"pubkey":"x","account":{"data":{"parsed":{"info":{"state":"frozen"}}}}}]}}`))
	}))
	defer solRPC.Close()

	var gpCalls atomic.Int32
	origGoPlus := goplusBaseURL
	gpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gpCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	goplusBaseURL = gpSrv.URL + "/api/v1/address_security/"
	defer func() {
		goplusBaseURL = origGoPlus
		gpSrv.Close()
	}()

	svc := newAddressRiskService(store, nil)
	svc.now = func() time.Time { return now }
	svc.chainRPCs = map[string][]string{"sol": {solRPC.URL}}

	res := svc.lookup(context.Background(), "sol", addr)
	if len(res.Sources) != 2 ||
		res.Sources[0].ID != "ofac" || res.Sources[0].Status != "ok" ||
		res.Sources[1].ID != "issuer_freeze" || res.Sources[1].Status != "ok" {
		t.Fatalf("sol sources = %+v, want [ofac, issuer_freeze] all ok (no goplus)", res.Sources)
	}
	for _, s := range res.Sources {
		if s.ID == "goplus" {
			t.Errorf("sol sources must not contain goplus: %+v", res.Sources)
		}
	}
	if gpCalls.Load() != 0 {
		t.Errorf("sol lookup made %d GoPlus HTTP calls, want 0", gpCalls.Load())
	}
	if res.Summary.Counts.Critical != 2 || res.Summary.Counts.Warning != 0 {
		t.Fatalf("sol summary counts = %+v, clues = %+v", res.Summary.Counts, res.Clues)
	}

	pool, err := store.riskPoolForAddresses(context.Background(), "sol", []string{addr})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool[addr]) != 1 || pool[addr][0] != "ofac" {
		t.Errorf("riskPoolForAddresses(sol) = %+v, want [ofac]", pool)
	}
}
