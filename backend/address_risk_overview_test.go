package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func TestTrendGranularity(t *testing.T) {
	d := func(s string) civil.Date { x, _ := civil.ParseDate(s); return x }
	if g := trendGranularity(d("2026-08-27"), d("2026-09-25")); g != "day" {
		t.Errorf("30-day cold start = %s, want day", g)
	}
	if g := trendGranularity(d("2026-06-28"), d("2026-09-25")); g != "month" {
		t.Errorf("90 days = %s, want month", g)
	}
	if g := trendGranularity(d("2017-11-28"), d("2026-09-25")); g != "month" {
		t.Errorf("full history = %s, want month", g)
	}
}

func TestFillTrend(t *testing.T) {
	d := func(s string) civil.Date { x, _ := civil.ParseDate(s); return x }
	counts := []trendCount{
		{"2026-09-02", "USDT", "freeze", 2}, {"2026-09-02", "USDC", "freeze", 1},
		{"2026-09-03", "USDC", "unfreeze", 1}, {"2026-08-01", "USDT", "freeze", 9}, // outside coverage: dropped
	}
	got := fillTrend(d("2026-09-01"), d("2026-09-03"), "day", counts)
	want := []riskTrendPoint{{"2026-09-01", 0, 0, 0}, {"2026-09-02", 2, 1, 0}, {"2026-09-03", 0, 0, 1}}
	if len(got) != len(want) {
		t.Fatalf("points = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	months := fillTrend(d("2017-11-28"), d("2018-02-03"), "month", []trendCount{{"2018-01", "USDT", "freeze", 4}})
	var buckets []string
	for _, p := range months {
		buckets = append(buckets, p.Bucket)
	}
	if strings.Join(buckets, ",") != "2017-11,2017-12,2018-01,2018-02" || months[2].USDTFreeze != 4 {
		t.Errorf("months = %+v", months)
	}
}

func TestBuildOverview(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	s.replaceList(ctx, "ofac", []listEntry{{Address: "0x1111111111111111111111111111111111111111"}, {Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	empty, err := s.buildOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Empty || !empty.TronEmpty || empty.KPIs.OFACCount != 2 || empty.KPIs.TronUSDTFrozenCount != 0 || empty.RecentEvents == nil || empty.Trend.Points == nil {
		t.Errorf("empty overview = %+v (slices must encode as [] not null)", empty)
	}
	a, b := "0x3333333333333333333333333333333333333333", "0x4444444444444444444444444444444444444444"
	destroy := stablecoinEvent{TxHash: "0xd", LogIndex: 1, Token: "USDT", Action: "destroy", Address: b, Amount: "1642752780747", BlockNumber: 30, BlockTime: "2026-09-10T00:00:00Z"}
	events := []stablecoinEvent{
		{TxHash: "0xa", LogIndex: 1, Token: "USDT", Action: "freeze", Address: a, BlockNumber: 10, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xb", LogIndex: 1, Token: "USDC", Action: "freeze", Address: a, BlockNumber: 11, BlockTime: "2026-09-01T01:00:00Z"},
		{TxHash: "0xc", LogIndex: 1, Token: "USDC", Action: "unfreeze", Address: a, BlockNumber: 20, BlockTime: "2026-09-05T00:00:00Z"},
		destroy, // b frozen before coverage, destroyed inside it: counts as frozen
	}
	s.insertStablecoinEvents(ctx, events, "2026-08-27", "2026-09-25", riskNow)
	if err := s.insertTronStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xt1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7", BlockNumber: 100, BlockTime: "2026-09-10T00:00:00Z"},
		{TxHash: "0xt2", LogIndex: 1, Token: "USDT", Action: "freeze", Address: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", BlockNumber: 101, BlockTime: "2026-09-11T00:00:00Z"},
		{TxHash: "0xt3", LogIndex: 2, Token: "USDT", Action: "unfreeze", Address: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", BlockNumber: 102, BlockTime: "2026-09-12T00:00:00Z"},
	}, "2026-08-27", "2026-09-25", riskNow); err != nil {
		t.Fatal(err)
	}
	ov, err := s.buildOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Empty || ov.TronEmpty || !ov.Coverage.Partial || ov.Coverage.CoverageFrom != "2026-08-27" || !ov.TronCoverage.Partial {
		t.Errorf("coverage = %+v, tron = %+v", ov.Coverage, ov.TronCoverage)
	}
	if ov.KPIs.USDTFrozenCount != 2 || ov.KPIs.USDCFrozenCount != 0 || ov.KPIs.TronUSDTFrozenCount != 1 || ov.KPIs.USDTDestroyedTotal != "1642752.78" {
		t.Errorf("kpis = %+v", ov.KPIs)
	}
	if ov.Trend.Granularity != "day" || len(ov.Trend.Points) != 30 {
		t.Errorf("trend = %s with %d points, want day × 30", ov.Trend.Granularity, len(ov.Trend.Points))
	}
	if len(ov.RecentEvents) != 4 || ov.RecentEvents[0].TxHash != "0xd" || ov.RecentEvents[0].Amount != "1642752.78" || ov.RecentEvents[1].Amount != "" {
		t.Errorf("recent = %+v (newest first; amount only on destroy)", ov.RecentEvents)
	}
}

func TestAddressRiskOverviewHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	riskHandler(t, nil, nil).AddressRiskOverview(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/overview", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("store unavailable: status = %d, want 503", rec.Code)
	}

	store := newTestRiskStore(t)
	h := riskHandler(t, store, nil)
	get := func() riskOverview {
		rec := httptest.NewRecorder()
		h.AddressRiskOverview(rec, httptest.NewRequest("GET", "/api/opendata/crypto/address-risk/overview", nil))
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
		var ov riskOverview
		json.NewDecoder(rec.Body).Decode(&ov)
		return ov
	}
	if !get().Empty {
		t.Fatal("fresh store must be empty")
	}
	store.insertStablecoinEvents(context.Background(), nil, "2026-08-27", "2026-09-25", time.Now())
	if !get().Empty {
		t.Error("second read must come from the 10-minute cache")
	}
	// What the syncers' onChange does in main.go.
	h.cache.Delete(riskOverviewCacheKey)
	if get().Empty {
		t.Error("after invalidation the overview must be rebuilt")
	}
}
