package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest records the scope of the last Dataplex import so it can be
// inspected ("last imported …") and reproduced (Refresh re-runs the recorded
// query). It is bundle metadata, not a concept: it lives in manifest.yaml
// (not a .md file), so ListConcepts/BuildGraph/Search never see it.
const manifestFile = "manifest.yaml"

type Manifest struct {
	Project         string         `yaml:"project" json:"project"`
	Location        string         `yaml:"location" json:"location"`
	Query           string         `yaml:"query" json:"query"`
	LineageLocation string         `yaml:"lineage_location,omitempty" json:"lineage_location,omitempty"`
	ImportedAt      string         `yaml:"imported_at" json:"imported_at"` // RFC3339 UTC
	Truncated       bool           `yaml:"truncated,omitempty" json:"truncated,omitempty"`
	EntryTypeCounts map[string]int `yaml:"entry_type_counts,omitempty" json:"entry_type_counts,omitempty"`
}

func (b *OKFBundle) manifestPath() string {
	return filepath.Join(b.root, manifestFile)
}

// WriteManifest writes manifest.yaml at the bundle root atomically
// (temp file + rename), mirroring WriteConcept. Concurrent imports are
// last-write-wins: the manifest records whichever import finished last.
func (b *OKFBundle) WriteManifest(m Manifest) error {
	if err := os.MkdirAll(b.root, 0o755); err != nil {
		return fmt.Errorf("create bundle root: %w", err)
	}
	y, err := yaml.Marshal(&m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	path := b.manifestPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, y, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit manifest: %w", err)
	}
	return nil
}

// ReadManifest loads manifest.yaml from the bundle root. When no import has
// ever been recorded the error satisfies errors.Is(err, os.ErrNotExist).
func (b *OKFBundle) ReadManifest() (*Manifest, error) {
	raw, err := os.ReadFile(b.manifestPath())
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// importQueryFor resolves the effective import query for a request:
// refresh=1 re-runs the query recorded in the manifest (ignoring q),
// otherwise the q parameter is used as-is.
func importQueryFor(r *http.Request, bundle *OKFBundle) (string, error) {
	if r.URL.Query().Get("refresh") != "1" {
		return r.URL.Query().Get("q"), nil
	}
	m, err := bundle.ReadManifest()
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("refresh requested but no previous import is recorded (manifest.yaml missing): %w", err)
		}
		return "", fmt.Errorf("read manifest: %w", err)
	}
	return m.Query, nil
}
