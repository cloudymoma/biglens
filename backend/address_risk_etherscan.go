package main

// Etherscan V2 client for the Address Risk association analysis (spec §5.4,
// §8.4). Every request goes through one process-wide limiter shared by
// lookups and key validation: 2.5 tokens/s with burst 1 keeps any 1-second
// window at ≤3 requests, the free tier's limit. Failures map to the error
// enum; err.Error() is never used because *url.Error embeds the apikey.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/time/rate"
)

const (
	etherscanSignupURL  = "https://etherscan.io/myapikey"
	etherscanHelpURL    = "https://docs.etherscan.io/set-up-your-api-key"
	etherscanPageSize   = "1000" // free-tier maximum per request
	blockscoutUserAgent = "Mozilla/5.0 (compatible; BigLens/1.0)"
)

var (
	etherscanBaseURL  = "https://api.etherscan.io/v2/api"
	blockscoutBaseURL = "https://eth.blockscout.com"
	etherscanLimiter  = rate.NewLimiter(2.5, 1)
)

type etherscanEnvelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

// classifyEtherscanMessage maps a status "0" string result (spec §5.4).
func classifyEtherscanMessage(msg string) string {
	switch {
	case strings.Contains(msg, "Too many invalid api key attempts"):
		return "key_throttled"
	case strings.Contains(msg, "Invalid API Key"), strings.Contains(msg, "Missing/Invalid API Key"):
		return "key_invalid"
	case strings.HasPrefix(msg, "Max rate limit"), strings.HasPrefix(msg, "Max calls per sec"):
		return "rate_limited"
	default:
		return "bad_response"
	}
}

// etherscanCall performs one GET. It returns the raw result on success; on
// failure an error code plus, for status "0" string results, Etherscan's own
// message (shown to the user only when validating a key).
func etherscanCall(ctx context.Context, key string, params url.Values) (json.RawMessage, string, string) {
	if err := etherscanLimiter.Wait(ctx); err != nil {
		return nil, "", "rate_limited_local"
	}
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("chainid", "1")
	q.Set("apikey", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, etherscanBaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", "bad_request"
	}
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, "", upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, 8<<20)
	if err != nil {
		return nil, "", "bad_response"
	}
	var env etherscanEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "", "bad_response"
	}
	switch env.Status {
	case "1":
		return env.Result, "", ""
	case "0":
		var msg string
		if err := json.Unmarshal(env.Result, &msg); err == nil {
			return nil, msg, classifyEtherscanMessage(msg)
		}
		if strings.HasPrefix(strings.TrimSpace(string(env.Result)), "[") {
			return env.Result, "", "" // "No transactions found": ok, zero rows
		}
	}
	return nil, "", "bad_response"
}

// etherscanRow covers txlist, tokentx and txlistinternal; fields absent from
// a list stay "". All values are decimal strings. Blockscout's txlistinternal
// names the tx hash field transactionHash.
type etherscanRow struct {
	Hash            string `json:"hash"`
	TransactionHash string `json:"transactionHash"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`
	IsError         string `json:"isError"`
	TimeStamp       string `json:"timeStamp"`
	ContractAddress string `json:"contractAddress"`
}

func normalizeEtherscanRows(rows []etherscanRow) []etherscanRow {
	for i := range rows {
		if rows[i].Hash == "" && rows[i].TransactionHash != "" {
			rows[i].Hash = rows[i].TransactionHash
		}
	}
	return rows
}

// etherscanList fetches the newest 1000 rows of one account action. On
// failure it returns Etherscan's own message (if any) and the error code.
func etherscanList(ctx context.Context, key, action, addr string) ([]etherscanRow, string, string) {
	raw, msg, code := etherscanCall(ctx, key, url.Values{
		"module": {"account"}, "action": {action}, "address": {addr},
		"page": {"1"}, "offset": {etherscanPageSize}, "sort": {"desc"},
	})
	if code != "" {
		return nil, msg, code
	}
	var rows []etherscanRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, "", "bad_response"
	}
	return normalizeEtherscanRows(rows), "", ""
}

// blockscoutListAt fetches the newest 1000 rows of one account action from
// Blockscout's keyless Etherscan-compatible RPC endpoint.
func blockscoutListAt(ctx context.Context, baseURL, action, addr string) ([]etherscanRow, string) {
	if baseURL == "" {
		return nil, "not_configured"
	}
	q := url.Values{
		"module": {"account"}, "action": {action}, "address": {addr},
		"page": {"1"}, "offset": {etherscanPageSize}, "sort": {"desc"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api?"+q.Encode(), nil)
	if err != nil {
		return nil, "bad_request"
	}
	req.Header.Set("User-Agent", blockscoutUserAgent)
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
	body, err := readCapped(resp.Body, 8<<20)
	if err != nil {
		return nil, "bad_response"
	}
	var env etherscanEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "bad_response"
	}
	// Blockscout returns status "1" (ok), "2" (partial internal index with a
	// valid array result), or "0" with result [] when no rows exist.
	if (env.Status == "1" || env.Status == "2" || env.Status == "0") &&
		strings.HasPrefix(strings.TrimSpace(string(env.Result)), "[") {
		var rows []etherscanRow
		if err := json.Unmarshal(env.Result, &rows); err != nil {
			return nil, "bad_response"
		}
		return normalizeEtherscanRows(rows), ""
	}
	return nil, "bad_response"
}
