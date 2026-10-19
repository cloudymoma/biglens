package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const btcMarketDailySchema = `
CREATE TABLE IF NOT EXISTS btc_market_daily (
  day TEXT PRIMARY KEY,
  payload TEXT NOT NULL,
  captured_at TEXT NOT NULL
);`

func (s *riskStore) ensureBtcMarketDailySchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("btc market daily: nil store")
	}
	if _, err := s.db.ExecContext(ctx, btcMarketDailySchema); err != nil {
		return fmt.Errorf("btc market daily schema: %w", err)
	}
	return nil
}

func (s *riskStore) hasBtcMarketDaily(ctx context.Context, day string) (bool, error) {
	if err := s.ensureBtcMarketDailySchema(ctx); err != nil {
		return false, err
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT day FROM btc_market_daily WHERE day = ?`, day).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return existing != "", nil
}

func (s *riskStore) putBtcMarketDaily(ctx context.Context, pt BtcMarketDailyPoint, capturedAt time.Time) (bool, error) {
	if pt.Date == "" {
		return false, fmt.Errorf("btc market daily: empty date")
	}
	if err := s.ensureBtcMarketDailySchema(ctx); err != nil {
		return false, err
	}
	b, err := json.Marshal(pt)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO btc_market_daily (day, payload, captured_at)
VALUES (?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  payload = excluded.payload,
  captured_at = excluded.captured_at
WHERE btc_market_daily.payload LIKE '%"total_oi_usd":null%'`,
		pt.Date, string(b), capturedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *riskStore) loadBtcMarketDaily(ctx context.Context, limit int) ([]BtcMarketDailyPoint, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if err := s.ensureBtcMarketDailySchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 365
	}
	rs, err := s.db.QueryContext(ctx, `
SELECT day, payload
FROM btc_market_daily
ORDER BY day DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []BtcMarketDailyPoint
	for rs.Next() {
		var (
			day     string
			payload string
			pt      BtcMarketDailyPoint
		)
		if err := rs.Scan(&day, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &pt); err != nil {
			continue
		}
		if pt.Date == "" {
			pt.Date = day
		}
		out = append(out, pt)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date < out[j].Date
	})
	return out, nil
}
