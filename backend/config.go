package main

import (
	"os"

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

	return &cfg, nil
}
