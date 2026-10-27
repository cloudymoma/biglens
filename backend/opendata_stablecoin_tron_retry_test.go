package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	waiting := make(chan time.Duration, 1)
	release := make(chan struct{})
	saved := make(chan struct{}, 1)
	c.waitFirst = func(ctx context.Context, d time.Duration) error {
		waiting <- d
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	c.onSaved = func(context.Context) {
		select {
		case saved <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runStableControlsSampler(ctx, c, h); close(done) }()
	if gotDelay := <-waiting; gotDelay != 2*time.Minute {
		t.Errorf("waitFirst duration = %v, want 2m", gotDelay)
	}
	if s := h.sampler("controls"); s.LastRun != "" {
		t.Errorf("controls ran before the first delay: %+v", s)
	}
	close(release)
	<-saved
	deadline := time.Now().Add(5 * time.Second)
	for h.sampler("controls").LastRun == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.sampler("controls").LastRun == "" {
		t.Error("controls never ran after the first delay")
	}
	cancel()
	<-done
}

// TRON control reads (Appendix A, M4 decision): one at a time, at least 1 s
// apart; a field still rate-limited is re-read once at the end of the
// sample, after every other chain.
func TestStableTronControlPacing(t *testing.T) {
	stub, rpcs := newStableControlsStub(t)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := newStableControlsSampler(StablecoinConfig{}.settings(), newTestRiskStore(t), rpcs, func() time.Time { return clock })
	ctx := context.Background()
	sample := func() {
		t.Helper()
		stub.mu.Lock()
		// upgradedAddress() answers 429 twice: the first read and its one
		// retry fail, the end-of-sample re-read succeeds.
		stub.tronLimited["upgradedAddress()"] = 2
		stub.order, stub.tronLog = nil, nil
		stub.mu.Unlock()
		c.tick(ctx)
		clock = clock.Add(stableControlsTick)
	}
	sample()
	stub.mu.Lock()
	log, order := append([]stableTronCall(nil), stub.tronLog...), append([]string(nil), stub.order...)
	stub.mu.Unlock()
	var sels []string
	for i, call := range log {
		sels = append(sels, call.Selector)
		if i > 0 && call.At.Sub(log[i-1].At) < stableTronPace {
			t.Errorf("TRON reads %d and %d only %v apart, want at least %v (%+v)", i-1, i, call.At.Sub(log[i-1].At), stableTronPace, log)
		}
	}
	want := []string{"owner()", "paused()", "deprecated()", "upgradedAddress()", "upgradedAddress()", "upgradedAddress()"}
	if strings.Join(sels, ",") != strings.Join(want, ",") || log[len(log)-1].Status != http.StatusOK {
		t.Fatalf("TRON reads = %v (last status %d), want %v ending with the re-read that succeeds", sels, log[len(log)-1].Status, want)
	}
	lastOther := -1
	for i, ep := range order {
		if ep != "tron" {
			lastOther = i
		}
	}
	if order[len(order)-1] != "tron" || lastOther > len(order)-2 {
		t.Errorf("request order %v: the re-read must come after every other chain", order)
	}
	states, err := c.store.stableControlStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st := states["usdt|tron|upgraded_address"]; st.LastCode != "" || st.PendingValue == nil {
		t.Errorf("after the first sample upgraded_address = %+v, want a pending value with no read error", st)
	}
	// TRON has one endpoint, so the second sample confirms the baseline.
	sample()
	states, _ = c.store.stableControlStates(ctx)
	if st := states["usdt|tron|upgraded_address"]; st.Value == nil || !st.Baseline || st.LastCode != "" {
		t.Errorf("after two samples upgraded_address = %+v, want the baseline", st)
	}
}
