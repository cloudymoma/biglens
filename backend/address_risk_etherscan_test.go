package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// withEtherscan points the client at h and removes the rate limit (tests
// that measure the limiter install their own).
func withEtherscan(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	origURL, origLim, origT := etherscanBaseURL, etherscanLimiter, riskSourceTimeout
	etherscanBaseURL, etherscanLimiter, riskSourceTimeout = srv.URL+"/v2/api", rate.NewLimiter(rate.Inf, 1), 300*time.Millisecond
	t.Cleanup(func() { srv.Close(); etherscanBaseURL, etherscanLimiter, riskSourceTimeout = origURL, origLim, origT })
	return srv
}

func TestClassifyEtherscanMessage(t *testing.T) {
	for msg, want := range map[string]string{
		"Invalid API Key (#err2)|x":                                     "key_invalid",
		"Missing/Invalid API Key":                                       "key_invalid",
		"Too many invalid api key attempts, please try again later":     "key_throttled",
		"Max rate limit reached":                                        "rate_limited",
		"Max calls per sec rate limit reached (3/sec)":                  "rate_limited",
		"Query Timeout occured. Please select a smaller result dataset": "bad_response",
	} {
		if got := classifyEtherscanMessage(msg); got != want {
			t.Errorf("%q → %q, want %q", msg, got, want)
		}
	}
}

func TestEtherscanCall(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
		wantMsg  string
		wantRaw  bool
	}{
		{"ok rows", 200, `{"status":"1","message":"OK","result":[{"hash":"0x1"}]}`, "", "", true},
		{"no transactions is ok with zero rows", 200, `{"status":"0","message":"No transactions found","result":[]}`, "", "", true},
		{"invalid key inside HTTP 200", 200, `{"status":"0","message":"NOTOK","result":"Invalid API Key (#err2)|x"}`, "key_invalid", "Invalid API Key (#err2)|x", false},
		{"rate limit inside HTTP 200", 200, `{"status":"0","message":"NOTOK","result":"Max calls per sec rate limit reached (3/sec)"}`, "rate_limited", "Max calls per sec rate limit reached (3/sec)", false},
		{"http 429", 429, ``, "rate_limited", "", false},
		{"http 502", 502, ``, "upstream_http_502", "", false},
		{"not json", 200, `<html>`, "bad_response", "", false},
		{"unknown status value", 200, `{"status":"2","result":"?"}`, "bad_response", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withEtherscan(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("chainid") != "1" || q.Get("apikey") != "SECRETKEY1234567" || q.Get("action") != "txlist" {
					t.Errorf("query = %v", q)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			})
			raw, msg, code := etherscanCall(context.Background(), "SECRETKEY1234567", url.Values{"action": {"txlist"}})
			if code != tt.wantCode || msg != tt.wantMsg || (raw != nil) != tt.wantRaw {
				t.Errorf("got (raw %v, %q, %q), want (raw %v, %q, %q)", raw != nil, msg, code, tt.wantRaw, tt.wantMsg, tt.wantCode)
			}
		})
	}
}

// The request URL carries the apikey; a timeout error code must not.
func TestEtherscanTimeoutDoesNotLeakKey(t *testing.T) {
	withEtherscan(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, msg, code := etherscanCall(ctx, "SECRETKEY1234567", url.Values{"action": {"txlist"}})
	if code != "timeout" || strings.Contains(code+msg, "SECRET") {
		t.Errorf("code %q msg %q", code, msg)
	}
}

// The shared limiter keeps every 1-second window at ≤3 requests (free tier).
func TestEtherscanLimiterWindow(t *testing.T) {
	var mu sync.Mutex
	var hits []time.Time
	withEtherscan(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		mu.Unlock()
		w.Write([]byte(`{"status":"1","result":[]}`))
	})
	etherscanLimiter = rate.NewLimiter(2.5, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); etherscanCall(context.Background(), "K", url.Values{}) }()
	}
	wg.Wait()
	for i := range hits {
		n := 0
		for j := range hits {
			if d := hits[j].Sub(hits[i]); d >= 0 && d < time.Second {
				n++
			}
		}
		if n > 3 {
			t.Fatalf("%d requests within 1s starting at hit %d", n, i)
		}
	}
}

func TestEtherscanLimiterWaitRespectsBudget(t *testing.T) {
	withEtherscan(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"1","result":[]}`)) })
	etherscanLimiter = rate.NewLimiter(0.1, 1)
	etherscanLimiter.Allow() // drain the only token
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, code := etherscanCall(ctx, "K", url.Values{}); code != "rate_limited_local" {
		t.Errorf("code = %q, want rate_limited_local", code)
	}
}
