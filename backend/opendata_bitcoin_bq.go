package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

const (
	btcOnchainJobID  = "btc_onchain"
	btcCoinageJobID  = "btc_coinage"
	btcBaselineJobID = "btc_utxo_baseline"

	// Default dry-run upper bounds for feasibility estimates (bytes).
	btcEstRollup1dBytes     int64 = 697_932_186     // 0.65 GiB (0.61 GiB A1 + 0.04 GiB A2)
	btcEstCoinAgeRunBytes   int64 = 109_682_721_587 // 102.15 GiB
	btcEstUTXOBaselineBytes int64 = 738_002_845_696 // 687.30 GiB

	btcReservationGrace = 15 * time.Minute
)

var (
	errBtcOverBudget       = errors.New("over_budget")
	errBtcBaselineInFlight = errors.New("baseline_in_flight")
	errBtcBaselineInterval = errors.New("baseline_min_interval")
)

const btcBQSchema = `
CREATE TABLE IF NOT EXISTS btc_blocks (
  height INTEGER PRIMARY KEY,
  ts TEXT NOT NULL,
  day TEXT NOT NULL,
  bits TEXT NOT NULL,
  tx_count INTEGER NOT NULL,
  weight INTEGER NOT NULL,
  subsidy_sats INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS btc_blocks_day ON btc_blocks(day);
CREATE TABLE IF NOT EXISTS btc_onchain_daily (
  day TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  payload TEXT NOT NULL,
  synced_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_spent_by_age (
  spend_day TEXT NOT NULL,
  create_day TEXT NOT NULL,
  sats INTEGER NOT NULL,
  inputs INTEGER NOT NULL,
  PRIMARY KEY (spend_day, create_day)
);
CREATE TABLE IF NOT EXISTS btc_whale_revivals (
  spend_tx TEXT NOT NULL,
  create_day TEXT NOT NULL,
  spend_day TEXT NOT NULL,
  spent_at TEXT NOT NULL,
  sats INTEGER NOT NULL,
  inputs INTEGER NOT NULL,
  PRIMARY KEY (spend_tx, create_day)
);
CREATE TABLE IF NOT EXISTS btc_coinage_daily (
  day TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  payload TEXT NOT NULL,
  computed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_utxo_baseline (
  generation INTEGER NOT NULL,
  create_day TEXT NOT NULL,
  sats INTEGER NOT NULL,
  utxos INTEGER NOT NULL,
  PRIMARY KEY (generation, create_day)
);
CREATE TABLE IF NOT EXISTS btc_utxo_state (
  create_day TEXT PRIMARY KEY,
  sats INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_holder_daily (
  day TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  payload TEXT NOT NULL,
  baseline_generation INTEGER NOT NULL,
  computed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_budget (
  month TEXT NOT NULL,
  job TEXT NOT NULL,
  billed_bytes INTEGER NOT NULL,
  PRIMARY KEY (month, job)
);
CREATE TABLE IF NOT EXISTS btc_reservations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  pid INTEGER NOT NULL,
  bq_job_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_market_daily (
  day TEXT PRIMARY KEY,
  payload TEXT NOT NULL,
  captured_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_nodes_snapshot (
  taken_at TEXT PRIMARY KEY,
  payload TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS btc_kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);`

func (s *riskStore) ensureBtcBQSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("btc bq: nil store")
	}
	if _, err := s.db.ExecContext(ctx, btcBQSchema); err != nil {
		return fmt.Errorf("btc bq schema: %w", err)
	}
	return nil
}

func (s *riskStore) getBtcKV(ctx context.Context, key string) (string, bool, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return "", false, err
	}
	var val string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM btc_kv WHERE k = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (s *riskStore) setBtcKV(ctx context.Context, key, val string) error {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO btc_kv (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, val)
	return err
}

func (s *riskStore) deleteBtcKV(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM btc_kv WHERE k = ?`, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// btcBudget manages monthly budget accounting and cross-process reservations.
type btcBudget struct {
	store    *riskStore
	settings btcSettings
	now      func() time.Time
}

func newBtcBudget(store *riskStore, settings btcSettings, now func() time.Time) *btcBudget {
	if now == nil {
		now = time.Now
	}
	return &btcBudget{store: store, settings: settings, now: now}
}

func (b *btcBudget) defaultJobTTL(job string) time.Duration {
	switch job {
	case btcCoinageJobID:
		return 30*time.Minute + btcReservationGrace
	case btcBaselineJobID:
		return 45*time.Minute + btcReservationGrace
	default:
		return 10*time.Minute + btcReservationGrace
	}
}

func (b *btcBudget) reserve(ctx context.Context, job string, bytes int64) (string, error) {
	return b.reserveWithOpts(ctx, job, bytes, 0, b.defaultJobTTL(job), 0)
}

// reserveWithOpts runs in a single BEGIN IMMEDIATE transaction (enforced by
// _txlock=immediate on the SQLite DSN) so concurrent CLI and server processes
// can never both observe sufficient remaining budget or bypass the baseline lock.
func (b *btcBudget) reserveWithOpts(ctx context.Context, job string, bytes, hardCap int64, ttl time.Duration, minInterval time.Duration) (string, error) {
	if b == nil || b.store == nil || b.store.db == nil {
		return "", fmt.Errorf("btc budget: nil store")
	}
	if err := b.store.ensureBtcBQSchema(ctx); err != nil {
		return "", err
	}
	if hardCap > 0 && bytes > hardCap {
		return "", errBtcOverBudget
	}
	now := b.now().UTC()
	nowStr := now.Format(time.RFC3339)
	expStr := now.Add(ttl).Format(time.RFC3339)
	month := now.Format("2006-01")

	tx, err := b.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if job == btcBaselineJobID {
		var active int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM btc_reservations WHERE job = ? AND expires_at > ?`,
			btcBaselineJobID, nowStr,
		).Scan(&active); err != nil {
			return "", err
		}
		if active > 0 {
			return "", errBtcBaselineInFlight
		}
		if minInterval > 0 {
			var lastStr string
			err := tx.QueryRowContext(ctx, `SELECT v FROM btc_kv WHERE k = 'last_baseline_at'`).Scan(&lastStr)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			if lastStr != "" {
				if lastAt, pErr := time.Parse(time.RFC3339, lastStr); pErr == nil {
					if now.Sub(lastAt) < minInterval {
						return "", errBtcBaselineInterval
					}
				}
			}
		}
	}

	var settled int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(billed_bytes), 0) FROM btc_budget WHERE month = ?`,
		month,
	).Scan(&settled); err != nil {
		return "", err
	}
	var reserved int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(bytes), 0) FROM btc_reservations WHERE expires_at > ? OR bq_job_id != ''`,
		nowStr,
	).Scan(&reserved); err != nil {
		return "", err
	}
	remaining := b.settings.BudgetBytes - settled - reserved
	if bytes > remaining {
		return "", errBtcOverBudget
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO btc_reservations (job, bytes, pid, bq_job_id, created_at, expires_at)
VALUES (?, ?, ?, '', ?, ?)`,
		job, bytes, os.Getpid(), nowStr, expStr,
	)
	if err != nil {
		return "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func (b *btcBudget) attachJob(ctx context.Context, id, bqJobID string) error {
	if b == nil || b.store == nil || b.store.db == nil {
		return fmt.Errorf("btc budget: nil store")
	}
	idNum, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	_, err = b.store.db.ExecContext(ctx,
		`UPDATE btc_reservations SET bq_job_id = CASE WHEN bq_job_id = '' THEN ? ELSE bq_job_id || ',' || ? END WHERE id = ?`,
		bqJobID, bqJobID, idNum,
	)
	return err
}

func (b *btcBudget) settle(ctx context.Context, id string, billed int64) error {
	if b == nil || b.store == nil || b.store.db == nil {
		return fmt.Errorf("btc budget: nil store")
	}
	tx, err := b.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := b.settleTx(ctx, tx, id, billed); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *btcBudget) settleTx(ctx context.Context, tx *sql.Tx, id string, billed int64) error {
	idNum, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	var job, createdAt string
	err = tx.QueryRowContext(ctx,
		`SELECT job, created_at FROM btc_reservations WHERE id = ?`,
		idNum,
	).Scan(&job, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM btc_reservations WHERE id = ?`, idNum); err != nil {
		return err
	}
	if billed > 0 {
		month := b.now().UTC().Format("2006-01")
		if len(createdAt) >= 7 {
			month = createdAt[:7]
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO btc_budget (month, job, billed_bytes) VALUES (?, ?, ?)
ON CONFLICT(month, job) DO UPDATE SET billed_bytes = btc_budget.billed_bytes + excluded.billed_bytes`,
			month, job, billed,
		); err != nil {
			return err
		}
	}
	return nil
}

// recoverStale settles expired reservations. When a reservation has a BigQuery
// job ID, lookup queries the job's metadata (0 bytes billed) to settle actual
// totalBytesBilled if the job finished; reservations without a job ID (e.g. a
// crash between query submission and attachJob) settle their reserved bytes
// conservatively so spend cannot be under-counted.
func (b *btcBudget) recoverStale(ctx context.Context, now time.Time, lookup func(jobID string) (billed int64, done bool, err error)) error {
	if b == nil || b.store == nil || b.store.db == nil {
		return nil
	}
	if err := b.store.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	nowStr := now.UTC().Format(time.RFC3339)
	rows, err := b.store.db.QueryContext(ctx,
		`SELECT id, bq_job_id, bytes FROM btc_reservations WHERE expires_at <= ? ORDER BY id`,
		nowStr,
	)
	if err != nil {
		return err
	}
	type staleRow struct {
		id      int64
		bqJobID string
		bytes   int64
	}
	var stale []staleRow
	for rows.Next() {
		var r staleRow
		if err := rows.Scan(&r.id, &r.bqJobID, &r.bytes); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, r)
	}
	rows.Close()

	for _, r := range stale {
		idStr := strconv.FormatInt(r.id, 10)
		if r.bqJobID == "" {
			if err := b.settle(ctx, idStr, r.bytes); err != nil {
				return err
			}
			continue
		}
		if lookup == nil {
			continue
		}
		billed, done, lErr := lookup(r.bqJobID)
		if lErr != nil || !done {
			continue
		}
		if err := b.settle(ctx, idStr, billed); err != nil {
			return err
		}
	}
	return nil
}

func (b *btcBudget) status(ctx context.Context, now time.Time) (BtcBudgetStatus, error) {
	if b == nil || b.store == nil || b.store.db == nil {
		return BtcBudgetStatus{}, fmt.Errorf("btc budget: nil store")
	}
	if err := b.store.ensureBtcBQSchema(ctx); err != nil {
		return BtcBudgetStatus{}, err
	}
	now = now.UTC()
	month := now.Format("2006-01")
	nowStr := now.Format(time.RFC3339)

	var settled, reserved int64
	if err := b.store.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(billed_bytes), 0) FROM btc_budget WHERE month = ?`,
		month,
	).Scan(&settled); err != nil {
		return BtcBudgetStatus{}, err
	}
	if err := b.store.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(bytes), 0) FROM btc_reservations WHERE expires_at > ? OR bq_job_id != ''`,
		nowStr,
	).Scan(&reserved); err != nil {
		return BtcBudgetStatus{}, err
	}
	rem := b.settings.BudgetBytes - settled - reserved
	if rem < 0 {
		rem = 0
	}
	expGiB, warn := btcFeasibility(b.settings, btcEstRollup1dBytes, btcEstCoinAgeRunBytes)
	const gib = float64(1 << 30)
	return BtcBudgetStatus{
		Month:          month,
		BudgetBytes:    b.settings.BudgetBytes,
		BudgetGiB:      float64(b.settings.BudgetBytes) / gib,
		SettledBytes:   settled,
		MonthBilledGiB: float64(settled) / gib,
		ReservedBytes:  reserved,
		ReservedGiB:    float64(reserved) / gib,
		RemainingBytes: rem,
		ExpectedGiB:    expGiB,
		Warning:        warn,
	}, nil
}

// btcFeasibility computes the expected steady-state monthly GiB for Job A (31
// daily runs) plus Job B (ceil(31 / cadence_days) runs) and returns a warning
// when the configured monthly budget is smaller than expected.
func btcFeasibility(settings btcSettings, rollup1dBytes, coinAgeRunBytes int64) (float64, string) {
	if rollup1dBytes <= 0 {
		rollup1dBytes = btcEstRollup1dBytes
	}
	if coinAgeRunBytes <= 0 {
		coinAgeRunBytes = btcEstCoinAgeRunBytes
	}
	const gib = float64(1 << 30)
	rollup1dGiB := float64(rollup1dBytes) / gib
	coinAgeRunGiB := float64(coinAgeRunBytes) / gib

	expectedGiB := 31.0 * rollup1dGiB
	if settings.CoinAgeEnabled && settings.CadenceDays > 0 {
		runs := math.Ceil(31.0 / float64(settings.CadenceDays))
		expectedGiB += runs * coinAgeRunGiB
	}
	budgetGiB := float64(settings.BudgetBytes) / gib
	if expectedGiB <= budgetGiB {
		return expectedGiB, ""
	}
	pauseDay := 31
	for d := 1; d <= 31; d++ {
		cum := float64(d) * rollup1dGiB
		if settings.CoinAgeEnabled && settings.CadenceDays > 0 {
			cum += float64(d/settings.CadenceDays) * coinAgeRunGiB
		}
		if cum > budgetGiB {
			pauseDay = d
			break
		}
	}
	warn := fmt.Sprintf("budget %.0f GiB < expected %.0f GiB/month: coin age will pause around day %d",
		budgetGiB, math.Round(expectedGiB), pauseDay)
	return expectedGiB, warn
}

// btcStopFlags persists billed failure history and stop flags in btc_kv.
type btcStopFlags struct {
	store *riskStore
}

func newBtcStopFlags(store *riskStore) *btcStopFlags {
	return &btcStopFlags{store: store}
}

func (f *btcStopFlags) stopped(ctx context.Context, job string) (string, bool, error) {
	if f == nil || f.store == nil {
		return "", false, nil
	}
	return f.store.getBtcKV(ctx, "stopped:"+job)
}

func (f *btcStopFlags) failures(ctx context.Context, job string) ([]syncFailureRecord, error) {
	if f == nil || f.store == nil {
		return nil, nil
	}
	raw, ok, err := f.store.getBtcKV(ctx, "failures:"+job)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var recs []syncFailureRecord
	if err := json.Unmarshal([]byte(raw), &recs); err != nil {
		return nil, nil
	}
	return recs, nil
}

func (f *btcStopFlags) recordFailure(ctx context.Context, job string, rec syncFailureRecord) error {
	if f == nil || f.store == nil {
		return nil
	}
	recs, err := f.failures(ctx, job)
	if err != nil {
		return err
	}
	recs = append(recs, rec)
	b, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	return f.store.setBtcKV(ctx, "failures:"+job, string(b))
}

func (f *btcStopFlags) stop(ctx context.Context, job, reason string) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.setBtcKV(ctx, "stopped:"+job, reason)
}

func (f *btcStopFlags) clear(ctx context.Context, job string) error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.deleteBtcKV(ctx, "stopped:"+job, "failures:"+job)
}

func btcJobFailurePolicy(job string) syncFailurePolicy {
	switch job {
	case btcCoinageJobID:
		return syncFailurePolicy{
			maxBilledFailures: 2,
			backoff:           []time.Duration{6 * time.Hour},
		}
	default:
		return syncFailurePolicy{
			maxBilledFailures: 4,
			within:            24 * time.Hour,
			backoff:           []time.Duration{3 * time.Hour, 6 * time.Hour, 12 * time.Hour},
		}
	}
}

// btcResumeJob clears the stop flag and recorded failures for job (`btc_onchain`
// or `btc_coinage`) in SQLite so the running server resumes on its next tick.
func btcResumeJob(ctx context.Context, store *riskStore, job string) error {
	job = strings.TrimSpace(job)
	if job != btcOnchainJobID && job != btcCoinageJobID {
		return fmt.Errorf("unknown Bitcoin BigQuery job %q (expected %s or %s)", job, btcOnchainJobID, btcCoinageJobID)
	}
	if store == nil {
		return fmt.Errorf("risk store unavailable")
	}
	flags := newBtcStopFlags(store)
	if err := flags.clear(ctx, job); err != nil {
		return err
	}
	if st, err := store.getSyncState(ctx, job); err == nil && strings.HasPrefix(st.LastError, "stopped") {
		_ = store.setSyncError(ctx, job, "")
	}
	return nil
}

func runBtcResumeCLI(ctx context.Context, cfg *Config, job string) int {
	var dbPath string
	if cfg != nil {
		dbPath = cfg.AddressRisk.dbPath()
	} else {
		dbPath = "data/security.db"
	}
	store, err := openRiskStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bitcoin-bq-resume: open store %s: %v\n", dbPath, err)
		return 1
	}
	defer store.db.Close()
	if err := btcResumeJob(ctx, store, job); err != nil {
		fmt.Fprintf(os.Stderr, "bitcoin-bq-resume: %v\n", err)
		return 1
	}
	fmt.Printf("resumed Bitcoin BigQuery job %s\n", job)
	return 0
}

// btcReadStoreImpl implements btcReadStore against riskStore.
type btcReadStoreImpl struct {
	store    *riskStore
	settings btcSettings
	budgetEl *btcBudget
	flags    *btcStopFlags
	now      func() time.Time
}

func newBtcReadStore(store *riskStore, settings btcSettings, now func() time.Time) btcReadStore {
	if store == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &btcReadStoreImpl{
		store:    store,
		settings: settings,
		budgetEl: newBtcBudget(store, settings, now),
		flags:    newBtcStopFlags(store),
		now:      now,
	}
}

func (r *btcReadStoreImpl) jobStatus(ctx context.Context, job string) (BtcBQStatus, error) {
	jobOn := r.settings.Jobs
	if job == btcCoinageJobID && !r.settings.CoinAgeEnabled {
		jobOn = false
	}
	st, err := r.store.getSyncState(ctx, job)
	if err != nil {
		return BtcBQStatus{Job: job, Jobs: jobOn, State: "off"}, err
	}
	out := BtcBQStatus{
		Job:           job,
		Jobs:          jobOn,
		SyncedThrough: st.Cursor,
		CoverageFrom:  st.CoverageFrom,
		LastError:     st.LastError,
	}
	reason, stopped, _ := r.flags.stopped(ctx, job)
	recs, _ := r.flags.failures(ctx, job)
	pol := btcJobFailurePolicy(job)
	now := r.now().UTC()
	var activeRecs []syncFailureRecord
	if pol.within > 0 {
		cutoff := now.Add(-pol.within)
		for _, f := range recs {
			if !f.At.Before(cutoff) {
				activeRecs = append(activeRecs, f)
			}
		}
	} else {
		activeRecs = recs
	}

	switch {
	case stopped:
		out.State = "stopped"
		out.StopReason = reason
		out.Failures = len(activeRecs)
	case !jobOn:
		out.State = "off"
	case st.LastError == "over_budget":
		out.State = "budget_paused"
	case len(activeRecs) > 0:
		out.State = "retrying"
		out.Failures = len(activeRecs)
		last := activeRecs[len(activeRecs)-1]
		idx := len(activeRecs) - 1
		if idx >= len(pol.backoff) {
			idx = len(pol.backoff) - 1
		}
		if idx >= 0 {
			out.NextRunAfter = last.At.Add(pol.backoff[idx]).UTC().Format(time.RFC3339)
		}
	case st.Cursor == "":
		out.State = "cold_start"
	default:
		out.State = "ready"
	}
	return out, nil
}

func (r *btcReadStoreImpl) budget(ctx context.Context, now time.Time) (BtcBudgetStatus, error) {
	return r.budgetEl.status(ctx, now)
}

func (r *btcReadStoreImpl) baselineStatus(ctx context.Context) (BtcBaselineStatus, error) {
	out := BtcBaselineStatus{
		AllowUI:         r.settings.BaselineAllowUI,
		MinIntervalDays: int(r.settings.BaselineMinInterval.Hours() / 24),
		CLICommand:      "./biglens-server --bitcoin-utxo-baseline",
	}
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return out, err
	}
	if lastAt, ok, _ := r.store.getBtcKV(ctx, "last_baseline_at"); ok && lastAt != "" {
		out.LastBaselineAt = lastAt
		if t, err := time.Parse(time.RFC3339, lastAt); err == nil {
			out.NextAllowedAt = t.Add(r.settings.BaselineMinInterval).UTC().Format(time.RFC3339)
		}
	}
	if pendRaw, ok, _ := r.store.getBtcKV(ctx, "baseline_pending"); ok && pendRaw != "" {
		out.Pending = true
	}
	var activeRes int64
	nowStr := r.budgetEl.now().UTC().Format(time.RFC3339)
	if err := r.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM btc_reservations WHERE job = ? AND expires_at > ?`,
		btcBaselineJobID, nowStr,
	).Scan(&activeRes); err == nil && activeRes > 0 {
		out.Building = true
		out.Pending = true
	}
	if metaRaw, ok, _ := r.store.getBtcKV(ctx, "baseline_meta"); ok && metaRaw != "" {
		var saved BtcBaselineStatus
		if json.Unmarshal([]byte(metaRaw), &saved) == nil {
			out.Available = saved.Available
			out.Generation = saved.Generation
			out.AsOf = saved.AsOf
			out.BuiltAt = saved.BuiltAt
			out.BaselineSats = saved.BaselineSats
			out.BaselineBTC = saved.BaselineBTC
			out.ExpectedSats = saved.ExpectedSats
			out.DeltaPct = saved.DeltaPct
			out.Suspect = saved.Suspect
			out.CMSplyCurBTC = saved.CMSplyCurBTC
			return out, nil
		}
	}
	var gen sql.NullInt64
	var sats sql.NullInt64
	if err := r.store.db.QueryRowContext(ctx,
		`SELECT MAX(generation), COALESCE(SUM(sats), 0) FROM btc_utxo_baseline WHERE generation = (SELECT MAX(generation) FROM btc_utxo_baseline)`,
	).Scan(&gen, &sats); err == nil && gen.Valid && gen.Int64 > 0 {
		out.Available = true
		out.Generation = gen.Int64
		out.BaselineSats = sats.Int64
		out.BaselineBTC = float64(sats.Int64) / 1e8
	}
	return out, nil
}

func (r *btcReadStoreImpl) onchainDays(ctx context.Context, from, to string) ([]BtcOnchainDay, error) {
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(ctx, `
SELECT payload FROM btc_onchain_daily
WHERE day >= ? AND day <= ?
ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BtcOnchainDay
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var d BtcOnchainDay
		if err := json.Unmarshal([]byte(p), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *btcReadStoreImpl) coinageDays(ctx context.Context, from, to string) ([]BtcCoinageDay, error) {
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(ctx, `
SELECT payload FROM btc_coinage_daily
WHERE day >= ? AND day <= ?
ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BtcCoinageDay
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var d BtcCoinageDay
		if err := json.Unmarshal([]byte(p), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *btcReadStoreImpl) holderDays(ctx context.Context, from, to string) ([]BtcHolderDay, error) {
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(ctx, `
SELECT payload FROM btc_holder_daily
WHERE day >= ? AND day <= ?
ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BtcHolderDay
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var d BtcHolderDay
		if err := json.Unmarshal([]byte(p), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *btcReadStoreImpl) whaleRevivals(ctx context.Context, from, to string) ([]BtcWhaleRevival, error) {
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.store.db.QueryContext(ctx, `
SELECT spend_tx, create_day, spend_day, spent_at, sats, inputs
FROM btc_whale_revivals
WHERE spend_day >= ? AND spend_day <= ?
ORDER BY spend_day DESC, sats DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BtcWhaleRevival
	for rows.Next() {
		var w BtcWhaleRevival
		if err := rows.Scan(&w.SpendTx, &w.CreateDay, &w.SpendDay, &w.SpentAt, &w.Sats, &w.Inputs); err != nil {
			return nil, err
		}
		w.BTC = float64(w.Sats) / 1e8
		if c, err1 := time.Parse("2006-01-02", w.CreateDay); err1 == nil {
			if s, err2 := time.Parse("2006-01-02", w.SpendDay); err2 == nil {
				w.AgeDays = int(s.Sub(c).Hours() / 24)
			}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// btcJobs holds the operator-enabled Bitcoin BigQuery job runners. It is never
// constructed when bitcoin.bigquery.jobs is false.
type btcJobs struct {
	settings  btcSettings
	store     *riskStore
	bq        *BQClient
	budget    *btcBudget
	flags     *btcStopFlags
	now       func() time.Time
	jobLookup func(jobID string) (int64, bool, error)
}

func newBitcoinJobs(settings btcSettings, store *riskStore, client *BQClient) *btcJobs {
	if !settings.Enabled || !settings.Jobs || store == nil || client == nil {
		return nil
	}
	return &btcJobs{
		settings: settings,
		store:    store,
		bq:       client,
		budget:   newBtcBudget(store, settings, time.Now),
		flags:    newBtcStopFlags(store),
		now:      time.Now,
	}
}

func (j *btcJobs) recoverStaleOnce(ctx context.Context) {
	if j == nil || j.budget == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	lookup := j.jobLookup
	if lookup == nil && j.bq != nil {
		lookup = bqLookupJobBytes(rctx, j.bq.client)
	}
	if err := j.budget.recoverStale(rctx, j.now().UTC(), lookup); err != nil {
		slog.Warn("bitcoin bigquery recover stale reservations", "error", err)
	}
}

// bqLookupJobBytes queries BigQuery job metadata (jobs.get, 0 bytes billed) to
// recover actual TotalBytesBilled for one or more comma-separated job IDs attached
// to a stale reservation.
func bqLookupJobBytes(ctx context.Context, client *bigquery.Client) func(jobID string) (int64, bool, error) {
	return func(jobID string) (int64, bool, error) {
		if client == nil {
			return 0, false, fmt.Errorf("nil bigquery client")
		}
		var total int64
		for _, part := range strings.Split(jobID, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			job, err := client.JobFromID(ctx, part)
			if err != nil {
				return 0, false, err
			}
			st := job.LastStatus()
			if st == nil {
				st, err = job.Status(ctx)
				if err != nil {
					return 0, false, err
				}
			}
			if !st.Done() {
				return 0, false, nil
			}
			if st.Statistics != nil {
				if qs, ok := st.Statistics.Details.(*bigquery.QueryStatistics); ok {
					total += qs.TotalBytesBilled
				}
			}
		}
		return total, true, nil
	}
}

// bitcoinSyncJobs returns the background BigQuery sync functions for Bitcoin.
// When bitcoin.bigquery.jobs is false (the default) or store/client is nil, it
// returns nil without constructing any syncers or touching BigQuery.
func bitcoinSyncJobs(settings btcSettings, store *riskStore, client *BQClient, prices ...*btcPriceSyncer) []func(context.Context) {
	jobs := newBitcoinJobs(settings, store, client)
	if jobs == nil {
		return nil
	}
	var priceSyncer *btcPriceSyncer
	if len(prices) > 0 {
		priceSyncer = prices[0]
	}
	maint := newBtcHolderMaintenance(store, priceSyncer, jobs.now)

	expGiB, warn := btcFeasibility(settings, btcEstRollup1dBytes, btcEstCoinAgeRunBytes)
	if warn != "" {
		slog.Warn("bitcoin bigquery feasibility", "warning", warn, "expected_gib", expGiB)
	} else {
		slog.Info("bitcoin bigquery jobs enabled",
			"initial_days", settings.InitialDays,
			"cadence_days", settings.CadenceDays,
			"expected_month_gib", expGiB,
			"budget_gib", float64(settings.BudgetBytes)/float64(1<<30),
		)
	}

	rollupSyncer := &dailyBQSyncer{
		store:          store,
		job:            newBtcOnchainJob(store, client),
		now:            jobs.now,
		initialDays:    settings.InitialDays,
		name:           btcOnchainJobID,
		hardCapBytes:   256 << 30,
		dryRunSizedCap: true,
		maxWindowDays:  31,
		chunkCatchUp:   true,
		timeout:        10 * time.Minute,
		prune:          func(context.Context, civil.Date) error { return nil },
		budget:         jobs.budget,
		failurePolicy:  btcJobFailurePolicy(btcOnchainJobID),
		stopFlags:      jobs.flags,
	}
	out := []func(context.Context){jobs.recoverStaleOnce, rollupSyncer.syncOnce}

	if settings.CoinAgeEnabled {
		retentionDays := settings.CoinAgeInitialDays + 30
		stateAsOf := func() string {
			v, _ := store.getBtcUTXOStateAsOf(context.Background())
			return v
		}
		coinAgeSyncer := &dailyBQSyncer{
			store:          store,
			job:            newBtcCoinAgeJob(store, client, settings, maint.runOnce),
			now:            jobs.now,
			initialDays:    settings.CoinAgeInitialDays,
			name:           btcCoinageJobID,
			hardCapBytes:   256 << 30,
			dryRunSizedCap: true,
			maxWindowDays:  731,
			minWindowDays:  settings.CadenceDays,
			timeout:        30 * time.Minute,
			prune:          pruneBtcCoinAge(store, retentionDays, stateAsOf),
			budget:         jobs.budget,
			failurePolicy:  btcJobFailurePolicy(btcCoinageJobID),
			stopFlags:      jobs.flags,
		}
		out = append(out, coinAgeSyncer.syncOnce)
	}
	out = append(out, maint.runOnce)
	return out
}

func btcApplyQueryLabels(q *bigquery.Query, jobLabel string) {
	applyQueryDefaults(q)
	if q.Labels == nil {
		q.Labels = make(map[string]string)
	}
	q.Labels["app"] = "biglens"
	q.Labels["feature"] = "bitcoin"
	if jobLabel != "" {
		q.Labels["job"] = jobLabel
	}
}

func btcDryRunQuery(ctx context.Context, client *bigquery.Client, sqlText, jobLabel string, params []bigquery.QueryParameter) (int64, error) {
	if client == nil {
		return 0, fmt.Errorf("nil bigquery client")
	}
	q := client.Query(sqlText)
	q.Parameters = params
	q.DryRun = true
	btcApplyQueryLabels(q, jobLabel)
	job, err := q.Run(ctx)
	if err != nil {
		return 0, err
	}
	return job.LastStatus().Statistics.TotalBytesProcessed, nil
}

func btcRunQuery[T any](ctx context.Context, client *bigquery.Client, sqlText, jobLabel string, params []bigquery.QueryParameter, maxBytes int64) ([]T, int64, error) {
	if client == nil {
		return nil, 0, fmt.Errorf("nil bigquery client")
	}
	q := client.Query(sqlText)
	q.Parameters = params
	q.MaxBytesBilled = maxBytes
	btcApplyQueryLabels(q, jobLabel)
	job, err := q.Run(ctx)
	if err != nil {
		return nil, 0, err
	}
	syncAttachJob(ctx, job.ID())
	status, err := job.Wait(ctx)
	if err != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = job.Cancel(cctx)
		var billed int64
		if st, sErr := job.Status(cctx); sErr == nil && st != nil && st.Statistics != nil {
			if qs, ok := st.Statistics.Details.(*bigquery.QueryStatistics); ok {
				billed = qs.TotalBytesBilled
			}
		}
		cancel()
		return nil, billed, err
	}
	if err := status.Err(); err != nil {
		return nil, 0, err
	}
	var billed int64
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		billed = qs.TotalBytesBilled
	}
	it, err := job.Read(ctx)
	if err != nil {
		return nil, billed, err
	}
	var out []T
	for {
		var r T
		err := it.Next(&r)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, billed, err
		}
		out = append(out, r)
	}
	return out, billed, nil
}
