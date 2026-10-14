package main

// On-chain USD prices derived inside BigQuery from Uniswap v3 swaps — no
// third-party price API (GET /api/opendata/crypto/prices).
//
// Source: bigquery-public-data.goog_blockchain_ethereum_mainnet_us.
// decoded_events, Swap(address,address,int256,int256,uint160,uint128,int24)
// events (args is a positional JSON array of decimal strings; args[6] is the
// pool tick) of four pools:
//   - USDC/WETH 0.01% and 0.05% (token0 USDC, 6 decimals; token1 WETH, 18):
//     ETH/USD = 1e12 / 1.0001^tick.
//   - WBTC/WETH 0.05% and 0.3% (token0 WBTC, 8 decimals; token1 WETH, 18):
//     WETH per WBTC = 1.0001^tick × 1e-10, and BTC/USD = WETH per WBTC × the
//     same hour's ETH/USD ("WBTC-derived"). WBTC/USDC (~6 swaps/h) is too
//     thin to use.
//
// Cost control: only four one-hour windows per UTC day are sampled (00, 06,
// 12 and 18 UTC), as OR'ed literal block_timestamp ranges generated in Go
// from validated civil dates (no request input reaches the SQL). They prune
// through the SQD clustering on block_timestamp: one window bills ~45–90 MiB
// (dry-run upper bound ~105–131 MiB). Per-hour median ticks and swap counts
// are computed in SQL; the daily price is the median of the day's qualifying
// sampled hours, computed in Go.
//
// A background syncer (hourly, wired in main.go) persists one row per UTC day
// in the Address Risk SQLite file (opendata_crypto_prices_store.go): a cold
// start of crypto_prices.initial_days (default 365 ≈ 125 GiB ≈ $0.8 once),
// then one increment per day once its 18:00 window has been ingested
// (~350 MiB ≈ $0.002/day). The endpoint reads memory/SQLite only, never
// BigQuery.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

const (
	decodedEventsTable     = "`bigquery-public-data.goog_blockchain_ethereum_mainnet_us.decoded_events`"
	uniswapV3SwapSignature = "Swap(address,address,int256,int256,uint160,uint128,int24)"

	poolUSDCWETH001 = "0xe0554a476a092703abdb3ef35c80e0d76d32939f" // USDC/WETH 0.01%, ~930 swaps/h
	poolUSDCWETH005 = "0x88e6a0c2ddd26feeb64f039a2c41296fcb3f5640" // USDC/WETH 0.05%, ~365 swaps/h
	poolWBTCWETH005 = "0x4585fe77225b41b697c938b018e2ac67ac5a20c0" // WBTC/WETH 0.05%, ~33 swaps/h
	poolWBTCWETH030 = "0xcbcdf9626bc03e24f779434178a73a0b4bad62ed" // WBTC/WETH 0.3%, ~8 swaps/h

	// A sampled hour counts only with at least this many swaps: a median of
	// a handful of trades is noise (the ETH pools see ~1,300 swaps/h, the
	// WBTC/WETH pools ~40/h).
	cryptoPricesMinETHSwaps  = 5
	cryptoPricesMinWBTCSwaps = 2

	cryptoPricesMaxDaysPerQuery = 8
	// cryptoPricesBytesPerWindow budgets one sampled hour. BigQuery rejects a
	// query up front (unbilled) when its dry-run upper bound exceeds
	// MaxBytesBilled, and that bound drifts, upward for recent days: on
	// 2026-10-13 a recent day's 4 windows estimated 687–719 MB and older
	// 8-day chunks 4.27–4.63 GB. 512 MiB per window keeps ≥ 2.9× headroom
	// (2 GiB per day; 8 days × 4 windows × 512 MiB = 16 GiB per query).
	// Actual billing is unaffected: ~360–500 MiB per day.
	cryptoPricesBytesPerWindow int64 = 512 << 20
	cryptoPricesMaxBytesBilled int64 = 16 << 30
	cryptoPricesReadyMaxBytes  int64 = 256 << 20

	cryptoPricesBilledRetry = 6 * time.Hour
	// A day is synced once rows past its last sampled window exist and that
	// window closed at least cryptoPricesFinality ago (re-org margin).
	cryptoPricesFinality         = 30 * time.Minute
	cryptoPricesSyncTimeout      = 30 * time.Minute
	cryptoPricesMaxChunksPerSync = 24
	// Without a readable SQLite store every restart is a cold start, so it is
	// limited to one month (~11–15 GiB) instead of initial_days.
	cryptoPricesMemoryOnlyMaxDays = 31
	// A day without any qualifying swap that is younger than this may still
	// be arriving: it is not stored and is re-queried on the next tick. Older
	// empty days are stored (with a warning) so they are not re-queried
	// forever.
	cryptoPricesProvisionalDays = 3

	cryptoPricesSource  = "uniswap_v3"
	cryptoPricesMethod  = "median of sampled hours (00/06/12/18 UTC)"
	cryptoPricesBtcNote = "WBTC-derived: Uniswap v3 WBTC/WETH × same-hour ETH/USD"
)

// cryptoPricesSampleHours are the sampled UTC hours of every day.
var cryptoPricesSampleHours = []int{0, 6, 12, 18}

var cryptoPricesDaysAllowed = []int{7, 30, 90, 365}

// --- SQL ---

// cryptoPricesWindows renders one literal one-hour block_timestamp range per
// sampled hour of each day. Days must be valid, distinct, and fit one query.
func cryptoPricesWindows(days []civil.Date) (string, error) {
	if len(days) == 0 {
		return "", errors.New("crypto prices: no days to query")
	}
	if len(days) > cryptoPricesMaxDaysPerQuery {
		return "", fmt.Errorf("crypto prices: %d days exceed one query (max %d)", len(days), cryptoPricesMaxDaysPerQuery)
	}
	seen := make(map[civil.Date]bool, len(days))
	parts := make([]string, 0, len(days)*len(cryptoPricesSampleHours))
	for _, day := range days {
		if !day.IsValid() {
			return "", fmt.Errorf("crypto prices: invalid day %v", day)
		}
		if seen[day] {
			return "", fmt.Errorf("crypto prices: duplicate day %s", day)
		}
		seen[day] = true
		for _, h := range cryptoPricesSampleHours {
			from := day.In(time.UTC).Add(time.Duration(h) * time.Hour)
			parts = append(parts, "(block_timestamp >= TIMESTAMP '"+from.Format(time.DateTime)+
				"+00' AND block_timestamp < TIMESTAMP '"+from.Add(time.Hour).Format(time.DateTime)+"+00')")
		}
	}
	return strings.Join(parts, "\n      OR "), nil
}

// cryptoPricesSQL returns per-hour median ticks and swap counts per pair
// ('eth' = USDC/WETH, 'wbtc' = WBTC/WETH) for the sampled hours of days.
// SAFE_CAST turns a malformed tick into NULL instead of failing the billed
// job; NULL ticks are dropped.
func cryptoPricesSQL(days []civil.Date) (string, error) {
	windows, err := cryptoPricesWindows(days)
	if err != nil {
		return "", err
	}
	return `
WITH swaps AS (
  SELECT
    TIMESTAMP_TRUNC(block_timestamp, HOUR) AS hour,
    IF(address IN ('` + poolUSDCWETH001 + `', '` + poolUSDCWETH005 + `'), 'eth', 'wbtc') AS pair,
    SAFE_CAST(JSON_VALUE(args, '$[6]') AS INT64) AS tick
  FROM ` + decodedEventsTable + `
  WHERE (` + windows + `)
    AND address IN ('` + poolUSDCWETH001 + `', '` + poolUSDCWETH005 + `', '` + poolWBTCWETH005 + `', '` + poolWBTCWETH030 + `')
    AND event_signature = '` + uniswapV3SwapSignature + `'
    AND NOT removed
)
SELECT
  FORMAT_TIMESTAMP('%Y-%m-%d %H:00', hour) AS hour_utc,
  pair,
  COUNT(*) AS swaps,
  APPROX_QUANTILES(tick, 100)[OFFSET(50)] AS median_tick
FROM swaps
WHERE tick IS NOT NULL
GROUP BY hour_utc, pair
ORDER BY hour_utc, pair`, nil
}

// cryptoPricesMaxBytes caps one query: nDays × 4 windows × the per-window
// budget, never above 16 GiB.
func cryptoPricesMaxBytes(nDays int) int64 {
	return min(int64(nDays)*int64(len(cryptoPricesSampleHours))*cryptoPricesBytesPerWindow, cryptoPricesMaxBytesBilled)
}

// cryptoPricesReadySQL finds the newest decoded event in the 2 hours after
// @from (a day's 19:00 UTC, the end of its last sampled window).
const cryptoPricesReadySQL = `
SELECT MAX(block_timestamp) AS t FROM ` + decodedEventsTable + `
WHERE block_timestamp >= @from AND block_timestamp < TIMESTAMP_ADD(@from, INTERVAL 2 HOUR)`

// --- tick math ---

// ethUSDFromTick converts a USDC/WETH tick (token1 WETH per token0 USDC in
// raw units = 1.0001^tick; 18 − 6 decimals) to USD per ETH.
func ethUSDFromTick(tick int64) float64 {
	return 1e12 / math.Pow(1.0001, float64(tick))
}

// wethPerWBTCFromTick converts a WBTC/WETH tick (8 vs 18 decimals) to WETH
// per WBTC.
func wethPerWBTCFromTick(tick int64) float64 {
	return math.Pow(1.0001, float64(tick)) * 1e-10
}

// --- daily aggregation ---

// priceHourRow is one sampled hour of one pair.
type priceHourRow struct {
	HourUTC    string             `bigquery:"hour_utc"` // "YYYY-MM-DD HH:00"
	Pair       string             `bigquery:"pair"`     // "eth" | "wbtc"
	Swaps      int64              `bigquery:"swaps"`
	MedianTick bigquery.NullInt64 `bigquery:"median_tick"`
}

// onchainPriceDay is one UTC day; nil prices mean no qualifying sampled hour.
// The swap counts are the swaps behind the qualifying hours.
type onchainPriceDay struct {
	Day      string   `json:"day"`
	EthUSD   *float64 `json:"eth_usd"`
	BtcUSD   *float64 `json:"btc_usd"`
	EthSwaps int64    `json:"eth_swaps"`
	BtcSwaps int64    `json:"btc_swaps"`
}

func medianFloat(vals []float64) float64 {
	s := slices.Clone(vals)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// dailyPricesFromHours returns one row per requested day (ascending). ETH/USD
// is the median of the day's qualifying sampled hours; BTC/USD pairs each
// qualifying WBTC/WETH hour with that hour's ETH/USD.
func dailyPricesFromHours(days []civil.Date, hours []priceHourRow) []onchainPriceDay {
	type hourPrice struct {
		price float64
		swaps int64
	}
	ethByHour := make(map[string]hourPrice, len(hours))
	wbtcByHour := make(map[string]hourPrice, len(hours))
	for _, h := range hours {
		if !h.MedianTick.Valid {
			continue
		}
		switch {
		case h.Pair == "eth" && h.Swaps >= cryptoPricesMinETHSwaps:
			ethByHour[h.HourUTC] = hourPrice{ethUSDFromTick(h.MedianTick.Int64), h.Swaps}
		case h.Pair == "wbtc" && h.Swaps >= cryptoPricesMinWBTCSwaps:
			wbtcByHour[h.HourUTC] = hourPrice{wethPerWBTCFromTick(h.MedianTick.Int64), h.Swaps}
		}
	}
	sorted := slices.Clone(days)
	slices.SortFunc(sorted, func(a, b civil.Date) int { return a.Compare(b) })
	out := make([]onchainPriceDay, 0, len(sorted))
	for _, day := range sorted {
		row := onchainPriceDay{Day: day.String()}
		var eth, btc []float64
		for _, h := range cryptoPricesSampleHours {
			key := fmt.Sprintf("%s %02d:00", day, h)
			e, ok := ethByHour[key]
			if !ok {
				continue
			}
			eth = append(eth, e.price)
			row.EthSwaps += e.swaps
			if w, ok := wbtcByHour[key]; ok {
				btc = append(btc, w.price*e.price)
				row.BtcSwaps += w.swaps
			}
		}
		if len(eth) > 0 {
			v := medianFloat(eth)
			row.EthUSD = &v
		}
		if len(btc) > 0 {
			v := medianFloat(btc)
			row.BtcUSD = &v
		}
		out = append(out, row)
	}
	return out
}

// --- BigQuery source ---

// cryptoPriceSource isolates BigQuery so the syncer is testable.
type cryptoPriceSource interface {
	// LatestEventTime returns the newest decoded event in [from, from+2h)
	// (zero when none yet).
	LatestEventTime(ctx context.Context, from time.Time) (time.Time, error)
	// HourlyMedians runs cryptoPricesSQL for days under maxBytes and also
	// reports the bytes billed (even on failure, when known).
	HourlyMedians(ctx context.Context, days []civil.Date, maxBytes int64) ([]priceHourRow, int64, error)
}

type bqCryptoPriceSource struct{ client *bigquery.Client }

func (b bqCryptoPriceSource) LatestEventTime(ctx context.Context, from time.Time) (time.Time, error) {
	q := b.client.Query(cryptoPricesReadySQL)
	q.Parameters = []bigquery.QueryParameter{{Name: "from", Value: from.UTC()}}
	q.MaxBytesBilled = cryptoPricesReadyMaxBytes
	rows, err := collectRows[cryptoReadyRow](q, ctx)
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) == 0 || !rows[0].T.Valid {
		return time.Time{}, nil
	}
	return rows[0].T.Timestamp, nil
}

func (b bqCryptoPriceSource) HourlyMedians(ctx context.Context, days []civil.Date, maxBytes int64) ([]priceHourRow, int64, error) {
	sql, err := cryptoPricesSQL(days)
	if err != nil {
		return nil, 0, err
	}
	q := b.client.Query(sql)
	q.MaxBytesBilled = maxBytes
	applyQueryDefaults(q) // app=biglens label
	job, err := q.Run(ctx)
	if err != nil {
		return nil, 0, err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		// The job may still run and bill server-side: cancel it and report
		// what it billed so the caller backs off instead of re-running it.
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = job.Cancel(cctx)
		var billed int64
		if st, sErr := job.Status(cctx); sErr == nil && st != nil && st.Statistics != nil {
			if qs, ok := st.Statistics.Details.(*bigquery.QueryStatistics); ok {
				billed = qs.TotalBytesBilled
			}
		}
		return nil, billed, err
	}
	if err := status.Err(); err != nil {
		return nil, 0, err
	}
	var billed int64
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		billed = qs.TotalBytesBilled
	}
	it, err := job.Read(ctx)
	if err != nil {
		return nil, billed, err
	}
	var rows []priceHourRow
	for {
		var r priceHourRow
		err := it.Next(&r)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, billed, err
		}
		rows = append(rows, r)
	}
	return rows, billed, nil
}

// --- syncer ---

// cryptoPriceStore is the persistence the syncer needs (*riskStore in
// production; tests inject failures).
type cryptoPriceStore interface {
	loadOnchainPrices(ctx context.Context) ([]onchainPriceDay, time.Time, error)
	upsertOnchainPrices(ctx context.Context, rows []onchainPriceDay, syncedAt time.Time) error
}

// cryptoPriceSyncer keeps the daily prices in memory (the endpoint's only
// source) and fills missing days of the target window from BigQuery,
// persisting them to SQLite when available.
type cryptoPriceSyncer struct {
	src         cryptoPriceSource
	store       cryptoPriceStore // nil: memory only
	now         func() time.Time
	initialDays int

	mu          sync.Mutex
	loaded      bool
	loadFailed  bool
	byDay       map[string]onchainPriceDay
	asOf        time.Time
	retryAfter  time.Time
	loadWarned  bool
	writeWarned bool
}

func newCryptoPriceSyncer(src cryptoPriceSource, store *riskStore, initialDays int, now func() time.Time) *cryptoPriceSyncer {
	if now == nil {
		now = time.Now
	}
	s := &cryptoPriceSyncer{src: src, now: now, initialDays: initialDays,
		byDay: make(map[string]onchainPriceDay)}
	if store != nil { // a nil *riskStore must stay a nil interface
		s.store = store
	}
	return s
}

// warnStoreLocked logs a SQLite failure once per operation ("load" / "write")
// until it recovers; the syncer keeps serving and syncing from memory.
// Caller holds s.mu.
func (s *cryptoPriceSyncer) warnStoreLocked(op string, err error) {
	if op == "load" {
		if s.loadWarned {
			return
		}
		s.loadWarned = true
	} else {
		if s.writeWarned {
			return
		}
		s.writeWarned = true
	}
	slog.Warn("crypto prices: SQLite unavailable, keeping prices in memory", "op", op, "error", err)
}

// tryLoad reads persisted days into memory once when s.store is readable.
// On a store error it leaves s.loaded false so the next sync tick retries the
// load instead of treating the unreadable store as empty.
func (s *cryptoPriceSyncer) tryLoad(ctx context.Context, fromSync bool) (skipTick bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return false
	}
	if s.store == nil {
		s.loaded = true
		return false
	}
	rows, asOf, err := s.store.loadOnchainPrices(ctx)
	if err != nil {
		s.warnStoreLocked("load", err)
		if fromSync {
			firstFail := !s.loadFailed
			s.loadFailed = true
			return firstFail
		}
		return false
	}
	for _, r := range rows {
		s.byDay[r.Day] = r
	}
	if asOf.After(s.asOf) {
		s.asOf = asOf
	}
	s.loaded = true
	s.loadFailed = false
	s.loadWarned = false
	return false
}

// targetDays is the window kept filled: initial_days, limited to one month
// when there is no readable SQLite store.
func (s *cryptoPriceSyncer) targetDays() int {
	if s.initialDays <= 0 {
		return 0
	}
	s.mu.Lock()
	memOnly := s.store == nil || !s.loaded
	s.mu.Unlock()
	if memOnly {
		return min(s.initialDays, cryptoPricesMemoryOnlyMaxDays)
	}
	return s.initialDays
}

// cryptoPricesWindowsEnd is the end of day's last sampled window (19:00 UTC).
func cryptoPricesWindowsEnd(day civil.Date) time.Time {
	last := cryptoPricesSampleHours[len(cryptoPricesSampleHours)-1]
	return day.In(time.UTC).Add(time.Duration(last+1) * time.Hour)
}

// readyEnd returns the newest day whose sampled windows are all ingested:
// today once its 18:00 window closed (plus the finality margin) and rows past
// it exist, otherwise yesterday if its rows are in. When every day in the
// candidate window is already held in memory, it returns without probing
// BigQuery so idle hourly ticks are free.
func (s *cryptoPriceSyncer) readyEnd(ctx context.Context, now time.Time, target int) (civil.Date, bool, error) {
	today := civil.DateOf(now)
	cand := today
	if now.Before(cryptoPricesWindowsEnd(today).Add(cryptoPricesFinality)) {
		cand = today.AddDays(-1)
	}
	for {
		if len(s.missingDays(cand.AddDays(-(target-1)), cand)) == 0 {
			return cand, true, nil
		}
		from := cryptoPricesWindowsEnd(cand)
		latest, err := s.src.LatestEventTime(ctx, from)
		if err != nil {
			return civil.Date{}, false, err
		}
		if !latest.IsZero() && !latest.Before(from) {
			return cand, true, nil
		}
		if cand != today {
			return civil.Date{}, false, nil
		}
		cand = cand.AddDays(-1)
	}
}

// missingDays lists the days of [first, end] not held yet, newest first.
func (s *cryptoPriceSyncer) missingDays(first, end civil.Date) []civil.Date {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []civil.Date
	for day := end; !day.Before(first); day = day.AddDays(-1) {
		if _, ok := s.byDay[day.String()]; !ok {
			out = append(out, day)
		}
	}
	return out
}

// put stores synced days in memory and, when possible, in SQLite.
func (s *cryptoPriceSyncer) put(ctx context.Context, rows []onchainPriceDay, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		s.byDay[r.Day] = r
	}
	if at.After(s.asOf) {
		s.asOf = at
	}
	if s.store != nil {
		if err := s.store.upsertOnchainPrices(ctx, rows, at); err != nil {
			s.warnStoreLocked("write", err)
		} else {
			s.writeWarned = false
		}
	}
}

type cryptoPriceRun struct {
	end     civil.Date
	days    int
	queries int
	billed  int64
	errCode string
	idle    bool // nothing to do: not logged
}

func (s *cryptoPriceSyncer) syncOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("crypto prices sync panic", "panic", r)
		}
	}()
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(ctx, cryptoPricesSyncTimeout)
	defer cancel()
	run := s.sync(ctx)
	if run.idle {
		return
	}
	slog.Info("crypto prices sync",
		"window_end", run.end.String(),
		"days", run.days,
		"queries", run.queries,
		"bytes_billed", run.billed,
		"duration", time.Since(t0).String(),
		"error", run.errCode,
	)
}

func (s *cryptoPriceSyncer) sync(ctx context.Context) cryptoPriceRun {
	if s.initialDays <= 0 {
		return cryptoPriceRun{idle: true} // crypto_prices.initial_days: 0
	}
	if s.tryLoad(ctx, true) {
		return cryptoPriceRun{errCode: "store load failed"}
	}
	target := s.targetDays()
	now := s.now().UTC()
	s.mu.Lock()
	backingOff := now.Before(s.retryAfter)
	s.mu.Unlock()
	if backingOff {
		return cryptoPriceRun{idle: true}
	}

	end, ok, err := s.readyEnd(ctx, now, target)
	if err != nil {
		slog.Warn("crypto prices: readiness probe failed", "error", err)
		return cryptoPriceRun{errCode: "probe failed"}
	}
	if !ok {
		return cryptoPriceRun{errCode: "source not caught up"}
	}
	missing := s.missingDays(end.AddDays(-(target - 1)), end)
	if len(missing) == 0 {
		return cryptoPriceRun{end: end, idle: true}
	}

	today := civil.DateOf(now)
	run := cryptoPriceRun{end: end}
	hadProvisional := false
	for i := 0; i < len(missing) && run.queries < cryptoPricesMaxChunksPerSync; i += cryptoPricesMaxDaysPerQuery {
		days := slices.Clone(missing[i:min(i+cryptoPricesMaxDaysPerQuery, len(missing))])
		slices.SortFunc(days, func(a, b civil.Date) int { return a.Compare(b) })
		hours, billed, err := s.src.HourlyMedians(ctx, days, cryptoPricesMaxBytes(len(days)))
		run.queries++
		run.billed += billed
		if err != nil {
			slog.Warn("crypto prices: bigquery", "error", err)
			if billed > 0 || errors.Is(err, context.DeadlineExceeded) {
				s.mu.Lock()
				s.retryAfter = now.Add(cryptoPricesBilledRetry)
				s.mu.Unlock()
			}
			run.errCode = "query failed"
			return run
		}
		rows := dailyPricesFromHours(days, hours)
		toStore := make([]onchainPriceDay, 0, len(rows))
		for _, r := range rows {
			if r.EthUSD == nil && r.BtcUSD == nil {
				if d, err := civil.ParseDate(r.Day); err == nil && today.DaysSince(d) < cryptoPricesProvisionalDays {
					slog.Warn("crypto prices: recent day has no qualifying swaps yet, leaving provisional", "day", r.Day)
					hadProvisional = true
					continue
				}
				slog.Warn("crypto prices: day has no qualifying swaps, storing empty row", "day", r.Day)
			}
			toStore = append(toStore, r)
		}
		if len(toStore) > 0 {
			s.put(ctx, toStore, s.now().UTC())
		}
		run.days += len(toStore)
	}
	if hadProvisional && run.billed > 0 {
		s.mu.Lock()
		s.retryAfter = now.Add(cryptoPricesBilledRetry)
		s.mu.Unlock()
	}
	return run
}

// --- API ---

// CryptoPricesData is GET /api/opendata/crypto/prices.
type CryptoPricesData struct {
	Days         int               `json:"days"`
	Rows         []onchainPriceDay `json:"rows"`
	Source       string            `json:"source"`
	Method       string            `json:"method"`
	BtcNote      string            `json:"btc_note"`
	CoverageFrom string            `json:"coverage_from"` // earliest stored day, "" when none
	AsOf         string            `json:"as_of"`         // last successful sync (RFC3339), "" when never
}

// snapshot returns the days in [today − days, today] from memory.
func (s *cryptoPriceSyncer) snapshot(days int) CryptoPricesData {
	s.tryLoad(context.Background(), false)
	from := civil.DateOf(s.now().UTC()).AddDays(-days).String()
	out := CryptoPricesData{Days: days, Rows: []onchainPriceDay{},
		Source: cryptoPricesSource, Method: cryptoPricesMethod, BtcNote: cryptoPricesBtcNote}
	s.mu.Lock()
	defer s.mu.Unlock()
	for day, r := range s.byDay {
		if out.CoverageFrom == "" || day < out.CoverageFrom {
			out.CoverageFrom = day
		}
		if day >= from {
			out.Rows = append(out.Rows, r)
		}
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Day < out.Rows[j].Day })
	if !s.asOf.IsZero() {
		out.AsOf = s.asOf.UTC().Format(time.RFC3339)
	}
	return out
}

// cryptoPricesHandler serves GET /api/opendata/crypto/prices?days=7|30|90|365
// from the syncer's memory (never BigQuery).
func cryptoPricesHandler(s *cryptoPriceSyncer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPricesDaysAllowed)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s == nil {
			writeJSON(w, CryptoPricesData{Days: days, Rows: []onchainPriceDay{},
				Source: cryptoPricesSource, Method: cryptoPricesMethod, BtcNote: cryptoPricesBtcNote})
			return
		}
		writeJSON(w, s.snapshot(days))
	}
}
