package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func controlsDeployment(t *testing.T, resp StableControlsResponse, asset, chain string) StableControlDeploymentView {
	t.Helper()
	for _, a := range resp.Assets {
		for _, d := range a.Deployments {
			if d.Asset == asset && d.Chain == chain {
				return d
			}
		}
	}
	t.Fatalf("no %s %s deployment", asset, chain)
	return StableControlDeploymentView{}
}

func controlsField(t *testing.T, d StableControlDeploymentView, field string) StableControlFieldView {
	t.Helper()
	for _, f := range d.Fields {
		if f.Field == field {
			return f
		}
	}
	t.Fatalf("no field %s on %s %s", field, d.Asset, d.Chain)
	return StableControlFieldView{}
}

func TestStablecoinControlsEndpoint(t *testing.T) {
	stub, rpcs := newStableControlsStub(t)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stableEndpoints{}, nil, &clock)
	h.stable.controls = newStableControlsSampler(h.stable.settings, h.stable.store, rpcs, func() time.Time { return clock })
	ctx := context.Background()

	// Cold: nothing sampled or synced. 200 with every value unknown.
	rec := stableGet(t, h.StablecoinControls, "/api/opendata/stablecoin/controls?asset=usdc")
	if rec.Code != http.StatusOK {
		t.Fatalf("cold status %d", rec.Code)
	}
	cold := decodeStable[StableControlsResponse](t, rec)
	// USDC: Ethereum, Solana and the native deployments on Arbitrum,
	// Optimism and Base.
	if len(cold.Assets) != 1 || len(cold.Assets[0].Deployments) != 5 || len(cold.Changes) != 0 || cold.Quarantined != nil {
		t.Fatalf("cold controls = %+v", cold)
	}
	d := controlsDeployment(t, cold, "usdc", "eth")
	if d.MonitoringSince != "" || d.Paused != nil || len(d.Fields) != 8 || controlsField(t, d, "owner").Value != nil {
		t.Errorf("cold usdc eth = %+v", d)
	}
	if len(cold.Freezes) != 1 || cold.Freezes[0].Status != "not_synced" || cold.Scam.ETH == nil || cold.Scam.ETH.PoisonHits != nil || cold.Scam.TRON != nil {
		t.Errorf("cold freezes %+v, scam %+v", cold.Freezes, cold.Scam)
	}

	// Two samples (every deployment baselined), then USDC paused on both
	// Ethereum endpoints: one confirmed, critical change.
	runStableControlsOnce(ctx, h.stable.controls, h.stable.health)
	clock = clock.Add(stableControlsTick)
	runStableControlsOnce(ctx, h.stable.controls, h.stable.health)
	stub.set("eth-a", "usdc", "eth", "paused", "true")
	stub.set("eth-b", "usdc", "eth", "paused", "true")
	clock = clock.Add(stableControlsTick)
	runStableControlsOnce(ctx, h.stable.controls, h.stable.health)
	// One pending (unconfirmed) value on Base.
	stub.set("base-a", "eurc", "base", "pauser", "0x00000000000000000000000000000000000000b2")
	clock = clock.Add(stableControlsTick)
	runStableControlsOnce(ctx, h.stable.controls, h.stable.health)
	seedFreezeHistory(t, h.stable.store)
	seedScamRadar(t, h.stable.store)
	if err := h.stable.store.insertStableTetherSnapshot(ctx, stableTetherSnapshot{UpdateID: 1792193400,
		ChainsJSON: `[{"name":"Tron","quarantined_native":12.25},{"name":"Ethereum","quarantined_native":0.5}]`, FetchedAt: clock}); err != nil {
		t.Fatal(err)
	}

	h.cache = NewCache(time.Minute)
	calls := stub.count("eth-a") + stub.count("eth-b") + stub.count("tron") + stub.count("sol-a") + stub.count("base-a")
	resp := decodeStable[StableControlsResponse](t, stableGet(t, h.StablecoinControls, "/api/opendata/stablecoin/controls"))
	if now := stub.count("eth-a") + stub.count("eth-b") + stub.count("tron") + stub.count("sol-a") + stub.count("base-a"); now != calls {
		t.Error("/controls called an RPC")
	}
	if len(resp.Assets) != 3 || resp.ProviderStatus != "ok" || resp.AsOf != stableFmt(clock) {
		t.Fatalf("controls = %d assets, provider %q, as of %q", len(resp.Assets), resp.ProviderStatus, resp.AsOf)
	}
	usdc := controlsDeployment(t, resp, "usdc", "eth")
	if usdc.Paused == nil || !*usdc.Paused || usdc.LastChange != stableFmt(clock.Add(-stableControlsTick)) ||
		len(usdc.ChangedLast7d) != 1 || usdc.ChangedLast7d[0] != "paused" || usdc.MonitoringSince != "2026-10-20T12:00:00Z" ||
		usdc.Contract != "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48" || !strings.HasPrefix(usdc.ExplorerURL, "https://etherscan.io/address/") {
		t.Errorf("usdc eth = %+v", usdc)
	}
	impl := controlsField(t, usdc, "implementation")
	if impl.Value == nil || *impl.Value != "0x43506849d7c04f9138d1a2050bbf3a0c054402dd" || !impl.Baseline || impl.ConfirmedBy != "two_rpcs" ||
		impl.ExplorerURL != "https://etherscan.io/address/0x43506849d7c04f9138d1a2050bbf3a0c054402dd" || impl.Stale {
		t.Errorf("usdc implementation = %+v", impl)
	}
	if r := controlsField(t, usdc, "rescuer"); r.Value == nil || !r.Unset || r.ExplorerURL != "" {
		t.Errorf("unset rescuer = %+v", r)
	}
	// Base and TRON (one endpoint) were baselined on the second sample.
	if tr := controlsDeployment(t, resp, "usdt", "tron"); tr.MonitoringSince != "2026-10-20T12:15:00Z" || tr.Deprecated == nil || *tr.Deprecated {
		t.Errorf("usdt tron = %+v", tr)
	}
	base := controlsDeployment(t, resp, "eurc", "base")
	if p := controlsField(t, base, "pauser"); p.Pending == nil || p.Pending.Value != "0x00000000000000000000000000000000000000b2" ||
		*p.Value != "0xfa223f7bb88c8df670bb3acdf16fcb3abbc01c50" {
		t.Errorf("eurc base pauser = %+v, want the confirmed value and a pending one", p)
	}
	if sol := controlsDeployment(t, resp, "usdc", "sol"); sol.Paused != nil || len(sol.Fields) != 2 ||
		*controlsField(t, sol, "freeze_authority").Value != "7dGbd2QZcCKcTndnHcTL8q7SMVXAkp688NTQYwrRCrar" {
		t.Errorf("usdc sol = %+v", sol)
	}
	// The change log, newest first, critical when paused turns true.
	if len(resp.Changes) != 1 || resp.Changes[0].Field != "paused" || !resp.Changes[0].Critical || resp.Changes[0].New != "true" {
		t.Errorf("changes = %+v", resp.Changes)
	}
	// Freeze activity, quarantined amounts and Scam Radar counters.
	if len(resp.Freezes) != 4 || resp.Quarantined == nil || resp.Quarantined.TotalNative != 12.75 || resp.Scam.ETH == nil ||
		resp.Scam.ETH.PoisonHits == nil || resp.Scam.TRON == nil {
		t.Errorf("freezes %d, quarantined %+v, scam %+v", len(resp.Freezes), resp.Quarantined, resp.Scam)
	}
	// Per asset: EURC has no quarantined amounts and no Scam Radar tracking.
	eurc := decodeStable[StableControlsResponse](t, stableGet(t, h.StablecoinControls, "/api/opendata/stablecoin/controls?asset=eurc"))
	if len(eurc.Assets) != 1 || len(eurc.Assets[0].Deployments) != 3 || eurc.Quarantined != nil || eurc.Scam.ETH != nil ||
		len(eurc.Freezes) != 1 || eurc.Freezes[0].CoverageNote == "" || len(eurc.Changes) != 0 {
		t.Errorf("eurc controls = %+v", eurc)
	}
	if rec := stableGet(t, h.StablecoinControls, "/api/opendata/stablecoin/controls?asset=usdt%27"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad asset = %d", rec.Code)
	}

	// Stale: no read for three intervals.
	clock = clock.Add(4 * stableControlsTick)
	h.cache = NewCache(time.Minute)
	late := decodeStable[StableControlsResponse](t, stableGet(t, h.StablecoinControls, "/api/opendata/stablecoin/controls?asset=usdc"))
	if f := controlsField(t, controlsDeployment(t, late, "usdc", "eth"), "owner"); !f.Stale {
		t.Errorf("owner after an hour without reads = %+v, want stale", f)
	}
}

// --stablecoin-verify reads every control field of every monitored
// deployment and fails on any read error.
func TestStableVerifyControls(t *testing.T) {
	stub, rpcs := newStableControlsStub(t)
	only := map[string]bool{}
	for _, p := range stableProviderRegistry {
		only[p.ID] = p.ID == "rpc_controls"
	}
	settings := StablecoinConfig{Providers: only}.settings()
	code, out := runVerifyForTest(t, settings, stableEndpoints{}, rpcs)
	if code != 0 || !strings.Contains(out, "PASS    rpc_controls") || strings.Count(out, "rpc_controls") != len(stableControlDeployments) {
		t.Fatalf("verify exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "USDC eth controls") || !strings.Contains(out, "paused=false") {
		t.Errorf("control lines lack detail:\n%s", out)
	}
	stub.set("eth-a", "usdc", "eth", "owner", zeroAddr)
	stub.set("eth-b", "usdc", "eth", "owner", zeroAddr)
	code, out = runVerifyForTest(t, settings, stableEndpoints{}, rpcs)
	if code == 0 || !strings.Contains(out, "owner: zero_address") {
		t.Errorf("zero owner: exit %d:\n%s", code, out)
	}
}
