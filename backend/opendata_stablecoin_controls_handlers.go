package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// /controls (stablecoin.md §7.2 tab 6, §9): the control table of every
// monitored deployment, the change log, freeze activity, Tether's
// quarantined amounts and the Scam Radar counters. Served from SQLite; it
// never calls upstreams.

const (
	stableControlsTTL       = 60 * time.Second
	stableControlChangesMax = 100
	// stableControlRecent: a confirmed change stays a warning for 7 days (S8).
	stableControlRecent = 7 * 24 * time.Hour
	// stableControlStaleAfter: a field not read for 3 intervals is stale.
	stableControlStaleAfter = 3 * stableControlsTick
)

type StableControlsResponse struct {
	AsOf           string                `json:"as_of,omitempty"` // the newest control read
	ProviderStatus string                `json:"provider_status"` // rpc_controls, as in /meta
	Assets         []StableControlsAsset `json:"assets"`
	Changes        []StableControlChange `json:"changes"` // newest first, at most 100
	Freezes        []StableFreezeStats   `json:"freezes"`
	Quarantined    *StableQuarantined    `json:"quarantined"` // USDT only; null before the first Tether update
	Scam           StableScamCounters    `json:"scam"`
}

type StableControlsAsset struct {
	Asset       string                        `json:"asset"`
	Symbol      string                        `json:"symbol"`
	Deployments []StableControlDeploymentView `json:"deployments"`
}

type StableControlDeploymentView struct {
	Asset       string `json:"asset"`
	Chain       string `json:"chain"`
	ChainLabel  string `json:"chain_label"`
	Kind        string `json:"kind"` // tether | fiattoken | spl
	Contract    string `json:"contract"`
	ExplorerURL string `json:"explorer_url,omitempty"`
	// MonitoringSince is when every field had a confirmed value (the
	// baseline); no change can be reported before it.
	MonitoringSince string                   `json:"monitoring_since,omitempty"`
	LastChange      string                   `json:"last_change,omitempty"`
	ChangedLast7d   []string                 `json:"changed_last_7d"`
	Paused          *bool                    `json:"paused"`               // null before the baseline and on Solana
	Deprecated      *bool                    `json:"deprecated,omitempty"` // USDT only
	Fields          []StableControlFieldView `json:"fields"`
}

type StableControlFieldView struct {
	Field       string                    `json:"field"`
	Value       *string                   `json:"value"`           // confirmed; null before the baseline; "" = SPL authority not set
	Unset       bool                      `json:"unset,omitempty"` // a field that may be empty and is (0x0, no authority)
	ExplorerURL string                    `json:"explorer_url,omitempty"`
	Baseline    bool                      `json:"baseline"` // unchanged since monitoring began
	FirstSeen   string                    `json:"first_seen,omitempty"`
	ConfirmedAt string                    `json:"confirmed_at,omitempty"`
	ConfirmedBy string                    `json:"confirmed_by,omitempty"` // two_rpcs | two_samples
	LastSeen    string                    `json:"last_seen,omitempty"`
	LastReadAt  string                    `json:"last_read_at,omitempty"`
	ReadError   string                    `json:"read_error,omitempty"` // the last read's error, or rpc_disagreement
	Endpoint    string                    `json:"endpoint,omitempty"`   // host behind the last good read
	Stale       bool                      `json:"stale"`
	Pending     *StableControlPendingView `json:"pending,omitempty"` // a new value awaiting confirmation
}

type StableControlPendingView struct {
	Value     string `json:"value"`
	FirstSeen string `json:"first_seen"`
	Disputed  bool   `json:"disputed"` // two endpoints disagreed: only two agreeing endpoints can confirm it
}

type StableControlChange struct {
	DetectedAt  string `json:"detected_at"`
	Asset       string `json:"asset"`
	Chain       string `json:"chain"`
	Field       string `json:"field"`
	Old         string `json:"old"`
	New         string `json:"new"`
	FirstSeen   string `json:"first_seen"`
	Source      string `json:"source"` // rpc
	ConfirmedBy string `json:"confirmed_by"`
	TxHash      string `json:"tx_hash,omitempty"`
	Critical    bool   `json:"critical"` // paused or deprecated turned true (S8 critical)
}

func (s *stablecoinService) buildControls(ctx context.Context, asset string, assets []string) StableControlsResponse {
	now := s.now().UTC()
	out := StableControlsResponse{ProviderStatus: "pending", Assets: []StableControlsAsset{}, Changes: []StableControlChange{},
		Freezes: []StableFreezeStats{}, Scam: StableScamCounters{WindowDays: stableScamWindowDays, Note: stableScamNote}}
	switch {
	case !s.settings.Providers["rpc_controls"]:
		out.ProviderStatus = "disabled"
	case s.health != nil:
		out.ProviderStatus = s.health.provider("rpc_controls").Status
	}
	if s.store == nil {
		return out
	}
	states, err := s.store.stableControlStates(ctx)
	if err != nil {
		slog.Warn("stablecoin controls read", "error", err)
		states = map[string]stableControlState{}
	}
	filter := asset
	if filter == "all" {
		filter = ""
	}
	events, err := s.store.stableControlEvents(ctx, filter, 10_000)
	if err != nil {
		slog.Warn("stablecoin control events read", "error", err)
	}
	var asOf time.Time
	for _, id := range assets {
		def, _ := stableAssetByID(id)
		va := StableControlsAsset{Asset: id, Symbol: def.Symbol, Deployments: []StableControlDeploymentView{}}
		for _, d := range stableControlDeployments {
			if d.Asset != id {
				continue
			}
			tok, _ := d.token()
			info := chains[d.Chain]
			dv := StableControlDeploymentView{Asset: d.Asset, Chain: d.Chain, ChainLabel: info.Label, Kind: d.Kind, Contract: tok.Contract,
				ChangedLast7d: []string{}, Fields: []StableControlFieldView{}}
			if info.AddressURL != "" {
				dv.ExplorerURL = fmt.Sprintf(info.AddressURL, tok.Contract)
			}
			var baselineAt time.Time
			complete := true
			for _, f := range stableControlFields[d.Kind] {
				st := states[d.Asset+"|"+d.Chain+"|"+f.Name]
				fv := StableControlFieldView{Field: f.Name, Value: st.Value, Baseline: st.Baseline, FirstSeen: stableFmt(st.FirstSeen),
					ConfirmedAt: stableFmt(st.ConfirmedAt), ConfirmedBy: st.ConfirmedBy, LastSeen: stableFmt(st.LastSeen),
					LastReadAt: stableFmt(st.LastReadAt), ReadError: st.LastCode, Endpoint: st.LastEndpoint,
					Stale: st.LastReadAt.IsZero() || now.Sub(st.LastReadAt) > stableControlStaleAfter}
				if st.LastReadAt.After(asOf) {
					asOf = st.LastReadAt
				}
				if st.Value != nil {
					v := *st.Value
					fv.Unset = f.Nullable && (v == "" || stableControlIsZero(v))
					if f.Kind != "bool" && !fv.Unset && info.AddressURL != "" {
						fv.ExplorerURL = fmt.Sprintf(info.AddressURL, v)
					}
					if f.Kind == "bool" {
						b := v == "true"
						switch f.Name {
						case "paused":
							dv.Paused = &b
						case "deprecated":
							dv.Deprecated = &b
						}
					}
				}
				if st.PendingValue != nil {
					fv.Pending = &StableControlPendingView{Value: *st.PendingValue, FirstSeen: stableFmt(st.PendingFirstSeen), Disputed: st.PendingDisputed}
				}
				if st.BaselineAt.IsZero() {
					complete = false
				} else if st.BaselineAt.After(baselineAt) {
					baselineAt = st.BaselineAt
				}
				dv.Fields = append(dv.Fields, fv)
			}
			if complete {
				dv.MonitoringSince = stableFmt(baselineAt)
			}
			seen := map[string]bool{}
			for _, ev := range events { // newest first
				if ev.Asset != d.Asset || ev.Chain != d.Chain {
					continue
				}
				if dv.LastChange == "" {
					dv.LastChange = stableFmt(ev.DetectedAt)
				}
				if now.Sub(ev.DetectedAt) <= stableControlRecent && !seen[ev.Field] {
					seen[ev.Field] = true
					dv.ChangedLast7d = append(dv.ChangedLast7d, ev.Field)
				}
			}
			sort.Strings(dv.ChangedLast7d)
			va.Deployments = append(va.Deployments, dv)
		}
		out.Assets = append(out.Assets, va)
	}
	out.AsOf = stableFmt(asOf)
	for i, ev := range events {
		if i >= stableControlChangesMax {
			break
		}
		out.Changes = append(out.Changes, StableControlChange{DetectedAt: stableFmt(ev.DetectedAt), Asset: ev.Asset, Chain: ev.Chain, Field: ev.Field,
			Old: ev.Old, New: ev.New, FirstSeen: stableFmt(ev.FirstSeen), Source: ev.Source, ConfirmedBy: ev.ConfirmedBy, TxHash: ev.TxHash,
			Critical: (ev.Field == "paused" || ev.Field == "deprecated") && ev.New == "true"})
	}
	if fz, err := s.store.stableFreezeStats(ctx, asset); err != nil {
		slog.Warn("stablecoin freeze stats read", "error", err)
	} else {
		out.Freezes = fz
	}
	if asset == "all" || asset == "usdt" {
		if q, err := s.store.stableTetherQuarantined(ctx); err != nil {
			slog.Warn("stablecoin quarantined read", "error", err)
		} else {
			out.Quarantined = q
		}
	}
	if sc, err := s.store.stableScamCounters(ctx, asset); err != nil {
		slog.Warn("stablecoin scam counters read", "error", err)
	} else {
		out.Scam = sc
	}
	return out
}

func (h *APIHandler) StablecoinControls(w http.ResponseWriter, r *http.Request) {
	if !h.stableAvailable(w) || !stableGETOnly(w, r) {
		return
	}
	asset := r.URL.Query().Get("asset")
	var assets []string
	switch asset {
	case "", "all":
		asset = "all"
		for _, a := range stableCoreAssets {
			assets = append(assets, a.ID)
		}
	default:
		if _, ok := stableAssetByID(asset); !ok {
			writeError(w, "invalid asset", http.StatusBadRequest)
			return
		}
		assets = []string{asset}
	}
	v, _ := h.cachedFetch("stablecoin:controls:"+asset, stableControlsTTL, func() (any, error) {
		// Shared by singleflight and cached: a cancelled first request must
		// not cancel everyone's read.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		return h.stable.buildControls(ctx, asset, assets), nil
	})
	writeJSON(w, v)
}
