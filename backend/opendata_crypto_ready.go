package main

// Completeness probe for the daily Crypto Pulse dashboards (/pulse, /fees,
// /whales, /tokens, /mining).
//
// Their window ends at the UTC day boundary 20 minutes after midnight
// (cryptoIngestLag), and the SQD-backed public datasets publish no freshness
// SLO. Before a response is cached until the next rollover, a cheap probe
// per chain checks that the transactions table already holds a row at or
// after the window end: ingestion is sequential, so such a row proves the
// previous UTC day is complete. The probe reads only block_timestamp in a
// 2-hour clustered range (dry run: ~8 MB BTC, ~2 MB ETH), runs concurrently
// with the main queries and is cached cryptoIncompleteTTL per (chain, end).
// A chain that is not ready yet (no row, or the probe failed) caches the
// response only cryptoIncompleteTTL and flags last_day_complete: false
// (plus last_day_probe_failed: true when the probe query itself errored).

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	// cryptoIncompleteTTL is how long a response whose newest UTC day is not
	// confirmed complete (and the probe result itself) stays cached.
	cryptoIncompleteTTL = 5 * time.Minute
	// cryptoReadyMaxBytesBilled caps one probe (actual: a few MB).
	cryptoReadyMaxBytesBilled int64 = 256 << 20
	// cryptoReadyTimeout keeps a slow probe from delaying the response long;
	// a timed-out probe counts as not ready.
	cryptoReadyTimeout = 30 * time.Second
	cryptoReadyKeyFm   = "opendata:crypto:ready:%s:%s"
)

// cryptoReadyTables are the per-chain tables probed for completeness.
var cryptoReadyTables = map[string]string{"btc": btcTxTable, "eth": ethTxTable}

func cryptoReadySQL(chain string) (string, error) {
	table, ok := cryptoReadyTables[chain]
	if !ok {
		return "", fmt.Errorf("unknown crypto chain %q", chain)
	}
	return `SELECT MAX(block_timestamp) AS t FROM ` + table + `
WHERE block_timestamp >= TIMESTAMP(@end_date) AND block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)`, nil
}

func cryptoReadyAtSQL(chain string) (string, error) {
	table, ok := cryptoReadyTables[chain]
	if !ok {
		return "", fmt.Errorf("unknown crypto chain %q", chain)
	}
	return `SELECT MAX(block_timestamp) AS t FROM ` + table + `
WHERE block_timestamp >= @at AND block_timestamp < TIMESTAMP_ADD(@at, INTERVAL 2 HOUR)`, nil
}

// cryptoDayComplete reports whether the newest probed row proves the UTC day
// before end complete (any row at or after end 00:00 UTC).
func cryptoDayComplete(t bigquery.NullTimestamp, end civil.Date) bool {
	return t.Valid && !t.Timestamp.Before(end.In(time.UTC))
}

// cryptoTimestampComplete reports whether the newest probed row is at or after at.
func cryptoTimestampComplete(t bigquery.NullTimestamp, at time.Time) bool {
	return t.Valid && !t.Timestamp.Before(at.UTC())
}

type cryptoReadyRow struct {
	T bigquery.NullTimestamp `bigquery:"t"`
}

// cryptoLatestRowTime runs the probe (labelled via collectRows).
func (b *BQClient) cryptoLatestRowTime(ctx context.Context, chain string, end civil.Date) (bigquery.NullTimestamp, error) {
	sql, err := cryptoReadySQL(chain)
	if err != nil {
		return bigquery.NullTimestamp{}, err
	}
	q := b.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{{Name: "end_date", Value: end}}
	q.MaxBytesBilled = cryptoReadyMaxBytesBilled
	rows, err := collectRows[cryptoReadyRow](q, ctx)
	if err != nil || len(rows) == 0 {
		return bigquery.NullTimestamp{}, err
	}
	return rows[0].T, nil
}

// cryptoLatestRowTimeAt probes whether a row exists in the 2-hour window starting at `at`.
func (b *BQClient) cryptoLatestRowTimeAt(ctx context.Context, chain string, at time.Time) (bigquery.NullTimestamp, error) {
	sql, err := cryptoReadyAtSQL(chain)
	if err != nil {
		return bigquery.NullTimestamp{}, err
	}
	q := b.client.Query(sql)
	q.Parameters = []bigquery.QueryParameter{{Name: "at", Value: at.UTC()}}
	q.MaxBytesBilled = cryptoReadyMaxBytesBilled
	rows, err := collectRows[cryptoReadyRow](q, ctx)
	if err != nil || len(rows) == 0 {
		return bigquery.NullTimestamp{}, err
	}
	return rows[0].T, nil
}

// cryptoReadyStatus captures both whether the newest UTC day before end is
// confirmed complete and whether the readiness probe query itself failed.
type cryptoReadyStatus struct {
	complete    bool
	probeFailed bool
}

// cryptoChainReadyStatus reports whether chain's UTC day before end is
// complete and whether the probe failed. The answer is cached
// cryptoIncompleteTTL per (chain, end) and shared via singleflight.
func (h *APIHandler) cryptoChainReadyStatus(ctx context.Context, chain string, end civil.Date) cryptoReadyStatus {
	key := fmt.Sprintf(cryptoReadyKeyFm, chain, end)
	if v, ok := h.cache.Get(key); ok {
		return v.(cryptoReadyStatus)
	}
	v, _, _ := cryptoFlight.Do(key, func() (any, error) {
		pctx, cancel := context.WithTimeout(ctx, cryptoReadyTimeout)
		defer cancel()
		var st cryptoReadyStatus
		if t, err := h.bq.cryptoLatestRowTime(pctx, chain, end); err != nil {
			slog.Warn("crypto: readiness probe failed", "chain", chain, "end", end.String(), "error", err)
			st.probeFailed = true
		} else {
			st.complete = cryptoDayComplete(t, end)
		}
		h.cache.SetWithTTL(key, st, cryptoIncompleteTTL)
		return st, nil
	})
	return v.(cryptoReadyStatus)
}

// cryptoReadyAsync probes chains concurrently (with each other and with the
// caller's main queries) and delivers whether all of them are complete and
// whether any probe failed.
func (h *APIHandler) cryptoReadyAsync(ctx context.Context, end civil.Date, chains ...string) <-chan cryptoReadyStatus {
	out := make(chan cryptoReadyStatus, 1)
	go func() {
		results := make(chan cryptoReadyStatus, len(chains))
		for _, c := range chains {
			go func() { results <- h.cryptoChainReadyStatus(ctx, c, end) }()
		}
		agg := cryptoReadyStatus{complete: true}
		for range chains {
			st := <-results
			agg.complete = agg.complete && st.complete
			agg.probeFailed = agg.probeFailed || st.probeFailed
		}
		out <- agg
	}()
	return out
}

// cryptoResponseTTL picks the cache TTL of a daily response: 2 minutes when a
// query failed (partial), 5 minutes while the newest day is not confirmed
// complete, otherwise until the next daily rollover.
func cryptoResponseTTL(end civil.Date, now time.Time, partial, complete bool) time.Duration {
	switch {
	case partial:
		return cryptoPartialTTL
	case !complete:
		return cryptoIncompleteTTL
	}
	return cryptoTTL(end, now)
}
