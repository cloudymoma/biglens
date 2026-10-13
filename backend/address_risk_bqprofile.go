package main

// Opt-in BigQuery address profile for Address Risk lookups on Ethereum
// (source "bigquery_profile"). It runs only when the user switches it on for
// a lookup (?bq=1) and the server allows it (address_risk.bigquery_profile).
// One parameterized job over the address-clustered views returns lifetime
// in/out activity, the first funder and the 200 most frequent counterparties;
// those are then matched against the local risk pool in SQLite (OFAC, MEW
// darklist, frozen USDT/USDC, scam lookalikes).
//
// The job is billed to the operator's own project (bigquery.project_id), so
// the address is not sent to a third party — but BigQuery keeps it in that
// project's job history (query parameters are stored with the job).
//
// Measured 2026-10-13: vitalik.eth billed 954,204,160 bytes (910 MiB) in 1.1 s;
// the dry-run upper bound is ~0.95 GB for any ordinary address (the cost is
// dominated by per-partition block reads, not activity) and 6.9 GB for the
// Uniswap Universal Router, which the default 2 GiB cap rejects up front
// without charge (status over_budget). The views have no receipt status or
// internal/token transfers: figures cover top-level transactions, including
// failed ones.
//
// TODO(security): BigLens has no app-level authentication, so anyone who can
// reach the server can trigger profiles; the server-side enabled switch, the
// per-job MaxBytesBilled cap and the global hourly token bucket are the only
// cost guardrails.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/googleapi"
)

const (
	bqProfileFromView = "`bigquery-public-data.goog_blockchain_ethereum_mainnet_us.transactions_by_from_address`"
	bqProfileToView   = "`bigquery-public-data.goog_blockchain_ethereum_mainnet_us.transactions_by_to_address`"

	bqProfileUIPeers    = 20               // counterparties shown in the UI card (the job returns 200)
	bqProfileCacheTTL   = 10 * time.Minute // same as the lookup cache
	bqProfileCacheMax   = 2000
	bqProfileJobTimeout = 2 * time.Minute // hard cap for a job that outlives its lookup

	bqStatusOK          = "ok"
	bqStatusSkipped     = "skipped" // not requested (switch off), non-Ethereum, or disabled
	bqStatusRateLimited = "rate_limited"
	bqStatusOverBudget  = "over_budget"
	bqStatusTimeout     = "timeout"
	bqStatusError       = "error"
)

// bqProfileAddrRe is the only accepted shape of @addr (already lowercased).
var bqProfileAddrRe = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// bqProfileSQL scans each view once (each CTE is referenced once, so no view
// is re-read) and selects only the columns it needs: bytes are the cost.
// transaction_hash would add ~0.53 GB per job, so the first funder is
// located by block number instead. Every output column is non-NULL.
const bqProfileSQL = `
WITH out_peers AS (
  SELECT
    to_address AS peer,
    COUNT(*) AS n,
    MIN(block_timestamp) AS first_ts,
    MAX(block_timestamp) AS last_ts,
    SUM(value) AS wei
  FROM ` + bqProfileFromView + `
  WHERE from_address = @addr
  GROUP BY peer
),
in_peers AS (
  SELECT
    from_address AS peer,
    COUNT(*) AS n,
    MIN(block_timestamp) AS first_ts,
    MAX(block_timestamp) AS last_ts,
    SUM(value) AS wei,
    ARRAY_AGG(IF(value > 0, STRUCT(block_timestamp AS ts, block_number AS block, value AS wei), NULL)
      IGNORE NULLS ORDER BY block_timestamp, block_number LIMIT 1)[SAFE_OFFSET(0)] AS funded
  FROM ` + bqProfileToView + `
  WHERE to_address = @addr
  GROUP BY peer
),
peers AS (
  SELECT
    peer,
    IFNULL(o.n, 0) AS sent,
    IFNULL(i.n, 0) AS received,
    o.first_ts AS out_first,
    o.last_ts AS out_last,
    i.first_ts AS in_first,
    i.last_ts AS in_last,
    IFNULL(o.wei, 0) AS wei_sent,
    IFNULL(i.wei, 0) AS wei_received,
    i.funded
  FROM out_peers AS o
  FULL OUTER JOIN in_peers AS i USING (peer)
)
SELECT
  IFNULL(SUM(sent), 0) AS out_txs,
  IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MIN(out_first)), '') AS out_first,
  IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MAX(out_last)), '') AS out_last,
  COUNTIF(sent > 0 AND peer IS NOT NULL AND peer != @addr) AS out_peers,
  CAST(IFNULL(SUM(wei_sent), 0) AS STRING) AS out_wei,
  IFNULL(SUM(received), 0) AS in_txs,
  IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MIN(in_first)), '') AS in_first,
  IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MAX(in_last)), '') AS in_last,
  COUNTIF(received > 0 AND peer IS NOT NULL AND peer != @addr) AS in_peers,
  CAST(IFNULL(SUM(wei_received), 0) AS STRING) AS in_wei,
  COUNTIF(peer IS NOT NULL AND peer != @addr) AS peers,
  ARRAY_AGG(
    IF(funded IS NULL OR peer IS NULL OR peer = @addr, NULL, STRUCT(
      peer AS address,
      funded.block AS block_number,
      FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', funded.ts) AS ts,
      CAST(funded.wei AS STRING) AS wei))
    IGNORE NULLS ORDER BY funded.ts, funded.block, peer LIMIT 1) AS first_funder,
  ARRAY_AGG(
    IF(peer IS NULL OR peer = @addr, NULL, STRUCT(
      peer AS address,
      sent,
      received,
      IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', LEAST(IFNULL(out_first, in_first), IFNULL(in_first, out_first))), '') AS first_ts,
      IFNULL(FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', GREATEST(IFNULL(out_last, in_last), IFNULL(in_last, out_last))), '') AS last_ts,
      CAST(wei_sent AS STRING) AS wei_sent,
      CAST(wei_received AS STRING) AS wei_received))
    IGNORE NULLS ORDER BY sent + received DESC, peer LIMIT 200) AS top_peers
FROM peers`

type bqProfileFunderRow struct {
	Address     string `bigquery:"address"`
	BlockNumber int64  `bigquery:"block_number"`
	TS          string `bigquery:"ts"`
	Wei         string `bigquery:"wei"`
}

type bqProfilePeerRow struct {
	Address     string `bigquery:"address"`
	Sent        int64  `bigquery:"sent"`
	Received    int64  `bigquery:"received"`
	FirstTS     string `bigquery:"first_ts"`
	LastTS      string `bigquery:"last_ts"`
	WeiSent     string `bigquery:"wei_sent"`
	WeiReceived string `bigquery:"wei_received"`
}

// bqProfileRow is the job's single result row.
type bqProfileRow struct {
	OutTxs      int64                `bigquery:"out_txs"`
	OutFirst    string               `bigquery:"out_first"`
	OutLast     string               `bigquery:"out_last"`
	OutPeers    int64                `bigquery:"out_peers"`
	OutWei      string               `bigquery:"out_wei"`
	InTxs       int64                `bigquery:"in_txs"`
	InFirst     string               `bigquery:"in_first"`
	InLast      string               `bigquery:"in_last"`
	InPeers     int64                `bigquery:"in_peers"`
	InWei       string               `bigquery:"in_wei"`
	Peers       int64                `bigquery:"peers"`
	FirstFunder []bqProfileFunderRow `bigquery:"first_funder"`
	TopPeers    []bqProfilePeerRow   `bigquery:"top_peers"`
}

// newBQProfileQuery builds the one parameterized profile job; addr must
// already match bqProfileAddrRe.
func newBQProfileQuery(client *bigquery.Client, addr string, maxBytes int64) *bigquery.Query {
	q := client.Query(bqProfileSQL)
	q.Parameters = []bigquery.QueryParameter{{Name: "addr", Value: addr}}
	q.MaxBytesBilled = maxBytes
	q.Labels = map[string]string{"feature": "address_profile"}
	applyQueryDefaults(q) // app=biglens
	return q
}

// bqProfileQuerier isolates BigQuery so the profiler can be tested.
type bqProfileQuerier interface {
	runProfile(ctx context.Context, addr string, maxBytes int64) (*bqProfileRow, int64, error)
}

type bqProfileClient struct{ client *bigquery.Client }

// runProfile runs the job (jobs.insert, so the statistics carry the billed
// bytes) and returns its row. An over-budget job fails in Wait with
// bytesBilledLimitExceeded before running (verified 2026-10-13). If ctx ends
// first, the job is cancelled.
func (c bqProfileClient) runProfile(ctx context.Context, addr string, maxBytes int64) (*bqProfileRow, int64, error) {
	job, err := newBQProfileQuery(c.client, addr, maxBytes).Run(ctx)
	if err != nil {
		return nil, 0, err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		if ctx.Err() != nil { // the job outlived its deadline: stop it (best effort)
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = job.Cancel(cctx)
			cancel()
		}
		return nil, 0, err
	}
	if err := status.Err(); err != nil {
		return nil, 0, err
	}
	var billed int64
	if status.Statistics != nil {
		if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
			billed = qs.TotalBytesBilled
		}
	}
	it, err := job.Read(ctx)
	if err != nil {
		return nil, billed, err
	}
	var row bqProfileRow
	if err := it.Next(&row); err != nil {
		return nil, billed, err // the aggregate always yields one row
	}
	return &row, billed, nil
}

// bqProfileErrStatus maps a job error to a client-safe status; the raw error
// never leaves the server.
func bqProfileErrStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return bqStatusTimeout
	}
	var jobErr *bigquery.Error
	if errors.As(err, &jobErr) && jobErr.Reason == "bytesBilledLimitExceeded" {
		return bqStatusOverBudget
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, e := range apiErr.Errors {
			if e.Reason == "bytesBilledLimitExceeded" {
				return bqStatusOverBudget
			}
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "exceeded limit for bytes billed") {
		return bqStatusOverBudget
	}
	return bqStatusError
}

// hourlyBucket is a global token bucket holding at most perHour tokens,
// refilled continuously at perHour per hour.
type hourlyBucket struct {
	mu     sync.Mutex
	cap    float64
	tokens float64
	last   time.Time
}

func newHourlyBucket(perHour int, now time.Time) *hourlyBucket {
	return &hourlyBucket{cap: float64(perHour), tokens: float64(perHour), last: now}
}

func (b *hourlyBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if el := now.Sub(b.last); el > 0 {
		b.tokens = min(b.cap, b.tokens+el.Hours()*b.cap)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// bqProfileRun is the outcome of one profile job.
type bqProfileRun struct {
	Status    string
	Row       *bqProfileRow
	Billed    int64
	ElapsedMs int64
}

type bqProfileEntry struct {
	run     bqProfileRun
	expires time.Time
}

// bqProfiler runs profile jobs with a per-address cache and singleflight (so
// repeated or concurrent lookups never bill twice), the global hourly bucket,
// and a lookup-side timeout. A job that outlives its lookup keeps running
// (up to bqProfileJobTimeout) and fills the cache for the retry.
type bqProfiler struct {
	settings   bqProfileSettings
	querier    bqProfileQuerier // nil: no BigQuery client
	bucket     *hourlyBucket
	now        func() time.Time
	jobTimeout time.Duration
	flight     singleflight.Group

	mu    sync.Mutex
	cache map[string]bqProfileEntry
}

func newBQProfiler(s bqProfileSettings, q bqProfileQuerier) *bqProfiler {
	return &bqProfiler{settings: s, querier: q, bucket: newHourlyBucket(s.MaxPerHour, time.Now()),
		now: time.Now, jobTimeout: bqProfileJobTimeout, cache: map[string]bqProfileEntry{}}
}

func (p *bqProfiler) available() bool {
	return p != nil && p.settings.Enabled && p.querier != nil
}

func (p *bqProfiler) cached(addr string) (bqProfileRun, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[addr]
	if !ok || p.now().After(e.expires) {
		return bqProfileRun{}, false
	}
	return e.run, true
}

func (p *bqProfiler) store(addr string, r bqProfileRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if len(p.cache) >= bqProfileCacheMax {
		for k, e := range p.cache {
			if now.After(e.expires) {
				delete(p.cache, k)
			}
		}
		if len(p.cache) >= bqProfileCacheMax {
			return // full of live entries; skip caching rather than grow
		}
	}
	p.cache[addr] = bqProfileEntry{run: r, expires: now.Add(bqProfileCacheTTL)}
}

// run returns the profile of addr (validated by the caller) and whether it
// came from the cache.
func (p *bqProfiler) run(ctx context.Context, addr string) (bqProfileRun, bool) {
	if r, ok := p.cached(addr); ok {
		return r, true
	}
	ch := p.flight.DoChan(addr, func() (any, error) {
		if r, ok := p.cached(addr); ok {
			return r, nil
		}
		if !p.bucket.allow(p.now()) {
			return bqProfileRun{Status: bqStatusRateLimited}, nil
		}
		jctx, cancel := context.WithTimeout(context.Background(), p.jobTimeout)
		defer cancel()
		start := time.Now()
		row, billed, err := p.querier.runProfile(jctx, addr, p.settings.MaxBytesBilled)
		r := bqProfileRun{Status: bqStatusOK, Row: row, Billed: billed, ElapsedMs: time.Since(start).Milliseconds()}
		if err == nil && row == nil {
			err = errors.New("profile job returned no row")
		}
		if err != nil {
			r.Status, r.Row = bqProfileErrStatus(err), nil
			if r.Status == bqStatusOverBudget {
				slog.Info("address_risk bigquery profile over budget", "max_bytes_billed", p.settings.MaxBytesBilled)
			} else {
				// Never log the address: scrub it from the driver's message
				// (which can also carry a long debug trace).
				msg := strings.ReplaceAll(err.Error(), addr, "<address>")
				slog.Warn("address_risk bigquery profile failed", "status", r.Status,
					"elapsed_ms", r.ElapsedMs, "error", msg[:min(len(msg), 512)])
			}
		}
		if r.Status == bqStatusOK || r.Status == bqStatusOverBudget {
			p.store(addr, r) // lasting answers; failures stay retryable
		}
		return r, nil
	})
	t := time.NewTimer(p.settings.Timeout)
	defer t.Stop()
	select {
	case res := <-ch:
		return res.Val.(bqProfileRun), false
	case <-t.C:
		return bqProfileRun{Status: bqStatusTimeout, ElapsedMs: p.settings.Timeout.Milliseconds()}, false
	case <-ctx.Done():
		return bqProfileRun{Status: bqStatusTimeout}, false
	}
}

// riskBQProfileInfo tells the UI whether the switch can be offered.
type riskBQProfileInfo struct {
	Available      bool   `json:"available"`
	MaxBytesBilled int64  `json:"max_bytes_billed"`
	PerHourLimit   int    `json:"per_hour_limit"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Reason         string `json:"reason,omitempty"` // when unavailable: disabled | not_configured
}

func (s *addressRiskService) bqProfileInfo() riskBQProfileInfo {
	p := s.bqProfile
	if p == nil || p.querier == nil {
		return riskBQProfileInfo{Reason: "not_configured"}
	}
	info := riskBQProfileInfo{Available: p.available(), MaxBytesBilled: p.settings.MaxBytesBilled,
		PerHourLimit: p.settings.MaxPerHour, TimeoutSeconds: int(p.settings.Timeout / time.Second)}
	if !info.Available {
		info.Reason = "disabled"
	}
	return info
}

// riskBQProfile is the "bq_profile" object of a lookup response.
type riskBQProfile struct {
	Status         string        `json:"status"`
	Message        string        `json:"message,omitempty"`
	Outgoing       *riskBQSide   `json:"outgoing,omitempty"`
	Incoming       *riskBQSide   `json:"incoming,omitempty"`
	Counterparties int64         `json:"counterparties"`
	FirstSeen      string        `json:"first_seen,omitempty"`
	LastActive     string        `json:"last_active,omitempty"`
	AccountAgeDays int           `json:"account_age_days,omitempty"`
	FirstFunder    *riskBQFunder `json:"first_funder,omitempty"`
	TopPeers       []riskBQPeer  `json:"top_peers,omitempty"`
	ListedPeers    []riskBQPeer  `json:"listed_peers,omitempty"`
	PeersAnalyzed  int           `json:"peers_analyzed"`
	ListsChecked   bool          `json:"lists_checked"`
	BytesBilled    int64         `json:"bytes_billed"`
	MaxBytesBilled int64         `json:"max_bytes_billed"`
	ElapsedMs      int64         `json:"elapsed_ms"`
	Cached         bool          `json:"cached"`
}

type riskBQSide struct {
	Txs   int64  `json:"txs"`
	Peers int64  `json:"peers"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	ETH   string `json:"eth"`
}

type riskBQFunder struct {
	Address     string   `json:"address"`
	BlockNumber int64    `json:"block_number"`
	Timestamp   string   `json:"timestamp"`
	ValueETH    string   `json:"value_eth"`
	Lists       []string `json:"lists"`
}

type riskBQPeer struct {
	Address     string   `json:"address"`
	Sent        int64    `json:"sent"`
	Received    int64    `json:"received"`
	FirstTs     string   `json:"first_ts"`
	LastTs      string   `json:"last_ts"`
	ETHSent     string   `json:"eth_sent"`
	ETHReceived string   `json:"eth_received"`
	Lists       []string `json:"lists"`
	FirstFunder bool     `json:"first_funder,omitempty"`
}

// formatWeiETH renders a decimal wei string as ETH with at most 6 decimals;
// a positive amount that rounds to zero shows as "<0.000001", never "0".
func formatWeiETH(wei string) string {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(wei))
	if !ok {
		return ""
	}
	eth := new(big.Rat).Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	s := strings.TrimRight(strings.TrimRight(eth.FloatString(6), "0"), ".")
	if s == "0" && r.Sign() > 0 {
		return "<0.000001"
	}
	return s
}

func fmtCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func accountAgeText(days int) string {
	switch {
	case days < 1:
		return "less than a day"
	case days < 60:
		return plural(days, "day")
	case days < 730:
		return plural(days/30, "month")
	default:
		return fmt.Sprintf("%.1f years", float64(days)/365.25)
	}
}

// poolVerb phrases one local-list membership for a clue title.
func poolVerb(source string) string {
	switch source {
	case "ofac":
		return "OFAC-listed"
	case "stablecoin":
		return "frozen by USDT/USDC"
	case "mew_darklist":
		return "on the MEW darklist"
	case "scam_lookalikes":
		return "a known address-poisoning lookalike"
	default:
		return "on local list " + source
	}
}

var bqProfileMessages = map[string]string{
	bqStatusRateLimited: "Hourly BigQuery profile limit reached on this server; try again later.",
	bqStatusTimeout:     "The BigQuery profile did not finish in time; a retry within 10 minutes reuses the job's result.",
	bqStatusError:       "BigQuery profile unavailable (the query failed; details are in the server log).",
}

func fmtGiB(bytes int64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(bytes)/(1<<30)), ".0") + " GiB"
}

// checkBQProfile runs the profile for one Ethereum lookup and turns it into
// the source entry, clues and the bq_profile object.
func (s *addressRiskService) checkBQProfile(ctx context.Context, addr string, now time.Time) (riskSource, []riskClue, *riskBQProfile) {
	src := riskSource{ID: "bigquery_profile", Status: bqStatusOK} // own GCP project: no third-party hosts
	if !bqProfileAddrRe.MatchString(addr) {
		src.Status, src.Error = bqStatusError, "bad_request"
		return src, nil, nil
	}
	run, cached := s.bqProfile.run(ctx, addr)
	prof := &riskBQProfile{Status: run.Status, BytesBilled: run.Billed, ElapsedMs: run.ElapsedMs, Cached: cached,
		MaxBytesBilled: s.bqProfile.settings.MaxBytesBilled}
	if run.Status != bqStatusOK {
		src.Status, src.Error = run.Status, run.Status
		if run.Status == bqStatusError {
			src.Error = "query_failed"
		}
		prof.Message = bqProfileMessages[run.Status]
		if run.Status == bqStatusOverBudget {
			prof.Message = fmt.Sprintf("Address too active for the configured BigQuery budget (cap %s per lookup); "+
				"BigQuery rejected the job before running it, so nothing was billed.", fmtGiB(prof.MaxBytesBilled))
		}
		return src, nil, prof
	}

	row := run.Row
	var addrs []string
	if len(row.FirstFunder) > 0 {
		addrs = append(addrs, row.FirstFunder[0].Address)
	}
	for _, p := range row.TopPeers {
		if !slices.Contains(addrs, p.Address) {
			addrs = append(addrs, p.Address)
		}
	}
	var pool riskPool
	if s.store != nil && len(addrs) > 0 {
		var err error
		if pool, err = s.store.riskPoolForAddresses(ctx, "eth", addrs); err != nil {
			slog.Warn("address_risk bigquery profile: local pool unavailable", "error", err)
			pool = nil
		}
	}
	prof.ListsChecked = s.store != nil && (pool != nil || len(addrs) == 0)
	if !prof.ListsChecked {
		src.Status, src.Error = "partial", "local_pool_unavailable"
	}
	fillBQProfile(prof, row, pool, now)
	return src, bqProfileClues(prof, fmtTime(now)), prof
}

func fillBQProfile(prof *riskBQProfile, row *bqProfileRow, pool riskPool, now time.Time) {
	lists := func(a string) []string { return slices.Clone(pool[a]) }
	prof.Outgoing = &riskBQSide{Txs: row.OutTxs, Peers: row.OutPeers, First: row.OutFirst, Last: row.OutLast, ETH: formatWeiETH(row.OutWei)}
	prof.Incoming = &riskBQSide{Txs: row.InTxs, Peers: row.InPeers, First: row.InFirst, Last: row.InLast, ETH: formatWeiETH(row.InWei)}
	prof.Counterparties = row.Peers
	for _, ts := range []string{row.OutFirst, row.InFirst} {
		if ts != "" && (prof.FirstSeen == "" || ts < prof.FirstSeen) {
			prof.FirstSeen = ts
		}
	}
	for _, ts := range []string{row.OutLast, row.InLast} {
		if ts > prof.LastActive {
			prof.LastActive = ts
		}
	}
	if t, err := time.Parse(time.RFC3339, prof.FirstSeen); err == nil && now.After(t) {
		prof.AccountAgeDays = int(now.Sub(t).Hours() / 24)
	}
	funder := ""
	if len(row.FirstFunder) > 0 {
		f := row.FirstFunder[0]
		funder = f.Address
		prof.FirstFunder = &riskBQFunder{Address: f.Address, BlockNumber: f.BlockNumber, Timestamp: f.TS,
			ValueETH: formatWeiETH(f.Wei), Lists: lists(f.Address)}
	}
	prof.PeersAnalyzed = len(row.TopPeers)
	for i, p := range row.TopPeers {
		peer := riskBQPeer{Address: p.Address, Sent: p.Sent, Received: p.Received, FirstTs: p.FirstTS, LastTs: p.LastTS,
			ETHSent: formatWeiETH(p.WeiSent), ETHReceived: formatWeiETH(p.WeiReceived), Lists: lists(p.Address),
			FirstFunder: p.Address == funder}
		if i < bqProfileUIPeers {
			prof.TopPeers = append(prof.TopPeers, peer)
		}
		if len(peer.Lists) > 0 {
			prof.ListedPeers = append(prof.ListedPeers, peer)
		}
	}
}

// bqProfileClues maps a profile to clues: association clues for a listed
// first funder and for listed counterparties (as the Etherscan association
// analysis does), plus Info clues for account age and lifetime activity.
func bqProfileClues(prof *riskBQProfile, asOf string) []riskClue {
	var out []riskClue
	clue := func(sev, code, title, detail, observed, ref string) {
		c := riskClue{Severity: sev, Source: "bigquery_profile", Code: code, Title: title, Detail: detail, AsOf: asOf, RefURL: ref}
		if observed != "" {
			c.ObservedAt = &observed
		}
		out = append(out, c)
	}
	addrURL := chains["eth"].AddressURL
	f := prof.FirstFunder
	if f != nil && len(f.Lists) > 0 {
		verbs := make([]string, 0, len(f.Lists))
		for _, l := range f.Lists {
			verbs = append(verbs, poolVerb(l))
		}
		clue(sevAssociation, "bq_first_funder_listed",
			fmt.Sprintf("First funder %s is %s", f.Address, strings.Join(verbs, " and ")),
			fmt.Sprintf("First incoming ETH: %s ETH in block %d (%s), full Ethereum history via BigQuery. Counterparty is on: %s. %s",
				f.ValueETH, f.BlockNumber, f.Timestamp[:min(10, len(f.Timestamp))], strings.Join(poolLabels(f.Lists), ", "), riskPoisoningNote),
			f.Timestamp, fmt.Sprintf(addrURL, f.Address))
	}
	var listed []riskBQPeer
	for _, p := range prof.ListedPeers {
		if f != nil && len(f.Lists) > 0 && p.Address == f.Address {
			continue // has its own clue
		}
		listed = append(listed, p)
	}
	if len(listed) > 0 {
		var parts []string
		newest := ""
		for i, p := range listed {
			if p.LastTs > newest {
				newest = p.LastTs
			}
			if i < 5 {
				parts = append(parts, fmt.Sprintf("%s (%s; %s sent / %s received)", p.Address,
					strings.Join(poolLabels(p.Lists), ", "), fmtCount(p.Sent), fmtCount(p.Received)))
			}
		}
		if len(listed) > 5 {
			parts = append(parts, fmt.Sprintf("and %d more (see BigQuery profile)", len(listed)-5))
		}
		title := fmt.Sprintf("%d direct counterparties appear on local lists", len(listed))
		if len(listed) == 1 {
			title = "1 direct counterparty appears on local lists"
		}
		clue(sevAssociation, "bq_counterparties_listed", title,
			fmt.Sprintf("Among the %d most frequent counterparties (of %s, full history): %s. %s",
				prof.PeersAnalyzed, fmtCount(prof.Counterparties), strings.Join(parts, "; "), riskPoisoningNote),
			newest, fmt.Sprintf(addrURL, listed[0].Address))
	}
	if prof.FirstSeen == "" {
		clue(sevInfo, "bq_no_activity", "No top-level transactions found in BigQuery's Ethereum history",
			"Internal transfers and token transfers are not covered by this profile.", "", "")
		return out
	}
	detail := "Earliest top-level transaction in BigQuery's full Ethereum history."
	if f != nil {
		detail += fmt.Sprintf(" First funded by %s with %s ETH in block %d.", f.Address, f.ValueETH, f.BlockNumber)
	}
	clue(sevInfo, "bq_first_seen",
		fmt.Sprintf("First seen on Ethereum %s (account age %s)", prof.FirstSeen[:min(10, len(prof.FirstSeen))], accountAgeText(prof.AccountAgeDays)),
		detail, prof.FirstSeen, "")
	clue(sevInfo, "bq_activity",
		fmt.Sprintf("Lifetime activity: %s outgoing / %s incoming transactions with %s counterparties; last active %s",
			fmtCount(prof.Outgoing.Txs), fmtCount(prof.Incoming.Txs), fmtCount(prof.Counterparties), prof.LastActive[:min(10, len(prof.LastActive))]),
		fmt.Sprintf("Sent %s ETH and received %s ETH in top-level transactions (internal transfers and tokens not included).",
			prof.Outgoing.ETH, prof.Incoming.ETH), "", "")
	return out
}
