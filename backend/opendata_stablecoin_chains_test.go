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
