package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
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
