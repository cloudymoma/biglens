package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 64,285 / 130,285 energy are the two modal USDT transfer costs (recipient
// already holds USDT / brand-new recipient), unchanged across the last 90
// days of BigQuery receipts.
func TestTronLiveFrom(t *testing.T) {
	price := 0.3346
	got := tronLiveFrom(tronLiveRaw{EnergySun: 100, BandwidthSun: 1000, EnergySince: "2025-08-29T12:00:00Z"}, *testCalibration(), &price)
	if got.EnergyPriceSun != 100 || got.BandwidthPriceSun != 1000 || got.EnergyPriceSince != "2025-08-29T12:00:00Z" {
		t.Errorf("prices = %+v", got)
	}
	want := []struct {
		energy int64
		trx    float64
	}{{64285, 6.7735}, {130285, 13.3735}} // + 345 bandwidth × 1000 sun = 0.345 TRX
	if len(got.Costs) != len(want) {
		t.Fatalf("got %d scenarios, want %d", len(got.Costs), len(want))
	}
	for i, w := range want {
		c := got.Costs[i]
		if c.Energy != w.energy || c.Bandwidth != 345 || !almost(c.BurnTRX, w.trx) {
			t.Errorf("scenario %d = %+v, want %d energy -> %.4f TRX", i, c, w.energy, w.trx)
		}
		if c.BurnUSD == nil || !almost(*c.BurnUSD, w.trx*price) {
			t.Errorf("scenario %d USD = %v, want %v", i, c.BurnUSD, w.trx*price)
		}
		if c.SharePct == 0 {
			t.Errorf("scenario %d has no share of transfers", i)
		}
	}
	if got.Note == "" {
		t.Error("the card must explain that staked or rented energy avoids the burn")
	}
}

func TestTronLiveFromWithoutSpot(t *testing.T) {
	for _, c := range tronLiveFrom(tronLiveRaw{EnergySun: 100, BandwidthSun: 1000}, *testCalibration(), nil).Costs {
		if c.BurnUSD != nil {
			t.Errorf("%s: USD = %v, want nil when the spot price is unavailable", c.Label, *c.BurnUSD)
		}
	}
}

func TestFetchTronLiveRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getbandwidthprices") {
			w.Write([]byte(`{"prices":"0:10,1606240800000:40,1627279200000:1000"}`))
			return
		}
		w.Write([]byte(`{"prices":"` + tronPricesFixture + `"}`))
	}))
	defer srv.Close()
	oldE, oldB := tronEnergyPricesURL, tronBandwidthPricesURL
	tronEnergyPricesURL, tronBandwidthPricesURL = srv.URL+"/wallet/getenergyprices", srv.URL+"/wallet/getbandwidthprices"
	defer func() { tronEnergyPricesURL, tronBandwidthPricesURL = oldE, oldB }()

	raw, err := fetchTronLiveRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw.EnergySun != 100 || raw.BandwidthSun != 1000 || raw.EnergySince != "2025-08-29T12:00:00Z" {
		t.Errorf("raw = %+v, want the latest energy (100) and bandwidth (1000) prices", raw)
	}
}
