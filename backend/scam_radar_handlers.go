package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"cloud.google.com/go/civil"
)

const (
	scamRadarCacheKey   = "address-risk:scam-radar"
	scamTrendWindowDays = 30
	scamFakeWindowDays  = 7
	scamFakeTopLimit    = 10
)

type scamRadarKPIs struct {
	ETHDay             string  `json:"eth_day,omitempty"`
	ETHPoisonHits      int64   `json:"eth_poison_hits"`
	ETHPoisonVictims   int64   `json:"eth_poison_victims"`
	ETHFakeTransfers   int64   `json:"eth_fake_transfers"`
	ETHFakeContracts   int64   `json:"eth_fake_contracts"`
	TronDay            string  `json:"tron_day,omitempty"`
	TronPoisonHits     int64   `json:"tron_poison_hits"`
	TronPoisonVictims  int64   `json:"tron_poison_victims"`
	TronCandidates     int64   `json:"tron_candidates"`
	TronUSDTFreezes30d int64   `json:"tron_usdt_freezes_30d"`
	BTCDay             string  `json:"btc_day,omitempty"`
	BTCRBFPercent      float64 `json:"btc_rbf_percent"`
}

type scamTrendPoint struct {
	Day               string `json:"day"`
	HasData           bool   `json:"has_data"`
	ETHPoisonHits     int64  `json:"eth_poison_hits"`
	ETHPoisonVictims  int64  `json:"eth_poison_victims"`
	TronPoisonHits    int64  `json:"tron_poison_hits"`
	TronPoisonVictims int64  `json:"tron_poison_victims"`
	ETHFakeTransfers  int64  `json:"eth_fake_transfers"`
}

type scamFakeToken struct {
	Contract    string `json:"contract"`
	Symbol      string `json:"symbol"`
	Transfers   int64  `json:"transfers"`
	Recipients  int64  `json:"recipients"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	ExplorerURL string `json:"explorer_url"`
}

type scamRBF struct {
	Day     string  `json:"day"`
	Txs     int64   `json:"txs"`
	RBFTxs  int64   `json:"rbf_txs"`
	Percent float64 `json:"percent"`
}

type scamRadarResponse struct {
	Coverage   map[string]riskSource `json:"coverage"` // scam_eth / scam_tron / scam_btc / tron_stablecoin_logs
	KPIs       scamRadarKPIs         `json:"kpis"`
	Trend      []scamTrendPoint      `json:"trend"`       // 30 days, zero-filled with has_data
	FakeTokens []scamFakeToken       `json:"fake_tokens"` // last 7 days Top 10
	BTCRBF     *scamRBF              `json:"btc_rbf"`     // latest settled day
	Empty      bool                  `json:"empty"`
}

func invalidateAddressRiskCaches(c *Cache) {
	if c == nil {
		return
	}
	c.Delete(riskOverviewCacheKey)
	c.Delete(scamRadarCacheKey)
}

func bumpMaxDate(cur *civil.Date, has *bool, candidate string) {
	if candidate == "" {
		return
	}
	d, err := civil.ParseDate(candidate)
	if err != nil {
		return
	}
	if !*has || d.After(*cur) {
		*cur = d
		*has = true
	}
}

func scamRadarSourceInfo(id string, st syncState, now time.Time) riskSource {
	status := scamLookalikeSourceStatus(st, now)
	if id == tronStablecoinSourceID {
		status = stablecoinSourceStatusFor(st, tronStablecoinFirstDay, now)
	}
	if st.LastError != "" {
		status = "error"
	}
	return riskSource{
		ID:           id,
		Status:       status,
		Error:        st.LastError,
		LastOKAt:     st.LastOKAt,
		CoverageFrom: st.CoverageFrom,
		Cursor:       st.Cursor,
		LastError:    st.LastError,
	}
}

func (s *riskStore) buildScamRadar(ctx context.Context, now time.Time) (*scamRadarResponse, error) {
	states, err := s.allSyncStates(ctx)
	if err != nil {
		return nil, err
	}

	coverage := make(map[string]riskSource, 4)
	scamSources := []string{scamEthSourceID, scamTronSourceID, scamBTCSourceID, tronStablecoinSourceID}
	var anchor civil.Date
	hasAnchor := false

	for _, id := range scamSources {
		st := states[id]
		coverage[id] = scamRadarSourceInfo(id, st, now)
		bumpMaxDate(&anchor, &hasAnchor, st.Cursor)
	}

	statRows, err := s.db.QueryContext(ctx, `SELECT day, metric, value FROM scam_daily_stats ORDER BY day ASC`)
	if err != nil {
		return nil, fmt.Errorf("query scam_daily_stats: %w", err)
	}
	defer statRows.Close()

	statsByDay := make(map[string]map[string]float64)
	for statRows.Next() {
		var day, metric string
		var val float64
		if err := statRows.Scan(&day, &metric, &val); err != nil {
			return nil, fmt.Errorf("scan scam_daily_stats: %w", err)
		}
		if statsByDay[day] == nil {
			statsByDay[day] = make(map[string]float64)
		}
		statsByDay[day][metric] = val
		bumpMaxDate(&anchor, &hasAnchor, day)
	}
	if err := statRows.Err(); err != nil {
		return nil, err
	}

	var maxFakeLastSeen string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_seen), '') FROM scam_fake_tokens WHERE chain = 'eth'`).Scan(&maxFakeLastSeen); err != nil {
		return nil, fmt.Errorf("query max fake_tokens last_seen: %w", err)
	}
	bumpMaxDate(&anchor, &hasAnchor, maxFakeLastSeen)

	var maxTronBlockDay string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(substr(block_time, 1, 10)), '') FROM tron_stablecoin_events`).Scan(&maxTronBlockDay); err != nil {
		return nil, fmt.Errorf("query max tron_stablecoin_events day: %w", err)
	}
	bumpMaxDate(&anchor, &hasAnchor, maxTronBlockDay)

	resp := &scamRadarResponse{
		Coverage:   coverage,
		Trend:      []scamTrendPoint{},
		FakeTokens: []scamFakeToken{},
		Empty:      !hasAnchor,
	}
	if resp.Empty {
		return resp, nil
	}

	// 1. 30-day zero-filled trend ending at anchor.
	trendStart := anchor.AddDays(-(scamTrendWindowDays - 1))
	trend := make([]scamTrendPoint, 0, scamTrendWindowDays)
	for d := trendStart; !d.After(anchor); d = d.AddDays(1) {
		dayStr := d.String()
		m, ok := statsByDay[dayStr]
		pt := scamTrendPoint{
			Day:     dayStr,
			HasData: ok,
		}
		if ok {
			pt.ETHPoisonHits = int64(math.Round(m["eth_poison_hits"]))
			pt.ETHPoisonVictims = int64(math.Round(m["eth_poison_victims"]))
			pt.TronPoisonHits = int64(math.Round(m["tron_poison_hits"]))
			pt.TronPoisonVictims = int64(math.Round(m["tron_poison_victims"]))
			pt.ETHFakeTransfers = int64(math.Round(m["eth_fake_transfers"]))
		}
		trend = append(trend, pt)
	}
	resp.Trend = trend

	// 2. KPIs from latest settled day per source.
	var ethDay, tronDay, btcDay string
	for day, m := range statsByDay {
		if _, ok := m["eth_poison_hits"]; ok || m["eth_fake_transfers"] > 0 || m["eth_fake_contracts"] > 0 {
			if day > ethDay {
				ethDay = day
			}
		}
		if _, ok := m["tron_poison_hits"]; ok || m["tron_candidates"] > 0 {
			if day > tronDay {
				tronDay = day
			}
		}
		if _, ok := m["btc_txs"]; ok {
			if day > btcDay {
				btcDay = day
			}
		}
	}
	if ethDay == "" {
		ethDay = states[scamEthSourceID].Cursor
	}
	if tronDay == "" {
		tronDay = states[scamTronSourceID].Cursor
	}
	if btcDay == "" {
		btcDay = states[scamBTCSourceID].Cursor
	}

	if ethDay != "" {
		m := statsByDay[ethDay]
		resp.KPIs.ETHDay = ethDay
		resp.KPIs.ETHPoisonHits = int64(math.Round(m["eth_poison_hits"]))
		resp.KPIs.ETHPoisonVictims = int64(math.Round(m["eth_poison_victims"]))
		resp.KPIs.ETHFakeTransfers = int64(math.Round(m["eth_fake_transfers"]))
		resp.KPIs.ETHFakeContracts = int64(math.Round(m["eth_fake_contracts"]))
	}
	if tronDay != "" {
		m := statsByDay[tronDay]
		resp.KPIs.TronDay = tronDay
		resp.KPIs.TronPoisonHits = int64(math.Round(m["tron_poison_hits"]))
		resp.KPIs.TronPoisonVictims = int64(math.Round(m["tron_poison_victims"]))
		resp.KPIs.TronCandidates = int64(math.Round(m["tron_candidates"]))
	}

	// TRON USDT 30-day freezes: max of scam_daily_stats sum and tron_stablecoin_events count in the 30-day window.
	freezeCutoff := trendStart.String()
	var statFreezes int64
	for day, m := range statsByDay {
		if day >= freezeCutoff {
			statFreezes += int64(math.Round(m["tron_usdt_freezes"]))
		}
	}
	var evFreezes int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tron_stablecoin_events WHERE action = 'freeze' AND substr(block_time, 1, 10) >= ?`, freezeCutoff).Scan(&evFreezes); err != nil {
		return nil, fmt.Errorf("count tron 30d freezes: %w", err)
	}
	resp.KPIs.TronUSDTFreezes30d = max(statFreezes, evFreezes)

	// 3. BTC RBF fact card & KPI.
	if btcDay != "" {
		m := statsByDay[btcDay]
		txs := int64(math.Round(m["btc_txs"]))
		rbfTxs := int64(math.Round(m["btc_rbf_txs"]))
		if txs > 0 {
			pct := math.Round(float64(rbfTxs)*1000.0/float64(txs)) / 10.0
			resp.BTCRBF = &scamRBF{
				Day:     btcDay,
				Txs:     txs,
				RBFTxs:  rbfTxs,
				Percent: pct,
			}
			resp.KPIs.BTCDay = btcDay
			resp.KPIs.BTCRBFPercent = pct
		}
	}

	// 4. Top 10 fake tokens in the 7-day window.
	fakeAnchor := anchor
	hasFakeAnchor := false
	var candAnchor civil.Date
	bumpMaxDate(&candAnchor, &hasFakeAnchor, states[scamEthSourceID].Cursor)
	bumpMaxDate(&candAnchor, &hasFakeAnchor, maxFakeLastSeen)
	if hasFakeAnchor {
		fakeAnchor = candAnchor
	}
	cutoff7d := fakeAnchor.AddDays(-(scamFakeWindowDays - 1)).String()

	ftRows, err := s.db.QueryContext(ctx, `SELECT contract, symbol, transfers, recipients, first_seen, last_seen
	  FROM scam_fake_tokens
	  WHERE chain = 'eth' AND last_seen >= ?
	  ORDER BY transfers DESC, recipients DESC, contract ASC
	  LIMIT ?`, cutoff7d, scamFakeTopLimit)
	if err != nil {
		return nil, fmt.Errorf("query top fake tokens: %w", err)
	}
	defer ftRows.Close()

	for ftRows.Next() {
		var ft scamFakeToken
		if err := ftRows.Scan(&ft.Contract, &ft.Symbol, &ft.Transfers, &ft.Recipients, &ft.FirstSeen, &ft.LastSeen); err != nil {
			return nil, fmt.Errorf("scan fake token: %w", err)
		}
		ft.ExplorerURL = "https://etherscan.io/address/" + ft.Contract
		resp.FakeTokens = append(resp.FakeTokens, ft)
	}
	if err := ftRows.Err(); err != nil {
		return nil, err
	}

	_ = now
	return resp, nil
}

func (h *APIHandler) AddressRiskScamRadar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.risk == nil || h.risk.store == nil {
		writeError(w, "address risk database unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.cache != nil {
		if cached, ok := h.cache.Get(scamRadarCacheKey); ok {
			writeJSON(w, cached)
			return
		}
	}
	resp, err := h.risk.store.buildScamRadar(r.Context(), time.Now().UTC())
	if err != nil {
		slog.Error("address_risk scam_radar", "error", err)
		writeError(w, "address risk database unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.cache != nil {
		h.cache.Set(scamRadarCacheKey, resp)
	}
	writeJSON(w, resp)
}
