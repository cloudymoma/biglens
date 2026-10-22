package main

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// Peg storage (stablecoin.md §8.5): venue quotes and peg samples per 5-minute
// bucket, the daily peg rollup, and the Uniswap price per 10-minute bucket.

const (
	stablePegTick = 5 * time.Minute
	stableDexTick = 10 * time.Minute
	// stableRelativeAsset is the stable_peg_5m/stable_peg_daily key of the
	// relative peg (USDT per USDC).
	stableRelativeAsset      = "usdc_usdt"
	stablePegSourceConsensus = "consensus"
)

// stablePeg5mRow is one 5-minute peg sample of an asset or the relative peg.
type stablePeg5mRow struct {
	Bucket           time.Time
	Asset            string // usdt | usdc | eurc | usdc_usdt
	Numeraire        string // USD | EUR | USDT
	State            string // ok | single_venue | unknown
	Consensus        *float64
	DevBps           *float64
	DispBps          *float64
	SingleVenuePrice *float64
	InputsUsed       int
	VenuesUsed       int
}

// stablePegDaily is one day of an asset's peg from one source: consensus
// (5-minute samples), bitstamp_ohlc / kraken_ohlc (daily candles) or defillama
// (daily price). Minutes off peg are known only for the consensus source; VWAP
// is populated for Kraken daily candles.
type stablePegDaily struct {
	Day                       string
	Asset                     string
	Source                    string
	Open, High, Low, Close    *float64
	VWAP                      *float64
	Volume                    *float64
	MinDevBps, MaxDevBps      *float64
	MinutesGE30, MinutesGE100 *int
	Samples                   int
	UpdatedAt                 time.Time
}

// stableBpsAtLeast reports |dev| ≥ threshold, inclusive at the boundary: a
// price of exactly 1.003 is 30.0 bps off peg although 1.003 − 1 is
// 29.99999999999989 bps in float64.
func stableBpsAtLeast(devBps, threshold float64) bool { return math.Abs(devBps) >= threshold-1e-9 }

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func ptrFromNull(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// insertStableQuotes5m stores the evaluated markets of one sample; a rerun in
// the same bucket replaces them. last maps venue|pair to the last trade price.
func (s *riskStore) insertStableQuotes5m(ctx context.Context, at time.Time, quotes []StableQuote, last map[string]*float64) error {
	if len(quotes) == 0 {
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
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stable_quotes_5m
(bucket, venue, pair, bid, ask, mid, last, vol_usd, spread_bps, age_seconds, included, exclude_reason) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	bucket := stableTS(at.UTC().Truncate(stablePegTick))
	for _, q := range quotes {
		var age any
		if q.AgeSeconds != nil {
			age = *q.AgeSeconds
		}
		included := 0
		if q.Included {
			included = 1
		}
		var reason any
		if q.ExcludeReason != "" {
			reason = q.ExcludeReason
		}
		if _, err := stmt.ExecContext(ctx, bucket, q.Venue, q.Pair, nullFloat(q.Bid), nullFloat(q.Ask), nullFloat(q.Mid),
			nullFloat(last[q.Venue+"|"+q.Pair]), nullFloat(q.VolUSD), nullFloat(q.SpreadBps), age, included, reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertStablePeg5m stores peg samples; a rerun in the same bucket replaces them.
func (s *riskStore) insertStablePeg5m(ctx context.Context, rows []stablePeg5mRow) error {
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
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stable_peg_5m
(bucket, asset, numeraire, state, consensus, dev_bps, disp_bps, single_venue_price, inputs_used, venues_used) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, stableTS(r.Bucket.UTC().Truncate(stablePegTick)), r.Asset, r.Numeraire, r.State, nullFloat(r.Consensus),
			nullFloat(r.DevBps), nullFloat(r.DispBps), nullFloat(r.SingleVenuePrice), r.InputsUsed, r.VenuesUsed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stablePeg5mSeries returns an asset's 5-minute samples from from on, oldest first.
func (s *riskStore) stablePeg5mSeries(ctx context.Context, asset string, from time.Time) ([]stablePeg5mRow, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT bucket, asset, numeraire, state, consensus, dev_bps, disp_bps, single_venue_price, inputs_used, venues_used
FROM stable_peg_5m WHERE asset = ? AND bucket >= ? ORDER BY bucket`, asset, stableTS(from))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stablePeg5mRow{}
	for rs.Next() {
		var r stablePeg5mRow
		var bucket string
		var c, d, disp, single sql.NullFloat64
		if err := rs.Scan(&bucket, &r.Asset, &r.Numeraire, &r.State, &c, &d, &disp, &single, &r.InputsUsed, &r.VenuesUsed); err != nil {
			return nil, err
		}
		r.Bucket = parseStableTS(bucket)
		r.Consensus, r.DevBps, r.DispBps, r.SingleVenuePrice = ptrFromNull(c), ptrFromNull(d), ptrFromNull(disp), ptrFromNull(single)
		out = append(out, r)
	}
	return out, rs.Err()
}

// rollupStablePegDaily recomputes an asset's consensus row for day from its
// 5-minute samples: open/high/low/close of the consensus, the deviation range,
// and 5 minutes per sample at |dev| ≥ 30 and ≥ 100 bps.
func (s *riskStore) rollupStablePegDaily(ctx context.Context, asset, day string, now time.Time) error {
	start, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return err
	}
	rows, err := s.stablePeg5mSeries(ctx, asset, start)
	if err != nil {
		return err
	}
	d := stablePegDaily{Day: day, Asset: asset, Source: stablePegSourceConsensus, UpdatedAt: now}
	ge30, ge100 := 0, 0
	for _, r := range rows {
		if !r.Bucket.Before(start.Add(24*time.Hour)) || r.Consensus == nil || r.DevBps == nil {
			continue
		}
		c, dev := *r.Consensus, *r.DevBps
		if d.Samples == 0 {
			d.Open, d.High, d.Low, d.MinDevBps, d.MaxDevBps = stableF(c), stableF(c), stableF(c), stableF(dev), stableF(dev)
		}
		d.Close = stableF(c)
		*d.High, *d.Low = math.Max(*d.High, c), math.Min(*d.Low, c)
		*d.MinDevBps, *d.MaxDevBps = math.Min(*d.MinDevBps, dev), math.Max(*d.MaxDevBps, dev)
		if stableBpsAtLeast(dev, 30) {
			ge30 += int(stablePegTick / time.Minute)
		}
		if stableBpsAtLeast(dev, 100) {
			ge100 += int(stablePegTick / time.Minute)
		}
		d.Samples++
	}
	if d.Samples == 0 {
		return nil
	}
	d.MinutesGE30, d.MinutesGE100 = &ge30, &ge100
	return s.upsertStablePegDaily(ctx, []stablePegDaily{d})
}

func (s *riskStore) upsertStablePegDaily(ctx context.Context, rows []stablePegDaily) error {
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
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stable_peg_daily
(day, asset, source, open, high, low, close, vwap, volume, min_dev_bps, max_dev_bps, minutes_ge_30, minutes_ge_100, samples, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.Asset, r.Source, nullFloat(r.Open), nullFloat(r.High), nullFloat(r.Low), nullFloat(r.Close),
			nullFloat(r.VWAP), nullFloat(r.Volume), nullFloat(r.MinDevBps), nullFloat(r.MaxDevBps), nullInt(r.MinutesGE30), nullInt(r.MinutesGE100), r.Samples, stableTS(r.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stablePegDailySeries returns an asset's daily peg rows of one source from
// fromDay on, oldest first.
func (s *riskStore) stablePegDailySeries(ctx context.Context, asset, source, fromDay string) ([]stablePegDaily, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, asset, source, open, high, low, close, vwap, volume, min_dev_bps, max_dev_bps, minutes_ge_30, minutes_ge_100, samples, updated_at
FROM stable_peg_daily WHERE asset = ? AND source = ? AND day >= ? ORDER BY day`, asset, source, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stablePegDaily{}
	for rs.Next() {
		var r stablePegDaily
		var o, h, l, c, vw, vol, mn, mx sql.NullFloat64
		var g30, g100 sql.NullInt64
		var updated string
		if err := rs.Scan(&r.Day, &r.Asset, &r.Source, &o, &h, &l, &c, &vw, &vol, &mn, &mx, &g30, &g100, &r.Samples, &updated); err != nil {
			return nil, err
		}
		r.Open, r.High, r.Low, r.Close, r.VWAP, r.Volume, r.MinDevBps, r.MaxDevBps = ptrFromNull(o), ptrFromNull(h), ptrFromNull(l), ptrFromNull(c), ptrFromNull(vw), ptrFromNull(vol), ptrFromNull(mn), ptrFromNull(mx)
		if g30.Valid {
			v := int(g30.Int64)
			r.MinutesGE30 = &v
		}
		if g100.Valid {
			v := int(g100.Int64)
			r.MinutesGE100 = &v
		}
		r.UpdatedAt = parseStableTS(updated)
		out = append(out, r)
	}
	return out, rs.Err()
}

// insertStableDex10m stores the first Uniswap reading of each 10-minute bucket.
func (s *riskStore) insertStableDex10m(ctx context.Context, at time.Time, r stableUniswapReading) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO stable_dex_10m (bucket, pool, price, tick, sqrt_price_x96) VALUES (?,?,?,?,?)`,
		stableTS(at.UTC().Truncate(stableDexTick)), stableUniswapPool, r.Price, r.Tick, r.SqrtPriceX96)
	return err
}
