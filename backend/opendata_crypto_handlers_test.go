package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func TestParseCryptoDays(t *testing.T) {
	allowed := []int{7, 30, 90, 365}
	tests := []struct {
		name    string
		query   string
		def     int
		want    int
		wantErr bool
	}{
		{"default when absent", "", 90, 90, false},
		{"allowed value", "days=30", 90, 30, false},
		{"max value", "days=365", 90, 365, false},
		{"not on whitelist", "days=45", 90, 0, true},
		{"not an integer", "days=abc", 90, 0, true},
		{"negative", "days=-7", 90, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/opendata/crypto/pulse?"+tt.query, nil)
			got, err := parseCryptoDays(r, tt.def, allowed)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("days = %d, want %d", got, tt.want)
			}
		})
	}
}

// The window is half-open [start, end): end is today UTC so results cover
// complete days only and span exactly `days` days.
func TestCryptoWindow(t *testing.T) {
	start, end := cryptoWindow(90)
	today := civil.DateOf(time.Now().UTC())
	if end != today {
		t.Errorf("end = %s, want today UTC %s", end, today)
	}
	if got := end.DaysSince(start); got != 90 {
		t.Errorf("window spans %d days, want 90", got)
	}
}

func TestRollupCryptoKpi(t *testing.T) {
	daily := []CryptoActivityRow{
		{Date: "2026-07-19", TxCount: 100, ValueSettled: 1.5, FeesTotal: 0.1},
		{Date: "2026-07-20", TxCount: 200, ValueSettled: 2.5, FeesTotal: 0.2},
	}
	blocks := []CryptoBlockRow{
		{Date: "2026-07-19", Blocks: 140, FullnessPct: 90},
		{Date: "2026-07-20", Blocks: 150, FullnessPct: 95.5},
	}
	kpi := rollupCryptoKpi(daily, blocks)
	if kpi.Date != "2026-07-20" || kpi.TxCount != 200 || kpi.Blocks != 150 || kpi.FullnessPct != 95.5 {
		t.Errorf("kpi = %+v, want latest day joined with its block stats", kpi)
	}
}

func TestRollupCryptoKpiEmpty(t *testing.T) {
	kpi := rollupCryptoKpi(nil, nil)
	if kpi.Date != "" || kpi.TxCount != 0 {
		t.Errorf("empty input must yield zero KPI, got %+v", kpi)
	}
}
