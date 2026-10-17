package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	btcBaselineHardCapBytes int64 = 1 << 40 // 1 TiB
	btcBaselineTimeout            = 45 * time.Minute
	btcBaselineTokenTTL           = 15 * time.Minute
)

var (
	errBtcJobsDisabled         = errors.New("bitcoin_jobs_disabled")
	errBtcBaselineBadToken     = errors.New("invalid_or_expired_token")
	btcGenesisBlockTimestamp   = time.Date(2009, 1, 3, 18, 15, 5, 0, time.UTC)
)

func btcUTXOBaselineSQL() string {
	return `WITH o AS (
  SELECT transaction_hash AS h, index AS ix, CAST(value AS INT64) AS sats, DATE(block_timestamp) AS create_day
  FROM ` + "`bigquery-public-data.crypto_bitcoin.outputs`" + `
  WHERE block_timestamp > TIMESTAMP(@genesis_time) AND block_timestamp < TIMESTAMP(@end_date)
    AND type != 'nulldata'
    AND NOT (transaction_hash IN UNNEST(@bip30_txids) AND block_timestamp IN UNNEST(@bip30_overwritten_times))
), i AS (
  SELECT spent_transaction_hash AS h, spent_output_index AS ix
  FROM ` + "`bigquery-public-data.crypto_bitcoin.inputs`" + `
  WHERE block_timestamp < TIMESTAMP(@end_date) AND spent_transaction_hash IS NOT NULL
)
SELECT FORMAT_DATE('%Y-%m-%d', create_day) AS create_day, SUM(sats) AS sats, COUNT(*) AS utxos
FROM o LEFT JOIN i USING (h, ix)
WHERE i.h IS NULL
GROUP BY create_day
ORDER BY create_day`
}

type btcUTXOBaselineRow struct {
	CreateDay string `bigquery:"create_day" json:"create_day"`
	Sats      int64  `bigquery:"sats" json:"sats"`
	UTXOs     int64  `bigquery:"utxos" json:"utxos"`
}

type btcPendingBaseline struct {
	Generation int64  `json:"generation"`
	AsOf       string `json:"as_of"`
	BuiltAt    string `json:"built_at"`
}

type btcBaselineQuerySource interface {
	DryRunBaseline(ctx context.Context, end civil.Date) (int64, error)
	FetchBaseline(ctx context.Context, end civil.Date, maxBytes int64) ([]btcUTXOBaselineRow, int64, error)
}

type bqBaselineSource struct {
	bq *BQClient
}

func (s bqBaselineSource) params(end civil.Date) []bigquery.QueryParameter {
	return []bigquery.QueryParameter{
		{Name: "genesis_time", Value: btcGenesisBlockTimestamp},
		{Name: "end_date", Value: end},
		{Name: "bip30_txids", Value: btcBIP30Txids},
		{Name: "bip30_overwritten_times", Value: btcBIP30OverwrittenTimes},
	}
}

func (s bqBaselineSource) DryRunBaseline(ctx context.Context, end civil.Date) (int64, error) {
	return btcDryRunQuery(ctx, s.bq.client, btcUTXOBaselineSQL(), btcBaselineJobID, s.params(end))
}

func (s bqBaselineSource) FetchBaseline(ctx context.Context, end civil.Date, maxBytes int64) ([]btcUTXOBaselineRow, int64, error) {
	return btcRunQuery[btcUTXOBaselineRow](ctx, s.bq.client, btcUTXOBaselineSQL(), btcBaselineJobID, s.params(end), maxBytes)
}

// reconcileBtcBaseline compares total baseline satoshis against scheduled supply
// at maxHeight minus the unspendable genesis subsidy (50 BTC), unclaimed miner
// subsidies, and OP_RETURN burns. Returns expectedSats, deltaPct, and suspect (|delta| > 0.01%).
func reconcileBtcBaseline(baselineSats, maxHeight, unclaimedSats, opretBurnSats int64) (int64, *float64, bool) {
	if maxHeight <= 0 {
		return 0, nil, false
	}
	expected := btcScheduledSupply(maxHeight) - btcSubsidySats(0) - unclaimedSats - opretBurnSats
	if expected <= 0 {
		return 0, nil, false
	}
	delta := float64(baselineSats-expected) / float64(expected) * 100.0
	suspect := math.Abs(delta) > 0.01
	return expected, &delta, suspect
}

type btcBaselineTokenEntry struct {
	endDate   civil.Date
	dryBytes  int64
	expiresAt time.Time
}

type btcBaselineRunner struct {
	store    *riskStore
	settings btcSettings
	budget   *btcBudget
	src      btcBaselineQuerySource
	now      func() time.Time

	// Test hook invoked inside the handoff transaction before Commit.
	beforeCommit func() error

	mu     sync.Mutex
	tokens map[string]btcBaselineTokenEntry
}

func newBtcBaselineRunner(store *riskStore, settings btcSettings, src btcBaselineQuerySource, now func() time.Time) *btcBaselineRunner {
	if now == nil {
		now = time.Now
	}
	return &btcBaselineRunner{
		store:    store,
		settings: settings,
		budget:   newBtcBudget(store, settings, now),
		src:      src,
		now:      now,
		tokens:   make(map[string]btcBaselineTokenEntry),
	}
}

func (r *btcBaselineRunner) resolveEndDate(ctx context.Context) (civil.Date, error) {
	if r.store != nil {
		if st, err := r.store.getSyncState(ctx, btcCoinageJobID); err == nil && st.Cursor != "" {
			if d, pErr := civil.ParseDate(st.Cursor); pErr == nil {
				return d.AddDays(1), nil
			}
		}
		if st, err := r.store.getSyncState(ctx, btcOnchainJobID); err == nil && st.Cursor != "" {
			if d, pErr := civil.ParseDate(st.Cursor); pErr == nil {
				return d.AddDays(1), nil
			}
		}
	}
	return civil.DateOf(r.now().UTC()), nil
}

func (r *btcBaselineRunner) checkPreconditions(ctx context.Context, force bool) error {
	if !r.settings.Enabled || !r.settings.Jobs {
		return errBtcJobsDisabled
	}
	if r.store == nil {
		return fmt.Errorf("btc baseline: nil store")
	}
	if err := r.store.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	now := r.now().UTC()
	nowStr := now.Format(time.RFC3339)

	var active int64
	if err := r.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM btc_reservations WHERE job = ? AND expires_at > ?`,
		btcBaselineJobID, nowStr,
	).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return errBtcBaselineInFlight
	}
	if !force && r.settings.BaselineMinInterval > 0 {
		if lastStr, ok, _ := r.store.getBtcKV(ctx, "last_baseline_at"); ok && lastStr != "" {
			if lastAt, err := time.Parse(time.RFC3339, lastStr); err == nil {
				if now.Sub(lastAt) < r.settings.BaselineMinInterval {
					return errBtcBaselineInterval
				}
			}
		}
	}
	return nil
}

// dryRun executes a free BigQuery dry run, checks budget and interval preconditions,
// and mints a 15-minute single-use confirmation token for the UI flow.
func (r *btcBaselineRunner) dryRun(ctx context.Context) (btcBaselinePlan, error) {
	if err := r.checkPreconditions(ctx, false); err != nil {
		return btcBaselinePlan{}, err
	}
	endDate, err := r.resolveEndDate(ctx)
	if err != nil {
		return btcBaselinePlan{}, err
	}
	dryBytes, err := r.src.DryRunBaseline(ctx, endDate)
	if err != nil {
		return btcBaselinePlan{}, err
	}
	if dryBytes > btcBaselineHardCapBytes {
		return btcBaselinePlan{}, errBtcOverBudget
	}
	bStatus, err := r.budget.status(ctx, r.now().UTC())
	if err != nil {
		return btcBaselinePlan{}, err
	}
	if dryBytes > bStatus.RemainingBytes {
		return btcBaselinePlan{}, errBtcOverBudget
	}

	var rawTok [16]byte
	if _, err := rand.Read(rawTok[:]); err != nil {
		return btcBaselinePlan{}, err
	}
	tok := hex.EncodeToString(rawTok[:])
	exp := r.now().UTC().Add(btcBaselineTokenTTL)

	r.mu.Lock()
	for k, e := range r.tokens {
		if !r.now().UTC().Before(e.expiresAt) {
			delete(r.tokens, k)
		}
	}
	r.tokens[tok] = btcBaselineTokenEntry{
		endDate:   endDate,
		dryBytes:  dryBytes,
		expiresAt: exp,
	}
	r.mu.Unlock()

	gib := float64(dryBytes) / float64(1<<30)
	usd := float64(dryBytes) / float64(1<<40) * 6.25
	return btcBaselinePlan{
		AsOf:           endDate.String(),
		EstimatedBytes: dryBytes,
		EstimatedGiB:   gib,
		EstimatedUSD:   usd,
		Token:          tok,
		ExpiresAt:      exp.Format(time.RFC3339),
	}, nil
}

// confirm consumes a single-use UI token and executes the baseline build.
func (r *btcBaselineRunner) confirm(ctx context.Context, token string) error {
	if !r.settings.Enabled || !r.settings.Jobs {
		return errBtcJobsDisabled
	}
	now := r.now().UTC()
	r.mu.Lock()
	entry, ok := r.tokens[token]
	if ok {
		delete(r.tokens, token)
	}
	r.mu.Unlock()
	if !ok || !now.Before(entry.expiresAt) {
		return errBtcBaselineBadToken
	}
	_, err := r.execute(ctx, entry.endDate, entry.dryBytes, false)
	return err
}

// runCLI runs the CLI UTXO baseline flow: dry-run only when yes is false, or
// reserve -> query -> atomic handoff when yes is true. force bypasses only the
// minimum interval lock, never the monthly budget.
func (r *btcBaselineRunner) runCLI(ctx context.Context, yes, force bool) (btcBaselinePlan, BtcBaselineStatus, error) {
	if err := r.checkPreconditions(ctx, force); err != nil {
		return btcBaselinePlan{}, BtcBaselineStatus{}, err
	}
	endDate, err := r.resolveEndDate(ctx)
	if err != nil {
		return btcBaselinePlan{}, BtcBaselineStatus{}, err
	}
	dryBytes, err := r.src.DryRunBaseline(ctx, endDate)
	if err != nil {
		return btcBaselinePlan{}, BtcBaselineStatus{}, err
	}
	bStatus, err := r.budget.status(ctx, r.now().UTC())
	if err != nil {
		return btcBaselinePlan{}, BtcBaselineStatus{}, err
	}
	if dryBytes > btcBaselineHardCapBytes || dryBytes > bStatus.RemainingBytes {
		return btcBaselinePlan{}, BtcBaselineStatus{}, errBtcOverBudget
	}

	gib := float64(dryBytes) / float64(1<<30)
	usd := float64(dryBytes) / float64(1<<40) * 6.25
	plan := btcBaselinePlan{
		AsOf:           endDate.String(),
		EstimatedBytes: dryBytes,
		EstimatedGiB:   gib,
		EstimatedUSD:   usd,
	}
	if !yes {
		return plan, BtcBaselineStatus{}, nil
	}
	st, err := r.execute(ctx, endDate, dryBytes, force)
	return plan, st, err
}

func (r *btcBaselineRunner) execute(ctx context.Context, endDate civil.Date, dryBytes int64, force bool) (BtcBaselineStatus, error) {
	if !r.settings.Enabled || !r.settings.Jobs {
		return BtcBaselineStatus{}, errBtcJobsDisabled
	}
	minInterval := r.settings.BaselineMinInterval
	if force {
		minInterval = 0
	}
	ttl := btcBaselineTimeout + btcReservationGrace
	resID, err := r.budget.reserveWithOpts(ctx, btcBaselineJobID, dryBytes, btcBaselineHardCapBytes, ttl, minInterval)
	if err != nil {
		return BtcBaselineStatus{}, err
	}

	maxCap := int64(math.Ceil(float64(dryBytes) * 1.25))
	if maxCap < 1<<30 {
		maxCap = 1 << 30
	}
	if maxCap > btcBaselineHardCapBytes {
		maxCap = btcBaselineHardCapBytes
	}
	rctx, cancel := context.WithTimeout(ctx, btcBaselineTimeout)
	defer cancel()
	rctx = withSyncJobAttach(rctx, func(bqJobID string) {
		_ = r.budget.attachJob(rctx, resID, bqJobID)
	})

	rows, billed, runErr := r.src.FetchBaseline(rctx, endDate, maxCap)
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) && billed == 0 && dryBytes > 0 {
			billed = dryBytes
		}
		_ = r.budget.settle(context.Background(), resID, billed)
		return BtcBaselineStatus{}, runErr
	}
	if len(rows) == 0 {
		_ = r.budget.settle(context.Background(), resID, billed)
		return BtcBaselineStatus{}, suspiciousEmpty(endDate.String(), "utxo baseline rows")
	}

	st, err := r.store.commitBtcBaseline(ctx, r.budget, r.settings, resID, endDate.String(), rows, billed, r.now().UTC(), r.beforeCommit)
	if err != nil {
		return BtcBaselineStatus{}, err
	}
	return st, nil
}

func runBtcBaselineCLI(ctx context.Context, cfg *Config, bq *BQClient, yes, force bool) int {
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "bitcoin-utxo-baseline: missing config")
		return 1
	}
	settings := cfg.Bitcoin.settings()
	if !settings.Enabled || !settings.Jobs {
		fmt.Fprintln(os.Stderr, "bitcoin-utxo-baseline: bitcoin.bigquery.jobs is false (set bitcoin.bigquery.jobs: true in conf.yaml first)")
		return 1
	}
	store, err := openRiskStore(cfg.AddressRisk.dbPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "bitcoin-utxo-baseline: open store %s: %v\n", cfg.AddressRisk.dbPath(), err)
		return 1
	}
	defer store.db.Close()

	runner := newBtcBaselineRunner(store, settings, bqBaselineSource{bq: bq}, time.Now)
	plan, st, err := runner.runCLI(ctx, yes, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bitcoin-utxo-baseline: %v\n", err)
		return 1
	}
	if !yes {
		fmt.Printf("Bitcoin UTXO baseline dry run (as_of < %s): %.2f GiB (~$%.2f)\nRe-run with --yes to execute.\n",
			plan.AsOf, plan.EstimatedGiB, plan.EstimatedUSD)
		return 0
	}
	fmt.Printf("Bitcoin UTXO baseline generation %d stored (as_of < %s, %.4f BTC, pending=%v, suspect=%v)\n",
		st.Generation, st.AsOf, st.BaselineBTC, st.Pending, st.Suspect)
	return 0
}
