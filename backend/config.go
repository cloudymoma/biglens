package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
	InitialDays   *int `yaml:"initial_days,omitempty"`   // default 30; 0 = disabled (spec P2.0 Q2)
	RetentionDays int  `yaml:"retention_days,omitempty"` // default 90 (spec P2.0 Q3)
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
		MempoolBaseURL:  orStr(c.MempoolBaseURL, "https://mempool.space"),
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
