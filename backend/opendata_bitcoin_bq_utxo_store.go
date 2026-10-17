package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	btcBaselinePendingKey = "baseline_pending"
	btcLastBaselineAtKey  = "last_baseline_at"
	btcBaselineMetaKey    = "baseline_meta"
)

// commitBtcBaseline writes the new UTXO baseline generation, sets baseline_pending,
// updates last_baseline_at and baseline_meta, and settles the reservation in one
// atomic SQLite transaction. It never writes btc_utxo_state or holder rows.
func (s *riskStore) commitBtcBaseline(
	ctx context.Context,
	budget *btcBudget,
	settings btcSettings,
	resID string,
	asOf string,
	rows []btcUTXOBaselineRow,
	billed int64,
	now time.Time,
	beforeCommit func() error,
) (BtcBaselineStatus, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return BtcBaselineStatus{}, err
	}

	// Compute reconciliation inputs from stored blocks and onchain daily rows (< asOf).
	maxHeight, unclaimedSats, opretSats, _ := s.loadBtcReconciliationInputs(ctx, asOf)

	var totalSats int64
	for _, r := range rows {
		totalSats += r.Sats
	}
	expectedSats, deltaPct, suspect := reconcileBtcBaseline(totalSats, maxHeight, unclaimedSats, opretSats)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BtcBaselineStatus{}, err
	}
	defer tx.Rollback()

	var maxGen sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(generation) FROM btc_utxo_baseline`).Scan(&maxGen); err != nil {
		return BtcBaselineStatus{}, err
	}
	newGen := maxGen.Int64 + 1

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_utxo_baseline (generation, create_day, sats, utxos)
VALUES (?, ?, ?, ?)`)
	if err != nil {
		return BtcBaselineStatus{}, err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, newGen, r.CreateDay, r.Sats, r.UTXOs); err != nil {
			return BtcBaselineStatus{}, err
		}
	}

	nowStr := now.UTC().Format(time.RFC3339)
	pend := btcPendingBaseline{
		Generation: newGen,
		AsOf:       asOf,
		BuiltAt:    nowStr,
	}
	pendJSON, err := json.Marshal(pend)
	if err != nil {
		return BtcBaselineStatus{}, err
	}

	st := BtcBaselineStatus{
		Available:       true,
		Generation:      newGen,
		AsOf:            asOf,
		BuiltAt:         nowStr,
		Pending:         true,
		BaselineSats:    totalSats,
		BaselineBTC:     float64(totalSats) / 1e8,
		ExpectedSats:    expectedSats,
		DeltaPct:        deltaPct,
		Suspect:         suspect,
		AllowUI:         settings.BaselineAllowUI,
		MinIntervalDays: int(settings.BaselineMinInterval.Hours() / 24),
		LastBaselineAt:  nowStr,
		NextAllowedAt:   now.UTC().Add(settings.BaselineMinInterval).Format(time.RFC3339),
		CLICommand:      "./biglens-server --bitcoin-utxo-baseline",
	}
	metaJSON, err := json.Marshal(st)
	if err != nil {
		return BtcBaselineStatus{}, err
	}

	for _, kv := range [][2]string{
		{btcBaselinePendingKey, string(pendJSON)},
		{btcLastBaselineAtKey, nowStr},
		{btcBaselineMetaKey, string(metaJSON)},
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO btc_kv (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, kv[0], kv[1]); err != nil {
			return BtcBaselineStatus{}, err
		}
	}

	if budget != nil && resID != "" {
		if err := budget.settleTx(ctx, tx, resID, billed); err != nil {
			return BtcBaselineStatus{}, err
		}
	}

	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return BtcBaselineStatus{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return BtcBaselineStatus{}, err
	}
	return st, nil
}

func (s *riskStore) loadBtcReconciliationInputs(ctx context.Context, asOf string) (maxHeight, unclaimedSats, opretSats int64, err error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return 0, 0, 0, err
	}
	var mh sql.NullInt64
	if qErr := s.db.QueryRowContext(ctx,
		`SELECT MAX(height) FROM btc_blocks WHERE day < ?`, asOf,
	).Scan(&mh); qErr == nil && mh.Valid {
		maxHeight = mh.Int64
	}
	rows, qErr := s.db.QueryContext(ctx,
		`SELECT payload FROM btc_onchain_daily WHERE day < ?`, asOf,
	)
	if qErr != nil {
		return maxHeight, 0, 0, qErr
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return maxHeight, unclaimedSats, opretSats, err
		}
		var d BtcOnchainDay
		if json.Unmarshal([]byte(p), &d) == nil {
			if d.ScheduledIssuanceSats > d.ClaimedIssuanceSats {
				unclaimedSats += d.ScheduledIssuanceSats - d.ClaimedIssuanceSats
			}
			opretSats += d.OpretValueSats
		}
	}
	return maxHeight, unclaimedSats, opretSats, rows.Err()
}

func (s *riskStore) getBtcPendingBaseline(ctx context.Context) (*btcPendingBaseline, bool, error) {
	raw, ok, err := s.getBtcKV(ctx, btcBaselinePendingKey)
	if err != nil || !ok || raw == "" {
		return nil, false, err
	}
	var pend btcPendingBaseline
	if err := json.Unmarshal([]byte(raw), &pend); err != nil {
		return nil, false, err
	}
	return &pend, true, nil
}

func (s *riskStore) loadBtcBaselineGeneration(ctx context.Context, gen int64) (map[string]int64, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT create_day, sats FROM btc_utxo_baseline WHERE generation = ?`, gen,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var cd string
		var sats int64
		if err := rows.Scan(&cd, &sats); err != nil {
			return nil, err
		}
		out[cd] = sats
	}
	return out, rows.Err()
}

func (s *riskStore) latestBtcBaselineGeneration(ctx context.Context) (int64, bool, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return 0, false, err
	}
	var gen sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(generation) FROM btc_utxo_baseline`).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) || !gen.Valid || gen.Int64 <= 0 {
		return 0, false, nil
	}
	return gen.Int64, err == nil, err
}

// pruneBtcBaselineGenerations keeps only the last two baseline generations.
func (s *riskStore) pruneBtcBaselineGenerations(ctx context.Context, currentGen int64) error {
	if currentGen <= 2 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM btc_utxo_baseline WHERE generation < ?`, currentGen-1,
	)
	return err
}
