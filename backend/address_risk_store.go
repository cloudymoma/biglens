package main

// SQLite store for the Address Risk tab. The file lives at a relative path
// (default data/security.db) resolved against the process working directory,
// like logs/. Pragmas go in the DSN: modernc applies _pragma to every pooled
// connection, whereas db.Exec("PRAGMA …") would only touch one.

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const riskSchema = `
CREATE TABLE IF NOT EXISTS stablecoin_events (
  tx_hash      TEXT NOT NULL,
  log_index    INTEGER NOT NULL,
  token        TEXT NOT NULL,
  action       TEXT NOT NULL,
  address      TEXT NOT NULL,
  amount       TEXT NOT NULL DEFAULT '',
  block_number INTEGER NOT NULL,
  block_time   TEXT NOT NULL,
  PRIMARY KEY (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS idx_se_address ON stablecoin_events(address);
CREATE TABLE IF NOT EXISTS list_entries (
  source    TEXT NOT NULL,
  address   TEXT NOT NULL,
  label     TEXT NOT NULL DEFAULT '',
  listed_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (source, address)
);
CREATE INDEX IF NOT EXISTS idx_le_address ON list_entries(address);
CREATE TABLE IF NOT EXISTS sync_state (
  source              TEXT PRIMARY KEY,
  coverage_from       TEXT NOT NULL DEFAULT '',
  cursor              TEXT NOT NULL DEFAULT '',
  last_ok_at          TEXT NOT NULL DEFAULT '',
  upstream_changed_at TEXT NOT NULL DEFAULT '',
  content_hash        TEXT NOT NULL DEFAULT '',
  pending_hash        TEXT NOT NULL DEFAULT '',
  pending_count       INTEGER NOT NULL DEFAULT 0,
  last_error          TEXT NOT NULL DEFAULT '',
  row_count           INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS backfill_meta (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  status         TEXT NOT NULL DEFAULT 'idle',
  since_date     TEXT NOT NULL DEFAULT '',
  through_date   TEXT NOT NULL DEFAULT '',
  events_stored  INTEGER NOT NULL DEFAULT 0,
  usdt_events    INTEGER NOT NULL DEFAULT 0,
  usdc_events    INTEGER NOT NULL DEFAULT 0,
  bytes_billed   INTEGER NOT NULL DEFAULT 0,
  started_at     TEXT NOT NULL DEFAULT '',
  completed_at   TEXT NOT NULL DEFAULT '',
  progress_label TEXT NOT NULL DEFAULT '',
  last_error     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tron_stablecoin_events (
  tx_hash      TEXT NOT NULL,
  log_index    INTEGER NOT NULL,
  token        TEXT NOT NULL,
  action       TEXT NOT NULL,
  address      TEXT NOT NULL,
  amount       TEXT NOT NULL DEFAULT '',
  block_number INTEGER NOT NULL,
  block_time   TEXT NOT NULL,
  PRIMARY KEY (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS idx_tse_address ON tron_stablecoin_events(address);
CREATE TABLE IF NOT EXISTS scam_lookalikes (
  chain      TEXT NOT NULL,
  lookalike  TEXT NOT NULL,
  imitated   TEXT NOT NULL,
  hits       INTEGER NOT NULL,
  victims    INTEGER NOT NULL,
  first_seen TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  PRIMARY KEY (chain, lookalike)
);
CREATE INDEX IF NOT EXISTS idx_sl_lookalike ON scam_lookalikes(lookalike);
CREATE TABLE IF NOT EXISTS scam_fake_tokens (
  chain      TEXT NOT NULL,
  contract   TEXT NOT NULL,
  symbol     TEXT NOT NULL,
  transfers  INTEGER NOT NULL,
  recipients INTEGER NOT NULL,
  first_seen TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  PRIMARY KEY (chain, contract)
);
CREATE TABLE IF NOT EXISTS scam_daily_stats (
  day    TEXT NOT NULL,
  metric TEXT NOT NULL,
  value  REAL NOT NULL,
  PRIMARY KEY (day, metric)
);`

type riskStore struct{ db *sql.DB }

type listEntry struct{ Address, Label, ListedAt string }

type listHit struct{ Source, Label, ListedAt string }

type syncState struct {
	Source, CoverageFrom, Cursor, LastOKAt, UpstreamChangedAt, ContentHash, PendingHash string
	PendingCount                                                                        int
	LastError                                                                           string
	RowCount                                                                            int
}

func openRiskStore(path string) (*riskStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(riskSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &riskStore{db: db}, nil
}

func (s *riskStore) Close() error { return s.db.Close() }

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

const syncStateCols = `source, coverage_from, cursor, last_ok_at, upstream_changed_at, content_hash,
  pending_hash, pending_count, last_error, row_count`

func scanSyncState(sc interface{ Scan(...any) error }) (syncState, error) {
	var st syncState
	err := sc.Scan(&st.Source, &st.CoverageFrom, &st.Cursor, &st.LastOKAt, &st.UpstreamChangedAt,
		&st.ContentHash, &st.PendingHash, &st.PendingCount, &st.LastError, &st.RowCount)
	return st, err
}

func (s *riskStore) getSyncState(ctx context.Context, source string) (syncState, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+syncStateCols+` FROM sync_state WHERE source = ?`, source)
	st, err := scanSyncState(row)
	if err == sql.ErrNoRows {
		return syncState{Source: source}, nil
	}
	if err != nil {
		return syncState{}, fmt.Errorf("read sync_state %s: %w", source, err)
	}
	return st, nil
}

func (s *riskStore) allSyncStates(ctx context.Context) (map[string]syncState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+syncStateCols+` FROM sync_state`)
	if err != nil {
		return nil, fmt.Errorf("read sync_state: %w", err)
	}
	defer rows.Close()
	out := map[string]syncState{}
	for rows.Next() {
		st, err := scanSyncState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sync_state: %w", err)
		}
		out[st.Source] = st
	}
	return out, rows.Err()
}

// ensureSyncRow makes the per-source row exist so UPDATEs always hit.
func ensureSyncRow(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, source string) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO sync_state(source) VALUES (?) ON CONFLICT(source) DO NOTHING`, source)
	return err
}

// replaceList swaps a snapshot list atomically. Entries must already be
// deduplicated by address (plain INSERT: a leftover PK conflict fails loudly).
func (s *riskStore) replaceList(ctx context.Context, source string, entries []listEntry, hash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := ensureSyncRow(ctx, tx, source); err != nil {
		return fmt.Errorf("ensure sync row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM list_entries WHERE source = ?`, source); err != nil {
		return fmt.Errorf("delete old %s: %w", source, err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO list_entries(source, address, label, listed_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, source, e.Address, e.Label, e.ListedAt); err != nil {
			return fmt.Errorf("insert %s %s: %w", source, e.Address, err)
		}
	}
	ts := fmtTime(now)
	_, err = tx.ExecContext(ctx, `UPDATE sync_state SET
	    upstream_changed_at = CASE WHEN content_hash = ? THEN upstream_changed_at ELSE ? END,
	    content_hash = ?, row_count = ?, last_ok_at = ?, pending_hash = '', pending_count = 0, last_error = ''
	  WHERE source = ?`, hash, ts, hash, len(entries), ts, source)
	if err != nil {
		return fmt.Errorf("update sync_state %s: %w", source, err)
	}
	return tx.Commit()
}

// markListUnchanged records a successful fetch whose content hash matched.
func (s *riskStore) markListUnchanged(ctx context.Context, source string, now time.Time) error {
	if err := ensureSyncRow(ctx, s.db, source); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sync_state SET last_ok_at = ?, last_error = '' WHERE source = ?`, fmtTime(now), source)
	return err
}

// markListRejected keeps the old rows and remembers the rejected snapshot so an
// identical payload on the next sync is accepted (legitimate large delistings).
func (s *riskStore) markListRejected(ctx context.Context, source, reason, pendingHash string, pendingCount int) error {
	if err := ensureSyncRow(ctx, s.db, source); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sync_state SET last_error = ?, pending_hash = ?, pending_count = ? WHERE source = ?`,
		reason, pendingHash, pendingCount, source)
	return err
}

func (s *riskStore) setSyncError(ctx context.Context, source, reason string) error {
	if err := ensureSyncRow(ctx, s.db, source); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sync_state SET last_error = ? WHERE source = ?`, reason, source)
	return err
}

func (s *riskStore) listHits(ctx context.Context, addr string) ([]listHit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, label, listed_at FROM list_entries WHERE address = ? ORDER BY source`, addr)
	if err != nil {
		return nil, fmt.Errorf("list hits: %w", err)
	}
	defer rows.Close()
	var out []listHit
	for rows.Next() {
		var h listHit
		if err := rows.Scan(&h.Source, &h.Label, &h.ListedAt); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// stablecoinSourceID is the internal sync_state key; lookups call the source
// "stablecoin" (spec §6).
const stablecoinSourceID = "stablecoin_logs"

// stablecoinEvent is one USDT/USDC blacklist log. Amount is set for destroy
// events only, as a decimal string in the token's smallest unit.
type stablecoinEvent struct {
	TxHash      string
	LogIndex    int64
	Token       string // "USDT" | "USDC"
	Action      string // "freeze" | "unfreeze" | "destroy"
	Address     string
	Amount      string
	BlockNumber int64
	BlockTime   string // RFC3339 UTC
}

// insertStablecoinEvents writes a batch and moves the coverage watermarks in
// the same transaction, so [coverage_from, cursor] never claims a day whose
// events are missing. newFrom/newCursor are YYYY-MM-DD, or "" to leave that
// watermark alone. cursor only grows and coverage_from only shrinks, which
// keeps a concurrent backfill CLI and service sync from moving either back.
// coverage_from cannot use MIN(): the empty string sorts before every date.
func (s *riskStore) insertStablecoinEvents(ctx context.Context, events []stablecoinEvent, newFrom, newCursor string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := ensureSyncRow(ctx, tx, stablecoinSourceID); err != nil {
		return fmt.Errorf("ensure sync row: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO stablecoin_events
	  (tx_hash, log_index, token, action, address, amount, block_number, block_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	for _, e := range events {
		if _, err := stmt.ExecContext(ctx, e.TxHash, e.LogIndex, e.Token, e.Action, e.Address, e.Amount, e.BlockNumber, e.BlockTime); err != nil {
			return fmt.Errorf("insert event %s:%d: %w", e.TxHash, e.LogIndex, err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE sync_state SET
	    coverage_from = CASE
	      WHEN ? = '' THEN coverage_from
	      WHEN coverage_from = '' OR ? < coverage_from THEN ?
	      ELSE coverage_from
	    END,
	    cursor = MAX(cursor, ?),
	    row_count = (SELECT COUNT(*) FROM stablecoin_events),
	    last_ok_at = ?, last_error = ''
	  WHERE source = ?`, newFrom, newFrom, newFrom, newCursor, fmtTime(now), stablecoinSourceID)
	if err != nil {
		return fmt.Errorf("update watermarks: %w", err)
	}
	return tx.Commit()
}

// latestStablecoinStateSQL is the single freeze-state rule (spec §6): the
// latest event per (token, address) decides, and freeze or destroy both mean
// "frozen" (USDT's destroyBlackFunds requires the address to be blacklisted
// and does not unblacklist it). %s is "" or a fixed WHERE clause — never
// user input. Lookups and the overview both use this query.
const latestStablecoinStateSQL = `
WITH latest AS (
  SELECT token, address, action, tx_hash, block_time,
         ROW_NUMBER() OVER (PARTITION BY token, address ORDER BY block_number DESC, log_index DESC) AS rn
  FROM stablecoin_events %s
)
SELECT token, address, action, tx_hash, block_time FROM latest WHERE rn = 1 ORDER BY token`

type stablecoinState struct{ Token, Address, Action, TxHash, BlockTime string }

// stablecoinStates returns the current freeze state per token for addr, or
// for every address when addr is "".
func (s *riskStore) stablecoinStates(ctx context.Context, addr string) ([]stablecoinState, error) {
	query, args := fmt.Sprintf(latestStablecoinStateSQL, ""), []any{}
	if addr != "" {
		query, args = fmt.Sprintf(latestStablecoinStateSQL, "WHERE address = ?"), []any{addr}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("stablecoin states: %w", err)
	}
	defer rows.Close()
	var out []stablecoinState
	for rows.Next() {
		var st stablecoinState
		if err := rows.Scan(&st.Token, &st.Address, &st.Action, &st.TxHash, &st.BlockTime); err != nil {
			return nil, fmt.Errorf("scan stablecoin state: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// destroyedTotals sums destroy amounts per token (smallest unit) for addr, or
// for every address when addr is "". Summed in Go: amounts are decimal text.
func (s *riskStore) destroyedTotals(ctx context.Context, addr string) (map[string]*big.Int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token, amount FROM stablecoin_events
	  WHERE action = 'destroy' AND (? = '' OR address = ?)`, addr, addr)
	if err != nil {
		return nil, fmt.Errorf("destroyed totals: %w", err)
	}
	defer rows.Close()
	out := map[string]*big.Int{}
	for rows.Next() {
		var token, amount string
		if err := rows.Scan(&token, &amount); err != nil {
			return nil, fmt.Errorf("scan destroyed: %w", err)
		}
		n, ok := new(big.Int).SetString(amount, 10)
		if !ok {
			return nil, fmt.Errorf("bad destroy amount %q", amount)
		}
		if out[token] == nil {
			out[token] = new(big.Int)
		}
		out[token].Add(out[token], n)
	}
	return out, rows.Err()
}

// riskPoolEntries returns the local Critical/Warning pool for association
// matching: OFAC and MEW entries plus currently frozen USDT/USDC addresses
// (the same freeze-state rule as lookups; unfrozen addresses do not count).
func (s *riskStore) riskPoolEntries(ctx context.Context) (riskPool, error) {
	pool := riskPool{}
	rows, err := s.db.QueryContext(ctx, `SELECT address, source FROM list_entries ORDER BY source DESC`)
	if err != nil {
		return nil, fmt.Errorf("pool lists: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var addr, source string
		if err := rows.Scan(&addr, &source); err != nil {
			return nil, fmt.Errorf("scan pool: %w", err)
		}
		pool[addr] = append(pool[addr], source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	states, err := s.stablecoinStates(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, st := range states {
		if st.Action == "unfreeze" || slices.Contains(pool[st.Address], "stablecoin") {
			continue
		}
		pool[st.Address] = append(pool[st.Address], "stablecoin")
	}
	return pool, nil
}

// riskPoolForAddresses queries only the specified addresses against
// list_entries and (for Ethereum) stablecoin_events using their address
// indexes, avoiding a full-table scan on every cached ETH Whales request.
func (s *riskStore) riskPoolForAddresses(ctx context.Context, chain string, addrs []string) (riskPool, error) {
	pool := riskPool{}
	if len(addrs) == 0 {
		return pool, nil
	}
	placeholders := make([]string, len(addrs))
	args := make([]any, len(addrs))
	for i, a := range addrs {
		placeholders[i] = "?"
		args[i] = a
	}
	want := riskSourcesFor(chain)
	inClause := "WHERE address IN (" + strings.Join(placeholders, ",") + ")"
	listWhere := inClause
	if !slices.Contains(want, "mew_darklist") {
		listWhere += " AND source = 'ofac'"
	}

	listQuery := `SELECT address, source FROM list_entries ` + listWhere + ` ORDER BY source DESC`
	rows, err := s.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("pool lists subset: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var addr, source string
		if err := rows.Scan(&addr, &source); err != nil {
			return nil, fmt.Errorf("scan pool subset: %w", err)
		}
		pool[addr] = append(pool[addr], source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if slices.Contains(want, "stablecoin") {
		stQuery := fmt.Sprintf(latestStablecoinStateSQL, inClause)
		stRows, err := s.db.QueryContext(ctx, stQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("stablecoin states subset: %w", err)
		}
		defer stRows.Close()
		for stRows.Next() {
			var st stablecoinState
			if err := stRows.Scan(&st.Token, &st.Address, &st.Action, &st.TxHash, &st.BlockTime); err != nil {
				return nil, fmt.Errorf("scan stablecoin state subset: %w", err)
			}
			if st.Action == "unfreeze" || slices.Contains(pool[st.Address], "stablecoin") {
				continue
			}
			pool[st.Address] = append(pool[st.Address], "stablecoin")
		}
		if err := stRows.Err(); err != nil {
			return nil, err
		}
	}

	if slices.Contains(want, "tron_stablecoin") {
		trQuery := fmt.Sprintf(latestTronStablecoinStateSQL, inClause)
		trRows, err := s.db.QueryContext(ctx, trQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("tron stablecoin states subset: %w", err)
		}
		defer trRows.Close()
		for trRows.Next() {
			var st stablecoinState
			if err := trRows.Scan(&st.Token, &st.Address, &st.Action, &st.TxHash, &st.BlockTime); err != nil {
				return nil, fmt.Errorf("scan tron stablecoin state subset: %w", err)
			}
			if st.Action == "unfreeze" || slices.Contains(pool[st.Address], "tron_stablecoin") {
				continue
			}
			pool[st.Address] = append(pool[st.Address], "tron_stablecoin")
		}
		if err := trRows.Err(); err != nil {
			return nil, err
		}
	}

	if slices.Contains(want, "scam_lookalikes") {
		lkHits, err := s.lookalikeHits(ctx, chain, addrs)
		if err != nil {
			return nil, err
		}
		for addr := range lkHits {
			if !slices.Contains(pool[addr], "scam_lookalikes") {
				pool[addr] = append(pool[addr], "scam_lookalikes")
			}
		}
	}
	return pool, nil
}

type backfillMeta struct {
	Status, SinceDate, ThroughDate             string
	EventsStored, USDTEvents, USDCEvents       int
	BytesBilled                                int64
	StartedAt, CompletedAt, ProgressLabel, Err string
}

func (s *riskStore) getBackfillMeta(ctx context.Context) (backfillMeta, error) {
	row := s.db.QueryRowContext(ctx, `SELECT status, since_date, through_date, events_stored,
	  usdt_events, usdc_events, bytes_billed, started_at, completed_at, progress_label, last_error
	  FROM backfill_meta WHERE id = 1`)
	var m backfillMeta
	err := row.Scan(&m.Status, &m.SinceDate, &m.ThroughDate, &m.EventsStored,
		&m.USDTEvents, &m.USDCEvents, &m.BytesBilled, &m.StartedAt, &m.CompletedAt, &m.ProgressLabel, &m.Err)
	if err == sql.ErrNoRows {
		return backfillMeta{Status: "idle"}, nil
	}
	if err != nil {
		return backfillMeta{}, fmt.Errorf("read backfill_meta: %w", err)
	}
	return m, nil
}

func (s *riskStore) saveBackfillMeta(ctx context.Context, m backfillMeta) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backfill_meta
	  (id, status, since_date, through_date, events_stored, usdt_events, usdc_events, bytes_billed, started_at, completed_at, progress_label, last_error)
	  VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	  ON CONFLICT(id) DO UPDATE SET
	    status = excluded.status,
	    since_date = excluded.since_date,
	    through_date = excluded.through_date,
	    events_stored = excluded.events_stored,
	    usdt_events = excluded.usdt_events,
	    usdc_events = excluded.usdc_events,
	    bytes_billed = excluded.bytes_billed,
	    started_at = excluded.started_at,
	    completed_at = excluded.completed_at,
	    progress_label = excluded.progress_label,
	    last_error = excluded.last_error`,
		m.Status, m.SinceDate, m.ThroughDate, m.EventsStored, m.USDTEvents, m.USDCEvents,
		m.BytesBilled, m.StartedAt, m.CompletedAt, m.ProgressLabel, m.Err)
	return err
}

func (s *riskStore) stablecoinEventCounts(ctx context.Context) (total, usdt, usdc int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token, COUNT(*) FROM stablecoin_events GROUP BY token`)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count stablecoin_events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tok string
		var n int
		if err := rows.Scan(&tok, &n); err != nil {
			return 0, 0, 0, err
		}
		total += n
		switch tok {
		case "USDT":
			usdt += n
		case "USDC":
			usdc += n
		}
	}
	return total, usdt, usdc, rows.Err()
}
