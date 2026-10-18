package main

// HTTP handlers for the Crypto Pulse Open Data dashboard:
//
//	GET /api/opendata/crypto/pulse?days=7|30|90|365
//	GET /api/opendata/crypto/fees?days=7|30|90|365     (Task 4)
//	GET /api/opendata/crypto/whales?days=7|30|90&chain=btc|eth (Task 6)
//	GET /api/opendata/crypto/tokens?days=7|30          (Task 8)
//	GET /api/opendata/crypto/gas-pulse                 (72h, 7 chains; opendata_crypto_gas_handlers.go)
//	GET /api/opendata/crypto/gas-live                  (BTC/TRON live bars, 30s; opendata_crypto_gas_live_handlers.go)
//	GET /api/opendata/crypto/prices?days=7|30|90|365   (on-chain USD from SQLite, never BigQuery; opendata_crypto_prices.go)
//
// Each endpoint is fetched lazily by its tab and guarded by singleflight so
// concurrent tab opens run one BigQuery round each. Daily responses are
// cached until the next UTC rollover once the readiness probe
// (opendata_crypto_ready.go) confirms the newest day is complete, 5 minutes
// while it is still ingesting (last_day_complete: false) and 2 minutes when
// a query failed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	cryptoDefaultDays = 90
	// cryptoAddressMaxDays caps the heavy distinct-address scans; the 365-day
	// pulse view simply omits the addresses series.
	cryptoAddressMaxDays = 90
	// cryptoIngestLag holds the daily window on the previous UTC day for 20
	// minutes past midnight so late-arriving blocks (BTC lags 2–8 minutes)
	// settle before the new day is locked into the daily cache.
	cryptoIngestLag    = 20 * time.Minute
	cryptoFetchTimeout = 2 * time.Minute
	cryptoPartialTTL   = 2 * time.Minute
)

var cryptoPulseDaysAllowed = []int{7, 30, 90, 365}

var cryptoFlight singleflight.Group

// cryptoFetchContext detaches from the triggering HTTP request's cancellation
// so one client disconnecting does not abort the shared singleflight BigQuery
// jobs for other waiters or discard already-billed results before caching.
func cryptoFetchContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), cryptoFetchTimeout)
}

func parseCryptoDays(r *http.Request, def int, allowed []int) (int, error) {
	raw := r.URL.Query().Get("days")
	if raw == "" {
		return def, nil
	}
	d, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid days %q: expected an integer", raw)
	}
	for _, a := range allowed {
		if d == a {
			return d, nil
		}
	}
	return 0, fmt.Errorf("days must be one of %v", allowed)
}

// cryptoWindow returns the half-open [start, end) day window ending at the
// latest settled UTC day boundary (accounting for cryptoIngestLag).
func cryptoWindow(days int) (start, end civil.Date) {
	return cryptoWindowAt(days, time.Now().UTC())
}

func cryptoWindowAt(days int, now time.Time) (start, end civil.Date) {
	end = civil.DateOf(now.UTC().Add(-cryptoIngestLag))
	return end.AddDays(-days), end
}

// cryptoTTL returns the cache TTL until the next daily window rollover
// (end + 1 day at 00:20 UTC), clamped to [1m, 24h].
func cryptoTTL(end civil.Date, now time.Time) time.Duration {
	nextRollover := end.AddDays(1).In(time.UTC).Add(cryptoIngestLag)
	ttl := nextRollover.Sub(now.UTC())
	if ttl < time.Minute {
		return time.Minute
	}
	if ttl > 24*time.Hour {
		return 24 * time.Hour
	}
	return ttl
}

// getCachedBtcBlocks shares BTC block stats between /pulse and /fees. Like
// the responses, the shared result outlives the rollover only once BTC's
// newest day is confirmed complete.
func (h *APIHandler) getCachedBtcBlocks(ctx context.Context, start, end civil.Date) ([]CryptoBlockRow, error) {
	qKey := fmt.Sprintf("opendata:crypto:q:btc_blocks:%s:%s", start, end)
	if cached, ok := h.cache.Get(qKey); ok {
		return cached.([]CryptoBlockRow), nil
	}
	v, err, _ := cryptoFlight.Do(qKey, func() (any, error) {
		ready := h.cryptoReadyAsync(ctx, end, "btc")
		rows, err := h.bq.GetCryptoBlockStats(ctx, "btc", start, end)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []CryptoBlockRow{}
		}
		h.cache.SetWithTTL(qKey, rows, cryptoResponseTTL(end, time.Now().UTC(), false, (<-ready).complete))
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]CryptoBlockRow), nil
}

// --- /pulse ---

type CryptoKpi struct {
	Date         string  `json:"date"`
	TxCount      int64   `json:"tx_count"`
	ValueSettled float64 `json:"value_settled"`
	FeesTotal    float64 `json:"fees_total"`
	Blocks       int64   `json:"blocks"`
	FullnessPct  float64 `json:"fullness_pct"`
}

type CryptoChainPulse struct {
	Daily     []CryptoActivityRow `json:"daily"`
	Addresses []CryptoAddressRow  `json:"addresses"`
	Blocks    []CryptoBlockRow    `json:"blocks"`
	Kpi       CryptoKpi           `json:"kpi"`
}

type CryptoPulseData struct {
	Days     int              `json:"days"`
	BTC      CryptoChainPulse `json:"btc"`
	ETH      CryptoChainPulse `json:"eth"`
	Warnings []string         `json:"warnings,omitempty"`
	// LastDayComplete is false while the newest UTC day may still be
	// ingesting (readiness probe found no row past the window end yet, or
	// the probe failed).
	LastDayComplete bool `json:"last_day_complete"`
	// LastDayProbeFailed is true when the readiness probe query itself
	// failed, so the UI can distinguish a probe error from normal ingestion lag.
	LastDayProbeFailed bool `json:"last_day_probe_failed,omitempty"`
}

// rollupCryptoKpi derives the KPI strip from the latest complete day,
// joining that day's block stats by date.
func rollupCryptoKpi(daily []CryptoActivityRow, blocks []CryptoBlockRow) CryptoKpi {
	if len(daily) == 0 {
		return CryptoKpi{}
	}
	last := daily[len(daily)-1]
	kpi := CryptoKpi{
		Date:         last.Date,
		TxCount:      last.TxCount,
		ValueSettled: last.ValueSettled,
		FeesTotal:    last.FeesTotal,
	}
	for _, b := range blocks {
		if b.Date == last.Date {
			kpi.Blocks = b.Blocks
			kpi.FullnessPct = b.FullnessPct
		}
	}
	return kpi
}

func (h *APIHandler) CryptoPulse(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPulseDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	start, end := cryptoWindowAt(days, now)
	baseKey := fmt.Sprintf("opendata:crypto:pulse:%d", days)
	key := fmt.Sprintf("%s:%s", baseKey, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	if cached, ok := h.cache.Get(baseKey); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		ctx, cancel := cryptoFetchContext(r)
		defer cancel()

		empty := func() CryptoChainPulse {
			return CryptoChainPulse{
				Daily:     []CryptoActivityRow{},
				Addresses: []CryptoAddressRow{},
				Blocks:    []CryptoBlockRow{},
			}
		}
		data := CryptoPulseData{Days: days, BTC: empty(), ETH: empty()}
		includeAddr := days <= cryptoAddressMaxDays
		ready := h.cryptoReadyAsync(ctx, end, "btc", "eth")

		type pulseResult struct {
			btcRows   []cryptoPulseRow
			btcBlocks []CryptoBlockRow
			ethRows   []cryptoPulseRow
			btcErr    error
			btcBlkErr error
			ethErr    error
		}
		var res pulseResult
		done := make(chan struct{}, 3)
		go func() {
			res.btcRows, res.btcErr = h.bq.getCryptoPulseChain(ctx, "btc", start, end, includeAddr)
			done <- struct{}{}
		}()
		go func() {
			res.btcBlocks, res.btcBlkErr = h.getCachedBtcBlocks(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			res.ethRows, res.ethErr = h.bq.getCryptoPulseChain(ctx, "eth", start, end, includeAddr)
			done <- struct{}{}
		}()
		for i := 0; i < 3; i++ {
			<-done
		}

		if res.btcErr != nil && res.ethErr != nil {
			return nil, fmt.Errorf("btc pulse: %v; eth pulse: %w", res.btcErr, res.ethErr)
		}
		if res.btcErr != nil {
			slog.Warn("crypto pulse: btc query failed", "error", res.btcErr)
			data.Warnings = append(data.Warnings, "btc: "+res.btcErr.Error())
		} else {
			for _, r := range res.btcRows {
				data.BTC.Daily = append(data.BTC.Daily, CryptoActivityRow{
					Date: r.Date, TxCount: r.TxCount, ValueSettled: r.ValueSettled, FeesTotal: r.FeesTotal,
				})
				if includeAddr {
					data.BTC.Addresses = append(data.BTC.Addresses, CryptoAddressRow{
						Date: r.Date, ActiveAddresses: r.ActiveAddresses,
					})
				}
			}
		}
		if res.btcBlkErr != nil {
			slog.Warn("crypto pulse: btc blocks query failed", "error", res.btcBlkErr)
			data.Warnings = append(data.Warnings, "btc_blocks: "+res.btcBlkErr.Error())
		} else if res.btcBlocks != nil {
			data.BTC.Blocks = res.btcBlocks
		}

		if res.ethErr != nil {
			slog.Warn("crypto pulse: eth query failed", "error", res.ethErr)
			data.Warnings = append(data.Warnings, "eth: "+res.ethErr.Error())
		} else {
			for _, r := range res.ethRows {
				if r.TxCount > 0 || r.ValueSettled > 0 || r.FeesTotal > 0 {
					data.ETH.Daily = append(data.ETH.Daily, CryptoActivityRow{
						Date: r.Date, TxCount: r.TxCount, ValueSettled: r.ValueSettled, FeesTotal: r.FeesTotal,
					})
				}
				if includeAddr && r.ActiveAddresses > 0 {
					data.ETH.Addresses = append(data.ETH.Addresses, CryptoAddressRow{
						Date: r.Date, ActiveAddresses: r.ActiveAddresses,
					})
				}
				if r.Blocks > 0 {
					data.ETH.Blocks = append(data.ETH.Blocks, CryptoBlockRow{
						Date: r.Date, Blocks: r.Blocks, FullnessPct: r.FullnessPct,
					})
				}
			}
		}

		data.BTC.Kpi = rollupCryptoKpi(data.BTC.Daily, data.BTC.Blocks)
		data.ETH.Kpi = rollupCryptoKpi(data.ETH.Daily, data.ETH.Blocks)
		st := <-ready
		data.LastDayComplete = st.complete
		data.LastDayProbeFailed = st.probeFailed
		h.cache.SetWithTTL(key, &data, cryptoResponseTTL(end, time.Now().UTC(), len(data.Warnings) > 0, data.LastDayComplete))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /fees ---

type CryptoFeesData struct {
	Days      int              `json:"days"`
	BTC       []BtcFeeRow      `json:"btc"`
	ETH       []EthFeeRow      `json:"eth"`
	BTCBlocks []CryptoBlockRow `json:"btc_blocks"`
	ETHBlocks []CryptoBlockRow `json:"eth_blocks"`
	Warnings  []string         `json:"warnings,omitempty"`
	// LastDayComplete / LastDayProbeFailed: see CryptoPulseData.
	LastDayComplete    bool `json:"last_day_complete"`
	LastDayProbeFailed bool `json:"last_day_probe_failed,omitempty"`
}

// mergeBtcFees returns new rows with SubsidyBTC = coinbase revenue − fees,
// clamped at 0. Inputs are not mutated. When coinbase is nil/empty, each row's
// inline CoinbaseBTC field (from conditional aggregation) is used.
func mergeBtcFees(fees []BtcFeeRow, coinbase []BtcCoinbaseRow) []BtcFeeRow {
	revenue := make(map[string]float64, len(coinbase))
	for _, c := range coinbase {
		revenue[c.Date] = c.CoinbaseBTC
	}
	out := make([]BtcFeeRow, 0, len(fees))
	for _, f := range fees {
		cb, ok := revenue[f.Date]
		if !ok {
			cb = f.CoinbaseBTC
		}
		if subsidy := cb - f.TotalFeesBTC; subsidy > 0 {
			f.SubsidyBTC = subsidy
		}
		out = append(out, f)
	}
	return out
}

// mergeEthFees returns new rows with the burn/tips split joined by date.
func mergeEthFees(fees []EthFeeRow, burn []EthBurnRow) []EthFeeRow {
	byDate := make(map[string]EthBurnRow, len(burn))
	for _, b := range burn {
		byDate[b.Date] = b
	}
	out := make([]EthFeeRow, 0, len(fees))
	for _, f := range fees {
		if b, ok := byDate[f.Date]; ok {
			f.BurnedETH = b.BurnedETH
			f.TipsETH = b.TipsETH
		}
		out = append(out, f)
	}
	return out
}

func (h *APIHandler) CryptoFees(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPulseDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	start, end := cryptoWindowAt(days, now)
	baseKey := fmt.Sprintf("opendata:crypto:fees:%d", days)
	key := fmt.Sprintf("%s:%s", baseKey, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	if cached, ok := h.cache.Get(baseKey); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		ctx, cancel := cryptoFetchContext(r)
		defer cancel()

		data := CryptoFeesData{
			Days:      days,
			BTC:       []BtcFeeRow{},
			ETH:       []EthFeeRow{},
			BTCBlocks: []CryptoBlockRow{},
			ETHBlocks: []CryptoBlockRow{},
		}
		var (
			btcFees      []BtcFeeRow
			btcBlocks    []CryptoBlockRow
			ethFees      []EthFeeRow
			btcFeeErr    error
			btcBlocksErr error
			ethFeeErr    error
		)
		btcReady := h.cryptoReadyAsync(ctx, end, "btc")
		ethReady := h.cryptoReadyAsync(ctx, end, "eth")

		done := make(chan struct{}, 3)
		go func() {
			btcFees, btcFeeErr = h.bq.GetBtcFees(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			btcBlocks, btcBlocksErr = h.getCachedBtcBlocks(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			ethFees, ethFeeErr = h.bq.GetEthFees(ctx, start, end)
			done <- struct{}{}
		}()
		for i := 0; i < 3; i++ {
			<-done
		}

		if btcFeeErr != nil && ethFeeErr != nil {
			return nil, fmt.Errorf("btc fees: %v; eth fees: %w", btcFeeErr, ethFeeErr)
		}
		btcSt, ethSt := <-btcReady, <-ethReady
		data.LastDayComplete = btcSt.complete && ethSt.complete
		data.LastDayProbeFailed = btcSt.probeFailed || ethSt.probeFailed
		if btcFeeErr != nil {
			slog.Warn("crypto fees: btc query failed", "error", btcFeeErr)
			data.Warnings = append(data.Warnings, "btc_fees: "+btcFeeErr.Error())
		} else {
			data.BTC = mergeBtcFees(btcFees, nil)
		}
		if btcBlocksErr != nil {
			slog.Warn("crypto fees: btc blocks query failed", "error", btcBlocksErr)
			data.Warnings = append(data.Warnings, "btc_blocks: "+btcBlocksErr.Error())
		} else if btcBlocks != nil {
			data.BTCBlocks = btcBlocks
		}
		if ethFeeErr != nil {
			slog.Warn("crypto fees: eth query failed", "error", ethFeeErr)
			data.Warnings = append(data.Warnings, "eth_fees: "+ethFeeErr.Error())
		} else if ethFees != nil {
			data.ETH = ethFees
			for _, r := range ethFees {
				data.ETHBlocks = append(data.ETHBlocks, CryptoBlockRow{
					Date: r.Date, Blocks: r.Blocks, FullnessPct: r.FullnessPct,
				})
			}
		}

		h.cache.SetWithTTL(key, &data, cryptoResponseTTL(end, time.Now().UTC(), len(data.Warnings) > 0, data.LastDayComplete))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /whales ---

var cryptoWhaleDaysAllowed = []int{7, 30, 90}

// Explorer links are built in the frontend from these hashes; only rows
// matching the chain's canonical hash shape survive (spec: validated
// server-side, dropped and logged otherwise).
var (
	btcHashRe = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	ethHashRe = regexp.MustCompile(`^0x[a-fA-F0-9]{64}$`)
)

func parseCryptoChain(r *http.Request) (string, error) {
	chain := r.URL.Query().Get("chain")
	switch chain {
	case "":
		return "btc", nil
	case "btc", "eth":
		return chain, nil
	}
	return "", fmt.Errorf("chain must be btc or eth, got %q", chain)
}

func filterWhaleTxs(chain string, txs []WhaleTx) []WhaleTx {
	re := btcHashRe
	if chain == "eth" {
		re = ethHashRe
	}
	out := make([]WhaleTx, 0, len(txs))
	for _, tx := range txs {
		if re.MatchString(tx.Hash) {
			out = append(out, tx)
		} else {
			slog.Warn("crypto: dropping whale row with malformed tx hash", "chain", chain)
		}
	}
	return out
}

type CryptoWhalesData struct {
	Days               int                `json:"days"`
	Chain              string             `json:"chain"`
	Threshold          float64            `json:"threshold"`
	Largest            []WhaleTx          `json:"largest"`
	TopReceivers       []WhaleAddress     `json:"top_receivers"`
	Trend              []WhaleTrendRow    `json:"trend"`
	Concentration      []ConcentrationRow `json:"concentration"`
	FreezeCoverageFrom string             `json:"freeze_coverage_from,omitempty"`
	// LastDayComplete / LastDayProbeFailed: see CryptoPulseData.
	LastDayComplete    bool `json:"last_day_complete"`
	LastDayProbeFailed bool `json:"last_day_probe_failed,omitempty"`
}

func (h *APIHandler) CryptoWhales(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoWhaleDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	chain, err := parseCryptoChain(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	start, end := cryptoWindowAt(days, now)
	baseKey := fmt.Sprintf("opendata:crypto:whales:%s:%d", chain, days)
	key := fmt.Sprintf("%s:%s", baseKey, end)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, h.risk.tagWhales(r.Context(), cached.(*CryptoWhalesData)))
		return
	}
	if cached, ok := h.cache.Get(baseKey); ok {
		writeJSON(w, h.risk.tagWhales(r.Context(), cached.(*CryptoWhalesData)))
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		ctx, cancel := cryptoFetchContext(r)
		defer cancel()

		threshold := whaleThresholdBTC
		if chain == "eth" {
			threshold = whaleThresholdETH
		}
		data := CryptoWhalesData{
			Days:          days,
			Chain:         chain,
			Threshold:     threshold,
			Largest:       []WhaleTx{},
			TopReceivers:  []WhaleAddress{},
			Trend:         []WhaleTrendRow{},
			Concentration: []ConcentrationRow{},
		}

		ready := h.cryptoReadyAsync(ctx, end, chain)
		bundle, err := h.bq.getCryptoWhalesBundle(ctx, chain, start, end)
		if err != nil {
			return nil, err
		}
		data.Largest = filterWhaleTxs(chain, bundle.Largest)
		if bundle.TopReceivers != nil {
			data.TopReceivers = bundle.TopReceivers
		}
		for _, d := range bundle.Daily {
			data.Trend = append(data.Trend, WhaleTrendRow{Date: d.Date, WhaleCount: d.WhaleCount})
			data.Concentration = append(data.Concentration, ConcentrationRow{Date: d.Date, Top1PctShare: d.Top1PctShare})
		}

		st := <-ready
		data.LastDayComplete = st.complete
		data.LastDayProbeFailed = st.probeFailed
		h.cache.SetWithTTL(key, &data, cryptoResponseTTL(end, time.Now().UTC(), false, data.LastDayComplete))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, h.risk.tagWhales(r.Context(), v.(*CryptoWhalesData)))
}

// --- /tokens ---

var cryptoTokenDaysAllowed = []int{7, 30}

const (
	cryptoTokenDefaultDays = 30
	tokenMetadataTTL       = 7 * 24 * time.Hour

	wstethContract      = "0x7f39c581f595b53c5cb19bd0b3f8da6c935e2ca0"
	selStEthPerToken    = "0x035faf82" // stEthPerToken()
	defaultWstETHRatio  = 1.22
	wstethRatioCacheKey = "crypto:tokens:wsteth_ratio"
	wstethRatioCacheTTL = 6 * time.Hour
)

type CryptoTokensData struct {
	Days        int                 `json:"days"`
	TopTokens   []TokenRow          `json:"top_tokens"`
	Daily       []TokenDailyRow     `json:"daily"`
	Contracts   []ContractRow       `json:"contracts"`
	MajorTokens []MajorTokenSummary `json:"major_tokens"`
	Warnings    []string            `json:"warnings,omitempty"`
	// LastDayComplete / LastDayProbeFailed: see CryptoPulseData (probes Ethereum).
	LastDayComplete    bool `json:"last_day_complete"`
	LastDayProbeFailed bool `json:"last_day_probe_failed,omitempty"`
}

// mergeTokenDaily zips transfer counts (including zero-value transfers) with
// native tx counts over the union of dates (sorted), zero-filling either side.
// Inputs are not mutated.
func mergeTokenDaily(transfers []TokenDailyRow, native []CryptoActivityRow) []TokenDailyRow {
	type pair struct {
		transfers int64
		zeroValue int64
		native    int64
	}
	byDate := make(map[string]*pair, len(transfers))
	touch := func(d string) *pair {
		p, ok := byDate[d]
		if !ok {
			p = &pair{}
			byDate[d] = p
		}
		return p
	}
	for _, r := range transfers {
		p := touch(r.Date)
		p.transfers = r.Transfers
		p.zeroValue = r.ZeroValueTransfers
	}
	for _, r := range native {
		touch(r.Date).native = r.TxCount
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	out := make([]TokenDailyRow, len(dates))
	for i, d := range dates {
		p := byDate[d]
		out[i] = TokenDailyRow{
			Date:               d,
			Transfers:          p.transfers,
			ZeroValueTransfers: p.zeroValue,
			NativeTxs:          p.native,
		}
	}
	return out
}

func isImpersonatingStablecoinSymbol(sym string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sym))
	if counterfeitSymbolRe.MatchString(upper) {
		return true
	}
	norm := normalizeMimicSymbol(sym)
	return counterfeitSymbolRe.MatchString(norm) || norm == "ETH" || norm == "WETH" || norm == "WBTC"
}

func applyTokenMetadata(top []TokenRow, meta map[string]tokenContractMeta) {
	for i := range top {
		addr := strings.ToLower(top[i].TokenAddress)
		if m, ok := meta[addr]; ok && m.Found {
			top[i].Symbol = m.Symbol
			top[i].Name = m.Name
			top[i].TokenStandard = m.TokenStandard
			top[i].Decimals = m.Decimals
			top[i].ProxyStandard = m.ProxyStandard
			top[i].CreatorAddress = m.CreatorAddress
			top[i].CreatedAt = m.CreatedAt
		}
		if allow, ok := majorTokenByAddr[addr]; ok {
			if top[i].Symbol == "" {
				top[i].Symbol = allow.Symbol
			}
			if top[i].Name == "" {
				top[i].Name = allow.Name
			}
			if top[i].Decimals == 0 {
				top[i].Decimals = allow.Decimals
			}
		}
	}
}

func enrichTopTokensRisk(ctx context.Context, store *riskStore, top []TokenRow) {
	if store == nil || len(top) == 0 {
		return
	}
	pool, _ := store.riskPoolEntries(ctx)
	fakeSet, _ := store.fakeTokenSet(ctx, "eth")

	var creators []string
	for _, t := range top {
		if t.CreatorAddress != "" {
			creators = append(creators, strings.ToLower(t.CreatorAddress))
		}
	}
	deployers, _ := store.fakeTokenDeployerHits(ctx, creators)

	for i := range top {
		addr := strings.ToLower(top[i].TokenAddress)
		_, isAllowlisted := riskTokenAllowlist[addr]

		var risk []string
		if !isAllowlisted {
			if fakeSet[addr] || isImpersonatingStablecoinSymbol(top[i].Symbol) {
				risk = append(risk, "counterfeit_token")
			}
		}
		for _, src := range pool[addr] {
			if !slices.Contains(risk, src) {
				risk = append(risk, src)
			}
		}
		top[i].Risk = risk

		if cAddr := strings.ToLower(top[i].CreatorAddress); cAddr != "" {
			var cRisk []string
			if _, ok := deployers[cAddr]; ok {
				cRisk = append(cRisk, "fake_token_deployers")
			}
			for _, src := range pool[cAddr] {
				if !slices.Contains(cRisk, src) {
					cRisk = append(cRisk, src)
				}
			}
			top[i].CreatorRisk = cRisk
		}
	}
}

// missingSettledRange returns the contiguous sub-range [missingStart, end) of
// [start, end) not yet present in storedDays.
func missingSettledRange(start, end civil.Date, storedDays map[string]bool) (civil.Date, civil.Date, bool) {
	var firstMissing, lastMissing civil.Date
	hasMissing := false
	for d := start; d.Before(end); d = d.AddDays(1) {
		if !storedDays[d.String()] {
			if !hasMissing {
				firstMissing = d
				hasMissing = true
			}
			lastMissing = d
		}
	}
	if !hasMissing {
		return civil.Date{}, civil.Date{}, false
	}
	return firstMissing, lastMissing.AddDays(1), true
}

// persistSettledTokenEconomyDays saves settled days (< end-1, or == end-1 when
// lastDayComplete is true, since end from cryptoWindowAt is an exclusive upper
// bound) into SQLite. To prevent failed queries from being saved as permanent
// zeros, token_economy_daily is only written when canSaveDaily is true (all
// three daily queries succeeded), and token_major_daily is only written when
// canSaveMajor is true (the bundle query succeeded).
func persistSettledTokenEconomyDays(
	ctx context.Context,
	store *riskStore,
	end civil.Date,
	lastDayComplete bool,
	canSaveDaily bool,
	canSaveMajor bool,
	bundleDaily []tokenEconomyDailyRow,
	native []CryptoActivityRow,
	contracts []ContractRow,
	major []tokenMajorDailyRow,
	now time.Time,
) {
	if store == nil {
		return
	}
	lastDayStr := end.AddDays(-1).String()
	isSettled := func(day string) bool {
		if day < lastDayStr {
			return true
		}
		return day == lastDayStr && lastDayComplete
	}

	if canSaveDaily {
		byDay := make(map[string]*tokenEconomyDailyRow, len(bundleDaily))
		touch := func(d string) *tokenEconomyDailyRow {
			r, ok := byDay[d]
			if !ok {
				r = &tokenEconomyDailyRow{Day: d}
				byDay[d] = r
			}
			return r
		}
		for _, b := range bundleDaily {
			r := touch(b.Day)
			r.Transfers = b.Transfers
			r.ZeroValueTransfers = b.ZeroValueTransfers
		}
		for _, n := range native {
			touch(n.Date).NativeTxs = n.TxCount
		}
		for _, c := range contracts {
			r := touch(c.Date)
			r.Contracts = c.Contracts
			r.ERC20 = c.Erc20
			r.ERC721 = c.Erc721
		}
		var dailyToStore []tokenEconomyDailyRow
		for d, r := range byDay {
			if isSettled(d) {
				dailyToStore = append(dailyToStore, *r)
			}
		}
		sort.Slice(dailyToStore, func(i, j int) bool { return dailyToStore[i].Day < dailyToStore[j].Day })
		_ = store.upsertTokenEconomyDaily(ctx, dailyToStore, now)
	}

	if canSaveMajor {
		var majorToStore []tokenMajorDailyRow
		for _, m := range major {
			if isSettled(m.Day) {
				majorToStore = append(majorToStore, m)
			}
		}
		_ = store.upsertTokenMajorDaily(ctx, majorToStore, now)
	}
}

// fetchWstETHRatio reads stEthPerToken() (uint256, 18 decimals) from the
// Ethereum wstETH contract via evmRPCFailover and caches it for 6 hours.
func (h *APIHandler) fetchWstETHRatio(ctx context.Context) (float64, string) {
	if h != nil && h.cache != nil {
		if v, ok := h.cache.Get(wstethRatioCacheKey); ok {
			return v.(float64), ""
		}
	}
	rpcs := h.payRPCs("eth")
	if len(rpcs) == 0 {
		return defaultWstETHRatio, "wsteth_ratio: using fallback ratio (no ETH RPC configured)"
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ratio, code := evmRPCFailover(cctx, rpcs, func(actx context.Context, rpcURL string) (float64, string) {
		res, err := evmRPC(actx, rpcURL, "eth_call", map[string]string{"to": wstethContract, "data": selStEthPerToken}, "latest")
		if err != nil {
			return 0, evmRPCErrCode(err)
		}
		raw, err := hexToFloat(res)
		if err != nil || raw <= 0 {
			return 0, "bad_response"
		}
		r := raw / 1e18
		if r < 1.0 || r > 3.0 {
			return 0, "bad_response"
		}
		return r, ""
	})
	if code != "" {
		return defaultWstETHRatio, "wsteth_ratio: RPC " + code + " (using fallback ratio)"
	}
	if h != nil && h.cache != nil {
		h.cache.SetWithTTL(wstethRatioCacheKey, ratio, wstethRatioCacheTTL)
	}
	return ratio, ""
}

func (h *APIHandler) loadPricesByDay(ctx context.Context, days int) map[string]onchainPriceDay {
	out := make(map[string]onchainPriceDay)
	if store := h.payRiskStore(); store != nil {
		if rows, _, err := store.loadOnchainPrices(ctx); err == nil {
			for _, r := range rows {
				out[r.Day] = r
			}
		}
	}
	if len(out) == 0 && h != nil && h.prices != nil {
		for _, r := range h.prices.snapshot(days).Rows {
			out[r.Day] = r
		}
	}
	return out
}

// resolveDayPrices returns the day's (ethUSD, btcUSD) from pricesByDay, falling
// back deterministically to the most recent prior date (or earliest available
// date) when a single day is missing. Returns hasAny=false when no prices exist.
func resolveDayPrices(pricesByDay map[string]onchainPriceDay) (func(day string) (float64, float64), bool) {
	if len(pricesByDay) == 0 {
		return func(string) (float64, float64) { return 0, 0 }, false
	}
	days := make([]string, 0, len(pricesByDay))
	for d := range pricesByDay {
		days = append(days, d)
	}
	sort.Strings(days)

	var fallbackETH, fallbackBTC float64
	for _, d := range days {
		p := pricesByDay[d]
		if p.EthUSD != nil && *p.EthUSD > 0 {
			fallbackETH = *p.EthUSD
		}
		if p.BtcUSD != nil && *p.BtcUSD > 0 {
			fallbackBTC = *p.BtcUSD
		}
	}
	if fallbackETH == 0 && fallbackBTC == 0 {
		return func(string) (float64, float64) { return 0, 0 }, false
	}
	return func(day string) (float64, float64) {
		eth, btc := fallbackETH, fallbackBTC
		if p, ok := pricesByDay[day]; ok {
			if p.EthUSD != nil && *p.EthUSD > 0 {
				eth = *p.EthUSD
			}
			if p.BtcUSD != nil && *p.BtcUSD > 0 {
				btc = *p.BtcUSD
			}
		}
		return eth, btc
	}, true
}

func tokenPriceUSD(meta majorTokenMeta, ethUSD, btcUSD, wstethRatio float64) float64 {
	if wstethRatio <= 0 {
		wstethRatio = defaultWstETHRatio
	}
	switch meta.Category {
	case "stablecoin":
		return 1.0
	case "eth":
		if ethUSD <= 0 {
			return 0
		}
		if meta.Symbol == "wstETH" {
			return ethUSD * wstethRatio
		}
		return ethUSD
	case "btc":
		if btcUSD <= 0 {
			return 0
		}
		return btcUSD
	default:
		return 0
	}
}

func rollupMajorTokens(daily []TokenDailyRow, majorRows []tokenMajorDailyRow, pricesByDay map[string]onchainPriceDay, wstethRatio float64) ([]TokenDailyRow, string) {
	priceFn, hasPrices := resolveDayPrices(pricesByDay)
	type dayUSD struct {
		stable, eth, btc float64
	}
	byDay := make(map[string]*dayUSD, len(daily))
	for _, m := range majorRows {
		meta, ok := majorTokenByAddr[strings.ToLower(m.TokenAddress)]
		if !ok {
			continue
		}
		ethP, btcP := priceFn(m.Day)
		usd := m.VolumeToken * tokenPriceUSD(meta, ethP, btcP, wstethRatio)
		d := byDay[m.Day]
		if d == nil {
			d = &dayUSD{}
			byDay[m.Day] = d
		}
		switch meta.Category {
		case "stablecoin":
			d.stable += usd
		case "eth":
			d.eth += usd
		case "btc":
			d.btc += usd
		}
	}
	out := make([]TokenDailyRow, len(daily))
	for i, r := range daily {
		out[i] = r
		if d := byDay[r.Date]; d != nil {
			out[i].StablecoinUSD = d.stable
			out[i].EthTokenUSD = d.eth
			out[i].BtcTokenUSD = d.btc
		}
	}
	var warn string
	if !hasPrices && len(majorRows) > 0 {
		warn = "onchain_prices: ETH/BTC USD prices unavailable; wrapped ETH/BTC USD volume is shown as $0"
	}
	return out, warn
}

func buildMajorTokenSummaries(majorRows []tokenMajorDailyRow, pricesByDay map[string]onchainPriceDay, wstethRatio float64) []MajorTokenSummary {
	priceFn, _ := resolveDayPrices(pricesByDay)
	byAddr := make(map[string]*MajorTokenSummary, len(majorTokenCatalog))
	for _, meta := range majorTokenCatalog {
		byAddr[meta.Address] = &MajorTokenSummary{
			TokenAddress:      meta.Address,
			Symbol:            meta.Symbol,
			Name:              meta.Name,
			Category:          meta.Category,
			MintBurnSupported: meta.MintBurnSupported,
		}
	}
	for _, r := range majorRows {
		addr := strings.ToLower(r.TokenAddress)
		meta, ok := majorTokenByAddr[addr]
		if !ok {
			continue
		}
		s := byAddr[addr]
		ethP, btcP := priceFn(r.Day)
		p := tokenPriceUSD(meta, ethP, btcP, wstethRatio)
		s.Transfers += r.Transfers
		s.ZeroValue += r.ZeroValue
		if r.Senders > s.Senders {
			s.Senders = r.Senders
		}
		if r.Receivers > s.Receivers {
			s.Receivers = r.Receivers
		}
		s.VolumeToken += r.VolumeToken
		s.VolumeUSD += r.VolumeToken * p
		if meta.MintBurnSupported {
			s.MintedToken += r.MintedToken
			s.BurnedToken += r.BurnedToken
			net := r.MintedToken - r.BurnedToken
			s.NetIssuedToken += net
			s.NetIssuedUSD += net * p
		}
	}
	out := make([]MajorTokenSummary, 0, len(majorTokenCatalog))
	for _, meta := range majorTokenCatalog {
		out = append(out, *byAddr[meta.Address])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].VolumeUSD != out[j].VolumeUSD {
			return out[i].VolumeUSD > out[j].VolumeUSD
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

func (h *APIHandler) CryptoTokens(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoTokenDefaultDays, cryptoTokenDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	start, end := cryptoWindowAt(days, now)
	startStr, endStr := start.String(), end.String()
	baseKey := fmt.Sprintf("opendata:crypto:tokens:%d", days)
	key := fmt.Sprintf("%s:%s", baseKey, endStr)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	if cached, ok := h.cache.Get(baseKey); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		if cached, ok := h.cache.Get(key); ok {
			return cached, nil
		}
		ctx, cancel := cryptoFetchContext(r)
		defer cancel()

		ready := h.cryptoReadyAsync(ctx, end, "eth")
		store := h.payRiskStore()

		// 1. Check which settled days are already stored in SQLite.
		var (
			storedDaily []tokenEconomyDailyRow
			storedMajor []tokenMajorDailyRow
		)
		storedDaySet := make(map[string]bool)
		if store != nil {
			if rows, err := store.loadTokenEconomyDaily(ctx, startStr, endStr); err == nil {
				storedDaily = rows
				for _, r := range rows {
					storedDaySet[r.Day] = true
				}
			}
			if rows, err := store.loadTokenMajorDaily(ctx, startStr, endStr); err == nil {
				storedMajor = rows
			}
		}
		missingStart, missingEnd, hasMissing := missingSettledRange(start, end, storedDaySet)

		var (
			top                          []TokenRow
			freshDaily                   []tokenEconomyDailyRow
			freshMajor                   []tokenMajorDailyRow
			freshNative                  []CryptoActivityRow
			freshContracts               []ContractRow
			topErr, bundleErr, nativeErr error
			contractsErr                 error
		)

		var g errgroup.Group
		g.Go(func() error {
			top, topErr = h.bq.GetTokenTopTransfers(ctx, start, end)
			return nil
		})
		if hasMissing {
			g.Go(func() error {
				freshDaily, freshMajor, bundleErr = h.bq.GetTokenDailyBundle(ctx, missingStart, missingEnd)
				return nil
			})
			g.Go(func() error {
				freshNative, nativeErr = h.bq.GetEthNativeTxs(ctx, missingStart, missingEnd)
				return nil
			})
			g.Go(func() error {
				freshContracts, contractsErr = h.bq.GetContractsDaily(ctx, missingStart, missingEnd)
				return nil
			})
		}
		_ = g.Wait()

		st := <-ready

		var warnings []string
		recordErr := func(label string, err error) {
			if err != nil {
				slog.Warn("crypto tokens sub-query failed", "query", label, "days", days, "error", err)
				warnings = append(warnings, label+": "+err.Error())
			}
		}
		recordErr("top_tokens", topErr)
		recordErr("token_daily_bundle", bundleErr)
		recordErr("eth_native_txs", nativeErr)
		recordErr("contracts_daily", contractsErr)
		if len(warnings) == 4 {
			return nil, fmt.Errorf("all crypto tokens queries failed: %s", strings.Join(warnings, "; "))
		}

		// 2. Persist newly fetched settled days (< end-1, or == end-1 when st.complete)
		// into SQLite only when all source queries for that table succeeded.
		if hasMissing && store != nil {
			canSaveDaily := bundleErr == nil && nativeErr == nil && contractsErr == nil
			canSaveMajor := bundleErr == nil
			persistSettledTokenEconomyDays(ctx, store, end, st.complete, canSaveDaily, canSaveMajor, freshDaily, freshNative, freshContracts, freshMajor, now)
		}

		// 3. Merge stored + fresh daily, native, contracts, and major rows.
		dailyByDay := make(map[string]tokenEconomyDailyRow, days)
		contractsByDay := make(map[string]ContractRow, days)
		for _, r := range storedDaily {
			dailyByDay[r.Day] = r
			contractsByDay[r.Day] = ContractRow{Date: r.Day, Contracts: r.Contracts, Erc20: r.ERC20, Erc721: r.ERC721}
		}
		freshNativeByDay := make(map[string]int64, len(freshNative))
		for _, n := range freshNative {
			freshNativeByDay[n.Date] = n.TxCount
		}
		for _, r := range freshDaily {
			r.NativeTxs = freshNativeByDay[r.Day]
			dailyByDay[r.Day] = r
		}
		for d, txCount := range freshNativeByDay {
			if existing, ok := dailyByDay[d]; ok {
				existing.NativeTxs = txCount
				dailyByDay[d] = existing
			} else {
				dailyByDay[d] = tokenEconomyDailyRow{Day: d, NativeTxs: txCount}
			}
		}
		for _, c := range freshContracts {
			contractsByDay[c.Date] = c
		}

		var mergedDaily []TokenDailyRow
		for d := range dailyByDay {
			r := dailyByDay[d]
			mergedDaily = append(mergedDaily, TokenDailyRow{
				Date:               r.Day,
				Transfers:          r.Transfers,
				ZeroValueTransfers: r.ZeroValueTransfers,
				NativeTxs:          r.NativeTxs,
			})
		}
		sort.Slice(mergedDaily, func(i, j int) bool { return mergedDaily[i].Date < mergedDaily[j].Date })

		var mergedContracts []ContractRow
		for _, c := range contractsByDay {
			mergedContracts = append(mergedContracts, c)
		}
		sort.Slice(mergedContracts, func(i, j int) bool { return mergedContracts[i].Date < mergedContracts[j].Date })

		majorKey := func(day, addr string) string { return day + "|" + strings.ToLower(addr) }
		majorMap := make(map[string]tokenMajorDailyRow, len(storedMajor)+len(freshMajor))
		for _, m := range storedMajor {
			majorMap[majorKey(m.Day, m.TokenAddress)] = m
		}
		for _, m := range freshMajor {
			majorMap[majorKey(m.Day, m.TokenAddress)] = m
		}
		mergedMajor := make([]tokenMajorDailyRow, 0, len(majorMap))
		for _, m := range majorMap {
			mergedMajor = append(mergedMajor, m)
		}
		sort.Slice(mergedMajor, func(i, j int) bool {
			if mergedMajor[i].Day != mergedMajor[j].Day {
				return mergedMajor[i].Day < mergedMajor[j].Day
			}
			return mergedMajor[i].TokenAddress < mergedMajor[j].TokenAddress
		})

		// 4. Resolve top_tokens metadata from SQLite cache + BigQuery tokens_latest on miss.
		if len(top) > 0 {
			addrs := make([]string, len(top))
			for i, t := range top {
				addrs[i] = strings.ToLower(t.TokenAddress)
			}
			var metaMap map[string]tokenContractMeta
			if store != nil {
				metaMap, _ = store.loadTokenMetadata(ctx, addrs, now, tokenMetadataTTL)
			}
			if metaMap == nil {
				metaMap = make(map[string]tokenContractMeta)
			}
			var missingAddrs []string
			for _, a := range addrs {
				if _, ok := metaMap[a]; !ok {
					missingAddrs = append(missingAddrs, a)
				}
			}
			if len(missingAddrs) > 0 {
				fetched, mErr := h.bq.GetTokenMetadataBatch(ctx, missingAddrs)
				if mErr != nil {
					recordErr("token_metadata", mErr)
				} else {
					for _, m := range fetched {
						metaMap[strings.ToLower(m.Address)] = m
					}
					if store != nil {
						_ = store.upsertTokenMetadataBatch(ctx, missingAddrs, fetched, now)
					}
				}
			}
			applyTokenMetadata(top, metaMap)
			enrichTopTokensRisk(ctx, store, top)
		}

		// 5. Compute USD volume & mint/burn rollups using onchain_prices_daily + wstETH ratio.
		pricesByDay := h.loadPricesByDay(ctx, days)
		wstethRatio, ratioWarn := h.fetchWstETHRatio(ctx)
		if ratioWarn != "" {
			warnings = append(warnings, ratioWarn)
		}
		var priceWarn string
		mergedDaily, priceWarn = rollupMajorTokens(mergedDaily, mergedMajor, pricesByDay, wstethRatio)
		if priceWarn != "" {
			warnings = append(warnings, priceWarn)
		}
		majorSummaries := buildMajorTokenSummaries(mergedMajor, pricesByDay, wstethRatio)

		data := CryptoTokensData{
			Days:               days,
			TopTokens:          nonNilSlice(top),
			Daily:              nonNilSlice(mergedDaily),
			Contracts:          nonNilSlice(mergedContracts),
			MajorTokens:        nonNilSlice(majorSummaries),
			Warnings:           warnings,
			LastDayComplete:    st.complete,
			LastDayProbeFailed: st.probeFailed,
		}
		h.cache.SetWithTTL(key, &data, cryptoResponseTTL(end, time.Now().UTC(), len(data.Warnings) > 0, data.LastDayComplete))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func registerCryptoRoutes(mux *http.ServeMux, api *APIHandler, prices *cryptoPriceSyncer, gzWrap func(http.HandlerFunc) http.Handler, logOnly func(http.Handler) http.Handler) {
	if api == nil {
		return
	}
	mux.Handle("/api/opendata/crypto/pulse", gzWrap(api.CryptoPulse))
	mux.Handle("/api/opendata/crypto/fees", gzWrap(api.CryptoFees))
	mux.Handle("/api/opendata/crypto/whales", gzWrap(api.CryptoWhales))
	mux.Handle("/api/opendata/crypto/tokens", gzWrap(api.CryptoTokens))
	mux.Handle("/api/opendata/crypto/tokens/live", gzWrap(api.CryptoTokensLive))
	mux.Handle("/api/opendata/crypto/gas-pulse", gzWrap(api.CryptoGasPulse))
	mux.Handle("/api/opendata/crypto/gas-live", gzWrap(api.CryptoGasLive))
	mux.Handle("/api/opendata/crypto/prices", gzWrap(cryptoPricesHandler(prices)))
	mux.Handle("/api/opendata/crypto/address-risk/lookup", gzWrap(api.AddressRiskLookup))
	mux.Handle("/api/opendata/crypto/address-risk/sources", gzWrap(api.AddressRiskSources))
	mux.Handle("/api/opendata/crypto/address-risk/overview", gzWrap(api.AddressRiskOverview))
	mux.Handle("/api/opendata/crypto/address-risk/scam-radar", gzWrap(api.AddressRiskScamRadar))
	mux.Handle("/api/opendata/crypto/address-risk/keys", logOnly(addressRiskKeysHandler(api)))
	mux.Handle("/api/opendata/crypto/address-risk/backfill", logOnly(addressRiskBackfillHandler(api)))
	mux.Handle("/api/opendata/crypto/payment-check/live", gzWrap(api.PaymentCheckLive))
	mux.Handle("/api/opendata/crypto/payment-check/history", gzWrap(api.PaymentCheckHistory))
}

// --- Coinbase spot helper (used by gas live bar) ---

var (
	spotURLFormat  = "https://api.coinbase.com/v2/prices/%s-USD/spot"
	spotHTTPClient = &http.Client{Timeout: 5 * time.Second}
)

type CryptoSpotData struct {
	PriceUSD float64 `json:"price_usd"`
	AsOf     string  `json:"as_of"`
	Source   string  `json:"source"`
}

// fetchSpot returns the Coinbase USD spot price for base (e.g. "TRX").
func fetchSpot(ctx context.Context, base string) (*CryptoSpotData, error) {
	return fetchSpotURL(ctx, fmt.Sprintf(spotURLFormat, base))
}

func fetchSpotURL(ctx context.Context, url string) (*CryptoSpotData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("spot request: %w", err)
	}
	resp, err := spotHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spot fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spot fetch: upstream status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Amount string `json:"amount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("spot decode: %w", err)
	}
	price, err := strconv.ParseFloat(body.Data.Amount, 64)
	if err != nil || price <= 0 {
		return nil, fmt.Errorf("spot decode: bad amount %q", body.Data.Amount)
	}
	return &CryptoSpotData{
		PriceUSD: price,
		AsOf:     time.Now().UTC().Format(time.RFC3339),
		Source:   "coinbase",
	}, nil
}
