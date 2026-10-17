package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/civil"
)

const (
	btcPriceCoverageStart    = "2010-08-18"
	btcPriceCoinbaseStart    = "2015-07-20"
	btcCoinbaseMaxCandleDays = 300

	defaultBlockchainPriceURL = "https://api.blockchain.info/charts/market-price?timespan=all&sampled=false&format=json"
	defaultCoinMetricsPriceURL = "https://community-api.coinmetrics.io/v4/timeseries/asset-metrics?assets=btc&metrics=PriceUSD&frequency=1d&page_size=10000&start_time=2010-08-18&end_time=2015-07-25"
	defaultCoinbaseCandlesURL = "https://api.exchange.coinbase.com/products/BTC-USD/candles"
	defaultKrakenOHLCURL      = "https://api.kraken.com/0/public/OHLC?pair=XBTUSD&interval=1440"
)

type BtcDateRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type BtcPriceCoverage struct {
	Complete      bool           `json:"complete"`
	Earliest      string         `json:"earliest"`
	Latest        string         `json:"latest"`
	TotalDays     int            `json:"total_days"`
	MissingDays   int            `json:"missing_days"`
	MissingRanges []BtcDateRange `json:"missing_ranges,omitempty"`
	Sources       map[string]int `json:"sources"`
}

type btcPriceSyncer struct {
	store              *riskStore
	settings           btcSettings
	now                func() time.Time
	blockchainURL      string
	coinmetricsURL     string
	coinbaseCandlesURL string
	krakenOHLCURL      string

	touched   atomic.Bool
	warming   atomic.Bool
	syncMu    sync.Mutex
	mu        sync.RWMutex
	loaded    bool
	byDay     map[string]btcPriceDay
	lastSync  time.Time
}

func newBtcPriceSyncer(store *riskStore, settings btcSettings, now func() time.Time) *btcPriceSyncer {
	if now == nil {
		now = time.Now
	}
	return &btcPriceSyncer{
		store:              store,
		settings:           settings,
		now:                now,
		blockchainURL:      defaultBlockchainPriceURL,
		coinmetricsURL:     defaultCoinMetricsPriceURL,
		coinbaseCandlesURL: defaultCoinbaseCandlesURL,
		krakenOHLCURL:      defaultKrakenOHLCURL,
		byDay:              make(map[string]btcPriceDay),
	}
}

func (s *btcPriceSyncer) touch() {
	if s == nil {
		return
	}
	s.touched.Store(true)
}

func (s *btcPriceSyncer) touchAsync() {
	if s == nil {
		return
	}
	s.touched.Store(true)
	s.mu.RLock()
	recent := time.Since(s.lastSync) < 10*time.Minute
	s.mu.RUnlock()
	if recent {
		return
	}
	if s.warming.CompareAndSwap(false, true) {
		go func() {
			defer s.warming.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.syncOnce(ctx)
		}()
	}
}

func (s *btcPriceSyncer) ensureLoaded(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	if s.store != nil {
		if rows, err := s.store.loadBtcPrices(ctx); err == nil {
			for _, r := range rows {
				s.byDay[r.Day] = r
			}
			s.loaded = true
			return
		}
	}
	s.loaded = true
}

func (s *btcPriceSyncer) putRows(ctx context.Context, rows []btcPriceDay) {
	if len(rows) == 0 {
		return
	}
	now := s.now().UTC()
	if s.store != nil {
		if err := s.store.upsertBtcPrices(ctx, rows, now); err != nil {
			slog.Warn("btc prices: sqlite upsert failed", "error", err)
		}
	}
	at := now.Format(time.RFC3339)
	s.mu.Lock()
	for _, r := range rows {
		if r.Day == "" || r.Close <= 0 {
			continue
		}
		r.SyncedAt = at
		s.byDay[r.Day] = r
	}
	s.lastSync = now
	s.mu.Unlock()
}

func (s *btcPriceSyncer) syncOnce(ctx context.Context) {
	if s == nil || !s.touched.Load() {
		return
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	s.ensureLoaded(ctx)

	s.mu.RLock()
	var earliest, latestExchange string
	for d, r := range s.byDay {
		if earliest == "" || d < earliest {
			earliest = d
		}
		if (r.Source == "coinbase" || r.Source == "kraken") && d > latestExchange {
			latestExchange = d
		}
	}
	s.mu.RUnlock()

	// 1. Bootstrap early history (2010-08-18..2015-07-19) if missing.
	if earliest == "" || earliest > btcPriceCoverageStart {
		var boot []btcPriceDay
		if s.settings.Providers["blockchain_com"] {
			boot = s.fetchBlockchainBootstrap(ctx)
		}
		if len(boot) == 0 && s.settings.Providers["coinmetrics"] {
			boot = s.fetchCoinMetricsBootstrap(ctx)
		}
		if len(boot) > 0 {
			s.putRows(ctx, boot)
		}
	}

	// 2. Daily candles from 2015-07-20 (or latestExchange + 1 day) through yesterday UTC.
	yesterday := civil.DateOf(s.now().UTC()).AddDays(-1)
	startDate, _ := civil.ParseDate(btcPriceCoinbaseStart)
	if latestExchange != "" {
		if d, err := civil.ParseDate(latestExchange); err == nil {
			next := d.AddDays(1)
			if next.After(startDate) {
				startDate = next
			}
		}
	}
	if startDate.After(yesterday) {
		return
	}

	var synced bool
	if s.settings.Providers["coinbase"] {
		rows, ok := s.fetchCoinbaseCandlesRange(ctx, startDate, yesterday)
		if len(rows) > 0 {
			s.putRows(ctx, rows)
		}
		synced = ok
	}
	if !synced && s.settings.Providers["kraken"] {
		if rows := s.fetchKrakenOHLC(ctx, startDate, yesterday); len(rows) > 0 {
			s.putRows(ctx, rows)
		}
	}
}

func (s *btcPriceSyncer) fetchBlockchainBootstrap(ctx context.Context) []btcPriceDay {
	var payload struct {
		Values []struct {
			X int64   `json:"x"`
			Y float64 `json:"y"`
		} `json:"values"`
	}
	if code := btcGetJSON(ctx, s.blockchainURL, btcDefaultMaxBody, &payload); code != "" {
		slog.Warn("btc prices: blockchain.com bootstrap failed", "code", code)
		return nil
	}
	var out []btcPriceDay
	for _, pt := range payload.Values {
		if pt.Y <= 0 {
			continue
		}
		day := time.Unix(pt.X, 0).UTC().Format("2006-01-02")
		if day < btcPriceCoverageStart {
			continue
		}
		out = append(out, btcPriceDay{
			Day:    day,
			Close:  pt.Y,
			Source: "blockchain_com",
		})
	}
	return out
}

func (s *btcPriceSyncer) fetchCoinMetricsBootstrap(ctx context.Context) []btcPriceDay {
	nextURL := s.coinmetricsURL
	var out []btcPriceDay
	for page := 0; page < 4 && nextURL != ""; page++ {
		var payload struct {
			Data []struct {
				Time     string `json:"time"`
				PriceUSD string `json:"PriceUSD"`
			} `json:"data"`
			NextPageURL string `json:"next_page_url"`
		}
		if code := btcGetJSON(ctx, nextURL, btcDefaultMaxBody, &payload); code != "" {
			slog.Warn("btc prices: coinmetrics bootstrap failed", "code", code)
			break
		}
		for _, r := range payload.Data {
			if len(r.Time) < 10 || r.PriceUSD == "" {
				continue
			}
			day := r.Time[:10]
			if day < btcPriceCoverageStart {
				continue
			}
			v, err := strconv.ParseFloat(r.PriceUSD, 64)
			if err != nil || v <= 0 {
				continue
			}
			out = append(out, btcPriceDay{
				Day:    day,
				Close:  v,
				Source: "coinmetrics",
			})
		}
		nextURL = payload.NextPageURL
	}
	return out
}

func (s *btcPriceSyncer) fetchCoinbaseCandlesRange(ctx context.Context, from, to civil.Date) ([]btcPriceDay, bool) {
	var all []btcPriceDay
	for cur := from; !cur.After(to); {
		chunkEnd := cur.AddDays(btcCoinbaseMaxCandleDays - 1)
		if chunkEnd.After(to) {
			chunkEnd = to
		}
		u, err := url.Parse(s.coinbaseCandlesURL)
		if err != nil {
			return all, false
		}
		q := u.Query()
		q.Set("granularity", "86400")
		q.Set("start", cur.In(time.UTC).Format(time.RFC3339))
		q.Set("end", chunkEnd.In(time.UTC).Format(time.RFC3339))
		u.RawQuery = q.Encode()

		var raw [][]float64
		if code := btcGetJSON(ctx, u.String(), btcDefaultMaxBody, &raw); code != "" {
			slog.Warn("btc prices: coinbase candles failed", "code", code, "from", cur.String(), "to", chunkEnd.String())
			return all, false
		}
		for _, c := range raw {
			// Coinbase candle format: [time, low, high, open, close, volume]
			if len(c) < 6 || c[4] <= 0 {
				continue
			}
			day := time.Unix(int64(c[0]), 0).UTC().Format("2006-01-02")
			low, high, open, closeP, vol := c[1], c[2], c[3], c[4], c[5]
			all = append(all, btcPriceDay{
				Day:    day,
				Open:   &open,
				High:   &high,
				Low:    &low,
				Close:  closeP,
				Volume: &vol,
				Source: "coinbase",
			})
		}
		cur = chunkEnd.AddDays(1)
	}
	return all, true
}

func (s *btcPriceSyncer) fetchKrakenOHLC(ctx context.Context, from, to civil.Date) []btcPriceDay {
	u, err := url.Parse(s.krakenOHLCURL)
	if err != nil {
		return nil
	}
	q := u.Query()
	if q.Get("pair") == "" {
		q.Set("pair", "XBTUSD")
	}
	if q.Get("interval") == "" {
		q.Set("interval", "1440")
	}
	q.Set("since", strconv.FormatInt(from.AddDays(-1).In(time.UTC).Unix(), 10))
	u.RawQuery = q.Encode()

	var payload struct {
		Error  []string       `json:"error"`
		Result map[string]any `json:"result"`
	}
	if code := btcGetJSON(ctx, u.String(), btcDefaultMaxBody, &payload); code != "" {
		slog.Warn("btc prices: kraken OHLC failed", "code", code)
		return nil
	}
	fromStr, toStr := from.String(), to.String()
	var out []btcPriceDay
	for k, v := range payload.Result {
		if k == "last" {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		for _, elem := range arr {
			row, ok := elem.([]any)
			if !ok || len(row) < 7 {
				continue
			}
			tsFloat, ok := row[0].(float64)
			if !ok {
				continue
			}
			day := time.Unix(int64(tsFloat), 0).UTC().Format("2006-01-02")
			if day < fromStr || day > toStr {
				continue
			}
			open := parseAnyFloat(row[1])
			high := parseAnyFloat(row[2])
			low := parseAnyFloat(row[3])
			closeP := parseAnyFloat(row[4])
			vol := parseAnyFloat(row[6])
			if closeP == nil || *closeP <= 0 {
				continue
			}
			out = append(out, btcPriceDay{
				Day:    day,
				Open:   open,
				High:   high,
				Low:    low,
				Close:  *closeP,
				Volume: vol,
				Source: "kraken",
			})
		}
	}
	return out
}

func parseAnyFloat(v any) *float64 {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return nil
		}
		return &f
	case float64:
		return &x
	default:
		return nil
	}
}

func (s *btcPriceSyncer) closes(from, to string) map[string]float64 {
	if s == nil {
		return nil
	}
	s.ensureLoaded(context.Background())
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]float64)
	for d, r := range s.byDay {
		if (from == "" || d >= from) && (to == "" || d <= to) && r.Close > 0 {
			out[d] = r.Close
		}
	}
	return out
}

func (s *btcPriceSyncer) rows(from, to string) []btcPriceDay {
	if s == nil {
		return nil
	}
	s.ensureLoaded(context.Background())
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []btcPriceDay
	for d, r := range s.byDay {
		if (from == "" || d >= from) && (to == "" || d <= to) && r.Close > 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func (s *btcPriceSyncer) priceCoverage() BtcPriceCoverage {
	if s == nil {
		return BtcPriceCoverage{Sources: map[string]int{}}
	}
	upTo := civil.DateOf(s.now().UTC()).AddDays(-1)
	return s.priceCoverageUpTo(upTo)
}

func (s *btcPriceSyncer) priceCoverageUpTo(upTo civil.Date) BtcPriceCoverage {
	cov := BtcPriceCoverage{Sources: make(map[string]int)}
	if s == nil {
		return cov
	}
	s.ensureLoaded(context.Background())
	s.mu.RLock()
	defer s.mu.RUnlock()

	for d, r := range s.byDay {
		if r.Close <= 0 {
			continue
		}
		cov.TotalDays++
		cov.Sources[r.Source]++
		if cov.Earliest == "" || d < cov.Earliest {
			cov.Earliest = d
		}
		if cov.Latest == "" || d > cov.Latest {
			cov.Latest = d
		}
	}
	start, err := civil.ParseDate(btcPriceCoverageStart)
	if err != nil || upTo.Before(start) {
		return cov
	}
	var rangeFrom, rangeTo string
	for d := start; !d.After(upTo); d = d.AddDays(1) {
		ds := d.String()
		if r, ok := s.byDay[ds]; ok && r.Close > 0 {
			if rangeFrom != "" {
				cov.MissingRanges = append(cov.MissingRanges, BtcDateRange{From: rangeFrom, To: rangeTo})
				rangeFrom, rangeTo = "", ""
			}
			continue
		}
		cov.MissingDays++
		if rangeFrom == "" {
			rangeFrom = ds
		}
		rangeTo = ds
	}
	if rangeFrom != "" {
		cov.MissingRanges = append(cov.MissingRanges, BtcDateRange{From: rangeFrom, To: rangeTo})
	}
	cov.Complete = cov.TotalDays > 0 && cov.Earliest <= btcPriceCoverageStart && cov.MissingDays == 0
	return cov
}

func formatMissingRanges(ranges []BtcDateRange) string {
	if len(ranges) == 0 {
		return ""
	}
	r := ranges[0]
	if r.From == r.To {
		return r.From
	}
	return fmt.Sprintf("%s..%s", r.From, r.To)
}
