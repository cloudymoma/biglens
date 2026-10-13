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
)

// vitalik.eth's real eth_getCode result on 2026-10-13: an EIP-7702
// delegation designator pointing at AmbireAccount7702.
const (
	eip7702TestAddr     = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"
	eip7702TestDelegate = "0x5a7fc11397e9a8ad41bf10bf13f22b0a63f96f6d"
	eip7702TestCode     = "0xef01005a7fc11397e9a8ad41bf10bf13f22b0a63f96f6d"
)

func TestClassifyEVMCode(t *testing.T) {
	tests := []struct {
		name, code   string
		wantKind     string
		wantDelegate string
		wantErr      bool
	}{
		{"empty code is a plain EOA", "0x", evmCodeEOA, "", false},
		{"delegation designator", eip7702TestCode, evmCodeDelegated, eip7702TestDelegate, false},
		{"designator is case-insensitive", "0xEF01005A7FC11397E9A8AD41BF10BF13F22B0A63F96F6D", evmCodeDelegated, eip7702TestDelegate, false},
		{"runtime bytecode is a contract", "0x6080604052348015600f57600080fd5b50", evmCodeContract, "", false},
		// 23 bytes but a different prefix: not a designator.
		{"23 bytes without the ef0100 prefix", "0xef02005a7fc11397e9a8ad41bf10bf13f22b0a63f96f6d", evmCodeContract, "", false},
		// The ef0100 prefix with the wrong length is ordinary code.
		{"ef0100 prefix but 24 bytes", eip7702TestCode + "00", evmCodeContract, "", false},
		// A zero delegate clears the code (EIP-7702); never report it as active.
		{"zero delegate is not an active delegation", "0xef0100" + strings.Repeat("0", 40), evmCodeEOA, "", false},
		{"missing 0x prefix", "ef0100", "", "", true},
		{"odd number of hex digits", "0x608", "", "", true},
		{"non-hex characters", "0xzz", "", "", true},
		{"empty string", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyEVMCode(tt.code)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Kind != tt.wantKind || got.Delegate != tt.wantDelegate {
				t.Errorf("classifyEVMCode(%q) = %+v, want kind %q delegate %q", tt.code, got, tt.wantKind, tt.wantDelegate)
			}
		})
	}
}

// fakeCodeRPC answers eth_getCode with code (or an HTTP status when code is
// empty and status != 0) and counts eth_getCode requests.
func fakeCodeRPC(t *testing.T, code string, status int, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Method != "eth_getCode" {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
			return
		}
		calls.Add(1)
		var tag string
		if len(req.Params) == 2 {
			_ = json.Unmarshal(req.Params[1], &tag)
		}
		if tag != "latest" {
			http.Error(w, "want latest", http.StatusBadRequest)
			return
		}
		time.Sleep(delay)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + code + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCheckEIP7702FallsBackToNextRPC(t *testing.T) {
	bad, badCalls := fakeCodeRPC(t, "", http.StatusBadGateway, 0)
	good, goodCalls := fakeCodeRPC(t, eip7702TestCode, 0, 0)
	info, code := checkEIP7702(context.Background(), []string{bad.URL, good.URL}, eip7702TestAddr)
	if code != "" || info.Kind != evmCodeDelegated || info.Delegate != eip7702TestDelegate {
		t.Fatalf("checkEIP7702 = %+v, %q; want delegated via the second RPC", info, code)
	}
	if badCalls.Load() != 1 || goodCalls.Load() != 1 {
		t.Errorf("calls bad=%d good=%d, want 1 each", badCalls.Load(), goodCalls.Load())
	}
}

func TestCheckEIP7702Errors(t *testing.T) {
	orig := riskSourceTimeout
	riskSourceTimeout = 150 * time.Millisecond
	t.Cleanup(func() { riskSourceTimeout = orig })

	down, _ := fakeCodeRPC(t, "", http.StatusTooManyRequests, 0)
	if _, code := checkEIP7702(context.Background(), []string{down.URL}, eip7702TestAddr); code != "rate_limited" {
		t.Errorf("429 code = %q, want rate_limited", code)
	}
	garbage, _ := fakeCodeRPC(t, "0x60z", 0, 0)
	if _, code := checkEIP7702(context.Background(), []string{garbage.URL}, eip7702TestAddr); code != "bad_response" {
		t.Errorf("malformed code = %q, want bad_response", code)
	}
	slow, _ := fakeCodeRPC(t, "0x", 0, 400*time.Millisecond)
	if _, code := checkEIP7702(context.Background(), []string{slow.URL}, eip7702TestAddr); code != "timeout" {
		t.Errorf("slow RPC code = %q, want timeout", code)
	}
	if _, code := checkEIP7702(context.Background(), nil, eip7702TestAddr); code != "unavailable" {
		t.Errorf("no RPC code = %q, want unavailable", code)
	}
}

func TestFetchBlockscoutAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/v2/addresses/") {
		case "0x1":
			w.Write([]byte(`{"is_contract":true,"is_verified":true,"name":"AmbireAccount7702","is_scam":false,"reputation":"ok"}`))
		case "0x2":
			w.Write([]byte(`{"is_contract":true,"is_verified":false,"name":null,"is_scam":false,"reputation":"ok",
				"public_tags":[{"display_name":"Fake_Phishing1234"}],"metadata":{"tags":[{"name":"Drainer"}]}}`))
		case "0x3":
			w.WriteHeader(http.StatusNotFound)
		case "0x4":
			w.Write([]byte(`{"is_scam":true,"name":{"unexpected":"object"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	info, code := fetchBlockscoutAddress(ctx, srv.URL, "0x1")
	if code != "" || !info.Found || !info.IsContract || !info.IsVerified || info.Name != "AmbireAccount7702" || info.Scam {
		t.Errorf("verified contract = %+v, %q", info, code)
	}
	info, code = fetchBlockscoutAddress(ctx, srv.URL, "0x2")
	if code != "" || info.IsVerified || info.Name != "" || strings.Join(info.RiskTags, ",") != "Fake_Phishing1234,Drainer" {
		t.Errorf("tagged contract = %+v, %q", info, code)
	}
	if info, code = fetchBlockscoutAddress(ctx, srv.URL, "0x3"); code != "" || info.Found {
		t.Errorf("404 = %+v, %q; want a definitive 'no record'", info, code)
	}
	// An unexpected name type must not break the scam verdict.
	if info, code = fetchBlockscoutAddress(ctx, srv.URL, "0x4"); code != "" || !info.Scam || info.Name != "" {
		t.Errorf("odd name type = %+v, %q", info, code)
	}
	if _, code = fetchBlockscoutAddress(ctx, srv.URL, "0x5"); code != "upstream_http_500" {
		t.Errorf("500 code = %q", code)
	}
}

func TestEIP7702Clues(t *testing.T) {
	const asOf = "2026-10-13T10:00:00Z"
	delegated := evmCodeInfo{Kind: evmCodeDelegated, Delegate: eip7702TestDelegate}
	tests := []struct {
		name       string
		info       evmCodeInfo
		bs         blockscoutAddressInfo
		bsCode     string
		bsDisabled bool
		wantSev    string
		wantTitle  []string // substrings
		wantDetail []string
	}{
		{name: "plain EOA has no clue", info: evmCodeInfo{Kind: evmCodeEOA}},
		{name: "contract is info", info: evmCodeInfo{Kind: evmCodeContract}, wantSev: sevInfo,
			wantTitle: []string{"This address is a smart contract (has bytecode)."}},
		{name: "delegate flagged as scam is critical", info: delegated,
			bs:      blockscoutAddressInfo{Found: true, IsContract: true, Scam: true, RiskTags: []string{"Drainer"}},
			wantSev: sevCritical, wantTitle: []string{"EIP-7702 delegation active → " + eip7702TestDelegate, "flagged"},
			wantDetail: []string{"Drainer", "revoke by signing a new authorization to the zero address"}},
		{name: "unverified delegate is a warning", info: delegated,
			bs:      blockscoutAddressInfo{Found: true, IsContract: true},
			wantSev: sevWarning, wantTitle: []string{"EIP-7702 delegation active → " + eip7702TestDelegate},
			wantDetail: []string{"no verified source code", "sweeper contract"}},
		{name: "verified delegate without a name is a warning", info: delegated,
			bs:      blockscoutAddressInfo{Found: true, IsContract: true, IsVerified: true},
			wantSev: sevWarning, wantTitle: []string{"EIP-7702 delegation active → " + eip7702TestDelegate}},
		{name: "delegate unknown to Blockscout is a warning", info: delegated,
			wantSev: sevWarning, wantDetail: []string{"no record of the delegate"}},
		{name: "verified named delegate is info", info: delegated,
			bs:      blockscoutAddressInfo{Found: true, IsContract: true, IsVerified: true, Name: "AmbireAccount7702"},
			wantSev: sevInfo, wantTitle: []string{"delegated to verified contract AmbireAccount7702", eip7702TestDelegate},
			wantDetail: []string{"not a safety rating", "revoke"}},
		{name: "enrichment failure keeps the warning", info: delegated, bsCode: "timeout",
			wantSev: sevWarning, wantTitle: []string{"EIP-7702 delegation active → " + eip7702TestDelegate},
			wantDetail: []string{"could not be checked (timeout)"}},
		{name: "Blockscout disabled keeps the warning", info: delegated, bsDisabled: true,
			wantSev: sevWarning, wantDetail: []string{"not checked"}},
		{name: "contract name is sanitized", info: delegated,
			bs:      blockscoutAddressInfo{Found: true, IsVerified: true, Name: "Evil\nName" + strings.Repeat("x", 100)},
			wantSev: sevInfo, wantTitle: []string{"Evil Name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eip7702Clues("eth", eip7702TestAddr, tt.info, tt.bs, tt.bsCode, tt.bsDisabled, asOf)
			if tt.wantSev == "" {
				if len(got) != 0 {
					t.Fatalf("clues = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("clues = %+v, want exactly one", got)
			}
			c := got[0]
			if c.Severity != tt.wantSev || c.Source != "eip7702" || c.AsOf != asOf || c.RefURL == "" {
				t.Errorf("clue = %+v, want severity %s from eip7702", c, tt.wantSev)
			}
			for _, s := range tt.wantTitle {
				if !strings.Contains(c.Title, s) {
					t.Errorf("title %q missing %q", c.Title, s)
				}
			}
			for _, s := range tt.wantDetail {
				if !strings.Contains(c.Detail, s) {
					t.Errorf("detail %q missing %q", c.Detail, s)
				}
			}
			if strings.ContainsAny(c.Title, "\n\r") || len([]rune(c.Title)) > 200 {
				t.Errorf("title not sanitized: %q", c.Title)
			}
			if tt.info.Kind == evmCodeDelegated && c.RefURL != "https://etherscan.io/address/"+eip7702TestDelegate {
				t.Errorf("ref = %q, want the delegate on the explorer", c.RefURL)
			}
		})
	}
}

// eip7702Upstreams fakes one RPC (eth_getCode returns code, everything else a
// false 32-byte word) and one Blockscout that answers the delegate with
// delegateBody (or delegateStatus) and every other address as clean.
type eip7702Upstreams struct {
	rpc, bs       *httptest.Server
	delegateHits  atomic.Int32
	targetBSCalls atomic.Int32
}

func newEIP7702Upstreams(t *testing.T, code, delegateBody string, delegateStatus int) *eip7702Upstreams {
	t.Helper()
	u := &eip7702Upstreams{}
	u.rpc, _ = fakeCodeRPC(t, code, 0, 0)
	u.bs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/addresses/" + eip7702TestDelegate:
			u.delegateHits.Add(1)
			if delegateStatus != 0 {
				w.WriteHeader(delegateStatus)
				return
			}
			w.Write([]byte(delegateBody))
		default:
			u.targetBSCalls.Add(1)
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
		}
	}))
	t.Cleanup(u.bs.Close)
	return u
}

func eip7702TestService(t *testing.T, u *eip7702Upstreams) *addressRiskService {
	t.Helper()
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	_ = up
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)
	svc := newAddressRiskService(store, []string{u.rpc.URL})
	svc.now = func() time.Time { return riskNow }
	svc.chainRPCs = map[string][]string{"base": {u.rpc.URL}}
	svc.blockscoutURLs = map[string]string{"base": u.bs.URL}
	return svc
}

func TestLookupEIP7702DelegatedEnriched(t *testing.T) {
	u := newEIP7702Upstreams(t, eip7702TestCode,
		`{"is_contract":true,"is_verified":false,"name":null,"is_scam":true,"reputation":"scam"}`, 0)
	svc := eip7702TestService(t, u)
	res := svc.lookup(context.Background(), "base", eip7702TestAddr)

	src := sourceByID(t, res.Sources, "eip7702")
	if src.Status != "ok" || !src.SendsAddress || strings.Join(src.Hosts, ",") != strings.Join(hostsOf(u.rpc.URL), ",") {
		t.Errorf("eip7702 source = %+v, want ok, sends_address, RPC hosts only", src)
	}
	var found *riskClue
	for i := range res.Clues {
		if res.Clues[i].Source == "eip7702" {
			found = &res.Clues[i]
		}
	}
	if found == nil || found.Severity != sevCritical || found.Code != "eip7702_delegated" {
		t.Fatalf("eip7702 clue = %+v in %+v", found, res.Clues)
	}
	if u.delegateHits.Load() != 1 {
		t.Errorf("delegate enrichment calls = %d, want 1", u.delegateHits.Load())
	}
	if !res.cacheable() {
		t.Error("a fully checked delegation must be cacheable")
	}
}

func TestLookupEIP7702EnrichmentFailureKeepsClue(t *testing.T) {
	u := newEIP7702Upstreams(t, eip7702TestCode, "", http.StatusBadGateway)
	svc := eip7702TestService(t, u)
	res := svc.lookup(context.Background(), "base", eip7702TestAddr)
	if src := sourceByID(t, res.Sources, "eip7702"); src.Status != "ok" {
		t.Errorf("eip7702 source = %+v; a failed enrichment must not fail the core check", src)
	}
	var sev string
	for _, c := range res.Clues {
		if c.Source == "eip7702" {
			sev = c.Severity
		}
	}
	if sev != sevWarning {
		t.Fatalf("eip7702 clue severity = %q in %+v, want the warning core clue", sev, res.Clues)
	}
	if res.cacheable() {
		t.Error("a lookup whose delegate check failed must not be cached")
	}
}

func TestLookupEIP7702EOAAndErrors(t *testing.T) {
	u := newEIP7702Upstreams(t, "0x", "", 0)
	svc := eip7702TestService(t, u)
	res := svc.lookup(context.Background(), "base", eip7702TestAddr)
	if src := sourceByID(t, res.Sources, "eip7702"); src.Status != "ok" {
		t.Errorf("eip7702 source = %+v", src)
	}
	for _, c := range res.Clues {
		if c.Source == "eip7702" {
			t.Errorf("plain EOA produced clue %+v", c)
		}
	}
	if u.delegateHits.Load() != 0 {
		t.Errorf("plain EOA triggered %d delegate lookups", u.delegateHits.Load())
	}

	down, _ := fakeCodeRPC(t, "", http.StatusInternalServerError, 0)
	svc.chainRPCs = map[string][]string{"base": {down.URL}}
	res = svc.lookup(context.Background(), "base", eip7702TestAddr)
	if src := sourceByID(t, res.Sources, "eip7702"); src.Status != "error" || src.Error != "upstream_http_500" {
		t.Errorf("eip7702 source with a failing RPC = %+v", src)
	}

	svc.chainRPCs = map[string][]string{"base": {}}
	res = svc.lookup(context.Background(), "base", eip7702TestAddr)
	if src := sourceByID(t, res.Sources, "eip7702"); src.Status != "not_configured" {
		t.Errorf("eip7702 source without RPCs = %+v, want not_configured (never 'unavailable', which means local lists)", src)
	}
}
