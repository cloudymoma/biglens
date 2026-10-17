package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoinMetricsParse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "coinmetrics_asset_metrics.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var calls int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			doc["next_page_url"] = srv.URL + "/page2"
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"asset":"btc","time":"2026-10-17T00:00:00.000000000Z","FlowInExUSD":"1000000","FlowOutExUSD":"750000","SplyExNtv":"2670000","SplyCur":"20100000"}]}`))
	}))
	defer srv.Close()

	prev := btcCoinMetricsBaseURL
	btcCoinMetricsBaseURL = srv.URL
	defer func() { btcCoinMetricsBaseURL = prev }()

	rows, code := fetchCMAssetMetrics(context.Background(), "2026-10-05")
	if code != "" {
		t.Fatalf("fetchCMAssetMetrics code = %q", code)
	}
	if calls != 2 {
		t.Fatalf("expected 2 paginated calls, got %d", calls)
	}
	if len(rows) != 11 {
		t.Fatalf("expected 11 rows across 2 pages, got %d", len(rows))
	}

	first := rows[0]
	if first.Date != "2026-10-07" {
		t.Fatalf("first date = %q, want 2026-10-07", first.Date)
	}
	if !first.Provisional {
		t.Fatalf("expected flash row to be marked Provisional")
	}
	if first.NetFlowExUSD == nil {
		t.Fatalf("expected NetFlowExUSD to be populated")
	}
	wantNet := 2579706457.154155843501504088 - 2346016111.30886896601104037
	if math.Abs(*first.NetFlowExUSD-wantNet) > 1e-3 {
		t.Fatalf("NetFlowExUSD = %v, want %v", *first.NetFlowExUSD, wantNet)
	}
	if first.ExchangeSharePct == nil {
		t.Fatalf("expected ExchangeSharePct to be populated")
	}
	wantShare := 2663366.68332263 / 20094916.75045256 * 100
	if math.Abs(*first.ExchangeSharePct-wantShare) > 1e-6 {
		t.Fatalf("ExchangeSharePct = %v, want %v", *first.ExchangeSharePct, wantShare)
	}

	last := rows[len(rows)-1]
	if last.Date != "2026-10-17" || last.Provisional {
		t.Fatalf("last row = %+v, want 2026-10-17 non-provisional", last)
	}
}

func TestCoinMetricsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	prev := btcCoinMetricsBaseURL
	btcCoinMetricsBaseURL = srv.URL
	defer func() { btcCoinMetricsBaseURL = prev }()

	rows, code := fetchCMAssetMetrics(context.Background(), "2026-10-01")
	if code != "geo_blocked" {
		t.Fatalf("code = %q, want geo_blocked", code)
	}
	if len(rows) != 0 {
		t.Fatalf("expected nil/empty rows on 403, got %d", len(rows))
	}
}

func TestCGTreasuries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "coingecko_treasuries.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	prev := btcCGTreasuriesURL
	btcCGTreasuriesURL = srv.URL
	defer func() { btcCGTreasuriesURL = prev }()

	summary, code := fetchCGTreasuries(context.Background())
	if code != "" {
		t.Fatalf("fetchCGTreasuries code = %q", code)
	}
	if summary == nil {
		t.Fatalf("expected non-nil summary")
	}
	if len(summary.Companies) != 20 {
		t.Fatalf("len(Companies) = %d, want 20 (capped from 25 in fixture)", len(summary.Companies))
	}
	if summary.Companies[0].Name != "Strategy" || summary.Companies[0].TotalHoldingsBTC != 848250.0 {
		t.Fatalf("top company = %+v", summary.Companies[0])
	}
	if math.Abs(summary.TotalHoldingsBTC-1302063.2192943657) > 1e-6 {
		t.Fatalf("TotalHoldingsBTC = %v", summary.TotalHoldingsBTC)
	}
}

func TestBitcoinHolders(t *testing.T) {
	cmRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "coinmetrics_asset_metrics.json"))
	if err != nil {
		t.Fatalf("read cm fixture: %v", err)
	}
	cgRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "coingecko_treasuries.json"))
	if err != nil {
		t.Fatalf("read cg fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cm":
			var doc map[string]any
			_ = json.Unmarshal(cmRaw, &doc)
			delete(doc, "next_page_url")
			_ = json.NewEncoder(w).Encode(doc)
		case "/cg":
			_, _ = w.Write(cgRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	prevCM := btcCoinMetricsBaseURL
	prevCG := btcCGTreasuriesURL
	btcCoinMetricsBaseURL = srv.URL + "/cm"
	btcCGTreasuriesURL = srv.URL + "/cg"
	defer func() {
		btcCoinMetricsBaseURL = prevCM
		btcCGTreasuriesURL = prevCG
	}()

	h := newTestBitcoinHandler(t)
	h.btc.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodGet, "/api/opendata/bitcoin/holders?days=30", nil)
	rec := httptest.NewRecorder()
	h.BitcoinHolders(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got BtcHoldersData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Days != 30 {
		t.Fatalf("Days = %d, want 30", got.Days)
	}
	if len(got.Flows) != 10 {
		t.Fatalf("len(Flows) = %d, want 10", len(got.Flows))
	}
	if !got.Flows[0].Provisional {
		t.Fatalf("expected flow row to carry Provisional=true")
	}
	if got.Treasuries == nil || len(got.Treasuries.Companies) != 20 {
		t.Fatalf("expected 20 treasury companies, got %+v", got.Treasuries)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2", len(got.Sources))
	}
	for _, src := range got.Sources {
		if src.Status != "ok" {
			t.Fatalf("source %s status = %q, want ok", src.ID, src.Status)
		}
	}
}
