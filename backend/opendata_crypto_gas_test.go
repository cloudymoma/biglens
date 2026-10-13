package main

import (
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
)

// Cost guardrail (gas_fee_design.md §2.1): every 72h query is bounded by the
// half-open window on its clustering timestamp, reads only its approved table,
// and emits all seven neutral columns so every chain scans into GasHourRow.
// Under the SQD layout `*_month` columns are no longer partition keys, so a
// month predicate only adds scanned bytes and is forbidden on every chain.
func TestGasHourlySQL(t *testing.T) {
	allCols := []string{"AS hour_utc", "AS tx_count", "AS band_low", "AS primary_val", "AS band_high", "AS load_val", "AS total_fee"}
	tests := []struct {
		chain   string
		want    []string
		notWant []string
	}{
		{gasChainBTC, []string{"crypto_bitcoin.transactions", "NOT is_coinbase",
			"block_timestamp >= @start_ts AND block_timestamp < @end_ts"},
			[]string{"_month"}},
		{gasChainETH, []string{"crypto_ethereum.transactions", "block_timestamp >= @start_ts AND block_timestamp < @end_ts"},
			[]string{"crypto_ethereum.blocks"}},
		{gasChainArb, []string{"goog_blockchain_arbitrum_one_us.blocks", "MIN(", "MAX(", "base_fee_per_gas"},
			[]string{"receipts"}},
		{gasChainOP, []string{"goog_blockchain_optimism_mainnet_us.receipts", "IF(effective_gas_price > 0"},
			[]string{"optimism_mainnet_us.blocks"}},
		{gasChainPoly, []string{"goog_blockchain_polygon_mainnet_us.receipts", "IF(effective_gas_price > 0"}, nil},
		{gasChainTron, []string{"goog_blockchain_tron_mainnet_us.receipts", "COUNTIF(gas_used > 0)"},
			[]string{"tron_mainnet_us.blocks", "effective_gas_price"}},
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
			for _, nw := range append([]string{"timestamp_month"}, tt.notWant...) {
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
	for _, w := range []string{"crypto_bitcoin.blocks",
		"timestamp >= @start_ts AND timestamp < @end_ts", "AS hour_utc", "AS load_val"} {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in SQL:\n%s", w, sql)
		}
	}
	// timestamp_month is no longer a partition key under SQD: it only adds bytes.
	if strings.Contains(sql, "_month") {
		t.Errorf("BTC load SQL must not filter a *_month column:\n%s", sql)
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
		"base_fee_per_gas > 0", "@since_ts", "@eth_start_block",
		"ORDER BY fee DESC, ts LIMIT 1", "ORDER BY fee, ts LIMIT 2"} {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in SQL:\n%s", w, sql)
		}
	}
	if strings.Contains(sql, "MIN_BY") || strings.Contains(sql, "MAX_BY") {
		t.Error("MIN_BY/MAX_BY pick an arbitrary row among ties; use ORDER BY fee, ts")
	}
}

func TestMergeGasRecordRow(t *testing.T) {
	seed := gasAllTimeSeeds[gasChainETH]
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Delta does not beat seed ATH or ATL: seed survives unchanged.
	unchanged := mergeGasRecordRow(seed, gasRecordRow{
		Chain: gasChainETH,
		Ath:   gasFeePoint{Fee: 100000000000, Ts: later},
		Atl:   []gasFeePoint{{Fee: 50000000, Ts: later}},
	})
	if unchanged.Ath != seed.Ath || len(unchanged.Atl) < 2 || unchanged.Atl[0] != seed.Atl[0] {
		t.Errorf("expected seed to win, got %+v", unchanged)
	}

	// Delta beats ATH and ties ATL: ATH updates, ATL becomes a floor tie.
	tied := mergeGasRecordRow(seed, gasRecordRow{
		Chain: gasChainETH,
		Ath:   gasFeePoint{Fee: seed.Ath.Fee + 1, Ts: later},
		Atl:   []gasFeePoint{{Fee: seed.Atl[0].Fee, Ts: later}},
	})
	got := gasAllTimeFromRow(tied)
	if tied.Ath.Fee != seed.Ath.Fee+1 || !got.AtlIsFloor || got.AtlTime != seed.Atl[0].Ts.UTC().Format(time.RFC3339) {
		t.Errorf("unexpected merged record: row=%+v, out=%+v", tied, got)
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

// The all-time cards must say how fresh they are. Before the first
// successful refresh they serve the pre-seeded baseline, verified through the
// seed watermark (stale, refresh pending); a failed refresh is flagged
// without moving the watermark; a success advances the watermark and clears
// the flags; the watermark never moves backwards.
func TestGasAllTimeTrackerFreshness(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 30, 0, 0, time.UTC)
	seedThrough := gasAllTimeSeedTime.UTC().Format(time.RFC3339)
	seedETH, seedArb := gasAllTimeSeeds[gasChainETH], gasAllTimeSeeds[gasChainArb]
	tr := newGasAllTimeTracker()

	// Cold start: seeds, verified through 2026-01-01, stale, refresh pending.
	m := tr.current(now)
	if len(m) != len(gasAllTimeSeeds) {
		t.Fatalf("cold start serves %d cards, want the %d seeds", len(m), len(gasAllTimeSeeds))
	}
	eth := m[gasChainETH]
	if eth.AthValue != float64(seedETH.Ath.Fee)/1e9 {
		t.Errorf("cold start ath = %v, want the seed", eth.AthValue)
	}
	if eth.RecordsThrough != seedThrough || !eth.RecordsStale || eth.RefreshFailed || eth.RefreshNote != gasAllTimeNoteRefreshing {
		t.Errorf("cold start freshness = %+v, want through %s, stale, not failed, note %q", eth, seedThrough, gasAllTimeNoteRefreshing)
	}

	// A failed refresh keeps the baseline and its watermark but says so.
	tr.markFailed()
	eth = tr.current(now)[gasChainETH]
	if !eth.RefreshFailed || eth.RecordsThrough != seedThrough || eth.RefreshNote != gasAllTimeNoteFailed {
		t.Errorf("after failure = %+v, want failed, through %s, note %q", eth, seedThrough, gasAllTimeNoteFailed)
	}

	// A successful delta merges, advances the watermark and clears the flags.
	next := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	newAth := gasFeePoint{Fee: seedETH.Ath.Fee + 1, Ts: next.Add(-time.Hour)}
	tr.applyDelta([]gasRecordRow{{Chain: gasChainETH, Ath: newAth, Atl: []gasFeePoint{{Fee: seedETH.Atl[0].Fee + 5, Ts: next}}}}, next)
	m = tr.current(now)
	eth = m[gasChainETH]
	if eth.AthValue != float64(newAth.Fee)/1e9 {
		t.Errorf("ath after delta = %v, want %v", eth.AthValue, float64(newAth.Fee)/1e9)
	}
	if eth.RefreshFailed || eth.RecordsStale || eth.RefreshNote != "" || eth.RecordsThrough != "2026-10-13T08:00:00Z" {
		t.Errorf("after success = %+v, want fresh, through 2026-10-13T08:00:00Z, no note", eth)
	}
	if arb := m[gasChainArb]; arb.AthValue != float64(seedArb.Ath.Fee)/1e9 || arb.RecordsThrough != "2026-10-13T08:00:00Z" {
		t.Errorf("a chain without delta rows keeps its seed and shares the watermark, got %+v", arb)
	}

	// A later failure flags the cards but keeps the last verified watermark.
	tr.markFailed()
	if e := tr.current(now)[gasChainETH]; !e.RefreshFailed || e.RecordsThrough != "2026-10-13T08:00:00Z" || e.AthValue != float64(newAth.Fee)/1e9 {
		t.Errorf("failure after success = %+v, want failed with the last verified records", e)
	}

	// Records age into staleness once the watermark is older than one
	// refresh period plus the re-scan overlap.
	if e := tr.current(next.Add(gasAllTimeStaleAfter - time.Minute))[gasChainETH]; e.RecordsStale {
		t.Errorf("records %v old must not be stale yet", gasAllTimeStaleAfter-time.Minute)
	}

	// The watermark never moves backwards; success still clears the failure.
	tr.applyDelta(nil, next.Add(-time.Hour))
	if e := tr.current(now)[gasChainETH]; e.RecordsThrough != "2026-10-13T08:00:00Z" || e.RefreshFailed {
		t.Errorf("after an older successful delta = %+v, want watermark kept and failure cleared", e)
	}
	if e := tr.current(next.Add(gasAllTimeStaleAfter + time.Minute))[gasChainETH]; !e.RecordsStale || e.RefreshNote != gasAllTimeNoteRefreshing {
		t.Errorf("records older than %v = %+v, want stale with note %q", gasAllTimeStaleAfter, e, gasAllTimeNoteRefreshing)
	}
}
