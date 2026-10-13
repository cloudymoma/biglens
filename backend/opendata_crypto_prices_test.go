package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/option"
)

func pDay(s string) civil.Date {
	v, err := civil.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func pDayRange(from, to civil.Date) []civil.Date {
	var out []civil.Date
	for x := from; !x.After(to); x = x.AddDays(1) {
		out = append(out, x)
	}
	return out
}

// --- SQL builder ---

// The price query reads only the four Uniswap v3 pools' decoded Swap events,
// drops re-orged rows, and prunes through block_timestamp clustering with one
// literal one-hour range per sampled hour (00/06/12/18 UTC) generated from
// validated civil dates — no request input ever reaches the SQL.
func TestCryptoPricesSQL(t *testing.T) {
	sql, err := cryptoPricesSQL([]civil.Date{pDay("2026-10-09"), pDay("2026-10-10")})
	if err != nil {
		t.Fatalf("cryptoPricesSQL: %v", err)
	}
	for _, w := range []string{
		"bigquery-public-data.goog_blockchain_ethereum_mainnet_us.decoded_events",
		"event_signature = 'Swap(address,address,int256,int256,uint160,uint128,int24)'",
		"NOT removed",
		"JSON_VALUE(args, '$[6]')",
		"APPROX_QUANTILES(",
		"COUNT(*) AS swaps",
		"0xe0554a476a092703abdb3ef35c80e0d76d32939f", "0x88e6a0c2ddd26feeb64f039a2c41296fcb3f5640",
		"0x4585fe77225b41b697c938b018e2ac67ac5a20c0", "0xcbcdf9626bc03e24f779434178a73a0b4bad62ed",
		"(block_timestamp >= TIMESTAMP '2026-10-09 00:00:00+00' AND block_timestamp < TIMESTAMP '2026-10-09 01:00:00+00')",
		"(block_timestamp >= TIMESTAMP '2026-10-09 06:00:00+00' AND block_timestamp < TIMESTAMP '2026-10-09 07:00:00+00')",
		"(block_timestamp >= TIMESTAMP '2026-10-09 12:00:00+00' AND block_timestamp < TIMESTAMP '2026-10-09 13:00:00+00')",
		"(block_timestamp >= TIMESTAMP '2026-10-10 18:00:00+00' AND block_timestamp < TIMESTAMP '2026-10-10 19:00:00+00')",
	} {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in SQL:\n%s", w, sql)
		}
	}
	if n := strings.Count(sql, "(block_timestamp >= TIMESTAMP '"); n != 8 {
		t.Errorf("got %d hour ranges, want 8 (2 days × 4 sampled hours)", n)
	}
	for _, nw := range []string{"0x99ac8ca7087fa4a2a1fb6357269965a2014abc35", "@", "03:00:00", "TIMESTAMP_SUB", "CURRENT_TIMESTAMP"} {
		if strings.Contains(sql, nw) {
			t.Errorf("SQL must not contain %q:\n%s", nw, sql)
		}
	}
}

func TestCryptoPricesSQLRejectsBadInput(t *testing.T) {
	tooMany := pDayRange(pDay("2026-09-01"), pDay("2026-09-01").AddDays(cryptoPricesMaxDaysPerQuery))
	tests := []struct {
		name string
		days []civil.Date
	}{
		{"no days", nil},
		{"invalid date", []civil.Date{{Year: 2026, Month: 2, Day: 30}}},
		{"duplicate day", []civil.Date{pDay("2026-10-09"), pDay("2026-10-09")}},
		{"more than one query's worth", tooMany},
	}
	for _, tt := range tests {
		if _, err := cryptoPricesSQL(tt.days); err == nil {
			t.Errorf("%s: want an error", tt.name)
		}
	}
	if cryptoPricesMaxDaysPerQuery > 31 {
		t.Errorf("cryptoPricesMaxDaysPerQuery = %d, must stay ≤ 31", cryptoPricesMaxDaysPerQuery)
	}
}

// The byte cap is sized per sampled hour window. BigQuery rejects a query up
// front when its dry-run upper bound exceeds the cap, and that bound drifts
// (measured 2026-10-13: a recent day's 4 windows 687–719 MB, older 8-day
// chunks 4.27–4.63 GB), so the cap keeps ≥ 2.5× headroom over the largest
// measurement and a query never exceeds 16 GiB.
func TestCryptoPricesMaxBytes(t *testing.T) {
	if cryptoPricesBytesPerWindow != 512<<20 || cryptoPricesMaxDaysPerQuery != 8 {
		t.Errorf("budget = %d B/window, %d days/query; want 512 MiB and 8", cryptoPricesBytesPerWindow, cryptoPricesMaxDaysPerQuery)
	}
	if got := cryptoPricesMaxBytes(1); got != 4*cryptoPricesBytesPerWindow || float64(got) < 2.5*719_271_916 {
		t.Errorf("1 day cap = %d, want %d (≥ 2.5× a recent day's 719 MB upper bound)", got, 4*cryptoPricesBytesPerWindow)
	}
	if got := cryptoPricesMaxBytes(cryptoPricesMaxDaysPerQuery); got != 16<<30 || float64(got) < 2.5*4_632_190_582 {
		t.Errorf("%d-day cap = %d, want 16 GiB (≥ 2.5× an 8-day chunk's 4.63 GB upper bound)", cryptoPricesMaxDaysPerQuery, got)
	}
	if got := cryptoPricesMaxBytes(31); got != 16<<30 {
		t.Errorf("31-day cap = %d, want the 16 GiB bound", got)
	}
}

// --- tick math (verified 2026-10-09 12:00 UTC) ---

func TestUniswapTickMath(t *testing.T) {
	rel := func(got, want float64) float64 { return math.Abs(got-want) / want }
	if got := ethUSDFromTick(198079); rel(got, 2500.1636) > 1e-6 {
		t.Errorf("ETH/USD at tick 198079 = %v, want ≈ 2500.16", got)
	}
	if got := ethUSDFromTick(0); got != 1e12 {
		t.Errorf("ETH/USD at tick 0 = %v, want 1e12 (USDC 6 dec vs WETH 18 dec)", got)
	}
	if got := wethPerWBTCFromTick(265293); rel(got, 33.18582) > 1e-6 {
		t.Errorf("WETH per WBTC at tick 265293 = %v, want ≈ 33.19", got)
	}
	if got := wethPerWBTCFromTick(265290) * ethUSDFromTick(198079); rel(got, 82945.09) > 1e-6 {
		t.Errorf("BTC/USD = %v, want ≈ 82,945", got)
	}
}

// --- daily median ---

func pHourRow(hour, pair string, swaps, tick int64) priceHourRow {
	return priceHourRow{HourUTC: hour, Pair: pair, Swaps: swaps, MedianTick: bigquery.NullInt64{Int64: tick, Valid: true}}
}

// Real hourly medians of 2026-10-09: the daily price is the median of the
// available sampled hours, BTC pairs each WBTC/WETH hour with the same
// hour's ETH/USD, thin hours are skipped, and every requested day gets a row.
func TestDailyPricesFromHours(t *testing.T) {
	hours := []priceHourRow{
		pHourRow("2026-10-09 00:00", "eth", 1174, 198173), pHourRow("2026-10-09 00:00", "wbtc", 68, 265227),
		pHourRow("2026-10-09 06:00", "eth", 1104, 198093), pHourRow("2026-10-09 06:00", "wbtc", 40, 265233),
		pHourRow("2026-10-09 12:00", "eth", 1299, 198079), pHourRow("2026-10-09 12:00", "wbtc", 41, 265290),
		pHourRow("2026-10-09 18:00", "eth", 1306, 198142), pHourRow("2026-10-09 18:00", "wbtc", 25, 265287),
		// 2026-10-10: one thin ETH hour, one thin WBTC hour, a WBTC hour
		// without an ETH price, a NULL tick.
		pHourRow("2026-10-10 00:00", "eth", cryptoPricesMinETHSwaps-1, 190000),
		pHourRow("2026-10-10 00:00", "wbtc", 30, 265000),
		pHourRow("2026-10-10 06:00", "eth", 900, 198079),
		pHourRow("2026-10-10 06:00", "wbtc", cryptoPricesMinWBTCSwaps-1, 260000),
		pHourRow("2026-10-10 12:00", "eth", 800, 198079),
		pHourRow("2026-10-10 12:00", "wbtc", 12, 265290),
		{HourUTC: "2026-10-10 18:00", Pair: "eth", Swaps: 700},
		// not requested
		pHourRow("2026-10-12 00:00", "eth", 1000, 198079),
	}
	got := dailyPricesFromHours([]civil.Date{pDay("2026-10-11"), pDay("2026-10-09"), pDay("2026-10-10")}, hours)
	if len(got) != 3 || got[0].Day != "2026-10-09" || got[1].Day != "2026-10-10" || got[2].Day != "2026-10-11" {
		t.Fatalf("rows = %+v, want one row per requested day in order", got)
	}
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want)/want < 1e-6 }

	d9 := got[0]
	if !near(d9.EthUSD, 2490.56442) || d9.EthSwaps != 1174+1104+1299+1306 {
		t.Errorf("2026-10-09 ETH = %v (%d swaps), want median of 4 hours ≈ 2490.56 (4883 swaps)", pDeref(d9.EthUSD), d9.EthSwaps)
	}
	if !near(d9.BtcUSD, 82378.89275) || d9.BtcSwaps != 68+40+41+25 {
		t.Errorf("2026-10-09 BTC = %v (%d swaps), want ≈ 82,378.89 (174 swaps)", pDeref(d9.BtcUSD), d9.BtcSwaps)
	}

	d10 := got[1]
	if !near(d10.EthUSD, 2500.16359) || d10.EthSwaps != 900+800 {
		t.Errorf("2026-10-10 ETH = %v (%d swaps), want the two qualifying hours ≈ 2500.16 (1700 swaps)", pDeref(d10.EthUSD), d10.EthSwaps)
	}
	if !near(d10.BtcUSD, 82945.09430) || d10.BtcSwaps != 12 {
		t.Errorf("2026-10-10 BTC = %v (%d swaps), want only the 12:00 pair ≈ 82,945 (12 swaps)", pDeref(d10.BtcUSD), d10.BtcSwaps)
	}

	if d11 := got[2]; d11.EthUSD != nil || d11.BtcUSD != nil || d11.EthSwaps != 0 || d11.BtcSwaps != 0 {
		t.Errorf("a day without swaps must have NULL prices, got %+v", d11)
	}
}

func pDeref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// --- syncer ---

type fakePriceQuery struct {
	days     []civil.Date
	maxBytes int64
}

type fakePriceSource struct {
	mu       sync.Mutex
	ready    func(from time.Time) (time.Time, error) // nil: every window ingested
	probes   []time.Time
	queries  []fakePriceQuery
	err      error           // fail HourlyMedians
	billed   int64           // bytes billed by a failing query
	nullDays map[string]bool // days without any swap rows (all-NULL prices)
}

func (f *fakePriceSource) LatestEventTime(_ context.Context, from time.Time) (time.Time, error) {
	f.mu.Lock()
	f.probes = append(f.probes, from)
	ready := f.ready
	f.mu.Unlock()
	if ready == nil {
		return from.Add(30 * time.Minute), nil
	}
	return ready(from)
}

func (f *fakePriceSource) HourlyMedians(_ context.Context, days []civil.Date, maxBytes int64) ([]priceHourRow, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, fakePriceQuery{days: append([]civil.Date(nil), days...), maxBytes: maxBytes})
	if f.err != nil {
		return nil, f.billed, f.err
	}
	var rows []priceHourRow
	for _, day := range days {
		if f.nullDays[day.String()] {
			continue
		}
		for _, h := range cryptoPricesSampleHours {
			hour := day.String() + " " + strconv.Itoa(100 + h)[1:] + ":00"
			rows = append(rows, pHourRow(hour, "eth", 1000, 198079), pHourRow(hour, "wbtc", 30, 265293))
		}
	}
	return rows, int64(len(days)) * (350 << 20), nil
}

func (f *fakePriceSource) setNullDays(days ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nullDays = map[string]bool{}
	for _, d := range days {
		f.nullDays[d] = true
	}
}

func (f *fakePriceSource) setReady(ready func(from time.Time) (time.Time, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready = ready
}

func (f *fakePriceSource) snapshot() ([]time.Time, []fakePriceQuery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.probes...), append([]fakePriceQuery(nil), f.queries...)
}

func totalQueriedDays(queries []fakePriceQuery) int {
	n := 0
	for _, q := range queries {
		n += len(q.days)
	}
	return n
}

// flakyPriceStore is a cryptoPriceStore whose first failLoads loads fail.
type flakyPriceStore struct {
	mu        sync.Mutex
	failLoads int
	loads     int
	rows      []onchainPriceDay
	asOf      time.Time
	upserted  []onchainPriceDay
}

func (f *flakyPriceStore) loadOnchainPrices(context.Context) ([]onchainPriceDay, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loads <= f.failLoads {
		return nil, time.Time{}, errors.New("database is locked")
	}
	return append([]onchainPriceDay(nil), f.rows...), f.asOf, nil
}

func (f *flakyPriceStore) upsertOnchainPrices(_ context.Context, rows []onchainPriceDay, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserted = append(f.upserted, rows...)
	return nil
}

func openPricesTestStore(t *testing.T) *riskStore {
	t.Helper()
	s, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type priceClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *priceClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *priceClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func pAt(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

// Cold start fills initial_days in ≤8-day queries, newest first, persists
// every day, then syncs one increment per complete day. The readiness probe
// only runs when a day is actually missing.
func TestCryptoPriceSyncColdStartAndIncrement(t *testing.T) {
	store := openPricesTestStore(t)
	clock := &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src := &fakePriceSource{}
	s := newCryptoPriceSyncer(src, store, 40, clock.now)

	s.syncOnce(context.Background())
	probes, queries := src.snapshot()
	if len(probes) != 1 || !probes[0].Equal(pAt("2026-10-12T19:00:00Z")) {
		t.Errorf("probes = %v, want one probe of yesterday's 19:00 (before today's 18:00 window closed)", probes)
	}
	if len(queries) != 5 {
		t.Fatalf("cold start ran %d queries, want 5 (5 × 8 days)", len(queries))
	}
	wantSpans := [][2]string{
		{"2026-10-05", "2026-10-12"}, {"2026-09-27", "2026-10-04"}, {"2026-09-19", "2026-09-26"},
		{"2026-09-11", "2026-09-18"}, {"2026-09-03", "2026-09-10"},
	}
	for i, q := range queries {
		first, last := q.days[0], q.days[len(q.days)-1]
		if first.String() != wantSpans[i][0] || last.String() != wantSpans[i][1] {
			t.Errorf("query %d covers %s..%s, want %s..%s (newest first)", i, first, last, wantSpans[i][0], wantSpans[i][1])
		}
		if q.maxBytes != 16<<30 {
			t.Errorf("query %d cap = %d, want 16 GiB for 8 days", i, q.maxBytes)
		}
	}
	stored, _, err := store.loadOnchainPrices(context.Background())
	if err != nil || len(stored) != 40 || stored[0].Day != "2026-09-03" || stored[39].Day != "2026-10-12" {
		t.Fatalf("stored %d rows (%v), want 40 days 2026-09-03..2026-10-12", len(stored), err)
	}

	// Later ticks the same day: nothing missing, so neither probe nor query.
	for _, at := range []string{"2026-10-13T11:00:00Z", "2026-10-13T19:10:00Z"} {
		clock.set(pAt(at))
		s.syncOnce(context.Background())
	}
	if p, q := src.snapshot(); len(p) != 1 || len(q) != 5 {
		t.Errorf("ticks with nothing missing ran %d probes and %d queries, want none", len(p)-1, len(q)-5)
	}

	// After today's 18:00 window has been ingested: one probe, one new day.
	clock.set(pAt("2026-10-13T20:00:00Z"))
	s.syncOnce(context.Background())
	probes, queries = src.snapshot()
	if len(queries) != 6 || len(queries[5].days) != 1 || queries[5].days[0].String() != "2026-10-13" {
		t.Fatalf("increment queries = %+v, want one query for 2026-10-13", queries[5:])
	}
	if queries[5].maxBytes != 4*cryptoPricesBytesPerWindow {
		t.Errorf("increment cap = %d, want %d", queries[5].maxBytes, 4*cryptoPricesBytesPerWindow)
	}
	if len(probes) != 2 || !probes[1].Equal(pAt("2026-10-13T19:00:00Z")) {
		t.Errorf("probes = %v, want exactly one more, of today's 19:00", probes)
	}

	// And quiet again until the next day's window closes.
	clock.set(pAt("2026-10-14T18:00:00Z"))
	s.syncOnce(context.Background())
	if p, q := src.snapshot(); len(p) != 2 || len(q) != 6 {
		t.Errorf("next day before 19:30: %d probes, %d queries; want no new ones", len(p), len(q))
	}
}

// Only days whose 18:00 window has been ingested are synced: a lagging
// source falls back to yesterday, then to nothing; probe errors never bill.
func TestCryptoPriceSyncReadiness(t *testing.T) {
	ctx := context.Background()
	todayWindow := pAt("2026-10-13T19:00:00Z")

	// Today not ingested yet: fall back to yesterday.
	src := &fakePriceSource{ready: func(from time.Time) (time.Time, error) {
		if from.Equal(todayWindow) {
			return time.Time{}, nil
		}
		return from.Add(time.Minute), nil
	}}
	s := newCryptoPriceSyncer(src, openPricesTestStore(t), 5, func() time.Time { return pAt("2026-10-13T20:00:00Z") })
	s.syncOnce(ctx)
	probes, queries := src.snapshot()
	if len(probes) != 2 || !probes[0].Equal(todayWindow) || !probes[1].Equal(pAt("2026-10-12T19:00:00Z")) {
		t.Errorf("probes = %v, want today's then yesterday's 19:00", probes)
	}
	if len(queries) != 1 || queries[0].days[len(queries[0].days)-1].String() != "2026-10-12" {
		t.Errorf("queries = %+v, want one query ending 2026-10-12", queries)
	}

	// Nothing ingested: no query.
	src = &fakePriceSource{ready: func(time.Time) (time.Time, error) { return time.Time{}, nil }}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 5, func() time.Time { return pAt("2026-10-13T20:00:00Z") })
	s.syncOnce(ctx)
	if _, q := src.snapshot(); len(q) != 0 {
		t.Errorf("a source that has not caught up ran %d queries, want 0", len(q))
	}

	// Probe error: no query, and the next tick probes again (no back-off).
	src = &fakePriceSource{ready: func(time.Time) (time.Time, error) { return time.Time{}, errors.New("quota") }}
	clock := &priceClock{t: pAt("2026-10-13T20:00:00Z")}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 5, clock.now)
	s.syncOnce(ctx)
	clock.set(pAt("2026-10-13T21:00:00Z"))
	s.syncOnce(ctx)
	if p, q := src.snapshot(); len(q) != 0 || len(p) < 2 {
		t.Errorf("probe errors: %d probes, %d queries; want re-probing and no query", len(p), len(q))
	}

	// Before 19:30 UTC today's window is not even probed.
	src = &fakePriceSource{}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 5, func() time.Time { return pAt("2026-10-13T19:10:00Z") })
	s.syncOnce(ctx)
	if p, _ := src.snapshot(); len(p) != 1 || !p[0].Equal(pAt("2026-10-12T19:00:00Z")) {
		t.Errorf("probes at 19:10 = %v, want only yesterday's window", p)
	}

	// Everything through yesterday is held and today's window is not in yet:
	// one probe for today, no fallback probe for yesterday, no query.
	clock = &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src = &fakePriceSource{}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 5, clock.now)
	s.syncOnce(ctx) // holds 2026-10-08..2026-10-12
	src.setReady(func(from time.Time) (time.Time, error) {
		if from.Equal(todayWindow) {
			return time.Time{}, nil
		}
		return from.Add(time.Minute), nil
	})
	clock.set(pAt("2026-10-13T20:00:00Z"))
	s.syncOnce(ctx)
	if p, q := src.snapshot(); len(p) != 2 || !p[1].Equal(todayWindow) || len(q) != 1 {
		t.Errorf("today lagging with yesterday held: probes %v, %d queries; want one extra probe (today) and no query", p, len(q)-1)
	}
}

// A billed failure backs off 6 hours; an unbilled one retries next tick.
func TestCryptoPriceSyncBackoff(t *testing.T) {
	ctx := context.Background()
	clock := &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src := &fakePriceSource{err: errors.New("row decode failed"), billed: 300 << 20}
	s := newCryptoPriceSyncer(src, openPricesTestStore(t), 3, clock.now)

	s.syncOnce(ctx)
	clock.set(clock.now().Add(time.Hour))
	s.syncOnce(ctx)
	if _, q := src.snapshot(); len(q) != 1 {
		t.Fatalf("billed failure: %d queries within the back-off, want 1", len(q))
	}
	clock.set(pAt("2026-10-13T10:00:00Z").Add(cryptoPricesBilledRetry + time.Minute))
	s.syncOnce(ctx)
	if _, q := src.snapshot(); len(q) != 2 {
		t.Errorf("after %v: %d queries, want a retry", cryptoPricesBilledRetry, len(q))
	}
	if cryptoPricesBilledRetry != 6*time.Hour {
		t.Errorf("cryptoPricesBilledRetry = %v, want 6h", cryptoPricesBilledRetry)
	}

	// Unbilled failure (e.g. rejected up front by the byte cap): next tick retries.
	clock = &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src = &fakePriceSource{err: errors.New("bytesBilledLimitExceeded")}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 3, clock.now)
	s.syncOnce(ctx)
	clock.set(clock.now().Add(time.Hour))
	s.syncOnce(ctx)
	if _, q := src.snapshot(); len(q) != 2 {
		t.Errorf("unbilled failure: %d queries after one tick, want 2", len(q))
	}

	// A timed-out job may still bill: treat as billed.
	clock = &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src = &fakePriceSource{err: context.DeadlineExceeded}
	s = newCryptoPriceSyncer(src, openPricesTestStore(t), 3, clock.now)
	s.syncOnce(ctx)
	clock.set(clock.now().Add(time.Hour))
	s.syncOnce(ctx)
	if _, q := src.snapshot(); len(q) != 1 {
		t.Errorf("deadline exceeded: %d queries within the back-off, want 1", len(q))
	}
}

func TestCryptoPriceSyncDisabledAndMemoryOnly(t *testing.T) {
	ctx := context.Background()
	src := &fakePriceSource{}
	newCryptoPriceSyncer(src, openPricesTestStore(t), 0, func() time.Time { return pAt("2026-10-13T10:00:00Z") }).syncOnce(ctx)
	if p, q := src.snapshot(); len(p) != 0 || len(q) != 0 {
		t.Errorf("initial_days 0: %d probes, %d queries; want the sync disabled", len(p), len(q))
	}

	// Without SQLite every restart is a cold start, so it is clamped.
	src = &fakePriceSource{}
	s := newCryptoPriceSyncer(src, nil, 365, func() time.Time { return pAt("2026-10-13T10:00:00Z") })
	s.syncOnce(ctx)
	_, queries := src.snapshot()
	n := 0
	for _, q := range queries {
		n += len(q.days)
	}
	if n != cryptoPricesMemoryOnlyMaxDays || cryptoPricesMemoryOnlyMaxDays != 31 {
		t.Errorf("memory-only cold start covered %d days, want %d", n, cryptoPricesMemoryOnlyMaxDays)
	}
	if got := s.snapshot(30); len(got.Rows) != 30 || got.Rows[29].Day != "2026-10-12" {
		t.Errorf("memory-only snapshot = %d rows, want 30 ending 2026-10-12", len(got.Rows))
	}
}

// A restart serves the persisted days without re-querying them.
func TestCryptoPriceSyncRestart(t *testing.T) {
	ctx := context.Background()
	store := openPricesTestStore(t)
	now := func() time.Time { return pAt("2026-10-13T10:00:00Z") }
	newCryptoPriceSyncer(&fakePriceSource{}, store, 10, now).syncOnce(ctx)

	src := &fakePriceSource{}
	s := newCryptoPriceSyncer(src, store, 10, now)
	if got := s.snapshot(7); len(got.Rows) != 7 || got.AsOf == "" || got.CoverageFrom != "2026-10-03" {
		t.Errorf("after restart snapshot = %+v, want 7 persisted rows, as_of and coverage_from 2026-10-03", got)
	}
	s.syncOnce(ctx)
	if p, q := src.snapshot(); len(p) != 0 || len(q) != 0 {
		t.Errorf("restart ran %d probes and %d queries, want none (nothing missing)", len(p), len(q))
	}
}

// A store that cannot be read is not an empty store: the tick whose load
// fails syncs nothing; while the load keeps failing the sync runs
// memory-only with the one-month cold start (never initial_days); a load
// that recovers serves the stored days and fetches only what is missing.
func TestCryptoPriceSyncLoadFailure(t *testing.T) {
	ctx := context.Background()
	clock := &priceClock{t: pAt("2026-10-13T10:00:00Z")}

	broken := openPricesTestStore(t)
	broken.Close()
	src := &fakePriceSource{}
	s := newCryptoPriceSyncer(src, broken, 365, clock.now)
	s.syncOnce(ctx)
	if p, q := src.snapshot(); len(p) != 0 || len(q) != 0 {
		t.Fatalf("tick with a failed load: %d probes, %d queries; want none", len(p), len(q))
	}
	clock.set(pAt("2026-10-13T11:00:00Z"))
	s.syncOnce(ctx)
	_, queries := src.snapshot()
	if n := totalQueriedDays(queries); n != cryptoPricesMemoryOnlyMaxDays {
		t.Errorf("load still failing: synced %d days, want the %d-day memory-only cold start, not initial_days 365", n, cryptoPricesMemoryOnlyMaxDays)
	}
	if got := s.snapshot(30); len(got.Rows) != 30 {
		t.Errorf("memory-only snapshot = %d rows, want 30", len(got.Rows))
	}

	// A transient failure (e.g. SQLite busy): the next tick loads the stored
	// days and only fetches the missing ones, over the full window.
	var stored []onchainPriceDay
	for _, day := range pDayRange(pDay("2026-10-04"), pDay("2026-10-12")) {
		eth := 2500.0
		stored = append(stored, onchainPriceDay{Day: day.String(), EthUSD: &eth})
	}
	flaky := &flakyPriceStore{failLoads: 1, rows: stored, asOf: pAt("2026-10-12T19:31:00Z")}
	src = &fakePriceSource{}
	s = newCryptoPriceSyncer(src, nil, 12, clock.now)
	s.store = flaky
	s.syncOnce(ctx)
	if p, q := src.snapshot(); len(p) != 0 || len(q) != 0 {
		t.Fatalf("tick with a failed load: %d probes, %d queries; want none", len(p), len(q))
	}
	s.syncOnce(ctx)
	_, queries = src.snapshot()
	if len(queries) != 1 || len(queries[0].days) != 3 || queries[0].days[0].String() != "2026-10-01" || queries[0].days[2].String() != "2026-10-03" {
		t.Fatalf("after the load recovered: queries %+v, want only the missing 2026-10-01..03", queries)
	}
	if got := s.snapshot(12); len(got.Rows) != 12 || got.AsOf == "" {
		t.Errorf("snapshot = %d rows (as_of %q), want 12 (9 stored + 3 fetched)", len(got.Rows), got.AsOf)
	}
	if flaky.loads != 2 {
		t.Errorf("loads = %d, want 2 (one failed, one retried on the next tick)", flaky.loads)
	}
}

// A day without any qualifying swap is not final while it is recent (< 3
// days old): it is neither stored nor served, and the next tick re-queries
// it. Older empty days are stored (with a warning) so they are not
// re-queried forever.
func TestCryptoPriceSyncProvisionalNullDays(t *testing.T) {
	ctx := context.Background()
	store := openPricesTestStore(t)
	clock := &priceClock{t: pAt("2026-10-13T10:00:00Z")}
	src := &fakePriceSource{}
	src.setNullDays("2026-10-12", "2026-10-11", "2026-10-10", "2026-10-05")
	s := newCryptoPriceSyncer(src, store, 10, clock.now) // window 2026-10-03..2026-10-12
	s.syncOnce(ctx)

	rows, _, err := store.loadOnchainPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byDay := map[string]onchainPriceDay{}
	for _, r := range rows {
		byDay[r.Day] = r
	}
	for _, day := range []string{"2026-10-11", "2026-10-12"} {
		if _, ok := byDay[day]; ok {
			t.Errorf("%s (recent, no swaps) was stored; it must stay provisional", day)
		}
	}
	for _, day := range []string{"2026-10-10", "2026-10-05"} {
		if r, ok := byDay[day]; !ok || r.EthUSD != nil || r.BtcUSD != nil {
			t.Errorf("%s (≥ 3 days old, no swaps) = %+v (stored %v), want stored with NULL prices", day, r, ok)
		}
	}
	if len(rows) != 8 {
		t.Errorf("stored %d days, want 8 (10 minus the 2 provisional)", len(rows))
	}
	if got := s.snapshot(7); len(got.Rows) != 5 {
		t.Errorf("snapshot(7) = %d rows, want 5 (provisional days are not served)", len(got.Rows))
	}

	// The data arrives: the next tick re-queries only the provisional days.
	src.setNullDays()
	clock.set(pAt("2026-10-13T11:00:00Z"))
	s.syncOnce(ctx)
	_, queries := src.snapshot()
	last := queries[len(queries)-1]
	if len(queries) != 3 || len(last.days) != 2 || last.days[0].String() != "2026-10-11" || last.days[1].String() != "2026-10-12" {
		t.Fatalf("retry queries = %+v, want one query for 2026-10-11..12", queries[1:])
	}
	rows, _, _ = store.loadOnchainPrices(ctx)
	if len(rows) != 10 || rows[9].EthUSD == nil {
		t.Errorf("after the retry stored %d days (last %+v), want 10 with prices for 2026-10-12", len(rows), rows[len(rows)-1])
	}
}

// --- handler ---

func getPrices(t *testing.T, s *cryptoPriceSyncer, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	cryptoPricesHandler(s)(w, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/prices?"+query, nil))
	return w
}

func TestCryptoPricesHandlerDaysAllowList(t *testing.T) {
	s := newCryptoPriceSyncer(&fakePriceSource{}, nil, 0, func() time.Time { return pAt("2026-10-13T10:00:00Z") })
	for _, q := range []string{"", "days=7", "days=30", "days=90", "days=365"} {
		if w := getPrices(t, s, q); w.Code != http.StatusOK {
			t.Errorf("%q: status %d, want 200", q, w.Code)
		}
	}
	for _, q := range []string{"days=45", "days=abc", "days=-7", "days=3650", "days=7%3BDROP", "days=7.5"} {
		if w := getPrices(t, s, q); w.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, w.Code)
		}
	}
}

// The endpoint serves SQLite/memory only — never BigQuery — with the
// documented row shape and metadata; NULL prices stay null.
func TestCryptoPricesHandlerJSON(t *testing.T) {
	store := openPricesTestStore(t)
	now := func() time.Time { return pAt("2026-10-13T10:00:00Z") }
	newCryptoPriceSyncer(&fakePriceSource{}, store, 10, now).syncOnce(context.Background())
	if err := store.upsertOnchainPrices(context.Background(), []onchainPriceDay{{Day: "2026-10-12"}}, pAt("2026-10-13T09:00:00Z")); err != nil {
		t.Fatal(err)
	}

	src := &fakePriceSource{}
	s := newCryptoPriceSyncer(src, store, 10, now)
	w := getPrices(t, s, "days=7")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if p, q := src.snapshot(); len(p) != 0 || len(q) != 0 {
		t.Errorf("the handler touched BigQuery (%d probes, %d queries)", len(p), len(q))
	}
	var body struct {
		Days         int              `json:"days"`
		Rows         []map[string]any `json:"rows"`
		Source       string           `json:"source"`
		Method       string           `json:"method"`
		BtcNote      string           `json:"btc_note"`
		CoverageFrom string           `json:"coverage_from"`
		AsOf         string           `json:"as_of"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Days != 7 || body.Source != "uniswap_v3" || body.Method != "median of sampled hours (00/06/12/18 UTC)" {
		t.Errorf("metadata = %+v", body)
	}
	if !strings.Contains(body.BtcNote, "WBTC-derived") {
		t.Errorf("btc_note = %q, want it labelled WBTC-derived", body.BtcNote)
	}
	if body.CoverageFrom != "2026-10-03" {
		t.Errorf("coverage_from = %q, want the earliest stored day 2026-10-03", body.CoverageFrom)
	}
	if _, err := time.Parse(time.RFC3339, body.AsOf); err != nil {
		t.Errorf("as_of = %q, want RFC3339: %v", body.AsOf, err)
	}
	if len(body.Rows) != 7 || body.Rows[0]["day"] != "2026-10-06" || body.Rows[6]["day"] != "2026-10-12" {
		t.Fatalf("rows = %v, want the 7 days 2026-10-06..2026-10-12", body.Rows)
	}
	for _, k := range []string{"day", "eth_usd", "btc_usd", "eth_swaps", "btc_swaps"} {
		if _, ok := body.Rows[0][k]; !ok {
			t.Errorf("row is missing %q: %v", k, body.Rows[0])
		}
	}
	if eth, ok := body.Rows[0]["eth_usd"].(float64); !ok || math.Abs(eth-2500.16) > 0.01 {
		t.Errorf("eth_usd = %v, want ≈ 2500.16", body.Rows[0]["eth_usd"])
	}
	if last := body.Rows[6]; last["eth_usd"] != nil || last["btc_usd"] != nil {
		t.Errorf("a day without prices must serialize null, got %v", last)
	}
}

// --- BigQuery source ---

// jobsQueryFake serves the jobs.insert → getQueryResults → jobs.get round
// trip of q.Run / job.Wait / job.Read with one canned result set.
type jobsQueryFake struct {
	cols   []bqCol
	rows   [][]any
	billed string

	mu       sync.Mutex
	inserted []insertedQueryJob
}

type insertedQueryJob struct {
	sql      string
	labels   map[string]string
	maxBytes string
}

func (f *jobsQueryFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobRef := map[string]string{"projectId": "test-project", "jobId": "prices-job", "location": "US"}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
		var job struct {
			Configuration struct {
				Labels map[string]string `json:"labels"`
				Query  struct {
					Query              string `json:"query"`
					MaximumBytesBilled string `json:"maximumBytesBilled"`
				} `json:"query"`
			} `json:"configuration"`
		}
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.inserted = append(f.inserted, insertedQueryJob{sql: job.Configuration.Query.Query,
			labels: job.Configuration.Labels, maxBytes: job.Configuration.Query.MaximumBytesBilled})
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"jobReference":  jobRef,
			"configuration": map[string]any{"query": map[string]any{"query": job.Configuration.Query.Query}},
			"status":        map[string]any{"state": "RUNNING"},
		})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/queries/"):
		fields := make([]map[string]string, len(f.cols))
		for i, c := range f.cols {
			fields[i] = map[string]string{"name": c.name, "type": c.typ, "mode": "NULLABLE"}
		}
		rows := make([]map[string]any, len(f.rows))
		for i, row := range f.rows {
			cells := make([]map[string]any, len(row))
			for j, v := range row {
				cells[j] = map[string]any{"v": v}
			}
			rows[i] = map[string]any{"f": cells}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"kind": "bigquery#getQueryResultsResponse", "jobReference": jobRef, "jobComplete": true,
			"schema": map[string]any{"fields": fields}, "rows": rows, "totalRows": strconv.Itoa(len(rows)),
		})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
		json.NewEncoder(w).Encode(map[string]any{
			"jobReference":  jobRef,
			"configuration": map[string]any{"query": map[string]any{"query": "SELECT 1"}},
			"status":        map[string]any{"state": "DONE"},
			"statistics": map[string]any{
				"totalBytesProcessed": f.billed,
				"query":               map[string]any{"totalBytesBilled": f.billed, "totalBytesProcessed": f.billed},
			},
		})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

func TestBqCryptoPriceSourceHourlyMedians(t *testing.T) {
	fake := &jobsQueryFake{
		cols: []bqCol{{"hour_utc", "STRING"}, {"pair", "STRING"}, {"swaps", "INTEGER"}, {"median_tick", "INTEGER"}},
		rows: [][]any{
			{"2026-10-09 12:00", "eth", "1299", "198079"},
			{"2026-10-09 12:00", "wbtc", "41", "265290"},
		},
		billed: "376438784",
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	src := bqCryptoPriceSource{client: client}
	days := []civil.Date{pDay("2026-10-09")}
	rows, billed, err := src.HourlyMedians(context.Background(), days, cryptoPricesMaxBytes(1))
	if err != nil {
		t.Fatalf("HourlyMedians: %v", err)
	}
	if billed != 376438784 {
		t.Errorf("billed = %d, want 376438784", billed)
	}
	if len(rows) != 2 || rows[0] != pHourRow("2026-10-09 12:00", "eth", 1299, 198079) || rows[1].Pair != "wbtc" || rows[1].MedianTick.Int64 != 265290 {
		t.Errorf("rows = %+v", rows)
	}
	if len(fake.inserted) != 1 {
		t.Fatalf("inserted %d jobs, want 1", len(fake.inserted))
	}
	job := fake.inserted[0]
	want, _ := cryptoPricesSQL(days)
	if job.sql != want {
		t.Errorf("job SQL differs from cryptoPricesSQL:\n%s", job.sql)
	}
	if job.labels["app"] != "biglens" || job.maxBytes != strconv.FormatInt(cryptoPricesMaxBytes(1), 10) {
		t.Errorf("job labels/cap = %v / %q, want app=biglens and %d", job.labels, job.maxBytes, cryptoPricesMaxBytes(1))
	}
}

func TestBqCryptoPriceSourceLatestEventTime(t *testing.T) {
	from := pAt("2026-10-12T19:00:00Z")
	ts := strconv.FormatInt(from.Add(42*time.Minute).UnixMicro(), 10)
	var null bool
	fake := &scriptedBQ{respond: func(sql string) bqScript {
		if null {
			return bqScript{cols: []bqCol{{"t", "TIMESTAMP"}}, rows: [][]any{{nil}}}
		}
		return bqScript{cols: []bqCol{{"t", "TIMESTAMP"}}, rows: [][]any{{ts}}}
	}}
	src := bqCryptoPriceSource{client: newScriptedBQClient(t, fake)}

	got, err := src.LatestEventTime(context.Background(), from)
	if err != nil || !got.Equal(from.Add(42*time.Minute)) {
		t.Fatalf("LatestEventTime = %v, %v; want %v", got, err, from.Add(42*time.Minute))
	}
	null = true
	if got, err := src.LatestEventTime(context.Background(), from); err != nil || !got.IsZero() {
		t.Errorf("no rows yet: got %v, %v; want the zero time", got, err)
	}
	reqs := fake.sent()
	if len(reqs) != 2 {
		t.Fatalf("sent %d probes, want 2", len(reqs))
	}
	r := reqs[0]
	for _, w := range []string{"goog_blockchain_ethereum_mainnet_us.decoded_events", "MAX(block_timestamp) AS t",
		"block_timestamp >= @from", "TIMESTAMP_ADD(@from, INTERVAL 2 HOUR)"} {
		if !strings.Contains(r.sql, w) {
			t.Errorf("probe SQL missing %q:\n%s", w, r.sql)
		}
	}
	if r.labels["app"] != "biglens" || r.maxBytes != strconv.FormatInt(256<<20, 10) {
		t.Errorf("probe labels/cap = %v / %q, want app=biglens and 256 MiB", r.labels, r.maxBytes)
	}
}
