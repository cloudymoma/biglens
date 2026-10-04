package main

// Pure helpers for the 72h Gas Pulse: the ingestion-safe window, the 72-bucket
// series and the per-chain stats cards (gas_fee_design.md §2.2, §3.1.1, §3.2).

import (
	"math"
	"slices"
	"time"
)

const (
	gasWindowHours = 72
	// gasIngestLag holds the window on the previous hour until BigQuery has
	// landed its last blocks (BTC lags 2–8 minutes).
	gasIngestLag  = 15 * time.Minute
	gasHourLayout = "2006-01-02 15:00"
)

// gasWindow returns the half-open [start, end) of the last 72 complete,
// ingested UTC hours.
func gasWindow(now time.Time) (start, end time.Time) {
	end = now.UTC().Add(-gasIngestLag).Truncate(time.Hour)
	return end.Add(-gasWindowHours * time.Hour), end
}

// fillGasHours returns exactly 72 ascending buckets from start; hours with no
// row stay empty (all NULL) and rows outside the window are dropped.
func fillGasHours(rows []GasHourRow, start time.Time) []GasHourRow {
	byHour := make(map[string]GasHourRow, len(rows))
	for _, r := range rows {
		byHour[r.HourUTC] = r
	}
	out := make([]GasHourRow, gasWindowHours)
	for i := range out {
		hour := start.Add(time.Duration(i) * time.Hour).Format(gasHourLayout)
		if r, ok := byHour[hour]; ok {
			out[i] = r
		} else {
			out[i] = GasHourRow{HourUTC: hour}
		}
	}
	return out
}

// GasStats feeds the four stats cards; every field derives from one 72h
// series so the badge never mixes sources.
type GasStats struct {
	Samples     int     `json:"samples"`
	LatestHour  string  `json:"latest_hour"`
	Latest      float64 `json:"latest"`
	Percentile  int     `json:"percentile"`
	MinValue    float64 `json:"min_value"`
	MinHour     string  `json:"min_hour"`
	MaxValue    float64 `json:"max_value"`
	MaxHour     string  `json:"max_hour"`
	Median      float64 `json:"median"`
	P90         float64 `json:"p90"`
	LoadMax     float64 `json:"load_max"`
	LoadMaxHour string  `json:"load_max_hour"`
	TotalFee    float64 `json:"total_fee"`
	TxCount     int64   `json:"tx_count"`
}

func computeGasStats(hours []GasHourRow) GasStats {
	var s GasStats
	var vals []float64
	for _, h := range hours {
		if h.TotalFee.Valid {
			s.TotalFee += h.TotalFee.Float64
		}
		if h.TxCount.Valid {
			s.TxCount += h.TxCount.Int64
		}
		if h.Load.Valid && (s.LoadMaxHour == "" || h.Load.Float64 > s.LoadMax) {
			s.LoadMax, s.LoadMaxHour = h.Load.Float64, h.HourUTC
		}
		if !h.Primary.Valid {
			continue
		}
		v := h.Primary.Float64
		vals = append(vals, v)
		if s.MinHour == "" || v < s.MinValue {
			s.MinValue, s.MinHour = v, h.HourUTC
		}
		if s.MaxHour == "" || v > s.MaxValue {
			s.MaxValue, s.MaxHour = v, h.HourUTC
		}
		s.Latest, s.LatestHour = v, h.HourUTC
	}
	s.Samples = len(vals)
	if s.Samples == 0 {
		return s
	}
	atOrBelow := 0
	for _, v := range vals {
		if v <= s.Latest {
			atOrBelow++
		}
	}
	s.Percentile = int(math.Round(float64(atOrBelow) * 100 / float64(s.Samples)))
	sorted := slices.Sorted(slices.Values(vals))
	s.Median = gasQuantile(sorted, 0.5)
	s.P90 = gasQuantile(sorted, 0.9)
	return s
}

// gasQuantile interpolates linearly between the closest ranks of sorted.
func gasQuantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}
