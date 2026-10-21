package main

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"sort"
	"time"
)

// Panel data for the Peg tab (stablecoin.md §7.2): each asset's consensus
// inputs per 5-minute sample (the per-venue deviation heatmap), the stored
// Uniswap readings (DEX panel) and the ECB reference rate (EURC mode). All of
// it is read from SQLite; nothing here calls an upstream.

// stableHeatmapMaxSpan: the inputs are kept as long as the venue quotes, so
// longer ranges have no heatmap.
const stableHeatmapMaxSpan = stableQuotesRetention

// stableInputRow is one consensus input of one sample.
type stableInputRow struct {
	Bucket        time.Time
	Asset         string // usdt | usdc | eurc | usdc_usdt
	Venue         string
	Pair          string
	Kind          string // direct | derived | cross | inverse | dex
	Mid           *float64
	Included      bool
	ExcludeReason string
}

// insertStableInputs5m stores the consensus inputs of every asset in one
// sample, plus the relative peg's inputs; a rerun in the same bucket replaces
// them.
func (s *riskStore) insertStableInputs5m(ctx context.Context, at time.Time, res stablePegResult) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stable_inputs_5m
(bucket, asset, venue, pair, kind, mid, included, exclude_reason) VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	bucket := stableTS(at.UTC().Truncate(stablePegTick))
	put := func(asset string, quotes []StableQuote) error {
		for _, q := range quotes {
			included := 0
			if q.Included {
				included = 1
			}
			var reason any
			if q.ExcludeReason != "" {
				reason = q.ExcludeReason
			}
			if _, err := stmt.ExecContext(ctx, bucket, asset, q.Venue, q.Pair, q.Kind, nullFloat(q.Mid), included, reason); err != nil {
				return err
			}
		}
		return nil
	}
	for asset, p := range res.Assets {
		if err := put(asset, p.Quotes); err != nil {
			return err
		}
	}
	if err := put(stableRelativeAsset, res.Relative.Quotes); err != nil {
		return err
	}
	return tx.Commit()
}

// stableInputs5mSeries returns an asset's stored inputs from from on, oldest
// first.
func (s *riskStore) stableInputs5mSeries(ctx context.Context, asset string, from time.Time) ([]stableInputRow, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT bucket, venue, pair, kind, mid, included, COALESCE(exclude_reason, '')
FROM stable_inputs_5m WHERE asset = ? AND bucket >= ? ORDER BY bucket, venue, pair`, asset, stableTS(from))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stableInputRow{}
	for rs.Next() {
		r := stableInputRow{Asset: asset}
		var bucket string
		var mid sql.NullFloat64
		var included int
		if err := rs.Scan(&bucket, &r.Venue, &r.Pair, &r.Kind, &mid, &included, &r.ExcludeReason); err != nil {
			return nil, err
		}
		r.Bucket, r.Mid, r.Included = parseStableTS(bucket), ptrFromNull(mid), included == 1
		out = append(out, r)
	}
	return out, rs.Err()
}

// StablePegHeatmap is the per-venue deviation heatmap (§7.2, §7.3): a row
// per consensus input, a column per bucket. A cell is the included sample
// with the largest |deviation| in its bucket, in bps of the asset's numéraire,
// so a short dislocation stays visible in hourly columns; null when the input
// was excluded or not sampled for the whole bucket.
type StablePegHeatmap struct {
	Resolution string          `json:"resolution"` // 5m | 1h
	Buckets    []string        `json:"buckets"`    // column starts, oldest first
	Rows       []StableHeatRow `json:"rows"`       // by venue, then pair
}

type StableHeatRow struct {
	Venue  string     `json:"venue"`
	Pair   string     `json:"pair"`
	Kind   string     `json:"kind"` // of the newest sample
	DevBps []*float64 `json:"dev_bps"`
}

// stableHeatmapStep: 5-minute columns for a day, hourly ones beyond.
func stableHeatmapStep(span time.Duration) time.Duration {
	if span <= 24*time.Hour {
		return stablePegTick
	}
	return time.Hour
}

// stableBuildHeatmap lays the rows out on span ÷ step columns, the newest
// being the column that contains end.
func stableBuildHeatmap(rows []stableInputRow, end time.Time, span, step time.Duration) StablePegHeatmap {
	n := int(span / step)
	last := end.UTC().Truncate(step)
	first := last.Add(-time.Duration(n-1) * step)
	res := "1h"
	if step == stablePegTick {
		res = "5m"
	}
	hm := StablePegHeatmap{Resolution: res, Buckets: make([]string, n), Rows: []StableHeatRow{}}
	for i := range hm.Buckets {
		hm.Buckets[i] = stableFmt(first.Add(time.Duration(i) * step))
	}
	idx := map[string]int{}
	for _, r := range rows {
		k := r.Venue + "|" + r.Pair
		i, ok := idx[k]
		if !ok {
			i = len(hm.Rows)
			idx[k] = i
			hm.Rows = append(hm.Rows, StableHeatRow{Venue: r.Venue, Pair: r.Pair, DevBps: make([]*float64, n)})
		}
		hm.Rows[i].Kind = r.Kind // rows arrive oldest first: the newest kind wins
		if !r.Included || r.Mid == nil {
			continue
		}
		col := int(r.Bucket.UTC().Truncate(step).Sub(first) / step)
		if col < 0 || col >= n {
			continue
		}
		dev := (*r.Mid - 1) * 1e4
		if c := hm.Rows[i].DevBps[col]; c == nil || math.Abs(dev) > math.Abs(*c) {
			hm.Rows[i].DevBps[col] = stableF(dev)
		}
	}
	sort.SliceStable(hm.Rows, func(a, b int) bool {
		if hm.Rows[a].Venue != hm.Rows[b].Venue {
			return hm.Rows[a].Venue < hm.Rows[b].Venue
		}
		return hm.Rows[a].Pair < hm.Rows[b].Pair
	})
	return hm
}

// pegHeatmap builds an asset's heatmap for ranges the inputs cover; nil for
// longer ranges.
func (s *stablecoinService) pegHeatmap(ctx context.Context, asset string, span time.Duration, now time.Time) *StablePegHeatmap {
	if span <= 0 || span > stableHeatmapMaxSpan {
		return nil
	}
	var rows []stableInputRow
	if s.store != nil {
		var err error
		if rows, err = s.store.stableInputs5mSeries(ctx, asset, now.Add(-span)); err != nil {
			slog.Warn("stablecoin heatmap read", "asset", asset, "error", err)
		}
	}
	hm := stableBuildHeatmap(rows, now, span, stableHeatmapStep(span))
	return &hm
}

// StableDexPoint is one stored Uniswap v3 reading: USDC priced in USDT.
type StableDexPoint struct {
	TS     string  `json:"ts"`
	Price  float64 `json:"price"`
	DevBps float64 `json:"dev_bps"`
}

// stableDex10mSeries returns the stored readings of the monitored pool from
// from on, oldest first.
func (s *riskStore) stableDex10mSeries(ctx context.Context, from time.Time) ([]StableDexPoint, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT bucket, price FROM stable_dex_10m WHERE pool = ? AND bucket >= ? AND price IS NOT NULL ORDER BY bucket`,
		stableUniswapPool, stableTS(from))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []StableDexPoint{}
	for rs.Next() {
		var bucket string
		var p float64
		if err := rs.Scan(&bucket, &p); err != nil {
			return nil, err
		}
		out = append(out, StableDexPoint{TS: stableFmt(parseStableTS(bucket)), Price: p, DevBps: (p - 1) * 1e4})
	}
	return out, rs.Err()
}

// pegDexSeries is the Uniswap series for a range: every 10-minute reading up
// to 30 days, beyond that the last reading of each UTC day.
func (s *stablecoinService) pegDexSeries(ctx context.Context, span time.Duration, daily bool, now time.Time) []StableDexPoint {
	out := []StableDexPoint{}
	if s.store == nil {
		return out
	}
	from := time.Time{}
	if span > 0 {
		from = now.Add(-span)
	}
	points, err := s.store.stableDex10mSeries(ctx, from)
	if err != nil {
		slog.Warn("stablecoin dex series read", "error", err)
		return out
	}
	if !daily {
		return points
	}
	for i, p := range points {
		if i+1 < len(points) && points[i+1].TS[:10] == p.TS[:10] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// StableECBRate is the newest stored ECB EUR/USD reference rate (EURC mode).
type StableECBRate struct {
	Day  string  `json:"day"`
	Rate float64 `json:"rate"` // USD per EUR
}

func stableLatestECB(history map[string]float64) *StableECBRate {
	var out *StableECBRate
	for day, rate := range history {
		if out == nil || day > out.Day {
			out = &StableECBRate{Day: day, Rate: rate}
		}
	}
	return out
}
