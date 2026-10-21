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
	stablePegSourceKraken   = "kraken_ohlc"
	stablePriceSourceLlama  = "defillama"
	stableKrakenOHLCPair    = "EURCEUR"
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
	VWAP                   float64
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

// parseKrakenOHLC reads /0/public/OHLC?pair=EURCEUR&interval=1440:
// result[wantPair] is a list of [timestamp, open, high, low, close, vwap,
// volume, count] arrays.
func parseKrakenOHLC(body []byte, wantPair string) ([]stableOHLC, error) {
	var raw struct {
		Error  []string                   `json:"error"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Error) > 0 {
		return nil, errors.New("kraken error list")
	}
	arrRaw, ok := raw.Result[wantPair]
	if !ok {
		return nil, errors.New("unexpected pair")
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(arrRaw, &rows); err != nil {
		return nil, err
	}
	out := make([]stableOHLC, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			return nil, errors.New("short candle")
		}
		var ts, o, h, l, cl, vw stableNum
		if json.Unmarshal(r[0], &ts) != nil || json.Unmarshal(r[1], &o) != nil ||
			json.Unmarshal(r[2], &h) != nil || json.Unmarshal(r[3], &l) != nil ||
			json.Unmarshal(r[4], &cl) != nil || json.Unmarshal(r[5], &vw) != nil ||
			!ts.Set || !(ts.V > 0) || !o.Set || !(o.V > 0) || !h.Set || !l.Set || !cl.Set || !vw.Set ||
			!(l.V > 0) || !(cl.V > 0) || !(vw.V > 0) || h.V < l.V {
			return nil, errors.New("malformed candle")
		}
		sec := int64(ts.V)
		out = append(out, stableOHLC{Day: time.Unix(sec, 0).UTC().Format(time.DateOnly), TS: sec,
			Open: o.V, High: h.V, Low: l.V, Close: cl.V, VWAP: vw.V})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func (f *stableVenueFetcher) fetchKrakenOHLC(ctx context.Context, pair string, since time.Time) ([]stableOHLC, string) {
	var sinceSec int64
	if !since.IsZero() && since.Unix() > 0 {
		sinceSec = since.Unix()
	}
	u := fmt.Sprintf("%s/0/public/OHLC?pair=%s&interval=1440&since=%d", f.ep.KrakenBase, url.QueryEscape(pair), sinceSec)
	body, code := f.get(ctx, "kraken", u)
	if code != "" {
		return nil, code
	}
	rows, err := parseKrakenOHLC(body, pair)
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

// stableLatestKrakenDayWithVWAP is the newest stored Kraken OHLC day that has
// a populated vwap column ("" if none, triggering a full backfill).
func (s *riskStore) stableLatestKrakenDayWithVWAP(ctx context.Context, asset string) (string, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return "", err
	}
	var day sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MAX(day) FROM stable_peg_daily WHERE asset = ? AND source = ? AND vwap IS NOT NULL`, asset, stablePegSourceKraken).Scan(&day)
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
	if enabled("kraken") && p.venues.ep.KrakenBase != "" {
		if code := p.syncKrakenOHLC(ctx, "eurc", stableKrakenOHLCPair, now); code != "" {
			ok = false
			slog.Warn("stablecoin depeg history: kraken candles", "pair", stableKrakenOHLCPair, "error", code)
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

// syncKrakenOHLC refreshes Kraken's daily EURCEUR candles (up to 720 days per
// call; EURCEUR starts on 2025-09-18). On incremental runs it re-reads from 3
// days before the newest stored day (with VWAP) so the in-progress candle settles.
func (p *stablePegSampler) syncKrakenOHLC(ctx context.Context, asset, pair string, now time.Time) string {
	var since time.Time
	if last, err := p.store.stableLatestKrakenDayWithVWAP(ctx, asset); err == nil && last != "" {
		if d, err := time.Parse(time.DateOnly, last); err == nil {
			since = d.Add(-stableOHLCOverlap)
		}
	}
	rows, code := p.venues.fetchKrakenOHLC(ctx, pair, since)
	if code != "" {
		return code
	}
	if len(rows) == 0 {
		return ""
	}
	daily := make([]stablePegDaily, 0, len(rows))
	for _, c := range rows {
		o, h, l, cl, vw := c.Open, c.High, c.Low, c.Close, c.VWAP
		daily = append(daily, stablePegDaily{Day: c.Day, Asset: asset, Source: stablePegSourceKraken, Open: &o, High: &h, Low: &l, Close: &cl, VWAP: &vw,
			MinDevBps: stableF((l - 1) * 1e4), MaxDevBps: stableF((h - 1) * 1e4), Samples: 1, UpdatedAt: now})
	}
	if err := p.store.upsertStablePegDaily(ctx, daily); err != nil {
		return "store_error"
	}
	return ""
}

// stableDevFromUSD converts a USD price of a EUR coin to deviation bps with
// the ECB rate of the same day (§7.2); without one (weekends, holidays) the
// day has no value. Used for EURC days before Kraken's EURCEUR daily OHLC
// history begins (2025-09-18).
func stableDevFromUSD(priceUSD float64, day string, fx map[string]float64) *float64 {
	if r, ok := fx[day]; ok && r > 0 {
		return stableF((priceUSD/r - 1) * 1e4)
	}
	return nil
}

// Depeg-day classification (stablecoin.md §7.2, "Depeg history").
const (
	// stableDepegEURLlamaECBBps is the resolution floor (180 bps) for EURC's
	// pre-Kraken DefiLlama ÷ same-day ECB segment (before 2025-09-18), caused
	// by the 00:00 UTC vs 14:15 CET fix-timing gap plus DefiLlama's $0.01
	// price rounding of euro-coin until 2025-10 (which alone adds up to
	// ±45 bps), not a tuned value.
	//
	// Measured over 2024-01-01 .. 2026-10-21:
	//   (a) DefiLlama USD ÷ same-day ECB rate: 714 days, stdev 41.03 bps,
	//       p99 |dev| 138.10 bps, max |dev| 171.87 bps (2025-08-01), 277 days
	//       ≥ 30 bps, 23 days ≥ 100 bps;
	//   (b) DefiLlama USD ÷ previous ECB business day: 714 days, stdev
	//       56.13 bps, p99 |dev| 162.61 bps, max |dev| 286.95 bps (2025-04-11),
	//       375 days ≥ 30 bps, 56 days ≥ 100 bps;
	//   (c) Kraken EURCEUR daily OHLC (399 days from 2025-09-18):
	//       - close alone: stdev 9.91 bps, p99 |dev| 32.30 bps, max |dev|
	//         101.00 bps (2026-02-03), 7 days ≥ 30 bps, 2 days ≥ 100 bps;
	//       - VWAP alone: stdev 11.39 bps, p99 |dev| 37.44 bps, max |dev|
	//         140.00 bps (2025-11-28), 7 days ≥ 30 bps, 2 days ≥ 100 bps;
	//       - same-sign min(|close_dev|, |vwap_dev|) (0 when signs differ):
	//         stdev 3.44 bps, p99 |dev| 18.06 bps, max |dev| 30.00 bps
	//         (2026-01-25), 1 day ≥ 30 bps, 0 days ≥ 100 bps.
	// From 2025-09-18 onward EURC uses Kraken's same-sign min(|close_dev|,
	// |vwap_dev|) at stableDepegThresholdBps (30 bps); before 2025-09-18 it
	// uses DefiLlama ÷ same-day ECB at stableDepegEURLlamaECBBps (180 bps).
	stableDepegEURLlamaECBBps = 180.0
	// stableLowMarkerBps: a Bitstamp low is a marker only this far off, and
	// only on a day whose candle close, consensus close or DefiLlama price is
	// at least stableDepegThresholdBps off.
	stableLowMarkerBps = 100.0
	// stableLastDevBps and stableIntradayMinutes define "the last ≥ 100 bps
	// deviation" for USD coins: a close or a marker that far off, or a
	// consensus day with at least 15 minutes (S1's window) of samples that
	// far off.
	stableLastDevBps      = 100.0
	stableIntradayMinutes = 15
)

// StableDepegDay is a day off peg by its daily rule: the BigLens consensus
// rollup close, else Kraken EURCEUR same-sign min(|close_dev|, |vwap_dev|)
// for EURC from 2025-09-18 onward, else DefiLlama's daily price (EURC before
// Kraken: ÷ the same-day ECB rate).
type StableDepegDay struct {
	Day          string   `json:"day"`
	Source       string   `json:"source"` // consensus | kraken_ohlc | defillama
	Close        float64  `json:"close"`  // in the asset's numéraire
	CloseDevBps  float64  `json:"close_dev_bps"`
	VWAP         *float64 `json:"vwap,omitempty"`
	VWAPDevBps   *float64 `json:"vwap_dev_bps,omitempty"`
	DevBps       float64  `json:"dev_bps"`       // classified day deviation
	ThresholdBps float64  `json:"threshold_bps"` // 30; EURC on DefiLlama ÷ ECB: 180
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

// StableDepegClose is one day of the depeg-history chart: the day's close and
// classified deviation by the same rule and the threshold that applies to it.
// Values are rounded for the chart payload (close to 1e-6, deviation to 0.01 bps).
type StableDepegClose struct {
	Day          string   `json:"day"`
	Source       string   `json:"source"` // consensus | kraken_ohlc | defillama
	Close        float64  `json:"close"`  // in the asset's numéraire
	VWAP         *float64 `json:"vwap,omitempty"`
	CloseDevBps  *float64 `json:"close_dev_bps,omitempty"`
	VWAPDevBps   *float64 `json:"vwap_dev_bps,omitempty"`
	DevBps       float64  `json:"dev_bps"`
	ThresholdBps float64  `json:"threshold_bps"`
}

// StableLastDeviation is the newest qualifying deviation in the stored
// history, carrying the threshold and source label of the day it cites.
type StableLastDeviation struct {
	Day          string  `json:"day"`
	Source       string  `json:"source"` // consensus | consensus_intraday | kraken_ohlc | defillama | bitstamp_ohlc
	SourceLabel  string  `json:"source_label,omitempty"`
	ThresholdBps float64 `json:"threshold_bps"`
	DevBps       float64 `json:"dev_bps"`
	DaysSince    int     `json:"days_since"`
}

// stableKrakenDayDevBps computes the Kraken day deviation: if the close and
// VWAP deviations have the same sign, the sign times the smaller of the two
// magnitudes; otherwise 0.
func stableKrakenDayDevBps(closePrice float64, vwapPrice *float64) (devBps, closeDevBps float64, vwapDevBps *float64) {
	closeDevBps = (closePrice - 1) * 1e4
	if vwapPrice == nil || !(*vwapPrice > 0) {
		return closeDevBps, closeDevBps, nil
	}
	vd := (*vwapPrice - 1) * 1e4
	vwapDevBps = &vd
	if (closeDevBps > 0 && vd > 0) || (closeDevBps < 0 && vd < 0) {
		mag := math.Min(math.Abs(closeDevBps), math.Abs(vd))
		if closeDevBps < 0 {
			return -mag, closeDevBps, vwapDevBps
		}
		return mag, closeDevBps, vwapDevBps
	}
	return 0, closeDevBps, vwapDevBps
}

type stableDepegInputs struct {
	Numeraire string
	Rollup    []stablePegDaily   // BigLens consensus, daily
	Candles   []stablePegDaily   // Bitstamp daily candles (USD coins only)
	Kraken    []stablePegDaily   // Kraken EURCEUR daily candles (EURC close where available)
	Prices    []stablePriceDaily // DefiLlama, USD
	ECB       map[string]float64 // EUR/USD by day (EUR coins before Kraken)
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
		kraken   *stablePegDaily
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
	for i := range in.Kraken {
		if c := &in.Kraken[i]; c.Close != nil {
			at(c.Day).kraken = c
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
	last := func(day, source, label string, thr, dev float64) {
		if out.LastGE100 == nil {
			out.LastGE100 = &StableLastDeviation{Day: day, Source: source, SourceLabel: label, ThresholdBps: thr, DevBps: dev, DaysSince: stableDaysBetween(day, in.Today)}
		}
	}
	for _, k := range keys {
		d := days[k]
		var closeDev *float64
		var closeSrc, closeLabel string
		closeThr := stableDepegThresholdBps
		switch {
		case d.roll != nil:
			r := d.roll
			dev := (*r.Close - 1) * 1e4
			closeDev, closeSrc, closeLabel = &dev, stablePegSourceConsensus, "consensus close"
			out.Closes = append(out.Closes, stableRoundedClose(k, stablePegSourceConsensus, *r.Close, dev, stableDepegThresholdBps))
			if stableBpsAtLeast(dev, stableDepegThresholdBps) {
				out.Days = append(out.Days, StableDepegDay{Day: k, Source: stablePegSourceConsensus, Close: *r.Close, CloseDevBps: dev, DevBps: dev,
					ThresholdBps: stableDepegThresholdBps, MinDevBps: r.MinDevBps, MaxDevBps: r.MaxDevBps, MinutesGE30: r.MinutesGE30, MinutesGE100: r.MinutesGE100})
			}
		case d.kraken != nil:
			c := d.kraken
			dev, cDev, vDev := stableKrakenDayDevBps(*c.Close, c.VWAP)
			closeDev, closeSrc, closeLabel, closeThr = &dev, stablePegSourceKraken, "Kraken close & VWAP", stableDepegThresholdBps
			rc := stableRoundedClose(k, stablePegSourceKraken, *c.Close, dev, closeThr)
			rcCloseDev := math.Round(cDev*100) / 100
			rc.CloseDevBps = &rcCloseDev
			if c.VWAP != nil && vDev != nil {
				rcVWAP := math.Round(*c.VWAP*1e6) / 1e6
				rcVWAPDev := math.Round(*vDev*100) / 100
				rc.VWAP, rc.VWAPDevBps = &rcVWAP, &rcVWAPDev
			}
			out.Closes = append(out.Closes, rc)
			if stableBpsAtLeast(dev, closeThr) {
				out.Days = append(out.Days, StableDepegDay{Day: k, Source: stablePegSourceKraken, Close: *c.Close, CloseDevBps: cDev,
					VWAP: c.VWAP, VWAPDevBps: vDev, DevBps: dev, ThresholdBps: closeThr})
			}
		case d.llamaDev != nil:
			closeLabel = "DefiLlama"
			if in.Numeraire == "EUR" {
				closeThr = stableDepegEURLlamaECBBps
				closeLabel = "DefiLlama ÷ ECB"
			}
			closeDev, closeSrc = d.llamaDev, stablePriceSourceLlama
			out.Closes = append(out.Closes, stableRoundedClose(k, stablePriceSourceLlama, *d.llama, *d.llamaDev, closeThr))
			if stableBpsAtLeast(*d.llamaDev, closeThr) {
				out.Days = append(out.Days, StableDepegDay{Day: k, Source: stablePriceSourceLlama, Close: *d.llama, CloseDevBps: *d.llamaDev, DevBps: *d.llamaDev, ThresholdBps: closeThr})
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
		// The newest qualifying deviation: for EURC each source uses its own
		// day threshold (30 bps on Kraken close & VWAP or consensus, 180 bps on
		// DefiLlama ÷ ECB); for USD coins it uses stableLastDevBps (100 bps).
		lastThr := math.Max(closeThr, stableLastDevBps)
		if in.Numeraire == "EUR" {
			lastThr = closeThr
		}
		switch {
		case closeDev != nil && stableBpsAtLeast(*closeDev, lastThr):
			last(k, closeSrc, closeLabel, lastThr, *closeDev)
		case d.roll != nil && d.roll.MinutesGE100 != nil && *d.roll.MinutesGE100 >= stableIntradayMinutes:
			last(k, "consensus_intraday", "consensus intraday", stableLastDevBps, stableWorstDev(d.roll.MinDevBps, d.roll.MaxDevBps))
		case marker != nil:
			last(k, stablePegSourceBitstamp, "Bitstamp low", stableLastDevBps, marker.LowDevBps)
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
