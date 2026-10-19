package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStableAvailableDisabled: with the dashboard disabled the guard answers
// 404 (stablecoin.md §9); with it enabled the guard lets the handler run.
func TestStableAvailableDisabled(t *testing.T) {
	off := false
	cfg := &Config{}
	cfg.Stablecoin.Enabled = &off
	h := &APIHandler{stable: newStablecoinService(cfg)}
	rec := httptest.NewRecorder()
	if h.stableAvailable(rec) {
		t.Fatal("stableAvailable = true with the dashboard disabled")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	h = &APIHandler{stable: newStablecoinService(&Config{})}
	rec = httptest.NewRecorder()
	if !h.stableAvailable(rec) {
		t.Fatalf("stableAvailable = false with the dashboard enabled (status %d)", rec.Code)
	}
}

func TestStableProviderEnabled(t *testing.T) {
	cfg := &Config{}
	cfg.Stablecoin.Providers = map[string]bool{"binance": false}
	s := newStablecoinService(cfg)
	if s.providerEnabled("binance") {
		t.Error("binance disabled in config but reported enabled")
	}
	if !s.providerEnabled("circle") {
		t.Error("circle should default to enabled")
	}
	if s.providerEnabled("not_a_provider") {
		t.Error("unknown provider must be disabled")
	}
	var nilSvc *stablecoinService
	if nilSvc.providerEnabled("circle") {
		t.Error("a nil (disabled) service has no enabled providers")
	}
}
