package main

// Assembles one Address Risk lookup: local list clues, live checks, per-source
// status, and a backend-computed summary the frontend renders verbatim (so
// the "never claim safe" and partial-failure rules are testable in Go).

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"slices"
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
var riskSourceOrder = map[string]int{
	"ofac":               0,
	"mew_darklist":       1,
	"stablecoin":         2,
	"issuer_freeze":      3,
	"chainalysis_oracle": 4,
	"goplus":             5,
	"blockscout":         6,
	"etherscan":          7,
}

func riskSourcesFor(chain string) []string {
	switch chain {
	case "eth":
		return []string{"ofac", "mew_darklist", "stablecoin", "issuer_freeze", "chainalysis_oracle", "goplus", "blockscout", "etherscan"}
	case "arb", "op":
		return []string{"ofac", "mew_darklist", "issuer_freeze", "chainalysis_oracle", "goplus", "blockscout"}
	case "base":
		return []string{"ofac", "mew_darklist", "issuer_freeze", "goplus", "blockscout"}
	case "tron":
		return []string{"ofac", "issuer_freeze", "goplus"}
	case "btc":
		return []string{"ofac"}
	default:
		return nil
	}
}

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
	Chain           string       `json:"chain"`
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
// a source errored, a list has not synced yet, or a live counterparty check
// failed, so a retry gets fresh data.
func (r *riskLookupResult) cacheable() bool {
	if r.AssociationScope != nil && r.AssociationScope.CounterpartyErrors > 0 {
		return false
	}
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
			if n := destroyed[st.Token]; n != nil && n.Sign() > 0 {
				c.Severity = sevWarning
				c.Detail = fmt.Sprintf(riskDestroyedFmt, formatTokenAmount(n, 6))
			}
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
	store          *riskStore // nil when the SQLite file could not be opened
	rpcURLs        []string
	chainRPCs      map[string][]string
	blockscoutURLs map[string]string
	now            func() time.Time
	flight         singleflight.Group
	// etherscanKey is the only place lookups read the key from: config
	// writes happen under configMu, and reading cfg here would race.
	etherscanKey    atomic.Pointer[string]
	cfg             *Config               // for saving the key; nil in tests that do not save
	credits         atomic.Pointer[int64] // creditsAvailable from the last key validation
	validateMu      sync.Mutex
	lastValidate    time.Time
	bqSrc           stablecoinSource
	invalidateCache func()
	backfillMu      sync.Mutex
	backfillRunning bool
	pendingPlan     *backfillPlan // last unconfirmed dry-run estimate; guarded by backfillMu
}

func (s *addressRiskService) rpcsFor(chain string) []string {
	if rpcs, ok := s.chainRPCs[chain]; ok {
		return rpcs
	}
	return s.rpcURLs
}

func (s *addressRiskService) blockscoutURL(chain string) string {
	return PaymentCheckConfig{BlockscoutURLs: s.blockscoutURLs}.blockscoutURL(chain)
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

// screenCounterparties checks up to riskMaxLiveCounterparties non-pool
// counterparties (starting with the earliest inbound ETH funder) against the
// Chainalysis oracle, GoPlus, and Blockscout, adding any hits into pool.
func (s *addressRiskService) screenCounterparties(ctx context.Context, candidates []string, firstFunder string, pool riskPool, now time.Time) (screened, errCount int) {
	if len(candidates) > 0 {
		scCtx, scCancel := context.WithTimeout(ctx, riskSourceTimeout)
		defer scCancel()
		type cpHit struct {
			cp      string
			sources []string
			errs    int
		}
		hits := make([]cpHit, len(candidates))
		var wg sync.WaitGroup
		for i, cp := range candidates {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var srcs []string
				var cwg sync.WaitGroup
				var oracleHit bool
				var oracleCode, gpCode, bsCode string
				var gpClues, bsClues []riskClue
				cwg.Add(3)
				go func() { defer cwg.Done(); oracleHit, oracleCode = checkOracle(scCtx, s.rpcsFor("eth"), cp) }()
				go func() { defer cwg.Done(); gpClues, gpCode = checkGoPlus(scCtx, "1", cp, now) }()
				go func() {
					defer cwg.Done()
					bsClues, bsCode = checkBlockscoutAddress(scCtx, s.blockscoutURL("eth"), cp, now)
				}()
				cwg.Wait()
				errs := 0
				if oracleCode != "" {
					errs++
				}
				if gpCode != "" {
					errs++
				}
				if bsCode != "" {
					errs++
				}
				if oracleHit {
					srcs = append(srcs, "chainalysis_oracle")
				}
				for _, c := range gpClues {
					if c.Severity == sevCritical || c.Severity == sevWarning {
						if c.Flag != "" && !goplusRules[c.Flag].Count {
							srcs = append(srcs, "goplus:"+c.Flag)
						} else {
							srcs = append(srcs, c.Title)
						}
					}
				}
				for _, c := range bsClues {
					if c.Code == "blockscout_scam" {
						srcs = append(srcs, "blockscout_scam")
					} else if c.Severity == sevWarning {
						srcs = append(srcs, c.Title)
					}
				}
				hits[i] = cpHit{cp: cp, sources: srcs, errs: errs}
			}()
		}
		wg.Wait()
		for _, h := range hits {
			errCount += h.errs
			if len(h.sources) > 0 {
				pool[h.cp] = append(pool[h.cp], h.sources...)
			}
		}
	}
	if firstFunder != "" && len(pool[firstFunder]) > 0 {
		pool[firstFunder] = append([]string{"first_funder"}, pool[firstFunder]...)
	}
	return len(candidates), errCount
}

// checkEtherscan runs the one-hop association analysis (spec §8.4). When no
// Etherscan key is configured (or Etherscan fails transiently) and Blockscout
// is enabled, it falls back to Blockscout's Etherscan-compatible API.
func (s *addressRiskService) checkEtherscan(ctx context.Context, addr, key string, now time.Time) (riskSource, []riskClue, *riskAssociationScope) {
	bsURL := s.blockscoutURL("eth")
	hosts := hostsOf(etherscanBaseURL)
	if key == "" {
		hosts = hostsOf(bsURL)
	}
	src := riskSource{ID: "etherscan", Status: "ok", Hosts: hosts, SendsAddress: true,
		SignupURL: etherscanSignupURL, HelpURL: etherscanHelpURL}
	fail := func(code string) (riskSource, []riskClue, *riskAssociationScope) {
		src.Status, src.Error = "error", code
		return src, nil, nil
	}
	if key == "" && bsURL == "" {
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

	type listResult struct {
		rows      []etherscanRow
		msg, code string
	}
	var results [3]listResult
	actions := []string{"txlist", "tokentx", "txlistinternal"}

	needFallback := key == ""
	var etherscanErrCode string
	if key != "" {
		esCtx, esCancel := context.WithTimeout(ctx, riskSourceTimeout)
		var wg sync.WaitGroup
		for i, action := range actions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rows, msg, code := etherscanList(esCtx, key, action, addr)
				results[i] = listResult{rows, msg, code}
			}()
		}
		wg.Wait()
		esCancel()
		for _, r := range results {
			if r.code != "" {
				src.LastError = strings.ReplaceAll(r.msg, key, "…")
				etherscanErrCode = r.code
				// Explicit key/plan errors from Etherscan are surfaced directly
				// unless Blockscout fallback can answer for transient failures.
				if r.code == "key_invalid" || r.code == "key_throttled" || r.code == "bad_response" || bsURL == "" {
					return fail(r.code)
				}
				needFallback = true
				break
			}
		}
	}

	if needFallback {
		bsCtx, bsCancel := context.WithTimeout(ctx, riskSourceTimeout)
		var bResults [3]listResult
		var wg sync.WaitGroup
		for i, action := range actions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rows, code := blockscoutListAt(bsCtx, bsURL, action, addr)
				bResults[i] = listResult{rows: rows, code: code}
			}()
		}
		wg.Wait()
		bsCancel()
		for _, r := range bResults {
			if r.code != "" {
				if etherscanErrCode != "" {
					return fail(etherscanErrCode)
				}
				return fail(r.code)
			}
		}
		results = bResults
		src.LastError = ""
		src.Hosts = hostsOf(bsURL)
	}

	candidates, firstFunder := topCounterpartiesToScreen(addr, results[0].rows, results[1].rows, results[2].rows, pool)
	screened, screenErrs := s.screenCounterparties(ctx, candidates, firstFunder, pool, now)

	clues, scope := associationClues(addr, results[0].rows, results[1].rows, results[2].rows, pool, fmtTime(now))
	if scope != nil {
		scope.CounterpartiesScreened = screened
		scope.CounterpartyErrors = screenErrs
	}
	return src, clues, scope
}

func newAddressRiskService(store *riskStore, rpcURLs []string) *addressRiskService {
	return &addressRiskService{store: store, rpcURLs: rpcURLs, now: time.Now}
}

// riskLocalIDs are the sources answered from SQLite, in response order.
var riskLocalIDs = []string{"ofac", "mew_darklist", "stablecoin"}

// localSources builds the local sources' status from one sync_state read,
// filtered to the local sources applicable to chain.
func (s *addressRiskService) localSources(chain string, states map[string]syncState, err error) []riskSource {
	want := riskSourcesFor(chain)
	out := make([]riskSource, 0, len(riskLocalIDs))
	now := s.now()
	for _, id := range riskLocalIDs {
		if !slices.Contains(want, id) {
			continue
		}
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

// listSources reports all local sources' status for /sources ("eth" is the
// superset that includes every local source: ofac, mew_darklist, stablecoin).
func (s *addressRiskService) listSources(ctx context.Context) []riskSource {
	states, err := s.readStates(ctx)
	return s.localSources("eth", states, err)
}

func (s *addressRiskService) localCheck(ctx context.Context, chain, addr string) ([]riskSource, []riskClue, []stablecoinState) {
	want := riskSourcesFor(chain)
	states, err := s.readStates(ctx)
	sources := s.localSources(chain, states, err)
	if s.store == nil || err != nil {
		return sources, nil, nil
	}
	rawHits, err := s.store.listHits(ctx, addr)
	var hits []listHit
	for _, h := range rawHits {
		if slices.Contains(want, h.Source) {
			hits = append(hits, h)
		}
	}
	var frozen []stablecoinState
	var destroyed map[string]*big.Int
	if chain == "eth" {
		if err == nil {
			frozen, err = s.store.stablecoinStates(ctx, addr)
		}
		if err == nil {
			destroyed, err = s.store.destroyedTotals(ctx, addr)
		}
	}
	if err != nil {
		for i := range sources {
			sources[i] = riskSource{ID: sources[i].ID, Status: "error", Error: "unavailable"}
		}
		return sources, nil, nil
	}
	clues := listClues(hits, states)
	if chain == "eth" {
		clues = append(clues, stablecoinClues(frozen, destroyed, states[stablecoinSourceID].Cursor)...)
	}
	sortClues(clues)
	return sources, clues, frozen
}

func (s *addressRiskService) lookup(ctx context.Context, chain, addr string) *riskLookupResult {
	return s.lookupKey(ctx, chain, addr, s.etherscanKeySnapshot())
}

func (s *addressRiskService) lookupKey(ctx context.Context, chain, addr, etherscanKey string) *riskLookupResult {
	if chain == "" {
		chain = "eth"
	}
	want := riskSourcesFor(chain)
	has := func(id string) bool { return slices.Contains(want, id) }
	rpcs := s.rpcsFor(chain)
	bsURL := s.blockscoutURL(chain)
	now := s.now()
	var (
		wg                                         sync.WaitGroup
		localSrc                                   []riskSource
		localClues, goplusFound, bsAddrClues       []riskClue
		localFrozen                                []stablecoinState
		freezeStates                               []issuerFreezeState
		oracleHit                                  bool
		freezeCode, oracleCode, goplusCode, bsCode string
		etherSrc                                   riskSource
		etherClues                                 []riskClue
		scope                                      *riskAssociationScope
	)
	wg.Add(1)
	go func() { defer wg.Done(); localSrc, localClues, localFrozen = s.localCheck(ctx, chain, addr) }()
	if has("issuer_freeze") {
		wg.Add(1)
		go func() { defer wg.Done(); freezeStates, freezeCode = checkIssuerFreeze(ctx, chain, addr, "", rpcs) }()
	}
	if has("chainalysis_oracle") {
		wg.Add(1)
		go func() { defer wg.Done(); oracleHit, oracleCode = checkOracle(ctx, rpcs, addr) }()
	}
	if has("goplus") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			goplusFound, goplusCode = checkGoPlus(ctx, chains[chain].GoPlusChainID, addr, now)
		}()
	}
	if has("blockscout") {
		wg.Add(1)
		go func() { defer wg.Done(); bsAddrClues, bsCode = checkBlockscoutAddress(ctx, bsURL, addr, now) }()
	}
	if has("etherscan") {
		wg.Add(1)
		go func() { defer wg.Done(); etherSrc, etherClues, scope = s.checkEtherscan(ctx, addr, etherscanKey, now) }()
	}
	wg.Wait()

	asOf := fmtTime(now)
	clues := append([]riskClue{}, localClues...)
	if has("issuer_freeze") {
		clues = append(clues, issuerFreezeClues(chain, freezeStates, localFrozen, asOf)...)
	}
	if has("chainalysis_oracle") && oracleHit {
		clues = append(clues, riskClue{Severity: sevCritical, Source: "chainalysis_oracle", Code: "oracle_sanctioned",
			Title: riskTitleOracle, AsOf: asOf,
			RefURL: fmt.Sprintf(chains[chain].AddressURL, riskOracleContract) + "#readContract"})
	}
	clues = append(clues, goplusFound...)
	clues = append(clues, bsAddrClues...)
	clues = append(clues, etherClues...)
	sortClues(clues)

	live := func(id, code string, hosts []string) riskSource {
		src := riskSource{ID: id, Status: "ok", Hosts: hosts, SendsAddress: true}
		if code != "" {
			src.Status, src.Error = "error", code
		}
		return src
	}
	sources := append([]riskSource{}, localSrc...)
	if has("issuer_freeze") {
		freezeHosts := hostsOf(rpcs...)
		if chains[chain].Family == familyTron {
			freezeHosts = hostsOf(tronGridBaseURL)
		}
		sources = append(sources, live("issuer_freeze", freezeCode, freezeHosts))
	}
	if has("chainalysis_oracle") {
		sources = append(sources, live("chainalysis_oracle", oracleCode, hostsOf(rpcs...)))
	}
	if has("goplus") {
		sources = append(sources, live("goplus", goplusCode, hostsOf(goplusBaseURL)))
	}
	if has("blockscout") && bsURL != "" {
		sources = append(sources, live("blockscout", bsCode, hostsOf(bsURL)))
	}
	if has("etherscan") {
		sources = append(sources, etherSrc)
	}

	return &riskLookupResult{
		Chain:            chain,
		Address:          addr,
		QueriedAt:        asOf,
		Summary:          buildRiskSummary(clues, sources),
		Disclaimer:       riskDisclaimer,
		Clues:            clues,
		Sources:          sources,
		AssociationScope: scope,
	}
}
