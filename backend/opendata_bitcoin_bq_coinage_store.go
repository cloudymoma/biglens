package main

import (
	"context"
	"encoding/json"
	"time"

	"cloud.google.com/go/civil"
)

const btcUTXOStateAsOfKey = "utxo_state_as_of"

func (s *riskStore) getBtcUTXOStateAsOf(ctx context.Context) (string, error) {
	if s == nil {
		return "", nil
	}
	v, _, err := s.getBtcKV(ctx, btcUTXOStateAsOfKey)
	return v, err
}

// applyBtcCoinAgeBatch writes matrix rows, whale revivals, price-independent
// coin-age daily rows, and the sync_state cursor in a single SQLite transaction.
// Daily and whale tables are upsert-only and never delete historical rows.
func (s *riskStore) applyBtcCoinAgeBatch(ctx context.Context, batch btcCoinAgePayload, newFrom, cursor string, now time.Time) error {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	matrixStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_spent_by_age (spend_day, create_day, sats, inputs)
VALUES (?, ?, ?, ?)
ON CONFLICT(spend_day, create_day) DO UPDATE SET
  sats = excluded.sats,
  inputs = excluded.inputs`)
	if err != nil {
		return err
	}
	defer matrixStmt.Close()

	for _, m := range batch.Matrix {
		if _, err := matrixStmt.ExecContext(ctx, m.SpendDay, m.CreateDay, m.Sats, m.Inputs); err != nil {
			return err
		}
	}

	whaleStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_whale_revivals (spend_tx, create_day, spend_day, spent_at, sats, inputs)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(spend_tx, create_day) DO UPDATE SET
  spend_day = excluded.spend_day,
  spent_at = excluded.spent_at,
  sats = excluded.sats,
  inputs = excluded.inputs`)
	if err != nil {
		return err
	}
	defer whaleStmt.Close()

	for _, w := range batch.Whales {
		if _, err := whaleStmt.ExecContext(ctx, w.SpendTx, w.CreateDay, w.SpendDay, w.SpentAt, w.Sats, w.Inputs); err != nil {
			return err
		}
	}

	dayStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_coinage_daily (day, version, payload, computed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  version = excluded.version,
  payload = excluded.payload,
  computed_at = excluded.computed_at`)
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
		if _, err := dayStmt.ExecContext(ctx, d.Date, btcCoinAgeSchemaVersion, string(p), at); err != nil {
			return err
		}
	}

	var totalRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM btc_coinage_daily`).Scan(&totalRows); err != nil {
		return err
	}

	covFrom := newFrom
	if covFrom == "" {
		_ = tx.QueryRowContext(ctx, `SELECT coverage_from FROM sync_state WHERE source = ?`, btcCoinageJobID).Scan(&covFrom)
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
		btcCoinageJobID, covFrom, cursor, at, totalRows,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *riskStore) loadBtcSpentMatrixByRange(ctx context.Context, fromSpendDay, toSpendDay string) (map[string]map[string]int64, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	q := `SELECT spend_day, create_day, sats FROM btc_spent_by_age WHERE 1=1`
	var args []any
	if fromSpendDay != "" {
		q += ` AND spend_day >= ?`
		args = append(args, fromSpendDay)
	}
	if toSpendDay != "" {
		q += ` AND spend_day <= ?`
		args = append(args, toSpendDay)
	}
	q += ` ORDER BY spend_day, create_day`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]map[string]int64)
	for rows.Next() {
		var sd, cd string
		var sats int64
		if err := rows.Scan(&sd, &cd, &sats); err != nil {
			return nil, err
		}
		m := out[sd]
		if m == nil {
			m = make(map[string]int64)
			out[sd] = m
		}
		m[cd] += sats
	}
	return out, rows.Err()
}

// pruneBtcCoinAge deletes btc_spent_by_age matrix rows older than the retention
// horizon (end - retentionDays) AND no later than stateAsOf(). It never deletes
// rows from btc_coinage_daily, btc_holder_daily, or btc_whale_revivals.
func pruneBtcCoinAge(store *riskStore, retentionDays int, stateAsOf func() string) func(context.Context, civil.Date) error {
	return func(ctx context.Context, end civil.Date) error {
		if store == nil || store.db == nil || retentionDays <= 0 {
			return nil
		}
		if err := store.ensureBtcBQSchema(ctx); err != nil {
			return err
		}
		horizon := end.AddDays(-retentionDays).String()
		var asOf string
		if stateAsOf != nil {
			asOf = stateAsOf()
		}
		if asOf != "" {
			_, err := store.db.ExecContext(ctx,
				`DELETE FROM btc_spent_by_age WHERE spend_day < ? AND spend_day <= ?`,
				horizon, asOf,
			)
			return err
		}
		_, err := store.db.ExecContext(ctx,
			`DELETE FROM btc_spent_by_age WHERE spend_day < ?`,
			horizon,
		)
		return err
	}
}
