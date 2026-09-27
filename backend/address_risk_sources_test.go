package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	oracleFalse = "0x0000000000000000000000000000000000000000000000000000000000000000"
	oracleTrue  = "0x0000000000000000000000000000000000000000000000000000000000000001"
)

func TestOracleCalldata(t *testing.T) {
	got := oracleCalldata("0x098b716b8aaf21512996dc57eb0615e2383e2f96")
	want := "0xdf592f7d000000000000000000000000098b716b8aaf21512996dc57eb0615e2383e2f96"
	if got != want || len(got) != 74 {
		t.Errorf("calldata = %s (len %d), want %s", got, len(got), want)
	}
}

func TestParseOracleResponse(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantHit  bool
		wantCode string
	}{
		{"true (Ronin exploiter today)", `{"jsonrpc":"2.0","id":1,"result":"` + oracleTrue + `"}`, true, ""},
		{"false (Tornado Router today)", `{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`, false, ""},
		{"missing 0x prefix", `{"result":"` + oracleTrue[2:] + `"}`, false, "bad_response"},
		{"empty 0x (wrong chain / no code)", `{"result":"0x"}`, false, "bad_response"},
		{"json-rpc error object", `{"error":{"code":3,"message":"execution reverted"}}`, false, "bad_response"},
		{"result is an object inside HTTP 200", `{"result":{"code":503}}`, false, "bad_response"},
		{"not json", `<html>`, false, "bad_response"},
		{"other non-zero value", `{"result":"0x0000000000000000000000000000000000000000000000000000000000000002"}`, false, "bad_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, code := parseOracleResponse([]byte(tt.body))
			if hit != tt.wantHit || code != tt.wantCode {
				t.Errorf("got (%v, %q), want (%v, %q)", hit, code, tt.wantHit, tt.wantCode)
			}
		})
	}
}

func oracleServer(t *testing.T, body string, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []json.RawMessage
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if req.Method != "eth_call" || !strings.Contains(string(b), riskOracleContract) {
			t.Errorf("unexpected request: %s", b)
		}
		time.Sleep(delay)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckOracleFailoverAndTimeout(t *testing.T) {
	orig := riskSourceTimeout
	riskSourceTimeout = 200 * time.Millisecond
	defer func() { riskSourceTimeout = orig }()
	addr := "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

	bad := oracleServer(t, `{"error":{"code":-32005,"message":"limit"}}`, 0)
	good := oracleServer(t, `{"result":"`+oracleTrue+`"}`, 0)
	if hit, code := checkOracle(context.Background(), []string{bad.URL, good.URL}, addr); !hit || code != "" {
		t.Errorf("failover: got (%v, %q), want (true, \"\")", hit, code)
	}

	slow := oracleServer(t, `{"result":"`+oracleTrue+`"}`, time.Second)
	// A hanging first RPC gets only its share of the budget; the backup still answers.
	if hit, code := checkOracle(context.Background(), []string{slow.URL, good.URL}, addr); !hit || code != "" {
		t.Errorf("failover after hang: got (%v, %q), want (true, \"\")", hit, code)
	}
	hit, code := checkOracle(context.Background(), []string{slow.URL}, addr)
	if hit || code != "timeout" {
		t.Errorf("timeout: got (%v, %q), want (false, timeout)", hit, code)
	}
	if strings.Contains(code, "http") {
		t.Errorf("error code leaks URL: %q", code)
	}

	down := oracleServer(t, ``, 0)
	down.Close()
	if _, code := checkOracle(context.Background(), []string{down.URL}, addr); code != "network_error" {
		t.Errorf("closed server: code = %q, want network_error", code)
	}
}

func TestGoPlusClues(t *testing.T) {
	result := map[string]any{
		"sanctioned": "1", "stealing_attack": "1", "blacklist_doubt": "1",
		"number_of_malicious_contracts_created": "3",
		"reinit":                                "1", "contract_address": "1", "data_source": "SlowMist,BlockSec",
		"mixer": "", "phishing_activities": "0", "brand_new_future_flag": "1",
	}
	got := goplusClues(result, "0xabc", "2026-09-26T12:00:00Z")
	type row struct{ sev, flag, title string }
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.Severity, c.Flag, c.Title})
		if c.Source != "goplus" || c.Code != "goplus_flag" || c.AsOf != "2026-09-26T12:00:00Z" ||
			c.RefURL != "https://console.gopluslabs.io/malicious-address-detection/0xabc" {
			t.Errorf("bad common fields: %+v", c)
		}
		if c.Detail != "data source: SlowMist, BlockSec" {
			t.Errorf("detail = %q", c.Detail)
		}
	}
	want := []row{
		{"critical", "sanctioned", "GoPlus: sanctioned address"},
		{"warning", "blacklist_doubt", "GoPlus: suspected malicious (unconfirmed)"},
		{"warning", "number_of_malicious_contracts_created", "GoPlus: created 3 malicious contracts"},
		{"warning", "stealing_attack", "GoPlus: stealing attack"},
		{"info", "reinit", "GoPlus: contract can be re-initialized"},
	}
	if len(rows) != len(want) {
		t.Fatalf("clues = %+v, want %+v (contract_address/data_source/unknown/empty must not become clues)", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("clue %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestGoPlusRulesCoverAllKnownFlags(t *testing.T) {
	// The 18 non-metadata fields GoPlus returns (verified 2026-09-26).
	for _, f := range []string{"sanctioned", "phishing_activities", "stealing_attack", "cybercrime",
		"money_laundering", "financial_crime", "blackmail_activities", "darkweb_transactions", "fake_kyc",
		"honeypot_related_address", "malicious_mining_activities", "mixer", "gas_abuse", "fake_token",
		"fake_standard_interface", "blacklist_doubt", "reinit", "number_of_malicious_contracts_created"} {
		if _, ok := goplusRules[f]; !ok {
			t.Errorf("no rule for GoPlus field %q", f)
		}
	}
	for _, f := range []string{"contract_address", "data_source"} {
		if _, ok := goplusRules[f]; ok {
			t.Errorf("%q is metadata and must not be a rule", f)
		}
	}
}

func TestCheckGoPlus(t *testing.T) {
	orig, origT := goplusBaseURL, riskSourceTimeout
	defer func() { goplusBaseURL, riskSourceTimeout = orig, origT }()
	riskSourceTimeout = 200 * time.Millisecond
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    int
		body      string
		delay     time.Duration
		wantCode  string
		wantClues int
	}{
		{"ok with hits", 200, `{"code":1,"message":"ok","result":{"sanctioned":"1","data_source":"SlowMist"}}`, 0, "", 1},
		{"ok no hits", 200, `{"code":1,"message":"ok","result":{"sanctioned":"0"}}`, 0, "", 0},
		{"rate limited in HTTP 200 body", 200, `{"code":4029,"message":"too many requests","result":{}}`, 0, "rate_limited", 0},
		{"data pending", 200, `{"code":2,"message":"pending","result":{}}`, 0, "pending", 0},
		{"other code", 200, `{"code":5000,"message":"err","result":{}}`, 0, "bad_response", 0},
		{"result not an object", 200, `{"code":1,"message":"ok","result":""}`, 0, "bad_response", 0},
		{"http 429", 429, ``, 0, "rate_limited", 0},
		{"http 500", 500, ``, 0, "upstream_http_500", 0},
		{"timeout", 200, `{"code":1,"result":{}}`, time.Second, "timeout", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/0xabc") || r.URL.Query().Get("chain_id") != "1" {
					t.Errorf("unexpected request %s", r.URL)
				}
				time.Sleep(tt.delay)
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			goplusBaseURL = srv.URL + "/api/v1/address_security/"
			clues, code := checkGoPlus(context.Background(), "0xabc", now)
			if code != tt.wantCode || len(clues) != tt.wantClues {
				t.Errorf("got (%d clues, %q), want (%d, %q)", len(clues), code, tt.wantClues, tt.wantCode)
			}
		})
	}
}

func TestBlockscoutRiskTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"Tornado.Cash":                        true,
		"Tornado.Cash: Router":                true,
		"Tornado Cash 100 ETH":                true,
		"tornado-cash":                        true,
		"TornadoCash":                         true,
		"Fake_Phishing123":                    true,
		"Exploiter 7":                         true,
		"Phishing (reported by Scam Sniffer)": true,
		"Scam Sniffer: Phishing":              true,
		"Scam Sniffer":                        false,
		"ScamSniffer":                         false,
		"SanctionsList":                       false,
		"ETHGlobal Hackathon":                 false,
		"OFAC compliance oracle":              false,
		"Uniswap V3: Router":                  false,
	} {
		if got := blockscoutRiskTag(tag); got != want {
			t.Errorf("blockscoutRiskTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
