package main

// HTTP endpoints for the Crypto Pulse "Address Risk" tab:
//
//	GET /api/opendata/crypto/address-risk/lookup?address=0x…[&chain=eth][&bq=1]
//	GET /api/opendata/crypto/address-risk/sources
//	POST|DELETE /api/opendata/crypto/address-risk/keys (address_risk_keys.go)
//
// bq=1 opts one Ethereum lookup into the BigQuery address profile
// (address_risk_bqprofile.go); it is ignored on other chains and when the
// server has the profile disabled.
//
// The access log records r.URL.Path only, so looked-up addresses (query
// string) never reach logs; error messages below never echo input either.

import (
	"context"
	"net/http"
	"net/url"
)

type riskSourcesResponse struct {
	Lists           []riskSource      `json:"lists"`
	RPCHosts        []string          `json:"rpc_hosts"`
	GoPlusHost      string            `json:"goplus_host"`
	Etherscan       riskKeyInfo       `json:"etherscan"`
	BigQueryProfile riskBQProfileInfo `json:"bigquery_profile"`
}

func (h *APIHandler) AddressRiskLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chain := r.URL.Query().Get("chain")
	if chain == "" {
		chain = "eth"
	}
	if _, ok := chains[chain]; !ok {
		writeError(w, "unknown chain", http.StatusBadRequest)
		return
	}
	addr, warn, err := parseChainAddress(chain, r.URL.Query().Get("address"))
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
	key := "address-risk:lookup:" + chain + ":" + addr + ":" + hasKey
	// The profile is honoured only on Ethereum and when the server allows it;
	// the effective flag is part of the cache key.
	opts := riskLookupOpts{BQProfile: r.URL.Query().Get("bq") == "1" && chain == "eth" && h.risk.bqProfile.available()}
	if opts.BQProfile {
		key += ":bq"
	}
	var res *riskLookupResult
	if cached, ok := h.cache.Get(key); ok {
		res = cached.(*riskLookupResult)
	} else {
		// WithoutCancel: one disconnecting caller must not cancel every waiter
		// sharing this flight; each source still has its own timeout.
		v, _, _ := h.risk.flight.Do(key, func() (any, error) {
			out := h.risk.lookupOpts(context.WithoutCancel(r.Context()), chain, addr, etherscanKey, opts)
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
		Lists:           h.risk.listSources(r.Context()),
		RPCHosts:        hostsOf(h.risk.rpcURLs...),
		GoPlusHost:      goplusHost,
		Etherscan:       h.risk.etherscanInfo(),
		BigQueryProfile: h.risk.bqProfileInfo(),
	})
}
