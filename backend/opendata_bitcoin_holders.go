package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	"cloud.google.com/go/civil"
)

const (
	btcHolderSchemaVersion    = 1
	btcUTXOStateGenerationKey = "utxo_state_generation"
)

// utxoState maps creation day ("YYYY-MM-DD") to unspent satoshis.
type utxoState map[string]int64

func (u utxoState) clone() utxoState {
	out := make(utxoState, len(u))
	maps.Copy(out, u)
	return out
}

func (u utxoState) totalSats() int64 {
	var sum int64
	for _, v := range u {
		sum += v
	}
	return sum
}

// applyDay updates UTXO state u in-place for day t.
// Forward (t):  U <- U - S_t + C_t * e_t
// Backward (t): U <- U + S_t - C_t * e_t
func applyDay(u utxoState, day string, spent map[string]int64, createdSats int64, forward bool) {
	if forward {
		for c, s := range spent {
			if s == 0 {
				continue
			}
			u[c] -= s
			if u[c] == 0 {
				delete(u, c)
			}
		}
		if createdSats != 0 {
			u[day] += createdSats
			if u[day] == 0 {
				delete(u, day)
			}
		}
		return
	}
	if createdSats != 0 {
		u[day] -= createdSats
		if u[day] == 0 {
			delete(u, day)
		}
	}
	for c, s := range spent {
		if s == 0 {
			continue
		}
		u[c] += s
		if u[c] == 0 {
			delete(u, c)
		}
	}
}

// initFromBaseline constructs the initial UTXO state at T0 - 1 day from the
// baseline (which covers blocks with timestamp < T0) and subtracts cross-boundary
// outputs created in a block dated >= T0 that were already spent in a block dated
// < T0 (U_init = baseline - sum_{c >= T0} S_{T0-1}[c] * e_c).
func initFromBaseline(baseline map[string]int64, t0 string, crossBoundary map[string]int64) utxoState {
	u := make(utxoState, len(baseline))
	for c, sats := range baseline {
		if sats != 0 {
			u[c] = sats
		}
	}
	for c, sats := range crossBoundary {
		if c >= t0 && sats != 0 {
			u[c] -= sats
			if u[c] == 0 {
				delete(u, c)
			}
		}
	}
	return u
}

// checkSupplyIdentity verifies the exact satoshi conservation identity:
// sum(U_t) - sum(U_{t-1}) = coinbase_sats_t - fees_sats_t - opret_value_sats_t.
func checkSupplyIdentity(prevSum, nextSum, coinbase, fees, opret int64) error {
	actualDelta := nextSum - prevSum
	expectedDelta := coinbase - fees - opret
	if actualDelta != expectedDelta {
		return fmt.Errorf("supply_identity_mismatch: delta_u=%d want=%d (coinbase=%d fees=%d opret=%d)",
			actualDelta, expectedDelta, coinbase, fees, opret)
	}
	return nil
}

// checkIssuanceBound verifies that claimed issuance (coinbase_sats - fees_sats)
// does not exceed scheduled issuance (sum of per-block subsidies). An underclaim
// (claimed < scheduled) is returned as a non-fatal warning.
func checkIssuanceBound(claimed, scheduled int64) (string, error) {
	if claimed > scheduled {
		return "", fmt.Errorf("issuance_bound_exceeded: claimed=%d > scheduled=%d", claimed, scheduled)
	}
	if claimed < scheduled {
		return fmt.Sprintf("underclaimed_issuance: claimed=%d < scheduled=%d sats", claimed, scheduled), nil
	}
	return "", nil
}

// checkBucketSigns verifies that after applying day t:
//   - every bucket c <= t has u[c] >= 0
//   - every bucket c > t + 1 has u[c] == 0
//
// Bucket c == t + 1 may be negative transiently on day t when a midnight
// timestamp inversion causes a block dated t to spend an output created in a
// parent block dated t + 1.
func checkBucketSigns(u utxoState, day string) error {
	d, err := civil.ParseDate(day)
	if err != nil {
		return fmt.Errorf("invalid day %q: %w", day, err)
	}
	plusOne := d.AddDays(1).String()
	for c, sats := range u {
		if sats == 0 {
			continue
		}
		if c <= day && sats < 0 {
			return fmt.Errorf("negative_bucket: bucket %s has %d sats after day %s", c, sats, day)
		}
		if c > plusOne {
			return fmt.Errorf("future_bucket_exceeded: bucket %s has %d sats after day %s (> %s)", c, sats, day, plusOne)
		}
	}
	return nil
}

// holderMetrics computes a day's holder metrics (supply, LTH/STH supply, HODL
// waves, and price-gated realized cap, realized price, LTH/STH realized prices,
// MVRV, and supply in profit) from UTXO state u.
func holderMetrics(day string, gen int64, u utxoState, prices map[string]float64, priceComplete bool) BtcHolderDay {
	d, _ := civil.ParseDate(day)
	bandSats := make(map[string]int64, len(btcAgeBands))
	var (
		supplySats int64
		lthSats    int64
		sthSats    int64
	)

	for cDay, sats := range u {
		if sats == 0 {
			continue
		}
		supplySats += sats
		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = d.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		if age >= 155 {
			lthSats += sats
		} else {
			sthSats += sats
		}
		bandSats[btcAgeBand(age)] += sats
	}

	waves := make(map[string]float64, len(btcAgeBands))
	for _, b := range btcAgeBands {
		if supplySats > 0 {
			waves[b] = float64(bandSats[b]) / float64(supplySats) * 100.0
		} else {
			waves[b] = 0
		}
	}

	supplyBTC := float64(supplySats) / 1e8
	out := BtcHolderDay{
		Date:               day,
		BaselineGeneration: gen,
		SupplySats:         supplySats,
		SupplyBTC:          supplyBTC,
		LTHSupplySats:      lthSats,
		STHSupplySats:      sthSats,
		HODLWavesPct:       waves,
	}

	closeT := prices[day]
	if !priceComplete || closeT <= 0 {
		out.Blocked = "price_history_incomplete"
		return out
	}

	var (
		realizedCapUSD float64
		lthCapUSD      float64
		sthCapUSD      float64
		profitSats     int64
	)
	for cDay, sats := range u {
		if sats == 0 {
			continue
		}
		// Coins created before earliest priced day (2010-08-18) have pc == 0 per §6.4.
		pc := prices[cDay]
		btc := float64(sats) / 1e8
		valUSD := btc * pc
		realizedCapUSD += valUSD

		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = d.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		if age >= 155 {
			lthCapUSD += valUSD
		} else {
			sthCapUSD += valUSD
		}
		if pc < closeT {
			profitSats += sats
		}
	}

	out.RealizedCapUSD = &realizedCapUSD
	if supplyBTC > 0 {
		rp := realizedCapUSD / supplyBTC
		out.RealizedPriceUSD = &rp
	}
	if lthSats > 0 {
		lthRP := lthCapUSD / (float64(lthSats) / 1e8)
		out.LTHRealizedPrice = &lthRP
	}
	if sthSats > 0 {
		sthRP := sthCapUSD / (float64(sthSats) / 1e8)
		out.STHRealizedPrice = &sthRP
	}
	if realizedCapUSD > 0 && supplyBTC > 0 {
		mvrv := (supplyBTC * closeT) / realizedCapUSD
		out.MVRV = &mvrv
	}
	if supplySats > 0 {
		profPct := float64(profitSats) / float64(supplySats) * 100.0
		out.SupplyInProfitPct = &profPct
	}
	return out
}

func blockHolderDayMetrics(hm *BtcHolderDay, reason string) {
	hm.Blocked = reason
	hm.RealizedCapUSD = nil
	hm.RealizedPriceUSD = nil
	hm.LTHRealizedPrice = nil
	hm.STHRealizedPrice = nil
	hm.MVRV = nil
	hm.SupplyInProfitPct = nil
}

// btcHolderMaintenance coordinates local UTXO state resets from a pending
// baseline, daily roll-forwards through min(coinage_cursor, rollup_cursor),
// and upsert-only holder/coinage metric recomputation. It never queries BigQuery.
type btcHolderMaintenance struct {
	store  *riskStore
	prices *btcPriceSyncer
	now    func() time.Time
	mu     sync.Mutex
}

func newBtcHolderMaintenance(store *riskStore, prices *btcPriceSyncer, now func() time.Time) *btcHolderMaintenance {
	if now == nil {
		now = time.Now
	}
	return &btcHolderMaintenance{
		store:  store,
		prices: prices,
		now:    now,
	}
}

func (m *btcHolderMaintenance) runOnce(ctx context.Context) {
	if m == nil {
		return
	}
	_ = m.run(ctx)
}

func (m *btcHolderMaintenance) run(ctx context.Context) error {
	if m == nil || m.store == nil || m.store.db == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.store.ensureBtcBQSchema(ctx); err != nil {
		return err
	}

	var closePrices map[string]float64
	var priceComplete bool
	if m.prices != nil {
		closePrices = m.prices.closes("", "")
		priceComplete = m.prices.priceCoverage().Complete
	}

	matrixByDay, err := m.store.loadBtcSpentMatrixByRange(ctx, "", "")
	if err != nil {
		return err
	}
	onchainByDay, err := m.store.loadBtcOnchainDaysMap(ctx)
	if err != nil {
		return err
	}

	var coinageUpdates []BtcCoinageDay
	for d, spent := range matrixByDay {
		coinageUpdates = append(coinageUpdates, coinageMetrics(d, spent, closePrices, priceComplete))
	}

	// Check raw baseline_pending first: never roll forward over a pending baseline.
	rawPend, hasPendKey, err := m.store.getBtcKV(ctx, btcBaselinePendingKey)
	if err != nil {
		return err
	}
	if hasPendKey && rawPend != "" {
		var pend btcPendingBaseline
		if err := json.Unmarshal([]byte(rawPend), &pend); err != nil || pend.Generation <= 0 || pend.AsOf == "" {
			return fmt.Errorf("invalid baseline_pending payload: %q", rawPend)
		}
		return m.applyPendingBaseline(ctx, pend, matrixByDay, onchainByDay, closePrices, priceComplete, coinageUpdates)
	}

	return m.rollForwardExisting(ctx, matrixByDay, onchainByDay, closePrices, priceComplete, coinageUpdates)
}

func (m *btcHolderMaintenance) minSyncedCursor(ctx context.Context) string {
	var curA, curB string
	if stA, errA := m.store.getSyncState(ctx, btcOnchainJobID); errA == nil && stA.Cursor != "" {
		curA = stA.Cursor
	} else {
		_ = m.store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(day), '') FROM btc_onchain_daily`).Scan(&curA)
	}
	if stB, errB := m.store.getSyncState(ctx, btcCoinageJobID); errB == nil && stB.Cursor != "" {
		curB = stB.Cursor
	} else {
		_ = m.store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(spend_day), '') FROM btc_spent_age_daily`).Scan(&curB)
	}
	if curA == "" || curB == "" {
		return ""
	}
	if curA < curB {
		return curA
	}
	return curB
}

func (m *btcHolderMaintenance) applyPendingBaseline(
	ctx context.Context,
	pend btcPendingBaseline,
	matrixByDay map[string]map[string]int64,
	onchainByDay map[string]BtcOnchainDay,
	closePrices map[string]float64,
	priceComplete bool,
	coinageUpdates []BtcCoinageDay,
) error {
	baseMap, err := m.store.loadBtcBaselineGeneration(ctx, pend.Generation)
	if err != nil {
		return err
	}
	if len(baseMap) == 0 {
		return fmt.Errorf("empty baseline for generation %d", pend.Generation)
	}
	t0Date, err := civil.ParseDate(pend.AsOf)
	if err != nil {
		return err
	}
	initDate := t0Date.AddDays(-1)
	initDayStr := initDate.String()

	uInit := initFromBaseline(baseMap, pend.AsOf, matrixByDay[initDayStr])

	holderByDay := make(map[string]BtcHolderDay)

	// 1. Reconstruct backward from initDate (T0 - 1) over days where matrix & rollup exist.
	uBack := uInit.clone()
	for cur := initDate; ; cur = cur.AddDays(-1) {
		dStr := cur.String()
		hm := holderMetrics(dStr, pend.Generation, uBack, closePrices, priceComplete)
		if bErr := checkBucketSigns(uBack, dStr); bErr != nil {
			blockHolderDayMetrics(&hm, bErr.Error())
		}
		oc, hasOC := onchainByDay[dStr]
		spent, hasMat := matrixByDay[dStr]
		if hasOC {
			if warn, iErr := checkIssuanceBound(oc.ClaimedIssuanceSats, oc.ScheduledIssuanceSats); iErr != nil {
				blockHolderDayMetrics(&hm, iErr.Error())
			} else if warn != "" {
				hm.Warnings = append(hm.Warnings, warn)
			}
		}
		if hasOC && hasMat {
			prevState := uBack.clone()
			applyDay(prevState, dStr, spent, oc.CreatedSats, false)
			if sErr := checkSupplyIdentity(prevState.totalSats(), uBack.totalSats(), oc.CoinbaseSats, oc.FeesSats, oc.OpretValueSats); sErr != nil {
				blockHolderDayMetrics(&hm, sErr.Error())
			}
			holderByDay[dStr] = hm
			uBack = prevState
		} else {
			holderByDay[dStr] = hm
			break
		}
	}

	// 2. Roll forward from T0 through min(coinage_cursor, rollup_cursor).
	uFwd := uInit.clone()
	finalAsOf := initDayStr
	targetCursor := m.minSyncedCursor(ctx)
	if targetCursor >= pend.AsOf {
		if targetDate, err := civil.ParseDate(targetCursor); err == nil {
			for cur := t0Date; !cur.After(targetDate); cur = cur.AddDays(1) {
				dStr := cur.String()
				oc, hasOC := onchainByDay[dStr]
				if !hasOC {
					break
				}
				spent := matrixByDay[dStr]
				prevSum := uFwd.totalSats()
				applyDay(uFwd, dStr, spent, oc.CreatedSats, true)
				nextSum := uFwd.totalSats()

				hm := holderMetrics(dStr, pend.Generation, uFwd, closePrices, priceComplete)
				if sErr := checkSupplyIdentity(prevSum, nextSum, oc.CoinbaseSats, oc.FeesSats, oc.OpretValueSats); sErr != nil {
					blockHolderDayMetrics(&hm, sErr.Error())
				}
				if warn, iErr := checkIssuanceBound(oc.ClaimedIssuanceSats, oc.ScheduledIssuanceSats); iErr != nil {
					blockHolderDayMetrics(&hm, iErr.Error())
				} else if warn != "" {
					hm.Warnings = append(hm.Warnings, warn)
				}
				if bErr := checkBucketSigns(uFwd, dStr); bErr != nil {
					blockHolderDayMetrics(&hm, bErr.Error())
				}
				holderByDay[dStr] = hm
				finalAsOf = dStr
			}
		}
	}

	holderUpdates := make([]BtcHolderDay, 0, len(holderByDay))
	for _, hm := range holderByDay {
		holderUpdates = append(holderUpdates, hm)
	}

	if err := m.store.applyHolderMaintenanceTx(ctx, &uFwd, finalAsOf, pend.Generation, true, holderUpdates, coinageUpdates, m.now().UTC()); err != nil {
		return err
	}
	_ = m.store.pruneBtcBaselineGenerations(ctx, pend.Generation)
	return nil
}

func (m *btcHolderMaintenance) rollForwardExisting(
	ctx context.Context,
	matrixByDay map[string]map[string]int64,
	onchainByDay map[string]BtcOnchainDay,
	closePrices map[string]float64,
	priceComplete bool,
	coinageUpdates []BtcCoinageDay,
) error {
	asOf, err := m.store.getBtcUTXOStateAsOf(ctx)
	if err != nil || asOf == "" {
		if len(coinageUpdates) > 0 {
			return m.store.applyHolderMaintenanceTx(ctx, nil, "", 0, false, nil, coinageUpdates, m.now().UTC())
		}
		return err
	}
	genStr, _, _ := m.store.getBtcKV(ctx, btcUTXOStateGenerationKey)
	gen, _ := strconv.ParseInt(genStr, 10, 64)
	if gen <= 0 {
		if latestGen, ok, _ := m.store.latestBtcBaselineGeneration(ctx); ok {
			gen = latestGen
		} else {
			gen = 1
		}
	}

	targetCursor := m.minSyncedCursor(ctx)
	if targetCursor <= asOf {
		if len(coinageUpdates) > 0 {
			return m.store.applyHolderMaintenanceTx(ctx, nil, "", 0, false, nil, coinageUpdates, m.now().UTC())
		}
		return nil
	}

	asOfDate, err1 := civil.ParseDate(asOf)
	targetDate, err2 := civil.ParseDate(targetCursor)
	if err1 != nil || err2 != nil {
		return nil
	}

	u, err := m.store.loadBtcUTXOState(ctx)
	if err != nil {
		return err
	}

	var holderUpdates []BtcHolderDay
	finalAsOf := asOf
	for cur := asOfDate.AddDays(1); !cur.After(targetDate); cur = cur.AddDays(1) {
		dStr := cur.String()
		oc, hasOC := onchainByDay[dStr]
		if !hasOC {
			break
		}
		spent := matrixByDay[dStr]
		prevSum := u.totalSats()
		applyDay(u, dStr, spent, oc.CreatedSats, true)
		nextSum := u.totalSats()

		hm := holderMetrics(dStr, gen, u, closePrices, priceComplete)
		if sErr := checkSupplyIdentity(prevSum, nextSum, oc.CoinbaseSats, oc.FeesSats, oc.OpretValueSats); sErr != nil {
			blockHolderDayMetrics(&hm, sErr.Error())
		}
		if warn, iErr := checkIssuanceBound(oc.ClaimedIssuanceSats, oc.ScheduledIssuanceSats); iErr != nil {
			blockHolderDayMetrics(&hm, iErr.Error())
		} else if warn != "" {
			hm.Warnings = append(hm.Warnings, warn)
		}
		if bErr := checkBucketSigns(u, dStr); bErr != nil {
			blockHolderDayMetrics(&hm, bErr.Error())
		}
		holderUpdates = append(holderUpdates, hm)
		finalAsOf = dStr
	}

	if finalAsOf == asOf && len(holderUpdates) == 0 && len(coinageUpdates) == 0 {
		return nil
	}
	var statePtr *utxoState
	if finalAsOf != asOf {
		statePtr = &u
	}
	return m.store.applyHolderMaintenanceTx(ctx, statePtr, finalAsOf, gen, false, holderUpdates, coinageUpdates, m.now().UTC())
}

func (s *riskStore) loadBtcUTXOState(ctx context.Context) (utxoState, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT create_day, sats FROM btc_utxo_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u := make(utxoState)
	for rows.Next() {
		var cd string
		var sats int64
		if err := rows.Scan(&cd, &sats); err != nil {
			return nil, err
		}
		if sats != 0 {
			u[cd] = sats
		}
	}
	return u, rows.Err()
}

func (s *riskStore) loadBtcOnchainDaysMap(ctx context.Context) (map[string]BtcOnchainDay, error) {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM btc_onchain_daily`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]BtcOnchainDay)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var d BtcOnchainDay
		if err := json.Unmarshal([]byte(p), &d); err != nil {
			return nil, err
		}
		out[d.Date] = d
	}
	return out, rows.Err()
}

func (s *riskStore) applyHolderMaintenanceTx(
	ctx context.Context,
	newState *utxoState,
	newAsOf string,
	gen int64,
	clearPending bool,
	holderDays []BtcHolderDay,
	coinageDays []BtcCoinageDay,
	now time.Time,
) error {
	if err := s.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if newState != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM btc_utxo_state`); err != nil {
			return err
		}
		stStmt, err := tx.PrepareContext(ctx, `INSERT INTO btc_utxo_state (create_day, sats) VALUES (?, ?)`)
		if err != nil {
			return err
		}
		defer stStmt.Close()
		for cd, sats := range *newState {
			if sats == 0 {
				continue
			}
			if _, err := stStmt.ExecContext(ctx, cd, sats); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO btc_kv (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, btcUTXOStateAsOfKey, newAsOf); err != nil {
			return err
		}
		if gen > 0 {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO btc_kv (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, btcUTXOStateGenerationKey, strconv.FormatInt(gen, 10)); err != nil {
				return err
			}
		}
	}

	if clearPending {
		if _, err := tx.ExecContext(ctx, `DELETE FROM btc_kv WHERE k = ?`, btcBaselinePendingKey); err != nil {
			return err
		}
	}

	at := now.UTC().Format(time.RFC3339)

	if len(holderDays) > 0 {
		hStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_holder_daily (day, version, payload, baseline_generation, computed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  version = excluded.version,
  payload = excluded.payload,
  baseline_generation = excluded.baseline_generation,
  computed_at = excluded.computed_at`)
		if err != nil {
			return err
		}
		defer hStmt.Close()
		for _, hd := range holderDays {
			p, err := json.Marshal(hd)
			if err != nil {
				return err
			}
			if _, err := hStmt.ExecContext(ctx, hd.Date, btcHolderSchemaVersion, string(p), hd.BaselineGeneration, at); err != nil {
				return err
			}
		}
	}

	if len(coinageDays) > 0 {
		cStmt, err := tx.PrepareContext(ctx, `
INSERT INTO btc_coinage_daily (day, version, payload, computed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  version = excluded.version,
  payload = excluded.payload,
  computed_at = excluded.computed_at`)
		if err != nil {
			return err
		}
		defer cStmt.Close()
		for _, cd := range coinageDays {
			p, err := json.Marshal(cd)
			if err != nil {
				return err
			}
			if _, err := cStmt.ExecContext(ctx, cd.Date, btcCoinAgeSchemaVersion, string(p), at); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
