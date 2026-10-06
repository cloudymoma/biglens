package main

// Live sources for the Address Risk lookup. Each check gets its own
// riskSourceTimeout budget and reports failures as an error-enum string,
// never as err.Error() (see upstreamErrCode).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// riskSourceTimeout bounds each live source. A lookup's worst case is three
	// budgets in sequence — Etherscan, then the Blockscout fallback, then live
	// counterparty screening — about 15 s. Keep 3× this well under the
	// server's 60 s WriteTimeout (main.go).
	riskSourceTimeout = 5 * time.Second
	riskHTTPClient    = &http.Client{Timeout: 10 * time.Second} // per-source ctx is the real budget
)

const (
	riskOracleContract = "0x40C57923924B5c5c5455c48D93317139ADDaC8fb"
	riskOracleSelector = "0xdf592f7d" // keccak256("isSanctioned(address)")[:4]
	riskOracleFalse    = "0x0000000000000000000000000000000000000000000000000000000000000000"
	riskOracleTrue     = "0x0000000000000000000000000000000000000000000000000000000000000001"
	riskMaxBody        = 1 << 20
)

func oracleCalldata(addr string) string {
	return riskOracleSelector + strings.Repeat("0", 24) + strings.TrimPrefix(addr, "0x")
}

func parseBool32Hex(res string) (bool, string) {
	switch strings.ToLower(res) {
	case riskOracleTrue:
		return true, ""
	case riskOracleFalse:
		return false, ""
	default:
		return false, "bad_response"
	}
}

// parseBool32Response accepts a JSON-RPC response carrying one of the two
// 32-byte booleans. Anything else — "0x" (no contract or function on that
// chain), an error object, or a result object wrapped in HTTP 200 — is an
// error, never "false".
func parseBool32Response(body []byte) (bool, string) {
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, "bad_response"
	}
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		return false, "bad_response"
	}
	var res string
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return false, "bad_response"
	}
	return parseBool32Hex(res)
}

func parseOracleResponse(body []byte) (bool, string) {
	return parseBool32Response(body)
}

func oracleCallOnce(ctx context.Context, rpcURL, addr string) (bool, string) {
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"%s","data":"%s"},"latest"]}`,
		riskOracleContract, oracleCalldata(addr))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader([]byte(payload)))
	if err != nil {
		return false, "bad_request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", evmUserAgent)
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return false, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return false, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return false, "bad_response"
	}
	return parseOracleResponse(body)
}

// checkOracle tries each RPC in order within one riskSourceTimeout budget and
// returns the first definitive answer; otherwise the last error code. The
// remaining budget is split evenly over the RPCs still to try, so a first node
// that hangs without answering cannot use up the whole budget before failover.
func checkOracle(ctx context.Context, rpcURLs []string, addr string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	code := "unavailable"
	for i, u := range rpcURLs {
		per := time.Until(deadline) / time.Duration(len(rpcURLs)-i)
		actx, acancel := context.WithTimeout(ctx, per)
		hit, c := oracleCallOnce(actx, u, addr)
		acancel()
		if c == "" {
			return hit, ""
		}
		code = c
		if ctx.Err() != nil {
			return false, "timeout"
		}
	}
	return false, code
}

const (
	sevCritical    = "critical"
	sevWarning     = "warning"
	sevAssociation = "association"
	sevInfo        = "info"
)

var sevRank = map[string]int{sevCritical: 0, sevWarning: 1, sevAssociation: 2, sevInfo: 3}

// riskClue is one piece of evidence. ObservedAt is a fact time from the
// source (freeze block time, list date); nil when the source gives none.
type riskClue struct {
	Severity   string  `json:"severity"`
	Source     string  `json:"source"`
	Token      string  `json:"token"`
	Flag       string  `json:"flag"`
	Code       string  `json:"code"`
	Title      string  `json:"title"`
	Detail     string  `json:"detail"`
	ObservedAt *string `json:"observed_at"`
	AsOf       string  `json:"as_of"`
	RefURL     string  `json:"ref_url"`
	// Association is set only on Etherscan association clues (spec §8.4).
	Association *riskAssociation `json:"association,omitempty"`
}

type goplusRule struct {
	Severity string
	Title    string // for Count rules: a fmt pattern with one %d
	Count    bool
}

// goplusRules maps GoPlus address_security fields to tiers (spec §5.2).
// contract_address and data_source are metadata and deliberately absent;
// unknown future fields are ignored.
var goplusRules = map[string]goplusRule{
	"sanctioned":                            {sevCritical, "GoPlus: sanctioned address", false},
	"phishing_activities":                   {sevWarning, "GoPlus: phishing activities", false},
	"stealing_attack":                       {sevWarning, "GoPlus: stealing attack", false},
	"cybercrime":                            {sevWarning, "GoPlus: cybercrime", false},
	"money_laundering":                      {sevWarning, "GoPlus: money laundering", false},
	"financial_crime":                       {sevWarning, "GoPlus: financial crime", false},
	"blackmail_activities":                  {sevWarning, "GoPlus: blackmail activities", false},
	"darkweb_transactions":                  {sevWarning, "GoPlus: darkweb transactions", false},
	"fake_kyc":                              {sevWarning, "GoPlus: fake KYC", false},
	"honeypot_related_address":              {sevWarning, "GoPlus: honeypot-related address", false},
	"malicious_mining_activities":           {sevWarning, "GoPlus: malicious mining activities", false},
	"mixer":                                 {sevWarning, "GoPlus: coin mixer", false},
	"gas_abuse":                             {sevWarning, "GoPlus: gas abuse", false},
	"fake_token":                            {sevWarning, "GoPlus: counterfeit token contract", false},
	"fake_standard_interface":               {sevWarning, "GoPlus: non-standard contract interface (common in scam assets)", false},
	"blacklist_doubt":                       {sevWarning, "GoPlus: suspected malicious (unconfirmed)", false},
	"reinit":                                {sevInfo, "GoPlus: contract can be re-initialized", false},
	"number_of_malicious_contracts_created": {sevWarning, "GoPlus: created %d malicious contracts", true},
}

var goplusBaseURL = "https://api.gopluslabs.io/api/v1/address_security/"

func goplusDataSource(v any) string {
	s, _ := v.(string)
	var parts []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "data source: " + strings.Join(parts, ", ")
}

func goplusClues(result map[string]any, addr, asOf string) []riskClue {
	detail := goplusDataSource(result["data_source"])
	var out []riskClue
	for field, rule := range goplusRules {
		v, _ := result[field].(string) // "" means unknown, not 0
		title := rule.Title
		if rule.Count {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				continue
			}
			title = fmt.Sprintf(rule.Title, n)
		} else if v != "1" {
			continue
		}
		out = append(out, riskClue{
			Severity: rule.Severity, Source: "goplus", Flag: field, Code: "goplus_flag",
			Title: title, Detail: detail, AsOf: asOf,
			RefURL: "https://console.gopluslabs.io/malicious-address-detection/" + addr,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if sevRank[out[i].Severity] != sevRank[out[j].Severity] {
			return sevRank[out[i].Severity] < sevRank[out[j].Severity]
		}
		return out[i].Flag < out[j].Flag
	})
	return out
}

// checkGoPlus reports GoPlus failures carried inside HTTP 200 (code != 1) as
// errors; a rate-limited reply must never read as "no hits".
func checkGoPlus(ctx context.Context, chainID, addr string, now time.Time) ([]riskClue, string) {
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, goplusBaseURL+addr+"?chain_id="+chainID, nil)
	if err != nil {
		return nil, "bad_request"
	}
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return nil, "bad_response"
	}
	var parsed struct {
		Code   int             `json:"code"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "bad_response"
	}
	switch parsed.Code {
	case 1:
	case 2:
		return nil, "pending"
	case 4029:
		return nil, "rate_limited"
	default:
		return nil, "bad_response"
	}
	var result map[string]any
	if err := json.Unmarshal(parsed.Result, &result); err != nil || result == nil {
		return nil, "bad_response"
	}
	return goplusClues(result, addr, fmtTime(now)), ""
}

const (
	riskTitleBlockscoutScam   = "Blockscout: flagged as scam or phishing address"
	riskTitleBlockscoutTagFmt = "Blockscout risk tag: %s"
)

// blockscoutRiskTagRe matches risk words in public/metadata tags (never
// contract self-names). The tag is usually spelled "Tornado.Cash".
var blockscoutRiskTagRe = regexp.MustCompile(`(?i)(?:\b|_)(phishing|scam|scammer|heist|exploiter|drainer|lazarus|tornado[\s._-]*cash|malicious|blackmail|darknet|rugpull|honeypot)(?:\b|[0-9_])`)

// blockscoutVendorRe strips security-vendor names that contain a risk word
// ("Scam Sniffer" is a company, not a verdict) before matching.
var blockscoutVendorRe = regexp.MustCompile(`(?i)scam[\s_-]*sniffer`)

// blockscoutRiskTag reports whether a tag's text names a risk.
func blockscoutRiskTag(tag string) bool {
	return blockscoutRiskTagRe.MatchString(blockscoutVendorRe.ReplaceAllString(tag, " "))
}

type blockscoutTag struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Label       string `json:"label"`
}

func (t blockscoutTag) text() string {
	for _, s := range []string{t.DisplayName, t.Label, t.Name} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// checkBlockscoutAddress inspects Blockscout V2 address metadata for scam
// reputation and high-risk public/metadata tags (never contract self-names).
func checkBlockscoutAddress(ctx context.Context, baseURL, addr string, now time.Time) ([]riskClue, string) {
	if baseURL == "" {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	base := strings.TrimRight(baseURL, "/")
	u := base + "/api/v2/addresses/" + addr
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "bad_request"
	}
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ""
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return nil, "bad_response"
	}
	var parsed struct {
		IsScam     bool            `json:"is_scam"`
		Reputation string          `json:"reputation"`
		PublicTags []blockscoutTag `json:"public_tags"`
		Metadata   *struct {
			Tags []blockscoutTag `json:"tags"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "bad_response"
	}
	asOf := fmtTime(now)
	ref := base + "/address/" + addr
	var out []riskClue
	if parsed.IsScam || strings.EqualFold(parsed.Reputation, "scam") {
		out = append(out, riskClue{
			Severity: sevWarning, Source: "blockscout", Flag: "is_scam", Code: "blockscout_scam",
			Title: riskTitleBlockscoutScam, AsOf: asOf, RefURL: ref,
		})
	}
	var allTags []string
	for _, t := range parsed.PublicTags {
		if s := t.text(); s != "" {
			allTags = append(allTags, s)
		}
	}
	if parsed.Metadata != nil {
		for _, t := range parsed.Metadata.Tags {
			if s := t.text(); s != "" {
				allTags = append(allTags, s)
			}
		}
	}
	var riskTags []string
	for _, tag := range allTags {
		if blockscoutRiskTag(tag) && !slices.Contains(riskTags, tag) {
			riskTags = append(riskTags, tag)
		}
	}
	if len(riskTags) > 0 {
		out = append(out, riskClue{
			Severity: sevWarning, Source: "blockscout", Flag: "risk_tag", Code: "blockscout_tag",
			Title: fmt.Sprintf(riskTitleBlockscoutTagFmt, strings.Join(riskTags, ", ")),
			AsOf:  asOf, RefURL: ref,
		})
	}
	return out, ""
}
