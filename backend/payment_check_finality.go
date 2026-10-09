package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type payLevel string

const (
	levelDanger    payLevel = "DANGER"
	levelSoft      payLevel = "SOFT"
	levelSafe      payLevel = "SAFE"
	levelFinalized payLevel = "FINALIZED"

	// tronBlockSeconds is TRON's protocol block production interval (3s).
	tronBlockSeconds = 3

	// payUpstreamTimeout bounds each Payment Check upstream call (Blockscout on
	// Arbitrum can take up to 19s under load; we cap at 8s per spec P1.0).
	payUpstreamTimeout = 8 * time.Second

	lagSampleCap         = 120
	lagMinSampleInterval = 30 * time.Second
)

type payHeads struct {
	Latest      uint64 `json:"latest"`
	Safe        uint64 `json:"safe"`        // 0 on TRON
	Finalized   uint64 `json:"finalized"`   // solidified block on TRON
	LatestTime  int64  `json:"latest_time"` // unix seconds
	SafeTime    int64  `json:"safe_time"`
	FinalTime   int64  `json:"final_time"`
	SafeLagSec  int    `json:"safe_lag_sec"` // 1-hour rolling median (including current sample)
	FinalLagSec int    `json:"final_lag_sec"`
}

type lagSample struct {
	safeLag  int
	finalLag int
}

// lagSampler maintains a 120-slot ring buffer (~1 hour at 1 sample per 30s)
// per chain to compute the median safe/finalized lag without background goroutines.
type lagSampler struct {
	mu      sync.Mutex
	samples []lagSample
	nextIdx int
	lastAt  time.Time
}

func (s *lagSampler) observe(now time.Time, safeLag, finalLag int) (int, int) {
	safeLag = max(0, safeLag)
	finalLag = max(0, finalLag)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAt.IsZero() || now.Sub(s.lastAt) >= lagMinSampleInterval {
		sample := lagSample{safeLag: safeLag, finalLag: finalLag}
		if len(s.samples) < lagSampleCap {
			s.samples = append(s.samples, sample)
		} else {
			s.samples[s.nextIdx] = sample
			s.nextIdx = (s.nextIdx + 1) % lagSampleCap
		}
		s.lastAt = now
	}
	if len(s.samples) == 0 {
		return safeLag, finalLag
	}
	return s.medianLocked()
}

func (s *lagSampler) median() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return 0, 0
	}
	return s.medianLocked()
}

func (s *lagSampler) medianLocked() (int, int) {
	n := len(s.samples)
	safeVals := make([]int, n)
	finalVals := make([]int, n)
	for i, v := range s.samples {
		safeVals[i] = v.safeLag
		finalVals[i] = v.finalLag
	}
	slices.Sort(safeVals)
	slices.Sort(finalVals)
	return (safeVals[(n-1)/2] + safeVals[n/2]) / 2, (finalVals[(n-1)/2] + finalVals[n/2]) / 2
}

// clampHeads enforces monotonic head ordering (spec §5.2): concurrent requests
// for latest/safe/finalized can land on load-balanced nodes at slightly
// different heights.
func clampHeads(h payHeads, tron bool) payHeads {
	if tron {
		h.Safe = 0
		h.SafeTime = 0
		h.SafeLagSec = 0
		if h.Finalized > h.Latest {
			h.Finalized = h.Latest
		}
		if h.LatestTime > 0 && h.FinalTime > h.LatestTime {
			h.FinalTime = h.LatestTime
		}
		return h
	}
	if h.Safe > h.Latest {
		h.Safe = h.Latest
	}
	if h.LatestTime > 0 && h.SafeTime > h.LatestTime {
		h.SafeTime = h.LatestTime
	}
	if h.Finalized > h.Safe {
		h.Finalized = h.Safe
	}
	if h.SafeTime > 0 && h.FinalTime > h.SafeTime {
		h.FinalTime = h.SafeTime
	}
	return h
}

// classifyEVM computes the settlement level, progress percentage, and
// estimated seconds to finality for an EVM transaction (spec §5.2).
// block == 0 means the block number is unknown and is never treated as finalized.
func classifyEVM(block uint64, blockTime int64, failed bool, h payHeads, now int64) (payLevel, int, int) {
	if failed {
		return levelDanger, 0, 0
	}
	if block == 0 {
		return levelSoft, 0, max(0, h.FinalLagSec)
	}
	if block <= h.Finalized {
		return levelFinalized, 100, 0
	}
	var age int64
	if blockTime > 0 {
		age = max(int64(0), now-blockTime)
	}
	eta := max(0, h.FinalLagSec-int(age))
	if block <= h.Safe {
		// F < B <= S guarantees h.Safe > h.Finalized.
		ratio := float64(h.Safe-block) / float64(h.Safe-h.Finalized)
		prog := min(95, max(85, 85+int(math.Round(10*ratio))))
		return levelSafe, prog, eta
	}
	safeLag := max(1, h.SafeLagSec)
	ratio := min(1.0, max(0.0, float64(age)/float64(safeLag)))
	prog := min(75, max(10, 10+int(math.Round(65*ratio))))
	return levelSoft, prog, eta
}

// classifyTron computes the settlement level, progress percentage, and
// estimated seconds to solidification for a TRON transaction (spec §5.2).
func classifyTron(block uint64, blockTime int64, failed bool, h payHeads) (payLevel, int, int) {
	if failed {
		return levelDanger, 0, 0
	}
	if block == 0 {
		if blockTime > 0 && h.FinalTime > 0 && blockTime <= h.FinalTime {
			return levelFinalized, 100, 0
		}
		return levelSoft, 0, max(0, h.FinalLagSec)
	}
	if block <= h.Finalized {
		return levelFinalized, 100, 0
	}
	solid := h.Finalized
	effNow := max(h.Latest, block)
	denom := max(uint64(1), effNow-solid+1)
	ratio := 1.0 - float64(block-solid)/float64(denom)
	prog := min(95, max(0, int(math.Round(95*ratio))))
	eta := int(block-solid) * tronBlockSeconds
	return levelSoft, prog, eta
}

func parseHexUint64(s string) (uint64, bool) {
	digits, ok := strings.CutPrefix(strings.TrimSpace(s), "0x")
	if !ok || digits == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(digits, 16, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func fetchEVMBlockTagOnce(ctx context.Context, rpcURL, tag string) (uint64, int64, string) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_getBlockByNumber",
		"params":  []any{tag, false},
	})
	if err != nil {
		return 0, 0, "bad_request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, "bad_request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", evmUserAgent)
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return 0, 0, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, 0, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	raw, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return 0, 0, "bad_response"
	}
	var out struct {
		Result *struct {
			Number    string `json:"number"`
			Timestamp string `json:"timestamp"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error != nil || out.Result == nil {
		return 0, 0, "bad_response"
	}
	num, ok1 := parseHexUint64(out.Result.Number)
	ts, ok2 := parseHexUint64(out.Result.Timestamp)
	if !ok1 || !ok2 || num == 0 {
		return 0, 0, "bad_response"
	}
	return num, int64(ts), ""
}

func fetchEVMBlockTagWithFailover(ctx context.Context, rpcs []string, tag string) (uint64, int64, string) {
	if len(rpcs) == 0 {
		return 0, 0, "not_configured"
	}
	deadline := time.Now().Add(payUpstreamTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	lastCode := "unavailable"
	for i, u := range rpcs {
		rem := time.Until(deadline)
		if rem <= 0 {
			return 0, 0, "timeout"
		}
		tryCtx, cancel := context.WithTimeout(ctx, rem/time.Duration(len(rpcs)-i))
		num, ts, code := fetchEVMBlockTagOnce(tryCtx, u, tag)
		cancel()
		if code == "" {
			return num, ts, ""
		}
		lastCode = code
	}
	return 0, 0, lastCode
}

// fetchEVMHeads queries latest, safe, and finalized blocks concurrently with
// per-tag RPC failover and returns monotonically clamped heads.
func fetchEVMHeads(ctx context.Context, rpcs []string) (payHeads, error) {
	type tagRes struct {
		num  uint64
		ts   int64
		code string
	}
	tags := [3]string{"latest", "safe", "finalized"}
	var res [3]tagRes
	var wg sync.WaitGroup
	for i, tag := range tags {
		wg.Add(1)
		go func(i int, tag string) {
			defer wg.Done()
			num, ts, code := fetchEVMBlockTagWithFailover(ctx, rpcs, tag)
			res[i] = tagRes{num: num, ts: ts, code: code}
		}(i, tag)
	}
	wg.Wait()
	for _, r := range res {
		if r.code != "" {
			return payHeads{}, errors.New(r.code)
		}
	}
	h := clampHeads(payHeads{
		Latest:     res[0].num,
		Safe:       res[1].num,
		Finalized:  res[2].num,
		LatestTime: res[0].ts,
		SafeTime:   res[1].ts,
		FinalTime:  res[2].ts,
	}, false)
	h.SafeLagSec = max(1, int(h.LatestTime-h.SafeTime))
	h.FinalLagSec = max(1, int(h.LatestTime-h.FinalTime))
	return h, nil
}

func fetchTronBlockOnce(ctx context.Context, path string) (uint64, int64, string) {
	raw, code := tronGridDo(ctx, http.MethodPost, path, nil)
	if code != "" {
		return 0, 0, code
	}
	var out struct {
		BlockHeader struct {
			RawData struct {
				Number    uint64 `json:"number"`
				Timestamp int64  `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.BlockHeader.RawData.Number == 0 {
		return 0, 0, "bad_response"
	}
	ts := out.BlockHeader.RawData.Timestamp
	if ts > 1e11 {
		ts /= 1000 // TronGrid returns unix milliseconds
	}
	return out.BlockHeader.RawData.Number, ts, ""
}

// fetchTronHeads queries /wallet/getnowblock and /walletsolidity/getnowblock
// concurrently via tronGridDo and returns monotonically clamped heads.
func fetchTronHeads(ctx context.Context) (payHeads, error) {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	var (
		latNum, solNum   uint64
		latTime, solTime int64
		latCode, solCode string
		wg               sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		latNum, latTime, latCode = fetchTronBlockOnce(ctx, "/wallet/getnowblock")
	}()
	go func() {
		defer wg.Done()
		solNum, solTime, solCode = fetchTronBlockOnce(ctx, "/walletsolidity/getnowblock")
	}()
	wg.Wait()
	if latCode != "" {
		return payHeads{}, errors.New(latCode)
	}
	if solCode != "" {
		return payHeads{}, errors.New(solCode)
	}
	h := clampHeads(payHeads{
		Latest:     latNum,
		Finalized:  solNum,
		LatestTime: latTime,
		FinalTime:  solTime,
	}, true)
	lag := int(h.LatestTime - h.FinalTime)
	if lag <= 0 && h.Latest > h.Finalized {
		lag = int(h.Latest-h.Finalized) * tronBlockSeconds
	}
	h.FinalLagSec = max(1, lag)
	return h, nil
}

const btcAvgBlockSec = 600

var (
	esploraFallbackAPI           = "https://blockstream.info/api"
	defaultMempoolURLForFailover = defaultMempoolBaseURL
)

func esploraAPIBases() []string {
	primary := strings.TrimSuffix(strings.TrimRight(mempoolBaseURL, "/"), "/api") + "/api"
	bases := []string{primary}
	if strings.TrimRight(mempoolBaseURL, "/") == strings.TrimRight(defaultMempoolURLForFailover, "/") && esploraFallbackAPI != "" {
		bases = append(bases, strings.TrimRight(esploraFallbackAPI, "/"))
	}
	return bases
}

func esploraGet(ctx context.Context, path string) ([]byte, string) {
	bases := esploraAPIBases()
	deadline := time.Now().Add(payUpstreamTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	lastCode := "unavailable"
	for i, base := range bases {
		rem := time.Until(deadline)
		if rem <= 0 {
			return nil, "timeout"
		}
		tryCtx, cancel := context.WithTimeout(ctx, rem/time.Duration(len(bases)-i))
		req, err := http.NewRequestWithContext(tryCtx, http.MethodGet, base+path, nil)
		if err != nil {
			cancel()
			return nil, "bad_request"
		}
		req.Header.Set("User-Agent", "biglens/1.0")
		resp, err := mempoolHTTPClient.Do(req)
		if err != nil {
			cancel()
			lastCode = upstreamErrCode(err)
			if ctx.Err() != nil {
				return nil, "timeout"
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			cancel()
			lastCode = "rate_limited"
			continue
		}
		if resp.StatusCode != http.StatusOK {
			code := fmt.Sprintf("upstream_http_%d", resp.StatusCode)
			resp.Body.Close()
			cancel()
			lastCode = code
			continue
		}
		raw, err := readCapped(resp.Body, 8<<20)
		resp.Body.Close()
		cancel()
		if err != nil {
			lastCode = "bad_response"
			continue
		}
		return raw, ""
	}
	return nil, lastCode
}

// classifyBTC computes settlement level, progress percentage, and estimated
// seconds remaining for a Bitcoin transaction using monotonically clamped heads
// (spec P3.1 §2, review B2 & S9).
func classifyBTC(block uint64, h payHeads) (payLevel, int, int) {
	if block == 0 {
		return levelDanger, 0, 6 * btcAvgBlockSec
	}
	if block <= h.Finalized {
		return levelFinalized, 100, 0
	}
	confs := uint64(1)
	if h.Latest >= block {
		confs = h.Latest - block + 1
	}
	eta := max(1, 6-int(confs)) * btcAvgBlockSec
	if block <= h.Safe {
		prog := min(90, max(70, 70+10*int(confs-3)))
		return levelSafe, prog, eta
	}
	prog := min(40, max(20, 20+20*int(confs-1)))
	return levelSoft, prog, eta
}

func fetchBTCHeads(ctx context.Context) (payHeads, error) {
	raw, code := esploraGet(ctx, "/blocks")
	if code != "" {
		return payHeads{}, errors.New(code)
	}
	var blocks []struct {
		Height    uint64 `json:"height"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) < 6 || blocks[0].Height == 0 {
		return payHeads{}, errors.New("bad_response")
	}
	h := clampHeads(payHeads{
		Latest:     blocks[0].Height,
		Safe:       blocks[2].Height,
		Finalized:  blocks[5].Height,
		LatestTime: blocks[0].Timestamp,
		SafeTime:   blocks[2].Timestamp,
		FinalTime:  blocks[5].Timestamp,
	}, false)
	h.SafeLagSec = max(1, int(h.LatestTime-h.SafeTime))
	h.FinalLagSec = max(1, int(h.LatestTime-h.FinalTime))
	return h, nil
}

// classifySolana computes settlement level, progress percentage, and estimated
// seconds remaining for a Solana transaction using slot watermarks (spec P3.1 §2).
func classifySolana(slot uint64, blockTime int64, failed bool, h payHeads, now int64) (payLevel, int, int) {
	if failed {
		return levelDanger, 0, 0
	}
	if slot == 0 {
		return levelSoft, 0, max(1, h.FinalLagSec)
	}
	if slot <= h.Finalized {
		return levelFinalized, 100, 0
	}
	if slot > h.Safe {
		return levelSoft, 25, max(1, h.FinalLagSec)
	}
	var age int64
	if blockTime > 0 {
		age = max(int64(0), now-blockTime)
	}
	eta := max(1, h.FinalLagSec-int(age))
	denom := max(uint64(1), h.Safe-h.Finalized)
	ratio := float64(h.Safe-slot) / float64(denom)
	prog := min(95, max(85, 85+int(math.Round(10*ratio))))
	return levelSafe, prog, eta
}

func fetchSolanaSlot(ctx context.Context, rpcs []string, commitment string) (uint64, string) {
	res, code := solanaRPCFailover(ctx, rpcs, "getSlot", map[string]string{"commitment": commitment})
	if code != "" {
		return 0, code
	}
	var slot uint64
	if err := json.Unmarshal(res, &slot); err != nil || slot == 0 {
		return 0, "bad_response"
	}
	return slot, ""
}

func fetchSolanaBlockTimeBestEffort(ctx context.Context, rpcs []string, slot uint64) int64 {
	res, code := solanaRPCFailover(ctx, rpcs, "getBlockTime", slot)
	if code != "" || len(res) == 0 || string(res) == "null" {
		return 0
	}
	var ts *int64
	if err := json.Unmarshal(res, &ts); err != nil || ts == nil || *ts <= 0 {
		return 0
	}
	return *ts
}

// fetchSolanaHeads fetches processed, confirmed, and finalized slot watermarks,
// and queries getBlockTime on confirmed and finalized slots best-effort (S2).
func fetchSolanaHeads(ctx context.Context, rpcs []string) (payHeads, error) {
	if len(rpcs) == 0 {
		return payHeads{}, errors.New("not_configured")
	}
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	commitments := [3]string{"processed", "confirmed", "finalized"}
	var slots [3]uint64
	var codes [3]string
	var wg sync.WaitGroup
	for i, c := range commitments {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			slots[i], codes[i] = fetchSolanaSlot(ctx, rpcs, c)
		}(i, c)
	}
	wg.Wait()
	for _, c := range codes {
		if c != "" {
			return payHeads{}, errors.New(c)
		}
	}

	var confTime, finTime int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		confTime = fetchSolanaBlockTimeBestEffort(ctx, rpcs, slots[1])
	}()
	go func() {
		defer wg.Done()
		finTime = fetchSolanaBlockTimeBestEffort(ctx, rpcs, slots[2])
	}()
	wg.Wait()

	h := clampHeads(payHeads{
		Latest:     slots[0],
		Safe:       slots[1],
		Finalized:  slots[2],
		LatestTime: confTime,
		SafeTime:   confTime,
		FinalTime:  finTime,
	}, false)

	finalLag := 0
	if h.SafeTime > 0 && h.FinalTime > 0 && h.SafeTime >= h.FinalTime {
		finalLag = int(h.SafeTime - h.FinalTime)
	}
	if finalLag <= 0 && h.Safe > h.Finalized {
		// ~400ms per Solana slot fallback when getBlockTime returns null (S2).
		finalLag = int((h.Safe-h.Finalized)*2) / 5
	}
	h.SafeLagSec = 1
	h.FinalLagSec = max(1, finalLag)
	return h, nil
}
