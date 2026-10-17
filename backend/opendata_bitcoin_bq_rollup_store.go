package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const btcBlocksCoverageKey = "blocks_coverage_start"

func (s *riskStore) getBtcBlocksCoverageStart(ctx context.Context) (int64, bool, error) {
	raw, ok, err := s.getBtcKV(ctx, btcBlocksCoverageKey)
	if err != nil || !ok || raw == "" {
		return 0, false, err
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

func (s *riskStore) loadBtcBlocksRange(ctx context.Context, minH, maxH int64) (map[int64]btcBlockRow, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT height, ts, day, bits, tx_count, weight, subsidy_sats
FROM btc_blocks
WHERE height >= ? AND height <= ?`, minH, maxH)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]btcBlockRow)
	for rows.Next() {
		var r btcBlockRow
		if err := rows.Scan(&r.Height, &r.TS, &r.Date, &r.Bits, &r.TransactionCount, &r.Weight, &r.SubsidySats); err != nil {
			return nil, err
		}
		out[r.Height] = r
	}
	return out, rows.Err()
}

// applyBtcRollupBatch writes A2 block rows, A1+A2 derived daily rows, the
// initial blocks_coverage_start marker, and the updated sync_state cursor in a
// single SQLite transaction.
func (s *riskStore) applyBtcRollupBatch(ctx context.Context, batch btcRollupPayload, newFrom, cursor string, now time.Time) error {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Persist blocks_coverage_start on the first window if not already set.
	var existingCov string
	err = tx.QueryRowContext(ctx, `SELECT v FROM btc_kv WHERE k = ?`, btcBlocksCoverageKey).Scan(&existingCov)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO btc_kv (k, v) VALUES (?, ?)`,
			btcBlocksCoverageKey, strconv.FormatInt(batch.CoverageStart, 10),
		); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	blockStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_blocks (height, ts, day, bits, tx_count, weight, subsidy_sats)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(height) DO UPDATE SET
  ts = excluded.ts,
  day = excluded.day,
  bits = excluded.bits,
  tx_count = excluded.tx_count,
  weight = excluded.weight,
  subsidy_sats = excluded.subsidy_sats`)
	if err != nil {
		return err
	}
	defer blockStmt.Close()

	for _, b := range batch.Blocks {
		if _, err := blockStmt.ExecContext(ctx,
			b.Height, b.TS, b.Date, b.Bits, b.TransactionCount, b.Weight, b.SubsidySats,
		); err != nil {
			return err
		}
	}

	dayStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_onchain_daily (day, version, payload, synced_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  version = excluded.version,
  payload = excluded.payload,
  synced_at = excluded.synced_at`)
	if err != nil {
		return err
	}
	defer dayStmt.Close()

	at := now.UTC().Format(time.RFC3339)
	for _, d := range batch.Days {
		p, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := dayStmt.ExecContext(ctx, d.Date, btcRollupSchemaVersion, string(p), at); err != nil {
			return err
		}
	}

	var totalRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM btc_onchain_daily`).Scan(&totalRows); err != nil {
		return err
	}

	covFrom := newFrom
	if covFrom == "" {
		_ = tx.QueryRowContext(ctx, `SELECT coverage_from FROM sync_state WHERE source = ?`, btcOnchainJobID).Scan(&covFrom)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_state (source, coverage_from, cursor, last_ok_at, last_error, row_count)
VALUES (?, ?, ?, ?, '', ?)
ON CONFLICT(source) DO UPDATE SET
  coverage_from = CASE WHEN sync_state.coverage_from = '' THEN excluded.coverage_from ELSE sync_state.coverage_from END,
  cursor = excluded.cursor,
  last_ok_at = excluded.last_ok_at,
  last_error = '',
  row_count = excluded.row_count`,
		btcOnchainJobID, covFrom, cursor, at, totalRows,
	); err != nil {
		return err
	}

	return tx.Commit()
}
