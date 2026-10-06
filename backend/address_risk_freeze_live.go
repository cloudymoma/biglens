package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

type issuerFreezeState struct {
	Token    string // registry Label, e.g. "USDT", "USD₮0", "USDC"
	Contract string
	Frozen   bool
}

var freezeSelectorSignatures = map[string]string{
	"e47d6060": "isBlackListed(address)",
	"fe575a87": "isBlacklisted(address)",
	"fbac3951": "isBlocked(address)",
}

func evmRPCErrCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return upstreamErrCode(err)
	}
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, "upstream status "); ok {
		code := strings.TrimSpace(after)
		if code == "429" {
			return "rate_limited"
		}
		return "upstream_http_" + code
	}
	return "bad_response"
}

func evmFreezeCallOnce(ctx context.Context, rpcURL, contract, freezeSel, addr string) (bool, string) {
	data := "0x" + strings.TrimPrefix(freezeSel, "0x") + strings.Repeat("0", 24) + strings.TrimPrefix(strings.ToLower(addr), "0x")
	res, err := evmRPC(ctx, rpcURL, "eth_call", map[string]string{"to": contract, "data": data}, "latest")
	if err != nil {
		return false, evmRPCErrCode(err)
	}
	return parseBool32Hex(res)
}

func checkEVMContractFreeze(ctx context.Context, rpcs []string, contract, freezeSel, addr string) (bool, string) {
	if len(rpcs) == 0 {
		return false, "unavailable"
	}
	deadline, hasDeadline := ctx.Deadline()
	code := "unavailable"
	for i, u := range rpcs {
		actx := ctx
		var acancel context.CancelFunc = func() {}
		if hasDeadline {
			per := time.Until(deadline) / time.Duration(len(rpcs)-i)
			actx, acancel = context.WithTimeout(ctx, per)
		}
		frozen, c := evmFreezeCallOnce(actx, u, contract, freezeSel, addr)
		acancel()
		if c == "" {
			return frozen, ""
		}
		code = c
		if ctx.Err() != nil {
			return false, "timeout"
		}
	}
	return false, code
}

func checkTronContractFreeze(ctx context.Context, contract, freezeSel, addr string) (bool, string) {
	hex20, err := tronBase58ToHex(addr)
	if err != nil {
		return false, "bad_request"
	}
	fnSig := freezeSelectorSignatures[freezeSel]
	if fnSig == "" {
		fnSig = "isBlackListed(address)"
	}
	payload, err := json.Marshal(map[string]any{
		"owner_address":     addr,
		"contract_address":  contract,
		"function_selector": fnSig,
		"parameter":         strings.Repeat("0", 24) + hex20,
		"visible":           true,
	})
	if err != nil {
		return false, "bad_request"
	}
	body, code := tronGridDo(ctx, "POST", "/wallet/triggerconstantcontract", payload)
	if code != "" {
		return false, code
	}
	var parsed struct {
		Result struct {
			Result bool `json:"result"`
		} `json:"result"`
		ConstantResult []string `json:"constant_result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || !parsed.Result.Result || len(parsed.ConstantResult) == 0 {
		return false, "bad_response"
	}
	raw := strings.TrimSpace(parsed.ConstantResult[0])
	if !strings.HasPrefix(strings.ToLower(raw), "0x") {
		raw = "0x" + raw
	}
	return parseBool32Hex(raw)
}

// checkIssuerFreeze checks whether addr is currently frozen by any registry
// contract with a FreezeSel on chain. When assetFilter is empty, all assets on
// chain are checked (Address Risk); otherwise only that asset (Payment Check).
// Any non-32-byte-boolean response, RPC error, or timeout returns an error
// code and never "not frozen".
func checkIssuerFreeze(ctx context.Context, chain, addr, assetFilter string, rpcs []string) ([]issuerFreezeState, string) {
	info, ok := chains[chain]
	if !ok {
		return nil, "bad_request"
	}
	var toks []registryToken
	for _, t := range registryTokens {
		if t.Network == chain && t.FreezeSel != "" && (assetFilter == "" || t.Asset == assetFilter) {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()

	switch info.Family {
	case familyEVM:
		if len(rpcs) == 0 {
			return nil, "unavailable"
		}
		states := make([]issuerFreezeState, len(toks))
		codes := make([]string, len(toks))
		var wg sync.WaitGroup
		for i, tok := range toks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				frozen, c := checkEVMContractFreeze(ctx, rpcs, tok.Contract, tok.FreezeSel, addr)
				states[i] = issuerFreezeState{Token: tok.Label, Contract: tok.Contract, Frozen: frozen}
				codes[i] = c
			}()
		}
		wg.Wait()
		for _, c := range codes {
			if c != "" {
				return nil, c
			}
		}
		return states, ""

	case familyTron:
		states := make([]issuerFreezeState, 0, len(toks))
		for _, tok := range toks {
			frozen, c := checkTronContractFreeze(ctx, tok.Contract, tok.FreezeSel, addr)
			if c != "" {
				return nil, c
			}
			states = append(states, issuerFreezeState{Token: tok.Label, Contract: tok.Contract, Frozen: frozen})
		}
		return states, ""

	default:
		return nil, ""
	}
}

// issuerFreezeClues converts live freeze states into critical clues,
// deduplicating against local stablecoin freeze history on Ethereum when the
// local database already records the same token as currently frozen.
func issuerFreezeClues(chain string, live []issuerFreezeState, localStates []stablecoinState, asOf string) []riskClue {
	localFrozen := map[string]bool{}
	if chain == "eth" {
		for _, st := range localStates {
			if st.Action == "freeze" || st.Action == "destroy" {
				localFrozen[st.Token] = true
			}
		}
	}
	info := chains[chain]
	var out []riskClue
	for _, st := range live {
		if !st.Frozen {
			continue
		}
		if chain == "eth" && localFrozen[st.Token] {
			continue
		}
		out = append(out, riskClue{
			Severity: sevCritical,
			Source:   "issuer_freeze",
			Token:    st.Token,
			Code:     "issuer_frozen_live",
			Title:    fmt.Sprintf("Currently frozen by the %s contract (live check)", st.Token),
			AsOf:     asOf,
			RefURL:   fmt.Sprintf(info.AddressURL, st.Contract),
		})
	}
	return out
}
