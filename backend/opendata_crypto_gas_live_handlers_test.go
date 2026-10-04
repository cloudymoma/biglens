package main

import (
	"errors"
	"testing"
	"time"
)

func gasLiveTestRaw() (*btcLiveRaw, *tronLiveRaw) {
	return &btcLiveRaw{
			Fees:    btcPreciseFees{Fastest: 1.5, HalfHour: 0.9, Economy: 0.2, Minimum: 0.1},
			Mempool: btcMempoolStats{Count: 10, Vsize: 2_000_000, TotalFee: 50_000, FeeHistogram: [][2]float64{{1.2, 2_000_000}}},
		},
		&tronLiveRaw{EnergySun: 100, BandwidthSun: 1000, EnergySince: "2025-08-29T12:00:00Z"}
}

// mempool.space failing must not take the TRON calculator down with it.
func TestBuildGasLivePartialFailure(t *testing.T) {
	_, tron := gasLiveTestRaw()
	trx := 0.3346
	d := buildGasLive(time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC), nil, errors.New("mempool.space fetch: upstream status 503"), tron, nil, nil, &trx)

	if d.AsOf != "2026-10-04T08:00:00Z" {
		t.Errorf("as_of = %q", d.AsOf)
	}
	if d.BTC != nil || d.BTCError == "" {
		t.Errorf("btc = %+v, btc_error = %q; want nil and the upstream error", d.BTC, d.BTCError)
	}
	if d.Tron == nil || d.TronError != "" || d.Tron.Costs[0].BurnUSD == nil {
		t.Errorf("tron = %+v, tron_error = %q; want a priced calculator", d.Tron, d.TronError)
	}
}

func TestBuildGasLiveTronFailure(t *testing.T) {
	btc, _ := gasLiveTestRaw()
	d := buildGasLive(time.Now(), btc, nil, nil, errors.New("tron prices fetch: upstream status 429"), nil, nil)
	if d.BTC == nil || d.BTCError != "" || d.Tron != nil || d.TronError == "" {
		t.Errorf("btc=%v btc_error=%q tron=%v tron_error=%q; want only TRON to fail", d.BTC, d.BTCError, d.Tron, d.TronError)
	}
}

// Coinbase down: every native value still renders, USD stays null.
func TestBuildGasLiveWithoutSpot(t *testing.T) {
	btc, tron := gasLiveTestRaw()
	d := buildGasLive(time.Now(), btc, nil, tron, nil, nil, nil)
	if d.BTC == nil || d.Tron == nil || d.BTCError != "" || d.TronError != "" {
		t.Fatalf("missing spot must not be an error: %+v", d)
	}
	for _, tier := range d.BTC.Tiers {
		if tier.USD != nil {
			t.Errorf("btc %s USD = %v, want nil", tier.Label, *tier.USD)
		}
	}
	for _, c := range d.Tron.Costs {
		if c.BurnUSD != nil || c.BurnTRX == 0 {
			t.Errorf("tron %s = %+v, want TRX cost without USD", c.Label, c)
		}
	}
}
