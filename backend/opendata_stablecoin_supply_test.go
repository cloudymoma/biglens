package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStableChainKeys(t *testing.T) {
	cases := []struct{ source, name, want string }{
		{"circle", "ETH", "eth"}, {"circle", "ARB", "arb"}, {"circle", "OP", "op"}, {"circle", "BASE", "base"},
		{"circle", "SOL", "sol"}, {"circle", "TRX", "tron"}, {"circle", "AVAX", "avax"}, {"circle", "XLM", "xlm"},
		{"tether", "Ethereum", "eth"}, {"tether", "Tron", "tron"}, {"tether", "Solana", "sol"},
		{"tether", "BNB Smart Chain", "bnb_smart_chain"}, {"tether", "Polkadot Asset Hub", "polkadot_asset_hub"},
		{"defillama", "Ethereum", "eth"}, {"defillama", "Arbitrum", "arb"}, {"defillama", "OP Mainnet", "op"},
		{"defillama", "Optimism", "op"}, {"defillama", "Base", "base"}, {"defillama", "Solana", "sol"},
		{"defillama", "Tron", "tron"}, {"defillama", "BSC", "bsc"}, {"defillama", "Hyperliquid L1", "hyperliquid_l1"},
	}
	for _, c := range cases {
		if got := stableChainKey(c.source, c.name); got != c.want {
			t.Errorf("stableChainKey(%s, %q) = %q, want %q", c.source, c.name, got, c.want)
		}
	}
	native, _ := lookupRegistryToken("arb", "0xaf88d065e77c8cc2239327c5edb3a432268e5831")
	bridged, _ := lookupRegistryToken("arb", "0xff970a61a04b1ca14834a43f5de4533ebddb5cc8")
	if stableOnchainKey(native) != "arb" || stableOnchainKey(bridged) != "arb.bridged" {
		t.Errorf("on-chain keys = %q / %q, want arb / arb.bridged", stableOnchainKey(native), stableOnchainKey(bridged))
	}
}

// stableTestReadings returns successful on-chain readings at readAt for the
// given asset, with supplies by network key (missing keys are not returned).
func stableTestReadings(asset string, readAt time.Time, supply map[string]float64) map[string]stableOnchainReading {
	out := map[string]stableOnchainReading{}
	for _, tok := range registryTokens {
		if tok.Asset != asset {
			continue
		}
		v, ok := supply[stableOnchainKey(tok)]
		if !ok {
			continue
		}
		out[tok.Network+"|"+tok.Contract] = stableOnchainReading{Token: tok, SupplyNative: v, ReadAt: readAt}
	}
	return out
}

func stableFixtureInputs(t *testing.T) (stableSupplyInputs, time.Time) {
	t.Helper()
	fetched := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	llama, err := parseLlamaStablecoins(readStableFixture(t, "defillama_stablecoins.json"), fetched)
	if err != nil {
		t.Fatal(err)
	}
	circle, err := parseCircleStablecoins(readStableFixture(t, "circle_stablecoins.json"), fetched)
	if err != nil {
		t.Fatal(err)
	}
	tether, err := parseTetherTransparency(readStableFixture(t, "tether_transparency.json"), fetched)
	if err != nil {
		t.Fatal(err)
	}
	in := stableSupplyInputs{
		Llama:  stableResult[llamaStableSnapshot]{Value: llama, Has: true, AsOf: fetched},
		Circle: stableResult[circleSnapshot]{Value: circle, Has: true, AsOf: fetched},
		Tether: stableResult[tetherSnapshot]{Value: tether, Has: true, AsOf: fetched},
	}
	return in, fetched
}

func rowByChain(t *testing.T, a StableSupplyAsset, chain string) StableSupplyRow {
	t.Helper()
	for _, r := range a.Chains {
		if r.Chain == chain {
			return r
		}
	}
	t.Fatalf("%s: no row for chain %q", a.Asset, chain)
	return StableSupplyRow{}
}

func TestBuildStableSupplyEURC(t *testing.T) {
	in, fetched := stableFixtureInputs(t)
	in.Onchain = stableTestReadings("EURC", fetched, map[string]float64{"eth": fxCircleEURCEth, "base": fxCircleEURCBase, "sol": 105020000})
	def, _ := stableAssetByID("eurc")
	a := buildStableSupplyAsset(def, in)
	if a.Numeraire != "EUR" || a.Symbol != "EURC" {
		t.Fatalf("asset header = %+v", a)
	}
	eth := rowByChain(t, a, "eth")
	approxPtr(t, "eth issuer", eth.IssuerCirculatingNative, fxCircleEURCEth, 1e-6)
	approxPtr(t, "eth on-chain", eth.OnchainTotalNative, fxCircleEURCEth, 1e-6)
	approxPtr(t, "eth inventory", eth.InventoryNative, 0, 1e-6)
	approxPtr(t, "eth aggregator (normalized)", eth.AggregatorCirculatingNative, fxLlamaEURCEthereumNative, 1e-3)
	if !eth.Verified || eth.Tier != "native" || eth.ChainLabel != "Ethereum" || eth.IssuerAsOf == "" || eth.OnchainAsOf == "" {
		t.Errorf("eth row = %+v", eth)
	}
	approxPtr(t, "eth share", eth.IssuerSharePct, fxCircleEURCEth/fxCircleEURCTotal*100, 1e-9)
	sol := rowByChain(t, a, "sol")
	approxPtr(t, "sol inventory", sol.InventoryNative, 105020000-fxCircleEURCSol, 1e-6)
	// Issuer-only chain: no on-chain read, so no inventory and not verified.
	arc := rowByChain(t, a, "arc")
	if arc.Verified || arc.OnchainTotalNative != nil || arc.InventoryNative != nil || arc.IssuerCirculatingNative == nil {
		t.Errorf("arc row = %+v", arc)
	}
	// Aggregator-only chain (DefiLlama lists it, Circle does not).
	poly := rowByChain(t, a, "polygon")
	if poly.IssuerCirculatingNative != nil || poly.AggregatorCirculatingNative == nil {
		t.Errorf("polygon row = %+v", poly)
	}
	approxPtr(t, "issuer total", a.Totals.IssuerCirculatingNative, fxCircleEURCTotal, 1e-6)
	approxPtr(t, "aggregator total", a.Totals.AggregatorCirculatingNative, fxLlamaEURCCirculatingNative, 0.01)
	approxPtr(t, "aggregator usd", a.Totals.AggregatorCirculatingUSD, fxLlamaEURCCirculatingUSD, 1e-6)
	approxPtr(t, "inventory total", a.Totals.InventoryNative, 105020000-fxCircleEURCSol, 1e-6)
	if a.Totals.InventoryChains != 3 {
		t.Errorf("inventory chains = %d, want 3", a.Totals.InventoryChains)
	}
	r := a.Reconcile.Aggregator
	approxPtr(t, "aggregator diff pct", r.DiffPct, (fxLlamaEURCCirculatingNative-fxCircleEURCTotal)/fxCircleEURCTotal*100, 1e-6)
	if r.AboveWatch || r.AboveWarning || r.WatchPct != 0.5 || r.WarningPct != 2 || r.SkewSeconds == nil {
		t.Errorf("aggregator recon = %+v", r)
	}
}

func TestBuildStableSupplyUSDT(t *testing.T) {
	in, fetched := stableFixtureInputs(t)
	in.Onchain = stableTestReadings("USDT", fetched, map[string]float64{
		"eth": fxTetherEthAuthorized, "tron": fxTetherTronAuthorized, "sol": fxTetherSolAuthorized,
		"arb": 1500000000, "op": 30000000, "op.bridged": 20000000, "base.bridged": 10000000,
	})
	def, _ := stableAssetByID("usdt")
	a := buildStableSupplyAsset(def, in)
	eth := rowByChain(t, a, "eth")
	approxPtr(t, "eth issuer", eth.IssuerCirculatingNative, fxTetherEthCirculating, 1e-3)
	approxPtr(t, "eth unissued", eth.IssuerUnissuedNative, fxTetherEthNotIssued, 1e-6)
	approxPtr(t, "eth inventory (= notIssued)", eth.InventoryNative, fxTetherEthNotIssued, 1e-3)
	sol := rowByChain(t, a, "sol")
	approxPtr(t, "sol quarantined", sol.IssuerQuarantinedNative, fxTetherSolQuarantined, 1e-6)
	approxPtr(t, "sol inventory", sol.InventoryNative, fxTetherSolNotIssued+fxTetherSolQuarantined, 1e-3)
	// USD₮0 on Arbitrum is native-tier on-chain but Tether reports no Arbitrum row.
	arb := rowByChain(t, a, "arb")
	if arb.IssuerCirculatingNative != nil || arb.InventoryNative != nil || arb.OnchainTotalNative == nil || arb.TokenLabel != "USD₮0" {
		t.Errorf("arb row = %+v", arb)
	}
	if b := rowByChain(t, a, "base.bridged"); b.Tier != "bridged" || b.ChainLabel != "Base (Bridged USDT)" || b.InventoryNative != nil {
		t.Errorf("base.bridged row = %+v", b)
	}
	approxPtr(t, "issuer total", a.Totals.IssuerCirculatingNative, fxTetherLiabilitiesUSD, 1e-3)
	if a.Totals.IssuerAsOf != "2026-10-16T23:30:00Z" {
		t.Errorf("issuer as_of = %q, want the Tether update time", a.Totals.IssuerAsOf)
	}
}

func TestStableOnchainRecon(t *testing.T) {
	if got := stableToleranceNative(500e6); got != 1e6 {
		t.Errorf("tolerance(500M) = %v, want 1M (absolute floor)", got)
	}
	if got := stableToleranceNative(50e9); got != 50e6 {
		t.Errorf("tolerance(50B) = %v, want 50M (0.1%%)", got)
	}
	def, _ := stableAssetByID("eurc")
	issuedAt := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC)
	mk := func(onchain float64, readAt time.Time) stableSupplyInputs {
		circle := circleSnapshot{Coins: map[string]circleCoin{"EURC": {Symbol: "EURC", Chains: []circleChain{
			{Chain: "ETH", AmountNative: 300e6, UpdatedAt: issuedAt},
			{Chain: "BASE", AmountNative: 50e6, UpdatedAt: issuedAt},
		}}}}
		return stableSupplyInputs{
			Circle:  stableResult[circleSnapshot]{Value: circle, Has: true},
			Onchain: stableTestReadings("EURC", readAt, map[string]float64{"eth": onchain}),
		}
	}
	find := func(rs []StableReconOnchain, chain string) StableReconOnchain {
		for _, r := range rs {
			if r.Chain == chain {
				return r
			}
		}
		t.Fatalf("no recon row for %s in %+v", chain, rs)
		return StableReconOnchain{}
	}
	streak := newStableReconState()
	// Excess exactly at the tolerance (1M) does not exceed it.
	rs := stableOnchainRecon(def, mk(299e6, issuedAt.Add(15*time.Minute)), streak)
	eth := find(rs, "eth")
	if !eth.Aligned || eth.ExceedsTolerance || eth.ConsecutiveSamples != 0 || eth.SkipReason != "" || eth.Method != "aligned_15m" {
		t.Errorf("at tolerance, 15 min skew: %+v", eth)
	}
	approxPtr(t, "excess", eth.ExcessNative, 1e6, 1e-6)
	approxPtr(t, "tolerance", eth.ToleranceNative, 1e6, 1e-6)
	// BASE has no on-chain reading.
	if b := find(rs, "base"); b.SkipReason != "missing_onchain" || b.ExceedsTolerance {
		t.Errorf("base without reading: %+v", b)
	}
	// One unit over: exceeds; two hourly samples in a row count up and confirm.
	for i := 1; i <= 2; i++ {
		eth = find(stableOnchainRecon(def, mk(299e6-1, issuedAt.Add(time.Minute)), streak), "eth")
		if !eth.ExceedsTolerance || eth.ConsecutiveSamples != i || eth.Confirmed != (i == 2) {
			t.Errorf("sample %d over tolerance: %+v", i, eth)
		}
	}
	// A clean sample resets the streak.
	if eth = find(stableOnchainRecon(def, mk(300e6, issuedAt), streak), "eth"); eth.ExceedsTolerance || eth.ConsecutiveSamples != 0 || eth.Confirmed {
		t.Errorf("clean sample: %+v", eth)
	}
	// Readings more than 15 minutes apart are not compared.
	eth = find(stableOnchainRecon(def, mk(1, issuedAt.Add(15*time.Minute+time.Second)), streak), "eth")
	if eth.Aligned || eth.ExceedsTolerance || eth.SkipReason != "not_aligned" || eth.ConsecutiveSamples != 0 {
		t.Errorf("15m01s skew: %+v", eth)
	}

	// USDT has no 15-minute pairing: the fixture's Tether data is ~62 h old
	// and is still compared with the latest on-chain total.
	in, fetched := stableFixtureInputs(t)
	in.Onchain = stableTestReadings("USDT", fetched, map[string]float64{"eth": fxTetherEthAuthorized})
	usdt, _ := stableAssetByID("usdt")
	if r := find(stableOnchainRecon(usdt, in, newStableReconState()), "eth"); r.SkipReason != "" || r.Method != "latest_update" ||
		r.ExceedsTolerance || r.IssuerUpdateID != fxTetherUpdateID || r.SkewSeconds == nil {
		t.Errorf("usdt eth recon = %+v", r)
	}
}

// S12 USDT exception: the latest Tether snapshot against the latest RPC
// total, confirmed across 2 consecutive Tether updates (new id); snapshots
// older than 7 days are skipped.
func TestStableOnchainReconUSDT(t *testing.T) {
	def, _ := stableAssetByID("usdt")
	t0 := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)
	// Circulating 900M on Ethereum (1,000M authorized − 100M not issued).
	mk := func(id int64, at time.Time, onchain float64, readAt time.Time) stableSupplyInputs {
		tz := tetherSnapshot{UpdateID: id, UpdatedAt: at, Chains: []tetherChain{
			{Name: "Ethereum", TotalAuthorizedNative: 1000e6, NotIssuedNative: 100e6, CirculatingNative: 900e6}}}
		return stableSupplyInputs{Tether: stableResult[tetherSnapshot]{Value: tz, Has: true},
			Onchain: stableTestReadings("USDT", readAt, map[string]float64{"eth": onchain})}
	}
	st := newStableReconState()
	eth := func(in stableSupplyInputs) StableReconOnchain {
		t.Helper()
		for _, r := range stableOnchainRecon(def, in, st) {
			if r.Chain == "eth" {
				return r
			}
		}
		t.Fatal("no eth row")
		return StableReconOnchain{}
	}
	h := time.Hour
	// 30 h old data is compared; on-chain ≥ issuer circulating is clean.
	r := eth(mk(1, t0, 1000e6, t0.Add(30*h)))
	if r.Method != "latest_update" || r.SkipReason != "" || r.Aligned || r.ExceedsTolerance || r.IssuerUpdateID != 1 ||
		r.SkewSeconds == nil || *r.SkewSeconds != 30*3600 || r.ConsecutiveUpdates != 0 || r.Confirmed {
		t.Errorf("clean, 30 h old: %+v", r)
	}
	// Issuer circulating exceeds the on-chain total by 2M (> max(0.1%, 1M)):
	// the first update with the condition is not confirmed.
	if r = eth(mk(1, t0, 898e6, t0.Add(31*h))); !r.ExceedsTolerance || r.ConsecutiveUpdates != 1 || r.Confirmed {
		t.Errorf("update 1 over tolerance: %+v", r)
	}
	approxPtr(t, "excess", r.ExcessNative, 2e6, 1e-6)
	// More hourly samples of the same update do not count.
	if r = eth(mk(1, t0, 898e6, t0.Add(32*h))); r.ConsecutiveUpdates != 1 || r.Confirmed {
		t.Errorf("update 1 again: %+v", r)
	}
	// A new update with the condition confirms it.
	if r = eth(mk(2, t0.Add(24*h), 898e6, t0.Add(33*h))); r.ConsecutiveUpdates != 2 || !r.Confirmed {
		t.Errorf("update 2 over tolerance: %+v", r)
	}
	// A failed RPC read is skipped and keeps the state.
	in := mk(3, t0.Add(48*h), 898e6, t0.Add(49*h))
	for k, rd := range in.Onchain {
		rd.Stale, rd.Code = true, "timeout"
		in.Onchain[k] = rd
	}
	if r = eth(in); r.SkipReason != "onchain_stale" || r.ExceedsTolerance || r.ConsecutiveUpdates != 0 || r.Confirmed {
		t.Errorf("stale on-chain reading: %+v", r)
	}
	if r = eth(mk(3, t0.Add(48*h), 898e6, t0.Add(50*h))); r.ConsecutiveUpdates != 3 || !r.Confirmed {
		t.Errorf("update 3 over tolerance after a failed read: %+v", r)
	}
	// The condition clearing on the same update resets the count.
	if r = eth(mk(3, t0.Add(48*h), 1000e6, t0.Add(51*h))); r.ExceedsTolerance || r.ConsecutiveUpdates != 0 || r.Confirmed {
		t.Errorf("update 3 clean: %+v", r)
	}
	if r = eth(mk(4, t0.Add(72*h), 898e6, t0.Add(73*h))); r.ConsecutiveUpdates != 1 || r.Confirmed {
		t.Errorf("update 4 after a clean update: %+v", r)
	}
	// No on-chain reading at all.
	in = mk(4, t0.Add(72*h), 898e6, t0.Add(74*h))
	in.Onchain = map[string]stableOnchainReading{}
	if r = eth(in); r.SkipReason != "missing_onchain" || r.OnchainNative != nil {
		t.Errorf("missing reading: %+v", r)
	}
	// A snapshot older than 7 days is skipped and clears the history.
	if r = eth(mk(4, t0.Add(72*h), 898e6, t0.Add(72*h+7*24*h+time.Second))); r.SkipReason != "issuer_stale" || r.ExceedsTolerance || r.Confirmed {
		t.Errorf("8 day old snapshot: %+v", r)
	}
	if r = eth(mk(5, t0.Add(300*h), 898e6, t0.Add(301*h))); r.ConsecutiveUpdates != 1 || r.Confirmed {
		t.Errorf("first update after a stale snapshot: %+v", r)
	}
}

// S12 info check: Tether totalAuthorized equals the on-chain totalSupply of
// the chain when no Issue/Redeem happened since the snapshot, i.e. the
// stored hourly readings from the one nearest the snapshot (±1 h) to the
// latest are unchanged.
func TestStableTetherAuthorizedChecks(t *testing.T) {
	ctx := context.Background()
	usdt, _ := stableAssetByID("usdt")
	snapAt := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)
	tz := tetherSnapshot{UpdateID: snapAt.Unix(), UpdatedAt: snapAt, Chains: []tetherChain{
		{Name: "Ethereum", TotalAuthorizedNative: 1000e6, NotIssuedNative: 50e6, CirculatingNative: 950e6},
		{Name: "Tron", TotalAuthorizedNative: 2000e6, NotIssuedNative: 10e6, CirculatingNative: 1990e6},
		{Name: "Solana", TotalAuthorizedNative: 300e6, NotIssuedNative: 30e6, CirculatingNative: 270e6},
	}}
	store := newTestRiskStore(t)
	reading := func(chain string, at time.Time, v float64) stableSupplySnapshot {
		return stableSupplySnapshot{TS: at, Asset: "usdt", Chain: chain, Source: "onchain", OnchainTotalNative: btcFloatPtr(v), FetchedAt: at}
	}
	var rows []stableSupplySnapshot
	for i := 0; i < 5; i++ {
		at := snapAt.Add(-20*time.Minute + time.Duration(i)*time.Hour)
		rows = append(rows, reading("eth", at, 1000e6))
		tron := 2000e6
		if i >= 2 {
			tron = 2010e6 // an Issue after the snapshot
		}
		rows = append(rows, reading("tron", at, tron))
	}
	rows = append(rows, reading("sol", snapAt.Add(3*time.Hour), 300e6)) // first Solana reading 3 h after the snapshot
	if err := store.insertStableSupplySnapshots(ctx, rows); err != nil {
		t.Fatal(err)
	}
	readAt := snapAt.Add(5 * time.Hour)
	mkIn := func(tz tetherSnapshot) stableSupplyInputs {
		return stableSupplyInputs{Tether: stableResult[tetherSnapshot]{Value: tz, Has: true},
			Onchain: stableTestReadings("USDT", readAt, map[string]float64{"eth": 1000e6, "tron": 2010e6, "sol": 300e6})}
	}
	by := func(cs []StableAuthorizedCheck) map[string]StableAuthorizedCheck {
		out := map[string]StableAuthorizedCheck{}
		for _, c := range cs {
			out[c.Chain] = c
		}
		return out
	}
	got := by(stableTetherAuthorizedChecks(ctx, store, usdt, mkIn(tz)))
	if len(got) != 3 {
		t.Fatalf("checks = %+v, want eth, tron and sol", got)
	}
	if c := got["eth"]; c.Result != "match" || c.Level != "info" || c.IssuerUpdateID != snapAt.Unix() || c.DiffNative == nil || *c.DiffNative != 0 {
		t.Errorf("eth = %+v", c)
	}
	if c := got["tron"]; c.Result != "skipped" || c.SkipReason != "supply_changed_since_snapshot" || c.DiffNative != nil {
		t.Errorf("tron = %+v", c)
	}
	if c := got["sol"]; c.Result != "skipped" || c.SkipReason != "no_reading_near_snapshot" {
		t.Errorf("sol = %+v", c)
	}
	// totalAuthorized differing from an unchanged supply is a mismatch (info only).
	tz2 := tz
	tz2.Chains = append([]tetherChain(nil), tz.Chains...)
	tz2.Chains[0].TotalAuthorizedNative = 1000e6 + 5
	if c := by(stableTetherAuthorizedChecks(ctx, store, usdt, mkIn(tz2)))["eth"]; c.Result != "mismatch" || c.Level != "info" || c.DiffNative == nil || *c.DiffNative != -5 {
		t.Errorf("eth mismatch = %+v", c)
	}
	// A stale reading, or one older than the snapshot, is not checked.
	in := mkIn(tz)
	for k, rd := range in.Onchain {
		rd.Stale = true
		in.Onchain[k] = rd
	}
	if c := by(stableTetherAuthorizedChecks(ctx, store, usdt, in))["eth"]; c.Result != "skipped" || c.SkipReason != "onchain_stale" {
		t.Errorf("stale = %+v", c)
	}
	in = mkIn(tz)
	in.Onchain = stableTestReadings("USDT", snapAt.Add(-10*time.Minute), map[string]float64{"eth": 1000e6})
	if c := by(stableTetherAuthorizedChecks(ctx, store, usdt, in))["eth"]; c.Result != "skipped" || c.SkipReason != "onchain_older_than_snapshot" {
		t.Errorf("reading before the snapshot = %+v", c)
	}
	// Without a store only the latest reading counts: within 1 h after the snapshot it is enough.
	in = mkIn(tz)
	in.Onchain = stableTestReadings("USDT", snapAt.Add(30*time.Minute), map[string]float64{"eth": 1000e6})
	if c := by(stableTetherAuthorizedChecks(ctx, nil, usdt, in))["eth"]; c.Result != "match" {
		t.Errorf("no store, reading 30 min after = %+v", c)
	}
	// Circle-issued assets have no such check.
	usdc, _ := stableAssetByID("usdc")
	if cs := stableTetherAuthorizedChecks(ctx, store, usdc, mkIn(tz)); len(cs) != 0 {
		t.Errorf("usdc checks = %+v", cs)
	}
}

func TestStableNetIssuance(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC)
	snap := func(ts time.Time, v float64) stableSupplySnapshot {
		return stableSupplySnapshot{TS: ts, Asset: "usdc", Chain: "all", Source: "issuer", CirculatingNative: btcFloatPtr(v), FetchedAt: ts}
	}
	if err := store.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{
		snap(now.Add(-24*time.Hour), 1000),              // exactly 24 h
		snap(now.Add(-7*24*time.Hour-3*time.Hour), 800), // 7 d − 3 h: inside ±4 h
		snap(now.Add(-7*24*time.Hour+5*time.Hour), 999), // outside ±4 h of 7 d
	}); err != nil {
		t.Fatal(err)
	}
	llama := &llamaAsset{CirculatingNative: btcFloatPtr(1100), PrevDayNative: btcFloatPtr(1090),
		PrevWeekNative: btcFloatPtr(1000), PrevMonthNative: btcFloatPtr(880)}
	llamaAt := now.Add(-5 * time.Minute)
	got := stableNetIssuance(ctx, store, "usdc", btcFloatPtr(1050), now, llama, llamaAt)
	byWin := map[string]StableNetIssuance{}
	for _, n := range got {
		byWin[n.Window] = n
	}
	d := byWin["24h"]
	if d.Source != "issuer" || d.From != "2026-10-18T13:00:00Z" || d.To != "2026-10-19T13:00:00Z" {
		t.Errorf("24h = %+v", d)
	}
	approxPtr(t, "24h net", d.NetNative, 50, 1e-9)
	approxPtr(t, "24h pct", d.NetPct, 5, 1e-9)
	w := byWin["7d"]
	if w.Source != "issuer" || w.From != "2026-10-12T10:00:00Z" {
		t.Errorf("7d = %+v", w)
	}
	approxPtr(t, "7d net", w.NetNative, 250, 1e-9)
	// No issuer snapshot near 30 d: DefiLlama prev values (cold start, §7.4).
	m := byWin["30d"]
	if m.Source != "aggregator" || m.From != "" || m.To != llamaAt.Format(time.RFC3339) {
		t.Errorf("30d = %+v", m)
	}
	approxPtr(t, "30d net", m.NetNative, 220, 1e-9)
	approxPtr(t, "30d pct", m.NetPct, 25, 1e-9)

	// A snapshot only 19 h old is outside the 20–28 h window.
	store2 := newTestRiskStore(t)
	_ = store2.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{snap(now.Add(-19*time.Hour), 1000)})
	for _, n := range stableNetIssuance(ctx, store2, "usdc", btcFloatPtr(1050), now, llama, llamaAt) {
		if n.Window == "24h" && n.Source != "aggregator" {
			t.Errorf("19 h old snapshot used for 24h: %+v", n)
		}
	}
	// Nothing known: nulls, never zero.
	for _, n := range stableNetIssuance(ctx, nil, "usdc", nil, time.Time{}, nil, time.Time{}) {
		if n.Source != "" || n.NetNative != nil || n.NetPct != nil {
			t.Errorf("unknown %s = %+v, want nulls", n.Window, n)
		}
	}
}

// --- sampler integration ---

type stableStub struct {
	mu   sync.Mutex
	hits map[string]int
	fail map[string]int // path → HTTP status to return instead
	srv  *httptest.Server
}

func (s *stableStub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *stableStub) setFail(path string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[path] = code
}

func newStableStub(t *testing.T) *stableStub {
	t.Helper()
	st := &stableStub{hits: map[string]int{}, fail: map[string]int{}}
	files := map[string][]byte{
		"/llama/stablecoins":          readStableFixture(t, "defillama_stablecoins.json"),
		"/circle":                     readStableFixture(t, "circle_stablecoins.json"),
		"/tether":                     readStableFixture(t, "tether_transparency.json"),
		"/llama/stablecoincharts/all": nil, // per id below
		"/ecb/EXR/D.USD.EUR.SP00.A":   readStableFixture(t, "ecb_exr_usd.json"),
		"/ecb/EST/B.EU000A2X2A25.WT":  readStableFixture(t, "ecb_estr.json"),
		"/ust/2026/all":               readStableFixture(t, "treasury_bill_rates.csv"),
	}
	chartUSDT := readStableFixture(t, "defillama_charts_usdt.json")
	chartEURC := readStableFixture(t, "defillama_charts_eurc.json")
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.hits[r.URL.Path]++
		code := st.fail[r.URL.Path]
		st.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		if r.URL.Path == "/llama/stablecoincharts/all" {
			if r.URL.Query().Get("stablecoin") == "50" {
				w.Write(chartEURC)
			} else {
				w.Write(chartUSDT)
			}
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (s *stableStub) endpoints() stableEndpoints {
	return stableEndpoints{LlamaBase: s.srv.URL + "/llama", CircleURL: s.srv.URL + "/circle", TetherURL: s.srv.URL + "/tether",
		ECBBase: s.srv.URL + "/ecb", TreasuryBase: s.srv.URL + "/ust"}
}

// stableRPCStub answers totalSupply (EVM), SPL mint supply (Solana) and TRON
// constant calls with Appendix A-like supplies so reconciliation is clean.
func stableRPCStub(t *testing.T) (rpcs func(string) []string, evmHits *int, mu *sync.Mutex) {
	t.Helper()
	supplies := map[string]float64{
		"0x1abaea1f7c830bd89acc67ec4af516284b1bc33c": fxCircleEURCEth,  // EURC eth (= Circle ETH)
		"0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42": fxCircleEURCBase, // EURC base (= Circle BASE)
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": fxCircleUSDCEth,
		"0xdac17f958d2ee523a2206206994597c13d831ec7": fxTetherEthAuthorized,
	}
	hex := func(v float64) string {
		raw, _ := new(big.Float).Mul(big.NewFloat(v), big.NewFloat(1e6)).Int(nil)
		return fmt.Sprintf("0x%064x", raw)
	}
	var hits int
	var m sync.Mutex
	evm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Lock()
		hits++
		m.Unlock()
		var body strings.Builder
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body.Write(buf[:n])
		v := 1000000.0
		for c, s := range supplies {
			if strings.Contains(strings.ToLower(body.String()), c) {
				v = s
			}
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"%s"}`, hex(v))
	}))
	t.Cleanup(evm.Close)
	sol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":{"data":{"parsed":{"info":{"decimals":6,"supply":"105020000000000"}}}}}}`))
	}))
	t.Cleanup(sol.Close)
	tron := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"result":{"result":true},"constant_result":["%s"]}`, strings.TrimPrefix(hex(fxTetherTronAuthorized), "0x"))
	}))
	t.Cleanup(tron.Close)
	orig := tronGridBaseURL
	tronGridBaseURL = tron.URL
	t.Cleanup(func() { tronGridBaseURL = orig })
	return func(network string) []string {
		if network == "sol" {
			return []string{sol.URL}
		}
		return []string{evm.URL}
	}, &hits, &m
}

func countStableRows(t *testing.T, s *riskStore, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stable_supply_snapshots `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStableSupplySamplerTick(t *testing.T) {
	stub := newStableStub(t)
	rpcs, evmHits, evmMu := stableRPCStub(t)
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	settings := StablecoinConfig{}.settings()
	s := newStableSupplySampler(settings, store, stub.endpoints(), rpcs, func() time.Time { return clock })
	ctx := context.Background()

	s.tick(ctx)
	if n := countStableRows(t, store, `WHERE asset = 'usdc' AND source = 'issuer'`); n != 40 {
		t.Errorf("usdc issuer rows = %d, want 39 chains + total", n)
	}
	if n := countStableRows(t, store, `WHERE asset = 'eurc' AND source = 'issuer'`); n != 10 {
		t.Errorf("eurc issuer rows = %d, want 9 chains + total", n)
	}
	if n := countStableRows(t, store, `WHERE asset = 'usdt' AND source = 'issuer' AND ts = '2026-10-16T23:30:00Z'`); n != 15 {
		t.Errorf("usdt issuer rows at the Tether update time = %d, want 14 chains + total", n)
	}
	if n := countStableRows(t, store, `WHERE source = 'onchain'`); n != len(registryTokens) {
		t.Errorf("onchain rows = %d, want %d (one per registry token)", n, len(registryTokens))
	}
	if n := countStableRows(t, store, `WHERE source = 'aggregator' AND chain = 'all'`); n != 3 {
		t.Errorf("aggregator total rows = %d, want 3", n)
	}
	var tetherSnaps int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM stable_tether_snapshots`).Scan(&tetherSnaps)
	if tetherSnaps != 1 {
		t.Errorf("tether snapshots = %d, want 1", tetherSnaps)
	}
	if d, _ := store.stableSupplyDailySeries(ctx, "usdc", "issuer", "2026-10-01"); len(d) != 1 || d[0].Day != "2026-10-19" {
		t.Errorf("usdc issuer daily = %+v", d)
	}
	if d, _ := store.stableChainDailySeries(ctx, "eurc", "onchain", "2026-10-01"); len(d) != 3 {
		t.Errorf("eurc onchain chain daily = %d rows, want 3", len(d))
	}
	st := s.snapshot()
	usdc := st.Assets["usdc"]
	approxPtr(t, "usdc issuer total", usdc.Totals.IssuerCirculatingNative, fxCircleUSDCTotal, 1e-3)
	for _, src := range usdc.Sources {
		if src.Status != "ok" {
			t.Errorf("usdc source %s = %+v, want ok", src.ID, src)
		}
	}
	if eth := rowByChain(t, st.Assets["eurc"], "eth"); eth.InventoryNative == nil || math.Abs(*eth.InventoryNative) > 1e-6 {
		t.Errorf("eurc eth inventory = %v, want 0", eth.InventoryNative)
	}
	if r := st.Assets["eurc"].Reconcile.Onchain; len(r) == 0 || st.Assets["eurc"].Reconcile.OnchainAsOf == "" {
		t.Errorf("eurc on-chain recon = %+v", st.Assets["eurc"].Reconcile)
	}
	// USDT: compared against the ~62 h old Tether update; the info check
	// needs a reading within an hour of the snapshot, which a fresh start lacks.
	usdtRec := st.Assets["usdt"].Reconcile
	for _, r := range usdtRec.Onchain {
		if r.Chain == "eth" && (r.Method != "latest_update" || r.SkipReason != "" || r.ExceedsTolerance) {
			t.Errorf("usdt eth recon = %+v", r)
		}
	}
	if len(usdtRec.TetherAuthorized) != 3 {
		t.Errorf("usdt authorized checks = %+v, want eth, tron and sol", usdtRec.TetherAuthorized)
	}
	for _, c := range usdtRec.TetherAuthorized {
		if c.Result != "skipped" || c.SkipReason != "no_reading_near_snapshot" {
			t.Errorf("usdt authorized check = %+v", c)
		}
	}
	if st.Assets["usdc"].Reconcile.TetherAuthorized != nil {
		t.Errorf("usdc has Tether checks: %+v", st.Assets["usdc"].Reconcile.TetherAuthorized)
	}
	if st.MarketTotalUSD == nil || *st.MarketTotalUSD <= 0 {
		t.Errorf("market total = %v", st.MarketTotalUSD)
	}

	// 10 minutes later, same hour: Circle and DefiLlama are fetched again,
	// Tether (30 min) and RPC totals (hourly) are not, and nothing new is stored.
	before := countStableRows(t, store, ``)
	evmMu.Lock()
	evmBefore := *evmHits
	evmMu.Unlock()
	clock = clock.Add(10 * time.Minute)
	s.tick(ctx)
	if got := countStableRows(t, store, ``); got != before {
		t.Errorf("rows after a second tick in the same hour = %d, want %d", got, before)
	}
	if stub.count("/circle") != 2 || stub.count("/llama/stablecoins") != 2 || stub.count("/tether") != 1 {
		t.Errorf("fetch counts circle=%d llama=%d tether=%d, want 2/2/1", stub.count("/circle"), stub.count("/llama/stablecoins"), stub.count("/tether"))
	}
	evmMu.Lock()
	if *evmHits != evmBefore {
		t.Errorf("RPC totals re-read after 10 min (%d calls)", *evmHits-evmBefore)
	}
	evmMu.Unlock()

	// Circle fails: the last good value is served as stale.
	stub.setFail("/circle", http.StatusBadGateway)
	clock = clock.Add(10 * time.Minute)
	s.tick(ctx)
	usdc = s.snapshot().Assets["usdc"]
	approxPtr(t, "usdc issuer (stale)", usdc.Totals.IssuerCirculatingNative, fxCircleUSDCTotal, 1e-3)
	var circleSrc *BtcSourceStatus
	for i := range usdc.Sources {
		if usdc.Sources[i].ID == "circle" {
			circleSrc = &usdc.Sources[i]
		}
	}
	if circleSrc == nil || circleSrc.Status != "stale" || circleSrc.Error != "5xx" {
		t.Errorf("circle source after a failure = %+v, want stale/5xx", circleSrc)
	}

	// An hour later the hourly sources are stored again.
	stub.setFail("/circle", 0)
	clock = clock.Add(time.Hour)
	s.tick(ctx)
	if got := countStableRows(t, store, `WHERE source = 'onchain'`); got != 2*len(registryTokens) {
		t.Errorf("onchain rows after an hour = %d, want %d", got, 2*len(registryTokens))
	}
}

func TestStableSupplySamplerDisabledProvider(t *testing.T) {
	stub := newStableStub(t)
	rpcs, _, _ := stableRPCStub(t)
	cfg := StablecoinConfig{Providers: map[string]bool{"circle": false, "rpc_supply": false}}
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	s := newStableSupplySampler(cfg.settings(), newTestRiskStore(t), stub.endpoints(), rpcs, func() time.Time { return clock })
	s.tick(context.Background())
	if stub.count("/circle") != 0 {
		t.Fatalf("disabled provider was called %d times", stub.count("/circle"))
	}
	usdc := s.snapshot().Assets["usdc"]
	if usdc.Totals.IssuerCirculatingNative != nil {
		t.Errorf("issuer total with Circle disabled = %v, want null", *usdc.Totals.IssuerCirculatingNative)
	}
	found := false
	for _, src := range usdc.Sources {
		if src.ID == "circle" {
			found = src.Status == "disabled"
		}
		if src.ID == "rpc_supply" && src.Status != "disabled" {
			t.Errorf("rpc_supply source = %+v, want disabled", src)
		}
	}
	if !found {
		t.Errorf("circle source not reported as disabled: %+v", usdc.Sources)
	}
	for _, n := range usdc.NetIssuance {
		if n.Source != "aggregator" {
			t.Errorf("net issuance %s source = %q, want aggregator fallback", n.Window, n.Source)
		}
	}
	if eth := rowByChain(t, usdc, "eth"); eth.OnchainTotalNative != nil {
		t.Errorf("on-chain total with rpc_supply disabled = %v", *eth.OnchainTotalNative)
	}
}

func TestStableSyncHistoryAndRates(t *testing.T) {
	stub := newStableStub(t)
	store := newTestRiskStore(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	s := newStableSupplySampler(StablecoinConfig{}.settings(), store, stub.endpoints(), nil, func() time.Time { return clock })
	ctx := context.Background()
	s.syncHistory(ctx)
	if stub.count("/llama/stablecoincharts/all") != 3 {
		t.Errorf("chart requests = %d, want 3", stub.count("/llama/stablecoincharts/all"))
	}
	rows, err := store.stableSupplyDailySeries(ctx, "eurc", "aggregator", "2026-10-01")
	if err != nil || len(rows) != 5 {
		t.Fatalf("eurc aggregator daily = %+v, %v", rows, err)
	}
	approxPtr(t, "eurc 2026-10-19 native", rows[4].CirculatingNative, fxChartEURCLastNative, 1e-6)
	approxPtr(t, "eurc 2026-10-19 usd", rows[4].CirculatingUSD, fxChartEURCLastUSD, 1e-6)
	// Not due again within 6 hours.
	clock = clock.Add(time.Hour)
	s.syncHistory(ctx)
	if stub.count("/llama/stablecoincharts/all") != 3 {
		t.Errorf("history re-fetched within 6 h")
	}

	s.syncRates(ctx)
	fx, _ := store.stableRatesSeries(ctx, stableSeriesECBEURUSD, "2026-10-01")
	estr, _ := store.stableRatesSeries(ctx, stableSeriesECBESTRPct, "2026-10-01")
	bill, _ := store.stableRatesSeries(ctx, stableSeriesUST13WPct, "2026-10-01")
	if len(fx) != 3 || len(estr) != 3 || len(bill) != 5 {
		t.Errorf("rates = %d / %d / %d rows, want 3 / 3 / 5", len(fx), len(estr), len(bill))
	}

	// Disabled rate providers are never called.
	stub2 := newStableStub(t)
	cfg := StablecoinConfig{Providers: map[string]bool{"ecb": false, "us_treasury": false, "defillama": false}}
	s2 := newStableSupplySampler(cfg.settings(), newTestRiskStore(t), stub2.endpoints(), nil, func() time.Time { return clock })
	s2.syncRates(ctx)
	s2.syncHistory(ctx)
	for _, p := range []string{"/ecb/EXR/D.USD.EUR.SP00.A", "/ecb/EST/B.EU000A2X2A25.WT", "/ust/2026/all", "/llama/stablecoincharts/all"} {
		if stub2.count(p) != 0 {
			t.Errorf("disabled provider path %s called %d times", p, stub2.count(p))
		}
	}
}
