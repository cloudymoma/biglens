package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		case strings.HasSuffix(r.URL.Path, "/fees/mempool-blocks"):
			w.Write([]byte(`[{"blockSize":1697856,"blockVSize":997967.25,"nTx":3028,"totalFees":1432036,"medianFee":1.05,"feeRange":[0.55,1.0,29.32]}]`))
		case strings.HasSuffix(r.URL.Path, "/v1/blocks"):
			w.Write([]byte(`[{"height":969816,"timestamp":1791102967,"tx_count":4012,"size":1568714,"weight":3995093,"extras":{"medianFee":2.28,"totalFees":2964792}}]`))
		case statsStatus != http.StatusOK:
			w.WriteHeader(statsStatus)
		default:
			w.Write([]byte(`{"count":3,"vsize":900,"total_fee":1200,"fee_histogram":[[2.5,600],[0.1,300]]}`))
		}
	}))
	t.Cleanup(srv.Close)
	oldFees, oldStats, oldProj, oldRecent := mempoolFeesURL, mempoolStatsURL, mempoolProjectedURL, mempoolRecentURL
	mempoolFeesURL, mempoolStatsURL = srv.URL+"/api/v1/fees/precise", srv.URL+"/api/mempool"
	mempoolProjectedURL, mempoolRecentURL = srv.URL+"/api/v1/fees/mempool-blocks", srv.URL+"/api/v1/blocks"
	t.Cleanup(func() {
		mempoolFeesURL, mempoolStatsURL, mempoolProjectedURL, mempoolRecentURL = oldFees, oldStats, oldProj, oldRecent
	})
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
	if len(raw.Projected) != 1 || len(raw.Recent) != 1 || raw.Recent[0].Height != 969816 {
		t.Errorf("conveyor raw = %+v / %+v", raw.Projected, raw.Recent)
	}
}

func TestFetchBtcLiveUpstreamError(t *testing.T) {
	mempoolTestServer(t, http.StatusServiceUnavailable)
	if _, err := fetchBtcLiveRaw(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want an error naming status 503", err)
	}
}

// The conveyor mirrors mempool.space: next blocks to be mined on the left,
// the latest mined blocks on the right, each with its fee level and size.
func TestBtcConveyor(t *testing.T) {
	projected := []mempoolProjectedBlock{
		{BlockVSize: 997967.25, NTx: 3028, TotalFees: 1432036, MedianFee: 1.05, FeeRange: []float64{0.55, 0.74, 1.0, 29.32}},
		{BlockVSize: 999000, NTx: 2500, TotalFees: 900000, MedianFee: 0.8, FeeRange: []float64{0.5, 0.8}},
		{BlockVSize: 999000, NTx: 2400, TotalFees: 800000, MedianFee: 0.6, FeeRange: []float64{0.4, 0.6}},
		{BlockVSize: 999000, NTx: 2300, TotalFees: 700000, MedianFee: 0.5, FeeRange: []float64{0.3, 0.5}}, // 4th: dropped
	}
	recent := make([]mempoolMinedBlock, 7)
	for i := range recent {
		recent[i].Height = int64(969816 - i)
	}
	recent[0].Timestamp, recent[0].TxCount, recent[0].Size, recent[0].Weight = 1791102967, 4012, 1568714, 3995093
	recent[0].Extras.MedianFee, recent[0].Extras.TotalFees = 2.28, 2964792

	p, r := btcConveyor(projected, recent)
	if len(p) != 3 || len(r) != 5 {
		t.Fatalf("got %d projected / %d recent, want 3 / 5", len(p), len(r))
	}
	if p[0].MinFee != 0.55 || p[0].MaxFee != 29.32 || !almost(p[0].VsizeMB, 0.99796725) || !almost(p[0].TotalFeeBTC, 0.01432036) || p[0].TxCount != 3028 {
		t.Errorf("projected[0] = %+v", p[0])
	}
	if r[0].Height != 969816 || r[4].Height != 969812 {
		t.Errorf("recent heights = %d..%d, want newest first 969816..969812", r[0].Height, r[4].Height)
	}
	wantAt := time.Unix(1791102967, 0).UTC().Format(time.RFC3339)
	if r[0].MinedAt != wantAt || !almost(r[0].FullnessPct, 99.877325) || !almost(r[0].SizeMB, 1.568714) ||
		!almost(r[0].TotalFeeBTC, 0.02964792) || r[0].MedianFee != 2.28 || r[0].TxCount != 4012 {
		t.Errorf("recent[0] = %+v", r[0])
	}
}

func TestBtcConveyorEmptyFeeRange(t *testing.T) {
	p, _ := btcConveyor([]mempoolProjectedBlock{{MedianFee: 1}}, nil)
	if len(p) != 1 || p[0].MinFee != 0 || p[0].MaxFee != 0 {
		t.Errorf("projected = %+v, want zero fee range without a panic", p)
	}
}

func TestBtcConveyorShortLists(t *testing.T) {
	p, r := btcConveyor(nil, nil)
	if p == nil || r == nil || len(p) != 0 || len(r) != 0 {
		t.Errorf("empty upstream lists must give empty, non-nil slices; got %v / %v", p, r)
	}
}
