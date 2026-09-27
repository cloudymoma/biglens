package main

// POST   /api/opendata/crypto/address-risk/keys           {"provider":"etherscan","key":"…"}
// DELETE /api/opendata/crypto/address-risk/keys?provider=etherscan
//
// Saves the optional Etherscan key after validating it upstream (spec §10).
// The route is wrapped in http.CrossOriginProtection: a cross-site
// text/plain form POST is a "simple request" and would otherwise let another
// site swap in its own key. The key is never echoed, logged, or returned —
// only a 4-character hint.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	etherscanKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	// riskKeyValidateEvery: >5 invalid keys in 30 s make Etherscan throttle
	// the VM's egress IP for 30 s, which would also break lookups.
	riskKeyValidateEvery = 10 * time.Second
)

// riskKeyInfo is all /sources reveals about the key.
type riskKeyInfo struct {
	Configured       bool   `json:"configured"`
	Hint             string `json:"hint"`
	CreditsAvailable *int64 `json:"credits_available,omitempty"`
}

// keyHint returns the last 4 characters, only for keys of at least 8.
func keyHint(k string) string {
	if len(k) < 8 {
		return ""
	}
	return k[len(k)-4:]
}

func (s *addressRiskService) etherscanInfo() riskKeyInfo {
	k := s.etherscanKeySnapshot()
	info := riskKeyInfo{Configured: k != "", Hint: keyHint(k)}
	if k != "" {
		info.CreditsAvailable = s.credits.Load()
	}
	return info
}

// allowValidation enforces one upstream key check per riskKeyValidateEvery,
// process-wide.
func (s *addressRiskService) allowValidation(now time.Time) bool {
	s.validateMu.Lock()
	defer s.validateMu.Unlock()
	if !s.lastValidate.IsZero() && now.Sub(s.lastValidate) < riskKeyValidateEvery {
		return false
	}
	s.lastValidate = now
	return true
}

// validateEtherscanKey calls getapilimit through the shared limiter. It
// returns creditsAvailable, or an error code plus Etherscan's message.
func validateEtherscanKey(ctx context.Context, key string) (int64, string, string) {
	ctx, cancel := context.WithTimeout(ctx, riskSourceTimeout)
	defer cancel()
	raw, msg, code := etherscanCall(ctx, key, url.Values{"module": {"getapilimit"}, "action": {"getapilimit"}})
	if code != "" {
		return 0, msg, code
	}
	var limit struct {
		CreditsAvailable *int64 `json:"creditsAvailable"`
	}
	if err := json.Unmarshal(raw, &limit); err != nil || limit.CreditsAvailable == nil {
		return 0, "", "bad_response"
	}
	return *limit.CreditsAvailable, "", ""
}

func (h *APIHandler) AddressRiskKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.saveEtherscanKey(w, r)
	case http.MethodDelete:
		h.deleteEtherscanKey(w, r)
	default:
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) saveEtherscanKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.Provider != "etherscan" {
		writeError(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	if !etherscanKeyRe.MatchString(body.Key) {
		writeError(w, "An Etherscan API key is 16-64 letters and digits.", http.StatusBadRequest)
		return
	}
	if h.risk.cfg == nil {
		writeError(w, "configuration is not writable", http.StatusInternalServerError)
		return
	}
	if !h.risk.allowValidation(time.Now()) {
		writeError(w, "Key checks are limited to one every 10 seconds; try again shortly.", http.StatusTooManyRequests)
		return
	}
	credits, msg, code := validateEtherscanKey(r.Context(), body.Key)
	switch code {
	case "":
	case "key_invalid", "key_throttled":
		msg = strings.ReplaceAll(msg, body.Key, "…") // never echo the key back
		extra := "New keys can take a few minutes to activate."
		if code == "key_throttled" {
			extra = "Wait 30 seconds before trying again."
		}
		writeError(w, fmt.Sprintf("Etherscan rejected the key: %s. %s", msg, extra), http.StatusBadRequest)
		return
	default:
		writeError(w, "Etherscan did not answer the key check ("+code+"); try again later.", http.StatusBadGateway)
		return
	}
	if err := UpdateConfig(h.risk.cfg, func(c *Config) { c.AddressRisk.EtherscanAPIKey = body.Key }); err != nil {
		slog.Error("address_risk key save failed", "error", err)
		writeError(w, "failed to save the key to conf.yaml", http.StatusInternalServerError)
		return
	}
	h.risk.setEtherscanKey(body.Key)
	h.risk.credits.Store(&credits)
	writeJSON(w, h.risk.etherscanInfo())
}

func (h *APIHandler) deleteEtherscanKey(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("provider") != "etherscan" {
		writeError(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	if h.risk.cfg == nil {
		writeError(w, "configuration is not writable", http.StatusInternalServerError)
		return
	}
	if err := UpdateConfig(h.risk.cfg, func(c *Config) { c.AddressRisk.EtherscanAPIKey = "" }); err != nil {
		slog.Error("address_risk key delete failed", "error", err)
		writeError(w, "failed to update conf.yaml", http.StatusInternalServerError)
		return
	}
	h.risk.setEtherscanKey("")
	h.risk.credits.Store(nil)
	writeJSON(w, h.risk.etherscanInfo())
}

// addressRiskKeysHandler adds cross-origin protection (Sec-Fetch-Site /
// Origin checks, standard library since Go 1.25).
func addressRiskKeysHandler(h *APIHandler) http.Handler {
	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(h.AddressRiskKeys))
}
