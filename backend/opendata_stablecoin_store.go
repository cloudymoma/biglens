package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Stablecoin storage (stablecoin.md §8.5): the M1 tables only. Amounts carry
// their unit in the column name; NULL means unknown and is never written as 0.
const stableSchema = `
CREATE TABLE IF NOT EXISTS stable_supply_snapshots (
  ts TEXT NOT NULL,
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  source TEXT NOT NULL,
  circulating_native REAL,
  onchain_total_native REAL,
  unissued_native REAL,
  quarantined_native REAL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY (ts, asset, chain, source)
);
CREATE INDEX IF NOT EXISTS stable_supply_snapshots_series ON stable_supply_snapshots(asset, source, ts);
CREATE TABLE IF NOT EXISTS stable_supply_daily (
  day TEXT NOT NULL,
  asset TEXT NOT NULL,
  source TEXT NOT NULL,
  circulating_native REAL,
  circulating_usd REAL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (day, asset, source)
);
CREATE TABLE IF NOT EXISTS stable_chain_daily (
  day TEXT NOT NULL,
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  source TEXT NOT NULL,
  circulating_native REAL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (day, asset, chain, source)
);
CREATE TABLE IF NOT EXISTS stable_tether_snapshots (
  update_id INTEGER PRIMARY KEY,
  total_assets_usd REAL,
  total_liabilities_usd REAL,
  shareholder_equity_usd REAL,
  chains_json TEXT NOT NULL,
  fetched_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stable_rates_daily (
  day TEXT NOT NULL,
  series TEXT NOT NULL,
  value REAL NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY (day, series)
);`

// stableSnapshotRetention: supply snapshots are kept 180 days; the daily
// tables keep one row per day forever (§8.5 "180 d, then daily").
const stableSnapshotRetention = 180 * 24 * time.Hour

// Rate series stored in stable_rates_daily; the unit is part of the name.
const (
	stableSeriesECBEURUSD  = "ecb_eurusd"      // USD per EUR, ECB reference rate
	stableSeriesECBESTRPct = "ecb_estr_pct"    // €STR, percent per year
	stableSeriesUST13WPct  = "ust_13w_cey_pct" // 13-week T-bill coupon equivalent, percent per year
)

// stableSupplySnapshot is one supply observation. TS is the observation time
// (issuer update time or RPC read time); FetchedAt is when BigLens fetched it.
type stableSupplySnapshot struct {
	TS                 time.Time
	Asset              string // usdt | usdc | eurc
	Chain              string // registry network, issuer chain id, or "all"
	Source             string // issuer | onchain | aggregator
	CirculatingNative  *float64
	OnchainTotalNative *float64
	UnissuedNative     *float64
	QuarantinedNative  *float64
	FetchedAt          time.Time
}

type stableSupplyDaily struct {
	Day               string // YYYY-MM-DD, UTC
	Asset             string
	Source            string
	CirculatingNative *float64
	CirculatingUSD    *float64
	UpdatedAt         time.Time // observation time of the value
}

type stableChainDaily struct {
	Day               string
	Asset             string
	Chain             string
	Source            string
	CirculatingNative *float64
	UpdatedAt         time.Time
}

type stableTetherSnapshot struct {
	UpdateID             int64 // Tether JSON "id": the update time, unix seconds
	TotalAssetsUSD       *float64
	TotalLiabilitiesUSD  *float64
	ShareholderEquityUSD *float64
	ChainsJSON           string
	FetchedAt            time.Time
}

type stableRateDaily struct {
	Day       string
	Series    string
	Value     float64
	FetchedAt time.Time
}

func stableTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseStableTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func (s *riskStore) ensureStableSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("stablecoin store: nil store")
	}
	if _, err := s.db.ExecContext(ctx, stableSchema); err != nil {
		return fmt.Errorf("stablecoin schema: %w", err)
	}
	return nil
}

// insertStableSupplySnapshots stores observations; an observation already
// stored (same ts, asset, chain, source) is left unchanged.
func (s *riskStore) insertStableSupplySnapshots(ctx context.Context, rows []stableSupplySnapshot) error {
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
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO stable_supply_snapshots (ts, asset, chain, source, circulating_native, onchain_total_native, unissued_native, quarantined_native, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ts, asset, chain, source) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, stableTS(r.TS), r.Asset, r.Chain, r.Source,
			r.CirculatingNative, r.OnchainTotalNative, r.UnissuedNative, r.QuarantinedNative, stableTS(r.FetchedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableSupplySnapshots returns one asset/source's snapshots with from ≤ ts ≤ to,
// ordered by ts, then chain.
func (s *riskStore) stableSupplySnapshots(ctx context.Context, asset, source string, from, to time.Time) ([]stableSupplySnapshot, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT ts, asset, chain, source, circulating_native, onchain_total_native, unissued_native, quarantined_native, fetched_at
FROM stable_supply_snapshots
WHERE asset = ? AND source = ? AND ts >= ? AND ts <= ?
ORDER BY ts, chain`, asset, source, stableTS(from), stableTS(to))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []stableSupplySnapshot
	for rs.Next() {
		var r stableSupplySnapshot
		var ts, fetched string
		if err := rs.Scan(&ts, &r.Asset, &r.Chain, &r.Source, &r.CirculatingNative, &r.OnchainTotalNative,
			&r.UnissuedNative, &r.QuarantinedNative, &fetched); err != nil {
			return nil, err
		}
		r.TS, r.FetchedAt = parseStableTS(ts), parseStableTS(fetched)
		out = append(out, r)
	}
	return out, rs.Err()
}

// upsertStableSupplyDaily keeps the newest observation of each day: a row
// whose updated_at is older than the stored one is ignored.
func (s *riskStore) upsertStableSupplyDaily(ctx context.Context, rows []stableSupplyDaily) error {
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
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO stable_supply_daily (day, asset, source, circulating_native, circulating_usd, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(day, asset, source) DO UPDATE SET
  circulating_native = excluded.circulating_native,
  circulating_usd = excluded.circulating_usd,
  updated_at = excluded.updated_at
WHERE excluded.updated_at >= stable_supply_daily.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.Asset, r.Source, r.CirculatingNative, r.CirculatingUSD, stableTS(r.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableSupplyDailySeries returns one asset/source's daily rows from fromDay
// (inclusive), oldest first.
func (s *riskStore) stableSupplyDailySeries(ctx context.Context, asset, source, fromDay string) ([]stableSupplyDaily, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, asset, source, circulating_native, circulating_usd, updated_at
FROM stable_supply_daily WHERE asset = ? AND source = ? AND day >= ? ORDER BY day`, asset, source, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []stableSupplyDaily
	for rs.Next() {
		var r stableSupplyDaily
		var updated string
		if err := rs.Scan(&r.Day, &r.Asset, &r.Source, &r.CirculatingNative, &r.CirculatingUSD, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt = parseStableTS(updated)
		out = append(out, r)
	}
	return out, rs.Err()
}

// upsertStableChainDaily keeps the newest observation per (day, asset, chain, source).
func (s *riskStore) upsertStableChainDaily(ctx context.Context, rows []stableChainDaily) error {
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
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO stable_chain_daily (day, asset, chain, source, circulating_native, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(day, asset, chain, source) DO UPDATE SET
  circulating_native = excluded.circulating_native,
  updated_at = excluded.updated_at
WHERE excluded.updated_at >= stable_chain_daily.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.Asset, r.Chain, r.Source, r.CirculatingNative, stableTS(r.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableChainDailySeries returns one asset/source's per-chain daily rows from
// fromDay (inclusive), ordered by day, then chain.
func (s *riskStore) stableChainDailySeries(ctx context.Context, asset, source, fromDay string) ([]stableChainDaily, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, asset, chain, source, circulating_native, updated_at
FROM stable_chain_daily WHERE asset = ? AND source = ? AND day >= ? ORDER BY day, chain`, asset, source, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []stableChainDaily
	for rs.Next() {
		var r stableChainDaily
		var updated string
		if err := rs.Scan(&r.Day, &r.Asset, &r.Chain, &r.Source, &r.CirculatingNative, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt = parseStableTS(updated)
		out = append(out, r)
	}
	return out, rs.Err()
}

// insertStableTetherSnapshot stores one Tether transparency update; the same
// update id is stored once.
func (s *riskStore) insertStableTetherSnapshot(ctx context.Context, r stableTetherSnapshot) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO stable_tether_snapshots (update_id, total_assets_usd, total_liabilities_usd, shareholder_equity_usd, chains_json, fetched_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(update_id) DO NOTHING`, r.UpdateID, r.TotalAssetsUSD, r.TotalLiabilitiesUSD, r.ShareholderEquityUSD, r.ChainsJSON, stableTS(r.FetchedAt))
	return err
}

// stableTetherSnapshots returns the newest Tether updates first.
func (s *riskStore) stableTetherSnapshots(ctx context.Context, limit int) ([]stableTetherSnapshot, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT update_id, total_assets_usd, total_liabilities_usd, shareholder_equity_usd, chains_json, fetched_at
FROM stable_tether_snapshots ORDER BY update_id DESC LIMIT ?`, max(limit, 1))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []stableTetherSnapshot
	for rs.Next() {
		var r stableTetherSnapshot
		var fetched string
		if err := rs.Scan(&r.UpdateID, &r.TotalAssetsUSD, &r.TotalLiabilitiesUSD, &r.ShareholderEquityUSD, &r.ChainsJSON, &fetched); err != nil {
			return nil, err
		}
		r.FetchedAt = parseStableTS(fetched)
		out = append(out, r)
	}
	return out, rs.Err()
}

// upsertStableRatesDaily stores one value per (day, series); a re-published
// value for the same day replaces the old one.
func (s *riskStore) upsertStableRatesDaily(ctx context.Context, rows []stableRateDaily) error {
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
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO stable_rates_daily (day, series, value, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT(day, series) DO UPDATE SET value = excluded.value, fetched_at = excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.Series, r.Value, stableTS(r.FetchedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableRatesSeries returns one series from fromDay (inclusive), oldest first.
func (s *riskStore) stableRatesSeries(ctx context.Context, series, fromDay string) ([]stableRateDaily, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, series, value, fetched_at FROM stable_rates_daily WHERE series = ? AND day >= ? ORDER BY day`, series, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []stableRateDaily
	for rs.Next() {
		var r stableRateDaily
		var fetched string
		if err := rs.Scan(&r.Day, &r.Series, &r.Value, &fetched); err != nil {
			return nil, err
		}
		r.FetchedAt = parseStableTS(fetched)
		out = append(out, r)
	}
	return out, rs.Err()
}

// pruneStable deletes supply snapshots older than the retention window and
// logs how many rows it removed. Daily tables, Tether snapshots and rates are
// kept forever.
func (s *riskStore) pruneStable(ctx context.Context, now time.Time) (int64, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return 0, err
	}
	cutoff := stableTS(now.Add(-stableSnapshotRetention))
	res, err := s.db.ExecContext(ctx, `DELETE FROM stable_supply_snapshots WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	slog.Info("stablecoin retention prune", "table", "stable_supply_snapshots", "cutoff", cutoff, "deleted", n)
	return n, nil
}
