package main

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestStableWatchlist(t *testing.T) {
	asset := func(id, sym, peg string, price, native float64) llamaAsset {
		a := llamaAsset{LlamaID: id, Symbol: sym, Name: sym, PegType: peg, PegMechanism: "fiat-backed", PriceUSD: stableF(price),
			CirculatingNative: stableF(native), CirculatingUSD: stableF(native * price), PrevDayNative: stableF(native * 0.99)}
		return a
	}
	snap := &llamaStableSnapshot{Assets: []llamaAsset{
		asset("1", "USDT", "peggedUSD", 0.9989, 183_000e6),
		asset("2", "USDC", "peggedUSD", 1.0001, 72_000e6),
		asset("50", "EURC", "peggedEUR", 1.12, 440e6),
		asset("51", "EURX", "peggedEUR", 1.13, 100e6),
		asset("60", "XCHF", "peggedCHF", 1.25, 50e6), // other peg types are left out
		asset("61", "FLEX", "peggedVAR", 1.0, 900e6),
		{LlamaID: "62", Symbol: "DEAD", PegType: "peggedUSD"}, // no supply: left out
	}}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	w := stableWatchlist(snap, at, false, stableF(1.12), at)
	if len(w.Rows) != 4 {
		t.Fatalf("rows = %+v, want USDT, USDC, EURC, EURX", w.Rows)
	}
	by := map[string]StableWatchRow{}
	for _, r := range w.Rows {
		by[r.Symbol] = r
	}
	// peggedUSD: (price − 1) × 1e4; peggedEUR: price ÷ EUR/USD − 1, so a €1
	// coin at $1.12 with EUR/USD 1.12 is about 0 bps, not +1,200.
	for _, c := range []struct {
		sym  string
		dev  float64
		core string
	}{{"USDT", -11, "usdt"}, {"USDC", 1, "usdc"}, {"EURC", 0, "eurc"}, {"EURX", (1.13/1.12 - 1) * 1e4, ""}} {
		r := by[c.sym]
		if r.DevBps == nil || math.Abs(*r.DevBps-c.dev) > 1e-6 || r.Core != c.core {
			t.Errorf("%s: dev %v core %q, want %.4f bps core %q", c.sym, r.DevBps, r.Core, c.dev, c.core)
		}
	}
	if r := by["EURC"]; r.Numeraire != "EUR" || r.CirculatingNative == nil || *r.CirculatingNative != 440e6 || r.Change1dPct == nil {
		t.Errorf("EURC row = %+v, want native € supply and a 1d change", r)
	}
	if w.EURSharePct == nil || w.USDTotalUSD == nil || math.Abs(*w.EURSharePct-(440e6*1.12+100e6*1.13)/(*w.USDTotalUSD+440e6*1.12+100e6*1.13)*100) > 1e-9 {
		t.Errorf("EUR share = %v", w.EURSharePct)
	}
	// Unknown FX: EUR deviations are null, never guessed.
	for _, r := range stableWatchlist(snap, at, false, nil, time.Time{}).Rows {
		if r.Numeraire == "EUR" && r.DevBps != nil {
			t.Errorf("%s dev %v without FX, want null", r.Symbol, *r.DevBps)
		}
	}
	if cold := stableWatchlist(nil, time.Time{}, false, nil, time.Time{}); cold.Rows == nil || len(cold.Rows) != 0 {
		t.Errorf("cold watchlist = %+v", cold)
	}
}

// Watchlist coins with |dev| >= 1,000 bps (such as EURR at $0.0758 -> -9,325 bps
// and a bogus 108M EURR native supply = value / price) hide their native supply
// and deviation behind nil ("—") and carry "price unreliable (aggregator)",
// while keeping their USD supply and share. Core-asset signals are unaffected.
func TestStableWatchlistUnreliablePrice(t *testing.T) {
	fx := 1.123
	// EURR at $0.0758 with $8.2M USD value -> ~108.18M native and -9,325.02 bps.
	eurrUSD := 8_200_000.0
	eurrPrice := 0.0758
	// NEAR999 is just under 1,000 bps (-999.0 bps, price = 0.9001).
	nearPrice := 0.9001
	// AT1000 is at the exact 1,000 bps boundary (-1,000.0 bps, price = 0.9000).
	at1000Price := 0.9000
	snap := &llamaStableSnapshot{Assets: []llamaAsset{
		{LlamaID: "1", Symbol: "USDT", Name: "Tether", PegType: "peggedUSD", PegMechanism: "fiat-backed",
			PriceUSD: stableF(0.9999), CirculatingNative: stableF(183_000e6), CirculatingUSD: stableF(183_000e6 * 0.9999)},
		{LlamaID: "50", Symbol: "EURC", Name: "EURC", PegType: "peggedEUR", PegMechanism: "fiat-backed",
			PriceUSD: stableF(1.123), CirculatingNative: stableF(440e6), CirculatingUSD: stableF(440e6 * 1.123)},
		{LlamaID: "80", Symbol: "EURR", Name: "StablR Euro", PegType: "peggedEUR", PegMechanism: "fiat-backed",
			PriceUSD: stableF(eurrPrice), CirculatingNative: stableF(eurrUSD / eurrPrice), CirculatingUSD: stableF(eurrUSD)},
		{LlamaID: "81", Symbol: "NEAR999", Name: "Just Under 1000", PegType: "peggedUSD", PegMechanism: "fiat-backed",
			PriceUSD: stableF(nearPrice), CirculatingNative: stableF(50e6), CirculatingUSD: stableF(50e6 * nearPrice)},
		{LlamaID: "82", Symbol: "AT1000", Name: "Exact 1000", PegType: "peggedUSD", PegMechanism: "fiat-backed",
			PriceUSD: stableF(at1000Price), CirculatingNative: stableF(40e6), CirculatingUSD: stableF(40e6 * at1000Price)},
	}}
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	w := stableWatchlist(snap, at, false, stableF(fx), at)
	by := map[string]StableWatchRow{}
	for _, r := range w.Rows {
		by[r.Symbol] = r
	}

	eurr := by["EURR"]
	if eurr.DevBps != nil || eurr.CirculatingNative != nil {
		t.Errorf("EURR ($0.0758, -9325 bps): dev=%v native=%v, want both nil (rendered as —)", eurr.DevBps, eurr.CirculatingNative)
	}
	if eurr.Note != "price unreliable (aggregator)" {
		t.Errorf("EURR note = %q, want %q", eurr.Note, "price unreliable (aggregator)")
	}
	if eurr.CirculatingUSD == nil || *eurr.CirculatingUSD != eurrUSD || eurr.SharePct == nil || *eurr.SharePct <= 0 {
		t.Errorf("EURR USD supply=%v share=%v, want both preserved", eurr.CirculatingUSD, eurr.SharePct)
	}

	near := by["NEAR999"]
	if near.DevBps == nil || math.Abs(*near.DevBps - -999.0) > 1e-6 || near.CirculatingNative == nil || *near.CirculatingNative != 50e6 || near.Note != "" {
		t.Errorf("NEAR999 (-999 bps, just under 1,000 bps): dev=%v native=%v note=%q, want -999 bps, 50M native and empty note",
			near.DevBps, near.CirculatingNative, near.Note)
	}

	at1000 := by["AT1000"]
	if at1000.DevBps != nil || at1000.CirculatingNative != nil || at1000.Note != "price unreliable (aggregator)" {
		t.Errorf("AT1000 (-1000 bps boundary): dev=%v native=%v note=%q, want nil/nil and unreliable note",
			at1000.DevBps, at1000.CirculatingNative, at1000.Note)
	}
}

// §2.1: the 15 largest USD coins by circulating supply, plus every EUR coin.
func TestStableWatchlistSize(t *testing.T) {
	snap := &llamaStableSnapshot{}
	for i := 0; i < 17; i++ {
		native := float64(1000-i) * 1e6
		snap.Assets = append(snap.Assets, llamaAsset{LlamaID: "u" + strconv.Itoa(i), Symbol: "USD" + strconv.Itoa(i), PegType: "peggedUSD",
			PriceUSD: stableF(1), CirculatingNative: stableF(native), CirculatingUSD: stableF(native)})
	}
	for i := 0; i < 12; i++ {
		native := float64(100-i) * 1e6
		snap.Assets = append(snap.Assets, llamaAsset{LlamaID: "e" + strconv.Itoa(i), Symbol: "EUR" + strconv.Itoa(i), PegType: "peggedEUR",
			PriceUSD: stableF(1.1), CirculatingNative: stableF(native), CirculatingUSD: stableF(native * 1.1)})
	}
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	w := stableWatchlist(snap, at, false, stableF(1.1), at)
	usd, eur := 0, 0
	for _, r := range w.Rows {
		switch r.Numeraire {
		case "USD":
			usd++
			if r.Symbol == "USD15" || r.Symbol == "USD16" {
				t.Errorf("%s is below the top 15 USD coins and must not be listed", r.Symbol)
			}
		case "EUR":
			eur++
		}
	}
	if usd != 15 || eur != 12 {
		t.Errorf("watchlist has %d USD and %d EUR rows, want the top 15 USD coins and all 12 EUR coins", usd, eur)
	}
}

func TestStablecoinOverviewEndpoint(t *testing.T) {
	stub := newStableStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stub.endpoints(), nil, &clock)

	// Cold: 200 with every signal unknown and an empty watchlist.
	rec := stableGet(t, h.StablecoinOverview, "/api/opendata/stablecoin/overview")
	if rec.Code != http.StatusOK {
		t.Fatalf("cold status %d", rec.Code)
	}
	cold := decodeStable[StableOverviewResponse](t, rec)
	if !cold.WarmingUp || cold.Range != "90d" || len(cold.Cards) != 3 || len(cold.Matrix) != 3 || len(cold.Watchlist.Rows) != 0 || cold.Disclaimer == "" {
		t.Fatalf("cold overview = %+v", cold)
	}
	for _, m := range cold.Matrix {
		if m.Worst != stableStateUnknown || len(m.Signals) != len(stableSignalDefs) {
			t.Errorf("cold matrix %s: worst %s, %d signals", m.Asset, m.Worst, len(m.Signals))
		}
	}
	if c := cold.Cards[0]; c.Asset != "usdt" || c.Consensus != nil || c.CirculatingNative != nil || c.Regulatory["EU-MiCA"] != "not_authorized" ||
		c.Attestation == nil || c.Attestation.Firm == "" {
		t.Errorf("cold usdt card = %+v (curated facts are available at once)", c)
	}
	if stableGet(t, h.StablecoinOverview, "/api/opendata/stablecoin/overview?range=5y").Code != http.StatusBadRequest {
		t.Error("range=5y accepted")
	}

	// Warm: one supply sample and the signal engine.
	h.stable.wireSignals()
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	h.cache.Delete("stablecoin:overview:90d")
	before := stub.total()
	warm := decodeStable[StableOverviewResponse](t, stableGet(t, h.StablecoinOverview, "/api/opendata/stablecoin/overview"))
	if stub.total() != before {
		t.Error("/overview called an upstream")
	}
	if warm.WarmingUp || len(warm.Watchlist.Rows) == 0 || warm.Watchlist.AsOf == "" || warm.Alerts == nil {
		t.Fatalf("warm overview: warming %v, %d watchlist rows, alerts %v", warm.WarmingUp, len(warm.Watchlist.Rows), warm.Alerts)
	}
	core := 0
	for _, r := range warm.Watchlist.Rows {
		if r.Core != "" {
			core++
		}
		if r.PegType != "peggedUSD" && r.PegType != "peggedEUR" {
			t.Errorf("watchlist row %s has peg type %s", r.Symbol, r.PegType)
		}
		if r.Numeraire == "EUR" && r.DevBps != nil {
			t.Errorf("%s: EUR deviation %v without a peg sample (no FX consensus), want null", r.Symbol, *r.DevBps)
		}
	}
	if core != 3 {
		t.Errorf("watchlist core rows = %d, want USDT, USDC and EURC", core)
	}
	for _, c := range warm.Cards {
		if c.CirculatingNative == nil || c.CirculatingSource == "" {
			t.Errorf("warm card %s: circulating %v (%s)", c.Asset, c.CirculatingNative, c.CirculatingSource)
		}
	}
	if len(warm.SupplyHistory) != 3 {
		t.Errorf("supply history series = %d, want 3", len(warm.SupplyHistory))
	}
}

func TestStablecoinReservesEndpoint(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stableEndpoints{}, nil, &clock)
	for i, assets := range []float64{189_000e6, 189_132e6} {
		if err := h.stable.store.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: 1_760_000_000 + int64(i)*86_400,
			TotalAssetsUSD: stableF(assets), TotalLiabilitiesUSD: stableF(183_581.2e6), ShareholderEquityUSD: stableF(5_466.9e6),
			ChainsJSON: "[]", FetchedAt: clock}); err != nil {
			t.Fatal(err)
		}
	}
	rec := stableGet(t, h.StablecoinReserves, "/api/opendata/stablecoin/reserves")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	resp := decodeStable[StableReservesResponse](t, rec)
	if resp.Asset != "all" || len(resp.Assets) != 3 || resp.FactsVersion == "" || len(resp.Regimes) == 0 {
		t.Fatalf("reserves = %+v", resp)
	}
	past, future := 0, 0
	for _, k := range resp.Regimes[0].KeyDates {
		if k.Past {
			past++
		} else {
			future++
		}
	}
	if past == 0 || future == 0 {
		t.Errorf("GENIUS key dates: %d past, %d future; want both (enacted 2025-07-18, effective by 2027-01-18)", past, future)
	}
	usdt := resp.Assets[0]
	if usdt.Asset != "usdt" || usdt.FactsStatus != "curated" || usdt.Tether == nil || len(usdt.Tether.History) != 2 || usdt.Tether.Latest == nil ||
		usdt.Tether.Latest.Coverage == nil || math.Abs(*usdt.Tether.Latest.Coverage-189_132e6/183_581.2e6) > 1e-12 {
		t.Fatalf("usdt reserves = %+v", usdt)
	}
	if len(usdt.Signals) != 3 || usdt.Signals[0].ID != "S5" || usdt.Signals[1].ID != "S6" || usdt.Signals[2].ID != "S11" {
		t.Errorf("usdt reserve signals = %+v, want S5, S6, S11", usdt.Signals)
	}
	for i, a := range usdt.Attestations {
		if a.Ratio == nil || a.URL == "" || (i > 0 && usdt.Attestations[i-1].PeriodEnd < a.PeriodEnd) {
			t.Errorf("attestation %d = %+v (want a ratio, a link, newest first)", i, a)
		}
	}
	if len(usdt.Regulatory) < 2 || usdt.Regulatory[0].Regime != "EU-MiCA" || usdt.Regulatory[1].Regime != "US-GENIUS" || usdt.Issuer == nil ||
		len(usdt.Redemption) == 0 || len(usdt.ReserveMix) == 0 {
		t.Errorf("usdt profile: regulatory %+v, issuer %v, redemption %d, mix %d", usdt.Regulatory, usdt.Issuer, len(usdt.Redemption), len(usdt.ReserveMix))
	}
	usdc := resp.Assets[1]
	if usdc.Tether != nil || len(usdc.ReserveMix) < 2 || usdc.ReserveMix[0].AsOf < usdc.ReserveMix[1].AsOf {
		t.Errorf("usdc reserves: tether %v, mix %+v (newest first)", usdc.Tether, usdc.ReserveMix)
	}
	one := decodeStable[StableReservesResponse](t, stableGet(t, h.StablecoinReserves, "/api/opendata/stablecoin/reserves?asset=eurc"))
	if one.Asset != "eurc" || len(one.Assets) != 1 || one.Assets[0].Regulatory[1].Status != "pending_regime" {
		t.Errorf("eurc reserves = %+v (EURC under GENIUS is pending_regime)", one.Assets)
	}
	if stableGet(t, h.StablecoinReserves, "/api/opendata/stablecoin/reserves?asset=dai").Code != http.StatusBadRequest {
		t.Error("asset=dai accepted")
	}
}
