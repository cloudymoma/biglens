package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	oldEtherscanKey = "OLDKEYOLDKEYOLDKEY01"
	newEtherscanKey = "NEWKEYNEWKEYNEWKEYa3f9"
	creditsOK       = `{"status":"1","message":"OK","result":{"creditsUsed":7,"creditsAvailable":99000,"creditLimit":100000}}`
)

// keyHandler returns a handler whose config lives in a temp conf.yaml that
// already holds oldEtherscanKey. Validation throttling is off unless a test
// turns it back on.
func keyHandler(t *testing.T) (*APIHandler, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	os.WriteFile(path, []byte("server:\n  port: 1983\naddress_risk:\n  etherscan_api_key: "+oldEtherscanKey+"\n"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := newAddressRiskService(nil, nil)
	svc.cfg = cfg
	svc.setEtherscanKey(cfg.AddressRisk.EtherscanAPIKey)
	orig := riskKeyValidateEvery
	riskKeyValidateEvery = 0
	t.Cleanup(func() { riskKeyValidateEvery = orig })
	return &APIHandler{cache: NewCache(time.Minute), risk: svc}, path
}

func postKey(h *APIHandler, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/opendata/crypto/address-risk/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	addressRiskKeysHandler(h).ServeHTTP(rec, req)
	return rec
}

func fileKey(t *testing.T, path string) string {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.AddressRisk.EtherscanAPIKey
}

// A malformed key is rejected locally: no upstream request, so a crafted
// value like "X&action=balance" can never reach Etherscan.
func TestKeysRejectBadInputWithoutUpstream(t *testing.T) {
	calls := fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, 0)
	h, _ := keyHandler(t)
	for _, body := range []string{
		`{"provider":"etherscan","key":"X&action=balance"}`,
		`{"provider":"etherscan","key":"short"}`,
		`{"provider":"bscscan","key":"` + newEtherscanKey + `"}`,
		`not json`,
		`{"provider":"etherscan","key":"` + strings.Repeat("A", 2000) + `"}`, // > 1 KiB body
	} {
		if rec := postKey(h, body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%.40s…: status %d, want 400", body, rec.Code)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestKeysUpstreamRejectionKeepsOldKey(t *testing.T) {
	fakeEtherscan(t, map[string]string{"getapilimit": `{"status":"0","message":"NOTOK","result":"Invalid API Key (#err2)|` + newEtherscanKey + `"}`}, 0)
	h, path := keyHandler(t)
	rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "New keys can take a few minutes to activate") {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), newEtherscanKey) {
		t.Error("the rejected key was echoed back")
	}
	if fileKey(t, path) != oldEtherscanKey || h.risk.etherscanKeySnapshot() != oldEtherscanKey {
		t.Error("a rejected key must not be saved")
	}
}

func TestKeysUpstreamTimeoutIs502(t *testing.T) {
	fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, time.Second)
	h, path := keyHandler(t)
	if rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if fileKey(t, path) != oldEtherscanKey {
		t.Error("key changed after a failed check")
	}
}

func TestKeysSaveSuccess(t *testing.T) {
	fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, 0)
	h, path := keyHandler(t)
	os.WriteFile(filepath.Join(filepath.Dir(path), ".conf.yaml.tmp"), []byte("stale"), 0o644)
	rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	var info riskKeyInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if !info.Configured || info.Hint != "a3f9" || info.CreditsAvailable == nil || *info.CreditsAvailable != 99000 {
		t.Errorf("info = %+v", info)
	}
	if strings.Contains(rec.Body.String(), newEtherscanKey) {
		t.Error("response contains the key")
	}
	if fileKey(t, path) != newEtherscanKey || h.risk.etherscanKeySnapshot() != newEtherscanKey {
		t.Error("key not saved to file and memory")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("conf.yaml mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestKeysSaveFailureKeepsOldKey(t *testing.T) {
	fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, 0)
	h, _ := keyHandler(t)
	h.risk.cfg = &Config{path: filepath.Join(t.TempDir(), "gone", "conf.yaml")}
	h.risk.cfg.AddressRisk.EtherscanAPIKey = oldEtherscanKey
	if rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if h.risk.cfg.AddressRisk.EtherscanAPIKey != oldEtherscanKey || h.risk.etherscanKeySnapshot() != oldEtherscanKey {
		t.Error("memory changed although the file write failed")
	}
}

func TestKeysCrossSiteRejected(t *testing.T) {
	calls := fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, 0)
	h, path := keyHandler(t)
	if rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site status = %d, want 403", rec.Code)
	}
	if calls.Load() != 0 || fileKey(t, path) != oldEtherscanKey {
		t.Error("a cross-site request reached the handler")
	}
	if rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 200 {
		t.Errorf("same-origin status = %d, want 200", rec.Code)
	}
}

func TestKeysDelete(t *testing.T) {
	h, path := keyHandler(t)
	req := httptest.NewRequest("DELETE", "/api/opendata/crypto/address-risk/keys?provider=etherscan", nil)
	rec := httptest.NewRecorder()
	addressRiskKeysHandler(h).ServeHTTP(rec, req)
	var info riskKeyInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if rec.Code != 200 || info.Configured || fileKey(t, path) != "" || h.risk.etherscanKeySnapshot() != "" {
		t.Errorf("status %d info %+v file %q", rec.Code, info, fileKey(t, path))
	}
}

func TestKeysValidationThrottled(t *testing.T) {
	calls := fakeEtherscan(t, map[string]string{"getapilimit": `{"status":"0","message":"NOTOK","result":"Invalid API Key (#err2)"}`}, 0)
	h, _ := keyHandler(t)
	riskKeyValidateEvery = 10 * time.Second
	postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil)
	if rec := postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second check status = %d, want 429", rec.Code)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestKeyHint(t *testing.T) {
	for k, want := range map[string]string{"": "", "abc": "", "abcdefg": "", "abcdefgh": "efgh", newEtherscanKey: "a3f9"} {
		if got := keyHint(k); got != want {
			t.Errorf("keyHint(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestAddressRiskSourcesEtherscanInfo(t *testing.T) {
	h, _ := keyHandler(t)
	rec := httptest.NewRecorder()
	h.AddressRiskSources(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/sources", nil))
	var resp riskSourcesResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Etherscan.Configured || resp.Etherscan.Hint != "EY01" || strings.Contains(rec.Body.String(), oldEtherscanKey) {
		t.Errorf("etherscan info = %+v", resp.Etherscan)
	}
}

// Under -race: key saves racing lookups, deletes and other config writers.
func TestKeysConcurrentWithLookupsAndConfigWrites(t *testing.T) {
	fakeEtherscan(t, map[string]string{"getapilimit": creditsOK}, 0)
	h, _ := keyHandler(t)
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	h.risk.rpcURLs = []string{up.oracle.URL}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); postKey(h, `{"provider":"etherscan","key":"`+newEtherscanKey+`"}`, nil) }()
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			addressRiskKeysHandler(h).ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/opendata/crypto/address-risk/keys?provider=etherscan", nil))
		}()
		go func() { defer wg.Done(); SaveConfig(h.risk.cfg) }()
		go func() { defer wg.Done(); h.risk.lookup(t.Context(), assocTarget) }()
	}
	wg.Wait()
}
