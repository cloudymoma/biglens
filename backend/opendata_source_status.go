package main

// SourceStatus reports the freshness, status and provenance of one data source.
// The Bitcoin and stablecoin dashboards share it.
type SourceStatus struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"` // ok | stale | error | disabled | geo_blocked | rate_limited | degraded (stablecoin Tether sum check)
	Via     string `json:"via,omitempty"`
	AsOf    string `json:"as_of,omitempty"`
	Error   string `json:"error,omitempty"`
	License string `json:"license,omitempty"`
}
