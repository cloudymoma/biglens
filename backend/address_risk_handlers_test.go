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
			var listIDs []string
			for _, l := range resp.Lists {
				listIDs = append(listIDs, l.ID)
			}
			if strings.Join(listIDs, ",") != "ofac,mew_darklist,stablecoin,tron_stablecoin,scam_lookalikes" {
				t.Errorf("lists = %+v, want ofac,mew_darklist,stablecoin,tron_stablecoin,scam_lookalikes", resp.Lists)
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
	h.risk.blockscoutURLs = map[string]string{"arb": ""}

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

// bqHandler is a lookup handler whose BigQuery profile is answered by f.
func bqHandler(t *testing.T, f *fakeBQQuerier, enabled bool) *APIHandler {
	t.Helper()
	svc := bqTestService(t, f)
	s := testBQSettings()
	s.Enabled = enabled
	svc.bqProfile = newBQProfiler(s, f)
	return &APIHandler{cache: NewCache(time.Minute), risk: svc}
}

func TestAddressRiskLookupBQParam(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow(), billed: 954204160}
	h := bqHandler(t, f, true)

	// Default (switch off): no job, source skipped, no bq_profile.
	rec, res := doLookup(t, h, "address="+bqTarget)
	if rec.Code != http.StatusOK || f.calls() != 0 || res.BQProfile != nil {
		t.Fatalf("default lookup: code %d, jobs %d, bq_profile %+v", rec.Code, f.calls(), res.BQProfile)
	}
	if src := sourceByID(t, res.Sources, "bigquery_profile"); src.Status != bqStatusSkipped || src.SendsAddress {
		t.Errorf("default source = %+v", src)
	}

	// bq=1 on eth runs one job; the cached plain result must not be reused.
	_, res = doLookup(t, h, "address="+bqTarget+"&bq=1")
	if f.calls() != 1 || res.BQProfile == nil || res.BQProfile.Status != bqStatusOK || res.BQProfile.BytesBilled != 954204160 {
		t.Fatalf("bq=1: jobs %d, bq_profile %+v", f.calls(), res.BQProfile)
	}
	if src := sourceByID(t, res.Sources, "bigquery_profile"); src.Status != bqStatusOK {
		t.Errorf("bq=1 source = %+v", src)
	}
	// Same request again: served from the lookup cache.
	if _, res = doLookup(t, h, "address="+bqTarget+"&bq=1"); f.calls() != 1 || res.BQProfile == nil {
		t.Errorf("repeat bq=1: jobs %d, bq_profile %+v", f.calls(), res.BQProfile)
	}
	// And the plain key still holds the switch-off result.
	if _, res = doLookup(t, h, "address="+bqTarget); res.BQProfile != nil {
		t.Errorf("plain lookup after bq=1 = %+v; cache keys must differ", res.BQProfile)
	}

	// Only "1" opts in, and only on Ethereum.
	doLookup(t, h, "address="+bqFunder+"&bq=true")
	_, arb := doLookup(t, h, "chain=arb&address="+bqFunder+"&bq=1")
	if f.calls() != 1 || arb.BQProfile != nil {
		t.Errorf("bq=true / chain=arb ran %d jobs, bq_profile %+v; both must be ignored", f.calls(), arb.BQProfile)
	}
	for _, s := range arb.Sources {
		if s.ID == "bigquery_profile" {
			t.Errorf("arb lookup lists %+v", s)
		}
	}
}

func TestAddressRiskLookupBQDisabledServerSide(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow()}
	h := bqHandler(t, f, false)
	_, res := doLookup(t, h, "address="+bqTarget+"&bq=1")
	if f.calls() != 0 || res.BQProfile != nil || sourceByID(t, res.Sources, "bigquery_profile").Status != bqStatusSkipped {
		t.Errorf("disabled server: jobs %d, bq_profile %+v", f.calls(), res.BQProfile)
	}
}

// A slow profile must not hold back the other sources, and a timed-out or
// failed profile must not be cached.
func TestAddressRiskLookupBQTimeoutNotCached(t *testing.T) {
	f := &fakeBQQuerier{row: testBQRow(), delay: 300 * time.Millisecond}
	h := bqHandler(t, f, true)
	h.risk.bqProfile.settings.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, res := doLookup(t, h, "address="+bqTarget+"&bq=1")
	if el := time.Since(start); el > 250*time.Millisecond {
		t.Errorf("lookup took %v; it must return after the profile timeout", el)
	}
	if src := sourceByID(t, res.Sources, "bigquery_profile"); src.Status != bqStatusTimeout {
		t.Fatalf("source = %+v, want timeout", src)
	}
	if sourceByID(t, res.Sources, "ofac").Status != "ok" || res.BQProfile == nil || res.BQProfile.Status != bqStatusTimeout {
		t.Errorf("other sources / profile = %+v / %+v", res.Sources, res.BQProfile)
	}
	time.Sleep(350 * time.Millisecond)
	_, res = doLookup(t, h, "address="+bqTarget+"&bq=1")
	if res.BQProfile == nil || res.BQProfile.Status != bqStatusOK || !res.BQProfile.Cached || f.calls() != 1 {
		t.Errorf("retry = %+v, jobs %d; want the finished job from the profile cache", res.BQProfile, f.calls())
	}
}

func TestAddressRiskSourcesBQProfileInfo(t *testing.T) {
	h := bqHandler(t, &fakeBQQuerier{}, true)
	rec := httptest.NewRecorder()
	h.AddressRiskSources(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/sources", nil))
	var resp riskSourcesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := riskBQProfileInfo{Available: true, MaxBytesBilled: 2 << 30, PerHourLimit: 60, TimeoutSeconds: 1}
	if resp.BigQueryProfile != want {
		t.Errorf("bigquery_profile = %+v, want %+v", resp.BigQueryProfile, want)
	}
	if !strings.Contains(rec.Body.String(), `"bigquery_profile":{"available":true,"max_bytes_billed":2147483648,"per_hour_limit":60`) {
		t.Errorf("JSON shape = %s", rec.Body.String())
	}

	off := bqHandler(t, &fakeBQQuerier{}, false)
	rec = httptest.NewRecorder()
	off.AddressRiskSources(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/sources", nil))
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.BigQueryProfile.Available || resp.BigQueryProfile.Reason != "disabled" {
		t.Errorf("disabled = %+v", resp.BigQueryProfile)
	}
	none := riskHandler(t, nil, nil)
	rec = httptest.NewRecorder()
	none.AddressRiskSources(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/sources", nil))
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.BigQueryProfile.Available || resp.BigQueryProfile.Reason != "not_configured" {
		t.Errorf("unwired = %+v", resp.BigQueryProfile)
	}
}
