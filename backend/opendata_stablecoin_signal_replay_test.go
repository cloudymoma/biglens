package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"testing"
	"time"
)

// Historical replay of the signals (stablecoin.md §13). Each daily value is
// fed to the sampled signals as 3 consecutive 5-minute samples on one
// continuous clock (a calendar gap restarts the signal), and to the
// level-triggered signals as one input. The scenario series reproduce the
// shapes measured on the real data (testdata README §3).

type replayDay struct {
	Day   string
	Price float64
}

// replayS1 returns S1's state after each day's 3 samples.
func replayS1(asset string, days []replayDay) map[string]string {
	var h stableHyst
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	out := map[string]string{}
	prev := ""
	for _, d := range days {
		if prev != "" {
			p, _ := time.Parse(time.DateOnly, prev)
			if p.AddDate(0, 0, 1).Format(time.DateOnly) != d.Day {
				at = at.Add(time.Hour) // not the next day: the signal restarts
			}
		}
		prev = d.Day
		peg := StablePeg{Asset: asset, Numeraire: "USD", State: "ok", Consensus: stableF(d.Price), DevBps: stableF((d.Price - 1) * 1e4),
			InputsUsed: 2, VenuesUsed: 2}
		var st string
		for i := 0; i < 3; i++ {
			at = at.Add(stablePegTick)
			h, st, _ = stableSampledStep(h, stableS1Raw(peg), stableS1Thr, at)
		}
		out[d.Day] = st
	}
	return out
}

func replayCoin(rows []replayPrice, coin string) []replayDay {
	var out []replayDay
	for _, r := range rows {
		if p, ok := r.Prices[coin]; ok {
			out = append(out, replayDay{Day: r.Day, Price: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// replayBitstampLows returns each day's low from a Bitstamp daily OHLC file.
func replayBitstampLows(t *testing.T, file string) []replayDay {
	t.Helper()
	var doc struct {
		Data struct {
			OHLC []struct {
				Timestamp string `json:"timestamp"`
				Low       string `json:"low"`
			} `json:"ohlc"`
		} `json:"data"`
	}
	if err := json.Unmarshal(readStableFixture(t, "replay/"+file), &doc); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out []replayDay
	for _, r := range doc.Data.OHLC {
		ts, err1 := strconv.ParseInt(r.Timestamp, 10, 64)
		low, err2 := strconv.ParseFloat(r.Low, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: bad candle %+v", file, r)
		}
		out = append(out, replayDay{Day: time.Unix(ts, 0).UTC().Format(time.DateOnly), Price: low})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func replayRank(state string) int {
	return map[string]int{stableStateUnknown: -1, stableStateNormal: 0, stableStateWatch: 1, stableStateWarning: 2, stableStateCritical: 3}[state]
}

func replayFirst(days []replayDay, states map[string]string, state string) string {
	for _, d := range days {
		if states[d.Day] == state {
			return d.Day
		}
	}
	return ""
}

func TestStableReplayS1(t *testing.T) {
	// Must fire: the Bitstamp daily lows of the two intraday depegs.
	for _, c := range []struct{ file, asset, day string }{
		{"bitstamp_ohlc_usdcusd_2023-03.json", "usdc", "2023-03-11"}, // low 0.80526, −1,947 bps
		{"bitstamp_ohlc_usdtusd_2022-05.json", "usdt", "2022-05-12"}, // low 0.91011, −899 bps
	} {
		days := replayBitstampLows(t, c.file)
		states := replayS1(c.asset, days)
		if states[c.day] != stableStateCritical || replayFirst(days, states, stableStateCritical) != c.day {
			t.Errorf("%s: S1 on %s = %s (first critical %q), want critical first on %s", c.file, c.day, states[c.day],
				replayFirst(days, states, stableStateCritical), c.day)
		}
	}

	// DefiLlama daily prices alone: USDC reaches critical only on the
	// 2023-03-12 row (0.96107, −389 bps; 03-11 shows −24 bps) ...
	usdc23 := replayCoin(replayPrices(t, "prices_2023-03.json"), "usd-coin")
	s23 := replayS1("usdc", usdc23)
	if s23["2023-03-11"] != stableStateNormal || replayFirst(usdc23, s23, stableStateCritical) != "2023-03-12" {
		t.Errorf("usdc 2023 on DefiLlama: 03-11 %s, first critical %q; want normal and 2023-03-12", s23["2023-03-11"],
			replayFirst(usdc23, s23, stableStateCritical))
	}
	// ... and USDT May 2022 never goes above watch: its only watch day is
	// the unrelated +32.7 bps premium on 2022-05-01. Nobody should rely on
	// DefiLlama for intraday depegs.
	usdt22 := replayCoin(replayPrices(t, "prices_2022-05.json"), "tether")
	s22 := replayS1("usdt", usdt22)
	peak := stableStateNormal
	for _, d := range usdt22 {
		if replayRank(s22[d.Day]) > replayRank(peak) {
			peak = s22[d.Day]
		}
	}
	if peak != stableStateWatch || replayFirst(usdt22, s22, stableStateWatch) != "2022-05-01" {
		t.Errorf("usdt 2022 on DefiLlama: peak %s, first watch %q; want watch, first on 2022-05-01", peak, replayFirst(usdt22, s22, stableStateWatch))
	}

	// Must stay quiet: 2024 never reaches warning (no day ≥ 100 bps). The
	// sparse 2024 file lists the days off peg, so each restarts the signal;
	// the days ending at watch equal the pinned ≥ 30 bps counts.
	for _, c := range []struct {
		coin, asset string
		watchDays   int
	}{{"tether", "usdt", 10}, {"usd-coin", "usdc", 14}} {
		days := replayCoin(replayPrices(t, "prices_2024.json"), c.coin)
		states := replayS1(c.asset, days)
		watch := 0
		for _, d := range days {
			switch states[d.Day] {
			case stableStateWatch:
				watch++
			case stableStateWarning, stableStateCritical:
				t.Errorf("%s 2024: S1 %s on %s, want below warning", c.asset, states[d.Day], d.Day)
			}
		}
		if watch != c.watchDays {
			t.Errorf("%s 2024: %d days at watch, want %d", c.asset, watch, c.watchDays)
		}
	}
}

// replayS4 returns S4's level after each day of a chart scenario, fed one
// daily point at a time.
func replayS4(t *testing.T, name string) ([]string, map[string]int) {
	t.Helper()
	rows, err := parseLlamaChart(readStableFixture(t, "replay/"+name), "peggedUSD")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	in := stableFlowInput{Source: "aggregator"}
	var days []string
	level := map[string]int{}
	for _, r := range rows {
		in.Points = append(in.Points, stableFlowPoint{Day: r.Day, Value: *r.CirculatingNative})
		in.Now, _ = time.Parse(time.DateOnly, r.Day)
		level[r.Day] = stableS4Raw(in).Level
		days = append(days, r.Day)
	}
	return days, level
}

func TestStableReplayS4(t *testing.T) {
	// Must fire (§13): S4 critical for USDT (24h −3.88% on 2022-05-12) and
	// USDC (24h −3.65% on 2023-03-12). With the 3-point median both reach
	// critical one day later, on 2022-05-13 and 2023-03-13: the median lags
	// by one reading.
	for _, c := range []struct{ file, day string }{
		{"charts_usdt_2022-05.json", "2022-05-13"},
		{"charts_usdc_2023-03.json", "2023-03-13"},
	} {
		days, lvl := replayS4(t, c.file)
		first := ""
		for _, d := range days {
			if lvl[d] == stableLevelCritical {
				first = d
				break
			}
		}
		if first != c.day || lvl[c.day] != stableLevelCritical {
			t.Errorf("%s: first critical %q (level %d on %s), want critical first on %s", c.file, first, lvl[c.day], c.day, c.day)
		}
	}

	// Pinned outcomes accepted at M4 (§13), day by day; every other day is
	// normal and the thresholds are unchanged:
	//   - USDC 2024-04-20, warning: the residue of a DefiLlama chart glitch
	//     (−6.48%, +8.59%, −4.45%). The median removes 04-18 and 04-19, but
	//     the third leg leaves the 3-day median 2.97% below the day before.
	//     A known false positive of the DefiLlama-only path; live S4 uses
	//     Circle's hourly snapshots first.
	//   - USDC 2024-04-25, warning: 04-24's real −1.02% outflow, which was
	//     not reversed. Correct.
	//   - USDC 2024-04-10..14, watch: a real outflow of about 2% a week.
	//   - USDT 2024-12-02, warning: the −1.38% of 12-01 was a real drop that
	//     was not reversed (−1.10% on the median). Correct.
	// A threshold change that raises or drops one of these fails here and
	// needs review.
	for _, c := range []struct {
		file string
		want map[string]int
	}{
		{"charts_usdc_2024.json", map[string]int{"2024-04-10": 1, "2024-04-11": 1, "2024-04-12": 1, "2024-04-13": 1, "2024-04-14": 1,
			"2024-04-20": 2, "2024-04-25": 2}},
		{"charts_usdt_2024.json", map[string]int{"2024-12-02": 2}},
	} {
		days, lvl := replayS4(t, c.file)
		for d := range c.want {
			if _, ok := lvl[d]; !ok {
				t.Errorf("%s: no day %s", c.file, d)
			}
		}
		for _, d := range days {
			got := lvl[d]
			if got == stableLevelUnknown {
				continue // the first days have no median to compare
			}
			if want := c.want[d]; got != want {
				t.Errorf("%s %s: S4 level %d, want %d", c.file, d, got, want)
			}
		}
	}
	// S4 must not fire on the glitch's first two legs.
	if _, lvl := replayS4(t, "charts_usdc_2024.json"); lvl["2024-04-18"] != stableLevelNormal || lvl["2024-04-19"] != stableLevelNormal {
		t.Errorf("usdc 2024-04-18 / 04-19: S4 %d / %d, want normal on both", lvl["2024-04-18"], lvl["2024-04-19"])
	}
}
