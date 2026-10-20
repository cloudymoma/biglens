package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
)

// Events feed (stablecoin.md §7.2, Overview): confirmed control changes
// (RPC), freeze events (Address Risk), peg state changes (S1 transitions),
// large supply moves (issuer snapshot deltas of at least $250M or €25M within
// an hour) and curated events (attestations, facts events and past regime key
// dates). It reads SQLite and the curated facts only.

const (
	stableEventsLimit = 50
	// stableLargeMoveUSD / EUR: a supply move worth an event (§7.2), in the
	// asset's own units (USD coins count at par).
	stableLargeMoveUSD = 250e6
	stableLargeMoveEUR = 25e6
	// stableLargeMoveWindow: consecutive issuer snapshots at most this far
	// apart ("within an hour"; snapshots are stored hourly).
	stableLargeMoveWindow = 65 * time.Minute
	stableEventsLookback  = 30 * 24 * time.Hour
)

// StableEvent is one entry of the events feed, newest first.
type StableEvent struct {
	TS       string         `json:"ts"`       // RFC3339; curated events: the day at 00:00 UTC
	Asset    string         `json:"asset"`    // usdt | usdc | eurc | all
	Kind     string         `json:"kind"`     // control_change | freeze | peg_state | supply_move | attestation | regulatory | incident | depeg | control | key_date
	Severity string         `json:"severity"` // info | watch | warning | critical
	Title    string         `json:"title"`
	Source   string         `json:"source"` // rpc | address_risk | signals | issuer | facts
	URL      string         `json:"url,omitempty"`
	Detail   map[string]any `json:"detail,omitempty"`
}

func stableDayTS(day string) string {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return ""
	}
	return stableFmt(t)
}

func stableChainName(chain string) string {
	if c, ok := chains[chain]; ok {
		return c.Label
	}
	if l, ok := stableChainLabels[chain]; ok {
		return l
	}
	return chain
}

// buildEvents returns the newest events of the given assets ("all" events,
// such as regime key dates, are always included).
func (s *stablecoinService) buildEvents(ctx context.Context, assets []string, now time.Time, limit int) []StableEvent {
	want := map[string]bool{}
	for _, a := range assets {
		want[a] = true
	}
	out := []StableEvent{}
	if s.store != nil {
		out = append(out, s.controlEvents(ctx, want, now)...)
		out = append(out, s.freezeEvents(ctx, assets)...)
		out = append(out, s.pegStateEvents(ctx, want)...)
		for _, a := range assets {
			out = append(out, s.supplyMoveEvents(ctx, a, now)...)
		}
	}
	out = append(out, s.curatedEvents(want, now)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *stablecoinService) controlEvents(ctx context.Context, want map[string]bool, now time.Time) []StableEvent {
	evs, err := s.store.stableControlEvents(ctx, "", 200)
	if err != nil {
		slog.Warn("stablecoin events: control changes", "error", err)
		return nil
	}
	var out []StableEvent
	for _, ev := range evs {
		if !want[ev.Asset] {
			continue
		}
		sev := stableStateInfo
		switch {
		case (ev.Field == "paused" || ev.Field == "deprecated") && ev.New == "true":
			sev = stableStateCritical
		case stableS8WarningFields[ev.Field] && now.Sub(ev.DetectedAt) <= stableS8Window:
			sev = stableStateWarning
		}
		out = append(out, StableEvent{TS: stableFmt(ev.DetectedAt), Asset: ev.Asset, Kind: "control_change", Severity: sev,
			Title:  fmt.Sprintf("%s on %s: %s changed", strings.ToUpper(ev.Asset), stableChainName(ev.Chain), ev.Field),
			Source: ev.Source, Detail: map[string]any{"chain": ev.Chain, "field": ev.Field, "old": ev.Old, "new": ev.New,
				"first_seen": stableFmt(ev.FirstSeen), "confirmed_by": ev.ConfirmedBy}})
	}
	return out
}

func (s *stablecoinService) freezeEvents(ctx context.Context, assets []string) []StableEvent {
	var out []StableEvent
	for _, a := range assets {
		stats, err := s.store.stableFreezeStats(ctx, a)
		if err != nil {
			slog.Warn("stablecoin events: freezes", "asset", a, "error", err)
			continue
		}
		for _, st := range stats {
			for _, fe := range st.Recent {
				what := map[string]string{"freeze": "address frozen", "unfreeze": "address unfrozen", "destroy": "frozen funds destroyed"}[fe.Action]
				if what == "" {
					what = fe.Action
				}
				title := fmt.Sprintf("%s on %s: %s", st.Token, stableChainName(st.Chain), what)
				d := map[string]any{"chain": st.Chain, "action": fe.Action, "address": fe.Address, "tx_hash": fe.TxHash}
				if fe.Amount != "" {
					d["amount"] = fe.Amount
				}
				out = append(out, StableEvent{TS: fe.BlockTime, Asset: st.Asset, Kind: "freeze", Severity: stableStateInfo, Title: title,
					Source: "address_risk", URL: fe.TxURL, Detail: d})
			}
		}
	}
	return out
}

// pegStateEvents are S1 state changes that enter or leave watch or above.
func (s *stablecoinService) pegStateEvents(ctx context.Context, want map[string]bool) []StableEvent {
	evs, err := s.store.stableSignalEvents(ctx, "", 500)
	if err != nil {
		slog.Warn("stablecoin events: signal events", "error", err)
		return nil
	}
	var out []StableEvent
	for _, ev := range evs {
		if ev.Signal != "S1" || !want[ev.Asset] || (stableLevelOf(ev.From) < stableLevelWatch && stableLevelOf(ev.To) < stableLevelWatch) {
			continue
		}
		sev := stableStateInfo
		if stableLevelOf(ev.To) >= stableLevelWatch {
			sev = ev.To
		}
		d := map[string]any{"from": ev.From, "to": ev.To}
		for _, k := range []string{"consensus", "dev_bps", "venues_used"} {
			if v, ok := ev.Inputs[k]; ok {
				d[k] = v
			}
		}
		out = append(out, StableEvent{TS: stableFmt(ev.TS), Asset: ev.Asset, Kind: "peg_state", Severity: sev,
			Title:  fmt.Sprintf("%s peg signal: %s → %s", strings.ToUpper(ev.Asset), ev.From, ev.To),
			Source: "signals", Detail: d})
	}
	return out
}

// supplyMoveEvents compares consecutive issuer snapshots of the total
// (chain "all") at most an hour apart. Tether updates about once a day, so
// USDT rarely has a pair this close.
func (s *stablecoinService) supplyMoveEvents(ctx context.Context, asset string, now time.Time) []StableEvent {
	def, ok := stableAssetByID(asset)
	if !ok {
		return nil
	}
	rows, err := s.store.stableSupplySnapshots(ctx, asset, "issuer", now.Add(-stableEventsLookback), now)
	if err != nil {
		slog.Warn("stablecoin events: supply snapshots", "asset", asset, "error", err)
		return nil
	}
	var totals []stableSupplySnapshot
	for _, r := range rows {
		if r.Chain == "all" && r.CirculatingNative != nil {
			totals = append(totals, r)
		}
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i].TS.Before(totals[j].TS) })
	threshold, unit := stableLargeMoveUSD, "$"
	if def.Numeraire == "EUR" {
		threshold, unit = stableLargeMoveEUR, "€"
	}
	var out []StableEvent
	for i := 1; i < len(totals); i++ {
		a, b := totals[i-1], totals[i]
		gap := b.TS.Sub(a.TS)
		delta := *b.CirculatingNative - *a.CirculatingNative
		if gap <= 0 || gap > stableLargeMoveWindow || math.Abs(delta) < threshold {
			continue
		}
		sign := "+"
		if delta < 0 {
			sign = "−"
		}
		out = append(out, StableEvent{TS: stableFmt(b.TS), Asset: asset, Kind: "supply_move", Severity: stableStateInfo,
			Title: fmt.Sprintf("%s issuer circulating %s%s%.0fM in %d min", def.Symbol, sign, unit, math.Abs(delta)/1e6,
				int(math.Round(gap.Minutes()))),
			Source: "issuer", Detail: map[string]any{"from": stableFmt(a.TS), "to": stableFmt(b.TS), "from_native": *a.CirculatingNative,
				"to_native": *b.CirculatingNative, "delta_native": delta}})
	}
	return out
}

// curatedEvents come from the facts: published attestations, facts events,
// and regime key dates up to today.
func (s *stablecoinService) curatedEvents(want map[string]bool, now time.Time) []StableEvent {
	var out []StableEvent
	today := now.UTC().Format(time.DateOnly)
	for _, def := range stableCoreAssets {
		if !want[def.ID] {
			continue
		}
		f := s.facts.Assets[def.ID]
		for _, at := range f.Attestations {
			if at.Published > today {
				continue
			}
			out = append(out, StableEvent{TS: stableDayTS(at.Published), Asset: def.ID, Kind: "attestation", Severity: stableStateInfo,
				Title:  fmt.Sprintf("%s %s for the period ending %s published (%s)", def.Symbol, at.Type, at.PeriodEnd, at.Firm),
				Source: "facts", URL: at.URL, Detail: map[string]any{"period_end": at.PeriodEnd, "reserves_native": at.ReservesNative,
					"liabilities_native": at.LiabilitiesNative}})
		}
		for _, e := range f.Events {
			if e.Date > today {
				continue
			}
			out = append(out, StableEvent{TS: stableDayTS(e.Date), Asset: def.ID, Kind: e.Kind, Severity: stableStateInfo, Title: e.Title,
				Source: "facts", URL: e.SourceURL})
		}
	}
	for _, g := range s.facts.Regimes {
		for _, k := range g.KeyDates {
			if k.Date > today {
				continue
			}
			url := k.SourceURL
			if url == "" {
				url = g.SourceURL
			}
			out = append(out, StableEvent{TS: stableDayTS(k.Date), Asset: "all", Kind: "key_date", Severity: stableStateInfo,
				Title: g.Regime + ": " + k.Label, Source: "facts", URL: url})
		}
	}
	return out
}
