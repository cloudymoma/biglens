package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

var (
	tronGridBaseURL   = "https://api.trongrid.io"
	tronGridLimiter   = rate.NewLimiter(3, 1)
	tronGridV1Limiter = rate.NewLimiter(rate.Every(1600*time.Millisecond), 1)
)

// tronGridDo sends one HTTP request to TronGrid through the process-wide rate
// limiter and returns the body or an error-enum code (never err.Error()).
// TronGrid's keyless /v1/ account transaction endpoints enforce allowed_rps(1)
// and suspend the IP for 5s when exceeded, so /v1/ paths are additionally paced
// by tronGridV1Limiter.
func tronGridDo(ctx context.Context, method, path string, reqBody []byte) ([]byte, string) {
	body, code, _ := tronGridDoRetryAfter(ctx, method, path, reqBody)
	return body, code
}

// tronGridDoRetryAfter is tronGridDo that also returns the Retry-After of an
// HTTP 429 (0 when absent or unparseable); callers decide whether to retry.
func tronGridDoRetryAfter(ctx context.Context, method, path string, reqBody []byte) ([]byte, string, time.Duration) {
	if strings.HasPrefix(path, "/v1/") && tronGridLimiter.Limit() != rate.Inf {
		if err := tronGridV1Limiter.Wait(ctx); err != nil {
			return nil, "rate_limited_local", 0
		}
	}
	if err := tronGridLimiter.Wait(ctx); err != nil {
		return nil, "rate_limited_local", 0
	}
	reqCtx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()
	u := strings.TrimRight(tronGridBaseURL, "/") + path
	var bodyReader io.Reader
	if len(reqBody) > 0 {
		bodyReader = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u, bodyReader)
	if err != nil {
		return nil, "bad_request", 0
	}
	req.Header.Set("User-Agent", evmUserAgent)
	if len(reqBody) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err), 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "rate_limited", parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode), 0
	}
	body, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return nil, "bad_response", 0
	}
	return body, "", 0
}

// parseRetryAfter reads a Retry-After header: delay seconds or an HTTP date
// (RFC 9110 §10.2.3). Absent, unparseable or past values are 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
