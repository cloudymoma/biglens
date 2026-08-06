package main

import (
	"os"
	"path/filepath"
	"testing"
)

// SaveConfig must round-trip every existing field, not just the opendata
// section, because it rewrites the whole file.
func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	src := `server:
  port: 1983
  mode: "debug"
bigquery:
  project_id: "du-hast-mich"
  credentials_path: ""
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OpenData.GCPBilling.Datasets = []string{"my-project.billing_ds"}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Server.Port != 1983 || reloaded.Server.Mode != "debug" {
		t.Errorf("server section lost: %+v", reloaded.Server)
	}
	if reloaded.BigQuery.ProjectID != "du-hast-mich" {
		t.Errorf("bigquery section lost: %+v", reloaded.BigQuery)
	}
	got := reloaded.OpenData.GCPBilling.Datasets
	if len(got) != 1 || got[0] != "my-project.billing_ds" {
		t.Errorf("datasets = %v, want [my-project.billing_ds]", got)
	}
}
