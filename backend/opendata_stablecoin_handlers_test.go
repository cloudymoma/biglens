package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stableGet(t *testing.T, h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeStable[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

// newStableTestHandler builds an enabled stablecoin handler whose sampler
// reads the given stub endpoints at a fixed clock.
func newStableTestHandler(t *testing.T, cfg StablecoinConfig, ep stableEndpoints, rpcs func(string) []string, clock *time.Time) *APIHandler {
	t.Helper()
	full := &Config{Stablecoin: cfg}
	svc := newStablecoinService(full)
	if svc == nil {
		t.Fatal("service disabled")
	}
	now := func() time.Time { return *clock }
	svc.now = now
	svc.store = newTestRiskStore(t)
	svc.health = newStableHealth(svc.settings, now)
	svc.supply = newStableSupplySampler(svc.settings, svc.store, ep, rpcs, now)
	svc.supply.onFetch = svc.health.recordFetch
	return &APIHandler{cache: NewCache(time.Minute), stable: svc}
}

func TestStablecoinRoutesDisabled404(t *testing.T) {
	off := false
	h := &APIHandler{cache: NewCache(time.Minute), stable: newStablecoinService(&Config{Stablecoin: StablecoinConfig{Enabled: &off}})}
	routes := stablecoinRoutes(h)
	if len(routes) != 8 {
		t.Fatalf("routes = %d, want meta, live, overview, supply, peg, reserves, chains and controls", len(routes))
	}
	mux := http.NewServeMux()
	registerStablecoinRoutes(mux, h, func(f http.HandlerFunc) http.Handler { return f })
	for _, rt := range routes {
		if rec := stableGet(t, rt.Handler, rt.Pattern); rec.Code != http.StatusNotFound {
			t.Errorf("%s handler with the dashboard disabled = %d, want 404", rt.Pattern, rec.Code)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, rt.Pattern, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s via mux with the dashboard disabled = %d, want 404", rt.Pattern, rec.Code)
		}
	}
}

func TestStablecoinMeta(t *testing.T) {
	stub := newStableStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{Providers: map[string]bool{"binance": false}}, stub.endpoints(), nil, &clock)
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	rec := stableGet(t, h.StablecoinMeta, "/api/opendata/stablecoin/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var meta StableMeta
	meta = decodeStable[StableMeta](t, rec)
	if len(meta.Assets) != 3 || meta.Assets[2].ID != "eurc" || meta.Assets[2].Numeraire != "EUR" {
		t.Errorf("assets = %+v", meta.Assets)
	}
	if len(meta.Providers) != len(stableProviderRegistry) || len(meta.Samplers) != 6 {
		t.Fatalf("providers %d samplers %d", len(meta.Providers), len(meta.Samplers))
	}
	byKey := map[string]StableProviderHealth{}
	for _, p := range meta.Providers {
		byKey[p.Key] = p
	}
	if byKey["defillama"].Status != "ok" || byKey["binance"].Status != "disabled" || byKey["ecb"].Attribution == "" {
		t.Errorf("providers = %+v", meta.Providers)
	}
	if meta.Facts.Version == "" || meta.Facts.OverrideStatus != "none" {
		t.Errorf("facts = %+v", meta.Facts)
	}
	if meta.BigQuery.Jobs || meta.BigQuery.MonthlyBudgetGiB != 512 || meta.BigQuery.InitialDays != 90 || meta.BigQuery.Status == "" {
		t.Errorf("bigquery = %+v", meta.BigQuery)
	}
	if len(meta.CoverageNotes) == 0 {
		t.Error("coverage notes missing")
	}
	if !strings.Contains(strings.Join(meta.CoverageNotes, " "), "2 consecutive Tether updates") {
		t.Errorf("coverage notes do not describe the USDT check: %v", meta.CoverageNotes)
	}
	// S13 before M4: only the Tether sum check is evaluated.
	if meta.DataHealth.State != "normal" || len(meta.DataHealth.Checks) != 1 || meta.DataHealth.Checks[0] != "tether_sum" || len(meta.DataHealth.Reasons) != 0 {
		t.Errorf("data health = %+v", meta.DataHealth)
	}
}

// A Tether update whose chains don't sum to total_liabilities is served with
// a degraded flag: /meta data_health goes to watch and /live flags USDT.
func TestStablecoinTetherDegraded(t *testing.T) {
	stub := newStableStub(t)
	body := strings.Replace(string(readStableFixture(t, "tether_transparency.json")),
		`"total_liabilities": "`+fxTetherLiabilitiesStr+`"`, `"total_liabilities": "1`+fxTetherLiabilitiesStr+`"`, 1)
	drift := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer drift.Close()
	ep := stub.endpoints()
	ep.TetherURL = drift.URL
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{Providers: map[string]bool{"rpc_supply": false}}, ep, nil, &clock)
	cold := decodeStable[StableMeta](t, stableGet(t, h.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	if cold.DataHealth.State != "unknown" || len(cold.DataHealth.Checks) != 0 {
		t.Errorf("cold data health = %+v, want unknown with nothing evaluated", cold.DataHealth)
	}
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	h.cache = NewCache(time.Minute)
	meta := decodeStable[StableMeta](t, stableGet(t, h.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	if meta.DataHealth.State != "watch" || len(meta.DataHealth.Reasons) != 1 || meta.DataHealth.Reasons[0].Code != "tether_sum_mismatch" {
		t.Errorf("meta data health = %+v, want watch", meta.DataHealth)
	}
	live := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	for _, a := range live.Assets {
		if want := a.Asset == "usdt"; a.CirculatingDegraded != want || (want && a.CirculatingNative == nil) {
			t.Errorf("live %s = %+v, want degraded=%v with the value served", a.Asset, a, want)
		}
	}
	sup := decodeStable[StableSupplyResponse](t, stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=usdt"))
	if a := sup.Assets[0]; a.DataHealth.State != "watch" || a.Totals.IssuerCirculatingNative == nil {
		t.Errorf("supply usdt = %+v", a)
	}
}

func TestStablecoinLive(t *testing.T) {
	stub := newStableStub(t)
	rpcs, _, _ := stableRPCStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stub.endpoints(), rpcs, &clock)

	// Cold start: no sample yet, so everything is null and nothing is fetched.
	rec := stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("cold status %d", rec.Code)
	}
	cold := decodeStable[StableLive](t, rec)
	if !cold.WarmingUp || len(cold.Assets) != 3 || cold.Assets[0].CirculatingNative != nil || cold.MarketTotalUSD != nil {
		t.Errorf("cold live = %+v", cold)
	}
	if stub.count("/circle") != 0 || stub.count("/llama/stablecoins") != 0 {
		t.Fatal("/live called an upstream")
	}
	// Peg fields exist and stay null before the first peg sample.
	raw := rec.Body.String()
	for _, key := range []string{`"consensus":null`, `"dev_bps":null`, `"venues_used":null`, `"worst_signal":null`} {
		if !strings.Contains(raw, key) {
			t.Errorf("cold /live lacks %s: %s", key, raw)
		}
	}

	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	h.cache = NewCache(time.Minute)
	before := stub.count("/circle")
	live := decodeStable[StableLive](t, stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live"))
	if stub.count("/circle") != before {
		t.Error("/live fetched upstream data")
	}
	if live.WarmingUp || live.MarketTotalUSD == nil {
		t.Errorf("live after a sample = %+v", live)
	}
	var usdc, usdt StableLiveAsset
	for _, a := range live.Assets {
		switch a.Asset {
		case "usdc":
			usdc = a
		case "usdt":
			usdt = a
		}
	}
	approxPtr(t, "usdc circulating", usdc.CirculatingNative, fxCircleUSDCTotal, 1e-3)
	if usdc.CirculatingSource != "issuer" || usdc.CirculatingAsOf == "" {
		t.Errorf("usdc live = %+v", usdc)
	}
	// Empty database: the 24h figure comes from DefiLlama (cold start, §7.4).
	if usdc.NetIssuance24hSource != "aggregator" || usdc.NetIssuance24hNative == nil || usdc.NetIssuance24hPct == nil {
		t.Errorf("usdc 24h net issuance = %+v", usdc)
	}
	if usdt.CirculatingAsOf != "2026-10-16T23:30:00Z" {
		t.Errorf("usdt circulating as_of = %q, want the Tether update time", usdt.CirculatingAsOf)
	}
}

func TestStablecoinSupplyParams(t *testing.T) {
	stub := newStableStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{}, stub.endpoints(), nil, &clock)
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)

	one := decodeStable[StableSupplyResponse](t, stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=usdc"))
	if len(one.Assets) != 1 || one.Assets[0].Asset != "usdc" || one.Range != "90d" {
		t.Errorf("asset=usdc: %d assets, range %q", len(one.Assets), one.Range)
	}
	all := decodeStable[StableSupplyResponse](t, stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply"))
	if len(all.Assets) != 3 {
		t.Errorf("default asset: %d assets, want all 3", len(all.Assets))
	}
	for _, bad := range []string{"dai", "USDC", "usdt'%3BDROP%20TABLE%20stable_supply_daily", ""} {
		target := "/api/opendata/stablecoin/supply?asset=" + bad
		if bad == "" {
			target = "/api/opendata/stablecoin/supply?asset="
		}
		rec := stableGet(t, h.StablecoinSupply, target)
		if bad == "" {
			if rec.Code != http.StatusOK {
				t.Errorf("empty asset = %d, want 200 (default all)", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "DROP") || strings.Contains(rec.Body.String(), "dai") {
			t.Errorf("asset=%s = %d %q, want a generic 400", bad, rec.Code, rec.Body.String())
		}
	}
	// An unknown range falls back to the default; a valid one is echoed.
	if r := decodeStable[StableSupplyResponse](t, stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=eurc&range=10y")); r.Range != "90d" {
		t.Errorf("range=10y -> %q, want 90d", r.Range)
	}
	r := decodeStable[StableSupplyResponse](t, stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=eurc&range=all"))
	if r.Range != "all" || len(r.Assets[0].History) != 5 {
		t.Fatalf("range=all: %q, %d history rows (want the 5 chart days)", r.Range, len(r.Assets[0].History))
	}
	last := r.Assets[0].History[4]
	approxPtr(t, "eurc history native", last.AggregatorCirculatingNative, fxChartEURCLastNative, 1e-6)
	if last.AggregatorChangeNative == nil {
		t.Error("daily change missing between consecutive days")
	}
	if len(r.Assets[0].Chains) == 0 || len(r.Assets[0].NetIssuance) != 3 || len(r.Assets[0].Sources) != 3 {
		t.Errorf("eurc supply payload = %+v", r.Assets[0])
	}
}

// Upstream failures degrade to 200 with error badges and nulls (§9), never a
// 500 and never a substitute value.
func TestStablecoinSupplyUpstreamFailure(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer down.Close()
	ep := stableEndpoints{LlamaBase: down.URL, CircleURL: down.URL, TetherURL: down.URL, ECBBase: down.URL, TreasuryBase: down.URL}
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	cfg := StablecoinConfig{Providers: map[string]bool{"rpc_supply": false}}
	h := newStableTestHandler(t, cfg, ep, nil, &clock)
	runStableOnce(context.Background(), h.stable.supply, h.stable.health)
	rec := stableGet(t, h.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=usdt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with badges", rec.Code)
	}
	resp := decodeStable[StableSupplyResponse](t, rec)
	a := resp.Assets[0]
	if a.Totals.IssuerCirculatingNative != nil || a.Totals.AggregatorCirculatingNative != nil {
		t.Errorf("totals = %+v, want nulls", a.Totals)
	}
	for _, src := range a.Sources {
		want := "error"
		if src.ID == "rpc_supply" {
			want = "disabled"
		}
		if src.Status != want {
			t.Errorf("source %s = %+v, want %s", src.ID, src, want)
		}
	}
	for _, n := range a.NetIssuance {
		if n.NetNative != nil || n.Source != "" {
			t.Errorf("net issuance %s = %+v, want null", n.Window, n)
		}
	}
	live := stableGet(t, h.StablecoinLive, "/api/opendata/stablecoin/live")
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"circulating_native":null`) {
		t.Errorf("/live with every upstream down = %d %s", live.Code, live.Body.String())
	}
}

// The runner prunes snapshots past the retention window once a day (§8.5).
func TestStableRunOncePrunesDaily(t *testing.T) {
	stub := newStableStub(t)
	clock := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	h := newStableTestHandler(t, StablecoinConfig{Providers: map[string]bool{"rpc_supply": false}}, stub.endpoints(), nil, &clock)
	ctx := context.Background()
	old := clock.Add(-stableSnapshotRetention - time.Hour)
	if err := h.stable.store.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{
		{TS: old, Asset: "usdc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(1), FetchedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	runStableOnce(ctx, h.stable.supply, h.stable.health)
	if n := countStableRows(t, h.stable.store, `WHERE ts = ?`, stableTS(old)); n != 0 {
		t.Fatalf("snapshot older than 180 d still stored after the runner's first pass")
	}
	// Not again within 24 h.
	if err := h.stable.store.insertStableSupplySnapshots(ctx, []stableSupplySnapshot{
		{TS: old, Asset: "usdc", Chain: "eth", Source: "issuer", CirculatingNative: btcFloatPtr(1), FetchedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	runStableOnce(ctx, h.stable.supply, h.stable.health)
	if n := countStableRows(t, h.stable.store, `WHERE ts = ?`, stableTS(old)); n != 1 {
		t.Errorf("pruned again within 24 h (rows %d)", n)
	}
	clock = clock.Add(24 * time.Hour)
	runStableOnce(ctx, h.stable.supply, h.stable.health)
	if n := countStableRows(t, h.stable.store, `WHERE ts = ?`, stableTS(old)); n != 0 {
		t.Errorf("not pruned after 24 h (rows %d)", n)
	}
}

func TestStableEnsureSchemaOnceAndAlterCheck(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	if err := store.ensureStableSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE stable_rates_daily`); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureStableSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.upsertStableRatesDaily(ctx, []stableRateDaily{{Day: "2026-10-18", Series: stableSeriesECBEURUSD, Value: 1.08, FetchedAt: time.Now()}}); err == nil {
		t.Errorf("upsertStableRatesDaily succeeded after dropping table; ensureStableSchema re-ran DDL instead of running once")
	}

	// Non-duplicate-column ALTER TABLE errors must not be ignored.
	badStore := newTestRiskStore(t)
	if _, err := badStore.db.ExecContext(ctx, `CREATE VIEW stable_peg_daily AS SELECT 1 AS day`); err != nil {
		t.Fatal(err)
	}
	if err := badStore.ensureStableSchema(ctx); err == nil {
		t.Errorf("ensureStableSchema with view blocking ALTER TABLE = nil, want error")
	}
}

func TestStableHistoryErrorNotCached(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	th, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)
	if err := th.stable.store.upsertStableSupplyDaily(ctx, []stableSupplyDaily{
		{Day: "2026-10-18", Asset: "usdt", Source: "issuer", CirculatingNative: btcFloatPtr(120e9)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := th.stable.store.upsertStablePegDaily(ctx, []stablePegDaily{
		{Day: "2026-10-18", Asset: "usdt", Source: stablePegSourceConsensus, Close: btcFloatPtr(1.0)},
	}); err != nil {
		t.Fatal(err)
	}
	// Rename history tables so reads fail.
	for _, q := range []string{
		`ALTER TABLE stable_supply_daily RENAME TO stable_supply_daily_bak`,
		`ALTER TABLE stable_peg_daily RENAME TO stable_peg_daily_bak`,
	} {
		if _, err := th.stable.store.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	supBad := decodeStable[StableSupplyResponse](t, stableGet(t, th.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=usdt&range=90d"))
	if !supBad.HistoryError {
		t.Errorf("supply with broken DB: HistoryError = false, want true")
	}
	pegBad := decodeStable[StablePegResponse](t, stableGet(t, th.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt&range=90d"))
	if !pegBad.HistoryError {
		t.Errorf("peg with broken DB: HistoryError = false, want true")
	}
	// Restore tables without clearing h.cache: next request must succeed because degraded responses are not cached.
	for _, q := range []string{
		`ALTER TABLE stable_supply_daily_bak RENAME TO stable_supply_daily`,
		`ALTER TABLE stable_peg_daily_bak RENAME TO stable_peg_daily`,
	} {
		if _, err := th.stable.store.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	supOK := decodeStable[StableSupplyResponse](t, stableGet(t, th.StablecoinSupply, "/api/opendata/stablecoin/supply?asset=usdt&range=90d"))
	if supOK.HistoryError || len(supOK.Assets[0].History) == 0 {
		t.Errorf("supply after DB recovery = HistoryError %v len(History) %d, want false and >0 (degraded response was cached)",
			supOK.HistoryError, len(supOK.Assets[0].History))
	}
	pegOK := decodeStable[StablePegResponse](t, stableGet(t, th.StablecoinPeg, "/api/opendata/stablecoin/peg?asset=usdt&range=90d"))
	if pegOK.HistoryError || len(pegOK.Assets[0].DailyCloses) == 0 {
		t.Errorf("peg after DB recovery = HistoryError %v len(DailyCloses) %d, want false and >0 (degraded response was cached)",
			pegOK.HistoryError, len(pegOK.Assets[0].DailyCloses))
	}
}
