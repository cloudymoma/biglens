package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"cloud.google.com/go/civil"
)

const (
	tronStablecoinSourceID = "tron_stablecoin_logs"
	scamEthSourceID        = "scam_eth"
	scamTronSourceID       = "scam_tron"
	scamBTCSourceID        = "scam_btc"

	scamStatsRetentionDays = 400
)

var errAlreadyApplied = errors.New("scam radar: day already applied")

type lookalikeRow struct {
	Chain     string
	Lookalike string
	Imitated  string
	Hits      int
	Victims   int
	FirstSeen string
	LastSeen  string
}

type fakeTokenRow struct {
	Chain      string
	Contract   string
	Symbol     string
	Transfers  int
	Recipients int
	FirstSeen  string
	LastSeen   string
}

type scamDayBatch struct {
	Day        string
	Lookalikes []lookalikeRow
	FakeTokens []fakeTokenRow
	Stats      map[string]float64
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scamSourceForChain(chain string) string {
	switch chain {
	case "tron":
		return scamTronSourceID
	case "btc":
		return scamBTCSourceID
	default:
		return scamEthSourceID
	}
}

func normalizeScamAddr(chain, addr string) string {
	s := strings.TrimSpace(addr)
	if chain == "tron" {
		return s
	}
	return strings.ToLower(s)
}

func readCursorTx(ctx context.Context, ex sqlExecer, source string) (string, error) {
	if err := ensureSyncRow(ctx, ex, source); err != nil {
		return "", fmt.Errorf("ensure sync row %s: %w", source, err)
	}
	var cur string
	if err := ex.QueryRowContext(ctx, `SELECT cursor FROM sync_state WHERE source = ?`, source).Scan(&cur); err != nil {
		return "", fmt.Errorf("read cursor %s: %w", source, err)
	}
	return cur, nil
}

func updateSyncWatermarksTx(ctx context.Context, ex sqlExecer, source, newFrom, cursor string, rowCount int, now time.Time) error {
	_, err := ex.ExecContext(ctx, `UPDATE sync_state SET
	    coverage_from = CASE
	      WHEN ? = '' THEN coverage_from
	      WHEN coverage_from = '' OR ? < coverage_from THEN ?
	      ELSE coverage_from
	    END,
	    cursor = MAX(cursor, ?),
	    row_count = ?,
	    last_ok_at = ?, last_error = ''
	  WHERE source = ?`, newFrom, newFrom, newFrom, cursor, rowCount, fmtTime(now), source)
	if err != nil {
		return fmt.Errorf("update watermarks %s: %w", source, err)
	}
	return nil
}

// insertTronStablecoinEvents writes TRON USDT freeze/unfreeze/destroy events
// and advances the tron_stablecoin_logs watermark in a single transaction.
func (s *riskStore) insertTronStablecoinEvents(ctx context.Context, evs []stablecoinEvent, newFrom, cursor string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	cur, err := readCursorTx(ctx, tx, tronStablecoinSourceID)
	if err != nil {
		return err
	}
	if cur != "" && cursor != "" && cursor <= cur {
		return errAlreadyApplied
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO tron_stablecoin_events
	  (tx_hash, log_index, token, action, address, amount, block_number, block_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert tron_stablecoin_events: %w", err)
	}
	defer stmt.Close()

	dayCounts := map[string]map[string]float64{}
	for _, e := range evs {
		if e.TxHash == "" || e.Address == "" || e.Token == "" || e.Action == "" {
			return fmt.Errorf("invalid tron stablecoin event %+v", e)
		}
		if _, err := stmt.ExecContext(ctx, e.TxHash, e.LogIndex, e.Token, e.Action, e.Address, e.Amount, e.BlockNumber, e.BlockTime); err != nil {
			return fmt.Errorf("insert tron event %s:%d: %w", e.TxHash, e.LogIndex, err)
		}
		if len(e.BlockTime) >= 10 {
			d := e.BlockTime[:10]
			if dayCounts[d] == nil {
				dayCounts[d] = map[string]float64{
					"tron_usdt_freezes":   0,
					"tron_usdt_unfreezes": 0,
					"tron_usdt_destroys":  0,
				}
			}
			switch e.Action {
			case "freeze":
				dayCounts[d]["tron_usdt_freezes"]++
			case "unfreeze":
				dayCounts[d]["tron_usdt_unfreezes"]++
			case "destroy":
				dayCounts[d]["tron_usdt_destroys"]++
			}
		}
	}

	for d, m := range dayCounts {
		if err := putDailyStatsTx(ctx, tx, d, m); err != nil {
			return err
		}
	}

	var totalRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tron_stablecoin_events`).Scan(&totalRows); err != nil {
		return fmt.Errorf("count tron_stablecoin_events: %w", err)
	}
	if err := updateSyncWatermarksTx(ctx, tx, tronStablecoinSourceID, newFrom, cursor, totalRows, now); err != nil {
		return err
	}
	return tx.Commit()
}

const latestTronStablecoinStateSQL = `
WITH latest AS (
  SELECT token, address, action, tx_hash, block_time,
         ROW_NUMBER() OVER (PARTITION BY token, address ORDER BY block_number DESC, log_index DESC) AS rn
  FROM tron_stablecoin_events %s
)
SELECT token, address, action, tx_hash, block_time FROM latest WHERE rn = 1 ORDER BY token`

// tronStablecoinStates returns the current freeze state per token for a TRON
// base58 address (exact case), or for every address when addr is "".
func (s *riskStore) tronStablecoinStates(ctx context.Context, addr string) ([]stablecoinState, error) {
	query, args := fmt.Sprintf(latestTronStablecoinStateSQL, ""), []any{}
	if addr != "" {
		query, args = fmt.Sprintf(latestTronStablecoinStateSQL, "WHERE address = ?"), []any{addr}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tron stablecoin states: %w", err)
	}
	defer rows.Close()
	var out []stablecoinState
	for rows.Next() {
		var st stablecoinState
		if err := rows.Scan(&st.Token, &st.Address, &st.Action, &st.TxHash, &st.BlockTime); err != nil {
			return nil, fmt.Errorf("scan tron stablecoin state: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// tronDestroyedTotals sums destroy amounts per token (smallest unit) for addr,
// or for every address when addr is "".
func (s *riskStore) tronDestroyedTotals(ctx context.Context, addr string) (map[string]*big.Int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token, amount FROM tron_stablecoin_events
	  WHERE action = 'destroy' AND (? = '' OR address = ?)`, addr, addr)
	if err != nil {
		return nil, fmt.Errorf("tron destroyed totals: %w", err)
	}
	defer rows.Close()
	out := map[string]*big.Int{}
	for rows.Next() {
		var token, amount string
		if err := rows.Scan(&token, &amount); err != nil {
			return nil, fmt.Errorf("scan tron destroyed: %w", err)
		}
		n, ok := new(big.Int).SetString(amount, 10)
		if !ok {
			return nil, fmt.Errorf("bad tron destroy amount %q", amount)
		}
		if out[token] == nil {
			out[token] = new(big.Int)
		}
		out[token].Add(out[token], n)
	}
	return out, rows.Err()
}

func upsertLookalikesTx(ctx context.Context, ex sqlExecer, chain string, rows []lookalikeRow, day string) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := ex.PrepareContext(ctx, `INSERT INTO scam_lookalikes
	  (chain, lookalike, imitated, hits, victims, first_seen, last_seen)
	  VALUES (?, ?, ?, ?, ?, ?, ?)
	  ON CONFLICT(chain, lookalike) DO UPDATE SET
	    imitated = CASE WHEN excluded.last_seen >= scam_lookalikes.last_seen THEN excluded.imitated ELSE scam_lookalikes.imitated END,
	    hits = scam_lookalikes.hits + excluded.hits,
	    victims = scam_lookalikes.victims + excluded.victims,
	    first_seen = CASE WHEN scam_lookalikes.first_seen = '' OR excluded.first_seen < scam_lookalikes.first_seen THEN excluded.first_seen ELSE scam_lookalikes.first_seen END,
	    last_seen = CASE WHEN excluded.last_seen > scam_lookalikes.last_seen THEN excluded.last_seen ELSE scam_lookalikes.last_seen END`)
	if err != nil {
		return fmt.Errorf("prepare upsertLookalikes: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		lk := normalizeScamAddr(chain, r.Lookalike)
		im := normalizeScamAddr(chain, r.Imitated)
		first := r.FirstSeen
		if first == "" {
			first = day
		}
		last := r.LastSeen
		if last == "" {
			last = day
		}
		if lk == "" || im == "" || r.Hits <= 0 || r.Victims <= 0 || first == "" || last == "" {
			return fmt.Errorf("invalid lookalike row for %s: %+v", chain, r)
		}
		if _, err := stmt.ExecContext(ctx, chain, lk, im, r.Hits, r.Victims, first, last); err != nil {
			return fmt.Errorf("upsert lookalike %s %s: %w", chain, lk, err)
		}
	}
	return nil
}

func (s *riskStore) upsertLookalikes(ctx context.Context, chain string, rows []lookalikeRow, day string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	cur, err := readCursorTx(ctx, tx, scamSourceForChain(chain))
	if err != nil {
		return err
	}
	if cur != "" && day != "" && day <= cur {
		return errAlreadyApplied
	}
	if err := upsertLookalikesTx(ctx, tx, chain, rows, day); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertFakeTokensTx(ctx context.Context, ex sqlExecer, chain string, rows []fakeTokenRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := ex.PrepareContext(ctx, `INSERT INTO scam_fake_tokens
	  (chain, contract, symbol, transfers, recipients, first_seen, last_seen)
	  VALUES (?, ?, ?, ?, ?, ?, ?)
	  ON CONFLICT(chain, contract) DO UPDATE SET
	    symbol = CASE WHEN excluded.last_seen >= scam_fake_tokens.last_seen AND excluded.symbol != '' THEN excluded.symbol ELSE scam_fake_tokens.symbol END,
	    transfers = scam_fake_tokens.transfers + excluded.transfers,
	    recipients = scam_fake_tokens.recipients + excluded.recipients,
	    first_seen = CASE WHEN scam_fake_tokens.first_seen = '' OR excluded.first_seen < scam_fake_tokens.first_seen THEN excluded.first_seen ELSE scam_fake_tokens.first_seen END,
	    last_seen = CASE WHEN excluded.last_seen > scam_fake_tokens.last_seen THEN excluded.last_seen ELSE scam_fake_tokens.last_seen END`)
	if err != nil {
		return fmt.Errorf("prepare upsertFakeTokens: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		contract := normalizeScamAddr(chain, r.Contract)
		sym := strings.TrimSpace(r.Symbol)
		first := r.FirstSeen
		last := r.LastSeen
		if first == "" {
			first = last
		}
		if last == "" {
			last = first
		}
		if contract == "" || sym == "" || r.Transfers <= 0 || r.Recipients <= 0 || first == "" || last == "" {
			return fmt.Errorf("invalid fake token row for %s: %+v", chain, r)
		}
		if _, err := stmt.ExecContext(ctx, chain, contract, sym, r.Transfers, r.Recipients, first, last); err != nil {
			return fmt.Errorf("upsert fake token %s %s: %w", chain, contract, err)
		}
	}
	return nil
}

func (s *riskStore) upsertFakeTokens(ctx context.Context, chain string, rows []fakeTokenRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	cur, err := readCursorTx(ctx, tx, scamSourceForChain(chain))
	if err != nil {
		return err
	}
	if cur != "" {
		for _, r := range rows {
			d := r.LastSeen
			if d == "" {
				d = r.FirstSeen
			}
			if d != "" && d <= cur {
				return errAlreadyApplied
			}
		}
	}
	if err := upsertFakeTokensTx(ctx, tx, chain, rows); err != nil {
		return err
	}
	return tx.Commit()
}

func putDailyStatsTx(ctx context.Context, ex sqlExecer, day string, m map[string]float64) error {
	if len(m) == 0 {
		return nil
	}
	if day == "" {
		return errors.New("putDailyStats: empty day")
	}
	stmt, err := ex.PrepareContext(ctx, `INSERT INTO scam_daily_stats(day, metric, value)
	  VALUES (?, ?, ?)
	  ON CONFLICT(day, metric) DO UPDATE SET value = excluded.value`)
	if err != nil {
		return fmt.Errorf("prepare putDailyStats: %w", err)
	}
	defer stmt.Close()

	for metric, val := range m {
		if metric == "" {
			return errors.New("putDailyStats: empty metric")
		}
		if _, err := stmt.ExecContext(ctx, day, metric, val); err != nil {
			return fmt.Errorf("put daily stat %s %s: %w", day, metric, err)
		}
	}
	return nil
}

func (s *riskStore) putDailyStats(ctx context.Context, day string, m map[string]float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := putDailyStatsTx(ctx, tx, day, m); err != nil {
		return err
	}
	return tx.Commit()
}

// applyScamBatch writes a window of daily lookalikes, fake tokens, and daily
// stats and advances the source cursor atomically in a single transaction. If
// any day in the batch is <= the current cursor (or cursor <= currentCursor),
// it aborts with errAlreadyApplied so cumulative counters are never doubled.
func (s *riskStore) applyScamBatch(ctx context.Context, source, chain string, days []scamDayBatch, newFrom, cursor string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	cur, err := readCursorTx(ctx, tx, source)
	if err != nil {
		return err
	}
	if cur != "" && cursor != "" && cursor <= cur {
		return errAlreadyApplied
	}
	for _, d := range days {
		if d.Day == "" {
			return errors.New("applyScamBatch: empty day")
		}
		if cur != "" && d.Day <= cur {
			return errAlreadyApplied
		}
		if cursor != "" && d.Day > cursor {
			return fmt.Errorf("applyScamBatch: day %s exceeds cursor %s", d.Day, cursor)
		}
	}

	for _, d := range days {
		if err := upsertLookalikesTx(ctx, tx, chain, d.Lookalikes, d.Day); err != nil {
			return err
		}
		if err := upsertFakeTokensTx(ctx, tx, chain, d.FakeTokens); err != nil {
			return err
		}
		if err := putDailyStatsTx(ctx, tx, d.Day, d.Stats); err != nil {
			return err
		}
	}

	var rowCount int
	switch source {
	case scamEthSourceID:
		var n1, n2 int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scam_lookalikes WHERE chain = 'eth'`).Scan(&n1); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scam_fake_tokens WHERE chain = 'eth'`).Scan(&n2); err != nil {
			return err
		}
		rowCount = n1 + n2
	case scamTronSourceID:
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scam_lookalikes WHERE chain = 'tron'`).Scan(&rowCount); err != nil {
			return err
		}
	case scamBTCSourceID:
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT day) FROM scam_daily_stats WHERE metric = 'btc_txs'`).Scan(&rowCount); err != nil {
			return err
		}
	}

	if err := updateSyncWatermarksTx(ctx, tx, source, newFrom, cursor, rowCount, now); err != nil {
		return err
	}
	return tx.Commit()
}

// lookalikeHits queries scam_lookalikes for addrs in a single IN (...) query.
// EVM chains (eth/arb/op/base) all query the "eth" lookalike corpus because
// EVM EOAs share the same address space across chains (spec P2.2).
func (s *riskStore) lookalikeHits(ctx context.Context, chain string, addrs []string) (map[string]lookalikeRow, error) {
	out := map[string]lookalikeRow{}
	if len(addrs) == 0 {
		return out, nil
	}
	lookupChain := chain
	if info, ok := chains[chain]; ok && info.Family == familyEVM {
		lookupChain = "eth"
	} else if chain == "" {
		lookupChain = "eth"
	}

	placeholders := make([]string, len(addrs))
	args := make([]any, 0, 1+len(addrs))
	args = append(args, lookupChain)
	for i, a := range addrs {
		placeholders[i] = "?"
		args = append(args, normalizeScamAddr(lookupChain, a))
	}

	query := `SELECT chain, lookalike, imitated, hits, victims, first_seen, last_seen
	  FROM scam_lookalikes
	  WHERE chain = ? AND lookalike IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookalike hits: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r lookalikeRow
		if err := rows.Scan(&r.Chain, &r.Lookalike, &r.Imitated, &r.Hits, &r.Victims, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, fmt.Errorf("scan lookalike hit: %w", err)
		}
		out[r.Lookalike] = r
	}
	return out, rows.Err()
}

// fakeTokenSet returns the set of known counterfeit token contracts for chain.
func (s *riskStore) fakeTokenSet(ctx context.Context, chain string) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT contract FROM scam_fake_tokens WHERE chain = ?`, chain)
	if err != nil {
		return nil, fmt.Errorf("fake token set: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan fake token: %w", err)
		}
		out[c] = true
	}
	return out, rows.Err()
}

// pruneScam deletes scam_lookalikes and scam_fake_tokens rows whose last_seen
// is older than today - retentionDays, and scam_daily_stats rows older than
// 400 days (spec P2.2).
func (s *riskStore) pruneScam(ctx context.Context, retentionDays int, today civil.Date) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prune: %w", err)
	}
	defer tx.Rollback()

	if retentionDays > 0 {
		cutoff := today.AddDays(-retentionDays).String()
		if _, err := tx.ExecContext(ctx, `DELETE FROM scam_lookalikes WHERE last_seen < ?`, cutoff); err != nil {
			return fmt.Errorf("prune scam_lookalikes: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scam_fake_tokens WHERE last_seen < ?`, cutoff); err != nil {
			return fmt.Errorf("prune scam_fake_tokens: %w", err)
		}
	}
	statsCutoff := today.AddDays(-scamStatsRetentionDays).String()
	if _, err := tx.ExecContext(ctx, `DELETE FROM scam_daily_stats WHERE day < ?`, statsCutoff); err != nil {
		return fmt.Errorf("prune scam_daily_stats: %w", err)
	}
	return tx.Commit()
}
