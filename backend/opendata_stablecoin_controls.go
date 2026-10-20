package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Contract controls (stablecoin.md §4.3 rpc_controls, §6.2 S8, Appendix A,
// Appendix E.4): who can mint, pause, freeze and upgrade each monitored
// deployment, read over the configured RPCs. EVM getters and the ZeppelinOS
// proxy slots use eth_call / eth_getStorageAt, TRON getters TronGrid's
// triggerconstantcontract, and Solana mints getAccountInfo.

const (
	stableKindTether    = "tether"    // TetherToken: owner, paused, deprecated, upgradedAddress (ETH, TRON)
	stableKindFiatToken = "fiattoken" // Circle FiatTokenProxy + FiatToken (USDC, EURC)
	stableKindSPL       = "spl"       // Solana SPL mint authorities

	// ZeppelinOS proxy storage slots (Appendix E.4).
	stableSlotImplementation = "0x7050c9e0f4ca769c69bd3a8ef740bc37934f8e2c036e5a723fd8ee048ed3f8c3"
	stableSlotAdmin          = "0x10d6a54a4754c8869d6886b5f5d7fbfa5b4522237ea5c60d11bc4e7a1ff9390b"

	stableEVMZero  = "0x0000000000000000000000000000000000000000"
	stableTronZero = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb" // TRON's zero address in base58

	stableControlCallTimeout = 8 * time.Second
)

// stableControlDeployment is one monitored deployment (Appendix A).
type stableControlDeployment struct{ Asset, Chain, Kind string }

var stableControlDeployments = []stableControlDeployment{
	{"usdt", "eth", stableKindTether},
	{"usdt", "tron", stableKindTether},
	{"usdt", "sol", stableKindSPL},
	{"usdc", "eth", stableKindFiatToken},
	{"usdc", "sol", stableKindSPL},
	{"eurc", "eth", stableKindFiatToken},
	{"eurc", "base", stableKindFiatToken},
	{"eurc", "sol", stableKindSPL},
}

// token is the deployment's native contract in the chain registry.
func (d stableControlDeployment) token() (registryToken, bool) {
	for _, t := range registryTokens {
		if strings.EqualFold(t.Asset, d.Asset) && t.Network == d.Chain && t.Tier == tierNative {
			return t, true
		}
	}
	return registryToken{}, false
}

func stableControlDeploymentFor(asset, chain string) (stableControlDeployment, bool) {
	for _, d := range stableControlDeployments {
		if d.Asset == asset && d.Chain == chain {
			return d, true
		}
	}
	return stableControlDeployment{}, false
}

// stableControlField is one control read. A zero address in a field that
// cannot be empty is a read error (S8); Nullable fields (rescuer, USDT
// upgradedAddress, SPL authorities) may be unset.
type stableControlField struct {
	Name     string
	Selector string // EVM getter selector (Appendix E.4)
	TronSig  string // TRON function_selector
	Slot     string // proxy storage slot
	Kind     string // address | bool | authority
	Nullable bool
	Critical bool // paused / deprecated: S8 critical
}

var stableControlFields = map[string][]stableControlField{
	stableKindTether: {
		{Name: "owner", Selector: "8da5cb5b", TronSig: "owner()", Kind: "address"},
		{Name: "paused", Selector: "5c975abb", TronSig: "paused()", Kind: "bool", Critical: true},
		{Name: "deprecated", Selector: "0e136b19", TronSig: "deprecated()", Kind: "bool", Critical: true},
		{Name: "upgraded_address", Selector: "26976e3f", TronSig: "upgradedAddress()", Kind: "address", Nullable: true},
	},
	stableKindFiatToken: {
		{Name: "implementation", Slot: stableSlotImplementation, Kind: "address"},
		{Name: "admin", Slot: stableSlotAdmin, Kind: "address"},
		{Name: "owner", Selector: "8da5cb5b", Kind: "address"},
		{Name: "master_minter", Selector: "35d99f35", Kind: "address"},
		{Name: "pauser", Selector: "9fd0506d", Kind: "address"},
		{Name: "blacklister", Selector: "bd102430", Kind: "address"},
		{Name: "rescuer", Selector: "38a63183", Kind: "address", Nullable: true},
		{Name: "paused", Selector: "5c975abb", Kind: "bool", Critical: true},
	},
	stableKindSPL: {
		{Name: "mint_authority", Kind: "authority", Nullable: true},
		{Name: "freeze_authority", Kind: "authority", Nullable: true},
	},
}

// stableControlRead is one field as read in a sample: a value, or a read
// error code. An SPL authority that is not set reads as "" with no code.
type stableControlRead struct {
	Value    string
	Endpoint string // host of the RPC that answered
	Code     string
}

// stableDecodeControlWord decodes one 32-byte return word: an address
// (EVM lowercase hex, TRON base58) or a bool ("true" / "false").
func stableDecodeControlWord(f stableControlField, chain, word string) (string, string) {
	h := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(word), "0x"))
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		return "", "bad_response"
	}
	allZero := func(bs []byte) bool {
		for _, x := range bs {
			if x != 0 {
				return false
			}
		}
		return true
	}
	switch f.Kind {
	case "bool":
		if !allZero(b[:31]) {
			return "", "bad_response"
		}
		switch b[31] {
		case 0:
			return "false", ""
		case 1:
			return "true", ""
		}
		return "", "bad_response"
	case "address":
		if !allZero(b[:12]) {
			return "", "bad_response"
		}
		if allZero(b[12:]) && !f.Nullable {
			return "", "zero_address"
		}
		if chain == "tron" {
			s, err := tronHexToBase58(hex.EncodeToString(b[12:]))
			if err != nil {
				return "", "bad_response"
			}
			return s, ""
		}
		return "0x" + hex.EncodeToString(b[12:]), ""
	}
	return "", "bad_response"
}

// stableControlIsZero reports an unset address value (EVM or TRON zero).
func stableControlIsZero(v string) bool { return v == stableEVMZero || v == stableTronZero }

// stableControlEndpoints is a chain's endpoint list (TRON: TronGrid).
func stableControlEndpoints(chain string, rpcs func(string) []string) []string {
	if chain == "tron" {
		return []string{tronGridBaseURL}
	}
	if rpcs == nil {
		return nil
	}
	return rpcs(chain)
}

// stableEndpointLabel is an endpoint's host, never the full URL (which may
// carry an API key).
func stableEndpointLabel(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return "rpc"
}

// stableReadControlAt reads one EVM or TRON field from one endpoint.
func stableReadControlAt(ctx context.Context, d stableControlDeployment, tok registryToken, f stableControlField, endpoint string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, stableControlCallTimeout)
	defer cancel()
	if d.Chain == "tron" {
		word, code := stableTronConstant(ctx, tok.Contract, f.TronSig)
		if code != "" {
			return "", code
		}
		return stableDecodeControlWord(f, d.Chain, word)
	}
	var res string
	var err error
	if f.Slot != "" {
		res, err = evmRPC(ctx, endpoint, "eth_getStorageAt", tok.Contract, f.Slot, "latest")
	} else {
		res, err = evmRPC(ctx, endpoint, "eth_call", map[string]string{"to": tok.Contract, "data": "0x" + f.Selector}, "latest")
	}
	if err != nil {
		return "", evmRPCErrCode(err)
	}
	return stableDecodeControlWord(f, d.Chain, res)
}

// stableReadSPLAt reads a mint's authorities from one endpoint; a null
// authority is "" (not set), a missing key is a malformed response.
func stableReadSPLAt(ctx context.Context, endpoint, mint string) (map[string]string, string) {
	ctx, cancel := context.WithTimeout(ctx, stableControlCallTimeout)
	defer cancel()
	raw, code := solanaRPC(ctx, endpoint, "getAccountInfo", mint, map[string]any{"encoding": "jsonParsed", "commitment": "finalized"})
	if code != "" {
		return nil, code
	}
	var out struct {
		Value *struct {
			Data struct {
				Parsed struct {
					Type string                     `json:"type"`
					Info map[string]json.RawMessage `json:"info"`
				} `json:"parsed"`
			} `json:"data"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Value == nil || out.Value.Data.Parsed.Type != "mint" {
		return nil, "bad_response"
	}
	vals := map[string]string{}
	for field, key := range map[string]string{"mint_authority": "mintAuthority", "freeze_authority": "freezeAuthority"} {
		v, ok := out.Value.Data.Parsed.Info[key]
		if !ok {
			return nil, "bad_response"
		}
		if string(v) == "null" {
			vals[field] = ""
			continue
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil, "bad_response"
		}
		if b, err := base58Decode(s); err != nil || len(b) != 32 {
			return nil, "bad_response"
		}
		vals[field] = s
	}
	return vals, ""
}

// stableReadDeployment reads every control field of d, starting at endpoint
// index primary (the samples rotate it) and failing over field by field.
func stableReadDeployment(ctx context.Context, d stableControlDeployment, rpcs func(string) []string, primary int) map[string]stableControlRead {
	fields := stableControlFields[d.Kind]
	out := make(map[string]stableControlRead, len(fields))
	fail := func(code string) map[string]stableControlRead {
		for _, f := range fields {
			out[f.Name] = stableControlRead{Code: code}
		}
		return out
	}
	tok, ok := d.token()
	eps := stableControlEndpoints(d.Chain, rpcs)
	if !ok || len(eps) == 0 {
		return fail("unavailable")
	}
	start := primary % len(eps)
	order := append(append([]string{}, eps[start:]...), eps[:start]...)
	if d.Kind == stableKindSPL {
		code := "unavailable"
		for _, ep := range order {
			vals, c := stableReadSPLAt(ctx, ep, tok.Contract)
			if c == "" {
				for _, f := range fields {
					out[f.Name] = stableControlRead{Value: vals[f.Name], Endpoint: stableEndpointLabel(ep)}
				}
				return out
			}
			code = c
			if ctx.Err() != nil {
				break
			}
		}
		return fail(code)
	}
	for _, f := range fields {
		r := stableControlRead{Code: "unavailable"}
		for _, ep := range order {
			v, c := stableReadControlAt(ctx, d, tok, f, ep)
			if c == "" {
				r = stableControlRead{Value: v, Endpoint: stableEndpointLabel(ep)}
				break
			}
			r.Code = c
			if ctx.Err() != nil {
				break
			}
		}
		out[f.Name] = r
	}
	return out
}

// --- confirmation (S8) and the controls sampler ---

const (
	stableControlsTick = 15 * time.Minute
	// stableControlPendingMaxAge: a pending value confirms only when it was
	// seen in the previous sample.
	stableControlPendingMaxAge = stableControlsTick * 3 / 2
	stableControlsRunTimeout   = 3 * time.Minute

	stableConfirmedTwoRPCs    = "two_rpcs"
	stableConfirmedTwoSamples = "two_samples"
)

// stableControlState is one field's row in stable_control_state. Value is
// nil until the baseline (the first confirmed snapshot) exists.
type stableControlState struct {
	Asset, Chain, Field string
	Value               *string
	FirstSeen           time.Time // first observation of the confirmed value
	ConfirmedAt         time.Time
	ConfirmedBy         string // two_rpcs | two_samples
	LastSeen            time.Time
	Baseline            bool      // the value has not changed since monitoring began
	BaselineAt          time.Time // when the first value was confirmed; kept across changes
	PendingValue        *string
	PendingFirstSeen    time.Time
	PendingLastSeen     time.Time
	PendingDisputed     bool // two endpoints disagreed on the pending value
	LastReadAt          time.Time
	LastCode            string // "" | a read error | rpc_disagreement
	LastEndpoint        string
}

// stableControlEvent is one confirmed change (stable_control_events).
type stableControlEvent struct {
	DetectedAt          time.Time // when the change was confirmed
	Asset, Chain, Field string
	Old, New            string
	FirstSeen           time.Time // first observation of the new value
	Source              string    // rpc (bigquery with the opt-in jobs, v1.1)
	ConfirmedBy         string
	TxHash              string
}

// stableControlStep applies one sample to a field's state. second is the
// same field read from another endpoint in the same sample; the sampler
// reads it only when the value differs from the confirmed one (nil when
// there is no other endpoint).
func stableControlStep(st stableControlState, r stableControlRead, second *stableControlRead, now time.Time) (stableControlState, *stableControlEvent) {
	st.LastReadAt = now
	if r.Code != "" {
		st.LastCode = r.Code
		return st, nil
	}
	st.LastCode, st.LastEndpoint = "", r.Endpoint
	v := r.Value
	if st.Value != nil && *st.Value == v {
		st.LastSeen = now
		st.PendingValue, st.PendingDisputed = nil, false
		st.PendingFirstSeen, st.PendingLastSeen = time.Time{}, time.Time{}
		return st, nil
	}
	freshPending := st.PendingValue != nil && *st.PendingValue == v && now.Sub(st.PendingLastSeen) <= stableControlPendingMaxAge
	setPending := func(disputed bool) {
		if !freshPending {
			st.PendingFirstSeen = now
		}
		st.PendingValue, st.PendingLastSeen, st.PendingDisputed = &v, now, disputed || (freshPending && st.PendingDisputed)
	}
	by := ""
	switch {
	case second != nil && second.Code == "" && second.Value == v:
		by = stableConfirmedTwoRPCs
	case second != nil && second.Code == "":
		// Two endpoints disagree in one sample: nothing is confirmed, and the
		// value can now only be confirmed by two agreeing endpoints.
		setPending(true)
		st.LastCode = "rpc_disagreement"
		return st, nil
	case freshPending && !st.PendingDisputed:
		by = stableConfirmedTwoSamples
	}
	if by == "" {
		setPending(false)
		return st, nil
	}
	firstSeen := now
	if freshPending && !st.PendingFirstSeen.IsZero() {
		firstSeen = st.PendingFirstSeen
	}
	var ev *stableControlEvent
	if st.Value != nil {
		ev = &stableControlEvent{DetectedAt: now, Asset: st.Asset, Chain: st.Chain, Field: st.Field, Old: *st.Value, New: v,
			FirstSeen: firstSeen, Source: "rpc", ConfirmedBy: by}
	}
	st.Baseline = st.Value == nil
	if st.Baseline {
		st.BaselineAt = now
	}
	st.Value, st.FirstSeen, st.ConfirmedAt, st.ConfirmedBy, st.LastSeen = &v, firstSeen, now, by, now
	st.PendingValue, st.PendingDisputed = nil, false
	st.PendingFirstSeen, st.PendingLastSeen = time.Time{}, time.Time{}
	return st, ev
}

type stableControlsSampler struct {
	settings stableSettings
	store    *riskStore
	rpcs     func(string) []string
	now      func() time.Time
	onFetch  func(provider string, took time.Duration, code string)

	mu  sync.Mutex
	seq int // samples taken; rotates the primary endpoint
}

func newStableControlsSampler(settings stableSettings, store *riskStore, rpcs func(string) []string, now func() time.Time) *stableControlsSampler {
	return &stableControlsSampler{settings: settings, store: store, rpcs: rpcs, now: now}
}

// tick takes one sample of every monitored deployment and reports whether
// every field was read.
func (c *stableControlsSampler) tick(ctx context.Context) bool {
	if !c.settings.Providers["rpc_controls"] || c.store == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, stableControlsRunTimeout)
	defer cancel()
	now := c.now().UTC()
	c.mu.Lock()
	seq := c.seq
	c.seq++
	c.mu.Unlock()
	prev, err := c.store.stableControlStates(ctx)
	if err != nil {
		slog.Warn("stablecoin controls: read state", "error", err)
		return false
	}
	type result struct {
		states []stableControlState
		events []stableControlEvent
		ok     bool
	}
	results := make([]result, len(stableControlDeployments))
	var wg sync.WaitGroup
	for i, d := range stableControlDeployments {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			reads := stableReadDeployment(ctx, d, c.rpcs, seq)
			res := result{ok: true}
			code := ""
			var splSecond map[string]stableControlRead
			for _, f := range stableControlFields[d.Kind] {
				r := reads[f.Name]
				if r.Code != "" {
					res.ok = false
					if code == "" {
						code = r.Code
					}
				}
				key := d.Asset + "|" + d.Chain + "|" + f.Name
				st, ok := prev[key]
				if !ok {
					st = stableControlState{Asset: d.Asset, Chain: d.Chain, Field: f.Name}
				}
				var second *stableControlRead
				if r.Code == "" && (st.Value == nil || *st.Value != r.Value) {
					if d.Kind == stableKindSPL {
						if splSecond == nil {
							splSecond = c.secondSPL(ctx, d, r.Endpoint)
						}
						if s, ok := splSecond[f.Name]; ok {
							second = &s
						}
					} else {
						second = c.secondRead(ctx, d, f, r.Endpoint)
					}
				}
				st, ev := stableControlStep(st, r, second, now)
				res.states = append(res.states, st)
				if ev != nil {
					res.events = append(res.events, *ev)
					slog.Warn("stablecoin control change", "asset", d.Asset, "chain", d.Chain, "field", f.Name,
						"old", ev.Old, "new", ev.New, "confirmed_by", ev.ConfirmedBy)
				}
			}
			if c.onFetch != nil {
				c.onFetch("rpc_controls", time.Since(t0), code)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	ok := true
	var states []stableControlState
	var events []stableControlEvent
	for _, r := range results {
		ok = ok && r.ok
		states = append(states, r.states...)
		events = append(events, r.events...)
	}
	if err := c.store.saveStableControls(ctx, states, events); err != nil {
		slog.Warn("stablecoin controls: store", "error", err)
		return false
	}
	return ok
}

// otherEndpoint is the first endpoint of the chain whose host differs from
// the one that answered ("" when there is none).
func (c *stableControlsSampler) otherEndpoint(chain, answered string) string {
	for _, ep := range stableControlEndpoints(chain, c.rpcs) {
		if stableEndpointLabel(ep) != answered {
			return ep
		}
	}
	return ""
}

// secondRead reads one EVM field from another endpoint (nil when the chain
// has only one).
func (c *stableControlsSampler) secondRead(ctx context.Context, d stableControlDeployment, f stableControlField, answered string) *stableControlRead {
	ep := c.otherEndpoint(d.Chain, answered)
	tok, ok := d.token()
	if ep == "" || !ok {
		return nil
	}
	v, code := stableReadControlAt(ctx, d, tok, f, ep)
	return &stableControlRead{Value: v, Endpoint: stableEndpointLabel(ep), Code: code}
}

// secondSPL reads a mint's authorities from another endpoint (nil when there
// is none).
func (c *stableControlsSampler) secondSPL(ctx context.Context, d stableControlDeployment, answered string) map[string]stableControlRead {
	ep := c.otherEndpoint(d.Chain, answered)
	tok, ok := d.token()
	if ep == "" || !ok {
		return map[string]stableControlRead{}
	}
	vals, code := stableReadSPLAt(ctx, ep, tok.Contract)
	out := map[string]stableControlRead{}
	for _, f := range stableControlFields[stableKindSPL] {
		out[f.Name] = stableControlRead{Value: vals[f.Name], Endpoint: stableEndpointLabel(ep), Code: code}
	}
	return out
}

func runStableControlsOnce(ctx context.Context, c *stableControlsSampler, h *stableHealth) {
	if !c.settings.Providers["rpc_controls"] {
		return // provider off: no reads, and no run to report
	}
	if c.onFetch == nil && h != nil {
		c.onFetch = h.recordFetch
	}
	start := c.now().UTC()
	ok := c.tick(ctx)
	if h != nil {
		h.recordRun("controls", stableControlsTick, start, start.Add(stableControlsTick), ok)
	}
}

// runStableControlsSampler samples the controls at boot and every 15 minutes
// until ctx ends; a panic in one iteration is logged and the loop continues.
func runStableControlsSampler(ctx context.Context, c *stableControlsSampler, h *stableHealth) {
	t := time.NewTicker(stableControlsTick)
	defer t.Stop()
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("stablecoin controls sampler panic", "panic", r)
				}
			}()
			runStableControlsOnce(ctx, c, h)
		}()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- store ---

func stableNullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return stableTS(t)
}

func stableNullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (s *riskStore) stableControlStates(ctx context.Context) (map[string]stableControlState, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT asset, chain, field, value, first_seen, confirmed_at, confirmed_by, last_seen, is_baseline,
baseline_at, pending_value, pending_first_seen, pending_last_seen, pending_disputed, last_read_at, last_code, last_endpoint FROM stable_control_state`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]stableControlState{}
	for rs.Next() {
		var st stableControlState
		var value, pending, firstSeen, confirmedAt, lastSeen, baselineAt, pFirst, pLast, lastRead sql.NullString
		var baseline, disputed int
		if err := rs.Scan(&st.Asset, &st.Chain, &st.Field, &value, &firstSeen, &confirmedAt, &st.ConfirmedBy, &lastSeen, &baseline,
			&baselineAt, &pending, &pFirst, &pLast, &disputed, &lastRead, &st.LastCode, &st.LastEndpoint); err != nil {
			return nil, err
		}
		if value.Valid {
			v := value.String
			st.Value = &v
		}
		if pending.Valid {
			v := pending.String
			st.PendingValue = &v
		}
		st.FirstSeen, st.ConfirmedAt, st.LastSeen = parseStableTS(firstSeen.String), parseStableTS(confirmedAt.String), parseStableTS(lastSeen.String)
		st.PendingFirstSeen, st.PendingLastSeen, st.LastReadAt = parseStableTS(pFirst.String), parseStableTS(pLast.String), parseStableTS(lastRead.String)
		st.Baseline, st.PendingDisputed, st.BaselineAt = baseline == 1, disputed == 1, parseStableTS(baselineAt.String)
		out[st.Asset+"|"+st.Chain+"|"+st.Field] = st
	}
	return out, rs.Err()
}

// saveStableControls writes one sample: every field's state and the
// confirmed changes, in one transaction.
func (s *riskStore) saveStableControls(ctx context.Context, states []stableControlState, events []stableControlEvent) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, st := range states {
		b2i := map[bool]int{false: 0, true: 1}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO stable_control_state
(asset, chain, field, value, first_seen, confirmed_at, confirmed_by, last_seen, is_baseline, baseline_at, pending_value, pending_first_seen,
 pending_last_seen, pending_disputed, last_read_at, last_code, last_endpoint) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			st.Asset, st.Chain, st.Field, stableNullStr(st.Value), stableNullTime(st.FirstSeen), stableNullTime(st.ConfirmedAt), st.ConfirmedBy,
			stableNullTime(st.LastSeen), b2i[st.Baseline], stableNullTime(st.BaselineAt), stableNullStr(st.PendingValue), stableNullTime(st.PendingFirstSeen),
			stableNullTime(st.PendingLastSeen), b2i[st.PendingDisputed], stableNullTime(st.LastReadAt), st.LastCode, st.LastEndpoint); err != nil {
			return err
		}
	}
	for _, ev := range events {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO stable_control_events
(detected_at, asset, chain, field, old_value, new_value, first_seen, source, confirmed_by, tx_hash) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			stableTS(ev.DetectedAt), ev.Asset, ev.Chain, ev.Field, ev.Old, ev.New, stableTS(ev.FirstSeen), ev.Source, ev.ConfirmedBy, ev.TxHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableControlEvents returns the newest changes first ("" asset: all).
func (s *riskStore) stableControlEvents(ctx context.Context, asset string, limit int) ([]stableControlEvent, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT detected_at, asset, chain, field, old_value, new_value, first_seen, source, confirmed_by, tx_hash
FROM stable_control_events WHERE (? = '' OR asset = ?) ORDER BY detected_at DESC, asset, chain, field LIMIT ?`, asset, asset, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stableControlEvent{}
	for rs.Next() {
		var ev stableControlEvent
		var detected, first string
		if err := rs.Scan(&detected, &ev.Asset, &ev.Chain, &ev.Field, &ev.Old, &ev.New, &first, &ev.Source, &ev.ConfirmedBy, &ev.TxHash); err != nil {
			return nil, err
		}
		ev.DetectedAt, ev.FirstSeen = parseStableTS(detected), parseStableTS(first)
		out = append(out, ev)
	}
	return out, rs.Err()
}
