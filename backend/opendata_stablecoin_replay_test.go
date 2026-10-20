package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// The replay series in testdata/stablecoin/replay are synthetic scenarios that
// reproduce the shapes measured on the real upstream data (README §3). These
// checks pin those shapes so a regenerated fixture cannot drift silently.

type replayFlow struct {
	worst24hDay, worst7dDay, best24hDay string
	worst24hPct, worst7dPct, best24hPct float64
}

func replayFlows(t *testing.T, name string) replayFlow {
	t.Helper()
	rows, err := parseLlamaChart(readStableFixture(t, "replay/"+name), "peggedUSD")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	f := replayFlow{worst24hPct: math.Inf(1), worst7dPct: math.Inf(1), best24hPct: math.Inf(-1)}
	pct := func(a, b *float64) float64 { return (*b - *a) / *a * 100 }
	for i := 1; i < len(rows); i++ {
		if p := pct(rows[i-1].CirculatingNative, rows[i].CirculatingNative); p < f.worst24hPct {
			f.worst24hPct, f.worst24hDay = p, rows[i].Day
		} else if p > f.best24hPct {
			f.best24hPct, f.best24hDay = p, rows[i].Day
		}
		if i >= 7 {
			if p := pct(rows[i-7].CirculatingNative, rows[i].CirculatingNative); p < f.worst7dPct {
				f.worst7dPct, f.worst7dDay = p, rows[i].Day
			}
		}
	}
	return f
}

type replayPrice struct {
	Day    string
	Prices map[string]float64
}

func replayPrices(t *testing.T, name string) []replayPrice {
	t.Helper()
	var raw []struct {
		Date   int64              `json:"date"`
		Prices map[string]float64 `json:"prices"`
	}
	if err := json.Unmarshal(readStableFixture(t, "replay/"+name), &raw); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := make([]replayPrice, len(raw))
	for i, r := range raw {
		out[i] = replayPrice{Day: time.Unix(r.Date, 0).UTC().Format("2006-01-02"), Prices: r.Prices}
	}
	return out
}

// daysAtOrAbove counts days whose |price − 1| is at least bps basis points.
// Prices of exactly 1.003 sit at 30.0 bps, but 1.003 − 1 is 29.99999999999989
// bps in float64: naive compares raw float bps and misses them, which is how
// the design doc's quiet-year counts were produced.
func daysAtOrAbove(rows []replayPrice, coin string, bps float64, naive bool) int {
	eps := 1e-9
	if naive {
		eps = 0
	}
	n := 0
	for _, r := range rows {
		if p, ok := r.Prices[coin]; ok && math.Abs(p-1)*1e4 >= bps-eps {
			n++
		}
	}
	return n
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func TestStableReplayFixtureShapes(t *testing.T) {
	flows := []struct {
		file                   string
		worst24hDay, worst7day string
		worst24h, worst7d      float64
	}{
		{"charts_usdt_2022-05.json", "2022-05-12", "2022-05-18", -3.88, -10.23},
		{"charts_usdc_2023-03.json", "2023-03-12", "2023-03-17", -3.65, -15.17},
		{"charts_usdt_2024.json", "2024-12-01", "2024-12-31", -1.38, -1.21},
		{"charts_usdc_2024.json", "2024-04-18", "2024-04-18", -6.48, -5.35},
	}
	for _, c := range flows {
		f := replayFlows(t, c.file)
		if f.worst24hDay != c.worst24hDay || round2(f.worst24hPct) != c.worst24h {
			t.Errorf("%s worst 24h = %.4f%% on %s, want %.2f%% on %s", c.file, f.worst24hPct, f.worst24hDay, c.worst24h, c.worst24hDay)
		}
		if f.worst7dDay != c.worst7day || round2(f.worst7dPct) != c.worst7d {
			t.Errorf("%s worst 7d = %.4f%% on %s, want %.2f%% on %s", c.file, f.worst7dPct, f.worst7dDay, c.worst7d, c.worst7day)
		}
	}
	// The 2024 USDC glitch rebounds the next day.
	if f := replayFlows(t, "charts_usdc_2024.json"); f.best24hDay != "2024-04-19" || round2(f.best24hPct) != 8.59 {
		t.Errorf("usdc 2024 rebound = %.4f%% on %s, want +8.59%% on 2024-04-19", f.best24hPct, f.best24hDay)
	}

	// USDT May 2022: the daily aggregator price hides the depeg.
	p22 := replayPrices(t, "prices_2022-05.json")
	worst, worstDay := math.Inf(1), ""
	for _, r := range p22 {
		if r.Day >= "2022-05-09" && r.Day <= "2022-05-16" {
			if bps := (r.Prices["tether"] - 1) * 1e4; bps < worst {
				worst, worstDay = bps, r.Day
			}
		}
	}
	if worstDay != "2022-05-15" || round2(worst) != -10.54 {
		t.Errorf("usdt 2022-05-09..16 worst = %.2f bps on %s, want −10.54 bps on 2022-05-15", worst, worstDay)
	}
	if n := daysAtOrAbove(p22, "tether", 30, false); n != 1 {
		t.Errorf("usdt 2022 window days ≥ 30 bps = %d, want 1 (the +32.7 bps premium on 05-01)", n)
	}

	// USDC March 2023.
	p23 := replayPrices(t, "prices_2023-03.json")
	for _, r := range p23 {
		if r.Day == "2023-03-12" && r.Prices["usd-coin"] != 0.961065 {
			t.Errorf("usdc 2023-03-12 = %v, want 0.961065", r.Prices["usd-coin"])
		}
	}
	for _, c := range []struct {
		coin                   string
		ge30, ge30naive, ge100 int
		label                  string
	}{
		{"usd-coin", 10, 9, 1, "usdc"},
		{"tether", 15, 13, 2, "usdt"},
	} {
		a, b, n := daysAtOrAbove(p23, c.coin, 30, false), daysAtOrAbove(p23, c.coin, 100, false), daysAtOrAbove(p23, c.coin, 30, true)
		if a != c.ge30 || b != c.ge100 || n != c.ge30naive {
			t.Errorf("%s 2023-03 days ≥ 30 / ≥ 100 bps = %d / %d (naive ≥ 30: %d), want %d / %d (%d)", c.label, a, b, n, c.ge30, c.ge100, c.ge30naive)
		}
	}

	// 2024: a quiet year.
	p24 := replayPrices(t, "prices_2024.json")
	for _, c := range []struct {
		coin            string
		ge30, ge30naive int
		maxBps          float64
		display         string
	}{{"tether", 10, 5, 60, "usdt"}, {"usd-coin", 14, 9, 80, "usdc"}} {
		peak := 0.0
		for _, r := range p24 {
			peak = math.Max(peak, math.Abs(r.Prices[c.coin]-1)*1e4)
		}
		n, naive := daysAtOrAbove(p24, c.coin, 30, false), daysAtOrAbove(p24, c.coin, 30, true)
		if n != c.ge30 || naive != c.ge30naive || daysAtOrAbove(p24, c.coin, 100, false) != 0 || math.Abs(peak-c.maxBps) > 1e-6 {
			t.Errorf("%s 2024 days ≥ 30 bps = %d (want %d; naive %d, want %d), max %.2f bps (want %.0f), none ≥ 100",
				c.display, n, c.ge30, naive, c.ge30naive, peak, c.maxBps)
		}
	}

	// Bitstamp daily lows show the intraday depegs.
	for _, c := range []struct {
		file, day string
		low       float64
	}{
		{"bitstamp_ohlc_usdtusd_2022-05.json", "2022-05-12", 0.91011},
		{"bitstamp_ohlc_usdcusd_2023-03.json", "2023-03-11", 0.80526},
	} {
		var doc struct {
			Data struct {
				OHLC []struct {
					Timestamp string `json:"timestamp"`
					Low       string `json:"low"`
				} `json:"ohlc"`
			} `json:"data"`
		}
		if err := json.Unmarshal(readStableFixture(t, "replay/"+c.file), &doc); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		minLow, minDay := math.Inf(1), ""
		for _, r := range doc.Data.OHLC {
			var low float64
			if err := json.Unmarshal([]byte(r.Low), &low); err != nil {
				t.Fatalf("%s low %q: %v", c.file, r.Low, err)
			}
			var ts int64
			if err := json.Unmarshal([]byte(r.Timestamp), &ts); err != nil {
				t.Fatalf("%s timestamp %q: %v", c.file, r.Timestamp, err)
			}
			if low < minLow {
				minLow, minDay = low, time.Unix(ts, 0).UTC().Format("2006-01-02")
			}
		}
		if minLow != c.low || minDay != c.day {
			t.Errorf("%s low = %v on %s, want %v on %s", c.file, minLow, minDay, c.low, c.day)
		}
	}
}

// replayDaily returns a chart scenario's 24h and 7d supply changes (%) by day.
func replayDaily(t *testing.T, name string) (d24, d7 map[string]float64) {
	t.Helper()
	rows, err := parseLlamaChart(readStableFixture(t, "replay/"+name), "peggedUSD")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	pct := func(a, b *float64) float64 { return (*b - *a) / *a * 100 }
	d24, d7 = map[string]float64{}, map[string]float64{}
	for i := 1; i < len(rows); i++ {
		d24[rows[i].Day] = pct(rows[i-1].CirculatingNative, rows[i].CirculatingNative)
		if i >= 7 {
			d7[rows[i].Day] = pct(rows[i-7].CirculatingNative, rows[i].CirculatingNative)
		}
	}
	return d24, d7
}

// The S4 flows inputs of each replay scenario (stablecoin.md §6.2 S4, §13):
// the daily 24h and 7d changes on the days that decide the signal. These pins
// keep the scenarios faithful to the measured data (README §3), so the signal
// replay (TestStableReplayS4) tests what really happened.
func TestStableReplayS4Scenarios(t *testing.T) {
	// Must fire: a 24h drop of at least 3%, followed by a 7d change ≤ −2% (a
	// sustained decline, not a glitch).
	for _, c := range []struct {
		file, day, next string
		d24             float64
	}{
		{"charts_usdt_2022-05.json", "2022-05-12", "2022-05-13", -3.88},
		{"charts_usdc_2023-03.json", "2023-03-12", "2023-03-13", -3.65},
	} {
		d24, d7 := replayDaily(t, c.file)
		if round2(d24[c.day]) != c.d24 || d7[c.next] > -2 {
			t.Errorf("%s: 24h %.2f%% on %s, next-day 7d %.2f%%; want %.2f%% and ≤ −2%%", c.file, d24[c.day], c.day, d7[c.next], c.d24)
		}
	}
	// USDT's −1.38% on 2024-12-01 is never followed by a 7d change ≤ −2% in
	// the window (it is not reversed either: README §3).
	d24, d7 := replayDaily(t, "charts_usdt_2024.json")
	if round2(d24["2024-12-01"]) != -1.38 {
		t.Errorf("usdt 2024-12-01 24h = %.3f%%", d24["2024-12-01"])
	}
	for day, v := range d7 {
		if v <= -2 {
			t.Errorf("usdt 2024 7d = %.3f%% on %s: a confirmation the real data does not have", v, day)
		}
	}
	// USDC, April 2024: DefiLlama's chart dips and rebounds over five days,
	// after a real outflow of about 3% a week (measured 24h and 7d, README §3).
	d24, d7 = replayDaily(t, "charts_usdc_2024.json")
	for _, w := range []struct {
		day      string
		d24, d7  float64
		decision string
	}{
		{"2024-04-11", -0.312, -2.104, "7d watch level"},
		{"2024-04-12", 0.100, -2.435, "7d watch level"},
		{"2024-04-13", -0.904, -2.998, "7d watch level"},
		{"2024-04-14", 1.242, -1.868, ""},
		{"2024-04-17", 0.614, 0.895, ""},
		{"2024-04-18", -6.483, -5.350, "critical on its 24h change"},
		{"2024-04-19", 8.591, 2.679, "the rebound"},
		{"2024-04-20", -4.450, -0.996, "critical on its 24h change"},
		{"2024-04-21", 0.169, -2.045, "kept 04-20 under the confirmation rule dropped in M3"},
		{"2024-04-22", 7.284, 5.284, ""},
	} {
		if math.Abs(d24[w.day]-w.d24) > 0.01 || math.Abs(d7[w.day]-w.d7) > 0.01 {
			t.Errorf("usdc %s: 24h %.3f%%, 7d %.3f%%; want %.3f%% and %.3f%% (%s)", w.day, d24[w.day], d7[w.day], w.d24, w.d7, w.decision)
		}
	}
	// The dip spans five days. The confirmation rule dropped in M3 ("a level
	// counts only if the next input still shows a 7d change ≤ −2%") would
	// have put S4 at critical on 2024-04-21; the 3-point median S4 is
	// replayed in TestStableReplayS4.
	if !(d24["2024-04-18"] <= -3 && d7["2024-04-19"] > -2 && d24["2024-04-20"] <= -3 && d7["2024-04-21"] <= -2) {
		t.Error("usdc 2024 glitch lost the shape that decides S4's confirmation")
	}
}
