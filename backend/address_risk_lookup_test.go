package main

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var riskNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func TestListSourceStatus(t *testing.T) {
	tests := []struct {
		name string
		st   syncState
		want string
	}{
		{"never synced", syncState{}, "empty"},
		{"fresh", syncState{LastOKAt: "2026-09-26T06:00:00Z", RowCount: 124}, "ok"},
		{"11h59m old is ok", syncState{LastOKAt: "2026-09-26T00:01:00Z", RowCount: 124}, "ok"},
		{"12h01m old is stale", syncState{LastOKAt: "2026-09-25T23:59:00Z", RowCount: 124}, "stale"},
		{"rejected shrink pending is stale", syncState{LastOKAt: "2026-09-26T06:00:00Z", RowCount: 652, PendingCount: 3}, "stale"},
		{"rejected on first sync: stale beats empty", syncState{PendingCount: 1}, "stale"},
		{"fetched but zero rows", syncState{LastOKAt: "2026-09-26T06:00:00Z"}, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listSourceStatus(tt.st, riskNow); got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListClues(t *testing.T) {
	states := map[string]syncState{
		"ofac":         {LastOKAt: "2026-09-26T06:00:00Z"},
		"mew_darklist": {LastOKAt: "2026-09-26T07:00:00Z"},
	}
	hits := []listHit{
		{Source: "mew_darklist", Label: "phishing site a", ListedAt: "2017-07-18"},
		{Source: "ofac", Label: "tagged USDT"},
	}
	got := listClues(hits, states)
	if len(got) != 2 {
		t.Fatalf("clues = %+v", got)
	}
	o, m := got[0], got[1]
	if o.Severity != sevCritical || o.Code != "ofac_listed" || o.Detail != "tagged USDT" || o.ObservedAt != nil ||
		o.AsOf != "2026-09-26T06:00:00Z" || o.RefURL != "https://sanctionssearch.ofac.treas.gov/" {
		t.Errorf("ofac clue = %+v", o)
	}
	if m.Severity != sevWarning || m.Code != "mew_darklist" || m.Detail != "phishing site a" ||
		m.ObservedAt == nil || *m.ObservedAt != "2017-07-18" || !strings.Contains(m.Title, "frozen since 2020-11") {
		t.Errorf("mew clue = %+v", m)
	}
}

func TestBuildRiskSummary(t *testing.T) {
	ok := func(id string) riskSource { return riskSource{ID: id, Status: "ok"} }
	crit := riskClue{Severity: sevCritical}
	warn := riskClue{Severity: sevWarning}
	tests := []struct {
		name        string
		clues       []riskClue
		sources     []riskSource
		wantText    string
		wantChecked int
		wantFailed  []string
	}{
		{"hits, all ok",
			[]riskClue{crit, crit, warn}, []riskSource{ok("ofac"), ok("mew_darklist"), ok("chainalysis_oracle"), ok("goplus")},
			"3 clues found — 2 critical · 1 warning · 0 association · 0 info", 4, nil},
		{"one hit with a failed source",
			[]riskClue{crit}, []riskSource{ok("ofac"), ok("mew_darklist"), ok("chainalysis_oracle"), {ID: "goplus", Status: "error", Error: "timeout"}},
			"1 clue found — 1 critical · 0 warning · 0 association · 0 info; 1 source could not be fully checked (see Sources)", 3, []string{"goplus"}},
		{"nothing, all ok (template 2)",
			nil, []riskSource{ok("ofac"), ok("mew_darklist"), ok("chainalysis_oracle"), ok("goplus")},
			"No records in the 4 sources checked.", 4, nil},
		{"nothing, goplus timeout (template 3)",
			nil, []riskSource{ok("ofac"), ok("mew_darklist"), ok("chainalysis_oracle"), {ID: "goplus", Status: "error", Error: "timeout"}},
			"Nothing found in 3 sources that completed; 1 could not be fully checked (see Sources).", 3, []string{"goplus"}},
		{"nothing, stale list counts as failed",
			nil, []riskSource{{ID: "ofac", Status: "stale"}, ok("mew_darklist"), ok("chainalysis_oracle"), ok("goplus")},
			"Nothing found in 3 sources that completed; 1 could not be fully checked (see Sources).", 3, []string{"ofac"}},
		{"nothing, never synced list is incomplete",
			nil, []riskSource{{ID: "ofac", Status: "empty"}, ok("mew_darklist"), ok("chainalysis_oracle"), ok("goplus")},
			"Nothing found in 3 sources that completed; 1 could not be fully checked (see Sources).", 3, nil},
		{"db unavailable (template 4)",
			nil, []riskSource{{ID: "ofac", Status: "error", Error: "unavailable"}, {ID: "mew_darklist", Status: "error", Error: "unavailable"}, ok("chainalysis_oracle"), ok("goplus")},
			"Nothing found in 2 sources that completed; 2 could not be fully checked (see Sources). Local lists are unavailable on this server.", 2, []string{"ofac", "mew_darklist"}},
		{"skipped source is not a failure",
			nil, []riskSource{ok("ofac"), {ID: "etherscan", Status: "not_configured"}},
			"No records in the 1 source checked. Not checked: Etherscan association analysis (no API key).", 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRiskSummary(tt.clues, tt.sources)
			if got.Text != tt.wantText {
				t.Errorf("text =\n  %q\nwant\n  %q", got.Text, tt.wantText)
			}
			if len(got.Checked) != tt.wantChecked {
				t.Errorf("checked = %v", got.Checked)
			}
			if strings.Join(got.Failed, ",") != strings.Join(tt.wantFailed, ",") {
				t.Errorf("failed = %v, want %v", got.Failed, tt.wantFailed)
			}
		})
	}
}

// Spec §1: backend text never claims an address is safe. Word-boundary
// match, so "unverified" would pass but "verified" would not.
func TestRiskCopyHasNoBannedWords(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(safe|clean|secure|verified|no risk|low risk)\b`)
	var texts []string
	for _, r := range goplusRules {
		texts = append(texts, r.Title)
	}
	texts = append(texts, riskTitleOFAC, riskTitleMEW, riskTitleOracle,
		riskTitleFrozenFmt, riskTitleUnfrozenFmt, riskDestroyedFmt, riskCoverageFmt)
	for _, srcs := range [][]riskSource{
		{{ID: "ofac", Status: "ok"}},
		{{ID: "ofac", Status: "error", Error: "unavailable"}},
		{{ID: "ofac", Status: "ok"}, {ID: "etherscan", Status: "not_configured"}},
		{{ID: "stablecoin", Status: "partial", CoverageFrom: "2026-08-27", Cursor: "2026-09-25"}},
	} {
		texts = append(texts, buildRiskSummary(nil, srcs).Text, buildRiskSummary([]riskClue{{Severity: sevInfo}}, srcs).Text)
	}
	texts = append(texts, strings.Replace(riskDisclaimer, "does not mean an address is safe", "", 1))
	for _, s := range texts {
		if m := banned.FindString(s); m != "" {
			t.Errorf("banned word %q in %q", m, s)
		}
	}
	if !strings.Contains(riskDisclaimer, "does not mean an address is safe") {
		t.Error("disclaimer must keep its single negated 'safe'")
	}
}

type fakeUpstreams struct {
	oracle, goplus *httptest.Server
	goplusCalls    atomic.Int32
}

func newFakeUpstreams(t *testing.T, oracleBody, goplusBody string, goplusDelay time.Duration) *fakeUpstreams {
	t.Helper()
	f := &fakeUpstreams{}
	f.oracle = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if !strings.Contains(string(b), riskOracleContract) {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
			return
		}
		w.Write([]byte(oracleBody))
	}))
	f.goplus = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.goplusCalls.Add(1)
		time.Sleep(goplusDelay)
		w.Write([]byte(goplusBody))
	}))
	origG, origBS, origT := goplusBaseURL, blockscoutBaseURL, riskSourceTimeout
	goplusBaseURL = f.goplus.URL + "/api/v1/address_security/"
	blockscoutBaseURL = ""
	riskSourceTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		f.oracle.Close()
		f.goplus.Close()
		goplusBaseURL, blockscoutBaseURL, riskSourceTimeout = origG, origBS, origT
	})
	return f
}

const ronin = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

// sourceByID finds a source regardless of its position in the response.
func sourceByID(t *testing.T, srcs []riskSource, id string) riskSource {
	t.Helper()
	for _, s := range srcs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("source %q missing from %+v", id, srcs)
	return riskSource{}
}

// markStablecoinSynced gives the stablecoin and scam_lookalikes sources
// coverage up to yesterday, so tests about other sources see them as ok.
func markStablecoinSynced(t *testing.T, store *riskStore, events ...stablecoinEvent) {
	t.Helper()
	cursor := riskNow.AddDate(0, 0, -1).Format("2006-01-02")
	if err := store.insertStablecoinEvents(context.Background(), events, stablecoinFirstDay, cursor, riskNow); err != nil {
		t.Fatal(err)
	}
	if err := store.applyScamBatch(context.Background(), scamEthSourceID, "eth", nil, "2026-08-27", cursor, riskNow); err != nil && err != errAlreadyApplied {
		t.Fatal(err)
	}
}

func TestServiceLookupRoninLikeHit(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleTrue+`"}`,
		`{"code":1,"result":{"sanctioned":"1","stealing_attack":"1","data_source":"SlowMist,BlockSec"}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow.Add(-time.Hour))
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow.Add(-time.Hour))
	markStablecoinSynced(t, store, stablecoinEvent{TxHash: "0xfreeze", LogIndex: 7, Token: "USDT", Action: "freeze",
		Address: ronin, BlockNumber: 14580000, BlockTime: "2026-04-14T18:00:00Z"})
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }

	res := svc.lookup(ctx, "eth", ronin)
	var codes []string
	for _, c := range res.Clues {
		codes = append(codes, c.Severity+":"+c.Code)
	}
	// Order: severity, then source order ofac, mew, stablecoin, scam_lookalikes, issuer_freeze, oracle, goplus.
	want := "critical:ofac_listed,critical:stablecoin_frozen,critical:oracle_sanctioned,critical:goplus_flag,warning:goplus_flag"
	if strings.Join(codes, ",") != want {
		t.Errorf("clues = %v, want %s", codes, want)
	}
	if res.Summary.Counts.Critical != 4 || res.Summary.Counts.Warning != 1 || res.hasError() {
		t.Errorf("summary = %+v", res.Summary)
	}
	var ids []string
	for _, s := range res.Sources {
		ids = append(ids, s.ID+"="+s.Status)
	}
	if strings.Join(ids, ",") != "ofac=ok,mew_darklist=ok,stablecoin=ok,scam_lookalikes=ok,issuer_freeze=ok,chainalysis_oracle=ok,goplus=ok,etherscan=not_configured" {
		t.Errorf("sources = %v", ids)
	}
	if res.Disclaimer != riskDisclaimer || res.QueriedAt != "2026-09-26T12:00:00Z" || res.Chain != "eth" {
		t.Errorf("result metadata = %+v", res)
	}
}

func TestServiceLookupStoreUnavailableStillRunsLiveSources(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{"sanctioned":"0"}}`, 0)
	svc := newAddressRiskService(nil, []string{up.oracle.URL})
	res := svc.lookup(context.Background(), "eth", ronin)
	if !res.hasError() || up.goplusCalls.Load() != 1 {
		t.Fatalf("hasError = %v, goplus calls = %d", res.hasError(), up.goplusCalls.Load())
	}
	for _, s := range res.Sources[:4] {
		if s.Status != "error" || s.Error != "unavailable" {
			t.Errorf("local source %+v", s)
		}
	}
	if !strings.HasSuffix(res.Summary.Text, "Local lists are unavailable on this server.") {
		t.Errorf("text = %q", res.Summary.Text)
	}
}

func TestServiceLookupGoPlusTimeout(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, time.Second)
	store := newTestRiskStore(t)
	store.replaceList(context.Background(), "ofac", []listEntry{{Address: "0x1111111111111111111111111111111111111111"}}, "h", riskNow)
	store.replaceList(context.Background(), "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	res := svc.lookup(context.Background(), "eth", ronin)
	gp := sourceByID(t, res.Sources, "goplus")
	if gp.ID != "goplus" || gp.Status != "error" || gp.Error != "timeout" || !res.hasError() {
		t.Errorf("goplus source = %+v", gp)
	}
	if res.Summary.Text != "Nothing found in 6 sources that completed; 1 could not be fully checked (see Sources)." {
		t.Errorf("text = %q", res.Summary.Text)
	}
}

func TestHostsOf(t *testing.T) {
	got := hostsOf("https://ethereum-rpc.publicnode.com", "https://eth.drpc.org/path?key=SECRET", "::bad")
	if strings.Join(got, ",") != "ethereum-rpc.publicnode.com,eth.drpc.org" {
		t.Errorf("hosts = %v (must be hostnames only, no path/query)", got)
	}
}

func TestStablecoinSourceStatus(t *testing.T) {
	// riskNow is 2026-09-26: today-2 = 2026-09-24.
	tests := []struct {
		name string
		st   syncState
		want string
	}{
		{"never synced", syncState{}, "empty"},
		{"full history, cursor today-2", syncState{CoverageFrom: "2017-11-28", Cursor: "2026-09-24"}, "ok"},
		{"cursor today-3 is stale", syncState{CoverageFrom: "2017-11-28", Cursor: "2026-09-23"}, "stale"},
		{"coverage from 2017-11-29 is partial", syncState{CoverageFrom: "2017-11-29", Cursor: "2026-09-25"}, "partial"},
		{"30-day cold start is partial", syncState{CoverageFrom: "2026-08-27", Cursor: "2026-09-25"}, "partial"},
		{"stale beats partial", syncState{CoverageFrom: "2026-08-27", Cursor: "2026-09-20"}, "stale"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stablecoinSourceStatus(tt.st, riskNow); got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStablecoinClues(t *testing.T) {
	destroyed := map[string]*big.Int{
		"USDT": big.NewInt(1642752780747),
		"USDC": big.NewInt(500000000),
	}
	states := []stablecoinState{
		{Token: "USDC", Action: "unfreeze", TxHash: "0xun", BlockTime: "2025-03-22T10:00:00Z"},
		{Token: "USDT", Action: "destroy", TxHash: "0xdes", BlockTime: "2026-09-20T08:00:00Z"},
	}
	got := stablecoinClues(states, destroyed, "2026-09-25")
	if len(got) != 2 {
		t.Fatalf("clues = %+v", got)
	}
	un, fr := got[0], got[1]
	if un.Severity != sevWarning || un.Code != "stablecoin_unfrozen" || un.Token != "USDC" ||
		un.Title != "Previously frozen by USDC contract (since unfrozen)" ||
		un.Detail != "Tether destroyed 500.00 USDT" || *un.ObservedAt != "2025-03-22T10:00:00Z" {
		t.Errorf("unfrozen with prior destroy clue = %+v", un)
	}
	// Unfrozen without prior destroy stays sevInfo with empty detail.
	plainUn := stablecoinClues([]stablecoinState{{Token: "USDC", Action: "unfreeze", TxHash: "0xun", BlockTime: "2025-03-22T10:00:00Z"}}, nil, "2026-09-25")
	if len(plainUn) != 1 || plainUn[0].Severity != sevInfo || plainUn[0].Detail != "" {
		t.Errorf("plain unfrozen clue = %+v", plainUn)
	}
	// Destroy-only history still means frozen (destroyBlackFunds requires the blacklist).
	if fr.Severity != sevCritical || fr.Code != "stablecoin_frozen" || fr.Title != "Frozen by USDT contract" ||
		fr.Detail != "Tether destroyed 1642752.78 USDT" || fr.RefURL != "https://etherscan.io/tx/0xdes" ||
		fr.AsOf != "2026-09-25T23:59:59Z" {
		t.Errorf("frozen clue = %+v", fr)
	}
}

func TestBuildRiskSummaryCoverageLine(t *testing.T) {
	partial := riskSource{ID: "stablecoin", Status: "partial", CoverageFrom: "2026-08-27", Cursor: "2026-09-25"}
	ok := riskSource{ID: "ofac", Status: "ok"}
	tests := []struct {
		name  string
		clues []riskClue
		srcs  []riskSource
		want  string
	}{
		{"template 1", []riskClue{{Severity: sevCritical}}, []riskSource{ok, partial},
			"1 clue found — 1 critical · 0 warning · 0 association · 0 info; 1 source could not be fully checked (see Sources). Freeze history covers 2026-08-27–2026-09-25 only."},
		{"template 3", nil, []riskSource{ok, partial},
			"Nothing found in 1 source that completed; 1 could not be fully checked (see Sources). Freeze history covers 2026-08-27–2026-09-25 only."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRiskSummary(tt.clues, tt.srcs)
			if got.Text != tt.want {
				t.Errorf("text =\n  %q\nwant\n  %q", got.Text, tt.want)
			}
			if strings.Join(got.Incomplete, ",") != "stablecoin" {
				t.Errorf("incomplete = %v", got.Incomplete)
			}
		})
	}
}

// After only the 30-day cold start, a 2022 freeze is not local: the result
// must say so instead of reading as "nothing found" (spec §7.2), and it may
// still be cached (partial is a lasting state, unlike empty).
func TestServiceLookupPartialCoverage(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: "0x1111111111111111111111111111111111111111"}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	store.insertStablecoinEvents(ctx, nil, "2026-08-27", "2026-09-25", riskNow)
	_ = store.applyScamBatch(ctx, scamEthSourceID, "eth", nil, "2026-08-27", "2026-09-25", riskNow)
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	res := svc.lookup(ctx, "eth", ronin)
	sc := sourceByID(t, res.Sources, "stablecoin")
	if sc.Status != "partial" || sc.CoverageFrom != "2026-08-27" || sc.Cursor != "2026-09-25" || sc.SendsAddress {
		t.Errorf("stablecoin source = %+v", sc)
	}
	if !strings.HasSuffix(res.Summary.Text, "Freeze history covers 2026-08-27–2026-09-25 only.") {
		t.Errorf("text = %q", res.Summary.Text)
	}
	if !res.cacheable() {
		t.Error("partial coverage is a lasting state and must be cacheable")
	}
}

func TestBlockscoutFallbackAndLiveCounterpartyScreening(t *testing.T) {
	const (
		target     = "0x1111111111111111111111111111111111111111"
		funderAddr = "0x5555555555555555555555555555555555555555"
	)
	oracleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"` + oracleFalse + `"}`))
	}))
	defer oracleSrv.Close()

	// GoPlus returns clean for target, but flags funderAddr as phishing_activities!
	goplusSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, funderAddr) {
			w.Write([]byte(`{"code":1,"result":{"phishing_activities":"1","data_source":"SlowMist"}}`))
			return
		}
		w.Write([]byte(`{"code":1,"result":{}}`))
	}))
	defer goplusSrv.Close()

	// Blockscout handles both /api/v2/addresses/{addr} (scam tag check) and /api (1-hop txlist fallback).
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/addresses/"+target:
			w.Write([]byte(`{"is_scam":true,"reputation":"scam","public_tags":[{"display_name":"Fake Phishing #99"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v2/addresses/"):
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
		case r.URL.Path == "/api":
			switch r.URL.Query().Get("action") {
			case "txlist":
				w.Write([]byte(`{"status":"1","message":"OK","result":[{"hash":"0xfund","from":"` + funderAddr + `","to":"` + target + `","value":"1000000000000000000","isError":"0","timeStamp":"1700000000"}]}`))
			default:
				w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
			}
		}
	}))
	defer bsSrv.Close()

	origG, origT := goplusBaseURL, riskSourceTimeout
	goplusBaseURL = goplusSrv.URL + "/api/v1/address_security/"
	riskSourceTimeout = time.Second
	defer func() { goplusBaseURL, riskSourceTimeout = origG, origT }()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)

	// No Etherscan API key configured: should fall back to Blockscout!
	svc := newAddressRiskService(store, []string{oracleSrv.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	res := svc.lookup(ctx, "eth", target)
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "ok" || len(es.Hosts) == 0 {
		t.Fatalf("etherscan source (via Blockscout fallback) = %+v", es)
	}
	bs := sourceByID(t, res.Sources, "blockscout")
	if bs.Status != "ok" || len(bs.Hosts) == 0 {
		t.Fatalf("blockscout source = %+v, want status=ok", bs)
	}
	if res.AssociationScope == nil || res.AssociationScope.CounterpartiesScreened != 1 || res.AssociationScope.CounterpartyErrors != 0 {
		t.Fatalf("AssociationScope = %+v, want CounterpartiesScreened=1 CounterpartyErrors=0", res.AssociationScope)
	}
	if !res.cacheable() {
		t.Error("a lookup whose counterparty screening fully succeeded must be cacheable")
	}

	var foundScam, foundFunderAssoc bool
	for _, c := range res.Clues {
		if c.Code == "blockscout_scam" && c.Severity == sevWarning {
			foundScam = true
		}
		if c.Code == "association" && c.Association != nil && c.Association.Counterparty == funderAddr {
			foundFunderAssoc = true
			if !strings.Contains(c.Detail, "First ETH funder") || !strings.Contains(c.Detail, "phishing") {
				t.Errorf("expected First ETH funder and phishing in Detail %q (sources=%v)", c.Detail, c.Association.CounterpartySources)
			}
		}
	}
	if !foundScam {
		t.Errorf("missing blockscout_scam clue in %+v", res.Clues)
	}
	if !foundFunderAssoc {
		t.Errorf("missing live-screened first-funder association clue in %+v", res.Clues)
	}
}

func TestCounterpartyScreenErrorsAreNotCached(t *testing.T) {
	const (
		target     = "0x1111111111111111111111111111111111111111"
		funderAddr = "0x5555555555555555555555555555555555555555"
	)
	oracleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"` + oracleFalse + `"}`))
	}))
	defer oracleSrv.Close()
	// The target's own checks succeed; only screening the funder fails.
	goplusSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, funderAddr) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"code":1,"result":{}}`))
	}))
	defer goplusSrv.Close()
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/addresses/"+funderAddr:
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/api/v2/addresses/"):
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
		case r.URL.Query().Get("action") == "txlist":
			w.Write([]byte(`{"status":"1","message":"OK","result":[{"hash":"0xfund","from":"` + funderAddr + `","to":"` + target + `","value":"1000000000000000000","isError":"0","timeStamp":"1700000000"}]}`))
		default:
			w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
		}
	}))
	defer bsSrv.Close()

	origG, origT := goplusBaseURL, riskSourceTimeout
	goplusBaseURL = goplusSrv.URL + "/api/v1/address_security/"
	riskSourceTimeout = time.Second
	defer func() { goplusBaseURL, riskSourceTimeout = origG, origT }()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)
	svc := newAddressRiskService(store, []string{oracleSrv.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	res := svc.lookup(ctx, "eth", target)
	if len(res.Summary.Failed) != 0 {
		t.Fatalf("Summary.Failed = %v, want empty (the target's own sources are healthy)", res.Summary.Failed)
	}
	if res.AssociationScope == nil || res.AssociationScope.CounterpartiesScreened != 1 || res.AssociationScope.CounterpartyErrors != 2 {
		t.Fatalf("AssociationScope = %+v, want 1 screened with 2 errors (GoPlus + Blockscout)", res.AssociationScope)
	}
	if res.cacheable() {
		t.Error("a lookup with failed counterparty checks must not be cached")
	}
}

func TestBlockscoutAddressErrorSurfacedInSources(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/addresses/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
	}))
	defer bsSrv.Close()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	res := svc.lookup(ctx, "eth", "0x1111111111111111111111111111111111111111")
	bs := sourceByID(t, res.Sources, "blockscout")
	if bs.Status != "error" || bs.Error != "upstream_http_500" {
		t.Fatalf("blockscout source = %+v, want status=error error=upstream_http_500", bs)
	}
	if len(res.Summary.Failed) != 1 || res.Summary.Failed[0] != "blockscout" {
		t.Fatalf("Summary.Failed = %v, want [blockscout]", res.Summary.Failed)
	}
}

func TestBlockscoutTagRegexRejectsFalsePositives(t *testing.T) {
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Contract name "SanctionsList" and benign tags "ETHGlobal Hackathon" / "OFAC compliance oracle"
		// must NOT produce false-positive warnings.
		w.Write([]byte(`{
			"is_scam": false,
			"reputation": "ok",
			"name": "SanctionsList",
			"public_tags": [{"display_name": "ETHGlobal Hackathon"}, {"display_name": "OFAC compliance oracle"}],
			"metadata": {"tags": [{"name": "SanctionsList", "tagType": "name"}]}
		}`))
	}))
	defer bsSrv.Close()

	clues, code := checkBlockscoutAddress(context.Background(), bsSrv.URL, "0x40c57923924b5c5c5455c48d93317139addac8fb", riskNow)
	if code != "" || len(clues) != 0 {
		t.Fatalf("clues = %+v, code = %q, want empty clues and empty code", clues, code)
	}
}

func TestBlockscoutFallbackOnEtherscanTimeout(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	// Etherscan hangs past riskSourceTimeout.
	withEtherscan(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(`{"status":"1","message":"OK","result":[]}`))
	})

	// Blockscout responds fast and healthy.
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/addresses/") {
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
			return
		}
		w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
	}))
	defer bsSrv.Close()

	origT := riskSourceTimeout
	riskSourceTimeout = 40 * time.Millisecond
	defer func() { riskSourceTimeout = origT }()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	res := svc.lookupKey(ctx, "eth", "0x1111111111111111111111111111111111111111", "slow-key")
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "ok" {
		t.Fatalf("etherscan source after timeout with Blockscout fallback = %+v, want status=ok", es)
	}
	if len(res.Summary.Failed) != 0 {
		t.Fatalf("Summary.Failed = %v, want empty", res.Summary.Failed)
	}
}

func TestRiskSourcesForChain(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/addresses/") {
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
			return
		}
		w.Write([]byte(`{"status":"0","message":"No transactions found","result":[]}`))
	}))
	defer bsSrv.Close()
	tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"result":true},"constant_result":["` + oracleFalse[2:] + `"]}`))
	}))
	defer tronSrv.Close()

	origTron := tronGridBaseURL
	tronGridBaseURL = tronSrv.URL
	defer func() { tronGridBaseURL = origTron }()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{
		"eth":  bsSrv.URL,
		"arb":  bsSrv.URL,
		"op":   bsSrv.URL,
		"base": bsSrv.URL,
	}

	tests := []struct {
		chain string
		addr  string
		want  string
	}{
		{"eth", "0x1111111111111111111111111111111111111111", "ofac,mew_darklist,stablecoin,scam_lookalikes,issuer_freeze,chainalysis_oracle,goplus,blockscout,etherscan"},
		{"arb", "0x1111111111111111111111111111111111111111", "ofac,mew_darklist,scam_lookalikes,issuer_freeze,chainalysis_oracle,goplus,blockscout"},
		{"op", "0x1111111111111111111111111111111111111111", "ofac,mew_darklist,scam_lookalikes,issuer_freeze,chainalysis_oracle,goplus,blockscout"},
		{"base", "0x1111111111111111111111111111111111111111", "ofac,mew_darklist,scam_lookalikes,issuer_freeze,goplus,blockscout"},
		{"tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "ofac,tron_stablecoin,scam_lookalikes,issuer_freeze,goplus"},
		{"btc", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", "ofac"},
	}
	for _, tt := range tests {
		t.Run(tt.chain, func(t *testing.T) {
			if got := strings.Join(riskSourcesFor(tt.chain), ","); got != tt.want {
				t.Fatalf("riskSourcesFor(%q) = %s, want %s", tt.chain, got, tt.want)
			}
			res := svc.lookup(ctx, tt.chain, tt.addr)
			var ids []string
			for _, s := range res.Sources {
				ids = append(ids, s.ID)
			}
			if got := strings.Join(ids, ","); got != tt.want {
				t.Errorf("lookup(%q).Sources = %s, want %s", tt.chain, got, tt.want)
			}
		})
	}
}

func TestLookupGoPlusChainID(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"result":true},"constant_result":["` + oracleFalse[2:] + `"]}`))
	}))
	defer tronSrv.Close()
	origTron := tronGridBaseURL
	tronGridBaseURL = tronSrv.URL
	defer func() { tronGridBaseURL = origTron }()

	var seenChainIDs []string
	gpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenChainIDs = append(seenChainIDs, r.URL.Query().Get("chain_id"))
		w.Write([]byte(`{"code":1,"result":{}}`))
	}))
	defer gpSrv.Close()
	goplusBaseURL = gpSrv.URL + "/api/v1/address_security/"

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"arb": ""}

	svc.lookup(ctx, "arb", "0x1111111111111111111111111111111111111111")
	svc.lookup(ctx, "tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	if strings.Join(seenChainIDs, ",") != "42161,tron" {
		t.Errorf("GoPlus chain_id values = %v, want [42161 tron]", seenChainIDs)
	}
}

func TestLookupBaseHasNoOracle(t *testing.T) {
	var oracleCalls atomic.Int32
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), riskOracleContract) {
			oracleCalls.Add(1)
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + oracleFalse + `"}`))
	}))
	defer rpcSrv.Close()

	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	_ = up
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	svc := newAddressRiskService(store, []string{rpcSrv.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"base": ""}

	res := svc.lookup(ctx, "base", "0x1111111111111111111111111111111111111111")
	for _, s := range res.Sources {
		if s.ID == "chainalysis_oracle" {
			t.Errorf("base lookup must not include chainalysis_oracle: %+v", res.Sources)
		}
	}
	if oracleCalls.Load() != 0 {
		t.Errorf("oracle contract received %d RPC calls on base, want 0", oracleCalls.Load())
	}
}

func TestLookupTronOFACHit(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"result":true},"constant_result":["` + oracleFalse[2:] + `"]}`))
	}))
	defer tronSrv.Close()
	origTron := tronGridBaseURL
	tronGridBaseURL = tronSrv.URL
	defer func() { tronGridBaseURL = origTron }()

	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: tronAddr, Label: "sanctioned TRON"}}, "h", riskNow)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	res := svc.lookup(ctx, "tron", tronAddr)
	if len(res.Clues) != 1 || res.Clues[0].Severity != sevCritical || res.Clues[0].Code != "ofac_listed" {
		t.Fatalf("tron OFAC clues = %+v", res.Clues)
	}

	h := &APIHandler{cache: NewCache(time.Minute), risk: svc}
	rec, _ := doLookup(t, h, "chain=eth&address="+tronAddr)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("chain=eth with TRON address: status = %d, want 400", rec.Code)
	}
}

func TestLookupBTCOnlyOFAC(t *testing.T) {
	var liveCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	origG, origTron := goplusBaseURL, tronGridBaseURL
	goplusBaseURL, tronGridBaseURL = srv.URL+"/", srv.URL
	defer func() { goplusBaseURL, tronGridBaseURL = origG, origTron }()

	const btcAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: btcAddr, Label: "sanctioned BTC"}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)

	svc := newAddressRiskService(store, []string{srv.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": srv.URL, "arb": srv.URL, "op": srv.URL, "base": srv.URL}
	res := svc.lookup(ctx, "btc", btcAddr)
	if len(res.Sources) != 1 || res.Sources[0].ID != "ofac" || res.Sources[0].Status != "ok" {
		t.Fatalf("btc sources = %+v, want only ofac=ok", res.Sources)
	}
	if len(res.Clues) != 1 || res.Clues[0].Code != "ofac_listed" {
		t.Fatalf("btc clues = %+v", res.Clues)
	}
	if liveCalls.Load() != 0 {
		t.Errorf("btc lookup made %d live calls, want 0", liveCalls.Load())
	}
}

func TestLocalCheckSkipsEthFreezeOnL2(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: "0x1111111111111111111111111111111111111111"}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store, stablecoinEvent{
		TxHash: "0xethfreeze", LogIndex: 1, Token: "USDT", Action: "freeze",
		Address: ronin, BlockNumber: 15000000, BlockTime: "2023-01-01T00:00:00Z",
	})

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"arb": ""}

	res := svc.lookup(ctx, "arb", ronin)
	for _, s := range res.Sources {
		if s.ID == "stablecoin" {
			t.Errorf("arb lookup must not include local Ethereum stablecoin source: %+v", res.Sources)
		}
	}
	for _, c := range res.Clues {
		if c.Source == "stablecoin" || c.Code == "stablecoin_frozen" {
			t.Errorf("arb lookup leaked Ethereum freeze clue: %+v", c)
		}
	}
}

func TestLookupBlockscoutPerChainURL(t *testing.T) {
	var cfg PaymentCheckConfig
	if got := cfg.blockscoutURL("arb"); got != "https://arbitrum.blockscout.com" {
		t.Errorf("default arb blockscoutURL = %q", got)
	}
	if got := cfg.blockscoutURL("op"); got != "https://explorer.optimism.io" {
		t.Errorf("default op blockscoutURL = %q", got)
	}
	if got := cfg.blockscoutURL("base"); got != "https://base.blockscout.com" {
		t.Errorf("default base blockscoutURL = %q", got)
	}
	if got := cfg.blockscoutURL("tron"); got != "" {
		t.Errorf("default tron blockscoutURL = %q, want empty", got)
	}

	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	var ethHits, arbHits atomic.Int32
	ethBS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ethHits.Add(1)
		w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
	}))
	defer ethBS.Close()
	arbBS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arbHits.Add(1)
		w.Write([]byte(`{"is_scam":true,"reputation":"scam","public_tags":[{"display_name":"Arb Phishing"}]}`))
	}))
	defer arbBS.Close()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{
		"eth": ethBS.URL,
		"arb": arbBS.URL,
	}

	res := svc.lookup(ctx, "arb", "0x1111111111111111111111111111111111111111")
	if arbHits.Load() != 1 || ethHits.Load() != 0 {
		t.Fatalf("arbHits = %d, ethHits = %d; arb lookup must hit arb Blockscout only", arbHits.Load(), ethHits.Load())
	}
	bs := sourceByID(t, res.Sources, "blockscout")
	if bs.Status != "ok" || strings.Join(bs.Hosts, ",") != strings.Join(hostsOf(arbBS.URL), ",") {
		t.Errorf("arb blockscout source = %+v, want hosts=%v", bs, hostsOf(arbBS.URL))
	}
}

func TestCheckEtherscanKeylessHostsAreBlockscout(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/addresses/") {
			w.Write([]byte(`{"is_scam":false,"reputation":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bsSrv.Close()

	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	markStablecoinSynced(t, store)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	// No Etherscan key configured: 1-hop check goes directly to Blockscout and fails with 502.
	// Its reported host must be Blockscout's host, never api.etherscan.io.
	res := svc.lookup(ctx, "eth", "0x1111111111111111111111111111111111111111")
	es := sourceByID(t, res.Sources, "etherscan")
	if es.Status != "error" || es.Error != "upstream_http_502" {
		t.Fatalf("etherscan source = %+v, want status=error error=upstream_http_502", es)
	}
	wantHost := strings.Join(hostsOf(bsSrv.URL), ",")
	if got := strings.Join(es.Hosts, ","); got != wantHost || strings.Contains(got, "etherscan") {
		t.Errorf("keyless etherscan error hosts = %v, want [%s] (must not report api.etherscan.io)", es.Hosts, wantHost)
	}
}

func TestLookupTronFreezeDedupAndUnfreeze(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	var liveFrozen atomic.Bool
	liveFrozen.Store(true)
	tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val := oracleFalse[2:]
		if liveFrozen.Load() {
			val = oracleTrue[2:]
		}
		w.Write([]byte(`{"result":{"result":true},"constant_result":["` + val + `"]}`))
	}))
	defer tronSrv.Close()
	origTron := tronGridBaseURL
	tronGridBaseURL = tronSrv.URL
	defer func() { tronGridBaseURL = origTron }()

	const (
		frozenTron   = "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7"
		unfrozenTron = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	)
	store := newTestRiskStore(t)
	ctx := context.Background()
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	if err := store.insertTronStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xabcdef1234", LogIndex: 1, Token: "USDT", Action: "freeze", Address: frozenTron, BlockNumber: 100, BlockTime: "2026-09-20T10:00:00Z"},
		{TxHash: "0x1111", LogIndex: 1, Token: "USDT", Action: "freeze", Address: unfrozenTron, BlockNumber: 90, BlockTime: "2026-09-10T10:00:00Z"},
		{TxHash: "0x2222", LogIndex: 2, Token: "USDT", Action: "unfreeze", Address: unfrozenTron, BlockNumber: 95, BlockTime: "2026-09-15T10:00:00Z"},
	}, "2026-08-27", "2026-09-25", riskNow); err != nil {
		t.Fatal(err)
	}
	_ = store.applyScamBatch(ctx, scamTronSourceID, "tron", nil, "2026-08-27", "2026-09-25", riskNow)

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }

	// 1. Frozen TRON address: critical local clue with Tronscan link, deduplicated with live issuer_freeze.
	res := svc.lookup(ctx, "tron", frozenTron)
	if len(res.Clues) != 1 {
		t.Fatalf("frozen TRON clues = %+v, want 1 deduplicated clue", res.Clues)
	}
	c := res.Clues[0]
	if c.Severity != sevCritical || c.Source != "tron_stablecoin" || c.Code != "stablecoin_frozen" ||
		!strings.Contains(c.RefURL, "tronscan.org/#/transaction/abcdef1234") {
		t.Errorf("frozen TRON clue = %+v", c)
	}

	// 2. Unfrozen TRON address (live also false): no clues.
	liveFrozen.Store(false)
	resUn := svc.lookup(ctx, "tron", unfrozenTron)
	if len(resUn.Clues) != 0 {
		t.Errorf("unfrozen TRON clues = %+v, want empty", resUn.Clues)
	}
}

func TestLookupArbHitsEthLookalikeCorpus(t *testing.T) {
	up := newFakeUpstreams(t, `{"result":"`+oracleFalse+`"}`, `{"code":1,"result":{}}`, 0)
	store := newTestRiskStore(t)
	ctx := context.Background()
	const (
		poisoner = "0x123400000000000000000000000000000000abcd"
		imitated = "0x123499999999999999999999999999999999abcd"
	)
	store.replaceList(ctx, "ofac", []listEntry{{Address: ronin}}, "h", riskNow)
	store.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h", riskNow)
	if err := store.applyScamBatch(ctx, scamEthSourceID, "eth", []scamDayBatch{{
		Day: "2026-09-25",
		Lookalikes: []lookalikeRow{
			{Lookalike: poisoner, Imitated: imitated, Hits: 42, Victims: 19, FirstSeen: "2026-09-01", LastSeen: "2026-09-25"},
		},
	}}, "2026-08-27", "2026-09-25", riskNow); err != nil {
		t.Fatal(err)
	}

	svc := newAddressRiskService(store, []string{up.oracle.URL})
	svc.now = func() time.Time { return riskNow }
	svc.blockscoutURLs = map[string]string{"arb": ""}

	res := svc.lookup(ctx, "arb", poisoner)
	if len(res.Clues) != 1 {
		t.Fatalf("arb lookalike clues = %+v, want 1 clue", res.Clues)
	}
	c := res.Clues[0]
	if c.Severity != sevWarning || c.Source != "scam_lookalikes" {
		t.Errorf("arb lookalike clue = %+v, want warning from scam_lookalikes", c)
	}
	combined := c.Title + " " + c.Detail
	if !strings.Contains(c.Title, "Known address-poisoning sender") ||
		!strings.Contains(c.Title, "seen 42 times") ||
		!strings.Contains(c.Title, "2026-09-01") ||
		!strings.Contains(combined, "seen on Ethereum") {
		t.Errorf("arb lookalike text = (%q, %q), want Known address-poisoning sender + seen 42 times + 2026-09-01 + seen on Ethereum", c.Title, c.Detail)
	}
}
