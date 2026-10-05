package main

// HTTP handlers for the Crypto Pulse Open Data dashboard:
//
//	GET /api/opendata/crypto/pulse?days=7|30|90|365
//	GET /api/opendata/crypto/fees?days=7|30|90|365     (Task 4)
//	GET /api/opendata/crypto/whales?days=7|30|90&chain=btc|eth (Task 6)
//	GET /api/opendata/crypto/tokens?days=7|30          (Task 8)
//	GET /api/opendata/crypto/mining?days=7|30|90|365
//	GET /api/opendata/crypto/spot                      (Coinbase proxy, not BigQuery)
//	GET /api/opendata/crypto/gas-pulse                 (72h, 7 chains; opendata_crypto_gas_handlers.go)
//	GET /api/opendata/crypto/gas-live                  (BTC/TRON live bars, 30s; opendata_crypto_gas_live_handlers.go)
//
// Each endpoint is fetched lazily by its tab, cached 10 minutes, and guarded
// by singleflight so concurrent tab opens run one BigQuery round each.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"cloud.google.com/go/civil"
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

// getCachedBtcBlocks shares BTC block stats between /pulse and /fees.
func (h *APIHandler) getCachedBtcBlocks(ctx context.Context, start, end civil.Date) ([]CryptoBlockRow, error) {
	qKey := fmt.Sprintf("opendata:crypto:q:btc_blocks:%s:%s", start, end)
	if cached, ok := h.cache.Get(qKey); ok {
		return cached.([]CryptoBlockRow), nil
	}
	v, err, _ := cryptoFlight.Do(qKey, func() (any, error) {
		rows, err := h.bq.GetCryptoBlockStats(ctx, "btc", start, end)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []CryptoBlockRow{}
		}
		h.cache.SetWithTTL(qKey, rows, cryptoTTL(end, time.Now().UTC()))
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]CryptoBlockRow), nil
}

// getCachedBtcCoinbase shares BTC coinbase revenue between /fees and /mining.
func (h *APIHandler) getCachedBtcCoinbase(ctx context.Context, start, end civil.Date) ([]BtcCoinbaseRow, error) {
	qKey := fmt.Sprintf("opendata:crypto:q:btc_coinbase:%s:%s", start, end)
	if cached, ok := h.cache.Get(qKey); ok {
		return cached.([]BtcCoinbaseRow), nil
	}
	v, err, _ := cryptoFlight.Do(qKey, func() (any, error) {
		rows, err := h.bq.GetBtcCoinbase(ctx, start, end)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []BtcCoinbaseRow{}
		}
		h.cache.SetWithTTL(qKey, rows, cryptoTTL(end, time.Now().UTC()))
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]BtcCoinbaseRow), nil
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
		ttl := cryptoTTL(end, time.Now().UTC())
		if len(data.Warnings) > 0 {
			ttl = cryptoPartialTTL
		}
		h.cache.SetWithTTL(key, &data, ttl)
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
		if btcFeeErr != nil {
			slog.Warn("crypto fees: btc query failed", "error", btcFeeErr)
			data.Warnings = append(data.Warnings, "btc_fees: "+btcFeeErr.Error())
		} else {
			data.BTC = mergeBtcFees(btcFees, nil)
			// Populate the shared coinbase cache so /mining can reuse it without a BigQuery scan.
			cbRows := make([]BtcCoinbaseRow, 0, len(btcFees))
			for _, f := range btcFees {
				cbRows = append(cbRows, BtcCoinbaseRow{Date: f.Date, CoinbaseBTC: f.CoinbaseBTC})
			}
			cbKey := fmt.Sprintf("opendata:crypto:q:btc_coinbase:%s:%s", start, end)
			h.cache.SetWithTTL(cbKey, cbRows, cryptoTTL(end, time.Now().UTC()))
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

		ttl := cryptoTTL(end, time.Now().UTC())
		if len(data.Warnings) > 0 {
			ttl = cryptoPartialTTL
		}
		h.cache.SetWithTTL(key, &data, ttl)
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
	Days          int                `json:"days"`
	Chain         string             `json:"chain"`
	Threshold     float64            `json:"threshold"`
	Largest       []WhaleTx          `json:"largest"`
	TopReceivers  []WhaleAddress     `json:"top_receivers"`
	Trend         []WhaleTrendRow    `json:"trend"`
	Concentration []ConcentrationRow `json:"concentration"`
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

		h.cache.SetWithTTL(key, &data, cryptoTTL(end, time.Now().UTC()))
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

const cryptoTokenDefaultDays = 30

type CryptoTokensData struct {
	Days      int             `json:"days"`
	TopTokens []TokenRow      `json:"top_tokens"`
	Daily     []TokenDailyRow `json:"daily"`
	Contracts []ContractRow   `json:"contracts"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// mergeTokenDaily zips transfer counts with native tx counts over the union
// of dates (sorted), zero-filling either side. Inputs are not mutated.
func mergeTokenDaily(transfers []TokenDailyRow, native []CryptoActivityRow) []TokenDailyRow {
	transferByDate := make(map[string]int64, len(transfers))
	nativeByDate := make(map[string]int64, len(native))
	dates := make(map[string]bool)
	for _, r := range transfers {
		transferByDate[r.Date] = r.Transfers
		dates[r.Date] = true
	}
	for _, r := range native {
		nativeByDate[r.Date] = r.TxCount
		dates[r.Date] = true
	}
	sorted := make([]string, 0, len(dates))
	for d := range dates {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	out := make([]TokenDailyRow, 0, len(sorted))
	for _, d := range sorted {
		out = append(out, TokenDailyRow{Date: d, Transfers: transferByDate[d], NativeTxs: nativeByDate[d]})
	}
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
	baseKey := fmt.Sprintf("opendata:crypto:tokens:%d", days)
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

		data := CryptoTokensData{
			Days:      days,
			TopTokens: []TokenRow{},
			Daily:     []TokenDailyRow{},
			Contracts: []ContractRow{},
		}
		var (
			topTokens    []TokenRow
			transfers    []TokenDailyRow
			native       []CryptoActivityRow
			contracts    []ContractRow
			topErr       error
			transfersErr error
			nativeErr    error
			contractsErr error
		)

		done := make(chan struct{}, 4)
		go func() {
			topTokens, topErr = h.bq.GetTokenTop(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			transfers, transfersErr = h.bq.GetTokenDaily(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			native, nativeErr = h.bq.GetEthNativeTxs(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			contracts, contractsErr = h.bq.GetContractsDaily(ctx, start, end)
			done <- struct{}{}
		}()
		for i := 0; i < 4; i++ {
			<-done
		}

		if topErr != nil && transfersErr != nil && contractsErr != nil {
			return nil, fmt.Errorf("tokens top: %v; daily: %v; contracts: %w", topErr, transfersErr, contractsErr)
		}
		if topErr != nil {
			slog.Warn("crypto tokens: top tokens query failed", "error", topErr)
			data.Warnings = append(data.Warnings, "top_tokens: "+topErr.Error())
		} else if topTokens != nil {
			data.TopTokens = topTokens
		}
		if transfersErr != nil {
			slog.Warn("crypto tokens: token daily query failed", "error", transfersErr)
			data.Warnings = append(data.Warnings, "token_daily: "+transfersErr.Error())
		}
		if nativeErr != nil {
			slog.Warn("crypto tokens: native txs query failed", "error", nativeErr)
			data.Warnings = append(data.Warnings, "native_txs: "+nativeErr.Error())
		}
		if contractsErr != nil {
			slog.Warn("crypto tokens: contracts query failed", "error", contractsErr)
			data.Warnings = append(data.Warnings, "contracts: "+contractsErr.Error())
		} else if contracts != nil {
			data.Contracts = contracts
		}

		data.Daily = mergeTokenDaily(transfers, native)
		ttl := cryptoTTL(end, time.Now().UTC())
		if len(data.Warnings) > 0 {
			ttl = cryptoPartialTTL
		}
		h.cache.SetWithTTL(key, &data, ttl)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /mining ---

// BtcMiningRow is one day's mining economics inputs. HashrateEhs is the
// implied network hashrate in EH/s; RevenueBTC is total coinbase output
// (subsidy + fees). The per-rig break-even math stays in the frontend so
// price/electricity inputs recompute without re-querying.
type BtcMiningRow struct {
	Date        string  `json:"date"`
	Blocks      int64   `json:"blocks"`
	HashrateEhs float64 `json:"hashrate_ehs"`
	RevenueBTC  float64 `json:"revenue_btc"`
}

type CryptoMiningData struct {
	Days  int            `json:"days"`
	Daily []BtcMiningRow `json:"daily"`
}

// mergeBtcMining joins daily block stats with coinbase revenue by date and
// derives hashrate = difficulty * 2^32 * blocks / 86400 (using the actual
// daily block count reflects Poisson block-finding luck, ~±8% 1σ at 144
// blocks/day; the frontend KPI smooths this with a 7-day average). Inputs are
// not mutated; days missing from blocks are dropped (no hashrate to show).
func mergeBtcMining(blocks []BtcMiningBlockRow, coinbase []BtcCoinbaseRow) []BtcMiningRow {
	const twoTo32 = 4294967296.0
	revenue := make(map[string]float64, len(coinbase))
	for _, c := range coinbase {
		revenue[c.Date] = c.CoinbaseBTC
	}
	out := make([]BtcMiningRow, 0, len(blocks))
	for _, b := range blocks {
		hs := b.Difficulty * twoTo32 * float64(b.Blocks) / 86400 / 1e18
		out = append(out, BtcMiningRow{
			Date:        b.Date,
			Blocks:      b.Blocks,
			HashrateEhs: hs,
			RevenueBTC:  revenue[b.Date],
		})
	}
	return out
}

func (h *APIHandler) CryptoMining(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPulseDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	start, end := cryptoWindowAt(days, now)
	baseKey := fmt.Sprintf("opendata:crypto:mining:%d", days)
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

		var (
			blocks      []BtcMiningBlockRow
			coinbase    []BtcCoinbaseRow
			blocksErr   error
			coinbaseErr error
		)

		done := make(chan struct{}, 2)
		go func() {
			blocks, blocksErr = h.bq.GetBtcMiningBlocks(ctx, start, end)
			done <- struct{}{}
		}()
		go func() {
			coinbase, coinbaseErr = h.getCachedBtcCoinbase(ctx, start, end)
			done <- struct{}{}
		}()
		<-done
		<-done

		if blocksErr != nil {
			return nil, blocksErr
		}
		if coinbaseErr != nil {
			return nil, coinbaseErr
		}

		data := CryptoMiningData{Days: days, Daily: mergeBtcMining(blocks, coinbase)}
		h.cache.SetWithTTL(key, &data, cryptoTTL(end, time.Now().UTC()))
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /spot ---

// btcSpotURL is a var so tests can point it at an httptest server. Coinbase's
// spot endpoint is free and keyless; the 10-minute cache keeps us well under
// any rate limit and the frontend treats failure as "type the price yourself".
var (
	btcSpotURL     = "https://api.coinbase.com/v2/prices/BTC-USD/spot"
	spotURLFormat  = "https://api.coinbase.com/v2/prices/%s-USD/spot"
	spotHTTPClient = &http.Client{Timeout: 5 * time.Second}
)

type CryptoSpotData struct {
	PriceUSD float64 `json:"price_usd"`
	AsOf     string  `json:"as_of"`
	Source   string  `json:"source"`
}

func fetchBtcSpot(ctx context.Context) (*CryptoSpotData, error) {
	return fetchSpotURL(ctx, btcSpotURL)
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

func (h *APIHandler) CryptoSpot(w http.ResponseWriter, r *http.Request) {
	const key = "opendata:crypto:spot"
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}
	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		ctx, cancel := cryptoFetchContext(r)
		defer cancel()
		data, err := fetchBtcSpot(ctx)
		if err != nil {
			return nil, err
		}
		h.cache.Set(key, data)
		return data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, v)
}

