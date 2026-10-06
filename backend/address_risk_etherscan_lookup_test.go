package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testEtherscanKey = "TESTKEYABCDEFGHIJ123"

// fakeEtherscan answers each account action from byAction ("" → no rows)
// and counts requests.
func fakeEtherscan(t *testing.T, byAction map[string]string, delay time.Duration) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	withEtherscan(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(delay)
		body, ok := byAction[r.URL.Query().Get("action")]
		if !ok || body == "" {
			body = `{"status":"0","message":"No transactions found","result":[]}`
		}
		w.Write([]byte(body))
	})
	return &calls
}

// seededService has every local source ok, the OFAC list containing ofacAddr,
// and fake oracle/GoPlus upstreams that find nothing.
func seededService(t *testing.T) (*addressRiskService, *riskStore) {
	t.Helper()
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ofacAddr}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: mewAddr}}, "h", riskNow)
	markStablecoinSynced(t, store)
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	return svc, store
}

func TestServiceLookupEtherscanNotConfigured(t *testing.T) {
	calls := fakeEtherscan(t, nil, 0)
	svc, _ := seededService(t)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "not_configured" || !es.SendsAddress || es.SignupURL != "https://etherscan.io/myapikey" ||
		es.HelpURL != "https://docs.etherscan.io/set-up-your-api-key" || calls.Load() != 0 || res.AssociationScope != nil {
		t.Errorf("etherscan source = %+v, calls %d", es, calls.Load())
	}
	want := "No records in the 6 sources checked. Not checked: Etherscan association analysis (no API key)."
	if res.Summary.Text != want || strings.Join(res.Summary.Skipped, ",") != "etherscan" {
		t.Errorf("summary = %+v", res.Summary)
	}
}

func TestServiceLookupAssociation(t *testing.T) {
	fakeEtherscan(t, map[string]string{
		"tokentx": `{"status":"1","message":"OK","result":[{"hash":"0xreal","from":"` + ofacAddr + `","to":"` + assocTarget +
			`","value":"2500000","timeStamp":"1700000000","contractAddress":"` + usdtAddr + `"}]}`,
	}, 0)
	svc, _ := seededService(t)
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "ok" || strings.Join(es.Hosts, ",") != strings.Join(hostsOf(etherscanBaseURL), ",") || !es.SendsAddress {
		t.Errorf("etherscan source = %+v", es)
	}
	if len(res.Clues) != 1 || res.Clues[0].Code != "association" || res.Summary.Counts.Association != 1 {
		t.Fatalf("clues = %+v", res.Clues)
	}
	if res.AssociationScope == nil || res.AssociationScope.TokenTx.N != 1 || res.AssociationScope.TxList.N != 0 {
		t.Errorf("scope = %+v", res.AssociationScope)
	}
	if !res.cacheable() {
		t.Error("a complete result with a key must be cacheable")
	}
}

// Without the local pool the intersection is always empty and would read as
// "no association": no request may be made.
func TestServiceLookupEtherscanWithoutLocalPool(t *testing.T) {
	calls := fakeEtherscan(t, nil, 0)
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	svc := newAddressRiskService(nil, []string{up.oracle.URL})
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "error" || es.Error != "local_pool_unavailable" || calls.Load() != 0 {
		t.Errorf("etherscan source = %+v, calls %d", es, calls.Load())
	}
}

func TestServiceLookupEtherscanKeyInvalid(t *testing.T) {
	fakeEtherscan(t, map[string]string{"txlist": `{"status":"0","message":"NOTOK","result":"Invalid API Key (#err2)|x"}`}, 0)
	svc, _ := seededService(t)
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "error" || es.Error != "key_invalid" || res.cacheable() || res.AssociationScope != nil {
		t.Errorf("etherscan source = %+v cacheable %v", es, res.cacheable())
	}
	if !strings.HasPrefix(res.Summary.Text, "Nothing found in 6 sources that completed; 1 could not be fully checked") {
		t.Errorf("text = %q", res.Summary.Text)
	}
}

// The cache key carries hasKey: a result cached before a key was configured
// must not hide the association analysis afterwards.
func TestAddressRiskLookupCacheKeyTracksEtherscanKey(t *testing.T) {
	calls := fakeEtherscan(t, nil, 0)
	svc, _ := seededService(t)
	h := &APIHandler{cache: NewCache(time.Minute), risk: svc}
	doLookup(t, h, "address="+assocTarget)
	svc.setEtherscanKey(testEtherscanKey)
	_, res := doLookup(t, h, "address="+assocTarget)
	if sourceByID(t, res.Sources, "etherscan").Status != "ok" || calls.Load() != 3 {
		t.Errorf("after configuring a key: etherscan %+v, calls %d", sourceByID(t, res.Sources, "etherscan"), calls.Load())
	}
}

// A forced Etherscan timeout: the key must appear in neither the response,
// the cache, nor the logs (*url.Error would embed it).
func TestEtherscanKeyNeverLeaks(t *testing.T) {
	fakeEtherscan(t, nil, time.Second)
	var logs bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(orig)
	svc, _ := seededService(t)
	svc.setEtherscanKey(testEtherscanKey)
	h := &APIHandler{cache: NewCache(time.Minute), risk: svc}
	rec, res := doLookup(t, h, "address="+assocTarget)
	if es := sourceByID(t, res.Sources, "etherscan"); es.Error != "timeout" {
		t.Fatalf("etherscan source = %+v", es)
	}
	cached, _ := h.cache.Get("address-risk:lookup:eth:" + assocTarget + ":1")
	cachedJSON, _ := json.Marshal(cached)
	for name, s := range map[string]string{"response": rec.Body.String(), "cache": string(cachedJSON), "logs": logs.String()} {
		if strings.Contains(s, testEtherscanKey) {
			t.Errorf("key leaked into %s", name)
		}
	}
}

// A fresh install or first sync: the store opens but holds no list entries.
// Matching against an empty pool would read as "no association" (spec §8.2).
func TestServiceLookupEtherscanEmptyPool(t *testing.T) {
	calls := fakeEtherscan(t, nil, 0)
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	svc := newAddressRiskService(newTestRiskStore(t), []string{up.oracle.URL})
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "error" || es.Error != "local_pool_unavailable" || calls.Load() != 0 {
		t.Errorf("etherscan source = %+v, calls %d", es, calls.Load())
	}
}

// If Etherscan tightens the free tier, the user must see its reason, not
// just "unexpected response" (spec §14) — with the key stripped.
func TestServiceLookupEtherscanShowsUpstreamReason(t *testing.T) {
	reason := "Free API access is not supported for this chain. Please upgrade your api plan. key=" + testEtherscanKey
	fakeEtherscan(t, map[string]string{"txlist": `{"status":"0","message":"NOTOK","result":"` + reason + `"}`}, 0)
	svc, _ := seededService(t)
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "error" || es.Error != "bad_response" || !strings.Contains(es.LastError, "Free API access is not supported") {
		t.Errorf("etherscan source = %+v", es)
	}
	if strings.Contains(es.LastError, testEtherscanKey) {
		t.Error("upstream reason leaked the key")
	}
}

// The three list calls share one 5 s budget; run one after another, three
// slow 1000-row responses would time out for exactly the busy addresses.
func TestServiceLookupEtherscanCallsOverlap(t *testing.T) {
	fakeEtherscan(t, nil, 400*time.Millisecond)
	svc, _ := seededService(t)
	riskSourceTimeout = time.Second // 3 × 400 ms only fits if the calls overlap
	svc.setEtherscanKey(testEtherscanKey)
	res := svc.lookup(context.Background(), "eth", assocTarget)
	if es := sourceByID(t, res.Sources, "etherscan"); es.Status != "ok" {
		t.Errorf("etherscan source = %+v", es)
	}
}
