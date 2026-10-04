package main

import (
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
)

// Cost guardrail (gas_fee_design.md §2.1): every 72h query is bounded by the
// half-open window on its partition timestamp, reads only its approved table,
// and emits all seven neutral columns so every chain scans into GasHourRow.
func TestGasHourlySQL(t *testing.T) {
	allCols := []string{"AS hour_utc", "AS tx_count", "AS band_low", "AS primary_val", "AS band_high", "AS load_val", "AS total_fee"}
	tests := []struct {
		chain   string
		want    []string
		notWant []string
	}{
		{gasChainBTC, []string{"crypto_bitcoin.transactions", "NOT is_coinbase",
			"block_timestamp_month >= DATE_TRUNC(DATE(@start_ts), MONTH)",
			"block_timestamp >= @start_ts AND block_timestamp < @end_ts"}, nil},
		{gasChainETH, []string{"crypto_ethereum.transactions", "block_timestamp >= @start_ts AND block_timestamp < @end_ts"},
			[]string{"crypto_ethereum.blocks"}},
		{gasChainArb, []string{"goog_blockchain_arbitrum_one_us.blocks", "MIN(", "MAX(", "base_fee_per_gas"},
			[]string{"receipts"}},
		{gasChainOP, []string{"goog_blockchain_optimism_mainnet_us.receipts", "IF(effective_gas_price > 0"},
			[]string{"optimism_mainnet_us.blocks"}},
		{gasChainPoly, []string{"goog_blockchain_polygon_mainnet_us.receipts", "IF(effective_gas_price > 0"}, nil},
		{gasChainTron, []string{"goog_blockchain_tron_mainnet_us.receipts", "COUNTIF(gas_used > 0)"},
			[]string{"tron_mainnet_us.blocks"}},
		{gasChainSol, []string{"crypto_solana_mainnet_us.Blocks", "leader_reward"},
			[]string{"_month"}},
	}
	for _, tt := range tests {
		t.Run(tt.chain, func(t *testing.T) {
			sql, err := gasHourlySQL(tt.chain)
			if err != nil {
				t.Fatalf("gasHourlySQL(%q): %v", tt.chain, err)
			}
			want := append([]string{"@start_ts", "< @end_ts"}, allCols...)
			for _, w := range append(want, tt.want...) {
				if !strings.Contains(sql, w) {
					t.Errorf("missing %q in SQL:\n%s", w, sql)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(sql, nw) {
					t.Errorf("SQL must not contain %q:\n%s", nw, sql)
				}
			}
		})
	}
}

func TestGasHourlySQLUnknownChain(t *testing.T) {
	if _, err := gasHourlySQL("doge"); err == nil {
		t.Fatal("expected an error for an unknown chain")
	}
}

func TestBtcGasLoadSQL(t *testing.T) {
	sql := btcGasLoadSQL()
	for _, w := range []string{"crypto_bitcoin.blocks", "timestamp_month >= DATE_TRUNC(DATE(@start_ts), MONTH)",
		"timestamp >= @start_ts AND timestamp < @end_ts", "AS hour_utc", "AS load_val"} {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in SQL:\n%s", w, sql)
		}
	}
}

// The frontend renders purely from metadata, so every selectable chain needs
// one, and only real quantile bands may be labelled as such.
func TestGasChainMetas(t *testing.T) {
	if len(gasChainOrder) != 7 {
		t.Fatalf("gasChainOrder has %d chains, want 7", len(gasChainOrder))
	}
	for _, id := range gasChainOrder {
		m, ok := gasChainMetas[id]
		if !ok {
			t.Fatalf("no metadata for chain %q", id)
		}
		if m.ID != id || m.Name == "" || m.PrimaryUnit == "" || m.LoadUnit == "" || m.FeeUnit == "" {
			t.Errorf("incomplete metadata for %q: %+v", id, m)
		}
	}
	wantBand := map[string]string{gasChainArb: "Min–Max", gasChainTron: "", gasChainSol: "", gasChainBTC: "P10–P90"}
	for id, want := range wantBand {
		if got := gasChainMetas[id].BandLabel; got != want {
			t.Errorf("%s band label = %q, want %q", id, got, want)
		}
	}
}

func TestMergeGasLoad(t *testing.T) {
	rows := []GasHourRow{
		{HourUTC: "2026-10-01 00:00", Primary: bigquery.NullFloat64{Float64: 2, Valid: true}},
		{HourUTC: "2026-10-01 01:00", Primary: bigquery.NullFloat64{Float64: 3, Valid: true}},
	}
	loads := []gasLoadRow{
		{HourUTC: "2026-10-01 01:00", Load: bigquery.NullFloat64{Float64: 99.5, Valid: true}},
		{HourUTC: "2026-10-01 02:00", Load: bigquery.NullFloat64{Float64: 80, Valid: true}}, // block hour with no fee-paying tx
	}
	got := mergeGasLoad(rows, loads)
	byHour := map[string]GasHourRow{}
	for _, r := range got {
		byHour[r.HourUTC] = r
	}
	if r := byHour["2026-10-01 00:00"]; r.Load.Valid {
		t.Errorf("00:00 has no block row, load must stay NULL, got %v", r.Load)
	}
	if r := byHour["2026-10-01 01:00"]; !r.Load.Valid || r.Load.Float64 != 99.5 || r.Primary.Float64 != 3 {
		t.Errorf("01:00 merged wrong: %+v", r)
	}
	if r, ok := byHour["2026-10-01 02:00"]; !ok || r.Load.Float64 != 80 || r.Primary.Valid {
		t.Errorf("02:00 load-only hour must be kept with NULL primary, got %+v (present=%v)", r, ok)
	}
}

// ATL times must be deterministic: Arbitrum has 178M blocks at its 0.01 gwei
// floor, so the SQL orders by (fee, ts) and fetches two rows to detect ties.
func TestGasAllTimeSQL(t *testing.T) {
	sql := gasAllTimeSQL()
	for _, w := range []string{"crypto_ethereum.blocks", "goog_blockchain_arbitrum_one_us.blocks",
		"goog_blockchain_optimism_mainnet_us.blocks", "goog_blockchain_polygon_mainnet_us.blocks",
		"base_fee_per_gas > 0", "ORDER BY fee DESC, ts LIMIT 1", "ORDER BY fee, ts LIMIT 2"} {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in SQL:\n%s", w, sql)
		}
	}
	if strings.Contains(sql, "MIN_BY") || strings.Contains(sql, "MAX_BY") {
		t.Error("MIN_BY/MAX_BY pick an arbitrary row among ties; use ORDER BY fee, ts")
	}
}

func TestGasAllTimeFromRow(t *testing.T) {
	ts := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	tests := []struct {
		name      string
		row       gasRecordRow
		wantFloor bool
	}{
		{"unique minimum (eth)", gasRecordRow{Chain: "eth",
			Ath: gasFeePoint{Fee: 8629051173688, Ts: ts("2022-05-01T01:09:03Z")},
			Atl: []gasFeePoint{{Fee: 8703865, Ts: ts("2025-12-04T03:55:59Z")}, {Fee: 8800000, Ts: ts("2025-12-04T03:56:11Z")}}}, false},
		{"protocol floor (arb)", gasRecordRow{Chain: "arb",
			Ath: gasFeePoint{Fee: 41459759000, Ts: ts("2025-10-10T21:20:47Z")},
			Atl: []gasFeePoint{{Fee: 10000000, Ts: ts("2024-03-19T07:31:10Z")}, {Fee: 10000000, Ts: ts("2024-03-19T07:31:11Z")}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gasAllTimeFromRow(tt.row)
			if got.Unit != "gwei" {
				t.Errorf("unit = %q, want gwei", got.Unit)
			}
			if want := float64(tt.row.Ath.Fee) / 1e9; got.AthValue != want {
				t.Errorf("ath = %v, want %v", got.AthValue, want)
			}
			if got.AtlTime != tt.row.Atl[0].Ts.UTC().Format(time.RFC3339) {
				t.Errorf("atl time = %q, want the first block reaching the minimum", got.AtlTime)
			}
			if got.AtlIsFloor != tt.wantFloor {
				t.Errorf("atl_is_floor = %v, want %v", got.AtlIsFloor, tt.wantFloor)
			}
		})
	}
}
