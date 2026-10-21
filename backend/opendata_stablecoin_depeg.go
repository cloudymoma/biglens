package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"sort"
	"time"
)

// Depeg history (stablecoin.md §4.1, §8.4, Appendix C): DefiLlama daily
// prices since 2020-12 (stable_price_daily) and Bitstamp daily candles for
// the intraday lows that daily prices hide (stable_peg_daily, source
// bitstamp_ohlc). Both refresh daily from the peg sampler. DefiLlama prices
// are in USD, so EURC's history also needs the ECB EUR/USD history, which
// the job loads once.

const (
	stableDepegEvery = 24 * time.Hour
	// stableOHLCPage is Bitstamp's maximum limit. With step=86400, start and
	// limit select the window start .. start + limit days (verified live:
	// usdtusd from 2021-06-01 with limit 5 answers nothing, with limit 20 the
	// 11 candles from its 2021-06-10 listing), so paging walks the windows.
	stableOHLCPage     = 1000
	stableOHLCMaxPages = 10
	// stableOHLCOverlap: the daily refresh re-reads the last 3 days, so the
	// in-progress day's candle is completed.
	stableOHLCOverlap = 3 * 24 * time.Hour
	// stableECBHistoryObs ECB business days reach back to about 2020-12.
	stableECBHistoryObs = 1500
	// stableECBHistoryMin: below this many stored rates the history is loaded.
	stableECBHistoryMin = 1000

	stablePegSourceBitstamp = "bitstamp_ohlc"
	stablePriceSourceLlama  = "defillama"
)

// stableOHLCFrom is where the candle bootstrap starts; before a pair was
// listed, empty pages advance the start.
var stableOHLCFrom = time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)

// stableGeckoIDs maps the core assets to DefiLlama gecko ids.
var stableGeckoIDs = map[string]string{"usdt": "tether", "usdc": "usd-coin", "eurc": "euro-coin"}

// stableOHLCPairs are the Bitstamp daily candles per core asset, priced in
// the asset's numéraire. EURC has none: Bitstamp's eurceur book is too thin
// for a history (§7.2), so EURC's history is DefiLlama ÷ the ECB rate.
var stableOHLCPairs = []struct{ Asset, Pair, Label string }{
	{"usdt", "usdtusd", "USDT/USD"}, {"usdc", "usdcusd", "USDC/USD"},
}

type stablePriceDaily struct {
	Day       string
	GeckoID   string
	PriceUSD  float64
	Source    string
	FetchedAt time.Time
}

// parseStablecoinPrices reads /stablecoinprices ([{date, prices{gecko: usd}}])
// for the wanted gecko ids. The live response starts with an undated row
// ("date": 0), which is skipped, and other coins' values may be strings, so
// only wanted values are decoded; a null or malformed wanted value is
// dropped. A response without a single usable value is an error.
func parseStablecoinPrices(body []byte, want map[string]bool) ([]stablePriceDaily, error) {
	var raw []struct {
		Date   stableNum                  `json:"date"`
		Prices map[string]json.RawMessage `json:"prices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out []stablePriceDaily
	for _, r := range raw {
		if !r.Date.Set || r.Date.V <= 0 {
			continue // the undated row
		}
		day := time.Unix(int64(r.Date.V), 0).UTC().Format(time.DateOnly)
		for gecko, v := range r.Prices {
			if !want[gecko] {
				continue
			}
			var p stableNum
			if json.Unmarshal(v, &p) != nil || !p.Set || !(p.V > 0) {
				continue
			}
			out = append(out, stablePriceDaily{Day: day, GeckoID: gecko, PriceUSD: p.V, Source: stablePriceSourceLlama})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no usable prices")
	}
	return out, nil
}

func fetchStablecoinPrices(ctx context.Context, ep stableEndpoints, now time.Time) ([]stablePriceDaily, string) {
	body, code := stableFetch(ctx, ep.LlamaBase+"/stablecoinprices", stableTimeoutLarge, stableMaxBodyLarge)
	if code != "" {
		return nil, code
	}
	want := map[string]bool{}
	for _, g := range stableGeckoIDs {
		want[g] = true
	}
	rows, err := parseStablecoinPrices(body, want)
	if err != nil {
		return nil, "bad_response"
	}
	for i := range rows {
		rows[i].FetchedAt = now
	}
	return rows, ""
}

type stableOHLC struct {
	Day                    string
	TS                     int64
	Open, High, Low, Close float64
}

// parseBitstampOHLC reads /api/v2/ohlc/{pair}/: data.pair must match.
func parseBitstampOHLC(body []byte, wantPair string) ([]stableOHLC, error) {
	var raw struct {
		Data *struct {
			Pair string `json:"pair"`
			OHLC []struct {
				Timestamp stableNum `json:"timestamp"`
				Open      stableNum `json:"open"`
				High      stableNum `json:"high"`
				Low       stableNum `json:"low"`
				Close     stableNum `json:"close"`
			} `json:"ohlc"`
		} `json:"data"`
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return nil, err
	}
	if raw.Data == nil || raw.Data.Pair != wantPair {
		return nil, errors.New("unexpected pair")
	}
	out := make([]stableOHLC, 0, len(raw.Data.OHLC))
	for _, c := range raw.Data.OHLC {
		if !c.Timestamp.Set || !c.Open.Set || !c.High.Set || !c.Low.Set || !c.Close.Set || !(c.Low.V > 0) || c.High.V < c.Low.V {
			return nil, errors.New("malformed candle")
		}
		ts := int64(c.Timestamp.V)
		out = append(out, stableOHLC{Day: time.Unix(ts, 0).UTC().Format(time.DateOnly), TS: ts,
			Open: c.Open.V, High: c.High.V, Low: c.Low.V, Close: c.Close.V})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func (f *stableVenueFetcher) fetchBitstampOHLC(ctx context.Context, pair, label string, start time.Time, limit int) ([]stableOHLC, string) {
	u := fmt.Sprintf("%s/api/v2/ohlc/%s/?step=86400&limit=%d&start=%d", f.ep.BitstampBase, url.PathEscape(pair), limit, start.Unix())
	body, code := f.get(ctx, "bitstamp", u)
	if code != "" {
		return nil, code
	}
	rows, err := parseBitstampOHLC(body, label)
	if err != nil {
		return nil, "bad_response"
	}
	return rows, ""
}

// --- storage ---

func (s *riskStore) upsertStablePriceDaily(ctx context.Context, rows []stablePriceDaily) error {
	if len(rows) == 0 {
		return nil
	}
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stable_price_daily (day, gecko_id, price_usd, source, fetched_at) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.GeckoID, r.PriceUSD, r.Source, stableTS(r.FetchedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stablePriceDailySeries returns one gecko id's daily prices from fromDay on.
func (s *riskStore) stablePriceDailySeries(ctx context.Context, gecko, fromDay string) ([]stablePriceDaily, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT day, gecko_id, price_usd, source, fetched_at FROM stable_price_daily
WHERE gecko_id = ? AND day >= ? ORDER BY day`, gecko, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stablePriceDaily{}
	for rs.Next() {
		var r stablePriceDaily
		var fetched string
		if err := rs.Scan(&r.Day, &r.GeckoID, &r.PriceUSD, &r.Source, &fetched); err != nil {
			return nil, err
		}
		r.FetchedAt = parseStableTS(fetched)
		out = append(out, r)
	}
	return out, rs.Err()
}

// stableLatestPegDay is the newest stored day of an asset/source ("" if none).
func (s *riskStore) stableLatestPegDay(ctx context.Context, asset, source string) (string, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return "", err
	}
	var day sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MAX(day) FROM stable_peg_daily WHERE asset = ? AND source = ?`, asset, source).Scan(&day)
	return day.String, err
}

// --- the daily job ---

func (p *stablePegSampler) depegNextDue() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.depegDue
}

// syncDepegHistory refreshes DefiLlama prices, the ECB EUR/USD history (once)
// and the Bitstamp candles when due (daily; 30 minutes after a failure).
func (p *stablePegSampler) syncDepegHistory(ctx context.Context) (ran, ok bool) {
	now := p.now().UTC()
	p.mu.Lock()
	due := p.store != nil && !now.Before(p.depegDue)
	p.mu.Unlock()
	if !due {
		return false, false
	}
	ok = true
	enabled := func(k string) bool { return p.settings.Providers[k] }
	if enabled("defillama") {
		t0 := time.Now()
		rows, code := fetchStablecoinPrices(ctx, p.ep, now)
		p.observe("defillama", t0, code)
		if code == "" {
			code = errCode(p.store.upsertStablePriceDaily(ctx, rows))
		}
		if code != "" {
			ok = false
			slog.Warn("stablecoin depeg history: prices", "error", code)
		}
	}
	p.mu.Lock()
	ecbLoaded := p.ecbLoaded
	p.mu.Unlock()
	if enabled("ecb") && !ecbLoaded {
		if have, err := p.store.stableRatesSeries(ctx, stableSeriesECBEURUSD, "0000-01-01"); err == nil && len(have) < stableECBHistoryMin {
			t0 := time.Now()
			obs, code := fetchECBEURUSD(ctx, p.ep, stableECBHistoryObs)
			p.observe("ecb", t0, code)
			if code == "" {
				rows := make([]stableRateDaily, 0, len(obs))
				for _, o := range obs {
					rows = append(rows, stableRateDaily{Day: o.Day, Series: stableSeriesECBEURUSD, Value: o.Value, FetchedAt: now})
				}
				code = errCode(p.store.upsertStableRatesDaily(ctx, rows))
			}
			if code != "" {
				ok = false
				slog.Warn("stablecoin depeg history: ecb", "error", code)
			} else {
				// Loaded once per process; the daily rates job keeps it current.
				p.mu.Lock()
				p.ecbLoaded = true
				p.mu.Unlock()
			}
		}
	}
	if enabled("bitstamp") {
		for _, pr := range stableOHLCPairs {
			if code := p.syncOHLC(ctx, pr.Asset, pr.Pair, pr.Label, now); code != "" {
				ok = false
				slog.Warn("stablecoin depeg history: candles", "pair", pr.Pair, "error", code)
			}
		}
	}
	p.mu.Lock()
	if ok {
		p.depegDue = now.Add(stableDepegEvery)
	} else {
		p.depegDue = now.Add(stableRetryAfterFail)
	}
	p.mu.Unlock()
	return true, ok
}

func errCode(err error) string {
	if err != nil {
		return "store_error"
	}
	return ""
}

// syncOHLC walks one pair's daily candles window by window, from the newest
// stored day (minus the overlap) or, on the first run, from stableOHLCFrom,
// until the window starts after now.
func (p *stablePegSampler) syncOHLC(ctx context.Context, asset, pair, label string, now time.Time) string {
	start := stableOHLCFrom
	if !p.ohlcFrom.IsZero() {
		start = p.ohlcFrom
	}
	if last, err := p.store.stableLatestPegDay(ctx, asset, stablePegSourceBitstamp); err == nil && last != "" {
		if d, err := time.Parse(time.DateOnly, last); err == nil {
			start = d.Add(-stableOHLCOverlap)
		}
	}
	limit := p.ohlcLimit
	if limit <= 0 {
		limit = stableOHLCPage
	}
	window := time.Duration(limit) * 24 * time.Hour
	for page := 0; page < stableOHLCMaxPages && start.Before(now); page, start = page+1, start.Add(window) {
		rows, code := p.venues.fetchBitstampOHLC(ctx, pair, label, start, limit)
		if code != "" {
			return code
		}
		if len(rows) == 0 {
			continue // a window before the listing, or a gap
		}
		daily := make([]stablePegDaily, 0, len(rows))
		for _, c := range rows {
			o, h, l, cl := c.Open, c.High, c.Low, c.Close
			daily = append(daily, stablePegDaily{Day: c.Day, Asset: asset, Source: stablePegSourceBitstamp, Open: &o, High: &h, Low: &l, Close: &cl,
				MinDevBps: stableF((l - 1) * 1e4), MaxDevBps: stableF((h - 1) * 1e4), Samples: 1, UpdatedAt: now})
		}
		if err := p.store.upsertStablePegDaily(ctx, daily); err != nil {
			return "store_error"
		}
	}
	return ""
}

// stableDevFromUSD converts a USD price of a EUR coin to deviation bps with
// the ECB rate of the same day (§7.2); without one (weekends, holidays) the
// day has no value. DefiLlama's daily price and the ECB fix are taken at
// different times of day, which is why EURC's day threshold is 100 bps.
func stableDevFromUSD(priceUSD float64, day string, fx map[string]float64) *float64 {
	if r, ok := fx[day]; ok && r > 0 {
		return stableF((priceUSD/r - 1) * 1e4)
	}
	return nil
}

// Depeg-day classification (stablecoin.md §7.2, "Depeg history").
const (
	// stableDepegEURLlamaBps is EURC's day threshold on DefiLlama ÷ the ECB
	// rate of the day (their timing mismatch); the consensus close and USD
	// coins use stableDepegThresholdBps.
	stableDepegEURLlamaBps = 100.0
	// stableLowMarkerBps: a Bitstamp low is a marker only this far off, and
	// only on a day whose candle close, consensus close or DefiLlama price is
	// at least stableDepegThresholdBps off.
	stableLowMarkerBps = 100.0
	// stableLastDevBps and stableIntradayMinutes define "the last ≥ 100 bps
	// deviation": a close or a marker that far off, or a consensus day with
	// at least 15 minutes (S1's window) of samples that far off.
	stableLastDevBps      = 100.0
	stableIntradayMinutes = 15
)

// StableDepegDay is a day off peg by its daily close: the BigLens consensus
// rollup close, else DefiLlama's daily price (EURC: ÷ the same-day ECB rate).
type StableDepegDay struct {
	Day          string  `json:"day"`
	Source       string  `json:"source"` // consensus | defillama
	Close        float64 `json:"close"`  // in the asset's numéraire
	CloseDevBps  float64 `json:"close_dev_bps"`
	ThresholdBps float64 `json:"threshold_bps"` // 30; EURC on DefiLlama ÷ ECB: 100
	// Consensus days only.
	MinDevBps    *float64 `json:"min_dev_bps,omitempty"`
	MaxDevBps    *float64 `json:"max_dev_bps,omitempty"`
	MinutesGE30  *int     `json:"minutes_ge_30,omitempty"`
	MinutesGE100 *int     `json:"minutes_ge_100,omitempty"`
}

// StableDepegMarker is a Bitstamp intraday low shown on the history chart.
type StableDepegMarker struct {
	Day       string   `json:"day"`
	Source    string   `json:"source"` // bitstamp_ohlc
	Low       float64  `json:"low"`
	LowDevBps float64  `json:"low_dev_bps"`
	Close     *float64 `json:"close"` // the candle's close
	// ConfirmedBy lists what was ≥ 30 bps off that day: bitstamp_close |
	// consensus | defillama.
	ConfirmedBy []string `json:"confirmed_by"`
}

// StableDepegClose is one day of the depeg-history chart: the day's close by
// the same rule (consensus rollup, else DefiLlama; EURC ÷ the same-day ECB
// rate) and the threshold that applies to it. Values are rounded for the
// chart payload (close to 1e-6, deviation to 0.01 bps).
type StableDepegClose struct {
	Day          string  `json:"day"`
	Source       string  `json:"source"` // consensus | defillama
	Close        float64 `json:"close"`  // in the asset's numéraire
	DevBps       float64 `json:"dev_bps"`
	ThresholdBps float64 `json:"threshold_bps"`
}

// StableLastDeviation is the newest day at ≥ 100 bps off peg in the stored
// history.
type StableLastDeviation struct {
	Day       string  `json:"day"`
	Source    string  `json:"source"` // consensus | consensus_intraday | defillama | bitstamp_ohlc
	DevBps    float64 `json:"dev_bps"`
	DaysSince int     `json:"days_since"`
}

type stableDepegInputs struct {
	Numeraire string
	Rollup    []stablePegDaily   // BigLens consensus, daily
	Candles   []stablePegDaily   // Bitstamp daily candles (USD coins only)
	Prices    []stablePriceDaily // DefiLlama, USD
	ECB       map[string]float64 // EUR/USD by day (EUR coins)
	Today     string             // YYYY-MM-DD, for days_since
}

type stableDepegHistory struct {
	Days        []StableDepegDay   // newest first
	Closes      []StableDepegClose // every day with a close, oldest first
	Total       int
	Markers     []StableDepegMarker // newest first
	LastGE100   *StableLastDeviation
	HistoryFrom string // the first stored day with a close or a candle; "" without history
}

// stableClassifyDepeg applies the §7.2 rules to one asset's stored history.
func stableClassifyDepeg(in stableDepegInputs) stableDepegHistory {
	type dayIn struct {
		roll     *stablePegDaily
		candle   *stablePegDaily
		llama    *float64 // price in the numéraire
		llamaDev *float64
	}
	days := map[string]*dayIn{}
	at := func(d string) *dayIn {
		if days[d] == nil {
			days[d] = &dayIn{}
		}
		return days[d]
	}
	for i := range in.Rollup {
		if r := &in.Rollup[i]; r.Close != nil {
			at(r.Day).roll = r
		}
	}
	for i := range in.Candles {
		if c := &in.Candles[i]; c.Low != nil {
			at(c.Day).candle = c
		}
	}
	for _, p := range in.Prices {
		dev, price := stableF((p.PriceUSD-1)*1e4), p.PriceUSD
		if in.Numeraire == "EUR" {
			if dev = stableDevFromUSD(p.PriceUSD, p.Day, in.ECB); dev == nil {
				continue
			}
			price = p.PriceUSD / in.ECB[p.Day]
		}
		d := at(p.Day)
		d.llama, d.llamaDev = &price, dev
	}
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	out := stableDepegHistory{Days: []StableDepegDay{}, Closes: []StableDepegClose{}, Markers: []StableDepegMarker{}}
	if len(keys) > 0 {
		out.HistoryFrom = keys[len(keys)-1]
	}
	last := func(day, source string, dev float64) {
		if out.LastGE100 == nil {
			out.LastGE100 = &StableLastDeviation{Day: day, Source: source, DevBps: dev, DaysSince: stableDaysBetween(day, in.Today)}
		}
	}
	for _, k := range keys {
		d := days[k]
		var closeDev *float64
		switch {
		case d.roll != nil:
			r := d.roll
			dev := (*r.Close - 1) * 1e4
			closeDev = &dev
			out.Closes = append(out.Closes, stableRoundedClose(k, stablePegSourceConsensus, *r.Close, dev, stableDepegThresholdBps))
			if stableBpsAtLeast(dev, stableDepegThresholdBps) {
				out.Days = append(out.Days, StableDepegDay{Day: k, Source: stablePegSourceConsensus, Close: *r.Close, CloseDevBps: dev,
					ThresholdBps: stableDepegThresholdBps, MinDevBps: r.MinDevBps, MaxDevBps: r.MaxDevBps, MinutesGE30: r.MinutesGE30, MinutesGE100: r.MinutesGE100})
			}
		case d.llamaDev != nil:
			closeDev = d.llamaDev
			thr := stableDepegThresholdBps
			if in.Numeraire == "EUR" {
				thr = stableDepegEURLlamaBps
			}
			out.Closes = append(out.Closes, stableRoundedClose(k, stablePriceSourceLlama, *d.llama, *d.llamaDev, thr))
			if stableBpsAtLeast(*d.llamaDev, thr) {
				out.Days = append(out.Days, StableDepegDay{Day: k, Source: stablePriceSourceLlama, Close: *d.llama, CloseDevBps: *d.llamaDev, ThresholdBps: thr})
			}
		}
		var marker *StableDepegMarker
		if c := d.candle; c != nil {
			lowDev := (*c.Low - 1) * 1e4
			var by []string
			if c.Close != nil && stableBpsAtLeast((*c.Close-1)*1e4, stableDepegThresholdBps) {
				by = append(by, "bitstamp_close")
			}
			if d.roll != nil && stableBpsAtLeast((*d.roll.Close-1)*1e4, stableDepegThresholdBps) {
				by = append(by, stablePegSourceConsensus)
			}
			if d.llamaDev != nil && stableBpsAtLeast(*d.llamaDev, stableDepegThresholdBps) {
				by = append(by, stablePriceSourceLlama)
			}
			if stableBpsAtLeast(lowDev, stableLowMarkerBps) && len(by) > 0 {
				marker = &StableDepegMarker{Day: k, Source: stablePegSourceBitstamp, Low: *c.Low, LowDevBps: lowDev, Close: c.Close, ConfirmedBy: by}
				out.Markers = append(out.Markers, *marker)
			}
		}
		// The newest ≥ 100 bps deviation: the close, then the consensus
		// samples, then a shown marker.
		switch {
		case closeDev != nil && stableBpsAtLeast(*closeDev, stableLastDevBps):
			src := stablePriceSourceLlama
			if d.roll != nil {
				src = stablePegSourceConsensus
			}
			last(k, src, *closeDev)
		case d.roll != nil && d.roll.MinutesGE100 != nil && *d.roll.MinutesGE100 >= stableIntradayMinutes:
			last(k, "consensus_intraday", stableWorstDev(d.roll.MinDevBps, d.roll.MaxDevBps))
		case marker != nil:
			last(k, stablePegSourceBitstamp, marker.LowDevBps)
		}
	}
	out.Total = len(out.Days)
	slices.Reverse(out.Closes)
	return out
}

func stableRoundedClose(day, source string, close, dev, threshold float64) StableDepegClose {
	return StableDepegClose{Day: day, Source: source, Close: math.Round(close*1e6) / 1e6, DevBps: math.Round(dev*100) / 100,
		ThresholdBps: threshold}
}

// stableWorstDev is the deviation with the larger magnitude.
func stableWorstDev(lo, hi *float64) float64 {
	switch {
	case lo == nil && hi == nil:
		return 0
	case lo == nil:
		return *hi
	case hi == nil || math.Abs(*lo) >= math.Abs(*hi):
		return *lo
	}
	return *hi
}

// stableDaysBetween counts calendar days from a to b (YYYY-MM-DD).
func stableDaysBetween(a, b string) int {
	ta, errA := time.Parse(time.DateOnly, a)
	tb, errB := time.Parse(time.DateOnly, b)
	if errA != nil || errB != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}
