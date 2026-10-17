package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

func TestBtcCoinAgeSQL(t *testing.T) {
	sqlText := btcCoinAgeSQL()
	for _, want := range []string{
		"CAST(value AS INT64) AS sats",
		"`hash` AS h",
		"IN UNNEST(@bip30_txids)",
		"IN UNNEST(@bip30_overwritten_times)",
		"PARTITION BY spend_day ORDER BY sats_sum DESC",
		"SELECT 'totals'",
		"SELECT 'matrix'",
		"SELECT 'whale'",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("btcCoinAgeSQL missing %q", want)
		}
	}
	if len(btcBIP30Txids) != 2 || len(btcBIP30OverwrittenTimes) != 2 {
		t.Fatalf("expected 2 BIP30 txids and overwritten timestamps, got %d / %d", len(btcBIP30Txids), len(btcBIP30OverwrittenTimes))
	}
}

func TestBtcCoinAgeGuardTotalsExact(t *testing.T) {
	start := civil.Date{Year: 2026, Month: 10, Day: 15}
	end := civil.Date{Year: 2026, Month: 10, Day: 16}

	// Totals say 100,000 inputs and 500,000,000,000 sats; matrix has 1 extra input (double counting).
	rows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000_000, N: 100_001},
	}
	_, err := buildBtcCoinAgePayload(start, end, rows, 100_000, 10*1e8, 20)
	if err == nil || !strings.Contains(err.Error(), "double counting") {
		t.Fatalf("expected double counting error on count mismatch, got %v", err)
	}

	// Sats mismatch by 1 satoshi -> exact integer check fails.
	rowsSats := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000_001, N: 100_000},
	}
	_, err = buildBtcCoinAgePayload(start, end, rowsSats, 100_000, 10*1e8, 20)
	if err == nil || !strings.Contains(err.Error(), "double counting") {
		t.Fatalf("expected double counting error on 1-sat mismatch, got %v", err)
	}
}

func TestBtcCoinAgeGuardUnmatched(t *testing.T) {
	start := civil.Date{Year: 2026, Month: 10, Day: 15}
	end := civil.Date{Year: 2026, Month: 10, Day: 16}

	// 10 unmatched out of 100,000 = 0.01% -> passes.
	okRows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 499_990_000_000, N: 99_990},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "", Sats: 10_000_000, N: 10},
	}
	if _, err := buildBtcCoinAgePayload(start, end, okRows, 100_000, 10*1e8, 20); err != nil {
		t.Fatalf("expected 0.01%% unmatched to pass, got %v", err)
	}

	// 11 unmatched out of 100,000 > 0.01% -> fails.
	badRows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 499_989_000_000, N: 99_989},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "", Sats: 11_000_000, N: 11},
	}
	if _, err := buildBtcCoinAgePayload(start, end, badRows, 100_000, 10*1e8, 20); err == nil || !strings.Contains(err.Error(), "unmatched") {
		t.Fatalf("expected unmatched error, got %v", err)
	}
}

func TestBtcCoinAgeGuardFutureCreate(t *testing.T) {
	start := civil.Date{Year: 2026, Month: 10, Day: 15}
	end := civil.Date{Year: 2026, Month: 10, Day: 16}

	// create_day = spend_day + 1 (midnight timestamp inversion) -> accepted and kept.
	invRows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-14", Sats: 499_000_000_000, N: 99_999},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-16", Sats: 1_000_000_000, N: 1},
	}
	payload, err := buildBtcCoinAgePayload(start, end, invRows, 100_000, 10*1e8, 20)
	if err != nil {
		t.Fatalf("expected create_day = spend_day + 1 to pass, got %v", err)
	}
	if len(payload.Matrix) != 2 || payload.Matrix[1].CreateDay != "2026-10-16" {
		t.Fatalf("expected negative-age row to be kept in matrix, got %+v", payload.Matrix)
	}

	// create_day = spend_day + 2 -> suspicious.
	badRows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-17", Sats: 500_000_000_000, N: 100_000},
	}
	if _, err := buildBtcCoinAgePayload(start, end, badRows, 100_000, 10*1e8, 20); err == nil || !strings.Contains(err.Error(), "future create_day") {
		t.Fatalf("expected future create_day error for +2 days, got %v", err)
	}
}

func TestBtcCoinAgeGuardFloor(t *testing.T) {
	start := civil.Date{Year: 2026, Month: 10, Day: 15}
	end := civil.Date{Year: 2026, Month: 10, Day: 17}

	// Missing day 2026-10-16 -> suspicious.
	missingDay := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000_000, N: 100_000},
	}
	if _, err := buildBtcCoinAgePayload(start, end, missingDay, 100_000, 10*1e8, 20); err == nil {
		t.Fatalf("expected error on missing day")
	}

	// Below floor (99,999 < 100,000) -> suspicious.
	lowFloor := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 99_999},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2026-10-10", Sats: 500_000_000_000, N: 99_999},
	}
	if _, err := buildBtcCoinAgePayload(start, start.AddDays(1), lowFloor, 100_000, 10*1e8, 20); err == nil {
		t.Fatalf("expected error when inputs < floor")
	}
}

func TestBtcCoinAgeWhalesPerDay(t *testing.T) {
	start := civil.Date{Year: 2026, Month: 10, Day: 15}
	end := civil.Date{Year: 2026, Month: 10, Day: 16}

	rows := []btcCoinAgeRawRow{
		{Kind: "totals", SpendDay: "2026-10-15", Sats: 500_000_000_000, N: 100_000},
		{Kind: "matrix", SpendDay: "2026-10-15", CreateDay: "2020-01-01", Sats: 500_000_000_000, N: 100_000},
	}
	for i := 1; i <= 5; i++ {
		rows = append(rows, btcCoinAgeRawRow{
			Kind:      "whale",
			SpendDay:  "2026-10-15",
			CreateDay: "2020-01-01",
			Sats:      int64(i) * 10 * 100_000_000,
			N:         2,
			SpendTx:   bigquery.NullString{StringVal: fmt.Sprintf("tx_%d", i), Valid: true},
			SpentAt:   bigquery.NullTimestamp{Timestamp: time.Date(2026, 10, 15, i, 0, 0, 0, time.UTC), Valid: true},
		})
	}

	payload, err := buildBtcCoinAgePayload(start, end, rows, 100_000, 10*1e8, 3)
	if err != nil {
		t.Fatalf("buildBtcCoinAgePayload: %v", err)
	}
	if len(payload.Whales) != 3 {
		t.Fatalf("len(Whales) = %d, want 3", len(payload.Whales))
	}
	if payload.Whales[0].SpendTx != "tx_5" || payload.Whales[1].SpendTx != "tx_4" || payload.Whales[2].SpendTx != "tx_3" {
		t.Fatalf("whales order = %+v, want tx_5, tx_4, tx_3", payload.Whales)
	}
}

func TestBtcCoinAgePruneRespectsState(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	batch := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2025-08-01", CreateDay: "2020-01-01", Sats: 100, Inputs: 1},
			{SpendDay: "2025-08-10", CreateDay: "2020-01-01", Sats: 200, Inputs: 1},
			{SpendDay: "2025-08-20", CreateDay: "2020-01-01", Sats: 300, Inputs: 1},
			{SpendDay: "2026-10-15", CreateDay: "2020-01-01", Sats: 400, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			{Date: "2025-08-01", SpentSats: 100},
			{Date: "2025-08-10", SpentSats: 200},
			{Date: "2025-08-20", SpentSats: 300},
			{Date: "2026-10-15", SpentSats: 400},
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, batch, "2025-08-01", "2026-10-15", now); err != nil {
		t.Fatal(err)
	}

	// Horizon = 2026-10-16 - 30 days = 2026-09-16 (all three 2025-08-* rows are older than horizon).
	// State is at 2025-08-10, so 2025-08-20 (> stateAsOf) MUST NOT be pruned.
	pruneFn := pruneBtcCoinAge(store, 30, func() string { return "2025-08-10" })
	if err := pruneFn(ctx, civil.Date{Year: 2026, Month: 10, Day: 16}); err != nil {
		t.Fatal(err)
	}

	m, err := store.loadBtcSpentMatrixByRange(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["2025-08-01"]; ok {
		t.Errorf("expected 2025-08-01 (<= stateAsOf and < horizon) to be pruned")
	}
	if _, ok := m["2025-08-10"]; ok {
		t.Errorf("expected 2025-08-10 (== stateAsOf and < horizon) to be pruned")
	}
	if _, ok := m["2025-08-20"]; !ok {
		t.Errorf("expected 2025-08-20 (> stateAsOf) to be preserved despite being older than horizon")
	}
	if _, ok := m["2026-10-15"]; !ok {
		t.Errorf("expected 2026-10-15 (>= horizon) to be preserved")
	}
}

func TestBtcCoinAgeDailyUpsertOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	batch1 := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2025-01-01", CreateDay: "2020-01-01", Sats: 1000, Inputs: 1},
		},
		Whales: []BtcWhaleRevival{
			{SpendTx: "whale_old", CreateDay: "2020-01-01", SpendDay: "2025-01-01", SpentAt: "2025-01-01T00:00:00Z", Sats: 20_0000_0000, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			{Date: "2025-01-01", SpentSats: 1000},
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, batch1, "2025-01-01", "2025-01-01", now); err != nil {
		t.Fatal(err)
	}

	batch2 := btcCoinAgePayload{
		Matrix: []btcSpentMatrixRow{
			{SpendDay: "2026-10-15", CreateDay: "2024-01-01", Sats: 2000, Inputs: 1},
		},
		Whales: []BtcWhaleRevival{
			{SpendTx: "whale_new", CreateDay: "2024-01-01", SpendDay: "2026-10-15", SpentAt: "2026-10-15T00:00:00Z", Sats: 30_0000_0000, Inputs: 1},
		},
		Days: []BtcCoinageDay{
			{Date: "2026-10-15", SpentSats: 2000},
		},
	}
	if err := store.applyBtcCoinAgeBatch(ctx, batch2, "", "2026-10-15", now); err != nil {
		t.Fatal(err)
	}

	// Prune the matrix aggressively.
	pruneFn := pruneBtcCoinAge(store, 7, func() string { return "2026-10-15" })
	if err := pruneFn(ctx, civil.Date{Year: 2026, Month: 10, Day: 16}); err != nil {
		t.Fatal(err)
	}

	rs := newBtcReadStore(store, (BitcoinConfig{}).settings(), func() time.Time { return now })
	days, err := rs.coinageDays(ctx, "2020-01-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Date != "2025-01-01" || days[1].Date != "2026-10-15" {
		t.Fatalf("expected both historical and new coinage daily rows to survive prune, got %+v", days)
	}
	whales, err := rs.whaleRevivals(ctx, "2020-01-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(whales) != 2 {
		t.Fatalf("expected both whale revivals to survive prune, got %+v", whales)
	}
}
