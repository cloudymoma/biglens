package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TronGrid HTTP 429 (Appendix A note): one retry that honors Retry-After
// (default 2 s); otherwise a read error, never a value.
func TestStableTronConstantRetry(t *testing.T) {
	var mu sync.Mutex
	var calls, limited int
	retryAfter := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if limited > 0 {
			limited--
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprintf(w, `{"result":{"result":true},"constant_result":["%064x"]}`, 1)
	}))
	t.Cleanup(srv.Close)
	origURL, origLim, origSleep := tronGridBaseURL, tronGridLimiter, stableTronSleep
	tronGridBaseURL, tronGridLimiter = srv.URL, rateLimiterUnlimited()
	var slept []time.Duration
	stableTronSleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { tronGridBaseURL, tronGridLimiter, stableTronSleep = origURL, origLim, origSleep })

	run := func(n int, after string) (string, string, int, []time.Duration) {
		mu.Lock()
		calls, limited, retryAfter, slept = 0, n, after, nil
		mu.Unlock()
		v, code := stableTronConstant(context.Background(), "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "paused()")
		mu.Lock()
		defer mu.Unlock()
		return v, code, calls, append([]time.Duration(nil), slept...)
	}
	word := fmt.Sprintf("0x%064x", 1)
	if v, code, n, s := run(1, "1"); code != "" || v != word || n != 2 || len(s) != 1 || s[0] != time.Second {
		t.Errorf("429 with Retry-After 1: %q %q, %d calls, slept %v", v, code, n, s)
	}
	if v, code, n, s := run(1, ""); code != "" || v != word || n != 2 || len(s) != 1 || s[0] != 2*time.Second {
		t.Errorf("429 without Retry-After: %q %q, %d calls, slept %v (want the 2 s default)", v, code, n, s)
	}
	if _, code, n, s := run(2, "1"); code != "rate_limited" || n != 2 || len(s) != 1 {
		t.Errorf("two 429s: %q, %d calls, slept %v; want one retry, then a read error", code, n, s)
	}
	if _, code, n, s := run(1, "30"); code != "rate_limited" || n != 1 || len(s) != 0 {
		t.Errorf("Retry-After 30: %q, %d calls, slept %v; want no retry", code, n, s)
	}
	date := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	if _, code, n, s := run(1, date); code != "" || n != 2 || len(s) != 1 || s[0] < time.Second || s[0] > 4*time.Second {
		t.Errorf("Retry-After as a date: %q, %d calls, slept %v", code, n, s)
	}
	if v, code, n, s := run(0, ""); code != "" || v != word || n != 1 || len(s) != 0 {
		t.Errorf("no 429: %q %q, %d calls, slept %v", v, code, n, s)
	}
}

// The first controls sample waits 2 minutes after boot, offset from the
// supply sampler (Appendix A note).
func TestStableControlsFirstDelay(t *testing.T) {
	if stableControlsFirstDelay != 2*time.Minute {
		t.Errorf("first delay = %v, want 2m", stableControlsFirstDelay)
	}
	_, rpcs := newStableControlsStub(t)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	settings := StablecoinConfig{}.settings()
	h := newStableHealth(settings, func() time.Time { return clock })
	c := newStableControlsSampler(settings, newTestRiskStore(t), rpcs, func() time.Time { return clock })
	if c.firstDelay != stableControlsFirstDelay {
		t.Errorf("sampler first delay = %v", c.firstDelay)
	}
	c.firstDelay = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runStableControlsSampler(ctx, c, h); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if s := h.sampler("controls"); s.LastRun != "" {
		t.Errorf("controls ran before the first delay: %+v", s)
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.sampler("controls").LastRun == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.sampler("controls").LastRun == "" {
		t.Error("controls never ran after the first delay")
	}
	cancel()
	<-done
}
