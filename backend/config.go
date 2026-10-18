package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int    `yaml:"port"`
		Mode string `yaml:"mode"`
	} `yaml:"server"`
	BigQuery struct {
		ProjectID       string `yaml:"project_id"`
		CredentialsPath string `yaml:"credentials_path"`
	} `yaml:"bigquery"`
	Catalog struct {
		BundlePath string `yaml:"bundle_path"`
		Dataplex   struct {
			ProjectID string `yaml:"project_id"`
			Location  string `yaml:"location"`
		} `yaml:"dataplex"`
		LineageLocation string `yaml:"lineage_location"`
	} `yaml:"catalog"`
	GCPBilling struct {
		Datasets []string `yaml:"datasets"`
	} `yaml:"gcp_billing"`
	GCPResources struct {
		Projects []string `yaml:"projects"`
	} `yaml:"gcp_resources"`
	AddressRisk  AddressRiskConfig  `yaml:"address_risk,omitempty"`
	CryptoGas    CryptoGasConfig    `yaml:"crypto_gas,omitempty"`
	PaymentCheck PaymentCheckConfig `yaml:"payment_check,omitempty"`
	CryptoPrices CryptoPricesConfig `yaml:"crypto_prices,omitempty"`
	Bitcoin      BitcoinConfig      `yaml:"bitcoin,omitempty"`

	// path is where this config was loaded from, so SaveConfig can write back.
	path string
}

var defaultBlockscoutURLs = map[string]string{
	"arb":  "https://arbitrum.blockscout.com",
	"op":   "https://explorer.optimism.io",
	"base": "https://base.blockscout.com",
}

type PaymentCheckConfig struct {
	BlockscoutURLs map[string]string `yaml:"blockscout_urls,omitempty"`
	LogsMaxSpan    uint64            `yaml:"logs_max_span,omitempty"` // 0 → 10000; Task 8
}

func (c PaymentCheckConfig) blockscoutURL(chain string) string {
	if u, ok := c.BlockscoutURLs[chain]; ok {
		return u
	}
	if u := defaultBlockscoutURLs[chain]; u != "" {
		return u
	}
	if chain == "eth" {
		return blockscoutBaseURL
	}
	return ""
}

type ScamRadarConfig struct {
	InitialDays         *int `yaml:"initial_days,omitempty"`          // default 30; 0 = disabled (spec P2.0 Q2)
	RetentionDays       int  `yaml:"retention_days,omitempty"`        // default 90 (spec P2.0 Q3)
	AllowZeroCandidates bool `yaml:"allow_zero_candidates,omitempty"` // bypass per-day dust/zero-value candidate guards without disabling Scam Radar
}

// AddressRiskConfig configures the Crypto Pulse "Address Risk" tab. Every
// field has a code default because VMs provisioned before this feature have
// no address_risk section in their generated conf.yaml.
type AddressRiskConfig struct {
	DBPath     string   `yaml:"db_path,omitempty"`
	EthRPCURLs []string `yaml:"eth_rpc_urls,omitempty"`
	// InitialSyncDays is a pointer so "absent" (nil → 30) differs from an
	// explicit 0 (cold start off): existing VMs have no such key.
	InitialSyncDays *int `yaml:"initial_sync_days,omitempty"`
	// EtherscanAPIKey is optional; set and cleared from the UI via
	// UpdateConfig. Never returned by any endpoint or logged.
	EtherscanAPIKey string          `yaml:"etherscan_api_key,omitempty"`
	ScamRadar       ScamRadarConfig `yaml:"scam_radar,omitempty"`

	// BigQueryProfile: guardrails of the opt-in per-lookup BigQuery profile.
	BigQueryProfile BigQueryProfileConfig `yaml:"bigquery_profile,omitempty"`
}

// BigQueryProfileConfig holds the server-side guardrails of the opt-in
// BigQuery address profile (Address Risk, Ethereum): users switch it on per
// lookup, and every profile is one job billed to bigquery.project_id.
type BigQueryProfileConfig struct {
	Enabled        *bool `yaml:"enabled,omitempty"`          // nil → true: users may opt in
	MaxBytesBilled int64 `yaml:"max_bytes_billed,omitempty"` // ≤0 → 2 GiB per job; at most 10 GiB
	MaxPerHour     int   `yaml:"max_per_hour,omitempty"`     // ≤0 → 60 jobs/hour (global token bucket); at most 600
	TimeoutSeconds int   `yaml:"timeout_seconds,omitempty"`  // ≤0 → 20; at most 45
}

// bqProfileSettings is BigQueryProfileConfig with defaults applied.
type bqProfileSettings struct {
	Enabled        bool
	MaxBytesBilled int64
	MaxPerHour     int
	Timeout        time.Duration
}

// bigQueryProfile resolves the profile guardrails. The timeout is capped at
// 45 s so a lookup always answers within the server's 60 s WriteTimeout.
func (c AddressRiskConfig) bigQueryProfile() bqProfileSettings {
	p := c.BigQueryProfile
	s := bqProfileSettings{Enabled: p.Enabled == nil || *p.Enabled, MaxBytesBilled: p.MaxBytesBilled,
		MaxPerHour: p.MaxPerHour, Timeout: time.Duration(p.TimeoutSeconds) * time.Second}
	if s.MaxBytesBilled <= 0 {
		s.MaxBytesBilled = 2 << 30
	}
	s.MaxBytesBilled = min(s.MaxBytesBilled, 10<<30)
	if s.MaxPerHour <= 0 {
		s.MaxPerHour = 60
	}
	s.MaxPerHour = min(s.MaxPerHour, 600)
	if s.Timeout <= 0 {
		s.Timeout = 20 * time.Second
	}
	s.Timeout = min(s.Timeout, 45*time.Second)
	return s
}

// defaultRiskRPCURLs are keyless public Ethereum RPCs, tried in order for
// the Chainalysis oracle eth_call (both verified 2026-09-26).
var defaultRiskRPCURLs = []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org"}

func (c AddressRiskConfig) dbPath() string {
	if c.DBPath == "" {
		return "data/security.db"
	}
	return c.DBPath
}

// initialSyncDays is the cold-start window for USDT/USDC freeze history
// (spec D14): absent → 30, explicit 0 → off, clamped to [0, 31].
func (c AddressRiskConfig) initialSyncDays() int {
	if c.InitialSyncDays == nil {
		return 30
	}
	return min(max(*c.InitialSyncDays, 0), 31)
}

func (c AddressRiskConfig) scamRadarInitialDays() int {
	if c.ScamRadar.InitialDays == nil {
		return 30
	}
	return min(max(*c.ScamRadar.InitialDays, 0), 31)
}

func (c AddressRiskConfig) scamRadarRetentionDays() int {
	if c.ScamRadar.RetentionDays <= 0 {
		return 90
	}
	return c.ScamRadar.RetentionDays
}

func (c AddressRiskConfig) rpcURLs() []string {
	if len(c.EthRPCURLs) == 0 {
		return defaultRiskRPCURLs
	}
	return c.EthRPCURLs
}

// CryptoGasConfig overrides the keyless endpoints the Gas Pulse live bars
// call. Every field is optional; empty means the default in withDefaults.
type CryptoGasConfig struct {
	EthRPCURLs      []string `yaml:"eth_rpc_urls,omitempty"`
	ArbitrumRPCURLs []string `yaml:"arbitrum_rpc_urls,omitempty"`
	OptimismRPCURLs []string `yaml:"optimism_rpc_urls,omitempty"`
	BaseRPCURLs     []string `yaml:"base_rpc_urls,omitempty"`
	SolanaRPCURLs   []string `yaml:"solana_rpc_urls,omitempty"`
	MempoolBaseURL  string   `yaml:"mempool_base_url,omitempty"`
	TronGridBaseURL string   `yaml:"trongrid_base_url,omitempty"`
	CoinbaseBaseURL string   `yaml:"coinbase_base_url,omitempty"`
}

// withDefaults fills every empty field with the endpoint verified in
// gas_fee_design.md §9–§10 (Ethereum gets publicnode plus drpc as fallback).
func (c CryptoGasConfig) withDefaults() CryptoGasConfig {
	orList := func(v, def []string) []string {
		if len(v) == 0 {
			return def
		}
		return v
	}
	orStr := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	return CryptoGasConfig{
		EthRPCURLs:      orList(c.EthRPCURLs, []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org"}),
		ArbitrumRPCURLs: orList(c.ArbitrumRPCURLs, []string{"https://arb1.arbitrum.io/rpc"}),
		OptimismRPCURLs: orList(c.OptimismRPCURLs, []string{"https://mainnet.optimism.io"}),
		BaseRPCURLs:     orList(c.BaseRPCURLs, []string{"https://mainnet.base.org"}),
		SolanaRPCURLs:   orList(c.SolanaRPCURLs, []string{"https://solana-rpc.publicnode.com", "https://api.mainnet-beta.solana.com"}),
		MempoolBaseURL:  orStr(c.MempoolBaseURL, defaultMempoolBaseURL),
		TronGridBaseURL: orStr(c.TronGridBaseURL, "https://api.trongrid.io"),
		CoinbaseBaseURL: orStr(c.CoinbaseBaseURL, "https://api.coinbase.com"),
	}
}

func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, err
	}
	cfg.path = path

	return &cfg, nil
}

// configMu serializes config writes; conf.yaml is the source of truth for
// billing datasets and concurrent POSTs must not interleave.
var configMu sync.Mutex

// SaveConfig rewrites the config file the Config was loaded from. Comments
// and formatting in the original file are not preserved.
func SaveConfig(cfg *Config) error {
	configMu.Lock()
	defer configMu.Unlock()
	return writeConfigLocked(cfg)
}

// UpdateConfig applies mutate and saves, all under configMu, so a mutation
// never races another handler's yaml.Marshal. If the write fails the
// in-memory config is restored, keeping memory and file in agreement.
func UpdateConfig(cfg *Config, mutate func(*Config)) error {
	configMu.Lock()
	defer configMu.Unlock()
	old := *cfg
	mutate(cfg)
	if err := writeConfigLocked(cfg); err != nil {
		*cfg = old
		return err
	}
	return nil
}

// writeConfigLocked writes via a temp file and rename; the caller holds
// configMu. The file can hold an API key, so it is always 0600: Chmod is
// explicit because os.WriteFile keeps the mode of a leftover temp file.
func writeConfigLocked(cfg *Config) error {
	if cfg.path == "" {
		return fmt.Errorf("config has no source path")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	tmp := filepath.Join(filepath.Dir(cfg.path), ".conf.yaml.tmp")
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("failed to chmod config: %w", err)
	}
	return os.Rename(tmp, cfg.path)
}

// CryptoPricesConfig configures the on-chain USD price sync (Uniswap v3 swaps
// in BigQuery → SQLite, opendata_crypto_prices.go).
type CryptoPricesConfig struct {
	// InitialDays is the cold-start window: a pointer so "absent" (nil →
	// 365) differs from an explicit 0 (sync disabled); clamped to [0, 730].
	InitialDays *int `yaml:"initial_days,omitempty"`
}

func (c CryptoPricesConfig) initialDays() int {
	if c.InitialDays == nil {
		return 365
	}
	return min(max(*c.InitialDays, 0), 730)
}

type BitcoinConfig struct {
	Enabled   *bool                 `yaml:"enabled,omitempty"`   // nil → true
	Providers map[string]bool       `yaml:"providers,omitempty"` // missing key → btcDefaultProviders
	BigQuery  BitcoinBigQueryConfig `yaml:"bigquery,omitempty"`
}

type BitcoinBigQueryConfig struct {
	Jobs             bool `yaml:"jobs,omitempty"`               // default false: operator opt-in
	InitialDays      *int `yaml:"initial_days,omitempty"`       // nil → 365; clamp [7, 730]
	MonthlyBudgetGiB int  `yaml:"monthly_budget_gib,omitempty"` // ≤0 → 2048; covers jobs A, B and C
	CoinAge          struct {
		Enabled      *bool   `yaml:"enabled,omitempty"`        // nil → true
		CadenceDays  int     `yaml:"cadence_days,omitempty"`   // ≤0 → 7; clamp [7, 30]
		InitialDays  *int    `yaml:"initial_days,omitempty"`   // nil → 365; clamp [7, 730]
		WhaleMinBTC  float64 `yaml:"whale_min_btc,omitempty"`  // ≤0 → 10 (sent to SQL as whale_min_sats)
		WhalesPerDay int     `yaml:"whales_per_day,omitempty"` // ≤0 → 20; clamp [1, 100]
	} `yaml:"coin_age,omitempty"`
	UTXOBaseline struct {
		AllowUI         bool `yaml:"allow_ui,omitempty"`          // default false: CLI only
		MinIntervalDays int  `yaml:"min_interval_days,omitempty"` // ≤0 → 30; clamp [7, 365]
	} `yaml:"utxo_baseline,omitempty"`
}

// btcDefaultProviders lists the M1–M4 default providers; M5 keys land with Task 5.2.
var btcDefaultProviders = map[string]bool{
	"mempool":        true,
	"coinbase":       true,
	"kraken":         true,
	"coinmetrics":    true,
	"coingecko":      true,
	"blockchain_com": true,
	"deribit":        true,
	"okx":            true,
	"binance":        true,
	"hyperliquid":    true,
	"cftc":           true,
	"alternative_me": true,
}

type btcSettings struct {
	Enabled             bool
	Providers           map[string]bool
	Jobs                bool
	InitialDays         int
	BudgetBytes         int64
	CoinAgeEnabled      bool
	CadenceDays         int
	CoinAgeInitialDays  int
	WhaleMinSats        int64
	WhalesPerDay        int
	BaselineAllowUI     bool
	BaselineMinInterval time.Duration
}

func (c BitcoinConfig) settings() btcSettings {
	providers := make(map[string]bool, len(btcDefaultProviders))
	for k, v := range btcDefaultProviders {
		providers[k] = v
	}
	for k, v := range c.Providers {
		if _, ok := btcDefaultProviders[k]; ok {
			providers[k] = v
		}
	}
	initialDays := 365
	if c.BigQuery.InitialDays != nil {
		initialDays = min(max(*c.BigQuery.InitialDays, 7), 730)
	}
	budgetGiB := c.BigQuery.MonthlyBudgetGiB
	if budgetGiB <= 0 {
		budgetGiB = 2048
	}
	ca := c.BigQuery.CoinAge
	cadenceDays := ca.CadenceDays
	if cadenceDays <= 0 {
		cadenceDays = 7
	} else {
		cadenceDays = min(max(cadenceDays, 7), 30)
	}
	caInitialDays := 365
	if ca.InitialDays != nil {
		caInitialDays = min(max(*ca.InitialDays, 7), 730)
	}
	whaleBTC := ca.WhaleMinBTC
	if whaleBTC <= 0 {
		whaleBTC = 10
	}
	whaleMinSats := int64(math.Round(whaleBTC * 100_000_000))
	whalesPerDay := ca.WhalesPerDay
	if whalesPerDay <= 0 {
		whalesPerDay = 20
	} else {
		whalesPerDay = min(max(whalesPerDay, 1), 100)
	}
	ub := c.BigQuery.UTXOBaseline
	intervalDays := ub.MinIntervalDays
	if intervalDays <= 0 {
		intervalDays = 30
	} else {
		intervalDays = min(max(intervalDays, 7), 365)
	}
	return btcSettings{
		Enabled:             c.Enabled == nil || *c.Enabled,
		Providers:           providers,
		Jobs:                c.BigQuery.Jobs,
		InitialDays:         initialDays,
		BudgetBytes:         int64(budgetGiB) << 30,
		CoinAgeEnabled:      ca.Enabled == nil || *ca.Enabled,
		CadenceDays:         cadenceDays,
		CoinAgeInitialDays:  caInitialDays,
		WhaleMinSats:        whaleMinSats,
		WhalesPerDay:        whalesPerDay,
		BaselineAllowUI:     ub.AllowUI,
		BaselineMinInterval: time.Duration(intervalDays) * 24 * time.Hour,
	}
}
