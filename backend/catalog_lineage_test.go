package main

import "testing"

func TestEntryLocation(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"projects/p/locations/us-central1/entryGroups/@bigquery/entries/x", "us-central1"},
		{"projects/p/locations/global/entryGroups/g/entries/x", "global"},
		{"projects/p/locations/us", "us"},
		{"projects/p/entryGroups/g/entries/x", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := entryLocation(tt.name); got != tt.want {
			t.Errorf("entryLocation(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestLineageLocationFor(t *testing.T) {
	tests := []struct {
		name          string
		fixedLocation string
		assetLocation string
		want          string
	}{
		{"config override wins over asset region", "europe-west1", "us-central1", "europe-west1"},
		{"asset region used when no override", "", "us-central1", "us-central1"},
		{"global asset falls back to us", "", "global", "us"},
		{"unknown asset region falls back to us", "", "", "us"},
	}
	for _, tt := range tests {
		l := &LineageClient{fixedLocation: tt.fixedLocation}
		if got := l.locationFor(tt.assetLocation); got != tt.want {
			t.Errorf("%s: locationFor(%q) = %q, want %q", tt.name, tt.assetLocation, got, tt.want)
		}
	}
}
