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
	origLim := tronGridLimiter
	// 1 token per hour, burst 1, pre-exhausted.
	lim := rate.NewLimiter(rate.Every(time.Hour), 1)
	lim.Allow()
	tronGridLimiter = lim
	defer func() { tronGridLimiter = origLim }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, code := tronGridDo(ctx, http.MethodGet, "/wallet/getnowblock", nil)
	if code != "rate_limited_local" {
		t.Errorf("tronGridDo code = %q, want rate_limited_local", code)
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
