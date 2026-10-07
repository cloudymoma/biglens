package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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
	if strings.HasPrefix(path, "/v1/") && tronGridLimiter.Limit() != rate.Inf {
		if err := tronGridV1Limiter.Wait(ctx); err != nil {
			return nil, "rate_limited_local"
		}
	}
	if err := tronGridLimiter.Wait(ctx); err != nil {
		return nil, "rate_limited_local"
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
		return nil, "bad_request"
	}
	req.Header.Set("User-Agent", evmUserAgent)
	if len(reqBody) > 0 {
		req.Header.Set("Content-Type", "application/json")
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
	return body, ""
}
