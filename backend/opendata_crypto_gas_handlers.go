package main

// GET /api/opendata/crypto/gas-pulse — the 72h Gas Pulse view
// (gas_fee_design.md §2.2, §3, §4).
//
// The response is assembled per request from independently cached pieces:
// each chain's 72h series (1h, keyed by the ingestion-safe window end), the
// BigQuery all-time base-fee records (24h) and TronGrid's energy price
// history (24h). A failing piece is never cached and only marks the chain or
// card it feeds, so one bad source cannot blank the other chains.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	gasSeriesTTL     = time.Hour
	gasSolSeriesTTL  = 4 * time.Hour
	gasRollingTTL    = 2 * time.Hour
	gasAllTimeTTL    = 24 * time.Hour
	gasAllTimeKey    = "opendata:crypto:gaspulse:alltime"
	gasTronKey       = "opendata:crypto:gaspulse:tron_energy"
	gasSeriesKeyFm   = "opendata:crypto:gaspulse:72h:%s:%s"
	gasRollingKeyFm  = "opendata:crypto:gaspulse:rolling:%s"
	// gasFetchTimeout bounds one shared fetch.
	gasFetchTimeout = 2 * time.Minute
)

type gasRollingSeries struct {
	end  time.Time
	byHr map[string]GasHourRow
}

// mergeRollingGasHours merges newly fetched hours into a rolling map and
// returns the rows falling within [start, end).
func mergeRollingGasHours(prev map[string]GasHourRow, delta []GasHourRow, start, end time.Time) (map[string]GasHourRow, []GasHourRow) {
	startStr := start.UTC().Format(gasHourLayout)
	endStr := end.UTC().Format(gasHourLayout)
	next := make(map[string]GasHourRow, len(prev)+len(delta))
	for k, r := range prev {
		if k >= startStr && k < endStr {
			next[k] = r
		}
	}
	for _, r := range delta {
		if r.HourUTC >= startStr && r.HourUTC < endStr {
			next[r.HourUTC] = r
		}
	}
	out := make([]GasHourRow, 0, len(next))
	for _, r := range next {
		out = append(out, r)
	}
	return next, out
}

// gasFetchContext detaches a shared fetch from the request that started it:
// singleflight hands the result to every waiter and BigQuery bills a submitted
// job either way, so one client disconnecting must not cancel the fetch for
// everyone and force a paid re-run.
func gasFetchContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), gasFetchTimeout)
}

type GasPulseChain struct {
	Meta         GasChainMeta `json:"meta"`
	Hours        []GasHourRow `json:"hours"`
	Stats        GasStats     `json:"stats"`
	Error        string       `json:"error,omitempty"`
	AllTime      *GasAllTime  `json:"all_time"`
	AllTimeError string       `json:"all_time_error,omitempty"`
}

type GasPulseData struct {
	WindowStart string          `json:"window_start"`
	WindowEnd   string          `json:"window_end"`
	Chains      []GasPulseChain `json:"chains"`
}

type gasSeriesResult struct {
	rows []GasHourRow
	err  error
}

// cachedFetch serves key from cache or runs fetch once (singleflight),
// caching only successful results for ttl.
func (h *APIHandler) cachedFetch(key string, ttl time.Duration, fetch func() (any, error)) (any, error) {
	if cached, ok := h.cache.Get(key); ok {
		return cached, nil
	}
	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		v, err := fetch()
		if err != nil {
			return nil, err
		}
		h.cache.SetWithTTL(key, v, ttl)
		return v, nil
	})
	return v, err
}

// gasFetchFailure marks a remembered failure in the cache.
type gasFetchFailure struct{ err error }

// cachedFetchOrBackoff is cachedFetch for expensive fetches: a failure is
// remembered for retryAfter, so repeated requests (the live bar polls every
// 30s) do not re-run it in the meantime.
func (h *APIHandler) cachedFetchOrBackoff(key string, ttl, retryAfter time.Duration, fetch func() (any, error)) (any, error) {
	if f, ok := h.cache.Get(key + ":failed"); ok {
		return nil, f.(gasFetchFailure).err
	}
	v, err := h.cachedFetch(key, ttl, fetch)
	if err != nil {
		h.cache.SetWithTTL(key+":failed", gasFetchFailure{err}, retryAfter)
	}
	return v, err
}

// buildGasPulse assembles the response in selector order. Every chain gets
// 72 buckets even when its query failed, so the frontend's axis never shifts.
func buildGasPulse(start, end time.Time, series map[string]gasSeriesResult,
	allTime map[string]GasAllTime, allTimeErr error, tron *GasAllTime, tronErr error) GasPulseData {
	data := GasPulseData{
		WindowStart: start.Format(gasHourLayout),
		WindowEnd:   end.Format(gasHourLayout),
		Chains:      make([]GasPulseChain, 0, len(gasChainOrder)),
	}
	for _, id := range gasChainOrder {
		res := series[id]
		c := GasPulseChain{Meta: gasChainMetas[id], Hours: fillGasHours(res.rows, start)}
		c.Stats = computeGasStats(c.Hours)
		if res.err != nil {
			c.Error = res.err.Error()
		}
		switch id {
		case gasChainETH, gasChainArb, gasChainOP, gasChainPoly:
			if allTimeErr != nil {
				c.AllTimeError = "all-time records unavailable: " + allTimeErr.Error()
			} else if at, ok := allTime[id]; ok {
				c.AllTime = &at
			} else {
				c.AllTimeError = "all-time records unavailable for this chain"
			}
		case gasChainTron:
			if tronErr != nil {
				c.AllTimeError = "energy price history unavailable: " + tronErr.Error()
			} else {
				c.AllTime = tron
			}
		}
		data.Chains = append(data.Chains, c)
	}
	return data
}

func (h *APIHandler) gasSeries(r *http.Request, chain string, start, end time.Time) ([]GasHourRow, error) {
	// Solana is MONTH-partitioned on block_timestamp: narrowing the window below
	// 72h scans the same monthly partition (~0.2–0.4 GB), so cache for 4 hours.
	if chain == gasChainSol {
		solBucket := end.Truncate(gasSolSeriesTTL).Format("2006010215")
		key := fmt.Sprintf(gasSeriesKeyFm, chain, solBucket)
		v, err := h.cachedFetch(key, gasSolSeriesTTL, func() (any, error) {
			ctx, cancel := gasFetchContext(r)
			defer cancel()
			rows, err := h.bq.GetGasHourly(ctx, chain, start, end)
			if err != nil {
				return nil, fmt.Errorf("%s 72h query: %w", chain, err)
			}
			return rows, nil
		})
		if err != nil {
			return nil, err
		}
		return v.([]GasHourRow), nil
	}

	key := fmt.Sprintf(gasSeriesKeyFm, chain, end.Format("2006010215"))
	v, err := h.cachedFetch(key, gasSeriesTTL, func() (any, error) {
		ctx, cancel := gasFetchContext(r)
		defer cancel()
		rollingKey := fmt.Sprintf(gasRollingKeyFm, chain)
		var prevMap map[string]GasHourRow
		qStart := start
		if prev, ok := h.cache.Get(rollingKey); ok {
			rs := prev.(gasRollingSeries)
			if rs.end.After(start) && rs.end.Before(end) && len(rs.byHr) >= gasWindowHours-4 {
				prevMap = rs.byHr
				// Re-read the last hour of the previous window to absorb late blocks.
				if overlap := rs.end.Add(-time.Hour); overlap.After(start) {
					qStart = overlap
				}
			}
		}
		rows, err := h.bq.GetGasHourly(ctx, chain, qStart, end)
		if err != nil {
			return nil, fmt.Errorf("%s 72h query: %w", chain, err)
		}
		mergedMap, mergedRows := mergeRollingGasHours(prevMap, rows, start, end)
		h.cache.SetWithTTL(rollingKey, gasRollingSeries{end: end, byHr: mergedMap}, gasRollingTTL)
		return mergedRows, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]GasHourRow), nil
}

func (h *APIHandler) gasAllTime(r *http.Request) (map[string]GasAllTime, error) {
	if cached, ok := h.cache.Get(gasAllTimeKey); ok {
		return cached.(map[string]GasAllTime), nil
	}
	if h.bq != nil && h.bq.client != nil {
		reqCtx := context.WithoutCancel(r.Context())
		go func() {
			_, _ = h.cachedFetchOrBackoff(gasAllTimeKey, gasAllTimeTTL, gasCalibrationRetryAfter, func() (any, error) {
				ctx, cancel := context.WithTimeout(reqCtx, gasFetchTimeout)
				defer cancel()
				rows, err := h.bq.GetGasAllTime(ctx)
				if err != nil {
					return nil, err
				}
				m := make(map[string]GasAllTime, len(rows))
				for _, row := range rows {
					m[row.Chain] = gasAllTimeFromRow(row)
				}
				return m, nil
			})
		}()
	}
	return currentGasAllTimeMap(), nil
}

func (h *APIHandler) gasTronAllTime(r *http.Request) (*GasAllTime, error) {
	v, err := h.cachedFetchOrBackoff(gasTronKey, gasAllTimeTTL, gasCalibrationRetryAfter, func() (any, error) {
		ctx, cancel := gasFetchContext(r)
		defer cancel()
		pts, err := fetchTronEnergyPrices(ctx)
		if err != nil {
			return nil, err
		}
		at := tronAllTime(pts)
		return &at, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*GasAllTime), nil
}

func (h *APIHandler) CryptoGasPulse(w http.ResponseWriter, r *http.Request) {
	start, end := gasWindow(time.Now())

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		series     = make(map[string]gasSeriesResult, len(gasChainOrder))
		allTime    map[string]GasAllTime
		allTimeErr error
		tron       *GasAllTime
		tronErr    error
	)
	for _, chain := range gasChainOrder {
		wg.Go(func() {
			rows, err := h.gasSeries(r, chain, start, end)
			mu.Lock()
			series[chain] = gasSeriesResult{rows: rows, err: err}
			mu.Unlock()
		})
	}
	wg.Go(func() { allTime, allTimeErr = h.gasAllTime(r) })
	wg.Go(func() { tron, tronErr = h.gasTronAllTime(r) })
	wg.Wait()

	writeJSON(w, buildGasPulse(start, end, series, allTime, allTimeErr, tron, tronErr))
}
