package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A real USDT transfer from TronGrid (211-byte raw_data, one signature);
// TronGrid's own receipt reports net_usage = 345 for transfers of this shape.
const (
	usdtFixtureRaw = "0a02a2692208e9869443c76dcd3540d8a291b290345aae01081f12a9010a31747970652e676f6f676c65617069732e636f6d2f70726f746f636f6c2e54726967676572536d617274436f6e747261637412740a1541f97fdc95d41f99ff72609608ca8ff410c0bf5de7121541a614f803b6fd780986a42c78ec9c7f77e6ded13c2244a9059cbb000000000000000000000000e340c10c9504a9865dae79218c473b4efc9fac14000000000000000000000000000000000000000000000000000000003cb8bd2070f1e78db29034900180b48913"
	usdtFixtureSig = "b9d1dc46e253bef2fb45a0ced9401f78ef171b938ec922cc248b08da0a67186d0d0bfc07865476f094cbca6353a4ee3ef3c8d000b5f532054127f49a097218891b"
)

// Cost guardrail: each calibration query is bounded in time and filtered to
// the exact contract and call it measures.
func TestCalibrationSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"usdt modes", tronUSDTModesSQL(), []string{"goog_blockchain_tron_mainnet_us.receipts",
			"INTERVAL 24 HOUR", tronUSDTReceiptAddress, "status = 1", "gas_used > 0", "LIMIT 2"}},
		{"usdt samples", tronUSDTSamplesSQL(), []string{"goog_blockchain_tron_mainnet_us.receipts",
			"INTERVAL 60 MINUTE", tronUSDTReceiptAddress, "status = 1", "UNNEST(@energies)",
			"ARRAY_AGG(transaction_hash ORDER BY block_timestamp DESC LIMIT 5)"}},
		{"usdc gas", usdcTransferGasSQL(), []string{"crypto_ethereum.transactions",
			"INTERVAL 6 HOUR", ethUSDCAddress, "STARTS_WITH(input, '0xa9059cbb')", "receipt_status = 1", "OFFSET(50)"}},
	}
	for _, tt := range tests {
		for _, w := range tt.want {
			if !strings.Contains(tt.sql, w) {
				t.Errorf("%s: missing %q in SQL:\n%s", tt.name, w, tt.sql)
			}
		}
	}
}

func TestUSDTProfilesFromModes(t *testing.T) {
	holder, newAddr, err := usdtProfilesFromModes([]usdtEnergyMode{{Energy: 64285, N: 1444030}, {Energy: 130285, N: 393784}})
	if err != nil {
		t.Fatal(err)
	}
	if holder.Energy != 64285 || newAddr.Energy != 130285 {
		t.Errorf("holder=%d new=%d", holder.Energy, newAddr.Energy)
	}
	if !almost(holder.SharePct+newAddr.SharePct, 100) || !almost(holder.SharePct, 1444030.0/(1444030+393784)*100) {
		t.Errorf("shares = %v / %v", holder.SharePct, newAddr.SharePct)
	}
}

// Writing a fresh balance slot costs more energy than updating one, so the
// larger mode is the new-address case even on a day it is more frequent.
func TestUSDTProfilesFromModesAssignsByEnergy(t *testing.T) {
	holder, newAddr, err := usdtProfilesFromModes([]usdtEnergyMode{{Energy: 130285, N: 900}, {Energy: 64285, N: 100}})
	if err != nil || holder.Energy != 64285 || newAddr.Energy != 130285 {
		t.Errorf("holder=%d new=%d err=%v; want assignment by energy, not by count", holder.Energy, newAddr.Energy, err)
	}
}

func TestUSDTProfilesFromModesTooFew(t *testing.T) {
	if _, _, err := usdtProfilesFromModes([]usdtEnergyMode{{Energy: 64285, N: 10}}); err == nil {
		t.Error("one mode cannot separate the two transfer cases; expected an error")
	}
}

// java-tron bandwidth = serialized tx without ret + 64 bytes per contract.
func TestTronTxBandwidth(t *testing.T) {
	got, err := tronTxBandwidth(usdtFixtureRaw, []string{usdtFixtureSig})
	if err != nil || got != 345 {
		t.Errorf("bandwidth = %d, %v; want 345 (TronGrid net_usage for this transfer)", got, err)
	}
	if _, err := tronTxBandwidth("zz", nil); err == nil {
		t.Error("malformed raw_data_hex must be an error")
	}
}

func TestFetchTronTxBandwidth(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Write([]byte(`{"raw_data_hex":"` + usdtFixtureRaw + `","signature":["` + usdtFixtureSig + `"]}`))
	}))
	defer srv.Close()
	old := tronTxByIDURL
	tronTxByIDURL = srv.URL
	defer func() { tronTxByIDURL = old }()

	got, err := fetchTronTxBandwidth(context.Background(), "0xABCDEF")
	if err != nil || got != 345 {
		t.Fatalf("got %d, %v; want 345", got, err)
	}
	if !strings.Contains(gotBody, `"abcdef"`) {
		t.Errorf("request body %q: TronGrid wants the hash without 0x, lower-case", gotBody)
	}
}

// One random transfer is not a measurement: a wallet that encodes a shorter
// raw_data (seen live: 338 bytes vs. the usual 345) must not set the day's
// bandwidth. The most common value wins; a tie goes to the larger (costlier).
func TestModeBandwidth(t *testing.T) {
	tests := []struct {
		in   []int64
		want int64
	}{
		{[]int64{345, 338, 345, 345, 345}, 345},
		{[]int64{338}, 338},
		{[]int64{338, 345}, 345},
		{[]int64{338, 338, 345}, 338},
	}
	for _, tt := range tests {
		if got := modeBandwidth(tt.in); got != tt.want {
			t.Errorf("modeBandwidth(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// A sample TronGrid fails to return is skipped; only when every sample fails
// does the calibration fail (and then nothing falls back to a constant).
func TestSampleBandwidthSkipsFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "bad") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"raw_data_hex":"` + usdtFixtureRaw + `","signature":["` + usdtFixtureSig + `"]}`))
	}))
	defer srv.Close()
	old, oldPause := tronTxByIDURL, usdtSamplePause
	tronTxByIDURL, usdtSamplePause = srv.URL, 0
	defer func() { tronTxByIDURL, usdtSamplePause = old, oldPause }()

	got, err := sampleBandwidth(context.Background(), []string{"0xaa", "0xbad1", "0xbb"})
	if err != nil || got != 345 {
		t.Errorf("got %d, %v; want 345 from the two good samples", got, err)
	}
	if _, err := sampleBandwidth(context.Background(), []string{"0xbad1", "0xbad2"}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want a failure carrying the upstream errors", err)
	}
	if _, err := sampleBandwidth(context.Background(), nil); err == nil {
		t.Error("no samples at all must be an error")
	}
}

// TronGrid's keyless API rate-limits bursts (429 seen live), so measuring
// stops once enough samples agree instead of always fetching every hash.
func TestSampleBandwidthStopsAtTarget(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"raw_data_hex":"` + usdtFixtureRaw + `","signature":["` + usdtFixtureSig + `"]}`))
	}))
	defer srv.Close()
	old, oldPause := tronTxByIDURL, usdtSamplePause
	tronTxByIDURL, usdtSamplePause = srv.URL, 0
	defer func() { tronTxByIDURL, usdtSamplePause = old, oldPause }()

	if got, err := sampleBandwidth(context.Background(), []string{"a", "b", "c", "d", "e"}); err != nil || got != 345 {
		t.Fatalf("got %d, %v", got, err)
	}
	if hits.Load() != usdtSampleTarget {
		t.Errorf("TronGrid hit %d times, want %d (stop once enough samples are measured)", hits.Load(), usdtSampleTarget)
	}
}
