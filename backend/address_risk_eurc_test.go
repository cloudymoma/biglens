package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

// EURC joins the chain registry (stablecoin.md §8.3, Appendix A) with its
// intended side effects; Payment Check's asset list does not change.
func TestRegistryHasEURC(t *testing.T) {
	want := map[string]registryToken{
		"eth":  {Asset: "EURC", Network: "eth", Contract: "0x1abaea1f7c830bd89acc67ec4af516284b1bc33c", Decimals: 6, Tier: tierNative, Label: "EURC", FreezeSel: "fe575a87"},
		"base": {Asset: "EURC", Network: "base", Contract: "0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42", Decimals: 6, Tier: tierNative, Label: "EURC", FreezeSel: "fe575a87"},
		"sol":  {Asset: "EURC", Network: "sol", Contract: "HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr", Decimals: 6, Tier: tierNative, Label: "EURC", FreezeSel: "spl-account-state"},
	}
	got := map[string]registryToken{}
	for _, tok := range registryTokens {
		if tok.Asset == "EURC" {
			got[tok.Network] = tok
		}
	}
	if len(got) != len(want) {
		t.Fatalf("EURC registry rows = %+v, want eth/base/sol", got)
	}
	for net, w := range want {
		if got[net] != w {
			t.Errorf("EURC %s = %+v, want %+v", net, got[net], w)
		}
	}
	if _, ok := payAssets["EURC"]; ok || validPair("EURC", "eth") {
		t.Error("payAssets must not gain EURC (Payment Check scope stays USDT/USDC)")
	}
	if len(payAssets["USDC"]) != 5 || len(payAssets["USDT"]) != 6 {
		t.Errorf("payAssets changed: %v", payAssets)
	}
}

// The freeze-sync SQL must label every contract explicitly; the old
// IF(address = usdt, 'USDT', 'USDC') stored anything else as USDC.
func TestStablecoinSQLLabelsEachContract(t *testing.T) {
	if strings.Contains(stablecoinSQL, "'USDT', 'USDC')") {
		t.Fatal("stablecoinSQL still uses the two-way IF label")
	}
	caseBlock := regexp.MustCompile(`(?s)CASE address(.*?)END AS token`).FindStringSubmatch(stablecoinSQL)
	if caseBlock == nil {
		t.Fatal("stablecoinSQL has no CASE address … END AS token label")
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`WHEN '(0x[0-9a-f]{40})' THEN '([A-Z]+)'`).FindAllStringSubmatch(caseBlock[1], -1) {
		got[m[1]] = m[2]
	}
	want := map[string]string{usdtContract: "USDT", usdcContract: "USDC", eurcContract: "EURC"}
	if len(got) != len(want) {
		t.Fatalf("CASE branches = %v, want %v", got, want)
	}
	for c, label := range want {
		if got[c] != label {
			t.Errorf("contract %s labeled %q, want %q", c, got[c], label)
		}
	}
	if strings.Contains(caseBlock[1], "ELSE") {
		t.Error("the label CASE must not have an ELSE: an unknown contract has to fail decoding, not borrow a label")
	}
	if !strings.Contains(stablecoinSQL, "AND address IN ('"+usdtContract+"', '"+usdcContract+"', '"+eurcContract+"')") {
		t.Error("address filter must list USDT, USDC and EURC")
	}
	// USDC and EURC are FiatToken contracts: the target address is topics[1].
	if !strings.Contains(stablecoinSQL, "IF(address = '"+usdtContract+"', data, topics[SAFE_OFFSET(1)])") {
		t.Error("target address extraction changed")
	}
	if eurcContract != "0x1abaea1f7c830bd89acc67ec4af516284b1bc33c" {
		t.Errorf("eurcContract = %s", eurcContract)
	}
}

func TestDecodeStablecoinRowEURC(t *testing.T) {
	addr := "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	for _, action := range []string{"freeze", "unfreeze"} {
		e, err := decodeStablecoinRow(stablecoinRow{Token: "EURC", Action: action, Address: addr})
		if err != nil || e.Token != "EURC" {
			t.Errorf("EURC %s: %+v, %v", action, e, err)
		}
	}
	// No CASE branch matched (NULL label): the batch must fail, not be stored.
	if _, err := decodeStablecoinRow(stablecoinRow{Token: "", Action: "freeze", Address: addr}); err == nil {
		t.Error("empty token accepted")
	}
}

func eurcCoverage(t *testing.T, s *riskStore) string {
	t.Helper()
	cov, err := s.stablecoinTokenCoverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cov["EURC"]
}

func day(s string) civil.Date {
	d, err := civil.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// EURC coverage starts at the first EURC-aware batch that reaches the cursor
// and only extends through contiguous batches (§8.3 item 3).
func TestStablecoinTokenCoverage(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := newTestRiskStore(t)
	// A database synced before EURC was added (legacy insert: no window).
	if err := s.insertStablecoinEvents(ctx, nil, "2026-08-27", "2026-09-24", now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, s); got != "" {
		t.Fatalf("EURC coverage before any EURC-aware batch = %q, want empty", got)
	}
	win := func(start, end, newFrom, newCursor string) stablecoinWindow {
		return stablecoinWindow{Start: day(start), End: day(end), NewFrom: newFrom, NewCursor: newCursor}
	}
	// Incremental batch [09-25, 09-26): EURC history starts on 09-25.
	if err := s.insertStablecoinWindow(ctx, nil, win("2026-09-25", "2026-09-26", "", "2026-09-25"), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, s); got != "2026-09-25" {
		t.Fatalf("EURC coverage after the first incremental batch = %q, want 2026-09-25", got)
	}
	st, _ := s.getSyncState(ctx, stablecoinSourceID)
	if st.CoverageFrom != "2026-08-27" || st.Cursor != "2026-09-25" {
		t.Errorf("source watermarks = %s..%s, want 2026-08-27..2026-09-25", st.CoverageFrom, st.Cursor)
	}
	// The next incremental batch keeps the start.
	if err := s.insertStablecoinWindow(ctx, nil, win("2026-09-26", "2026-09-27", "", "2026-09-26"), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, s); got != "2026-09-25" {
		t.Errorf("EURC coverage after a second incremental batch = %q, want 2026-09-25", got)
	}
	// A backfill window older than the source coverage leaves a gap
	// (08-27..09-24 was scanned without EURC): no extension.
	if err := s.insertStablecoinWindow(ctx, nil, win("2026-01-01", "2026-08-27", "2026-01-01", ""), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, s); got != "2026-09-25" {
		t.Errorf("EURC coverage after a non-contiguous backfill = %q, want 2026-09-25", got)
	}
	// A re-scan that ends inside EURC coverage is contiguous and extends it.
	if err := s.insertStablecoinWindow(ctx, nil, win("2026-09-01", "2026-09-26", "2026-09-01", "2026-09-25"), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, s); got != "2026-09-01" {
		t.Errorf("EURC coverage after a contiguous re-scan = %q, want 2026-09-01", got)
	}

	// Fresh database: the cold start covers EURC from its first day.
	fresh := newTestRiskStore(t)
	if err := fresh.insertStablecoinWindow(ctx, nil, win("2026-08-27", "2026-09-26", "2026-08-27", "2026-09-25"), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, fresh); got != "2026-08-27" {
		t.Errorf("cold start EURC coverage = %q, want 2026-08-27", got)
	}

	// A backfill that does not reach the cursor cannot start EURC coverage.
	legacy := newTestRiskStore(t)
	if err := legacy.insertStablecoinEvents(ctx, nil, "2026-08-27", "2026-09-24", now); err != nil {
		t.Fatal(err)
	}
	if err := legacy.insertStablecoinWindow(ctx, nil, win("2026-01-01", "2026-08-27", "2026-01-01", ""), now); err != nil {
		t.Fatal(err)
	}
	if got := eurcCoverage(t, legacy); got != "" {
		t.Errorf("EURC coverage from an old-only backfill = %q, want empty", got)
	}
}

// The syncer passes its window, so EURC coverage starts at the first
// incremental window, and an EURC row is stored as EURC.
func TestStablecoinSyncStoresEURC(t *testing.T) {
	s, src, store := newTestStableSyncer(t, at(1, 0), 30)
	setCursor(t, store, syncToday.AddDays(-30), syncToday.AddDays(-2))
	src.events = []stablecoinEvent{{TxHash: "0xe1", LogIndex: 1, Token: "EURC", Action: "freeze",
		Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", BlockNumber: 100, BlockTime: syncToday.AddDays(-1).String() + "T10:00:00Z"}}
	s.syncOnce(context.Background())
	if got := eurcCoverage(t, store); got != syncToday.AddDays(-1).String() {
		t.Errorf("EURC coverage = %q, want %s (first EURC-aware window)", got, syncToday.AddDays(-1))
	}
	states, err := store.stablecoinStates(context.Background(), "0x098b716b8aaf21512996dc57eb0615e2383e2f96")
	if err != nil || len(states) != 1 || states[0].Token != "EURC" || states[0].Action != "freeze" {
		t.Fatalf("states = %+v, %v; want one EURC freeze", states, err)
	}
}

func TestOverviewCountsEURC(t *testing.T) {
	ctx := context.Background()
	s := newTestRiskStore(t)
	if err := s.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xa", LogIndex: 1, Token: "USDC", Action: "freeze", Address: "0x1111111111111111111111111111111111111111", BlockNumber: 1, BlockTime: "2026-09-10T00:00:00Z"},
	}, "2026-09-01", "2026-09-19", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.insertStablecoinWindow(ctx, []stablecoinEvent{
		{TxHash: "0xb", LogIndex: 1, Token: "EURC", Action: "freeze", Address: "0x2222222222222222222222222222222222222222", BlockNumber: 2, BlockTime: "2026-09-21T00:00:00Z"},
	}, stablecoinWindow{Start: day("2026-09-20"), End: day("2026-09-26"), NewCursor: "2026-09-25"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ov, err := s.buildOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.KPIs.USDCFrozenCount != 1 || ov.KPIs.EURCFrozenCount != 1 {
		t.Errorf("frozen counts USDC=%d EURC=%d, want 1 and 1 (EURC is not USDC)", ov.KPIs.USDCFrozenCount, ov.KPIs.EURCFrozenCount)
	}
	if ov.EURCCoverageFrom != "2026-09-20" {
		t.Errorf("eurc_coverage_from = %q, want 2026-09-20", ov.EURCCoverageFrom)
	}
	var eurcTotal int
	for _, p := range ov.Trend.Points {
		switch {
		case p.Bucket < "2026-09-20":
			// Days before EURC coverage are unknown, never zero.
			if p.EURCFreeze != nil {
				t.Errorf("%s: eurc_freeze = %d, want null before EURC coverage", p.Bucket, *p.EURCFreeze)
			}
		default:
			if p.EURCFreeze == nil {
				t.Errorf("%s: eurc_freeze = null inside EURC coverage", p.Bucket)
			} else {
				eurcTotal += *p.EURCFreeze
			}
		}
		if p.Bucket == "2026-09-21" && p.USDCFreeze != 0 {
			t.Errorf("EURC freeze counted as USDC on %s", p.Bucket)
		}
	}
	if eurcTotal != 1 {
		t.Errorf("EURC freezes in trend = %d, want 1", eurcTotal)
	}
	b, _ := json.Marshal(ov)
	for _, key := range []string{`"eurc_frozen_count":1`, `"eurc_coverage_from":"2026-09-20"`, `"eurc_freeze":null`, `"token":"EURC"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("overview JSON lacks %s", key)
		}
	}
}

func TestRiskSummaryEURCCoverage(t *testing.T) {
	base := riskSource{ID: "stablecoin", Status: "ok", CoverageFrom: "2017-11-28", Cursor: "2026-09-25"}
	cases := []struct {
		name     string
		coverage []riskTokenCoverage
		want     string
	}{
		{"later EURC start", []riskTokenCoverage{{Token: "EURC", CoverageFrom: "2026-09-20"}}, "No records in the 1 source checked. EURC freeze history from 2026-09-20."},
		{"EURC not synced", []riskTokenCoverage{{Token: "EURC"}}, "No records in the 1 source checked. EURC freeze history not synced yet."},
		{"same start", []riskTokenCoverage{{Token: "EURC", CoverageFrom: "2017-11-28"}}, "No records in the 1 source checked."},
		{"no token coverage", nil, "No records in the 1 source checked."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := base
			src.TokenCoverage = tc.coverage
			if got := buildRiskSummary(nil, []riskSource{src}).Text; got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// Address Risk's live check (no asset filter) now also reads EURC; Payment
// Check (asset filter) is unchanged (§8.3 side effect 1).
func TestCheckIssuerFreezeIncludesEURC(t *testing.T) {
	orig := riskSourceTimeout
	riskSourceTimeout = time.Second
	defer func() { riskSourceTimeout = orig }()
	const addr = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(strings.ToLower(string(b)), eurcContract) || strings.Contains(strings.ToLower(string(b)), "0x60a3e35cc302bfa44cb288bc5a4f316fdb1adb42") {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleTrue + `"}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
	}))
	defer srv.Close()

	states, code := checkIssuerFreeze(context.Background(), "eth", addr, "", []string{srv.URL})
	if code != "" || len(states) != 3 || states[2].Token != "EURC" || !states[2].Frozen || states[0].Frozen || states[1].Frozen {
		t.Fatalf("eth live check = %+v, %q; want USDT, USDC, EURC (frozen)", states, code)
	}
	states, code = checkIssuerFreeze(context.Background(), "base", addr, "", []string{srv.URL})
	if code != "" || len(states) != 2 || states[1].Token != "EURC" || !states[1].Frozen {
		t.Fatalf("base live check = %+v, %q; want USDC and EURC", states, code)
	}
	states, code = checkIssuerFreeze(context.Background(), "eth", addr, "USDC", []string{srv.URL})
	if code != "" || len(states) != 1 || states[0].Token != "USDC" {
		t.Fatalf("asset-filtered check = %+v, %q; want USDC only", states, code)
	}
	// The local-history dedup also works per token for EURC.
	clues := issuerFreezeClues("eth", []issuerFreezeState{{Token: "EURC", Contract: eurcContract, Frozen: true}},
		[]stablecoinState{{Token: "EURC", Action: "freeze"}}, "2026-10-19T00:00:00Z")
	if len(clues) != 0 {
		t.Errorf("EURC live freeze already in local history must not be duplicated: %+v", clues)
	}
}

func TestCheckIssuerFreezeSolanaIncludesEURC(t *testing.T) {
	const holder = "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN"
	seen := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		var filter map[string]string
		if len(req.Params) > 1 {
			_ = json.Unmarshal(req.Params[1], &filter)
		}
		seen <- filter["mint"]
		state := "initialized"
		if filter["mint"] == "HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr" {
			state = "frozen"
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":[{"pubkey":"x","account":{"data":{"parsed":{"info":{"state":"` + state + `"}}}}}]}}`))
	}))
	defer srv.Close()
	states, code := checkIssuerFreeze(context.Background(), "sol", holder, "", []string{srv.URL})
	if code != "" || len(states) != 3 || states[2].Token != "EURC" || !states[2].Frozen {
		t.Fatalf("sol live check = %+v, %q; want USDT, USDC, EURC (frozen)", states, code)
	}
	close(seen)
	mints := map[string]bool{}
	for m := range seen {
		mints[m] = true
	}
	if !mints["HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr"] {
		t.Errorf("EURC mint not queried: %v", mints)
	}
}

// Crypto Pulse's supply matrix loops over registryTokens, so it gains EURC
// rows (§8.3 side effect 2).
func TestSupplyMatrixIncludesEURC(t *testing.T) {
	evm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x000000000000000000000000000000000000000000000000000009184e72a000"}`))
	}))
	defer evm.Close()
	sol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":{"data":{"parsed":{"info":{"decimals":6,"supply":"5000000000000000"}}}}}}`))
	}))
	defer sol.Close()
	rs := newAddressRiskService(nil, []string{evm.URL})
	rs.chainRPCs = map[string][]string{"eth": {evm.URL}, "arb": {evm.URL}, "op": {evm.URL}, "base": {evm.URL}, "sol": {sol.URL}}
	h := &APIHandler{cache: NewCache(time.Minute), risk: rs}
	rows, _ := h.refreshSupplyMatrix(context.Background(), nil, time.Now())
	nets := map[string]bool{}
	for _, r := range rows {
		if r.Asset == "EURC" {
			nets[r.Network] = true
			if r.Network != "tron" && r.Error == "" && r.Supply <= 0 {
				t.Errorf("EURC %s supply = %v", r.Network, r.Supply)
			}
		}
	}
	if !nets["eth"] || !nets["base"] || !nets["sol"] || len(nets) != 3 {
		t.Errorf("EURC supply rows on %v, want eth/base/sol", nets)
	}
}
