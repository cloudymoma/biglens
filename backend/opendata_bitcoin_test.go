package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBtcSubsidy(t *testing.T) {
	tests := []struct {
		height int64
		want   int64
	}{
		{0, 5_000_000_000},
		{209_999, 5_000_000_000},
		{210_000, 2_500_000_000},
		{839_999, 625_000_000},
		{840_000, 312_500_000},
		{1_050_000, 156_250_000},
		{13_440_000, 0}, // 64th halving
	}
	for _, tt := range tests {
		if got := btcSubsidySats(tt.height); got != tt.want {
			t.Errorf("btcSubsidySats(%d) = %d, want %d", tt.height, got, tt.want)
		}
	}
}

func TestBtcScheduledIssuance(t *testing.T) {
	if got := btcScheduledIssuance(839_999, 840_000); got != 937_500_000 {
		t.Errorf("btcScheduledIssuance(839999, 840000) = %d, want 937500000", got)
	}
	// 144 blocks in epoch 4 (971,548..971,691): 144 * 312,500,000 = 45,000,000,000 sats,
	// matching mempool reward-stats (totalReward 45,303,128,842 - totalFee 303,128,842).
	if got := btcScheduledIssuance(971_548, 971_691); got != 45_000_000_000 {
		t.Errorf("btcScheduledIssuance(971548, 971691) = %d, want 45000000000", got)
	}
}

func TestBtcScheduledSupply(t *testing.T) {
	if got := btcScheduledSupply(971_691); got != 2_009_903_750_000_000 {
		t.Errorf("btcScheduledSupply(971691) = %d, want 2009903750000000", got)
	}
}

func TestBtcDifficultyFromBits(t *testing.T) {
	tests := []struct {
		bits string
		want float64
	}{
		{"17021ef0", 132_716_002_350_731.3},
		{"17034219", 86_388_558_925_171.0},
	}
	for _, tt := range tests {
		got, err := btcDifficultyFromBits(tt.bits)
		if err != nil {
			t.Fatalf("btcDifficultyFromBits(%q) unexpected error: %v", tt.bits, err)
		}
		rel := math.Abs(got-tt.want) / tt.want
		if rel >= 1e-12 {
			t.Errorf("btcDifficultyFromBits(%q) = %.4f, want %.4f (rel=%g)", tt.bits, got, tt.want, rel)
		}
	}
	for _, bad := range []string{"", "17021ef", "17021ef00", "zzzzzzzz", "17000000"} {
		if _, err := btcDifficultyFromBits(bad); err == nil {
			t.Errorf("btcDifficultyFromBits(%q) succeeded, want error", bad)
		}
	}
}

func TestBtcHalvingAt(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	h := btcHalvingAt(971_691, now, 567.0)
	if h.NextHeight == nil || *h.NextHeight != 1_050_000 {
		t.Errorf("NextHeight = %v, want 1050000", h.NextHeight)
	}
	if h.BlocksRemaining == nil || *h.BlocksRemaining != 78_309 {
		t.Errorf("BlocksRemaining = %v, want 78309", h.BlocksRemaining)
	}
	if h.SubsidyNowBTC == nil || *h.SubsidyNowBTC != 3.125 || h.SubsidyNextBTC == nil || *h.SubsidyNextBTC != 1.5625 {
		t.Errorf("subsidies = %v -> %v, want 3.125 -> 1.5625", h.SubsidyNowBTC, h.SubsidyNextBTC)
	}
	if h.ScheduledSupply == nil || math.Abs(*h.ScheduledSupply-20_099_037.5) > 1e-6 {
		t.Errorf("ScheduledSupply = %v, want 20099037.5", h.ScheduledSupply)
	}
	// 78,309 * 567 s = 44,401,203 s ≈ 513.9 days -> ~2028-03-13
	if h.EstAt == nil {
		t.Fatal("EstAt is nil")
	}
	est, err := time.Parse(time.RFC3339, *h.EstAt)
	if err != nil {
		t.Fatalf("EstAt parse error: %v", err)
	}
	days := est.Sub(now).Hours() / 24
	if math.Abs(days-513.9028) > 0.01 {
		t.Errorf("EstAt days = %.4f, want ~513.9028", days)
	}
}

func TestBtcGetJSONCodes(t *testing.T) {
	var mode string
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		switch mode {
		case "ok":
			w.Write([]byte(`{"ok":true}`))
		case "429":
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
		case "451":
			w.WriteHeader(http.StatusUnavailableForLegalReasons)
		case "403":
			w.WriteHeader(http.StatusForbidden)
		case "502":
			w.WriteHeader(http.StatusBadGateway)
		case "bad_json":
			w.Write([]byte(`{not-json`))
		case "too_big":
			w.Write([]byte(strings.Repeat("x", 1024)))
		case "slow":
			time.Sleep(150 * time.Millisecond)
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	var dst struct {
		OK bool `json:"ok"`
	}
	mode = "ok"
	if code := btcGetJSON(context.Background(), srv.URL, 1024, &dst); code != "" || !dst.OK {
		t.Fatalf("ok case: code=%q ok=%v", code, dst.OK)
	}
	if gotUA != "biglens/1.0" {
		t.Errorf("User-Agent = %q, want biglens/1.0", gotUA)
	}

	cases := []struct {
		mode string
		max  int64
		want string
	}{
		{"429", 1024, "rate_limited"},
		{"451", 1024, "geo_blocked"},
		{"403", 1024, "geo_blocked"},
		{"502", 1024, "5xx"},
		{"bad_json", 1024, "bad_response"},
		{"too_big", 100, "bad_response"},
	}
	for _, tc := range cases {
		mode = tc.mode
		if got := btcGetJSON(context.Background(), srv.URL, tc.max, &dst); got != tc.want {
			t.Errorf("mode %s: code = %q, want %q", tc.mode, got, tc.want)
		}
	}

	mode = "slow"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := btcGetJSON(ctx, srv.URL, 1024, &dst); got != "timeout" {
		t.Errorf("slow case: code = %q, want timeout", got)
	}
}

func TestBtcPostJSON(t *testing.T) {
	var gotMethod, gotCT string
	var gotReq map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b, _ := readCapped(r.Body, 1024)
		_ = gotReq
		if !strings.Contains(string(b), `"metaAndAssetCtxs"`) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	var resp struct {
		Status string `json:"status"`
	}
	code := btcPostJSON(context.Background(), srv.URL, map[string]string{"type": "metaAndAssetCtxs"}, 1024, &resp)
	if code != "" || resp.Status != "ok" {
		t.Fatalf("btcPostJSON = %q, %+v", code, resp)
	}
	if gotMethod != http.MethodPost || !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("method=%s content-type=%s", gotMethod, gotCT)
	}
}

func TestBtcLastGood(t *testing.T) {
	lg := newBtcLastGood()
	if _, _, ok := lg.Get("k1"); ok {
		t.Fatal("expected miss on empty store")
	}
	lg.Set("k1", 42, "2026-10-16T00:00:00Z")
	v, asOf, ok := lg.Get("k1")
	if !ok || v.(int) != 42 || asOf != "2026-10-16T00:00:00Z" {
		t.Errorf("Get(k1) = (%v, %q, %v)", v, asOf, ok)
	}
}

func TestBtcCheckRedirectEnforcesHTTPSAndPort443(t *testing.T) {
	req1, _ := http.NewRequest(http.MethodGet, "https://bitnodes.io/api/v1/snapshots/latest/", nil)
	cases := []struct {
		target string
		wantOK bool
	}{
		{"https://btcnodes.io/api/v1/snapshots/latest/", true},
		{"https://btcnodes.io:443/api/v1/snapshots/latest/", true},
		{"http://btcnodes.io/api/v1/snapshots/latest/", false},
		{"https://btcnodes.io:8443/api/v1/snapshots/latest/", false},
		{"https://evil.example.com/api/v1/snapshots/latest/", false},
	}
	for _, tc := range cases {
		req2, err := http.NewRequest(http.MethodGet, tc.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		gotErr := btcHTTPClient.CheckRedirect(req2, []*http.Request{req1})
		if tc.wantOK && gotErr != nil {
			t.Errorf("CheckRedirect(%q) = %v, want nil", tc.target, gotErr)
		}
		if !tc.wantOK && !errors.Is(gotErr, errBtcRedirectBlocked) {
			t.Errorf("CheckRedirect(%q) = %v, want errBtcRedirectBlocked", tc.target, gotErr)
		}
	}
}

func TestBtcBaselineRouteCrossOriginProtection(t *testing.T) {
	h := newTestBitcoinHandler(t)
	mux := http.NewServeMux()
	registerBitcoinRoutes(mux, h,
		func(fn http.HandlerFunc) http.Handler { return fn },
		func(next http.Handler) http.Handler { return next },
	)

	req := httptest.NewRequest(http.MethodPost, "/api/opendata/bitcoin/onchain/baseline", strings.NewReader(`{"action":"dry_run"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST to baseline status = %d, want 403", rec.Code)
	}
}

func TestBtcCachedSourceDetachesCallerContext(t *testing.T) {
	h := newTestBitcoinHandler(t)
	started := make(chan struct{})
	release := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan SourceStatus, 1)
	go func() {
		_, st := h.btcCachedSource(ctx, "test:detach:ctx", 1*time.Minute, "mempool", func(fctx context.Context) (any, string) {
			close(started)
			<-release
			if fctx.Err() != nil {
				return nil, "canceled"
			}
			return "ok-value", ""
		})
		done <- st
	}()

	<-started
	cancel() // Caller disconnects while fetch is in flight.
	close(release)

	st := <-done
	if st.Status != "ok" {
		t.Fatalf("btcCachedSource status after caller cancel = %+v, want ok", st)
	}
}
