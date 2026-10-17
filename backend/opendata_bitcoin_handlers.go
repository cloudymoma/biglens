package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	btcLiveStatsKey   = "opendata:bitcoin:live:stats"
	btcLiveDiffKey    = "opendata:bitcoin:live:diff"
	btcLivePools1wKey = "opendata:bitcoin:live:pools:1w"

	btcLiveStatsTTL   = 30 * time.Second
	btcLiveDiffTTL    = 30 * time.Second
	btcLivePools1wTTL = 10 * time.Minute
)

type BtcMetaProvider struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	License string `json:"license,omitempty"`
}

type BtcMetaCosts struct {
	FirstDayUSD     float64 `json:"first_day_usd"`
	SteadyMonthUSD  float64 `json:"steady_month_usd"`
	UTXOBaselineUSD float64 `json:"utxo_baseline_usd"`
	Rollup1dGiB     float64 `json:"rollup_1d_gib"`
	CoinAgeRunGiB   float64 `json:"coin_age_run_gib"`
	UTXOBaselineGiB float64 `json:"utxo_baseline_gib"`
}

type BtcMetaBigQuery struct {
	Jobs           bool                   `json:"jobs"`
	DataAvailable  bool                   `json:"data_available"`
	Project        string                 `json:"project"`
	CadenceDays    int                    `json:"cadence_days"`
	BudgetGiB      float64                `json:"budget_gib"`
	MonthBilledGiB float64                `json:"month_billed_gib"`
	ReservedGiB    float64                `json:"reserved_gib"`
	BudgetWarning  string                 `json:"budget_warning,omitempty"`
	BaselineUI     bool                   `json:"baseline_ui"`
	StoppedJobs    []string               `json:"stopped_jobs,omitempty"`
	Statuses       map[string]BtcBQStatus `json:"statuses"`
	Costs          BtcMetaCosts           `json:"costs"`
}

type BtcMetaData struct {
	Providers     []BtcMetaProvider `json:"providers"`
	BigQuery      BtcMetaBigQuery   `json:"bigquery"`
	PriceCoverage BtcPriceCoverage  `json:"price_coverage"`
}

type bitcoinRoute struct {
	Pattern string
	Handler http.HandlerFunc
	Gzip    bool
}

// bitcoinRoutes returns the Bitcoin dashboard route table. Spending-invariant
// tests iterate over this slice so every new route is checked automatically.
func bitcoinRoutes(h *APIHandler) []bitcoinRoute {
	return []bitcoinRoute{
		{Pattern: "/api/opendata/bitcoin/meta", Handler: h.BitcoinMeta, Gzip: true},
		{Pattern: "/api/opendata/bitcoin/live", Handler: h.BitcoinLive, Gzip: true},
		{Pattern: "/api/opendata/bitcoin/onchain/baseline", Handler: h.BitcoinBaseline, Gzip: false},
	}
}

func registerBitcoinRoutes(mux *http.ServeMux, h *APIHandler, gzWrap func(http.HandlerFunc) http.Handler, logOnly func(http.Handler) http.Handler) {
	if h == nil || h.btc == nil {
		return
	}
	for _, rt := range bitcoinRoutes(h) {
		if rt.Gzip {
			mux.Handle(rt.Pattern, gzWrap(rt.Handler))
		} else {
			mux.Handle(rt.Pattern, logOnly(rt.Handler))
		}
	}
}

func (h *APIHandler) btcCachedSource(ctx context.Context, cacheKey string, ttl time.Duration, providerID string, fetch func(context.Context) (any, string)) (any, BtcSourceStatus) {
	s := h.btc
	meta := s.providerMeta(providerID)
	status := BtcSourceStatus{
		ID:      meta.ID,
		Label:   meta.Label,
		License: meta.License,
	}
	if !s.providerEnabled(providerID) {
		status.Status = "disabled"
		return nil, status
	}
	nowStr := s.now().UTC().Format(time.RFC3339)
	v, err := h.cachedFetch(cacheKey, ttl, func() (any, error) {
		val, code := fetch(ctx)
		if code != "" {
			return nil, fmt.Errorf("%s", code)
		}
		s.lastGood.Set(cacheKey, val, nowStr)
		return val, nil
	})
	if err == nil {
		status.Status = "ok"
		if _, asOf, ok := s.lastGood.Get(cacheKey); ok && asOf != "" {
			status.AsOf = asOf
		} else {
			status.AsOf = nowStr
		}
		return v, status
	}
	code := err.Error()
	if prev, asOf, ok := s.lastGood.Get(cacheKey); ok {
		status.Status = "stale"
		status.AsOf = asOf
		status.Error = code
		return prev, status
	}
	switch code {
	case "geo_blocked", "rate_limited":
		status.Status = code
	default:
		status.Status = "error"
	}
	status.Error = code
	return nil, status
}

func (h *APIHandler) BitcoinMeta(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	s := h.btc
	s.prices.touchAsync()

	providers := make([]BtcMetaProvider, 0, len(btcProviderRegistry))
	for _, p := range btcProviderRegistry {
		providers = append(providers, BtcMetaProvider{
			ID:      p.ID,
			Label:   p.Label,
			Enabled: s.providerEnabled(p.ID),
			License: p.License,
		})
	}

	budgetGiB := float64(s.settings.BudgetBytes) / float64(1<<30)
	statuses := map[string]BtcBQStatus{
		"btc_onchain": {Job: "btc_onchain", Jobs: s.settings.Jobs, State: "off"},
		"btc_coinage": {Job: "btc_coinage", Jobs: s.settings.Jobs, State: "off"},
	}
	var dataAvailable bool
	var monthBilledGiB, reservedGiB float64
	var budgetWarning string
	var stoppedJobs []string

	if s.read != nil {
		ctx := r.Context()
		for _, jobID := range []string{"btc_onchain", "btc_coinage"} {
			if st, err := s.read.jobStatus(ctx, jobID); err == nil {
				statuses[jobID] = st
				if st.SyncedThrough != "" {
					dataAvailable = true
				}
				if st.State == "stopped" {
					stoppedJobs = append(stoppedJobs, jobID)
				}
			}
		}
		if bs, err := s.read.baselineStatus(ctx); err == nil && bs.Available {
			dataAvailable = true
		}
		if b, err := s.read.budget(ctx, s.now().UTC()); err == nil {
			monthBilledGiB = b.MonthBilledGiB
			reservedGiB = b.ReservedGiB
			budgetWarning = b.Warning
		}
	}

	writeJSON(w, BtcMetaData{
		Providers: providers,
		BigQuery: BtcMetaBigQuery{
			Jobs:           s.settings.Jobs,
			DataAvailable:  dataAvailable,
			Project:        s.projectID,
			CadenceDays:    s.settings.CadenceDays,
			BudgetGiB:      budgetGiB,
			MonthBilledGiB: monthBilledGiB,
			ReservedGiB:    reservedGiB,
			BudgetWarning:  budgetWarning,
			BaselineUI:     s.settings.BaselineAllowUI,
			StoppedJobs:    stoppedJobs,
			Statuses:       statuses,
			Costs: BtcMetaCosts{
				FirstDayUSD:     1.49,
				SteadyMonthUSD:  2.88,
				UTXOBaselineUSD: 4.20,
				Rollup1dGiB:     0.65,
				CoinAgeRunGiB:   102.15,
				UTXOBaselineGiB: 687.30,
			},
		},
		PriceCoverage: s.prices.priceCoverage(),
	})
}

func (h *APIHandler) BitcoinLive(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	s := h.btc
	s.prices.touchAsync()
	now := s.now().UTC()
	ctx := r.Context()

	var (
		wg       sync.WaitGroup
		memRaw   *btcLiveRaw
		memErr   error
		spotVal  any
		spotSrc  BtcSourceStatus
		diffVal  any
		diffSrc  BtcSourceStatus
		poolsVal any
		poolsSrc BtcSourceStatus
	)

	if s.providerEnabled("mempool") {
		wg.Go(func() { memRaw, memErr = h.gasLiveBtc(r) })
		wg.Go(func() {
			diffVal, diffSrc = h.btcCachedSource(ctx, btcLiveDiffKey, btcLiveDiffTTL, "mempool", func(c context.Context) (any, string) {
				return fetchMempoolDifficultyAdj(c)
			})
		})
		wg.Go(func() {
			poolsVal, poolsSrc = h.btcCachedSource(ctx, btcLivePools1wKey, btcLivePools1wTTL, "mempool", func(c context.Context) (any, string) {
				return fetchMempoolPoolsSummary(c, "1w")
			})
		})
	} else {
		diffSrc = BtcSourceStatus{ID: "mempool", Label: "mempool.space", Status: "disabled", License: "fair use"}
	}

	wg.Go(func() {
		spotVal, spotSrc = h.btcCachedSource(ctx, btcLiveStatsKey, btcLiveStatsTTL, "coinbase", func(c context.Context) (any, string) {
			return fetchCoinbaseBTCStats(c)
		})
	})
	wg.Wait()

	out := BtcLiveData{
		AsOf: now.Format(time.RFC3339),
	}

	if st, ok := spotVal.(*btcSpotStats); ok && st != nil {
		p := st.PriceUSD
		out.PriceUSD = &p
		out.Change24hPct = st.Change24hPct
	} else if s.providerEnabled("mempool") {
		if mp, code := fetchMempoolBTCPrice(ctx); code == "" && mp != nil {
			out.PriceUSD = mp
		}
	}

	if memRaw != nil && memErr == nil {
		s.lastGood.Set("opendata:bitcoin:live:memraw", memRaw, out.AsOf)
	} else if prev, _, ok := s.lastGood.Get("opendata:bitcoin:live:memraw"); ok {
		memRaw, _ = prev.(*btcLiveRaw)
	}

	if memRaw != nil {
		out.NextBlockSatVB = memRaw.Fees.Fastest
		out.NextBlockUSD = btcTxUSD(memRaw.Fees.Fastest, out.PriceUSD)
		out.MempoolVMB = float64(memRaw.Mempool.Vsize) / 1e6
		out.BlocksToClear = float64(memRaw.Mempool.Vsize) / btcBlockVB
		if len(memRaw.Recent) > 0 {
			out.TipHeight = memRaw.Recent[0].Height
			out.TipTime = time.Unix(memRaw.Recent[0].Timestamp, 0).UTC().Format(time.RFC3339)
		}
	}

	avgBlockSec := btcDefaultBlockSec
	if d, ok := diffVal.(*BtcDifficultyAdj); ok && d != nil {
		out.Difficulty = d
		if d.AvgBlockSeconds > 0 {
			avgBlockSec = d.AvgBlockSeconds
		}
	}
	if p, ok := poolsVal.(*btcMempoolPoolsSummary); ok && p != nil {
		hr := p.LastEstimatedHashrate1w
		if hr <= 0 {
			hr = p.LastEstimatedHashrate
		}
		if hr > 0 {
			ehs := hr / 1e18
			out.Hashrate7dEHs = &ehs
		}
	}

	out.Halving = btcHalvingAt(out.TipHeight, now, avgBlockSec)
	out.Sources = []BtcSourceStatus{spotSrc, diffSrc}
	if poolsSrc.ID != "" && poolsSrc.Status != "ok" && diffSrc.Status == "ok" {
		out.Sources = append(out.Sources, poolsSrc)
	}
	writeJSON(w, out)
}

func (h *APIHandler) BitcoinBaseline(w http.ResponseWriter, r *http.Request) {
	if h.btc == nil {
		writeError(w, "bitcoin dashboard disabled", http.StatusNotFound)
		return
	}
	s := h.btc
	if r.Method == http.MethodGet {
		if s.read != nil {
			if st, err := s.read.baselineStatus(r.Context()); err == nil {
				writeJSON(w, st)
				return
			}
		}
		writeJSON(w, BtcBaselineStatus{
			AllowUI:         s.settings.BaselineAllowUI,
			MinIntervalDays: int(s.settings.BaselineMinInterval.Hours() / 24),
			CLICommand:      "./biglens-server --bitcoin-utxo-baseline",
		})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.settings.BaselineAllowUI {
		writeError(w, "UTXO baseline UI trigger is disabled (set bitcoin.bigquery.utxo_baseline.allow_ui: true or use --bitcoin-utxo-baseline CLI)", http.StatusForbidden)
		return
	}
	if s.starter == nil {
		writeError(w, "Bitcoin BigQuery jobs are disabled", http.StatusForbidden)
		return
	}
	writeError(w, "baseline starter not yet wired", http.StatusNotImplemented)
}
