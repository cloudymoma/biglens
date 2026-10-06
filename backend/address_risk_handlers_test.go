package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func riskHandler(t *testing.T, store *riskStore, rpc []string) *APIHandler {
	t.Helper()
	svc := newAddressRiskService(store, rpc)
	svc.now = func() time.Time { return riskNow }
	return &APIHandler{cache: NewCache(time.Minute), risk: svc}
}

func doLookup(t *testing.T, h *APIHandler, query string) (*httptest.ResponseRecorder, riskLookupResult) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.AddressRiskLookup(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/lookup?"+query, nil))
	var res riskLookupResult
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec, res
}

func TestAddressRiskLookupRejectsBadInput(t *testing.T) {
	h := riskHandler(t, nil, nil)
	for _, q := range []string{"", "address=", "address=vitalik.eth", "address=0x123"} {
		rec, _ := doLookup(t, h, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", q, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "vitalik") {
			t.Errorf("error echoes user input: %q", rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.AddressRiskLookup(rec, httptest.NewRequest("POST", "/api/opendata/crypto/address-risk/lookup?address="+ronin, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestAddressRiskLookupCachesAndRecomputesChecksumWarning(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleTrue+`"}`, `{"code":1,"result":{"sanctioned":"1"}}`, 0)
	store := newTestRiskStore(t)
	store.replaceList(context.Background(), "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(context.Background(), "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)
	h := riskHandler(t, store, []string{up.oracle.URL})

	// Mixed case with one flipped letter: warning on this request only.
	rec, res := doLookup(t, h, "address=%200x098b716B8Aaf21512996dC57EB0615e2383E2f96%20")
	if rec.Code != 200 || !res.ChecksumWarning || res.Address != ronin {
		t.Fatalf("first: code %d, %+v", rec.Code, res)
	}
	_, res2 := doLookup(t, h, "address="+ronin)
	if res2.ChecksumWarning {
		t.Error("cached result leaked the previous request's checksum_warning")
	}
	if up.goplusCalls.Load() != 1 {
		t.Errorf("goplus called %d times; the second lookup must hit the cache", up.goplusCalls.Load())
	}
}

func TestAddressRiskLookupDoesNotCacheErrors(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":4029,"result":{}}`, 0)
	h := riskHandler(t, newTestRiskStore(t), []string{up.oracle.URL})
	doLookup(t, h, "address="+ronin)
	_, res := doLookup(t, h, "address="+ronin)
	if up.goplusCalls.Load() != 2 {
		t.Errorf("goplus called %d times; results with an errored source must not be cached", up.goplusCalls.Load())
	}
	if gp := sourceByID(t, res.Sources, "goplus"); gp.Error != "rate_limited" {
		t.Errorf("goplus source = %+v", gp)
	}
}

func TestAddressRiskLookupStoreUnavailableReturns200(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	h := riskHandler(t, nil, []string{up.oracle.URL})
	rec, res := doLookup(t, h, "address="+ronin)
	if rec.Code != http.StatusOK || res.Sources[0].Error != "unavailable" {
		t.Fatalf("code %d, sources %+v", rec.Code, res.Sources)
	}
}

func TestAddressRiskSources(t *testing.T) {
	for name, store := range map[string]*riskStore{"store ok": newTestRiskStore(t), "store unavailable": nil} {
		t.Run(name, func(t *testing.T) {
			h := riskHandler(t, store, []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org/?k=SECRET"})
			rec := httptest.NewRecorder()
			h.AddressRiskSources(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/sources", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 even when the store is down", rec.Code)
			}
			body := rec.Body.String()
			var resp riskSourcesResponse
			json.Unmarshal([]byte(body), &resp)
			if strings.Join(resp.RPCHosts, ",") != "ethereum-rpc.publicnode.com,eth.drpc.org" || resp.GoPlusHost != "api.gopluslabs.io" {
				t.Errorf("hosts = %+v", resp)
			}
			if strings.Contains(body, "SECRET") {
				t.Error("full RPC URL leaked")
			}
			if len(resp.Lists) != 3 {
				t.Errorf("lists = %+v", resp.Lists)
			}
		})
	}
}

// A lookup made before the first list sync must not be cached: once the lists
// land, retrying has to show the OFAC hit instead of a 10-minute-old "empty".
func TestAddressRiskLookupDoesNotCacheBeforeFirstSync(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	h := riskHandler(t, store, []string{up.oracle.URL})
	if _, res := doLookup(t, h, "address="+ronin); res.Sources[0].Status != "empty" {
		t.Fatalf("precondition: ofac status = %q, want empty", res.Sources[0].Status)
	}
	store.replaceList(context.Background(), "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(context.Background(), "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	_, res := doLookup(t, h, "address="+ronin)
	if len(res.Clues) == 0 || res.Clues[0].Code != "ofac_listed" {
		t.Errorf("clues after first sync = %+v; the pre-sync result was served from cache", res.Clues)
	}
}

func TestAddressRiskLookupChainParam(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)
	h := riskHandler(t, store, []string{up.oracle.URL})

	// Omitting chain defaults to eth.
	rec, resEth := doLookup(t, h, "address="+ronin)
	if rec.Code != http.StatusOK || resEth.Chain != "eth" {
		t.Fatalf("default chain: code=%d, chain=%q", rec.Code, resEth.Chain)
	}
	if up.goplusCalls.Load() != 1 {
		t.Fatalf("goplus calls after eth lookup = %d, want 1", up.goplusCalls.Load())
	}

	// Unknown chain → 400 "unknown chain".
	recBad, _ := doLookup(t, h, "chain=solana&address="+ronin)
	if recBad.Code != http.StatusBadRequest || !strings.Contains(recBad.Body.String(), "unknown chain") {
		t.Errorf("unknown chain: code=%d, body=%q", recBad.Code, recBad.Body.String())
	}

	// Same EVM address on arb has its own cache key and does not reuse eth's cached result.
	recArb, resArb := doLookup(t, h, "chain=arb&address="+ronin)
	if recArb.Code != http.StatusOK || resArb.Chain != "arb" {
		t.Fatalf("arb chain: code=%d, chain=%q", recArb.Code, resArb.Chain)
	}
	if up.goplusCalls.Load() != 2 {
		t.Errorf("goplus calls after arb lookup = %d, want 2 (eth and arb cache keys must not collide)", up.goplusCalls.Load())
	}
}
