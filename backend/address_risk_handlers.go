package main

// HTTP endpoints for the Crypto Pulse "Address Risk" tab:
//
//	GET /api/opendata/crypto/address-risk/lookup?address=0x…
//	GET /api/opendata/crypto/address-risk/sources
//	POST|DELETE /api/opendata/crypto/address-risk/keys (address_risk_keys.go)
//
// The access log records r.URL.Path only, so looked-up addresses (query
// string) never reach logs; error messages below never echo input either.

import (
	"context"
	"net/http"
	"net/url"
)

type riskSourcesResponse struct {
	Lists      []riskSource `json:"lists"`
	RPCHosts   []string     `json:"rpc_hosts"`
	GoPlusHost string       `json:"goplus_host"`
	Etherscan  riskKeyInfo  `json:"etherscan"`
}

func (h *APIHandler) AddressRiskLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	addr, warn, err := parseEthAddress(r.URL.Query().Get("address"))
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest) // fixed message, no user input
		return
	}
	// One key snapshot per request feeds both the cache key and the lookup.
	etherscanKey := h.risk.etherscanKeySnapshot()
	hasKey := "0"
	if etherscanKey != "" {
		hasKey = "1"
	}
	key := "address-risk:lookup:" + addr + ":" + hasKey
	var res *riskLookupResult
	if cached, ok := h.cache.Get(key); ok {
		res = cached.(*riskLookupResult)
	} else {
		// WithoutCancel: one disconnecting caller must not cancel every waiter
		// sharing this flight; each source still has its own timeout.
		v, _, _ := h.risk.flight.Do(key, func() (any, error) {
			out := h.risk.lookupKey(context.WithoutCancel(r.Context()), addr, etherscanKey)
			if out.cacheable() {
				h.cache.Set(key, out)
			}
			return out, nil
		})
		res = v.(*riskLookupResult)
	}
	resp := *res // checksum_warning depends on this request's input, not the cached one
	resp.ChecksumWarning = warn
	writeJSON(w, &resp)
}

func (h *APIHandler) AddressRiskSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	goplusHost := ""
	if u, err := url.Parse(goplusBaseURL); err == nil {
		goplusHost = u.Hostname()
	}
	writeJSON(w, riskSourcesResponse{
		Lists:      h.risk.listSources(r.Context()),
		RPCHosts:   hostsOf(h.risk.rpcURLs...),
		GoPlusHost: goplusHost,
		Etherscan:  h.risk.etherscanInfo(),
	})
}
