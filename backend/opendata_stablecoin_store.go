package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"
)

// Stablecoin storage (stablecoin.md §8.5): the M1 supply tables and the M2
// peg tables. Amounts carry their unit in the column name; NULL means unknown
// and is never written as 0.
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
);
CREATE TABLE IF NOT EXISTS stable_quotes_5m (
  bucket TEXT NOT NULL,
  venue TEXT NOT NULL,
  pair TEXT NOT NULL,
  bid REAL,
  ask REAL,
  mid REAL,
  last REAL,
  vol_usd REAL,
  spread_bps REAL,
  age_seconds INTEGER,
  included INTEGER NOT NULL,
  exclude_reason TEXT,
  PRIMARY KEY (bucket, venue, pair)
);
CREATE TABLE IF NOT EXISTS stable_peg_5m (
  bucket TEXT NOT NULL,
  asset TEXT NOT NULL,
  numeraire TEXT NOT NULL,
  state TEXT NOT NULL,
  consensus REAL,
  dev_bps REAL,
  disp_bps REAL,
  single_venue_price REAL,
  inputs_used INTEGER NOT NULL,
  venues_used INTEGER NOT NULL,
  PRIMARY KEY (bucket, asset)
);
CREATE TABLE IF NOT EXISTS stable_peg_daily (
  day TEXT NOT NULL,
  asset TEXT NOT NULL,
  source TEXT NOT NULL,
  open REAL,
  high REAL,
  low REAL,
  close REAL,
  vwap REAL,
  volume REAL,
  min_dev_bps REAL,
  max_dev_bps REAL,
  minutes_ge_30 INTEGER,
  minutes_ge_100 INTEGER,
  samples INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (day, asset, source)
);
CREATE TABLE IF NOT EXISTS stable_price_daily (
  day TEXT NOT NULL,
  gecko_id TEXT NOT NULL,
  price_usd REAL NOT NULL,
  source TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY (day, gecko_id)
);
CREATE TABLE IF NOT EXISTS stable_dex_10m (
  bucket TEXT NOT NULL,
  pool TEXT NOT NULL,
  price REAL,
  tick INTEGER,
  sqrt_price_x96 TEXT,
  shares_json TEXT,
  tvl_usd REAL,
  PRIMARY KEY (bucket, pool)
);
CREATE TABLE IF NOT EXISTS stable_inputs_5m (
  bucket TEXT NOT NULL,
  asset TEXT NOT NULL,
  venue TEXT NOT NULL,
  pair TEXT NOT NULL,
  kind TEXT NOT NULL,
  mid REAL,
  included INTEGER NOT NULL,
  exclude_reason TEXT,
  PRIMARY KEY (bucket, asset, venue, pair)
);
CREATE TABLE IF NOT EXISTS stable_control_state (
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  field TEXT NOT NULL,
  value TEXT,
  first_seen TEXT,
  confirmed_at TEXT,
  confirmed_by TEXT NOT NULL DEFAULT '',
  last_seen TEXT,
  is_baseline INTEGER NOT NULL DEFAULT 0,
  baseline_at TEXT,
  pending_value TEXT,
  pending_first_seen TEXT,
  pending_last_seen TEXT,
  pending_disputed INTEGER NOT NULL DEFAULT 0,
  last_read_at TEXT,
  last_code TEXT NOT NULL DEFAULT '',
  last_endpoint TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (asset, chain, field)
);
CREATE TABLE IF NOT EXISTS stable_control_events (
  detected_at TEXT NOT NULL,
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  field TEXT NOT NULL,
  old_value TEXT NOT NULL,
  new_value TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  source TEXT NOT NULL,
  confirmed_by TEXT NOT NULL DEFAULT '',
  tx_hash TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (detected_at, asset, chain, field)
);
CREATE TABLE IF NOT EXISTS stable_signal_state (
  asset TEXT NOT NULL,
  signal TEXT NOT NULL,
  state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  rule TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  since TEXT NOT NULL,
  as_of TEXT,
  inputs_json TEXT NOT NULL DEFAULT '{}',
  hyst_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (asset, signal)
);
CREATE TABLE IF NOT EXISTS stable_signal_events (
  ts TEXT NOT NULL,
  asset TEXT NOT NULL,
  signal TEXT NOT NULL,
  from_state TEXT NOT NULL,
  to_state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  inputs_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (ts, asset, signal)
);
CREATE TABLE IF NOT EXISTS stable_budget (
  month TEXT NOT NULL,
  job TEXT NOT NULL,
  billed_bytes INTEGER NOT NULL,
  PRIMARY KEY (month, job)
);
CREATE TABLE IF NOT EXISTS stable_reservations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  pid INTEGER NOT NULL,
  bq_job_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stable_kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stable_onchain_daily (
  day TEXT NOT NULL,
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  transfers INTEGER NOT NULL,
  zero_value INTEGER NOT NULL,
  volume_raw TEXT NOT NULL,
  senders INTEGER NOT NULL,
  receivers INTEGER NOT NULL,
  ge_1m_count INTEGER NOT NULL,
  ge_1m_volume_raw TEXT NOT NULL,
  minted_raw TEXT,
  burned_raw TEXT,
  synced_at TEXT NOT NULL,
  PRIMARY KEY (day, asset, chain)
);
CREATE TABLE IF NOT EXISTS stable_large_transfers (
  day TEXT NOT NULL,
  asset TEXT NOT NULL,
  chain TEXT NOT NULL,
  tx_hash TEXT NOT NULL,
  log_index INTEGER NOT NULL,
  ts TEXT NOT NULL,
  from_addr TEXT NOT NULL,
  to_addr TEXT NOT NULL,
  amount_raw TEXT NOT NULL,
  PRIMARY KEY (day, asset, chain, tx_hash, log_index)
);
CREATE TABLE IF NOT EXISTS stable_supply_events (
  chain TEXT NOT NULL,
  tx_hash TEXT NOT NULL,
  log_index INTEGER NOT NULL,
  day TEXT NOT NULL,
  ts TEXT NOT NULL,
  asset TEXT NOT NULL,
  kind TEXT NOT NULL,
  amount_raw TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (chain, tx_hash, log_index)
);`

// stableSnapshotRetention: supply snapshots are kept 180 days; the daily
// tables keep one row per day forever (§8.5 "180 d, then daily").
const stableSnapshotRetention = 180 * 24 * time.Hour

// Peg retention (§8.5): venue quotes and each asset's consensus inputs (the
// heatmap rows) 30 days; 5-minute peg samples and DEX readings 180 days, then
// stable_peg_daily keeps the day forever.
const (
	stableQuotesRetention = 30 * 24 * time.Hour
	stablePegRetention    = 180 * 24 * time.Hour
	stableDexRetention    = 180 * 24 * time.Hour
)

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

// stableChainDaily is the newest observation of one chain per day. For
// source "onchain" the value is the on-chain total supply of the deployment;
// for "issuer" and "aggregator" it is circulating supply.
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
	s.stableOnce.Do(func() {
		if _, err := s.db.ExecContext(ctx, stableSchema); err != nil {
			s.stableSchemaErr = fmt.Errorf("stablecoin schema: %w", err)
			return
		}
		// Idempotent migrations for existing databases created before vwap/volume were added.
		for _, stmt := range []string{
			`ALTER TABLE stable_peg_daily ADD COLUMN vwap REAL`,
			`ALTER TABLE stable_peg_daily ADD COLUMN volume REAL`,
		} {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				s.stableSchemaErr = fmt.Errorf("stablecoin migrate: %w", err)
				return
			}
		}
	})
	return s.stableSchemaErr
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

// pruneStable deletes rows older than each table's retention window and logs
// how many rows it removed. Daily tables, Tether snapshots and rates are kept
// forever, and so are the control tables: stable_control_state holds the
// current value per field and stable_control_events the change log (§8.5).
func (s *riskStore) pruneStable(ctx context.Context, now time.Time) (int64, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return 0, err
	}
	var total int64
	for _, t := range []struct {
		table, column string
		keep          time.Duration
	}{
		{"stable_supply_snapshots", "ts", stableSnapshotRetention},
		{"stable_quotes_5m", "bucket", stableQuotesRetention},
		{"stable_inputs_5m", "bucket", stableQuotesRetention},
		{"stable_peg_5m", "bucket", stablePegRetention},
		{"stable_dex_10m", "bucket", stableDexRetention},
	} {
		cutoff := stableTS(now.Add(-t.keep))
		res, err := s.db.ExecContext(ctx, `DELETE FROM `+t.table+` WHERE `+t.column+` < ?`, cutoff)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		slog.Info("stablecoin retention prune", "table", t.table, "cutoff", cutoff, "deleted", n)
		total += n
	}
	dayCutoff := now.Add(-365 * 24 * time.Hour).UTC().Format(time.DateOnly)
	if res, err := s.db.ExecContext(ctx, `DELETE FROM stable_large_transfers WHERE day < ?`, dayCutoff); err != nil {
		return total, err
	} else if n, err := res.RowsAffected(); err != nil {
		return total, err
	} else {
		slog.Info("stablecoin retention prune", "table", "stable_large_transfers", "cutoff", dayCutoff, "deleted", n)
		total += n
	}
	return total, nil
}

// hasStableSnapshotInHour reports whether any snapshot of asset/source was
// stored for the UTC hour containing t (the sampler stores at most one
// observation per source per hour).
func (s *riskStore) hasStableSnapshotInHour(ctx context.Context, asset, source string, t time.Time) (bool, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return false, err
	}
	start := t.UTC().Truncate(time.Hour)
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (SELECT 1 FROM stable_supply_snapshots
WHERE asset = ? AND source = ? AND ts >= ? AND ts < ? LIMIT 1)`, asset, source, stableTS(start), stableTS(start.Add(time.Hour))).Scan(&n)
	return n > 0, err
}

// stableSupplyNear returns the asset/source/chain snapshot closest to target
// within ±tol, or nil when there is none.
func (s *riskStore) stableSupplyNear(ctx context.Context, asset, source, chain string, target time.Time, tol time.Duration) (*stableSupplySnapshot, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT ts, asset, chain, source, circulating_native, onchain_total_native, unissued_native, quarantined_native, fetched_at
FROM stable_supply_snapshots
WHERE asset = ? AND source = ? AND chain = ? AND ts >= ? AND ts <= ?
ORDER BY ABS(CAST(strftime('%s', ts) AS INTEGER) - ?), ts
LIMIT 1`, asset, source, chain, stableTS(target.Add(-tol)), stableTS(target.Add(tol)), target.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	if !rs.Next() {
		return nil, rs.Err()
	}
	var r stableSupplySnapshot
	var ts, fetched string
	if err := rs.Scan(&ts, &r.Asset, &r.Chain, &r.Source, &r.CirculatingNative, &r.OnchainTotalNative,
		&r.UnissuedNative, &r.QuarantinedNative, &fetched); err != nil {
		return nil, err
	}
	r.TS, r.FetchedAt = parseStableTS(ts), parseStableTS(fetched)
	return &r, nil
}

func (s *riskStore) getStableKV(ctx context.Context, k string) (string, bool, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return "", false, err
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM stable_kv WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *riskStore) setStableKV(ctx context.Context, k, v string) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO stable_kv (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

func (s *riskStore) deleteStableKV(ctx context.Context, keys ...string) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM stable_kv WHERE k = ?`, k); err != nil {
			return err
		}
	}
	return nil
}

type stableOnchainDailyRow struct {
	Day           string
	Asset         string
	Chain         string
	Transfers     int64
	ZeroValue     int64
	VolumeRaw     string
	Senders       int64
	Receivers     int64
	Ge1MCount     int64
	Ge1MVolumeRaw string
	MintedRaw     string
	BurnedRaw     string
	SyncedAt      time.Time
}

type StableOnchainDay struct {
	Day              string   `json:"day"`
	Asset            string   `json:"asset"`
	Chain            string   `json:"chain"`
	Transfers        int64    `json:"transfers"`
	ZeroValue        int64    `json:"zero_value"`
	VolumeRaw        string   `json:"volume_raw"`
	VolumeNative     float64  `json:"volume_native"`
	Senders          int64    `json:"senders"`
	Receivers        int64    `json:"receivers"`
	Ge1MCount        int64    `json:"ge_1m_count"`
	Ge1MVolumeRaw    string   `json:"ge_1m_volume_raw"`
	Ge1MVolumeNative float64  `json:"ge_1m_volume_native"`
	MintedRaw        string   `json:"minted_raw,omitempty"`
	MintedNative     *float64 `json:"minted_native"`
	BurnedRaw        string   `json:"burned_raw,omitempty"`
	BurnedNative     *float64 `json:"burned_native"`
	Velocity         *float64 `json:"velocity,omitempty"`
	SyncedAt         string   `json:"synced_at"`
}

type StableLargeTransfer struct {
	Day          string  `json:"day"`
	Asset        string  `json:"asset"`
	Chain        string  `json:"chain"`
	TxHash       string  `json:"tx_hash"`
	LogIndex     int64   `json:"log_index"`
	TS           string  `json:"ts"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	AmountRaw    string  `json:"amount_raw"`
	AmountNative float64 `json:"amount_native"`
	ExplorerURL  string  `json:"explorer_url,omitempty"`
}

type StableSupplyEvent struct {
	Day          string  `json:"day"`
	TS           string  `json:"ts"`
	Asset        string  `json:"asset"`
	Chain        string  `json:"chain"`
	TxHash       string  `json:"tx_hash"`
	LogIndex     int64   `json:"log_index"`
	Kind         string  `json:"kind"` // mint | burn | issue | redeem
	AmountRaw    string  `json:"amount_raw"`
	AmountNative float64 `json:"amount_native"`
	Actor        string  `json:"actor,omitempty"`
	ExplorerURL  string  `json:"explorer_url,omitempty"`
}

// stableRawToNative6 converts a 6-decimal integer string to float64 native units.
func stableRawToNative6(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	z, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return 0, false
	}
	r := new(big.Rat).SetFrac(z, big.NewInt(1_000_000))
	f, _ := r.Float64()
	return f, true
}

func (s *riskStore) upsertStableOnchainDaily(ctx context.Context, rows []stableOnchainDailyRow) error {
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
INSERT INTO stable_onchain_daily (
  day, asset, chain, transfers, zero_value, volume_raw, senders, receivers,
  ge_1m_count, ge_1m_volume_raw, minted_raw, burned_raw, synced_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(day, asset, chain) DO UPDATE SET
  transfers = excluded.transfers,
  zero_value = excluded.zero_value,
  volume_raw = excluded.volume_raw,
  senders = excluded.senders,
  receivers = excluded.receivers,
  ge_1m_count = excluded.ge_1m_count,
  ge_1m_volume_raw = excluded.ge_1m_volume_raw,
  minted_raw = excluded.minted_raw,
  burned_raw = excluded.burned_raw,
  synced_at = excluded.synced_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		var minted, burned any
		if r.MintedRaw != "" {
			minted = r.MintedRaw
		}
		if r.BurnedRaw != "" {
			burned = r.BurnedRaw
		}
		if _, err := stmt.ExecContext(ctx,
			r.Day, r.Asset, r.Chain, r.Transfers, r.ZeroValue, r.VolumeRaw,
			r.Senders, r.Receivers, r.Ge1MCount, r.Ge1MVolumeRaw, minted, burned,
			stableTS(r.SyncedAt),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) replaceStableLargeTransfers(ctx context.Context, chain, fromDay, toDay string, rows []StableLargeTransfer) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM stable_large_transfers WHERE chain = ? AND day >= ? AND day <= ?`,
		chain, fromDay, toDay,
	); err != nil {
		return err
	}
	if len(rows) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
INSERT OR REPLACE INTO stable_large_transfers (
  day, asset, chain, tx_hash, log_index, ts, from_addr, to_addr, amount_raw
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rows {
			if _, err := stmt.ExecContext(ctx,
				r.Day, r.Asset, r.Chain, r.TxHash, r.LogIndex, r.TS, r.From, r.To, r.AmountRaw,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *riskStore) insertStableSupplyEvents(ctx context.Context, rows []StableSupplyEvent) error {
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
INSERT OR REPLACE INTO stable_supply_events (
  chain, tx_hash, log_index, day, ts, asset, kind, amount_raw, actor
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx,
			r.Chain, r.TxHash, r.LogIndex, r.Day, r.TS, r.Asset, r.Kind, r.AmountRaw, r.Actor,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) insertStableBQControlEvents(ctx context.Context, events []stableControlEvent) error {
	if len(events) == 0 {
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
	for _, ev := range events {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO stable_control_events
(detected_at, asset, chain, field, old_value, new_value, first_seen, source, confirmed_by, tx_hash) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			stableTS(ev.DetectedAt), ev.Asset, ev.Chain, ev.Field, ev.Old, ev.New, stableTS(ev.FirstSeen), ev.Source, ev.ConfirmedBy, ev.TxHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) stableOnchainDaily(ctx context.Context, asset, fromDay string) ([]StableOnchainDay, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, asset, chain, transfers, zero_value, volume_raw, senders, receivers,
       ge_1m_count, ge_1m_volume_raw, minted_raw, burned_raw, synced_at
FROM stable_onchain_daily
WHERE (? = '' OR ? = 'all' OR asset = ?) AND day >= ?
ORDER BY day, asset, chain`, asset, asset, asset, fromDay)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []StableOnchainDay{}
	for rs.Next() {
		var r StableOnchainDay
		var minted, burned sql.NullString
		if err := rs.Scan(
			&r.Day, &r.Asset, &r.Chain, &r.Transfers, &r.ZeroValue, &r.VolumeRaw,
			&r.Senders, &r.Receivers, &r.Ge1MCount, &r.Ge1MVolumeRaw,
			&minted, &burned, &r.SyncedAt,
		); err != nil {
			return nil, err
		}
		if v, ok := stableRawToNative6(r.VolumeRaw); ok {
			r.VolumeNative = v
		}
		if v, ok := stableRawToNative6(r.Ge1MVolumeRaw); ok {
			r.Ge1MVolumeNative = v
		}
		if minted.Valid && minted.String != "" {
			r.MintedRaw = minted.String
			if v, ok := stableRawToNative6(minted.String); ok {
				r.MintedNative = &v
			}
		}
		if burned.Valid && burned.String != "" {
			r.BurnedRaw = burned.String
			if v, ok := stableRawToNative6(burned.String); ok {
				r.BurnedNative = &v
			}
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *riskStore) stableLargeTransfers(ctx context.Context, asset, fromDay string, limit int) ([]StableLargeTransfer, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, asset, chain, tx_hash, log_index, ts, from_addr, to_addr, amount_raw
FROM stable_large_transfers
WHERE (? = '' OR ? = 'all' OR asset = ?) AND day >= ?
ORDER BY day DESC, CAST(amount_raw AS INTEGER) DESC, tx_hash, log_index
LIMIT ?`, asset, asset, asset, fromDay, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []StableLargeTransfer{}
	for rs.Next() {
		var r StableLargeTransfer
		if err := rs.Scan(&r.Day, &r.Asset, &r.Chain, &r.TxHash, &r.LogIndex, &r.TS, &r.From, &r.To, &r.AmountRaw); err != nil {
			return nil, err
		}
		if v, ok := stableRawToNative6(r.AmountRaw); ok {
			r.AmountNative = v
		}
		if info, ok := chains[r.Chain]; ok && info.TxURL != "" && r.TxHash != "" {
			r.ExplorerURL = fmt.Sprintf(info.TxURL, r.TxHash)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *riskStore) stableSupplyEvents(ctx context.Context, asset, fromDay string, limit int) ([]StableSupplyEvent, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, ts, asset, chain, tx_hash, log_index, kind, amount_raw, actor
FROM stable_supply_events
WHERE (? = '' OR ? = 'all' OR asset = ?) AND day >= ?
ORDER BY ts DESC, tx_hash, log_index
LIMIT ?`, asset, asset, asset, fromDay, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []StableSupplyEvent{}
	for rs.Next() {
		var r StableSupplyEvent
		if err := rs.Scan(&r.Day, &r.TS, &r.Asset, &r.Chain, &r.TxHash, &r.LogIndex, &r.Kind, &r.AmountRaw, &r.Actor); err != nil {
			return nil, err
		}
		if v, ok := stableRawToNative6(r.AmountRaw); ok {
			r.AmountNative = v
		}
		if info, ok := chains[r.Chain]; ok && info.TxURL != "" && r.TxHash != "" {
			r.ExplorerURL = fmt.Sprintf(info.TxURL, r.TxHash)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *riskStore) stableHasOnchainData(ctx context.Context) (bool, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM stable_onchain_daily LIMIT 1)`).Scan(&n)
	return n > 0, err
}
