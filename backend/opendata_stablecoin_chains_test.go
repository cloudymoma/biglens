package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func chainRow(t *testing.T, tables []StableChainTable, asset, chain string) StableChainRow {
	t.Helper()
	for _, tb := range tables {
		if tb.Asset != asset {
			continue
		}
		for _, r := range tb.Rows {
			if r.Chain == chain {
				return r
			}
		}
	}
	t.Fatalf("no %s row for %s", chain, asset)
	return StableChainRow{}
}

// warmGasLive puts what the Gas page caches into h.cache: TRON resource
// prices, today's calibration, Coinbase spots and the L1/L2 quotes.
func warmGasLive(h *APIHandler) (cal *GasCalibration) {
	cal = &GasCalibration{USDTHolder: TransferProfile{Energy: 64_285, Bandwidth: 345}, USDTNew: TransferProfile{Energy: 130_285, Bandwidth: 345},
		USDCTransferGas: 45_000, MeasuredAt: "2026-10-20T00:10:00Z"}
	h.cache.Set(gasLiveCalibrationKey, cal)
	h.cache.Set(gasLiveTronKey, &tronLiveRaw{EnergySun: 100, BandwidthSun: 1000, EnergySince: "2026-01-01"})
	h.cache.Set("opendata:crypto:spot:TRX", &CryptoSpotData{PriceUSD: 0.25, AsOf: "2026-10-20T12:00:00Z"})
	h.cache.Set("opendata:crypto:spot:ETH", &CryptoSpotData{PriceUSD: 4000, AsOf: "2026-10-20T12:00:00Z"})
	for _, id := range []string{"eth", "arb", "op", "base"} {
		l1 := 0.0
		if id != "eth" {
			l1 = 1e12
		}
		h.cache.Set("opendata:crypto:gaslive:l2:"+id+":45000", &l2Quote{GasPriceWei: 1e9, L1Wei: []float64{l1, l1}})
	}
	return cal
}

func TestStablecoinChainsEndpoint(t *testing.T) {
	stub := newStableStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stub.endpoints(), nil, &clock)

	// Cold: no supply sample, nothing in the gas-live cache. Still 200: the
	// registry deployments with their controls, links and unknown values.
	rec := stableGet(t, h.StablecoinChains, "/api/opendata/stablecoin/chains?asset=usdt")
	if rec.Code != http.StatusOK {
		t.Fatalf("cold status %d", rec.Code)
	}
	cold := decodeStable[StableChainsResponse](t, rec)
	if !cold.WarmingUp || len(cold.Assets) != 1 || len(cold.Assets[0].Rows) == 0 {
		t.Fatalf("cold chains = %+v", cold)
	}
	tron := chainRow(t, cold.Assets, "usdt", "tron")
	if tron.Tier != "native" || !tron.Verified || tron.IssuerCirculatingNative != nil || tron.FreezeMechanism == nil ||
		tron.FreezeMechanism.Kind != "blacklist" || tron.FreezeMechanism.Function != "isBlackListed(address)" || !tron.FreezeMechanism.ControlsMonitored ||
		tron.ExplorerURL != "https://tronscan.org/#/address/TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" {
		t.Errorf("cold tron row = %+v", tron)
	}
	if tron.VerifyPayment == nil || tron.VerifyPayment.Hash != "#pay?asset=USDT&network=tron" {
		t.Errorf("tron verify link = %+v", tron.VerifyPayment)
	}
	// A priced chain without a cached gas-live value says so; it is never
	// fetched by /chains.
	if c := tron.TransferCost; c == nil || c.Status != "not_cached" || c.USD != nil {
		t.Errorf("cold tron cost = %+v", c)
	}
	if arb := chainRow(t, cold.Assets, "usdt", "arb"); arb.FreezeMechanism == nil || arb.FreezeMechanism.Kind != "blocklist" ||
		arb.FreezeMechanism.ControlsMonitored || arb.TransferCost != nil {
		t.Errorf("USD₮0 on Arbitrum = %+v (BigLens does not price USDT on EVM chains)", arb)
	}
	if br := chainRow(t, cold.Assets, "usdt", "base.bridged"); br.Tier != "bridged" || br.FreezeMechanism != nil {
		t.Errorf("bridged USDT on Base = %+v, want no known freeze mechanism", br)
	}

	// Warm: one supply sample (issuer amounts and shares) and a gas-live
	// cache filled by the Gas page.
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	cal := warmGasLive(h)
	// /chains serves the copy the controls run takes from the gas cache (§8.3).
	h.stable.refreshGasCosts(h.stableGasCosts())
	h.cache.Delete("stablecoin:chains:all")
	before := stub.total()
	resp := decodeStable[StableChainsResponse](t, stableGet(t, h.StablecoinChains, "/api/opendata/stablecoin/chains"))
	if stub.total() != before {
		t.Error("/chains called an upstream")
	}
	if resp.WarmingUp || len(resp.Assets) != 3 || resp.AsOf == "" {
		t.Fatalf("warm chains: warming %v, %d assets", resp.WarmingUp, len(resp.Assets))
	}
	tron = chainRow(t, resp.Assets, "usdt", "tron")
	if tron.IssuerCirculatingNative == nil || tron.IssuerSharePct == nil || *tron.IssuerSharePct <= 0 {
		t.Errorf("warm tron supply = %+v", tron)
	}
	// TRON: (energy × energy price + bandwidth × bandwidth price) in sun,
	// at the TRX spot.
	holderTRX := float64(cal.USDTHolder.Energy*100+cal.USDTHolder.Bandwidth*1000) / 1e6
	newTRX := float64(cal.USDTNew.Energy*100+cal.USDTNew.Bandwidth*1000) / 1e6
	if c := tron.TransferCost; c == nil || c.Status != "ok" || c.NativeUnit != "TRX" || c.Native == nil || math.Abs(*c.Native-holderTRX) > 1e-9 ||
		c.USD == nil || math.Abs(*c.USD-holderTRX*0.25) > 1e-9 || c.USDNewAddress == nil || math.Abs(*c.USDNewAddress-newTRX*0.25) > 1e-9 {
		t.Errorf("tron cost = %+v", c)
	}
	// USDC on EVM chains: the measured USDC transfer gas × gas price + the
	// L1 data fee, at the ETH spot.
	for chain, l1 := range map[string]float64{"eth": 0, "arb": 1e12, "base": 1e12} {
		want := (45_000*1e9 + l1) / 1e18 * 4000
		if c := chainRow(t, resp.Assets, "usdc", chain).TransferCost; c == nil || c.Status != "ok" || c.USD == nil || math.Abs(*c.USD-want) > 1e-9 || c.NativeUnit != "ETH" {
			t.Errorf("usdc %s cost = %+v, want %v USD", chain, c, want)
		}
	}
	// EURC: no Payment Check link and no priced transfer.
	eurc := chainRow(t, resp.Assets, "eurc", "eth")
	if eurc.VerifyPayment != nil || eurc.TransferCost != nil || eurc.FreezeMechanism == nil || eurc.FreezeMechanism.Function != "isBlacklisted(address)" {
		t.Errorf("eurc eth = %+v", eurc)
	}
	if sol := chainRow(t, resp.Assets, "eurc", "sol"); sol.FreezeMechanism == nil || sol.FreezeMechanism.Kind != "spl_freeze_authority" ||
		!strings.HasPrefix(sol.ExplorerURL, "https://solscan.io/account/") {
		t.Errorf("eurc sol = %+v", sol)
	}
	// Issuer-reported chains outside the registry have no contract, link or
	// freeze facts, and are not priced.
	for _, tb := range resp.Assets {
		for _, r := range tb.Rows {
			if !r.Verified && (r.ExplorerURL != "" || r.FreezeMechanism != nil || r.TransferCost != nil || r.Contract != "") {
				t.Errorf("%s %s (issuer-reported) = %+v", tb.Asset, r.Chain, r)
			}
		}
	}
	// Chains that only DefiLlama reports (EURC: Tempo, Sonic, Polygon,
	// Cardano, ICP) are "aggregator" rows with the aggregator amount, listed
	// after every row with an issuer amount.
	for _, tb := range resp.Assets {
		if tb.Asset != "eurc" {
			continue
		}
		var agg []string
		seenAgg := false
		for _, r := range tb.Rows {
			switch {
			case r.Tier == "aggregator":
				seenAgg = true
				agg = append(agg, r.ChainLabel)
				if r.IssuerCirculatingNative != nil || r.AggregatorCirculatingNative == nil || r.Verified {
					t.Errorf("eurc aggregator row = %+v", r)
				}
			case r.Tier == "issuer" && r.IssuerCirculatingNative == nil:
				t.Errorf("eurc row %s labelled issuer without an issuer amount", r.Chain)
			case seenAgg && r.IssuerCirculatingNative != nil:
				t.Errorf("eurc issuer row %s listed after aggregator rows", r.Chain)
			}
		}
		if len(agg) != 5 {
			t.Errorf("eurc aggregator rows = %v, want Tempo, Sonic, Polygon, Cardano, ICP", agg)
		}
	}
	// Whitelist.
	if rec := stableGet(t, h.StablecoinChains, "/api/opendata/stablecoin/chains?asset=dai"); rec.Code != http.StatusBadRequest {
		t.Errorf("asset=dai = %d", rec.Code)
	}

	// The gas cache expires; /chains keeps the last good copy, stale after
	// an hour, with the time it was read.
	readAt := clock
	h.cache = NewCache(time.Minute)
	clock = clock.Add(2 * time.Hour)
	h.stable.refreshGasCosts(h.stableGasCosts()) // the cache is empty: nothing replaces the copy
	late := decodeStable[StableChainsResponse](t, stableGet(t, h.StablecoinChains, "/api/opendata/stablecoin/chains?asset=usdt"))
	if c := chainRow(t, late.Assets, "usdt", "tron").TransferCost; c == nil || c.Status != "stale" || c.USD == nil ||
		math.Abs(*c.USD-holderTRX*0.25) > 1e-9 || c.AsOf != stableFmt(readAt) {
		t.Errorf("tron cost 2 h after the last read = %+v, want the stale copy read at %s", c, stableFmt(readAt))
	}
}

// The last-good copy of the gas-live transfer costs (§8.3): ok for an hour
// after the read, stale up to 24 h, then not_cached, as when never filled.
func TestStableGasLastGood(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	g := newStableGasLastGood()
	never := g.view(t0)
	if never.tronUSDT == nil || never.tronUSDT.Status != "not_cached" || never.evmUSDC["eth"] == nil || never.evmUSDC["eth"].Status != "not_cached" {
		t.Fatalf("never filled = %+v %+v", never.tronUSDT, never.evmUSDC["eth"])
	}
	usd := 1.5
	g.update(stableGasCosts{tronUSDT: &StableTransferCost{Status: "ok", USD: &usd, NativeUnit: "TRX"},
		evmUSDC: map[string]*StableTransferCost{"eth": {Status: "ok", USD: &usd}, "arb": {Status: "not_cached"}}}, t0)
	for _, c := range []struct {
		after time.Duration
		want  string
	}{{30 * time.Minute, "ok"}, {time.Hour, "ok"}, {time.Hour + time.Second, "stale"}, {24 * time.Hour, "stale"}, {24*time.Hour + time.Second, "not_cached"}} {
		v := g.view(t0.Add(c.after))
		if v.tronUSDT.Status != c.want || v.evmUSDC["eth"].Status != c.want {
			t.Errorf("after %v: tron %s, eth %s; want %s", c.after, v.tronUSDT.Status, v.evmUSDC["eth"].Status, c.want)
		}
		if c.want != "not_cached" && (v.tronUSDT.USD == nil || *v.tronUSDT.USD != 1.5 || v.tronUSDT.AsOf != stableFmt(t0)) {
			t.Errorf("after %v: tron = %+v", c.after, v.tronUSDT)
		}
		if c.want == "not_cached" && v.tronUSDT.USD != nil {
			t.Errorf("after %v: an expired value is still served: %+v", c.after, v.tronUSDT)
		}
	}
	if v := g.view(t0); v.evmUSDC["arb"].Status != "not_cached" || v.evmUSDC["arb"].Label == "" {
		t.Errorf("arb, never cached = %+v", v.evmUSDC["arb"])
	}
	// A later read without a value keeps the previous copy and its time.
	g.update(stableGasCosts{tronUSDT: &StableTransferCost{Status: "not_cached"}, evmUSDC: map[string]*StableTransferCost{}}, t0.Add(30*time.Minute))
	if v := g.view(t0.Add(90 * time.Minute)); v.tronUSDT.Status != "stale" || v.tronUSDT.AsOf != stableFmt(t0) {
		t.Errorf("after an empty read = %+v", v.tronUSDT)
	}
}

// Every controls run reads the gas cache, even with rpc_controls off.
func TestStableControlsRunReadsGas(t *testing.T) {
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	settings := StablecoinConfig{Providers: map[string]bool{"rpc_controls": false}}.settings()
	c := newStableControlsSampler(settings, newTestRiskStore(t), nil, func() time.Time { return clock })
	var reads []time.Time
	c.gas = func(now time.Time) { reads = append(reads, now) }
	runStableControlsOnce(context.Background(), c, newStableHealth(settings, func() time.Time { return clock }))
	if len(reads) != 1 || !reads[0].Equal(clock) {
		t.Errorf("gas reads = %v, want one at %v", reads, clock)
	}
}

// A Cloudflare challenge (HTTP 403 with an HTML body, as Blockscout Base
// answered in M0) is "blocked" and never parsed.
func TestStableFetchBlockscoutChallenge(t *testing.T) {
	body := fixtureBody(t, "blockscout_usdc_holders.json", "base")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	got, code := stableFetch(context.Background(), srv.URL+"/api/v2/tokens/x/holders", stableTimeoutSmall, stableMaxBodySmall)
	if code != "blocked" || got != nil {
		t.Errorf("challenge page = %q, %d bytes; want blocked and no body", code, len(got))
	}
}

// total counts every request the stub has answered.
func (s *stableStub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.hits {
		n += v
	}
	return n
}
