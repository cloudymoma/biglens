package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Real TronGrid history as of 2026-10 (gas_fee_design.md §4.1), trimmed.
const tronPricesFixture = "0:100,1542607200000:20,1544724000000:10,1670133600000:420,1726747200000:210,1756468800000:100"

func TestParseTronEnergyPrices(t *testing.T) {
	pts, err := parseTronEnergyPrices(tronPricesFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 6 {
		t.Fatalf("got %d points, want 6", len(pts))
	}
	if !pts[0].At.IsZero() || pts[0].Sun != 100 {
		t.Errorf("genesis point = %+v, want zero time at 100 sun", pts[0])
	}
	if got := pts[3].At.UTC().Format("2006-01-02 15:04"); got != "2022-12-04 06:00" || pts[3].Sun != 420 {
		t.Errorf("point 3 = %s %d, want 2022-12-04 06:00 420", got, pts[3].Sun)
	}
}

func TestParseTronEnergyPricesMalformed(t *testing.T) {
	for _, in := range []string{"", "abc", "0:100,oops", "0:100,1542607200000:"} {
		if _, err := parseTronEnergyPrices(in); err == nil {
			t.Errorf("parseTronEnergyPrices(%q): expected an error", in)
		}
	}
}

// The card must reflect governance history: ATH 420 sun (2022-12-04), ATL
// 10 sun (2018-12-13), current 100 sun since 2025-08-29 — not a constant.
func TestTronAllTime(t *testing.T) {
	pts, _ := parseTronEnergyPrices(tronPricesFixture)
	at := tronAllTime(pts)
	if at.Unit != "sun" || at.AthValue != 420 || at.AthTime != "2022-12-04T06:00:00Z" {
		t.Errorf("ath = %v %s @ %q", at.AthValue, at.Unit, at.AthTime)
	}
	if at.AtlValue != 10 || at.AtlTime != "2018-12-13T18:00:00Z" || at.AtlIsFloor {
		t.Errorf("atl = %v @ %q floor=%v", at.AtlValue, at.AtlTime, at.AtlIsFloor)
	}
	if at.CurrentValue == nil || *at.CurrentValue != 100 || at.CurrentTime != "2025-08-29T12:00:00Z" {
		t.Errorf("current = %v @ %q", at.CurrentValue, at.CurrentTime)
	}
}

// Genesis default counts as a record and is reported with an empty time.
func TestTronAllTimeGenesisRecord(t *testing.T) {
	pts, _ := parseTronEnergyPrices("0:100,1542607200000:20")
	if at := tronAllTime(pts); at.AthValue != 100 || at.AthTime != "" {
		t.Errorf("ath = %v @ %q, want 100 @ genesis", at.AthValue, at.AthTime)
	}
}

func TestFetchTronEnergyPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"prices":"` + tronPricesFixture + `"}`))
	}))
	defer srv.Close()
	old := tronEnergyPricesURL
	tronEnergyPricesURL = srv.URL
	defer func() { tronEnergyPricesURL = old }()

	pts, err := fetchTronEnergyPrices(context.Background())
	if err != nil || len(pts) != 6 {
		t.Fatalf("got %d points, err %v; want 6, nil", len(pts), err)
	}
}

func TestFetchTronEnergyPricesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	old := tronEnergyPricesURL
	tronEnergyPricesURL = srv.URL
	defer func() { tronEnergyPricesURL = old }()

	_, err := fetchTronEnergyPrices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want an error naming status 429", err)
	}
}
