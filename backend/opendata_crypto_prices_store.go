package main

// SQLite persistence for the on-chain USD prices (opendata_crypto_prices.go).
// The table lives in the Address Risk database file (address_risk.db_path)
// but is owned and created lazily here, so the shared riskSchema DDL list is
// untouched. One row per synced UTC day; a day without qualifying swaps keeps
// NULL prices so it is not re-queried.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const onchainPricesSchema = `
CREATE TABLE IF NOT EXISTS onchain_prices_daily (
  day       TEXT PRIMARY KEY,
  eth_usd   REAL,
  btc_usd   REAL,
  eth_swaps INTEGER NOT NULL DEFAULT 0,
  btc_swaps INTEGER NOT NULL DEFAULT 0,
  synced_at TEXT NOT NULL DEFAULT ''
);`

func (s *riskStore) ensureOnchainPricesSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, onchainPricesSchema); err != nil {
		return fmt.Errorf("onchain prices schema: %w", err)
	}
	return nil
}

// upsertOnchainPrices writes (or overwrites) one row per day in one
// transaction, stamping syncedAt.
func (s *riskStore) upsertOnchainPrices(ctx context.Context, rows []onchainPriceDay, syncedAt time.Time) error {
	if err := s.ensureOnchainPricesSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO onchain_prices_daily (day, eth_usd, btc_usd, eth_swaps, btc_swaps, synced_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  eth_usd = excluded.eth_usd, btc_usd = excluded.btc_usd,
  eth_swaps = excluded.eth_swaps, btc_swaps = excluded.btc_swaps,
  synced_at = excluded.synced_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := syncedAt.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, nullablePrice(r.EthUSD), nullablePrice(r.BtcUSD), r.EthSwaps, r.BtcSwaps, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullablePrice(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// loadOnchainPrices returns every stored day (ascending) and the newest
// synced_at (zero when empty).
func (s *riskStore) loadOnchainPrices(ctx context.Context) ([]onchainPriceDay, time.Time, error) {
	if err := s.ensureOnchainPricesSchema(ctx); err != nil {
		return nil, time.Time{}, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, eth_usd, btc_usd, eth_swaps, btc_swaps, synced_at
FROM onchain_prices_daily ORDER BY day`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rs.Close()
	var (
		out  []onchainPriceDay
		asOf time.Time
	)
	for rs.Next() {
		var (
			r        onchainPriceDay
			eth, btc sql.NullFloat64
			synced   string
		)
		if err := rs.Scan(&r.Day, &eth, &btc, &r.EthSwaps, &r.BtcSwaps, &synced); err != nil {
			return nil, time.Time{}, err
		}
		if eth.Valid {
			r.EthUSD = &eth.Float64
		}
		if btc.Valid {
			r.BtcUSD = &btc.Float64
		}
		if t, err := time.Parse(time.RFC3339, synced); err == nil && t.After(asOf) {
			asOf = t
		}
		out = append(out, r)
	}
	return out, asOf, rs.Err()
}
