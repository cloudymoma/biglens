package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const btcPricesSchema = `
CREATE TABLE IF NOT EXISTS btc_price_daily (
  day TEXT PRIMARY KEY,
  open REAL,
  high REAL,
  low REAL,
  close REAL NOT NULL,
  volume REAL,
  source TEXT NOT NULL,
  synced_at TEXT NOT NULL
);`

type btcPriceDay struct {
	Day      string   `json:"day"`
	Open     *float64 `json:"open,omitempty"`
	High     *float64 `json:"high,omitempty"`
	Low      *float64 `json:"low,omitempty"`
	Close    float64  `json:"close"`
	Volume   *float64 `json:"volume,omitempty"`
	Source   string   `json:"source"`
	SyncedAt string   `json:"synced_at,omitempty"`
}

func (s *riskStore) ensureBtcPricesSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("btc prices: nil store")
	}
	if _, err := s.db.ExecContext(ctx, btcPricesSchema); err != nil {
		return fmt.Errorf("btc prices schema: %w", err)
	}
	return nil
}

func (s *riskStore) upsertBtcPrices(ctx context.Context, rows []btcPriceDay, syncedAt time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	if err := s.ensureBtcPricesSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_price_daily (day, open, high, low, close, volume, source, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  open = excluded.open,
  high = excluded.high,
  low = excluded.low,
  close = excluded.close,
  volume = excluded.volume,
  source = excluded.source,
  synced_at = excluded.synced_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := syncedAt.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if r.Day == "" || r.Close <= 0 {
			continue
		}
		src := r.Source
		if src == "" {
			src = "unknown"
		}
		if _, err := stmt.ExecContext(ctx,
			r.Day,
			nullablePrice(r.Open),
			nullablePrice(r.High),
			nullablePrice(r.Low),
			r.Close,
			nullablePrice(r.Volume),
			src,
			at,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadBtcPrices(ctx context.Context) ([]btcPriceDay, error) {
	if err := s.ensureBtcPricesSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, open, high, low, close, volume, source, synced_at
FROM btc_price_daily
ORDER BY day`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []btcPriceDay
	for rs.Next() {
		var (
			r                   btcPriceDay
			open, high, low, vl sql.NullFloat64
		)
		if err := rs.Scan(&r.Day, &open, &high, &low, &r.Close, &vl, &r.Source, &r.SyncedAt); err != nil {
			return nil, err
		}
		if open.Valid {
			v := open.Float64
			r.Open = &v
		}
		if high.Valid {
			v := high.Float64
			r.High = &v
		}
		if low.Valid {
			v := low.Float64
			r.Low = &v
		}
		if vl.Valid {
			v := vl.Float64
			r.Volume = &v
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
