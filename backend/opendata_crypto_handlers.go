package main

// HTTP handlers for the Crypto Pulse Open Data dashboard:
//
//	GET /api/opendata/crypto/pulse?days=7|30|90|365
//	GET /api/opendata/crypto/fees?days=7|30|90|365     (Task 4)
//	GET /api/opendata/crypto/whales?days=7|30|90&chain=btc|eth (Task 6)
//	GET /api/opendata/crypto/tokens?days=7|30          (Task 8)
//	GET /api/opendata/crypto/mining?days=7|30|90|365
//	GET /api/opendata/crypto/spot                      (Coinbase proxy, not BigQuery)
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
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	cryptoDefaultDays = 90
	// cryptoAddressMaxDays caps the heavy distinct-address scans; the 365-day
	// pulse view simply omits the addresses series.
	cryptoAddressMaxDays = 90
)

var cryptoPulseDaysAllowed = []int{7, 30, 90, 365}

var cryptoFlight singleflight.Group

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

// cryptoWindow returns the half-open [start, end) day window ending at
// today UTC, so every returned day is complete.
func cryptoWindow(days int) (start, end civil.Date) {
	end = civil.DateOf(time.Now().UTC())
	return end.AddDays(-days), end
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
	Days int              `json:"days"`
	BTC  CryptoChainPulse `json:"btc"`
	ETH  CryptoChainPulse `json:"eth"`
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

// fetchChainPulse launches the per-chain queries on g, writing into dst.
// The addresses series is skipped beyond cryptoAddressMaxDays (cost cap).
func (h *APIHandler) fetchChainPulse(g *errgroup.Group, ctx context.Context, chain string, start, end civil.Date, days int, dst *CryptoChainPulse) {
	g.Go(func() error {
		rows, err := h.bq.GetCryptoActivity(ctx, chain, start, end)
		if err != nil {
			return fmt.Errorf("%s activity: %w", chain, err)
		}
		if rows != nil {
			dst.Daily = rows
		}
		return nil
	})
	g.Go(func() error {
		rows, err := h.bq.GetCryptoBlockStats(ctx, chain, start, end)
		if err != nil {
			return fmt.Errorf("%s blocks: %w", chain, err)
		}
		if rows != nil {
			dst.Blocks = rows
		}
		return nil
	})
	if days <= cryptoAddressMaxDays {
		g.Go(func() error {
			rows, err := h.bq.GetCryptoActiveAddresses(ctx, chain, start, end)
			if err != nil {
				return fmt.Errorf("%s addresses: %w", chain, err)
			}
			if rows != nil {
				dst.Addresses = rows
			}
			return nil
		})
	}
}

func (h *APIHandler) CryptoPulse(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPulseDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:crypto:pulse:%d", days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
		empty := func() CryptoChainPulse {
			return CryptoChainPulse{
				Daily:     []CryptoActivityRow{},
				Addresses: []CryptoAddressRow{},
				Blocks:    []CryptoBlockRow{},
			}
		}
		data := CryptoPulseData{Days: days, BTC: empty(), ETH: empty()}

		g, ctx := errgroup.WithContext(r.Context())
		h.fetchChainPulse(g, ctx, "btc", start, end, days, &data.BTC)
		h.fetchChainPulse(g, ctx, "eth", start, end, days, &data.ETH)
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.BTC.Kpi = rollupCryptoKpi(data.BTC.Daily, data.BTC.Blocks)
		data.ETH.Kpi = rollupCryptoKpi(data.ETH.Daily, data.ETH.Blocks)
		h.cache.Set(key, &data)
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
}

// mergeBtcFees returns new rows with SubsidyBTC = coinbase revenue − fees,
// clamped at 0. Inputs are not mutated.
func mergeBtcFees(fees []BtcFeeRow, coinbase []BtcCoinbaseRow) []BtcFeeRow {
	revenue := make(map[string]float64, len(coinbase))
	for _, c := range coinbase {
		revenue[c.Date] = c.CoinbaseBTC
	}
	out := make([]BtcFeeRow, 0, len(fees))
	for _, f := range fees {
		if subsidy := revenue[f.Date] - f.TotalFeesBTC; subsidy > 0 {
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

	key := fmt.Sprintf("opendata:crypto:fees:%d", days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
		data := CryptoFeesData{
			Days:      days,
			BTC:       []BtcFeeRow{},
			ETH:       []EthFeeRow{},
			BTCBlocks: []CryptoBlockRow{},
			ETHBlocks: []CryptoBlockRow{},
		}
		var btcFees []BtcFeeRow
		var btcCoinbase []BtcCoinbaseRow
		var ethFees []EthFeeRow
		var ethBurn []EthBurnRow

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() (err error) { btcFees, err = h.bq.GetBtcFees(ctx, start, end); return })
		g.Go(func() (err error) { btcCoinbase, err = h.bq.GetBtcCoinbase(ctx, start, end); return })
		g.Go(func() (err error) { ethFees, err = h.bq.GetEthFees(ctx, start, end); return })
		g.Go(func() (err error) { ethBurn, err = h.bq.GetEthBurn(ctx, start, end); return })
		g.Go(func() error {
			rows, err := h.bq.GetCryptoBlockStats(ctx, "btc", start, end)
			if rows != nil {
				data.BTCBlocks = rows
			}
			return err
		})
		g.Go(func() error {
			rows, err := h.bq.GetCryptoBlockStats(ctx, "eth", start, end)
			if rows != nil {
				data.ETHBlocks = rows
			}
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.BTC = mergeBtcFees(btcFees, btcCoinbase)
		data.ETH = mergeEthFees(ethFees, ethBurn)
		h.cache.Set(key, &data)
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

	key := fmt.Sprintf("opendata:crypto:whales:%s:%d", chain, days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
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

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetCryptoLargestTxs(ctx, chain, start, end)
			if err != nil {
				return err
			}
			data.Largest = filterWhaleTxs(chain, rows)
			return nil
		})
		g.Go(func() error {
			rows, err := h.bq.GetCryptoTopReceivers(ctx, chain, start, end)
			if rows != nil {
				data.TopReceivers = rows
			}
			return err
		})
		g.Go(func() error {
			rows, err := h.bq.GetCryptoWhaleTrend(ctx, chain, start, end)
			if rows != nil {
				data.Trend = rows
			}
			return err
		})
		g.Go(func() error {
			rows, err := h.bq.GetCryptoConcentration(ctx, chain, start, end)
			if rows != nil {
				data.Concentration = rows
			}
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// --- /tokens ---

var cryptoTokenDaysAllowed = []int{7, 30}

const cryptoTokenDefaultDays = 30

type CryptoTokensData struct {
	Days      int             `json:"days"`
	TopTokens []TokenRow      `json:"top_tokens"`
	Daily     []TokenDailyRow `json:"daily"`
	Contracts []ContractRow   `json:"contracts"`
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

	key := fmt.Sprintf("opendata:crypto:tokens:%d", days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
		data := CryptoTokensData{
			Days:      days,
			TopTokens: []TokenRow{},
			Daily:     []TokenDailyRow{},
			Contracts: []ContractRow{},
		}
		var transfers []TokenDailyRow
		var native []CryptoActivityRow

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() error {
			rows, err := h.bq.GetTokenTop(ctx, start, end)
			if rows != nil {
				data.TopTokens = rows
			}
			return err
		})
		g.Go(func() (err error) { transfers, err = h.bq.GetTokenDaily(ctx, start, end); return })
		g.Go(func() (err error) { native, err = h.bq.GetCryptoActivity(ctx, "eth", start, end); return })
		g.Go(func() error {
			rows, err := h.bq.GetContractsDaily(ctx, start, end)
			if rows != nil {
				data.Contracts = rows
			}
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.Daily = mergeTokenDaily(transfers, native)
		h.cache.Set(key, &data)
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
// block count absorbs luck and intra-epoch hashrate growth). Inputs are not
// mutated; days missing from blocks are dropped (no hashrate to show).
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

	key := fmt.Sprintf("opendata:crypto:mining:%d", days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
		var blocks []BtcMiningBlockRow
		var coinbase []BtcCoinbaseRow

		g, ctx := errgroup.WithContext(r.Context())
		g.Go(func() (err error) { blocks, err = h.bq.GetBtcMiningBlocks(ctx, start, end); return })
		g.Go(func() (err error) { coinbase, err = h.bq.GetBtcCoinbase(ctx, start, end); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data := CryptoMiningData{Days: days, Daily: mergeBtcMining(blocks, coinbase)}
		h.cache.Set(key, &data)
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
	spotHTTPClient = &http.Client{Timeout: 5 * time.Second}
)

type CryptoSpotData struct {
	PriceUSD float64 `json:"price_usd"`
	AsOf     string  `json:"as_of"`
	Source   string  `json:"source"`
}

func fetchBtcSpot(ctx context.Context) (*CryptoSpotData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, btcSpotURL, nil)
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
		data, err := fetchBtcSpot(r.Context())
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
