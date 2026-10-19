package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	btcDefaultMaxBody  int64 = 4 << 20  // 4 MiB default cap for JSON responses
	btcStreamMaxBody   int64 = 32 << 20 // 32 MiB cap for streamed arrays
	btcUserAgent             = "biglens/1.0"
	btcTotalMaxSupply        = 21_000_000.0
	btcDefaultBlockSec       = 600.0
)

var errBtcRedirectBlocked = errors.New("redirect blocked")

// btcAllowedRedirectHosts lists hosts that btcHTTPClient may follow a single
// redirect to (e.g., bitnodes.io -> btcnodes.io).
var btcAllowedRedirectHosts = map[string]bool{
	"btcnodes.io": true,
	"bitnodes.io": true,
}

var btcHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 1 {
			return errBtcRedirectBlocked
		}
		if strings.ToLower(req.URL.Scheme) != "https" {
			return errBtcRedirectBlocked
		}
		if port := req.URL.Port(); port != "" && port != "443" {
			return errBtcRedirectBlocked
		}
		host := strings.ToLower(req.URL.Hostname())
		if !btcAllowedRedirectHosts[host] {
			return errBtcRedirectBlocked
		}
		req.Header.Set("User-Agent", btcUserAgent)
		return nil
	},
}

// BtcSourceStatus reports the freshness, status and provenance of one data source.
type BtcSourceStatus struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"` // ok | stale | error | disabled | geo_blocked | rate_limited
	Via     string `json:"via,omitempty"`
	AsOf    string `json:"as_of,omitempty"`
	Error   string `json:"error,omitempty"`
	License string `json:"license,omitempty"`
}

// BtcBQStatus describes the state of a Bitcoin BigQuery job.
type BtcBQStatus struct {
	Job           string `json:"job"`   // btc_onchain | btc_coinage | btc_utxo_baseline
	Jobs          bool   `json:"jobs"`  // operator setting
	State         string `json:"state"` // off | cold_start | ready | retrying | stopped | budget_paused
	SyncedThrough string `json:"synced_through"`
	CoverageFrom  string `json:"coverage_from"`
	LastError     string `json:"last_error,omitempty"`
	Failures      int    `json:"failures,omitempty"`
	StopReason    string `json:"stop_reason,omitempty"`
	NextRunAfter  string `json:"next_run_after,omitempty"`
}

// BtcBaselineStatus reports the current UTXO baseline generation and handoff state.
type BtcBaselineStatus struct {
	Available       bool     `json:"available"`
	Generation      int64    `json:"generation"`
	AsOf            string   `json:"as_of"`
	BuiltAt         string   `json:"built_at"`
	Pending         bool     `json:"pending"`
	Building        bool     `json:"building,omitempty"`
	BaselineSats    int64    `json:"baseline_sats"`
	BaselineBTC     float64  `json:"baseline_btc"`
	ExpectedSats    int64    `json:"expected_sats"`
	DeltaPct        *float64 `json:"delta_pct,omitempty"`
	Suspect         bool     `json:"suspect"`
	CMSplyCurBTC    *float64 `json:"cm_sply_cur_btc,omitempty"`
	AllowUI         bool     `json:"allow_ui"`
	MinIntervalDays int      `json:"min_interval_days"`
	LastBaselineAt  string   `json:"last_baseline_at,omitempty"`
	NextAllowedAt   string   `json:"next_allowed_at,omitempty"`
	CLICommand      string   `json:"cli_command"`
}

// BtcBudgetStatus summarizes the monthly Bitcoin BigQuery budget and active reservations.
type BtcBudgetStatus struct {
	Month          string  `json:"month"`
	BudgetBytes    int64   `json:"budget_bytes"`
	BudgetGiB      float64 `json:"budget_gib"`
	SettledBytes   int64   `json:"settled_bytes"`
	MonthBilledGiB float64 `json:"month_billed_gib"`
	ReservedBytes  int64   `json:"reserved_bytes"`
	ReservedGiB    float64 `json:"reserved_gib"`
	RemainingBytes int64   `json:"remaining_bytes"`
	ExpectedGiB    float64 `json:"expected_gib"`
	Warning        string  `json:"warning,omitempty"`
}

type BtcLiveData struct {
	AsOf           string            `json:"as_of"`
	PriceUSD       *float64          `json:"price_usd"`
	Change24hPct   *float64          `json:"change_24h_pct"`
	TipHeight      *int64            `json:"tip_height"`
	TipTime        string            `json:"tip_time"`
	NextBlockSatVB float64           `json:"next_block_sat_vb"`
	NextBlockUSD   *float64          `json:"next_block_usd"`
	MempoolVMB     float64           `json:"mempool_vmb"`
	BlocksToClear  float64           `json:"blocks_to_clear"`
	Difficulty     *BtcDifficultyAdj `json:"difficulty"`
	Halving        BtcHalving        `json:"halving"`
	Hashrate7dEHs  *float64          `json:"hashrate_7d_ehs"`
	Sources        []BtcSourceStatus `json:"sources"`
}

type BtcDifficultyAdj struct {
	ProgressPct        float64 `json:"progress_pct"`
	EstChangePct       float64 `json:"est_change_pct"`
	PrevChangePct      float64 `json:"prev_change_pct"`
	RemainingBlocks    int64   `json:"remaining_blocks"`
	EstRetargetAt      string  `json:"est_retarget_at"`
	NextRetargetHeight int64   `json:"next_retarget_height"`
	AvgBlockSeconds    float64 `json:"avg_block_seconds"`
}

type BtcHalving struct {
	NextHeight      *int64   `json:"next_height"`
	BlocksRemaining *int64   `json:"blocks_remaining"`
	EstAt           *string  `json:"est_at"`
	SubsidyNowBTC   *float64 `json:"subsidy_now_btc"`
	SubsidyNextBTC  *float64 `json:"subsidy_next_btc"`
	ScheduledSupply *float64 `json:"scheduled_supply_btc"`
	PctOf21M        *float64 `json:"pct_of_21m"`
}

type btcBaselinePlan struct {
	AsOf           string  `json:"as_of"`
	EstimatedBytes int64   `json:"estimated_bytes"`
	EstimatedGiB   float64 `json:"estimated_gib"`
	EstimatedUSD   float64 `json:"estimated_usd"`
	Token          string  `json:"token,omitempty"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
}

type BtcHolderEngineStatus struct {
	State     string `json:"state"` // "ok" | "halted"
	HaltedDay string `json:"halted_day,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Since     string `json:"since,omitempty"`
	Remedy    string `json:"remedy,omitempty"`
}

// btcReadStore is the read-only SQLite interface exposed to HTTP handlers.
type btcReadStore interface {
	onchainDays(ctx context.Context, from, to string) ([]BtcOnchainDay, error)
	coinageDays(ctx context.Context, from, to string) ([]BtcCoinageDay, error)
	holderDays(ctx context.Context, from, to string) ([]BtcHolderDay, error)
	whaleRevivals(ctx context.Context, from, to string) ([]BtcWhaleRevival, error)
	jobStatus(ctx context.Context, job string) (BtcBQStatus, error)
	baselineStatus(ctx context.Context) (BtcBaselineStatus, error)
	holderEngineStatus(ctx context.Context) (BtcHolderEngineStatus, error)
	budget(ctx context.Context, now time.Time) (BtcBudgetStatus, error)
}

type btcBaselineStarter interface {
	dryRun(ctx context.Context) (btcBaselinePlan, error)
	confirm(ctx context.Context, token string) error
}

// Placeholder structs for btcReadStore methods implemented in M2 and M3.
type BtcOnchainDay struct {
	Date                  string    `json:"date"`
	Blocks                int64     `json:"blocks"`
	EmptyBlocks           int64     `json:"empty_blocks"`
	ScheduledIssuanceSats int64     `json:"scheduled_issuance_sats"`
	ClaimedIssuanceSats   int64     `json:"claimed_issuance_sats"`
	AvgDifficulty         float64   `json:"avg_difficulty"`
	ImpliedHashrateEHs    float64   `json:"implied_hashrate_ehs"`
	Txs                   int64     `json:"txs"`
	CoinbaseTxs           int64     `json:"coinbase_txs"`
	Vbytes                int64     `json:"vbytes"`
	Bytes                 int64     `json:"bytes"`
	WitnessBytes          int64     `json:"witness_bytes"`
	WitnessSharePct       float64   `json:"witness_share_pct"`
	FeesSats              int64     `json:"fees_sats"`
	CoinbaseSats          int64     `json:"coinbase_sats"`
	CreatedSats           int64     `json:"created_sats"`
	OpretValueSats        int64     `json:"opret_value_sats"`
	FeeSharePct           float64   `json:"fee_share_pct"`
	FeeP10SatVB           float64   `json:"fee_p10_sat_vb"`
	FeeP25SatVB           float64   `json:"fee_p25_sat_vb"`
	FeeP50SatVB           float64   `json:"fee_p50_sat_vb"`
	FeeP75SatVB           float64   `json:"fee_p75_sat_vb"`
	FeeP90SatVB           float64   `json:"fee_p90_sat_vb"`
	VbPayment             int64     `json:"vb_payment"`
	VbConsolidation       int64     `json:"vb_consolidation"`
	VbBatch               int64     `json:"vb_batch"`
	VbInscriptionLike     int64     `json:"vb_inscription_like"`
	VbRunes               int64     `json:"vb_runes"`
	VbOpReturn            int64     `json:"vb_op_return"`
	VbCoinbase            int64     `json:"vb_coinbase"`
	OpretOutputs          int64     `json:"opret_outputs"`
	OpretBytes            int64     `json:"opret_bytes"`
	Runestones            int64     `json:"runestones"`
	OpretLarge            int64     `json:"opret_large"`
	OutP2TR               int64     `json:"out_p2tr"`
	OutP2WPKH             int64     `json:"out_p2wpkh"`
	OutP2WSH              int64     `json:"out_p2wsh"`
	OutP2SH               int64     `json:"out_p2sh"`
	OutP2PKH              int64     `json:"out_p2pkh"`
	OutAnchor             int64     `json:"out_anchor"`
	OutAll                int64     `json:"out_all"`
	InP2TR                int64     `json:"in_p2tr"`
	InP2WPKH              int64     `json:"in_p2wpkh"`
	InP2WSH               int64     `json:"in_p2wsh"`
	InP2SH                int64     `json:"in_p2sh"`
	InP2PKH               int64     `json:"in_p2pkh"`
	InAnchor              int64     `json:"in_anchor"`
	InAll                 int64     `json:"in_all"`
	BandLt0001            int64     `json:"band_lt_0_001"`
	Band0001To001         int64     `json:"band_0_001_0_01"`
	Band001To01           int64     `json:"band_0_01_0_1"`
	Band01To1             int64     `json:"band_0_1_1"`
	Band1To10             int64     `json:"band_1_10"`
	Band10To100           int64     `json:"band_10_100"`
	BandGe100             int64     `json:"band_ge_100"`
	ActiveAddresses       int64     `json:"active_addresses"`
	FeeQ                  []float64 `json:"-"`
}

type BtcCoinageDay struct {
	Date              string             `json:"date"`
	SpentSats         int64              `json:"spent_sats"`
	CDDSatDays        int64              `json:"cdd_sat_days"`
	CDDBTCDays        float64            `json:"cdd_btc_days"`
	DormancyDays      float64            `json:"dormancy_days"`
	Revived1ySats     int64              `json:"revived_1y_sats"`
	Revived2ySats     int64              `json:"revived_2y_sats"`
	Revived5ySats     int64              `json:"revived_5y_sats"`
	SpentAgeSharesPct map[string]float64 `json:"spent_age_shares_pct"`
	SOPR              *float64           `json:"sopr"`
	SOPRAdjusted      *float64           `json:"sopr_adjusted"`
	SOPRSTH           *float64           `json:"sopr_sth"`
	SOPRLTH           *float64           `json:"sopr_lth"`
	Blocked           string             `json:"blocked,omitempty"`
}

type BtcHolderDay struct {
	Date               string             `json:"date"`
	BaselineGeneration int64              `json:"baseline_generation"`
	SupplySats         int64              `json:"supply_sats"`
	SupplyBTC          float64            `json:"supply_btc"`
	LTHSupplySats      int64              `json:"lth_supply_sats"`
	STHSupplySats      int64              `json:"sth_supply_sats"`
	HODLWavesPct       map[string]float64 `json:"hodl_waves_pct"`
	RealizedCapUSD     *float64           `json:"realized_cap_usd"`
	RealizedPriceUSD   *float64           `json:"realized_price_usd"`
	LTHRealizedPrice   *float64           `json:"lth_realized_price_usd"`
	STHRealizedPrice   *float64           `json:"sth_realized_price_usd"`
	MVRV               *float64           `json:"mvrv"`
	SupplyInProfitPct  *float64           `json:"supply_in_profit_pct"`
	Blocked            string             `json:"blocked,omitempty"`
	Warnings           []string           `json:"warnings,omitempty"`
}

type BtcWhaleRevival struct {
	SpendTx   string  `json:"spend_tx"`
	CreateDay string  `json:"create_day"`
	SpendDay  string  `json:"spend_day"`
	SpentAt   string  `json:"spent_at"`
	AgeDays   int     `json:"age_days"`
	Sats      int64   `json:"sats"`
	BTC       float64 `json:"btc"`
	Inputs    int64   `json:"inputs"`
}

type btcProviderDef struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	License string `json:"license,omitempty"`
}

// btcProviderRegistry lists the active Bitcoin providers in display order.
var btcProviderRegistry = []btcProviderDef{
	{ID: "mempool", Label: "mempool.space", License: "fair use"},
	{ID: "coinbase", Label: "Coinbase (Exchange / INTX / CDE)", License: "public market data"},
	{ID: "kraken", Label: "Kraken", License: "public market data"},
	{ID: "deribit", Label: "Deribit", License: "public market data"},
	{ID: "okx", Label: "OKX", License: "public market data"},
	{ID: "binance", Label: "Binance Futures (auto)", License: "public market data"},
	{ID: "hyperliquid", Label: "Hyperliquid", License: "public market data"},
	{ID: "coinmetrics", Label: "Coin Metrics Community", License: "CC BY-NC 4.0 (non-commercial)"},
	{ID: "coingecko", Label: "CoinGecko", License: "attribution required"},
	{ID: "alternative_me", Label: "alternative.me Fear & Greed", License: "attribution required"},
	{ID: "cftc", Label: "CFTC Commitments of Traders", License: "public domain"},
	{ID: "blockchain_com", Label: "blockchain.com", License: "public chart data"},
	{ID: "bitnodes", Label: "Bitnodes", License: "public API (≤ 4 calls/day)"},
	{ID: "dbip", Label: "DB-IP Lite", License: "CC BY 4.0"},
}

type bitcoinService struct {
	settings  btcSettings
	store     *riskStore
	read      btcReadStore
	lastGood  *btcLastGood
	prices    *btcPriceSyncer
	binanceMu sync.Mutex
	binance   *binanceAdapter
	nodes     *btcNodesSyncer
	starter   btcBaselineStarter
	projectID string
	now       func() time.Time
}

func (s *bitcoinService) getBinanceAdapter() *binanceAdapter {
	s.binanceMu.Lock()
	defer s.binanceMu.Unlock()
	if s.binance == nil {
		s.binance = newBinanceAdapter(s.store, s.now)
	} else {
		s.binance.setDeps(s.store, s.now)
	}
	return s.binance
}

func newBitcoinService(cfg *Config) *bitcoinService {
	var bc BitcoinConfig
	var projectID string
	if cfg != nil {
		bc = cfg.Bitcoin
		projectID = cfg.BigQuery.ProjectID
	}
	s := bc.settings()
	if !s.Enabled {
		return nil
	}
	return &bitcoinService{
		settings:  s,
		lastGood:  newBtcLastGood(),
		prices:    newBtcPriceSyncer(nil, s, time.Now),
		binance:   newBinanceAdapter(nil, time.Now),
		nodes:     newBtcNodesSyncer(nil, s, filepath.Join("data", "geo"), time.Now),
		projectID: projectID,
		now:       time.Now,
	}
}

func (s *bitcoinService) providerEnabled(id string) bool {
	if s == nil {
		return false
	}
	return s.settings.Providers[id]
}

func (s *bitcoinService) providerMeta(id string) btcProviderDef {
	for _, p := range btcProviderRegistry {
		if p.ID == id {
			return p
		}
	}
	return btcProviderDef{ID: id, Label: id}
}

type btcLastGoodEntry struct {
	val  any
	asOf string
}

type btcLastGood struct {
	mu sync.RWMutex
	m  map[string]btcLastGoodEntry
}

func newBtcLastGood() *btcLastGood {
	return &btcLastGood{m: make(map[string]btcLastGoodEntry)}
}

func (lg *btcLastGood) Set(key string, val any, asOf string) {
	if lg == nil {
		return
	}
	lg.mu.Lock()
	lg.m[key] = btcLastGoodEntry{val: val, asOf: asOf}
	lg.mu.Unlock()
}

func (lg *btcLastGood) Get(key string) (any, string, bool) {
	if lg == nil {
		return nil, "", false
	}
	lg.mu.RLock()
	e, ok := lg.m[key]
	lg.mu.RUnlock()
	return e.val, e.asOf, ok
}

func btcMapStatusCode(code int) string {
	switch {
	case code == http.StatusOK:
		return ""
	case code == http.StatusTooManyRequests:
		return "rate_limited"
	case code == http.StatusUnavailableForLegalReasons || code == http.StatusForbidden:
		return "geo_blocked"
	case code >= 500:
		return "5xx"
	default:
		return "bad_response"
	}
}

func btcMapTransportErr(err error) string {
	if errors.Is(err, errBtcRedirectBlocked) {
		return "bad_response"
	}
	var ue *url.Error
	if errors.As(err, &ue) && errors.Is(ue.Err, errBtcRedirectBlocked) {
		return "bad_response"
	}
	return upstreamErrCode(err)
}

// btcGetJSON fetches rawURL with a capped body and unmarshals JSON into dst.
// Returns "" on success or a short error code ("rate_limited", "geo_blocked",
// "5xx", "bad_response", "timeout", "network_error").
func btcGetJSON(ctx context.Context, rawURL string, maxBody int64, dst any) string {
	if maxBody <= 0 {
		maxBody = btcDefaultMaxBody
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "bad_response"
	}
	req.Header.Set("User-Agent", btcUserAgent)
	resp, err := btcHTTPClient.Do(req)
	if err != nil {
		return btcMapTransportErr(err)
	}
	defer resp.Body.Close()
	if code := btcMapStatusCode(resp.StatusCode); code != "" {
		return code
	}
	b, err := readCapped(resp.Body, maxBody)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "bad_response"
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return "bad_response"
	}
	return ""
}

// btcPostJSON sends a JSON POST to rawURL and unmarshals the response into dst.
func btcPostJSON(ctx context.Context, rawURL string, reqBody any, maxBody int64, dst any) string {
	if maxBody <= 0 {
		maxBody = btcDefaultMaxBody
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "bad_response"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return "bad_response"
	}
	req.Header.Set("User-Agent", btcUserAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := btcHTTPClient.Do(req)
	if err != nil {
		return btcMapTransportErr(err)
	}
	defer resp.Body.Close()
	if code := btcMapStatusCode(resp.StatusCode); code != "" {
		return code
	}
	b, err := readCapped(resp.Body, maxBody)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "bad_response"
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return "bad_response"
	}
	return ""
}

type byteCountReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (b *byteCountReader) Read(p []byte) (int, error) {
	if b.n > b.max {
		return 0, fmt.Errorf("stream exceeds %d bytes", b.max)
	}
	k, err := b.r.Read(p)
	b.n += int64(k)
	if b.n > b.max {
		return k, fmt.Errorf("stream exceeds %d bytes", b.max)
	}
	return k, err
}

// btcStreamJSONArray stream-decodes a top-level JSON array one element at a time
// without holding the entire response body in memory.
func btcStreamJSONArray[T any](ctx context.Context, rawURL string, maxBody int64, onElem func(T) error) string {
	if maxBody <= 0 {
		maxBody = btcStreamMaxBody
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "bad_response"
	}
	req.Header.Set("User-Agent", btcUserAgent)
	resp, err := btcHTTPClient.Do(req)
	if err != nil {
		return btcMapTransportErr(err)
	}
	defer resp.Body.Close()
	if code := btcMapStatusCode(resp.StatusCode); code != "" {
		return code
	}
	cr := &byteCountReader{r: resp.Body, max: maxBody}
	dec := json.NewDecoder(cr)
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "bad_response"
	}
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return "timeout"
			}
			return "bad_response"
		}
		if err := onElem(item); err != nil {
			return "bad_response"
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return "bad_response"
	}
	return ""
}

func btcSubsidySats(h int64) int64 {
	if h < 0 {
		return 0
	}
	halvings := h / 210_000
	if halvings >= 64 {
		return 0
	}
	return (50 * 100_000_000) >> halvings
}

// btcScheduledIssuance returns the satoshis issued by blocks minH..maxH inclusive.
func btcScheduledIssuance(minH, maxH int64) int64 {
	if minH < 0 {
		minH = 0
	}
	var sats int64
	for h := minH; h <= maxH; {
		last := min(maxH, (h/210_000+1)*210_000-1)
		sats += (last - h + 1) * btcSubsidySats(h)
		h = last + 1
	}
	return sats
}

// btcScheduledSupply returns the cumulative scheduled supply in satoshis at height h (inclusive).
func btcScheduledSupply(h int64) int64 {
	return btcScheduledIssuance(0, h)
}

// btcHalvingAt computes halving countdown and scheduled supply metrics at height.
// When height <= 0 (unknown tip height), all fields remain nil instead of using
// any fallback constant.
func btcHalvingAt(height int64, now time.Time, avgBlockSeconds float64) BtcHalving {
	if height <= 0 {
		return BtcHalving{}
	}
	nextHeight := (height/210_000 + 1) * 210_000
	rem := nextHeight - height
	secPerBlock := avgBlockSeconds
	if secPerBlock <= 0 {
		secPerBlock = btcDefaultBlockSec
	}
	estAt := now.UTC().Add(time.Duration(float64(rem)*secPerBlock) * time.Second).Format(time.RFC3339)
	subNow := float64(btcSubsidySats(height)) / 1e8
	subNext := float64(btcSubsidySats(nextHeight)) / 1e8
	supplyBTC := float64(btcScheduledSupply(height)) / 1e8
	pctOf21M := supplyBTC / btcTotalMaxSupply * 100
	return BtcHalving{
		NextHeight:      &nextHeight,
		BlocksRemaining: &rem,
		EstAt:           &estAt,
		SubsidyNowBTC:   &subNow,
		SubsidyNextBTC:  &subNext,
		ScheduledSupply: &supplyBTC,
		PctOf21M:        &pctOf21M,
	}
}

// btcDifficultyFromBits decodes the compact target: 65535/mantissa × 256^(29−exponent).
func btcDifficultyFromBits(bits string) (float64, error) {
	v, err := strconv.ParseUint(bits, 16, 32)
	if err != nil || len(bits) != 8 {
		return 0, fmt.Errorf("bad bits %q", bits)
	}
	exp, mant := int(v>>24), float64(v&0xffffff)
	if mant == 0 {
		return 0, fmt.Errorf("zero mantissa in %q", bits)
	}
	return 65535 / mant * math.Pow(256, float64(29-exp)), nil
}
