package main

import (
	"context"
	"math"
	"testing"
	"time"
)

// stablePegFixtureInputs fetches every venue fixture through the adapters at
// a fixed clock 8–30 s after the fixtures' own timestamps.
func stablePegFixtureInputs(t *testing.T) stablePegInputs {
	t.Helper()
	at := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	stub := newStableVenueStub(t)
	vf := newStableVenueFetcher(stub.endpoints(), func() time.Time { return at })
	quotes := vf.fetchAll(context.Background(), map[string]bool{"coinbase": true, "kraken": true, "bitstamp": true, "okx": true, "binance": true})
	uni := stableUniswapReading{Price: fxUniswapPrice, ReadAt: at}
	return stablePegInputs{Quotes: quotes, Uniswap: &uni, Now: at}
}

func quoteOf(qs []StableQuote, venue, kind string) (StableQuote, bool) {
	for _, q := range qs {
		if q.Venue == venue && q.Kind == kind {
			return q, true
		}
	}
	return StableQuote{}, false
}

func TestStableWeightedMedian(t *testing.T) {
	in := func(venue string, mid, w float64) stablePegInput {
		return stablePegInput{venue: venue, mid: mid, weight: w}
	}
	cases := []struct {
		name   string
		inputs []stablePegInput
		state  string
		want   float64
	}{
		{"three equal venues", []stablePegInput{in("a", 1, 1), in("b", 2, 1), in("c", 3, 1)}, "ok", 2},
		// a is capped at 50% (its weight becomes the others' sum, 2): the
		// cumulative weight reaches exactly 50% between 1 and 2.
		{"one venue capped at the edge", []stablePegInput{in("a", 1, 10), in("b", 2, 1), in("c", 3, 1)}, "ok", 1.5},
		{"capped venue in the middle", []stablePegInput{in("b", 1, 1), in("a", 2, 10), in("c", 3, 1)}, "ok", 2},
		{"uncapped weighted median", []stablePegInput{in("a", 1, 1), in("b", 2, 1), in("c", 3, 3)}, "ok", 2.5},
		// Exactly two venues: the mean of the two venue prices.
		{"two venues", []stablePegInput{in("a", 1, 5), in("b", 2, 1)}, "ok", 1.5},
		// A venue's price is its own weighted median: a = 1.2 (weight 3 of 4).
		{"two venues, one with two inputs", []stablePegInput{in("a", 1, 1), in("a", 1.2, 3), in("b", 2, 1)}, "ok", 1.6},
		// Two inputs from one venue are still one venue.
		{"single venue", []stablePegInput{in("a", 1, 1), in("a", 1.2, 3)}, "single_venue", 1.2},
	}
	for _, c := range cases {
		state, consensus, single, venues, _ := stableAggregate(c.inputs)
		got := consensus
		if c.state == "single_venue" {
			got = single
		}
		if state != c.state || got == nil || math.Abs(*got-c.want) > 1e-12 {
			t.Errorf("%s: state %s, consensus %v, single %v, venues %v; want %s %v", c.name, state, consensus, single, venues, c.state, c.want)
		}
	}
	if state, consensus, single, venues, _ := stableAggregate(nil); state != "unknown" || consensus != nil || single != nil || len(venues) != 0 {
		t.Errorf("no inputs: %s %v %v %v", state, consensus, single, venues)
	}
	// Display weights: the capped venue holds exactly half.
	_, _, _, _, w := stableAggregate([]stablePegInput{in("a", 1, 10), in("b", 2, 1), in("c", 3, 1)})
	if math.Abs(w[0]-0.5) > 1e-12 || math.Abs(w[1]-0.25) > 1e-12 || math.Abs(w[2]-0.25) > 1e-12 {
		t.Errorf("weights = %v, want [0.5 0.25 0.25]", w)
	}
}

// §5.2–5.3 on the synthetic venue fixtures.
func TestStableComputePegFixtures(t *testing.T) {
	in := stablePegFixtureInputs(t)
	res := stableComputePeg(in)
	mid := func(bid, ask float64) float64 { return (bid + ask) / 2 }

	// USDT: three direct USD venues. Kraken's volume dominates and is capped
	// at 50%, so the cumulative weight reaches exactly half between Coinbase
	// and Kraken: the consensus is their mean.
	usdt := res.Assets["usdt"]
	cb, kr, bs := mid(fxCoinbaseUSDTUSDBid, fxCoinbaseUSDTUSDAsk), mid(fxKrakenUSDTZUSDBid, fxKrakenUSDTZUSDAsk), mid(fxBitstampUSDTUSDBid, fxBitstampUSDTUSDAsk)
	if usdt.State != "ok" || usdt.VenuesUsed != 3 || usdt.InputsUsed != 3 || usdt.Numeraire != "USD" {
		t.Fatalf("usdt = %+v", usdt)
	}
	approxPtr(t, "usdt consensus", usdt.Consensus, (cb+kr)/2, 1e-12)
	approxPtr(t, "usdt dev_bps", usdt.DevBps, ((cb+kr)/2-1)*1e4, 1e-9)
	approxPtr(t, "usdt disp_bps", usdt.DispBps, (kr-bs)*1e4, 1e-9)
	if q, ok := quoteOf(usdt.Quotes, "kraken", "direct"); !ok || !q.Included || q.Weight == nil || math.Abs(*q.Weight-0.5) > 1e-12 {
		t.Errorf("usdt kraken quote = %+v", q)
	}
	// Quote age counts from the fetch for every venue (§5.2). Coinbase's
	// ticker time is its last trade (29 s before the sample here), kept as
	// last_trade_age_s; the other venues' own times are not ages.
	if q, _ := quoteOf(usdt.Quotes, "coinbase", "direct"); q.VolUSD == nil || math.Abs(*q.VolUSD-fxCoinbaseUSDTUSDVol*cb) > 1e-3 ||
		q.AgeSeconds == nil || *q.AgeSeconds != 0 || q.LastTradeAgeS == nil || *q.LastTradeAgeS != 29 {
		t.Errorf("usdt coinbase quote = %+v", q)
	}
	for _, venue := range []string{"kraken", "bitstamp"} {
		if q, _ := quoteOf(usdt.Quotes, venue, "direct"); q.AgeSeconds == nil || *q.AgeSeconds != 0 || q.LastTradeAgeS != nil {
			t.Errorf("usdt %s age = %v, last trade age = %v; want 0 and none", venue, q.AgeSeconds, q.LastTradeAgeS)
		}
	}

	// USDC: Kraken and Bitstamp direct, plus Coinbase derived from
	// USDT-USD ÷ USDT-USDC (one fetch cycle at one venue), weighted by the
	// smaller leg.
	usdc := res.Assets["usdc"]
	cbUSDTUSD, cbUSDTUSDC := mid(fxCoinbaseUSDTUSDBid, fxCoinbaseUSDTUSDAsk), mid(fxCoinbaseUSDTUSDCBid, fxCoinbaseUSDTUSDCAsk)
	der, ok := quoteOf(usdc.Quotes, "coinbase", "derived")
	if !ok || !der.Included || len(der.Legs) != 2 {
		t.Fatalf("usdc coinbase derived = %+v", der)
	}
	approxPtr(t, "usdc derived mid", der.Mid, cbUSDTUSD/cbUSDTUSDC, 1e-12)
	approxPtr(t, "usdc derived bid", der.Bid, fxCoinbaseUSDTUSDBid/fxCoinbaseUSDTUSDCAsk, 1e-12)
	approxPtr(t, "usdc derived vol (smaller leg)", der.VolUSD, math.Min(fxCoinbaseUSDTUSDVol*cbUSDTUSD, fxCoinbaseUSDTUSDCVol*cbUSDTUSDC), 1e-3)
	// The older of the legs' last trades (USDT-USD 29 s, USDT-USDC 28 s).
	if der.LastTradeAgeS == nil || *der.LastTradeAgeS != 29 {
		t.Errorf("usdc derived last trade age = %v, want 29", der.LastTradeAgeS)
	}
	krC, bsC := mid(fxKrakenUSDCUSDBid, fxKrakenUSDCUSDAsk), mid(fxBitstampUSDCUSDBid, fxBitstampUSDCUSDAsk)
	if usdc.State != "ok" || usdc.VenuesUsed != 3 {
		t.Fatalf("usdc = %+v", usdc)
	}
	// Kraken is capped; the half-way point falls between Bitstamp and Kraken.
	approxPtr(t, "usdc consensus", usdc.Consensus, (bsC+krC)/2, 1e-12)

	// EUR/USD: median (here the mean) of Kraken and Bitstamp.
	fx := (mid(fxKrakenZEURZUSDBid, fxKrakenZEURZUSDAsk) + mid(fxBitstampEURUSDBid, fxBitstampEURUSDAsk)) / 2
	approxPtr(t, "fx eur_usd", res.FX.EURUSD, fx, 1e-12)
	if res.FX.VenuesUsed != 2 {
		t.Errorf("fx venues used = %d, want 2", res.FX.VenuesUsed)
	}

	// EURC in EUR: Kraken direct + Kraken derived (one venue group) and
	// Coinbase cross-derived; Bitstamp eurceur fails the volume filter after
	// conversion to USD. Two venues: the mean of the venue prices.
	eurc := res.Assets["eurc"]
	if eurc.Numeraire != "EUR" || eurc.State != "ok" || eurc.VenuesUsed != 2 || eurc.InputsUsed != 3 {
		t.Fatalf("eurc = %+v", eurc)
	}
	bsE, ok := quoteOf(eurc.Quotes, "bitstamp", "direct")
	if !ok || bsE.Included || bsE.ExcludeReason != "low_volume" {
		t.Errorf("bitstamp eurceur = %+v, want excluded for low volume", bsE)
	}
	approxPtr(t, "bitstamp eurceur vol_usd", bsE.VolUSD, fxBitstampEURCEURVol*mid(fxBitstampEURCEURBid, fxBitstampEURCEURAsk)*fx, 1e-3)
	krDirect := mid(fxKrakenEURCEURBid, fxKrakenEURCEURAsk)
	krDerived := mid(fxKrakenEURCUSDBid, fxKrakenEURCUSDAsk) / mid(fxKrakenZEURZUSDBid, fxKrakenZEURZUSDAsk)
	cross := mid(fxCoinbaseEURCUSDCBid, fxCoinbaseEURCUSDCAsk) * *usdc.Consensus / fx
	if q, ok := quoteOf(eurc.Quotes, "coinbase", "cross"); !ok || !q.Included || len(q.Legs) != 3 || q.LastTradeAgeS == nil || *q.LastTradeAgeS != 27 {
		t.Errorf("eurc cross = %+v", q)
	}
	// Kraken's group price is its weighted median: the derived input
	// outweighs the direct one (volume 820k EURC vs 250k).
	approxPtr(t, "eurc consensus", eurc.Consensus, (krDerived+cross)/2, 1e-12)
	if q, _ := quoteOf(eurc.Quotes, "kraken", "direct"); !q.Included || q.Mid == nil || math.Abs(*q.Mid-krDirect) > 1e-12 {
		t.Errorf("kraken eurceur = %+v", q)
	}
	if eurc.DirectEURVenues == nil || *eurc.DirectEURVenues != 1 || eurc.SecondDirectEURVenue == nil || *eurc.SecondDirectEURVenue ||
		eurc.PartiallyDependent == nil || !*eurc.PartiallyDependent {
		t.Errorf("eurc flags: direct EUR venues %v, second %v, partially dependent %v", eurc.DirectEURVenues, eurc.SecondDirectEURVenue, eurc.PartiallyDependent)
	}
	if usdt.DirectEURVenues != nil || usdt.SecondDirectEURVenue != nil {
		t.Error("EUR-only flags set on usdt")
	}

	// Relative peg (USDT per USDC): median of OKX, Binance, Coinbase
	// 1/USDT-USDC and Uniswap; the exchange median and Uniswap separately.
	rel := res.Relative
	okx, bin := mid(fxOKXUSDCUSDTBid, fxOKXUSDCUSDTAsk), mid(fxBinanceUSDCUSDTBid, fxBinanceUSDCUSDTAsk)
	inv := 1 / cbUSDTUSDC
	if rel.InputsUsed != 4 {
		t.Fatalf("relative = %+v", rel)
	}
	vals := []float64{okx, bin, inv, fxUniswapPrice}
	approxPtr(t, "relative median", rel.Median, stableMedian(vals), 1e-12)
	approxPtr(t, "relative exchange median", rel.ExchangeMedian, stableMedian([]float64{okx, bin, inv}), 1e-12)
	approxPtr(t, "relative uniswap", rel.Uniswap, fxUniswapPrice, 1e-12)
	approxPtr(t, "relative dev_bps", rel.DevBps, (stableMedian(vals)-1)*1e4, 1e-9)
	if q, ok := quoteOf(rel.Quotes, "uniswap", "dex"); !ok || !q.Included || q.SpreadBps != nil || q.VolUSD != nil {
		t.Errorf("uniswap quote = %+v", q)
	}
	if q, ok := quoteOf(rel.Quotes, "coinbase", "inverse"); !ok || q.LastTradeAgeS == nil || *q.LastTradeAgeS != 28 {
		t.Errorf("coinbase inverse = %+v, want last trade age 28", q)
	}
	// Every raw market is evaluated for the venue table.
	if len(res.Markets) != len(stableVenuePairs)+1 {
		t.Errorf("markets = %d, want %d (venue pairs + Uniswap)", len(res.Markets), len(stableVenuePairs)+1)
	}
}

func TestStablePegFilters(t *testing.T) {
	base := stablePegFixtureInputs(t)
	mutate := func(f func(q *stableVenueQuote)) stablePegInputs {
		in := base
		in.Quotes = append([]stableVenueQuote(nil), base.Quotes...)
		for i := range in.Quotes {
			f(&in.Quotes[i])
		}
		return in
	}
	// Stale: a quote fetched more than 300 s before the sample.
	res := stableComputePeg(mutate(func(q *stableVenueQuote) {
		if q.Venue == "bitstamp" && q.Pair == "usdtusd" {
			q.FetchedAt = base.Now.Add(-301 * time.Second)
		}
	}))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "bitstamp", "direct"); q.Included || q.ExcludeReason != "stale" || q.AgeSeconds == nil || *q.AgeSeconds != 301 {
		t.Errorf("stale bitstamp = %+v", q)
	}
	if res.Assets["usdt"].VenuesUsed != 2 {
		t.Errorf("usdt venues with bitstamp stale = %d", res.Assets["usdt"].VenuesUsed)
	}
	// A venue's own ticker time is not the quote age: an old one is fine,
	// one more than 300 s ahead of the fetch is a malformed response.
	res = stableComputePeg(mutate(func(q *stableVenueQuote) {
		if q.Venue == "bitstamp" && q.Pair == "usdtusd" {
			q.TS = q.FetchedAt.Add(-10 * time.Minute)
		}
		if q.Venue == "okx" {
			q.TS = q.FetchedAt.Add(301 * time.Second)
		}
	}))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "bitstamp", "direct"); !q.Included || *q.AgeSeconds != 0 {
		t.Errorf("bitstamp with an old ticker time = %+v, want included at age 0", q)
	}
	if q, _ := quoteOf(res.Relative.Quotes, "okx", "direct"); q.Included || q.ExcludeReason != "bad_response" {
		t.Errorf("okx with a future ticker time = %+v", q)
	}
	// Coinbase: the last trade is information until it is more than an
	// hour old; then the quote is stale, and so is what derives from it.
	lastTrade := func(age time.Duration) stablePegInputs {
		return mutate(func(q *stableVenueQuote) {
			if q.Venue == "coinbase" && q.Pair == "USDT-USD" {
				q.LastTrade = base.Now.Add(-age)
			}
		})
	}
	res = stableComputePeg(lastTrade(time.Hour))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "coinbase", "direct"); !q.Included || q.LastTradeAgeS == nil || *q.LastTradeAgeS != 3600 {
		t.Errorf("coinbase last trade exactly 1 h old = %+v, want included", q)
	}
	res = stableComputePeg(lastTrade(time.Hour + time.Second))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "coinbase", "direct"); q.Included || q.ExcludeReason != "stale" || *q.LastTradeAgeS != 3601 || *q.AgeSeconds != 0 {
		t.Errorf("coinbase last trade 1 h 1 s old = %+v, want stale", q)
	}
	if q, _ := quoteOf(res.Assets["usdc"].Quotes, "coinbase", "derived"); q.Included || q.ExcludeReason != "leg_excluded" {
		t.Errorf("usdc derived from a stale leg = %+v", q)
	}
	res = stableComputePeg(lastTrade(-301 * time.Second))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "coinbase", "direct"); q.Included || q.ExcludeReason != "bad_response" {
		t.Errorf("coinbase last trade in the future = %+v, want bad_response", q)
	}
	// Wide spread (> 50 bps).
	res = stableComputePeg(mutate(func(q *stableVenueQuote) {
		if q.Venue == "kraken" && q.Pair == "USDTZUSD" {
			q.Ask = q.Bid * 1.0051
		}
	}))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "kraken", "direct"); q.Included || q.ExcludeReason != "wide_spread" {
		t.Errorf("wide kraken = %+v", q)
	}
	// Low volume (< $50k).
	res = stableComputePeg(mutate(func(q *stableVenueQuote) {
		if q.Venue == "coinbase" && q.Pair == "USDT-USD" {
			v := 40_000.0
			q.VolBase = &v
		}
	}))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "coinbase", "direct"); q.Included || q.ExcludeReason != "low_volume" {
		t.Errorf("thin coinbase = %+v", q)
	}
	// A derived quote is excluded when either leg is (here the USDT-USD leg).
	if q, _ := quoteOf(res.Assets["usdc"].Quotes, "coinbase", "derived"); q.Included || q.ExcludeReason != "leg_excluded" {
		t.Errorf("usdc derived with a thin leg = %+v", q)
	}
	// A failed fetch keeps its code as the reason; values stay null.
	res = stableComputePeg(mutate(func(q *stableVenueQuote) {
		if q.Venue == "kraken" {
			*q = stableVenueQuote{Venue: q.Venue, Pair: q.Pair, Base: q.Base, Quote: q.Quote, FetchedAt: q.FetchedAt, Code: "timeout"}
		}
	}))
	if q, _ := quoteOf(res.Assets["usdt"].Quotes, "kraken", "direct"); q.Included || q.ExcludeReason != "timeout" || q.Mid != nil {
		t.Errorf("kraken timeout = %+v", q)
	}
	// Without Kraken: EURC keeps Coinbase cross only, FX comes from Bitstamp
	// alone, and one venue is not a consensus.
	eurc := res.Assets["eurc"]
	if eurc.State != "single_venue" || eurc.Consensus != nil || eurc.SingleVenuePrice == nil || eurc.VenuesUsed != 1 || eurc.DevBps != nil {
		t.Errorf("eurc without kraken = %+v", eurc)
	}
	approxPtr(t, "fx from bitstamp only", res.FX.EURUSD, (fxBitstampEURUSDBid+fxBitstampEURUSDAsk)/2, 1e-12)
	if res.FX.VenuesUsed != 1 {
		t.Errorf("fx venues used without kraken = %d, want 1", res.FX.VenuesUsed)
	}
	// No FX at all: the cross input is excluded, and EUR volumes convert with
	// the ECB reference rate when one is known.
	noFX := mutate(func(q *stableVenueQuote) {
		if q.Pair == "ZEURZUSD" || q.Pair == "eurusd" {
			q.Code = "5xx"
		}
	})
	res = stableComputePeg(noFX)
	if q, _ := quoteOf(res.Assets["eurc"].Quotes, "coinbase", "cross"); q.Included || q.ExcludeReason != "leg_excluded" {
		t.Errorf("cross without fx = %+v", q)
	}
	if q, _ := quoteOf(res.Assets["eurc"].Quotes, "kraken", "direct"); q.Included || q.ExcludeReason != "fx_unavailable" {
		t.Errorf("kraken eurceur without any fx = %+v", q)
	}
	ecb := fxECBEURUSD2
	noFX.ECBEURUSD = &ecb
	res = stableComputePeg(noFX)
	if q, _ := quoteOf(res.Assets["eurc"].Quotes, "kraken", "direct"); !q.Included || q.VolUSD == nil ||
		math.Abs(*q.VolUSD-fxKrakenEURCEURVol*(fxKrakenEURCEURBid+fxKrakenEURCEURAsk)/2*ecb) > 1e-3 {
		t.Errorf("kraken eurceur with the ECB rate = %+v", q)
	}
	if res.FX.EURUSD != nil || res.FX.VolumeSource != "ecb" || res.FX.VenuesUsed != 0 {
		t.Errorf("fx = %+v, want no consensus and the ECB volume rate", res.FX)
	}
	// Nothing included anywhere: unknown, never a substitute value.
	res = stableComputePeg(mutate(func(q *stableVenueQuote) { q.Code = "disabled" }))
	for id, p := range res.Assets {
		if p.State != "unknown" || p.Consensus != nil || p.SingleVenuePrice != nil || p.VenuesUsed != 0 {
			t.Errorf("%s with every venue off = %+v", id, p)
		}
	}
	if res.Relative.InputsUsed != 1 || res.Relative.Median == nil || res.Relative.ExchangeMedian != nil {
		t.Errorf("relative with only Uniswap = %+v", res.Relative)
	}
	// A stale Uniswap reading is excluded too.
	in := mutate(func(q *stableVenueQuote) { q.Code = "disabled" })
	old := stableUniswapReading{Price: fxUniswapPrice, ReadAt: base.Now.Add(-10 * time.Minute)}
	in.Uniswap = &old
	if r := stableComputePeg(in).Relative; r.Median != nil || r.InputsUsed != 0 {
		t.Errorf("relative with a stale pool read = %+v", r)
	}
}
