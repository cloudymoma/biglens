package main

// GET /api/opendata/crypto/address-risk/overview — the dashboard under the
// lookup (spec §9). SQLite only, no BigQuery cost; cached 10 minutes and
// invalidated by the syncers. The only Address Risk endpoint that returns 503
// when the database is unavailable.

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"time"

	"cloud.google.com/go/civil"
)

const (
	riskOverviewCacheKey = "address-risk:overview"
	riskRecentEvents     = 20
	// riskTrendDailyMaxDays: shorter coverage is bucketed by day, so a fresh
	// 30-day cold start still draws a useful chart.
	riskTrendDailyMaxDays = 90
)

type riskOverviewCoverage struct {
	CoverageFrom string `json:"coverage_from"`
	Cursor       string `json:"cursor"`
	Partial      bool   `json:"partial"`
}

type riskOverviewKPIs struct {
	OFACCount          int    `json:"ofac_count"`
	USDTFrozenCount    int    `json:"usdt_frozen_count"`
	USDCFrozenCount    int    `json:"usdc_frozen_count"`
	USDTDestroyedTotal string `json:"usdt_destroyed_total"` // whole USDT, 2 decimals
	MEWCount           int    `json:"mew_darklist_count"`
}

type riskTrendPoint struct {
	Bucket     string `json:"bucket"`
	USDTFreeze int    `json:"usdt_freeze"`
	USDCFreeze int    `json:"usdc_freeze"`
	Unfreeze   int    `json:"unfreeze"`
}

type riskTrend struct {
	Granularity string           `json:"granularity"` // "day" | "month"
	Points      []riskTrendPoint `json:"points"`
}

type riskRecentEvent struct {
	TxHash      string `json:"tx_hash"`
	Token       string `json:"token"`
	Action      string `json:"action"`
	Address     string `json:"address"`
	Amount      string `json:"amount"` // destroy only: whole USDT, 2 decimals
	BlockNumber int64  `json:"block_number"`
	BlockTime   string `json:"block_time"`
}

type riskOverview struct {
	Empty        bool                 `json:"empty"` // no freeze history synced yet
	Coverage     riskOverviewCoverage `json:"coverage"`
	KPIs         riskOverviewKPIs     `json:"kpis"`
	Trend        riskTrend            `json:"trend"`
	RecentEvents []riskRecentEvent    `json:"recent_events"`
}

type trendCount struct {
	Bucket, Token, Action string
	N                     int
}

// trendGranularity picks day buckets for coverage under 90 days.
func trendGranularity(from, cursor civil.Date) string {
	if cursor.DaysSince(from)+1 < riskTrendDailyMaxDays {
		return "day"
	}
	return "month"
}

// fillTrend returns one point per bucket from coverage_from to cursor, with
// zeros for buckets without events (a category axis would otherwise hide gaps).
func fillTrend(from, cursor civil.Date, granularity string, counts []trendCount) []riskTrendPoint {
	idx := map[string]*riskTrendPoint{}
	var out []riskTrendPoint
	if granularity == "day" {
		for d := from; !d.After(cursor); d = d.AddDays(1) {
			out = append(out, riskTrendPoint{Bucket: d.String()})
		}
	} else {
		for y, m := from.Year, from.Month; y < cursor.Year || (y == cursor.Year && m <= cursor.Month); {
			out = append(out, riskTrendPoint{Bucket: fmt.Sprintf("%04d-%02d", y, int(m))})
			if m++; m > time.December {
				y, m = y+1, time.January
			}
		}
	}
	for i := range out {
		idx[out[i].Bucket] = &out[i]
	}
	for _, c := range counts {
		p := idx[c.Bucket]
		if p == nil {
			continue
		}
		switch {
		case c.Action == "unfreeze":
			p.Unfreeze += c.N
		case c.Token == "USDT":
			p.USDTFreeze += c.N
		case c.Token == "USDC":
			p.USDCFreeze += c.N
		}
	}
	return out
}

func (s *riskStore) trendCounts(ctx context.Context, granularity string) ([]trendCount, error) {
	n := 10 // YYYY-MM-DD
	if granularity == "month" {
		n = 7 // YYYY-MM
	}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(block_time, 1, ?) AS bucket, token, action, COUNT(*)
	  FROM stablecoin_events WHERE action IN ('freeze', 'unfreeze') GROUP BY bucket, token, action`, n)
	if err != nil {
		return nil, fmt.Errorf("trend: %w", err)
	}
	defer rows.Close()
	var out []trendCount
	for rows.Next() {
		var c trendCount
		if err := rows.Scan(&c.Bucket, &c.Token, &c.Action, &c.N); err != nil {
			return nil, fmt.Errorf("scan trend: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// parseBaseUnits parses a stored destroy amount; "" (non-destroy) is not ok.
func parseBaseUnits(s string) (*big.Int, bool) {
	if s == "" {
		return nil, false
	}
	return new(big.Int).SetString(s, 10)
}

func (s *riskStore) recentStablecoinEvents(ctx context.Context) ([]riskRecentEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tx_hash, token, action, address, amount, block_number, block_time
	  FROM stablecoin_events ORDER BY block_number DESC, log_index DESC LIMIT ?`, riskRecentEvents)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	defer rows.Close()
	out := []riskRecentEvent{}
	for rows.Next() {
		var e riskRecentEvent
		var amount string
		if err := rows.Scan(&e.TxHash, &e.Token, &e.Action, &e.Address, &amount, &e.BlockNumber, &e.BlockTime); err != nil {
			return nil, fmt.Errorf("scan recent: %w", err)
		}
		if n, ok := parseBaseUnits(amount); ok {
			e.Amount = formatTokenAmount(n, 6)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *riskStore) buildOverview(ctx context.Context) (*riskOverview, error) {
	states, err := s.allSyncStates(ctx)
	if err != nil {
		return nil, err
	}
	st := states[stablecoinSourceID]
	ov := &riskOverview{
		Empty:    st.Cursor == "",
		Coverage: riskOverviewCoverage{CoverageFrom: st.CoverageFrom, Cursor: st.Cursor, Partial: st.Cursor != "" && st.CoverageFrom > stablecoinFirstDay},
		KPIs:     riskOverviewKPIs{OFACCount: states["ofac"].RowCount, MEWCount: states["mew_darklist"].RowCount, USDTDestroyedTotal: "0.00"},
		Trend:    riskTrend{Granularity: "day", Points: []riskTrendPoint{}},
	}
	latest, err := s.stablecoinStates(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, l := range latest {
		if l.Action == "unfreeze" {
			continue
		}
		if l.Token == "USDT" {
			ov.KPIs.USDTFrozenCount++
		} else {
			ov.KPIs.USDCFrozenCount++
		}
	}
	totals, err := s.destroyedTotals(ctx, "")
	if err != nil {
		return nil, err
	}
	if n := totals["USDT"]; n != nil {
		ov.KPIs.USDTDestroyedTotal = formatTokenAmount(n, 6)
	}
	if !ov.Empty {
		from, err1 := civil.ParseDate(st.CoverageFrom)
		cursor, err2 := civil.ParseDate(st.Cursor)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad coverage %q..%q", st.CoverageFrom, st.Cursor)
		}
		ov.Trend.Granularity = trendGranularity(from, cursor)
		counts, err := s.trendCounts(ctx, ov.Trend.Granularity)
		if err != nil {
			return nil, err
		}
		ov.Trend.Points = fillTrend(from, cursor, ov.Trend.Granularity, counts)
	}
	if ov.RecentEvents, err = s.recentStablecoinEvents(ctx); err != nil {
		return nil, err
	}
	return ov, nil
}

func (h *APIHandler) AddressRiskOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.risk.store == nil {
		writeError(w, "address risk database unavailable", http.StatusServiceUnavailable)
		return
	}
	if cached, ok := h.cache.Get(riskOverviewCacheKey); ok {
		writeJSON(w, cached)
		return
	}
	ov, err := h.risk.store.buildOverview(r.Context())
	if err != nil {
		slog.Error("address_risk overview", "error", err)
		writeError(w, "address risk database unavailable", http.StatusServiceUnavailable)
		return
	}
	h.cache.Set(riskOverviewCacheKey, ov)
	writeJSON(w, ov)
}
