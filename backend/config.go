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

	// path is where this config was loaded from, so SaveConfig can write back.
	path string
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

	if cfg.path == "" {
		return fmt.Errorf("config has no source path")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	tmp := filepath.Join(filepath.Dir(cfg.path), ".conf.yaml.tmp")
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return os.Rename(tmp, cfg.path)
}
