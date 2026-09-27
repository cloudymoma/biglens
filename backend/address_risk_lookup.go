package main

// Assembles one Address Risk lookup: local list clues, live checks, per-source
// status, and a backend-computed summary the frontend renders verbatim (so
// the "never claim safe" and partial-failure rules are testable in Go).

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/singleflight"
)

const (
	riskTitleOFAC   = "OFAC SDN list (via 0xB10C extract)"
	riskTitleMEW    = "MyEtherWallet darklist (historical list, frozen since 2020-11)"
	riskTitleOracle = "Chainalysis sanctions oracle (on-chain; last updated 2026-03)"

	riskTitleFrozenFmt   = "Frozen by %s contract"
	riskTitleUnfrozenFmt = "Previously frozen by %s contract (since unfrozen)"
	riskDestroyedFmt     = "Tether destroyed %s USDT"
	riskCoverageFmt      = "Freeze history covers %s–%s only."

	riskListStaleAfter = 12 * time.Hour

	riskDisclaimer = "Risk clues aggregate public lists and third-party signals. They may be incomplete, " +
		"delayed or wrong. Absence of records does not mean an address is safe. Not legal or compliance " +
		"advice; for sanctions screening use OFAC's official Sanctions List Search."
)

// riskSourceOrder fixes source and clue order in every response.
var riskSourceOrder = map[string]int{"ofac": 0, "mew_darklist": 1, "stablecoin": 2, "chainalysis_oracle": 3, "goplus": 4, "etherscan": 5}

// riskSkippedNotes explains not_configured sources.
var riskSkippedNotes = map[string]string{"etherscan": "Etherscan association analysis (no API key)"}

type riskSource struct {
	ID                string   `json:"id"`
	Status            string   `json:"status"`
	Error             string   `json:"error,omitempty"`
	LastOKAt          string   `json:"last_ok_at,omitempty"`
	UpstreamChangedAt string   `json:"upstream_changed_at,omitempty"`
	CoverageFrom      string   `json:"coverage_from,omitempty"`
	Cursor            string   `json:"cursor,omitempty"`
	LastError         string   `json:"last_error,omitempty"`
	Hosts             []string `json:"hosts,omitempty"`
	SendsAddress      bool     `json:"sends_address"`
	SignupURL         string   `json:"signup_url,omitempty"`
	HelpURL           string   `json:"help_url,omitempty"`
}

type riskCounts struct {
	Critical    int `json:"critical"`
	Warning     int `json:"warning"`
	Association int `json:"association"`
	Info        int `json:"info"`
}

type riskSummary struct {
	Counts     riskCounts `json:"counts"`
	Checked    []string   `json:"checked"`
	Failed     []string   `json:"failed"`
	Incomplete []string   `json:"incomplete"`
	Skipped    []string   `json:"skipped"`
	Text       string     `json:"text"`
}

type riskLookupResult struct {
	Address         string       `json:"address"`
	ChecksumWarning bool         `json:"checksum_warning"`
	QueriedAt       string       `json:"queried_at"`
	Summary         riskSummary  `json:"summary"`
	Disclaimer      string       `json:"disclaimer"`
	Clues           []riskClue   `json:"clues"`
	Sources         []riskSource `json:"sources"`
	// AssociationScope says what the Etherscan analysis covered; null when
	// it did not run (no key, or the source failed).
	AssociationScope *riskAssociationScope `json:"association_scope"`
}

func (r *riskLookupResult) hasError() bool {
	for _, s := range r.Sources {
		if s.Status == "error" {
			return true
		}
	}
	return false
}

// cacheable reports whether the result may be cached for 10 minutes: not when
// a source errored or a list has not synced yet, so a retry gets fresh data.
func (r *riskLookupResult) cacheable() bool {
	for _, s := range r.Sources {
		if s.Status == "error" || s.Status == "empty" {
			return false
		}
	}
	return true
}

// listSourceStatus applies precedence stale > empty > ok ("error" is decided
// by the caller when the store itself is unavailable).
func listSourceStatus(st syncState, now time.Time) string {
	if st.PendingCount > 0 {
		return "stale"
	}
	if st.LastOKAt == "" {
		return "empty"
	}
	if t, err := time.Parse(time.RFC3339, st.LastOKAt); err != nil || now.Sub(t) > riskListStaleAfter {
		return "stale"
	}
	if st.RowCount == 0 {
		return "empty"
	}
	return "ok"
}

// stablecoinSourceStatus: precedence stale > empty > partial > ok. Coverage
// starting after the first USDT freeze means older freezes are not local.
func stablecoinSourceStatus(st syncState, now time.Time) string {
	if st.Cursor == "" {
		return "empty"
	}
	if st.Cursor < civil.DateOf(now.UTC()).AddDays(-2).String() {
		return "stale"
	}
	if st.CoverageFrom > stablecoinFirstDay {
		return "partial"
	}
	return "ok"
}

// stablecoinClues turns the current freeze state per token into clues:
// frozen (latest event freeze or destroy) is Critical, unfrozen is Info.
// destroyed holds per-token destroy totals for this address.
func stablecoinClues(states []stablecoinState, destroyed map[string]*big.Int, cursor string) []riskClue {
	var out []riskClue
	for _, st := range states {
		observed := st.BlockTime
		c := riskClue{Source: "stablecoin", Token: st.Token, ObservedAt: &observed,
			AsOf: cursor + "T23:59:59Z", RefURL: "https://etherscan.io/tx/" + st.TxHash}
		switch st.Action {
		case "freeze", "destroy":
			c.Severity, c.Code, c.Title = sevCritical, "stablecoin_frozen", fmt.Sprintf(riskTitleFrozenFmt, st.Token)
			if n := destroyed[st.Token]; n != nil && n.Sign() > 0 {
				c.Detail = fmt.Sprintf(riskDestroyedFmt, formatTokenAmount(n, 6))
			}
		case "unfreeze":
			c.Severity, c.Code, c.Title = sevInfo, "stablecoin_unfrozen", fmt.Sprintf(riskTitleUnfrozenFmt, st.Token)
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

func listClues(hits []listHit, states map[string]syncState) []riskClue {
	var out []riskClue
	for _, h := range hits {
		c := riskClue{Source: h.Source, Detail: h.Label, AsOf: states[h.Source].LastOKAt}
		switch h.Source {
		case "ofac":
			c.Severity, c.Code, c.Title, c.RefURL = sevCritical, "ofac_listed", riskTitleOFAC, "https://sanctionssearch.ofac.treas.gov/"
		case "mew_darklist":
			c.Severity, c.Code, c.Title = sevWarning, "mew_darklist", riskTitleMEW
			c.RefURL = "https://github.com/MyEtherWallet/ethereum-lists/blob/master/src/addresses/addresses-darklist.json"
			if h.ListedAt != "" {
				d := h.ListedAt
				c.ObservedAt = &d
			}
		default:
			continue
		}
		out = append(out, c)
	}
	sortClues(out)
	return out
}

func sortClues(cs []riskClue) {
	sort.SliceStable(cs, func(i, j int) bool {
		if sevRank[cs[i].Severity] != sevRank[cs[j].Severity] {
			return sevRank[cs[i].Severity] < sevRank[cs[j].Severity]
		}
		return riskSourceOrder[cs[i].Source] < riskSourceOrder[cs[j].Source]
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// buildRiskSummary implements spec §8.5. The four lists never overlap:
// checked = ok; failed = error|stale; incomplete = partial|empty; skipped = not_configured.
func buildRiskSummary(clues []riskClue, sources []riskSource) riskSummary {
	s := riskSummary{Checked: []string{}, Failed: []string{}, Incomplete: []string{}, Skipped: []string{}}
	for _, c := range clues {
		switch c.Severity {
		case sevCritical:
			s.Counts.Critical++
		case sevWarning:
			s.Counts.Warning++
		case sevAssociation:
			s.Counts.Association++
		case sevInfo:
			s.Counts.Info++
		}
	}
	unavailable := false
	coverage := ""
	for _, src := range sources {
		if src.ID == "stablecoin" && src.Status == "partial" {
			coverage = fmt.Sprintf(riskCoverageFmt, src.CoverageFrom, src.Cursor)
		}
		switch src.Status {
		case "ok":
			s.Checked = append(s.Checked, src.ID)
		case "error", "stale":
			s.Failed = append(s.Failed, src.ID)
			unavailable = unavailable || src.Error == "unavailable"
		case "partial", "empty":
			s.Incomplete = append(s.Incomplete, src.ID)
		case "not_configured":
			s.Skipped = append(s.Skipped, src.ID)
		}
	}
	k, m := len(s.Checked), len(s.Failed)+len(s.Incomplete)
	switch {
	case len(clues) > 0:
		s.Text = fmt.Sprintf("%s found — %d critical · %d warning · %d association · %d info",
			plural(len(clues), "clue"), s.Counts.Critical, s.Counts.Warning, s.Counts.Association, s.Counts.Info)
		if m > 0 {
			s.Text += fmt.Sprintf("; %s could not be fully checked (see Sources)", plural(m, "source"))
		}
	case m == 0:
		s.Text = fmt.Sprintf("No records in the %s checked.", plural(k, "source"))
		var notes []string
		for _, id := range s.Skipped {
			if n := riskSkippedNotes[id]; n != "" {
				notes = append(notes, n)
			}
		}
		if len(notes) > 0 {
			s.Text += " Not checked: " + strings.Join(notes, "; ") + "."
		}
	default:
		s.Text = fmt.Sprintf("Nothing found in %s that completed; %d could not be fully checked (see Sources).",
			plural(k, "source"), m)
	}
	if coverage != "" {
		if !strings.HasSuffix(s.Text, ".") {
			s.Text += "."
		}
		s.Text += " " + coverage
	}
	if unavailable {
		s.Text += " Local lists are unavailable on this server."
	}
	return s
}

// hostsOf returns hostnames only: a keyed RPC URL can carry its secret in the
// path or query, so full URLs are never exposed (spec §10).
func hostsOf(urls ...string) []string {
	var out []string
	for _, u := range urls {
		p, err := url.Parse(u)
		if err != nil || p.Host == "" {
			continue
		}
		out = append(out, p.Hostname())
	}
	return out
}

type addressRiskService struct {
	store   *riskStore // nil when the SQLite file could not be opened
	rpcURLs []string
	now     func() time.Time
	flight  singleflight.Group
	// etherscanKey is the only place lookups read the key from: config
	// writes happen under configMu, and reading cfg here would race.
	etherscanKey atomic.Pointer[string]
	cfg          *Config               // for saving the key; nil in tests that do not save
	credits      atomic.Pointer[int64] // creditsAvailable from the last key validation
	validateMu   sync.Mutex
	lastValidate time.Time
}

func (s *addressRiskService) setEtherscanKey(k string) { s.etherscanKey.Store(&k) }

// etherscanKeySnapshot is taken once per request and used for both the
// Etherscan calls and the cache key's hasKey flag.
func (s *addressRiskService) etherscanKeySnapshot() string {
	if p := s.etherscanKey.Load(); p != nil {
		return *p
	}
	return ""
}

// checkEtherscan runs the one-hop association analysis (spec §8.4). No key
// → not_configured. No local pool → no request at all: the match against an
// empty pool would read as "no association".
func (s *addressRiskService) checkEtherscan(ctx context.Context, addr, key string, now time.Time) (riskSource, []riskClue, *riskAssociationScope) {
	src := riskSource{ID: "etherscan", Status: "ok", Hosts: hostsOf(etherscanBaseURL), SendsAddress: true,
		SignupURL: etherscanSignupURL, HelpURL: etherscanHelpURL}
	fail := func(code string) (riskSource, []riskClue, *riskAssociationScope) {
		src.Status, src.Error = "error", code
		return src, nil, nil
	}
	if key == "" {
		src.Status = "not_configured"
		return src, nil, nil
	}
	if s.store == nil {
		return fail("local_pool_unavailable")
	}
	pool, err := s.store.riskPoolEntries(ctx)
	if err != nil || len(pool) == 0 { // an empty pool (first sync) would read as "no association"
		return fail("local_pool_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	// The three calls overlap (the shared limiter still spaces the requests):
	// run one after another, slow 1000-row answers would exhaust the budget.
	type listResult struct {
		rows      []etherscanRow
		msg, code string
	}
	var results [3]listResult
	var wg sync.WaitGroup
	for i, action := range []string{"txlist", "tokentx", "txlistinternal"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, msg, code := etherscanList(ctx, key, action, addr)
			results[i] = listResult{rows, msg, code}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.code != "" {
			// Etherscan's own reason (e.g. a tightened free tier), key stripped.
			src.LastError = strings.ReplaceAll(r.msg, key, "…")
			return fail(r.code)
		}
	}
	clues, scope := associationClues(addr, results[0].rows, results[1].rows, results[2].rows, pool, fmtTime(now))
	return src, clues, scope
}

func newAddressRiskService(store *riskStore, rpcURLs []string) *addressRiskService {
	return &addressRiskService{store: store, rpcURLs: rpcURLs, now: time.Now}
}

// riskLocalIDs are the sources answered from SQLite, in response order.
var riskLocalIDs = []string{"ofac", "mew_darklist", "stablecoin"}

// localSources builds the local sources' status from one sync_state read.
func (s *addressRiskService) localSources(states map[string]syncState, err error) []riskSource {
	out := make([]riskSource, 0, len(riskLocalIDs))
	now := s.now()
	for _, id := range riskLocalIDs {
		if s.store == nil || err != nil {
			out = append(out, riskSource{ID: id, Status: "error", Error: "unavailable"})
			continue
		}
		if id == "stablecoin" {
			st := states[stablecoinSourceID]
			out = append(out, riskSource{ID: id, Status: stablecoinSourceStatus(st, now), LastOKAt: st.LastOKAt,
				CoverageFrom: st.CoverageFrom, Cursor: st.Cursor, LastError: st.LastError})
			continue
		}
		st := states[id]
		out = append(out, riskSource{ID: id, Status: listSourceStatus(st, now),
			LastOKAt: st.LastOKAt, UpstreamChangedAt: st.UpstreamChangedAt, LastError: st.LastError})
	}
	return out
}

func (s *addressRiskService) readStates(ctx context.Context) (map[string]syncState, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.allSyncStates(ctx)
}

// listSources reports the local sources' status; used by /sources.
func (s *addressRiskService) listSources(ctx context.Context) []riskSource {
	states, err := s.readStates(ctx)
	return s.localSources(states, err)
}

func (s *addressRiskService) localCheck(ctx context.Context, addr string) ([]riskSource, []riskClue) {
	states, err := s.readStates(ctx)
	sources := s.localSources(states, err)
	if s.store == nil || err != nil {
		return sources, nil
	}
	hits, err := s.store.listHits(ctx, addr)
	var frozen []stablecoinState
	var destroyed map[string]*big.Int
	if err == nil {
		frozen, err = s.store.stablecoinStates(ctx, addr)
	}
	if err == nil {
		destroyed, err = s.store.destroyedTotals(ctx, addr)
	}
	if err != nil {
		for i := range sources {
			sources[i] = riskSource{ID: sources[i].ID, Status: "error", Error: "unavailable"}
		}
		return sources, nil
	}
	clues := append(listClues(hits, states), stablecoinClues(frozen, destroyed, states[stablecoinSourceID].Cursor)...)
	sortClues(clues)
	return sources, clues
}

func (s *addressRiskService) lookup(ctx context.Context, addr string) *riskLookupResult {
	return s.lookupKey(ctx, addr, s.etherscanKeySnapshot())
}

func (s *addressRiskService) lookupKey(ctx context.Context, addr, etherscanKey string) *riskLookupResult {
	now := s.now()
	var (
		wg                      sync.WaitGroup
		localSrc                []riskSource
		localClues, goplusFound []riskClue
		oracleHit               bool
		oracleCode, goplusCode  string
		etherSrc                riskSource
		etherClues              []riskClue
		scope                   *riskAssociationScope
	)
	wg.Add(4)
	go func() { defer wg.Done(); localSrc, localClues = s.localCheck(ctx, addr) }()
	go func() { defer wg.Done(); oracleHit, oracleCode = checkOracle(ctx, s.rpcURLs, addr) }()
	go func() { defer wg.Done(); goplusFound, goplusCode = checkGoPlus(ctx, addr, now) }()
	go func() { defer wg.Done(); etherSrc, etherClues, scope = s.checkEtherscan(ctx, addr, etherscanKey, now) }()
	wg.Wait()

	clues := append([]riskClue{}, localClues...)
	if oracleHit {
		clues = append(clues, riskClue{Severity: sevCritical, Source: "chainalysis_oracle", Code: "oracle_sanctioned",
			Title: riskTitleOracle, AsOf: fmtTime(now),
			RefURL: "https://etherscan.io/address/" + riskOracleContract + "#readContract"})
	}
	clues = append(clues, goplusFound...)
	clues = append(clues, etherClues...)
	sortClues(clues)

	live := func(id, code string, hosts []string) riskSource {
		src := riskSource{ID: id, Status: "ok", Hosts: hosts, SendsAddress: true}
		if code != "" {
			src.Status, src.Error = "error", code
		}
		return src
	}
	sources := append(localSrc,
		live("chainalysis_oracle", oracleCode, hostsOf(s.rpcURLs...)),
		live("goplus", goplusCode, hostsOf(goplusBaseURL)),
		etherSrc)

	return &riskLookupResult{
		Address:          addr,
		QueriedAt:        fmtTime(now),
		Summary:          buildRiskSummary(clues, sources),
		Disclaimer:       riskDisclaimer,
		Clues:            clues,
		Sources:          sources,
		AssociationScope: scope,
	}
}
