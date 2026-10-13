package main

// EIP-7702 delegation detection: the live Address Risk source "eip7702"
// (Ethereum, Arbitrum, Optimism, Base) and the Payment Check own-address
// alert. eth_getCode at "latest" tells three cases apart:
//
//	"0x"                        plain EOA (no clue)
//	0xef0100 ‖ 20-byte address  EOA whose code is delegated (EIP-7702)
//	any other bytecode          smart contract
//
// BigQuery's goog_blockchain accounts views are deliberately not used: on
// 2026-10-13 they carried duplicate rows per address, is_contract = true for
// code "0x", and JSON-quoted code values, so only a live RPC read is reliable.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	evmCodeEOA       = "eoa"
	evmCodeDelegated = "delegated"
	evmCodeContract  = "contract"

	// eip7702Designator prefixes the 23-byte delegation designator
	// 0xef0100 ‖ address (EIP-7702 "Delegation designation").
	eip7702Designator    = "ef0100"
	eip7702DesignatorLen = 2 * 23 // hex digits
)

var errBadEVMCode = errors.New("malformed eth_getCode result")

// eip7702Chains are the chains whose lookups run the "eip7702" source.
var eip7702Chains = map[string]bool{"eth": true, "arb": true, "op": true, "base": true}

type evmCodeInfo struct {
	Kind     string // evmCodeEOA | evmCodeDelegated | evmCodeContract
	Delegate string // lowercase 0x address when Kind == evmCodeDelegated
}

// classifyEVMCode interprets an eth_getCode result. Anything that is not
// 0x-prefixed, even-length hex is an error, never "plain EOA".
func classifyEVMCode(code string) (evmCodeInfo, error) {
	h, ok := strings.CutPrefix(code, "0x")
	if !ok || len(h)%2 != 0 {
		return evmCodeInfo{}, errBadEVMCode
	}
	if _, err := hex.DecodeString(h); err != nil {
		return evmCodeInfo{}, errBadEVMCode
	}
	h = strings.ToLower(h)
	switch {
	case h == "":
		return evmCodeInfo{Kind: evmCodeEOA}, nil
	case len(h) == eip7702DesignatorLen && strings.HasPrefix(h, eip7702Designator):
		delegate := "0x" + h[len(eip7702Designator):]
		if delegate == riskZeroAddress {
			// Delegating to the zero address clears the code (EIP-7702), so
			// this designator cannot be active; never report it as one.
			return evmCodeInfo{Kind: evmCodeEOA}, nil
		}
		return evmCodeInfo{Kind: evmCodeDelegated, Delegate: delegate}, nil
	default:
		return evmCodeInfo{Kind: evmCodeContract}, nil
	}
}

// checkEIP7702 reads addr's code at "latest" from the first RPC that gives a
// well-formed answer, within one riskSourceTimeout budget shared by all RPCs.
func checkEIP7702(ctx context.Context, rpcs []string, addr string) (evmCodeInfo, string) {
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	return evmRPCFailover(ctx, rpcs, func(actx context.Context, rpcURL string) (evmCodeInfo, string) {
		code, err := evmRPC(actx, rpcURL, "eth_getCode", addr, "latest")
		if err != nil {
			return evmCodeInfo{}, evmRPCErrCode(err)
		}
		info, err := classifyEVMCode(code)
		if err != nil {
			return evmCodeInfo{}, "bad_response"
		}
		return info, ""
	})
}

const (
	riskEIP7702TitleFmt        = "EIP-7702 delegation active → %s"
	riskEIP7702FlaggedTitleFmt = "EIP-7702 delegation active → %s (delegate flagged as scam on Blockscout)"
	riskEIP7702NamedTitleFmt   = "EIP-7702 delegation active → delegated to verified contract %s (%s)"
	riskEIP7702Advice          = "If you did not authorize this, the account may be controlled by a sweeper contract; " +
		"revoke by signing a new authorization to the zero address."
	riskTitleContract = "This address is a smart contract (has bytecode)."

	maxContractNameRunes = 64
)

// displayContractName makes an attacker-chosen contract name safe to put in a
// one-line title: control characters become spaces and length is capped.
// (React escapes it on render; this only keeps titles readable.)
func displayContractName(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if n == maxContractNameRunes {
			b.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// eip7702Clues turns one code classification into at most one clue. For a
// delegated EOA the Blockscout view of the delegate sets the severity:
// flagged → Critical, verified with a name → Info, anything else (unverified,
// unnamed, unknown, or the lookup failed or is disabled) → Warning, so a
// failed enrichment never drops the core clue.
func eip7702Clues(chain, addr string, info evmCodeInfo, bs blockscoutAddressInfo, bsCode string, bsDisabled bool, asOf string) []riskClue {
	explorer := chains[chain].AddressURL
	switch info.Kind {
	case evmCodeContract:
		return []riskClue{{Severity: sevInfo, Source: "eip7702", Code: "address_is_contract",
			Title: riskTitleContract, AsOf: asOf, RefURL: fmt.Sprintf(explorer, addr) + "#code"}}
	case evmCodeDelegated:
	default:
		return nil
	}
	d := info.Delegate
	c := riskClue{Severity: sevWarning, Source: "eip7702", Code: "eip7702_delegated", AsOf: asOf,
		Title: fmt.Sprintf(riskEIP7702TitleFmt, d), RefURL: fmt.Sprintf(explorer, d)}
	name := displayContractName(bs.Name)
	switch {
	case bsDisabled:
		c.Flag = "delegate_unchecked"
		c.Detail = riskEIP7702Advice + " Delegate reputation not checked (Blockscout is disabled for this chain on this server)."
	case bsCode != "":
		c.Flag = "delegate_unchecked"
		c.Detail = riskEIP7702Advice + fmt.Sprintf(" Delegate reputation could not be checked (%s).", bsCode)
	case bs.Scam || len(bs.RiskTags) > 0:
		c.Severity, c.Flag = sevCritical, "delegate_flagged"
		c.Title = fmt.Sprintf(riskEIP7702FlaggedTitleFmt, d)
		var why []string
		if bs.Scam {
			why = append(why, "scam reputation")
		}
		if len(bs.RiskTags) > 0 {
			why = append(why, "risk tags: "+strings.Join(bs.RiskTags, ", "))
		}
		c.Detail = "Blockscout flags the delegate contract (" + strings.Join(why, "; ") + "). " + riskEIP7702Advice
	case !bs.Found:
		c.Flag = "delegate_unknown"
		c.Detail = "Blockscout has no record of the delegate contract. " + riskEIP7702Advice
	case bs.IsVerified && name != "":
		c.Severity, c.Flag = sevInfo, "delegate_verified"
		c.Title = fmt.Sprintf(riskEIP7702NamedTitleFmt, name, d)
		c.Detail = "Blockscout lists verified source code for the delegate (not a safety rating). " + riskEIP7702Advice
	default:
		c.Flag = "delegate_unverified"
		c.Detail = "The delegate contract has no verified source code with a name on Blockscout. " + riskEIP7702Advice
	}
	return []riskClue{c}
}

// checkEIP7702Source runs the "eip7702" source for one lookup: eth_getCode on
// the chain's RPC list (the address is sent there), then — only for a
// delegated EOA — the delegate contract (not the address) on the chain's
// Blockscout. noCache is set when the delegate check failed transiently.
func (s *addressRiskService) checkEIP7702Source(ctx context.Context, chain, addr string, now time.Time) (src riskSource, clues []riskClue, noCache bool) {
	rpcs := s.rpcsFor(chain)
	src = riskSource{ID: "eip7702", Status: "ok", Hosts: hostsOf(rpcs...), SendsAddress: true}
	if len(rpcs) == 0 {
		src.Status = "not_configured"
		return src, nil, false
	}
	info, code := checkEIP7702(ctx, rpcs, addr)
	if code != "" {
		src.Status, src.Error = "error", code
		return src, nil, false
	}
	var (
		bs     blockscoutAddressInfo
		bsCode string
	)
	bsURL := s.blockscoutURL(chain)
	if info.Kind == evmCodeDelegated && bsURL != "" {
		bs, bsCode = fetchBlockscoutAddress(ctx, bsURL, info.Delegate)
	}
	clues = eip7702Clues(chain, addr, info, bs, bsCode, info.Kind == evmCodeDelegated && bsURL == "", fmtTime(now))
	return src, clues, bsCode != ""
}
