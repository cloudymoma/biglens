package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Sub-1 sat/vB fees are normal now; rounding them (as /fees/recommended
// does) shows "1 1 1 1 1" and hides the real economy rate.
func TestBtcFeeTiersKeepSubSatPrecision(t *testing.T) {
	price := 85000.0
	tiers := btcFeeTiers(btcPreciseFees{Fastest: 1.503, HalfHour: 0.902, Hour: 0.468, Economy: 0.2, Minimum: 0.1}, &price)
	want := []struct {
		label string
		rate  float64
	}{{"Next block", 1.503}, {"~30 min", 0.902}, {"Economy", 0.2}}
	if len(tiers) != len(want) {
		t.Fatalf("got %d tiers, want %d", len(tiers), len(want))
	}
	for i, w := range want {
		if tiers[i].Label != w.label || tiers[i].SatVB != w.rate {
			t.Errorf("tier %d = %+v, want %s at %v sat/vB", i, tiers[i], w.label, w.rate)
		}
	}
	// 1.503 sat/vB × 140 vB × $85,000 / 1e8 = $0.178857
	if tiers[0].USD == nil || !almost(*tiers[0].USD, 0.178857) {
		t.Errorf("next-block USD = %v, want 0.178857", tiers[0].USD)
	}
}

func TestBtcFeeTiersWithoutSpot(t *testing.T) {
	for _, tier := range btcFeeTiers(btcPreciseFees{Fastest: 2}, nil) {
		if tier.USD != nil {
			t.Errorf("%s: USD = %v, want nil when the spot price is unavailable", tier.Label, *tier.USD)
		}
	}
}

func TestBandBtcMempool(t *testing.T) {
	hist := [][2]float64{{25, 100000}, {12, 200000}, {3, 300000}, {1.0, 400000}, {0.5, 500000}, {0.0, 1000}}
	got := bandBtcMempool(hist)
	want := []struct {
		label string
		mb    float64
	}{{"<1", 0.501}, {"1–2", 0.4}, {"2–5", 0.3}, {"5–10", 0}, {"10–20", 0.2}, {"20+", 0.1}}
	if len(got) != len(want) {
		t.Fatalf("got %d bands, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Label != w.label || !almost(got[i].VsizeMB, w.mb) {
			t.Errorf("band %d = %+v, want %s %.3f vMB", i, got[i], w.label, w.mb)
		}
	}
}

func TestBandBtcMempoolEmpty(t *testing.T) {
	if got := bandBtcMempool(nil); len(got) != 0 {
		t.Errorf("empty histogram gave %d bands, want 0", len(got))
	}
}

func TestBtcLiveFrom(t *testing.T) {
	raw := btcLiveRaw{
		Fees:    btcPreciseFees{Fastest: 1.5, HalfHour: 0.9, Economy: 0.2, Minimum: 0.1},
		Mempool: btcMempoolStats{Count: 76507, Vsize: 41709888, TotalFee: 6988847, FeeHistogram: [][2]float64{{0.5, 41709888}}},
	}
	got := btcLiveFrom(raw, nil)
	if got.TxCount != 76507 || !almost(got.VsizeMB, 41.709888) || !almost(got.BlocksToClear, 41.709888) {
		t.Errorf("mempool = %d tx, %v vMB, %v blocks", got.TxCount, got.VsizeMB, got.BlocksToClear)
	}
	if !almost(got.TotalFeeBTC, 0.06988847) || got.MinimumSatVB != 0.1 || got.StandardTxVB != 140 {
		t.Errorf("total fee %v BTC, minimum %v, standard tx %d vB", got.TotalFeeBTC, got.MinimumSatVB, got.StandardTxVB)
	}
}

func mempoolTestServer(t *testing.T, statsStatus int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fees/precise"):
			w.Write([]byte(`{"fastestFee":1.503,"halfHourFee":0.902,"hourFee":0.468,"economyFee":0.2,"minimumFee":0.1}`))
		case statsStatus != http.StatusOK:
			w.WriteHeader(statsStatus)
		default:
			w.Write([]byte(`{"count":3,"vsize":900,"total_fee":1200,"fee_histogram":[[2.5,600],[0.1,300]]}`))
		}
	}))
	t.Cleanup(srv.Close)
	oldFees, oldStats := mempoolFeesURL, mempoolStatsURL
	mempoolFeesURL, mempoolStatsURL = srv.URL+"/api/v1/fees/precise", srv.URL+"/api/mempool"
	t.Cleanup(func() { mempoolFeesURL, mempoolStatsURL = oldFees, oldStats })
}

func TestFetchBtcLiveRaw(t *testing.T) {
	mempoolTestServer(t, http.StatusOK)
	raw, err := fetchBtcLiveRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw.Fees.Economy != 0.2 || raw.Mempool.Count != 3 || len(raw.Mempool.FeeHistogram) != 2 {
		t.Errorf("raw = %+v", raw)
	}
}

func TestFetchBtcLiveUpstreamError(t *testing.T) {
	mempoolTestServer(t, http.StatusServiceUnavailable)
	if _, err := fetchBtcLiveRaw(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want an error naming status 503", err)
	}
}
