package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
	// stableOHLCPage is Bitstamp's maximum candles per request; a full page
	// means there may be more.
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
// the asset's numéraire.
var stableOHLCPairs = []struct{ Asset, Pair, Label string }{
	{"usdt", "usdtusd", "USDT/USD"}, {"usdc", "usdcusd", "USDC/USD"}, {"eurc", "eurceur", "EURC/EUR"},
}

type stablePriceDaily struct {
	Day       string
	GeckoID   string
	PriceUSD  float64
	Source    string
	FetchedAt time.Time
}

// parseStablecoinPrices reads /stablecoinprices ([{date, prices{gecko: usd}}])
// for the wanted gecko ids. A null price is absent; a malformed row fails
// the whole response.
func parseStablecoinPrices(body []byte, want map[string]bool) ([]stablePriceDaily, error) {
	var raw []struct {
		Date   stableNum             `json:"date"`
		Prices map[string]*stableNum `json:"prices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("no rows")
	}
	var out []stablePriceDaily
	for _, r := range raw {
		if !r.Date.Set || r.Date.V <= 0 {
			return nil, errors.New("bad date")
		}
		day := time.Unix(int64(r.Date.V), 0).UTC().Format(time.DateOnly)
		for gecko, p := range r.Prices {
			if !want[gecko] || p == nil || !p.Set {
				continue
			}
			if !(p.V > 0) {
				return nil, fmt.Errorf("%s %s: non-positive price", day, gecko)
			}
			out = append(out, stablePriceDaily{Day: day, GeckoID: gecko, PriceUSD: p.V, Source: stablePriceSourceLlama})
		}
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

// syncOHLC pages through one pair's daily candles from the newest stored day
// (minus the overlap) or, on the first run, from stableOHLCFrom.
func (p *stablePegSampler) syncOHLC(ctx context.Context, asset, pair, label string, now time.Time) string {
	start, bootstrap := stableOHLCFrom, true
	if last, err := p.store.stableLatestPegDay(ctx, asset, stablePegSourceBitstamp); err == nil && last != "" {
		if d, err := time.Parse(time.DateOnly, last); err == nil {
			start, bootstrap = d.Add(-stableOHLCOverlap), false
		}
	}
	limit := p.ohlcLimit
	if limit <= 0 {
		limit = stableOHLCPage
	}
	gotAny := false
	for page := 0; page < stableOHLCMaxPages && start.Before(now); page++ {
		rows, code := p.venues.fetchBitstampOHLC(ctx, pair, label, start, limit)
		if code != "" {
			return code
		}
		if len(rows) == 0 {
			if !bootstrap || gotAny {
				return "" // no newer candles
			}
			// Bootstrap before the listing date: move on by one page span.
			start = start.Add(time.Duration(limit) * 24 * time.Hour)
			continue
		}
		gotAny = true
		daily := make([]stablePegDaily, 0, len(rows))
		for _, c := range rows {
			o, h, l, cl := c.Open, c.High, c.Low, c.Close
			daily = append(daily, stablePegDaily{Day: c.Day, Asset: asset, Source: stablePegSourceBitstamp, Open: &o, High: &h, Low: &l, Close: &cl,
				MinDevBps: stableF((l - 1) * 1e4), MaxDevBps: stableF((h - 1) * 1e4), Samples: 1, UpdatedAt: now})
		}
		if err := p.store.upsertStablePegDaily(ctx, daily); err != nil {
			return "store_error"
		}
		if len(rows) < limit {
			return ""
		}
		start = time.Unix(rows[len(rows)-1].TS, 0).UTC().Add(24 * time.Hour)
	}
	return ""
}

// stableDevFromUSD converts a USD price of a EUR coin to deviation bps with
// the ECB rate of that day or the nearest earlier one within 5 days.
func stableDevFromUSD(priceUSD float64, day string, fx map[string]float64) *float64 {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil
	}
	for i := 0; i <= 5; i++ {
		if r, ok := fx[d.AddDate(0, 0, -i).Format(time.DateOnly)]; ok && r > 0 {
			return stableF((priceUSD/r - 1) * 1e4)
		}
	}
	return nil
}
