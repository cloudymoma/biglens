package main

import (
	"net/http"
	"time"
)

// stablecoinService holds the Stablecoins dashboard state (stablecoin.md §8.2).
// It is nil when the dashboard is disabled; every stablecoin route then
// answers 404 (§9).
type stablecoinService struct {
	settings stableSettings
	now      func() time.Time
}

func newStablecoinService(cfg *Config) *stablecoinService {
	var sc StablecoinConfig
	if cfg != nil {
		sc = cfg.Stablecoin
	}
	s := sc.settings()
	if !s.Enabled {
		return nil
	}
	return &stablecoinService{settings: s, now: time.Now}
}

// providerEnabled reports whether a provider key may be called; false means
// the source is never called (§4, §8.6).
func (s *stablecoinService) providerEnabled(key string) bool {
	if s == nil {
		return false
	}
	return s.settings.Providers[key]
}

// stableAvailable writes 404 and returns false when the dashboard is disabled.
func (h *APIHandler) stableAvailable(w http.ResponseWriter) bool {
	if h == nil || h.stable == nil {
		writeError(w, "stablecoin dashboard disabled", http.StatusNotFound)
		return false
	}
	return true
}
