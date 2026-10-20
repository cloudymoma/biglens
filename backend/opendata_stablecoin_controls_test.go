package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Control state observed on-chain on 2026-10-19 (stablecoin.md Appendix A):
// public contract facts, used as the stub's answers. EURC rescuers and the
// Base EURC paused flag are not listed there; the stub answers 0x0 / false.
const zeroAddr = "0x0000000000000000000000000000000000000000"

var stableControlsAppendixA = map[string]map[string]string{
	"usdt|eth": {"owner": "0xc6cde7c39eb2f0f0095f41570af89efc2c1ea828", "paused": "false", "deprecated": "false", "upgraded_address": zeroAddr},
	// TRON values are base58 (the stub encodes them as 20-byte words).
	"usdt|tron": {"owner": "TBPxhVAsuzoFnKyXtc1o2UySEydPHgATto", "paused": "false", "deprecated": "false", "upgraded_address": stableTronZero},
	"usdt|sol":  {"mint_authority": "Q6XprfkF8RQQKoQVG33xT88H7wi8Uk1B1CC7YAs69Gi", "freeze_authority": "Q6XprfkF8RQQKoQVG33xT88H7wi8Uk1B1CC7YAs69Gi"},
	"usdc|eth": {"implementation": "0x43506849d7c04f9138d1a2050bbf3a0c054402dd", "admin": "0x807a96288a1a408dbc13de2b1d087d10356395d2",
		"owner": "0xfcb19e6a322b27c06842a71e8c725399f049ae3a", "master_minter": "0xe982615d461dd5cd06575bbea87624fda4e3de17",
		"blacklister": "0x0a06be16275b95a7d2567fbdae118b36c7da78f9", "pauser": "0x4914f61d25e5c567143774b76edbf4d5109a8566",
		"rescuer": zeroAddr, "paused": "false"},
	"usdc|sol": {"mint_authority": "BJE5MMbqXjVwjAF7oxwPYXnTXDyspzZyt4vwenNw5ruG", "freeze_authority": "7dGbd2QZcCKcTndnHcTL8q7SMVXAkp688NTQYwrRCrar"},
	"eurc|eth": {"implementation": "0x43506849d7c04f9138d1a2050bbf3a0c054402dd", "admin": "0x9ed85723c5c55b746eb2c4b7e2a359b2091b8fe7",
		"owner": "0x3101386a38bef8b79e49f12dea12938a30e8af96", "master_minter": "0x02398771fd1db790ef2b656ca3bcb3075f27a72c",
		"blacklister": "0x601c97ed567253bf09a51998d53c5aa73290bd73", "pauser": "0x2c576cfc9612541eb827b5c52479f364ddfafeb7",
		"rescuer": zeroAddr, "paused": "false"},
	"eurc|base": {"implementation": "0x2ce6311ddae708829bc0784c967b7d77d19fd779", "admin": "0x4fa3c7461ba47ff34d4033ebaa30525c939991e1",
		"owner": "0xc25a042ae1a6350f4236139150ea6eff4f7d9b51", "master_minter": "0x114b64a8bf7bf0ab2a8a64a3e348307bd568726e",
		"blacklister": "0x8415c913a925586b5169b0322f6ead8f5c9a1ead", "pauser": "0xfa223f7bb88c8df670bb3acdf16fcb3abbc01c50",
		"rescuer": zeroAddr, "paused": "false"},
	"eurc|sol": {"mint_authority": "Hy9168u7b2Toujh8SJKtKK8DWbyaBbXPzxm337R4o4XY", "freeze_authority": "6FF2CPcL6fat16QVJH6W27DeBcX7TSqbwY3a5wtyVJLC"},
	// Native USDC on the L2s (monitored since M4; Appendix A lists no control
	// values for them, so these are made-up test addresses).
	"usdc|arb":  stableControlsSyntheticFiatToken("a1"),
	"usdc|op":   stableControlsSyntheticFiatToken("0a"),
	"usdc|base": stableControlsSyntheticFiatToken("ba"),
}

// stableControlsSyntheticFiatToken is a FiatToken control set with made-up
// addresses tagged by a two-hex-digit prefix.
func stableControlsSyntheticFiatToken(tag string) map[string]string {
	addr := func(n string) string { return "0x" + tag + strings.Repeat("0", 36) + n }
	return map[string]string{"implementation": addr("01"), "admin": addr("02"), "owner": addr("03"), "master_minter": addr("04"),
		"blacklister": addr("05"), "pauser": addr("06"), "rescuer": zeroAddr, "paused": "false"}
}

// stableControlsStub answers the control reads: two EVM endpoints (a, b),
// one Base endpoint, two Solana endpoints and TronGrid. Every endpoint
// serves the Appendix A values unless a test overrides a field on one
// endpoint (set) or fails the endpoint (fail).
type stableControlsStub struct {
	mu        sync.Mutex
	override  map[string]map[string]string // endpoint → "asset|chain|field" → value ("" = SPL authority not set; "raw:0x…" = raw word)
	fail      map[string]int               // endpoint → HTTP status
	calls     map[string]int               // endpoint → requests
	byKey     map[string]string            // "endpoint|method|selector-or-slot" → requests seen (count as string)
	urls      map[string]string            // endpoint → URL
	contracts map[string]string            // lowercase contract → "asset|chain"
}

func (s *stableControlsStub) set(endpoint, asset, chain, field, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override[endpoint] == nil {
		s.override[endpoint] = map[string]string{}
	}
	s.override[endpoint][asset+"|"+chain+"|"+field] = value
}

func (s *stableControlsStub) clear(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.override, endpoint)
}

func (s *stableControlsStub) setFail(endpoint string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == 0 {
		delete(s.fail, endpoint)
		return
	}
	s.fail[endpoint] = status
}

func (s *stableControlsStub) count(endpoint string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[endpoint]
}

func (s *stableControlsStub) value(endpoint, deployment, field string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.override[endpoint][deployment+"|"+field]; ok {
		return v
	}
	return stableControlsAppendixA[deployment][field]
}

// begin counts a request and reports a forced failure status (0 = none).
func (s *stableControlsStub) begin(endpoint string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[endpoint]++
	return s.fail[endpoint]
}

func stableWord(t *testing.T, kind, v string) string {
	t.Helper()
	if raw, ok := strings.CutPrefix(v, "raw:"); ok {
		return raw
	}
	switch kind {
	case "bool":
		if v == "true" {
			return "0x" + strings.Repeat("0", 63) + "1"
		}
		return "0x" + strings.Repeat("0", 64)
	default:
		h := strings.TrimPrefix(v, "0x")
		if strings.HasPrefix(v, "T") {
			var err error
			if h, err = tronBase58ToHex(v); err != nil {
				t.Fatalf("stub tron address %q: %v", v, err)
			}
		}
		return "0x" + strings.Repeat("0", 24) + h
	}
}

func newStableControlsStub(t *testing.T) (*stableControlsStub, func(string) []string) {
	t.Helper()
	s := &stableControlsStub{override: map[string]map[string]string{}, fail: map[string]int{}, calls: map[string]int{},
		urls: map[string]string{}, contracts: map[string]string{}}
	for _, d := range stableControlDeployments {
		tok, ok := d.token()
		if !ok {
			t.Fatalf("no registry token for %s %s", d.Asset, d.Chain)
		}
		s.contracts[strings.ToLower(tok.Contract)] = d.Asset + "|" + d.Chain
	}
	fieldBy := func(kind string, match func(f stableControlField) bool) (stableControlField, bool) {
		for _, f := range stableControlFields[kind] {
			if match(f) {
				return f, true
			}
		}
		return stableControlField{}, false
	}
	rpcError := func(w http.ResponseWriter, msg string) {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":%q}}`, msg)
	}
	evm := func(endpoint string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if st := s.begin(endpoint); st != 0 {
				w.WriteHeader(st)
				return
			}
			var req struct {
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			b, _ := io.ReadAll(r.Body)
			if json.Unmarshal(b, &req) != nil || len(req.Params) == 0 {
				rpcError(w, "bad request")
				return
			}
			var dep string
			var f stableControlField
			var ok bool
			switch req.Method {
			case "eth_call":
				var call struct{ To, Data string }
				_ = json.Unmarshal(req.Params[0], &call)
				dep = s.contracts[strings.ToLower(call.To)]
				kind := stableControlKindOf(dep)
				f, ok = fieldBy(kind, func(f stableControlField) bool { return f.Selector != "" && "0x"+f.Selector == call.Data })
			case "eth_getStorageAt":
				var addr, slot string
				_ = json.Unmarshal(req.Params[0], &addr)
				if len(req.Params) > 1 {
					_ = json.Unmarshal(req.Params[1], &slot)
				}
				dep = s.contracts[strings.ToLower(addr)]
				f, ok = fieldBy(stableControlKindOf(dep), func(f stableControlField) bool { return f.Slot != "" && f.Slot == slot })
			}
			if dep == "" || !ok {
				rpcError(w, "execution reverted")
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, stableWord(t, f.Kind, s.value(endpoint, dep, f.Name)))
		}))
		t.Cleanup(srv.Close)
		s.urls[endpoint] = srv.URL
		return srv
	}
	sol := func(endpoint string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if st := s.begin(endpoint); st != 0 {
				w.WriteHeader(st)
				return
			}
			var req struct {
				Params []json.RawMessage `json:"params"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &req)
			var mint string
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params[0], &mint)
			}
			dep := s.contracts[strings.ToLower(mint)]
			if dep == "" {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"value":null}}`)
				return
			}
			auth := func(field string) string {
				v := s.value(endpoint, dep, field)
				if v == "" {
					return "null"
				}
				return fmt.Sprintf("%q", v)
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"data":{"parsed":{"type":"mint","info":{"decimals":6,"supply":"1000000","isInitialized":true,"mintAuthority":%s,"freezeAuthority":%s}},"program":"spl-token"}}}}`,
				auth("mint_authority"), auth("freeze_authority"))
		}))
		t.Cleanup(srv.Close)
		s.urls[endpoint] = srv.URL
		return srv
	}
	evmA, evmB, base, arb, op, solA, solB := evm("eth-a"), evm("eth-b"), evm("base-a"), evm("arb-a"), evm("op-a"), sol("sol-a"), sol("sol-b")
	tron := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st := s.begin("tron"); st != 0 {
			w.WriteHeader(st)
			return
		}
		var req struct {
			Contract string `json:"contract_address"`
			Selector string `json:"function_selector"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		dep := s.contracts[strings.ToLower(req.Contract)]
		f, ok := fieldBy(stableKindTether, func(f stableControlField) bool { return f.TronSig == req.Selector })
		if dep != "usdt|tron" || !ok {
			fmt.Fprint(w, `{"result":{"result":false}}`)
			return
		}
		fmt.Fprintf(w, `{"result":{"result":true},"constant_result":[%q]}`, strings.TrimPrefix(stableWord(t, f.Kind, s.value("tron", dep, f.Name)), "0x"))
	}))
	t.Cleanup(tron.Close)
	s.urls["tron"] = tron.URL
	orig, origLim := tronGridBaseURL, tronGridLimiter
	tronGridBaseURL, tronGridLimiter = tron.URL, rateLimiterUnlimited()
	t.Cleanup(func() { tronGridBaseURL, tronGridLimiter = orig, origLim })
	return s, func(network string) []string {
		switch network {
		case "eth":
			return []string{evmA.URL, evmB.URL}
		case "base":
			return []string{base.URL}
		case "arb":
			return []string{arb.URL}
		case "op":
			return []string{op.URL}
		case "sol":
			return []string{solA.URL, solB.URL}
		}
		return nil
	}
}

// stableControlKindOf maps "asset|chain" to the deployment's contract kind.
func stableControlKindOf(dep string) string {
	for _, d := range stableControlDeployments {
		if d.Asset+"|"+d.Chain == dep {
			return d.Kind
		}
	}
	return ""
}

func TestStableControlDeployments(t *testing.T) {
	want := map[string]string{"usdt|eth": stableKindTether, "usdt|tron": stableKindTether, "usdt|sol": stableKindSPL,
		"usdc|eth": stableKindFiatToken, "usdc|sol": stableKindSPL, "eurc|eth": stableKindFiatToken, "eurc|base": stableKindFiatToken, "eurc|sol": stableKindSPL,
		"usdc|arb": stableKindFiatToken, "usdc|op": stableKindFiatToken, "usdc|base": stableKindFiatToken}
	if len(stableControlDeployments) != len(want) {
		t.Fatalf("deployments = %d, want %d (Appendix A and its M3 note)", len(stableControlDeployments), len(want))
	}
	for _, d := range stableControlDeployments {
		key := d.Asset + "|" + d.Chain
		tok, ok := d.token()
		if want[key] != d.Kind || !ok || tok.Tier != tierNative {
			t.Errorf("%s: kind %s, registry %v (%+v)", key, d.Kind, ok, tok)
		}
		if got, want := len(stableControlFields[d.Kind]), len(stableControlsAppendixA[key]); got != want {
			t.Errorf("%s: %d fields, Appendix A lists %d", key, got, want)
		}
	}
	// The selectors and slots pinned in stablecoin.md Appendix E.4.
	pins := map[string]string{"owner": "8da5cb5b", "paused": "5c975abb", "master_minter": "35d99f35", "blacklister": "bd102430",
		"pauser": "9fd0506d", "rescuer": "38a63183", "deprecated": "0e136b19", "upgraded_address": "26976e3f"}
	for kind, fields := range stableControlFields {
		for _, f := range fields {
			if sel, ok := pins[f.Name]; ok && f.Selector != sel {
				t.Errorf("%s.%s selector %s, want %s", kind, f.Name, f.Selector, sel)
			}
		}
	}
	if stableSlotImplementation != "0x7050c9e0f4ca769c69bd3a8ef740bc37934f8e2c036e5a723fd8ee048ed3f8c3" ||
		stableSlotAdmin != "0x10d6a54a4754c8869d6886b5f5d7fbfa5b4522237ea5c60d11bc4e7a1ff9390b" {
		t.Errorf("proxy slots %s / %s", stableSlotImplementation, stableSlotAdmin)
	}
}

func TestStableDecodeControlWord(t *testing.T) {
	addr := stableControlField{Name: "owner", Kind: "address"}
	rescuer := stableControlField{Name: "rescuer", Kind: "address", Nullable: true}
	paused := stableControlField{Name: "paused", Kind: "bool"}
	word := func(h string) string { return "0x" + strings.Repeat("0", 64-len(h)) + h }
	cases := []struct {
		name      string
		f         stableControlField
		chain, in string
		value     string
		code      string
	}{
		{"address", addr, "eth", word("c6cde7c39eb2f0f0095f41570af89efc2c1ea828"), "0xc6cde7c39eb2f0f0095f41570af89efc2c1ea828", ""},
		{"address, upper case", addr, "eth", word("C6CDE7C39EB2F0F0095F41570AF89EFC2C1EA828"), "0xc6cde7c39eb2f0f0095f41570af89efc2c1ea828", ""},
		{"dirty high bytes", addr, "eth", "0x01" + strings.Repeat("0", 22) + "c6cde7c39eb2f0f0095f41570af89efc2c1ea828", "", "bad_response"},
		{"short word", addr, "eth", "0x" + strings.Repeat("0", 40), "", "bad_response"},
		{"empty result (no contract)", addr, "eth", "0x", "", "bad_response"},
		{"zero owner is a read error", addr, "eth", word(""), "", "zero_address"},
		{"zero rescuer is a value", rescuer, "eth", word(""), zeroAddr, ""},
		{"tron address", addr, "tron", word("0fa695d6b065707cb4e0ef73b751c93347682bf2"), "TBPxhVAsuzoFnKyXtc1o2UySEydPHgATto", ""},
		{"tron zero upgradedAddress", stableControlField{Name: "upgraded_address", Kind: "address", Nullable: true}, "tron", word(""), stableTronZero, ""},
		{"false", paused, "eth", word(""), "false", ""},
		{"true", paused, "eth", word("1"), "true", ""},
		{"bool 2", paused, "eth", word("2"), "", "bad_response"},
		{"bool not hex", paused, "eth", "0x" + strings.Repeat("z", 64), "", "bad_response"},
	}
	for _, c := range cases {
		v, code := stableDecodeControlWord(c.f, c.chain, c.in)
		if v != c.value || code != c.code {
			t.Errorf("%s: %q %q, want %q %q", c.name, v, code, c.value, c.code)
		}
	}
}

func TestStableReadControls(t *testing.T) {
	stub, rpcs := newStableControlsStub(t)
	ctx := context.Background()
	for _, d := range stableControlDeployments {
		key := d.Asset + "|" + d.Chain
		reads := stableReadDeployment(ctx, d, rpcs, 0)
		if len(reads) != len(stableControlsAppendixA[key]) {
			t.Errorf("%s: %d reads", key, len(reads))
		}
		for field, want := range stableControlsAppendixA[key] {
			r := reads[field]
			if r.Code != "" || r.Value != want || r.Endpoint == "" {
				t.Errorf("%s.%s = %+v, want %q", key, field, r, want)
			}
		}
	}
	// One getAccountInfo per mint; one call per EVM getter or slot.
	if n := stub.count("sol-a"); n != 3 {
		t.Errorf("solana calls = %d, want 3 (one per mint)", n)
	}
	if n := stub.count("eth-a"); n != 4+8+8 {
		t.Errorf("eth calls = %d, want 20 (USDT 4, USDC 8, EURC 8)", n)
	}
	if n := stub.count("tron"); n != 4 {
		t.Errorf("tron calls = %d, want 4", n)
	}

	// The primary endpoint rotates; a failing endpoint fails over per field.
	d, _ := stableControlDeploymentFor("usdc", "eth")
	if r := stableReadDeployment(ctx, d, rpcs, 1)["owner"]; r.Endpoint != stableEndpointLabel(stub.urls["eth-b"]) {
		t.Errorf("primary 1 read from %q, want eth-b", r.Endpoint)
	}
	stub.setFail("eth-a", http.StatusBadGateway)
	if r := stableReadDeployment(ctx, d, rpcs, 0)["owner"]; r.Code != "" || r.Endpoint != stableEndpointLabel(stub.urls["eth-b"]) {
		t.Errorf("eth-a down: %+v, want a read from eth-b", r)
	}
	stub.setFail("eth-b", http.StatusBadGateway)
	if r := stableReadDeployment(ctx, d, rpcs, 0)["owner"]; r.Code != "upstream_http_502" || r.Value != "" {
		t.Errorf("both down: %+v", r)
	}
	stub.setFail("eth-a", 0)
	stub.setFail("eth-b", 0)

	// Zero addresses: an error for the owner, a value for the rescuer; an
	// SPL authority that is not set is a value too (none).
	stub.set("eth-a", "usdc", "eth", "owner", zeroAddr)
	stub.set("eth-b", "usdc", "eth", "owner", zeroAddr)
	if r := stableReadDeployment(ctx, d, rpcs, 0)["owner"]; r.Code != "zero_address" || r.Value != "" {
		t.Errorf("zero owner = %+v, want a read error", r)
	}
	sd, _ := stableControlDeploymentFor("eurc", "sol")
	stub.set("sol-a", "eurc", "sol", "freeze_authority", "")
	if r := stableReadDeployment(ctx, sd, rpcs, 0)["freeze_authority"]; r.Code != "" || r.Value != "" {
		t.Errorf("no freeze authority = %+v, want the value none", r)
	}
	// A malformed word is bad_response.
	stub.set("eth-a", "usdc", "eth", "paused", "raw:0x02")
	stub.set("eth-b", "usdc", "eth", "paused", "raw:0x02")
	if r := stableReadDeployment(ctx, d, rpcs, 0)["paused"]; r.Code != "bad_response" {
		t.Errorf("malformed paused = %+v", r)
	}
	// Without an RPC list the reads are unavailable, not empty values.
	none := func(string) []string { return nil }
	if r := stableReadDeployment(ctx, d, none, 0)["owner"]; r.Code != "unavailable" {
		t.Errorf("no rpcs = %+v", r)
	}
}

// S8 confirmation (§6.2): a change counts when two consecutive samples agree
// or two different RPCs agree in one sample; the first confirmed snapshot is
// the baseline; endpoints that disagree confirm nothing.
func TestStableControlStep(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * stableControlsTick) }
	read := func(v, ep string) stableControlRead { return stableControlRead{Value: v, Endpoint: ep} }
	bad := func(code string) stableControlRead { return stableControlRead{Code: code} }
	st := stableControlState{Asset: "usdc", Chain: "eth", Field: "owner"}

	// Cold start, one endpoint: pending, then the baseline on the next sample.
	st, ev := stableControlStep(st, read("0xa", "e1"), nil, at(0))
	if ev != nil || st.Value != nil || st.PendingValue == nil || *st.PendingValue != "0xa" {
		t.Fatalf("first read = %+v %+v", st, ev)
	}
	st, ev = stableControlStep(st, read("0xa", "e1"), nil, at(1))
	if ev != nil || st.Value == nil || *st.Value != "0xa" || !st.Baseline || st.ConfirmedBy != "two_samples" ||
		!st.FirstSeen.Equal(at(0)) || !st.ConfirmedAt.Equal(at(1)) || st.PendingValue != nil || !st.BaselineAt.Equal(at(1)) {
		t.Fatalf("baseline = %+v %+v", st, ev)
	}
	base := st

	// Cold start, two endpoints agreeing: the baseline at once.
	two, ev := stableControlStep(stableControlState{Field: "owner"}, read("0xa", "e1"), &stableControlRead{Value: "0xa", Endpoint: "e2"}, at(0))
	if ev != nil || two.Value == nil || !two.Baseline || two.ConfirmedBy != "two_rpcs" {
		t.Errorf("two-rpc baseline = %+v %+v", two, ev)
	}

	// A single-sample change does not fire, and a reverting read clears it.
	st, ev = stableControlStep(base, read("0xb", "e1"), nil, at(2))
	if ev != nil || *st.Value != "0xa" || st.PendingValue == nil || *st.PendingValue != "0xb" {
		t.Fatalf("one sample of 0xb = %+v %+v", st, ev)
	}
	st, ev = stableControlStep(st, read("0xa", "e1"), nil, at(3))
	if ev != nil || st.PendingValue != nil || !st.LastSeen.Equal(at(3)) {
		t.Fatalf("revert = %+v %+v", st, ev)
	}
	// Two consecutive samples: the change is confirmed, first seen at the first.
	st, _ = stableControlStep(st, read("0xb", "e1"), nil, at(4))
	st, ev = stableControlStep(st, read("0xb", "e2"), nil, at(5))
	if ev == nil || ev.Old != "0xa" || ev.New != "0xb" || ev.ConfirmedBy != "two_samples" || !ev.FirstSeen.Equal(at(4)) ||
		!ev.DetectedAt.Equal(at(5)) || ev.Source != "rpc" || *st.Value != "0xb" || st.Baseline || !st.BaselineAt.Equal(at(1)) {
		t.Fatalf("two samples = %+v %+v (the baseline time is kept)", st, ev)
	}
	// Two RPCs in one sample: confirmed at once (the immediate path).
	st, ev = stableControlStep(st, read("0xc", "e1"), &stableControlRead{Value: "0xc", Endpoint: "e2"}, at(6))
	if ev == nil || ev.Old != "0xb" || ev.New != "0xc" || ev.ConfirmedBy != "two_rpcs" || !ev.FirstSeen.Equal(at(6)) {
		t.Fatalf("two rpcs = %+v %+v", st, ev)
	}
	confirmed := st

	// Endpoints that disagree confirm nothing, and the disputed value is not
	// confirmed by a later single read either; only two agreeing RPCs are.
	st, ev = stableControlStep(confirmed, read("0xd", "e1"), &stableControlRead{Value: "0xc", Endpoint: "e2"}, at(7))
	if ev != nil || st.LastCode != "rpc_disagreement" || st.PendingValue == nil || !st.PendingDisputed || *st.Value != "0xc" {
		t.Fatalf("disagreement = %+v %+v", st, ev)
	}
	st, ev = stableControlStep(st, read("0xd", "e1"), &stableControlRead{Code: "timeout"}, at(8))
	if ev != nil || *st.Value != "0xc" {
		t.Fatalf("disputed then a single read = %+v %+v", st, ev)
	}
	st, ev = stableControlStep(st, read("0xd", "e2"), &stableControlRead{Value: "0xd", Endpoint: "e1"}, at(9))
	if ev == nil || ev.New != "0xd" || ev.ConfirmedBy != "two_rpcs" || st.PendingDisputed || st.LastCode != "" {
		t.Fatalf("agreement after a dispute = %+v %+v", st, ev)
	}

	// A pending value counts only from the previous sample: 40 minutes later
	// it starts over.
	st, _ = stableControlStep(confirmed, read("0xe", "e1"), nil, at(10))
	st, ev = stableControlStep(st, read("0xe", "e1"), nil, at(10).Add(40*time.Minute))
	if ev != nil || *st.Value != "0xc" || !st.PendingFirstSeen.Equal(at(10).Add(40*time.Minute)) {
		t.Fatalf("expired pending = %+v %+v", st, ev)
	}

	// Read errors (a zero owner, a timeout) are not observations.
	st, ev = stableControlStep(confirmed, bad("zero_address"), nil, at(11))
	if ev != nil || *st.Value != "0xc" || st.LastCode != "zero_address" || !st.LastSeen.Equal(confirmed.LastSeen) || !st.LastReadAt.Equal(at(11)) {
		t.Fatalf("zero address read = %+v %+v", st, ev)
	}
	// An SPL authority removed ("" = not set) is a change like any other.
	spl := stableControlState{Field: "freeze_authority"}
	spl, _ = stableControlStep(spl, read("7dGb", "s1"), &stableControlRead{Value: "7dGb", Endpoint: "s2"}, at(0))
	spl, ev = stableControlStep(spl, read("", "s1"), &stableControlRead{Value: "", Endpoint: "s2"}, at(1))
	if ev == nil || ev.Old != "7dGb" || ev.New != "" || spl.Value == nil || *spl.Value != "" {
		t.Errorf("authority removed = %+v %+v", spl, ev)
	}
}

func controlValue(t *testing.T, store *riskStore, asset, chain, field string) stableControlState {
	t.Helper()
	states, err := store.stableControlStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return states[asset+"|"+chain+"|"+field]
}

func TestStableControlsSampler(t *testing.T) {
	stub, rpcs := newStableControlsStub(t)
	store := newTestRiskStore(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := newStableControlsSampler(StablecoinConfig{}.settings(), store, rpcs, func() time.Time { return clock })
	var mu sync.Mutex
	fetches := map[string]int{}
	c.onFetch = func(provider string, _ time.Duration, _ string) {
		mu.Lock()
		defer mu.Unlock()
		fetches[provider]++
	}
	events := func() []stableControlEvent {
		t.Helper()
		evs, err := store.stableControlEvents(ctx, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}

	// First sample: Ethereum and Solana have two endpoints, so their baseline
	// is confirmed at once; Base and TRON have one and wait for the next.
	if ok := c.tick(ctx); !ok {
		t.Fatal("first tick not ok")
	}
	for _, d := range stableControlDeployments {
		key := d.Asset + "|" + d.Chain
		for field, want := range stableControlsAppendixA[key] {
			st := controlValue(t, store, d.Asset, d.Chain, field)
			twoRPCs := d.Chain == "eth" || d.Chain == "sol"
			switch {
			case twoRPCs && (st.Value == nil || *st.Value != want || !st.Baseline || st.ConfirmedBy != "two_rpcs"):
				t.Errorf("%s.%s after one sample = %+v, want the baseline %q", key, field, st, want)
			case !twoRPCs && (st.Value != nil || st.PendingValue == nil || *st.PendingValue != want):
				t.Errorf("%s.%s after one sample = %+v, want pending %q", key, field, st, want)
			}
		}
	}
	if fetches["rpc_controls"] != len(stableControlDeployments) {
		t.Errorf("health fetches = %v, want one per deployment", fetches)
	}
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	if st := controlValue(t, store, "usdt", "tron", "owner"); st.Value == nil || *st.Value != "TBPxhVAsuzoFnKyXtc1o2UySEydPHgATto" || st.ConfirmedBy != "two_samples" {
		t.Errorf("tron owner after two samples = %+v", st)
	}
	if st := controlValue(t, store, "eurc", "base", "implementation"); st.Value == nil || !st.Baseline {
		t.Errorf("base implementation after two samples = %+v", st)
	}
	if n := len(events()); n != 0 {
		t.Fatalf("events after the baseline = %d, want 0", n)
	}

	// USDC paused on Ethereum: two endpoints agree in one sample, so the
	// change is confirmed in that sample (the immediate-critical path).
	stub.set("eth-a", "usdc", "eth", "paused", "true")
	stub.set("eth-b", "usdc", "eth", "paused", "true")
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	evs := events()
	if len(evs) != 1 || evs[0].Asset != "usdc" || evs[0].Field != "paused" || evs[0].Old != "false" || evs[0].New != "true" ||
		evs[0].ConfirmedBy != "two_rpcs" || !evs[0].DetectedAt.Equal(clock) {
		t.Fatalf("paused events = %+v", evs)
	}

	// A new EURC owner on Base (one endpoint) needs two samples.
	stub.set("base-a", "eurc", "base", "owner", "0x00000000000000000000000000000000000000b1")
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	if n := len(events()); n != 1 {
		t.Fatalf("one sample of a new Base owner fired: %d events", n)
	}
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	evs = events()
	if len(evs) != 2 || evs[0].Field != "owner" || evs[0].Chain != "base" || evs[0].New != "0x00000000000000000000000000000000000000b1" ||
		evs[0].ConfirmedBy != "two_samples" || !evs[0].FirstSeen.Equal(clock.Add(-stableControlsTick)) {
		t.Fatalf("base owner events = %+v (newest first)", evs)
	}

	// TRON USDT deprecated: one endpoint, so two samples (≤ 15 minutes).
	stub.set("tron", "usdt", "tron", "deprecated", "true")
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	clock = clock.Add(stableControlsTick)
	c.tick(ctx)
	if evs = events(); len(evs) != 3 || evs[0].Chain != "tron" || evs[0].Field != "deprecated" || evs[0].New != "true" {
		t.Fatalf("tron deprecated events = %+v", evs)
	}

	// One endpoint reporting a different USDT owner: the endpoints disagree
	// (or the other one, as the primary, reads the confirmed value), so
	// nothing is confirmed in any number of samples.
	stub.set("eth-a", "usdt", "eth", "owner", "0x00000000000000000000000000000000000000ee")
	for i := 0; i < 3; i++ {
		clock = clock.Add(stableControlsTick)
		c.tick(ctx)
	}
	if n := len(events()); n != 3 {
		t.Fatalf("a lone endpoint confirmed a change: %d events", n)
	}
	if st := controlValue(t, store, "usdt", "eth", "owner"); *st.Value != "0xc6cde7c39eb2f0f0095f41570af89efc2c1ea828" {
		t.Errorf("usdt owner = %+v", st)
	}
	stub.clear("eth-a")
	stub.set("eth-a", "usdc", "eth", "paused", "true")

	// A zero owner is a read error on both endpoints: no change, the code
	// is kept, and the sample is not ok.
	stub.set("eth-a", "eurc", "eth", "owner", zeroAddr)
	stub.set("eth-b", "eurc", "eth", "owner", zeroAddr)
	clock = clock.Add(stableControlsTick)
	if ok := c.tick(ctx); ok {
		t.Error("tick with a read error reported ok")
	}
	if st := controlValue(t, store, "eurc", "eth", "owner"); st.LastCode != "zero_address" || *st.Value != "0x3101386a38bef8b79e49f12dea12938a30e8af96" {
		t.Errorf("eurc owner after a zero read = %+v", st)
	}
	if n := len(events()); n != 3 {
		t.Errorf("events after a zero read = %d", n)
	}

	// Disabled: no calls at all.
	before := stub.count("eth-a") + stub.count("tron") + stub.count("sol-a")
	off := newStableControlsSampler(StablecoinConfig{Providers: map[string]bool{"rpc_controls": false}}.settings(), store, rpcs, func() time.Time { return clock })
	off.tick(ctx)
	if after := stub.count("eth-a") + stub.count("tron") + stub.count("sol-a"); after != before {
		t.Errorf("disabled sampler made %d calls", after-before)
	}
}

// The runner records the controls sampler in /meta health.
func TestRunStableControlsOnce(t *testing.T) {
	_, rpcs := newStableControlsStub(t)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	settings := StablecoinConfig{}.settings()
	h := newStableHealth(settings, func() time.Time { return clock })
	c := newStableControlsSampler(settings, newTestRiskStore(t), rpcs, func() time.Time { return clock })
	runStableControlsOnce(context.Background(), c, h)
	if s := h.sampler("controls"); s.LastOK == "" || s.NextDue != "2026-10-20T12:15:00Z" {
		t.Errorf("controls sampler health = %+v", s)
	}
	if p := h.provider("rpc_controls"); p.Status != "ok" {
		t.Errorf("rpc_controls provider = %+v", p)
	}
}
