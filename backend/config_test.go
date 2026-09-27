package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	cfg.GCPBilling.Datasets = []string{"my-project.billing_ds"}
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
	got := reloaded.GCPBilling.Datasets
	if len(got) != 1 || got[0] != "my-project.billing_ds" {
		t.Errorf("datasets = %v, want [my-project.billing_ds]", got)
	}
}

func TestAddressRiskConfigDefaults(t *testing.T) {
	var c AddressRiskConfig
	if got := c.dbPath(); got != "data/security.db" {
		t.Errorf("dbPath() = %q, want data/security.db", got)
	}
	want := []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org"}
	if got := c.rpcURLs(); !reflect.DeepEqual(got, want) {
		t.Errorf("rpcURLs() = %v, want %v", got, want)
	}
	c = AddressRiskConfig{DBPath: "/tmp/x.db", EthRPCURLs: []string{"https://rpc.example"}}
	if c.dbPath() != "/tmp/x.db" || !reflect.DeepEqual(c.rpcURLs(), []string{"https://rpc.example"}) {
		t.Errorf("explicit values not honoured: %+v", c)
	}
}

// Existing VMs have no address_risk section; loading and re-saving such a
// config must not write an empty section with zero values that change behaviour.
func TestAddressRiskConfigAbsentSectionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 1983\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AddressRisk.dbPath() != "data/security.db" {
		t.Fatalf("default db path lost: %q", cfg.AddressRisk.dbPath())
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "address_risk") {
		t.Errorf("empty address_risk section was written:\n%s", out)
	}
}

// VMs provisioned before M2 have no initial_sync_days key: that must mean the
// 30-day default, not 0 (which would silently switch the cold start off).
func TestAddressRiskInitialSyncDays(t *testing.T) {
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name string
		in   *int
		want int
	}{
		{"absent means 30", nil, 30},
		{"explicit 0 disables", ptr(0), 0},
		{"explicit 7", ptr(7), 7},
		{"above 31 clamps", ptr(45), 31},
		{"negative clamps to 0", ptr(-3), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (AddressRiskConfig{InitialSyncDays: tt.in}).initialSyncDays(); got != tt.want {
				t.Errorf("initialSyncDays() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAddressRiskInitialSyncDaysYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	if err := os.WriteFile(path, []byte("address_risk:\n  initial_sync_days: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AddressRisk.initialSyncDays(); got != 0 {
		t.Fatalf("explicit 0 in yaml read as %d", got)
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "initial_sync_days: 0") {
		t.Errorf("explicit 0 lost on save:\n%s", out)
	}
}

// conf.yaml may hold the Etherscan key: it must end up 0600 even when a
// crashed earlier save left a 0644 temp file behind (os.WriteFile keeps it).
func TestUpdateConfigWritesMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	os.WriteFile(path, []byte("server:\n  port: 1983\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".conf.yaml.tmp"), []byte("stale"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateConfig(cfg, func(c *Config) { c.AddressRisk.EtherscanAPIKey = "ABCDEFGHIJKLMNOP1234" }); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	again, _ := LoadConfig(path)
	if again.AddressRisk.EtherscanAPIKey != "ABCDEFGHIJKLMNOP1234" || again.Server.Port != 1983 {
		t.Errorf("round trip lost data: %+v", again.AddressRisk)
	}
}

func TestUpdateConfigRestoresOnWriteFailure(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "missing-dir", "conf.yaml")}
	cfg.AddressRisk.EtherscanAPIKey = "OLDKEYOLDKEYOLDKEY"
	err := UpdateConfig(cfg, func(c *Config) { c.AddressRisk.EtherscanAPIKey = "NEWKEYNEWKEYNEWKEY" })
	if err == nil {
		t.Fatal("expected a write error")
	}
	if cfg.AddressRisk.EtherscanAPIKey != "OLDKEYOLDKEYOLDKEY" {
		t.Errorf("memory kept the unsaved key: %q", cfg.AddressRisk.EtherscanAPIKey)
	}
}

// Billing/resources handlers call SaveConfig while the key handler calls
// UpdateConfig; under -race neither may observe a half-mutated Config.
func TestUpdateConfigConcurrentWithSaveConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yaml")
	os.WriteFile(path, []byte("server:\n  port: 1983\n"), 0o600)
	cfg, _ := LoadConfig(path)
	done := make(chan error, 40)
	for i := 0; i < 20; i++ {
		go func(i int) {
			done <- UpdateConfig(cfg, func(c *Config) { c.AddressRisk.EtherscanAPIKey = fmt.Sprintf("KEY%017d", i) })
		}(i)
		go func() { done <- SaveConfig(cfg) }()
	}
	for i := 0; i < 40; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
