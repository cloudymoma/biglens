package main

import (
	"context"
	"math"
	"testing"
	"time"
)

// Panel fields the dashboard needs (stablecoin.md §7.1 ticker, §7.2 Peg tab,
// §7.4 cold start): S1 and the start of the 24h range on /live, the market's
// 30-day change; on /peg the per-venue deviation heatmap, the Uniswap series,
// the daily closes behind the depeg-history chart and the ECB reference rate.

func TestStablecoinLivePanelFields(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	h, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)

	// Without the signal engine S1 is null, like worst_signal.
	cold := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	for _, a := range cold.Assets {
		if a.PegSignal != nil || a.DevRangeFrom != "" || a.DevRangePartial {
			t.Errorf("cold %s: peg signal %v, range from %q partial %v", a.Asset, a.PegSignal, a.DevRangeFrom, a.DevRangePartial)
		}
	}
	if cold.MarketTotal30dChangePct != nil {
		t.Errorf("cold market 30d change = %v, want null", *cold.MarketTotal30dChangePct)
	}

	h.stable.wireSignals()
	runStableOnce(ctx, h.stable.supply, h.stable.health)
	first := clock
	runStablePegOnce(ctx, h.stable.peg, h.stable.health)
	clock = clock.Add(5 * time.Minute)
	runStablePegOnce(ctx, h.stable.peg, h.stable.health)
	h.cache = NewCache(time.Minute)
	live := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	for _, a := range live.Assets {
		var s1 string
		for _, sg := range h.stable.signalsFor(a.Asset) {
			if sg.ID == "S1" {
				s1 = sg.State
			}
		}
		// Two samples: S1 is still warming up, and the chip says so.
		if a.PegSignal == nil || *a.PegSignal != s1 || s1 != "unknown" {
			t.Errorf("%s peg signal = %v, want the S1 state %q (unknown while warming up)", a.Asset, a.PegSignal, s1)
		}
		// Cold start (§7.4): the 24h range is labeled "since HH:MM" until 24 h
		// of samples exist.
		if want := stableFmt(first.Truncate(stablePegTick)); a.DevRangeFrom != want || !a.DevRangePartial {
			t.Errorf("%s 24h range from %q partial %v, want %s and partial", a.Asset, a.DevRangeFrom, a.DevRangePartial, want)
		}
	}
	// Total supply's 30-day change (§7.1): Σ circulating vs Σ a month ago, in
	// USD, over the coins that report both.
	var cur, prev float64
	for _, a := range h.stable.snapshot().Llama.Assets {
		if a.CirculatingUSD != nil && a.PrevMonthUSD != nil {
			cur, prev = cur+*a.CirculatingUSD, prev+*a.PrevMonthUSD
		}
	}
	if prev <= 0 {
		t.Fatal("fixture has no month-ago supply")
	}
	approxPtr(t, "market 30d change", live.MarketTotal30dChangePct, (cur/prev-1)*100, 1e-9)

	// A sample in the window's first 5 minutes fills it: no longer partial.
	old := clock.Add(-24*time.Hour + stablePegTick).Truncate(stablePegTick)
	if err := h.stable.store.insertStablePeg5m(ctx, []stablePeg5mRow{{Bucket: old, Asset: "usdt", Numeraire: "USD", State: "ok",
		Consensus: stableF(0.9995), DevBps: stableF(-5), InputsUsed: 3, VenuesUsed: 3}}); err != nil {
		t.Fatal(err)
	}
	h.cache = NewCache(time.Minute)
	full := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	if a := full.Assets[0]; a.Asset != "usdt" || a.DevRangePartial || a.DevRangeFrom != stableFmt(old) {
		t.Errorf("usdt with 24 h of samples: from %q partial %v", a.DevRangeFrom, a.DevRangePartial)
	}
}

func TestStablecoinPegPanels(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	h, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)

	// Cold: no heatmap rows, no Uniswap points, never an error.
	cold := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdc&range=24h"))
	if hm := cold.Assets[0].Heatmap; hm == nil || len(hm.Rows) != 0 || len(hm.Buckets) != 288 || hm.Resolution != "5m" {
		t.Errorf("cold heatmap = %+v", hm)
	}
	if cold.Relative.UniswapSeries == nil || len(cold.Relative.UniswapSeries) != 0 {
		t.Errorf("cold uniswap series = %+v", cold.Relative.UniswapSeries)
	}

	runStablePegOnce(ctx, h.stable.peg, h.stable.health)
	h.cache = NewCache(time.Minute)
	resp := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdc&range=24h"))
	latest := h.stable.peg.latest()

	// Heatmap (§7.2): one row per consensus input, one column per 5-minute
	// bucket over 24 h; the newest bucket holds the sample, as deviation in
	// the asset's numéraire; excluded inputs leave the cell empty.
	usdc := resp.Assets[0]
	hm := usdc.Heatmap
	if hm == nil || hm.Resolution != "5m" || len(hm.Buckets) != 288 || hm.Buckets[287] != stableFmt(clock.Truncate(stablePegTick)) {
		t.Fatalf("usdc heatmap = %+v", hm)
	}
	quotes := latest.Assets["usdc"].Quotes
	if len(hm.Rows) != len(quotes) {
		t.Fatalf("heatmap rows %d, want one per input (%d)", len(hm.Rows), len(quotes))
	}
	for _, q := range quotes {
		var row *StableHeatRow
		for i := range hm.Rows {
			if hm.Rows[i].Venue == q.Venue && hm.Rows[i].Pair == q.Pair {
				row = &hm.Rows[i]
			}
		}
		if row == nil || row.Kind != q.Kind || len(row.DevBps) != 288 {
			t.Errorf("heatmap row for %s %s = %+v", q.Venue, q.Pair, row)
			continue
		}
		for i, v := range row.DevBps[:287] {
			if v != nil {
				t.Errorf("%s %s bucket %d = %v before the first sample", q.Venue, q.Pair, i, *v)
			}
		}
		switch got := row.DevBps[287]; {
		case q.Included && (got == nil || math.Abs(*got-(*q.Mid-1)*1e4) > 1e-9):
			t.Errorf("%s %s newest cell = %v, want %.4f bps", q.Venue, q.Pair, got, (*q.Mid-1)*1e4)
		case !q.Included && got != nil:
			t.Errorf("%s %s is excluded (%s) but its cell = %v", q.Venue, q.Pair, q.ExcludeReason, *got)
		}
	}
	// The relative peg's inputs (exchanges and Uniswap) get a heatmap too.
	if rh := resp.Relative.Heatmap; rh == nil || len(rh.Rows) != len(latest.Relative.Quotes) {
		t.Errorf("relative heatmap = %+v", rh)
	}
	// Uniswap series (DEX panel): the stored 10-minute readings.
	if us := resp.Relative.UniswapSeries; len(us) != 1 || us[0].TS != stableFmt(clock.Truncate(stableDexTick)) ||
		math.Abs(us[0].Price-*latest.Relative.Uniswap) > 1e-12 || math.Abs(us[0].DevBps-(*latest.Relative.Uniswap-1)*1e4) > 1e-9 {
		t.Errorf("uniswap series = %+v, want the reading %v", us, *latest.Relative.Uniswap)
	}

	// Depeg-history chart: every stored day's close by the §7.2 rule, oldest
	// first. USDC 2023-03-12 closes −389 bps on DefiLlama (rounded for the chart).
	closes := usdc.DailyCloses
	if len(closes) == 0 || closes[0].Day < usdc.HistoryFrom {
		t.Fatalf("usdc daily closes = %d, first %+v, history from %s", len(closes), closes, usdc.HistoryFrom)
	}
	found := false
	for i, c := range closes {
		if i > 0 && c.Day <= closes[i-1].Day {
			t.Fatalf("daily closes out of order at %s", c.Day)
		}
		if c.Day == "2023-03-12" {
			found = true
			if c.Source != stablePriceSourceLlama || c.ThresholdBps != 30 || c.DevBps != math.Round((0.961065-1)*1e4*100)/100 || c.Close != 0.961065 {
				t.Errorf("usdc 2023-03-12 close = %+v", c)
			}
		}
	}
	if !found {
		t.Error("usdc daily closes lack 2023-03-12")
	}

	// Long ranges: the 5-minute inputs are kept 30 days, so no heatmap; 7d
	// uses hourly columns.
	if r := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdc&range=90d")); r.Assets[0].Heatmap != nil {
		t.Errorf("90d heatmap = %+v, want null", r.Assets[0].Heatmap)
	}
	if r := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdc&range=7d")); r.Assets[0].Heatmap == nil ||
		r.Assets[0].Heatmap.Resolution != "1h" || len(r.Assets[0].Heatmap.Buckets) != 168 {
		t.Errorf("7d heatmap = %+v", r.Assets[0].Heatmap)
	}

	// EURC mode: the ECB reference rate and its day; USD-only requests omit it.
	rates, err := h.stable.store.stableRatesSeries(ctx, stableSeriesECBEURUSD, "0000-01-01")
	if err != nil || len(rates) == 0 {
		t.Fatalf("ecb rates %v, %v", rates, err)
	}
	eurc := decodeStable[StablePegResponse](t, stableGet(t, h.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=eurc"))
	if e := eurc.ECB; e == nil || e.Day != rates[len(rates)-1].Day || e.Rate != rates[len(rates)-1].Value {
		t.Errorf("ecb = %+v, want %+v", e, rates[len(rates)-1])
	}
	for _, c := range eurc.Assets[0].DailyCloses {
		switch {
		case c.Source == stablePriceSourceLlama && c.ThresholdBps != stableDepegEURLlamaECBBps:
			t.Errorf("eurc defillama ÷ ecb close %+v, want the 180 bps resolution floor", c)
		case c.Source == stablePegSourceKraken && c.ThresholdBps != stableDepegThresholdBps:
			t.Errorf("eurc kraken close %+v, want the 30 bps threshold", c)
		}
	}
	if resp.ECB != nil {
		t.Errorf("usdc request carries ecb %+v", resp.ECB)
	}
}

// The heatmap keeps the worst included sample of each bucket.
func TestStableBuildHeatmap(t *testing.T) {
	end := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return end.Add(time.Duration(m) * time.Minute) }
	rows := []stableInputRow{
		{Bucket: at(-60), Venue: "kraken", Pair: "USDTZUSD", Kind: "direct", Mid: stableF(0.9990), Included: true},
		{Bucket: at(-55), Venue: "kraken", Pair: "USDTZUSD", Kind: "direct", Mid: stableF(0.9970), Included: true},
		{Bucket: at(-50), Venue: "kraken", Pair: "USDTZUSD", Kind: "direct", Mid: stableF(0.9900), Included: false},
		{Bucket: at(0), Venue: "bitstamp", Pair: "usdtusd", Kind: "direct", Mid: stableF(1.0040), Included: true},
		{Bucket: at(5), Venue: "kraken", Pair: "USDTZUSD", Kind: "direct", Mid: nil, Included: false},
	}
	hm := stableBuildHeatmap(rows, end, 2*time.Hour, time.Hour)
	if hm.Resolution != "1h" || len(hm.Buckets) != 2 || hm.Buckets[0] != stableFmt(at(-60)) || hm.Buckets[1] != stableFmt(end) {
		t.Fatalf("buckets = %+v", hm)
	}
	if len(hm.Rows) != 2 || hm.Rows[0].Venue != "bitstamp" || hm.Rows[1].Venue != "kraken" {
		t.Fatalf("rows = %+v", hm.Rows)
	}
	cell := func(r, b int) *float64 { return hm.Rows[r].DevBps[b] }
	// kraken 09:00 hour: −10 and −30 bps included, −100 excluded → −30.
	if v := cell(1, 0); v == nil || math.Abs(*v+30) > 1e-9 {
		t.Errorf("kraken 09:00 = %v, want -30", v)
	}
	if cell(1, 1) != nil || cell(0, 0) != nil {
		t.Errorf("empty cells = %v %v, want null", cell(1, 1), cell(0, 0))
	}
	if v := cell(0, 1); v == nil || math.Abs(*v-40) > 1e-9 {
		t.Errorf("bitstamp 10:00 = %v, want +40", v)
	}
}
