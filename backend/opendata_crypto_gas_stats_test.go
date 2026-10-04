package main

import (
	"math"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
)

// BQ lands BTC blocks 2–8 minutes late, so the window must not roll to a new
// hour until 15 minutes past it, whatever the server's time zone.
func TestGasWindow(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		now     time.Time
		wantEnd string
	}{
		{"just before watermark", time.Date(2026, 10, 4, 10, 14, 59, 0, time.UTC), "2026-10-04T09:00:00Z"},
		{"at watermark", time.Date(2026, 10, 4, 10, 15, 0, 0, time.UTC), "2026-10-04T10:00:00Z"},
		{"non-UTC server clock", time.Date(2026, 10, 4, 18, 20, 0, 0, shanghai), "2026-10-04T10:00:00Z"},
		{"watermark crosses midnight", time.Date(2026, 10, 4, 0, 5, 0, 0, time.UTC), "2026-10-03T23:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := gasWindow(tt.now)
			if got := end.Format(time.RFC3339); got != tt.wantEnd {
				t.Errorf("end = %s, want %s", got, tt.wantEnd)
			}
			if end.Sub(start) != 72*time.Hour {
				t.Errorf("window = %s, want 72h", end.Sub(start))
			}
		})
	}
}

// GROUP BY drops hours with no blocks (BTC: 2 of the last 720 hours), so the
// series is rebuilt to exactly 72 buckets with empty hours left NULL.
func TestFillGasHours(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	rows := []GasHourRow{
		{HourUTC: "2026-10-01 10:00", Primary: nf(1)},
		{HourUTC: "2026-10-01 12:00", Primary: nf(3)}, // 11:00 missing
		{HourUTC: "2026-10-04 10:00", Primary: nf(9)}, // == end, outside the window
	}
	got := fillGasHours(rows, start)
	if len(got) != 72 {
		t.Fatalf("got %d buckets, want 72", len(got))
	}
	if got[0].HourUTC != "2026-10-01 10:00" || got[71].HourUTC != "2026-10-04 09:00" {
		t.Errorf("bucket range = %s .. %s", got[0].HourUTC, got[71].HourUTC)
	}
	if got[1].HourUTC != "2026-10-01 11:00" || got[1].Primary.Valid {
		t.Errorf("missing hour must be an empty bucket, got %+v", got[1])
	}
	if got[2].Primary.Float64 != 3 {
		t.Errorf("12:00 bucket = %+v, want primary 3", got[2])
	}
	for _, h := range got {
		if h.Primary.Valid && h.Primary.Float64 == 9 {
			t.Error("row at the exclusive window end leaked into the series")
		}
	}
}

func TestComputeGasStats(t *testing.T) {
	hours := []GasHourRow{
		{HourUTC: "h0", Primary: nf(3), TotalFee: nf(1.5), TxCount: bigquery.NullInt64{Int64: 10, Valid: true}, Load: nf(50)},
		{HourUTC: "h1", Primary: nf(1), TotalFee: nf(0.5), Load: nf(90)},
		{HourUTC: "h2"}, // empty bucket
		{HourUTC: "h3", Primary: nf(4), Load: nf(90)},
		{HourUTC: "h4", Primary: nf(1)},
		{HourUTC: "h5", Primary: nf(5), TxCount: bigquery.NullInt64{Int64: 5, Valid: true}},
	}
	s := computeGasStats(hours)
	checks := []struct {
		name      string
		got, want any
	}{
		{"samples", s.Samples, 5},
		{"latest", s.Latest, 5.0},
		{"latest hour", s.LatestHour, "h5"},
		{"percentile", s.Percentile, 100},
		{"min", s.MinValue, 1.0},
		{"min hour (first of ties)", s.MinHour, "h1"},
		{"max", s.MaxValue, 5.0},
		{"max hour", s.MaxHour, "h5"},
		{"median", s.Median, 3.0},
		{"p90", s.P90, 4.6},
		{"load max", s.LoadMax, 90.0},
		{"load max hour (first of ties)", s.LoadMaxHour, "h1"},
		{"total fee", s.TotalFee, 2.0},
		{"tx count", s.TxCount, int64(15)},
	}
	for _, c := range checks {
		gf, isFloat := c.got.(float64)
		if isFloat && math.Abs(gf-c.want.(float64)) < 1e-9 {
			continue
		}
		if !isFloat && c.got == c.want {
			continue
		}
		t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
	}
}

// The badge ranks the latest complete hour against the same 72h series.
func TestComputeGasStatsPercentile(t *testing.T) {
	s := computeGasStats([]GasHourRow{{HourUTC: "a", Primary: nf(5)}, {HourUTC: "b", Primary: nf(1)}, {HourUTC: "c", Primary: nf(3)}})
	if s.Percentile != 67 {
		t.Errorf("percentile = %d, want 67 (2 of 3 hours at or below the latest)", s.Percentile)
	}
}

// An empty newest bucket must not hide the series: latest falls back to the
// most recent non-empty hour, and the card shows that hour.
func TestComputeGasStatsLatestBucketEmpty(t *testing.T) {
	s := computeGasStats([]GasHourRow{{HourUTC: "a", Primary: nf(2)}, {HourUTC: "b", Primary: nf(8)}, {HourUTC: "c"}})
	if s.LatestHour != "b" || s.Latest != 8 {
		t.Errorf("latest = %v @ %q, want 8 @ b", s.Latest, s.LatestHour)
	}
}

// A stalled dataset yields 72 empty buckets; the UI keys "no data" off
// samples == 0, so nothing may look like a real P0 reading.
func TestComputeGasStatsAllEmpty(t *testing.T) {
	s := computeGasStats([]GasHourRow{{HourUTC: "a"}, {HourUTC: "b"}})
	if s.Samples != 0 || s.LatestHour != "" || s.Percentile != 0 {
		t.Errorf("all-empty stats = %+v, want zero samples and no latest hour", s)
	}
}
