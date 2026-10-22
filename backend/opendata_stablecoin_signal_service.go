package main

import (
	"context"
	"log/slog"
	"time"
)

// Service side of the signal engine (stablecoin.md §6, §8.4): wiring to the
// samplers, a watchdog tick, and the inputs read from memory and SQLite.

// stableSignalWatchdogEvery: the engine also runs on its own tick, so a
// stalled peg sampler still shows (S1 unknown, S13 watch).
const stableSignalWatchdogEvery = stablePegTick

// stableS4LookbackDays: daily supply rows read for S4. A 7d change on
// 3-point medians needs 10 days; the margin covers gaps.
const stableS4LookbackDays = 40

// wireSignals creates the engine and runs it after every peg, supply and
// controls sample.
func (s *stablecoinService) wireSignals() {
	if s == nil {
		return
	}
	s.signals = newStableSignalEngine(s.store, s.now)
	eval := func(ctx context.Context) { s.evaluateSignals(ctx) }
	if s.peg != nil {
		s.peg.onResult = eval
	}
	if s.supply != nil {
		s.supply.onState = eval
	}
	if s.controls != nil {
		s.controls.onSaved = eval
	}
}

// evaluateSignals runs the engine on the latest inputs.
func (s *stablecoinService) evaluateSignals(ctx context.Context) {
	if s == nil || s.signals == nil {
		return
	}
	if err := s.signals.apply(ctx, s.signalInputs(ctx)); err != nil {
		slog.Warn("stablecoin signals", "error", err)
	}
}

// runStableSignalWatchdog runs the engine every 5 minutes until ctx ends; a
// panic in one run is logged and the loop continues.
func runStableSignalWatchdog(ctx context.Context, s *stablecoinService) {
	t := time.NewTicker(stableSignalWatchdogEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("stablecoin signal watchdog panic", "panic", r)
				}
			}()
			s.evaluateSignals(ctx)
		}()
	}
}

func (s *stablecoinService) signalInputs(ctx context.Context) stableSignalInputs {
	now := s.now().UTC()
	in := stableSignalInputs{Now: now, Peg: s.pegLatest(), Supply: s.snapshot(), Facts: s.facts,
		ControlsEnabled: s.settings.Providers["rpc_controls"], Flows: map[string]stableFlowInput{}, Health: map[string]stableHealthInput{}}
	if s.health != nil {
		in.PegStalled = s.health.sampler("peg").Stalled
	}
	if s.store != nil {
		if st, err := s.store.stableControlStates(ctx); err != nil {
			slog.Warn("stablecoin signals: control state", "error", err)
		} else {
			in.Controls = st
		}
		if evs, err := s.store.stableControlEvents(ctx, "", 500); err != nil {
			slog.Warn("stablecoin signals: control events", "error", err)
		} else {
			for _, ev := range evs {
				if now.Sub(ev.DetectedAt) <= stableS8Window {
					in.ControlEvents = append(in.ControlEvents, ev)
				}
			}
		}
		if tr, err := s.store.stableTetherReserves(ctx, 1); err != nil {
			slog.Warn("stablecoin signals: tether snapshot", "error", err)
		} else {
			in.Tether = tr.Latest
		}
		for _, def := range stableCoreAssets {
			in.Flows[def.ID] = s.flowInput(ctx, def.ID, now)
		}
	}
	for _, def := range stableCoreAssets {
		in.Health[def.ID] = s.healthInput(def, in.Supply)
	}
	return in
}

// flowInput is S4's daily series: the issuer's once it covers the last 10
// days, else DefiLlama's (§7.4: flows start from the aggregator).
func (s *stablecoinService) flowInput(ctx context.Context, asset string, now time.Time) stableFlowInput {
	from := now.AddDate(0, 0, -stableS4LookbackDays).Format(time.DateOnly)
	points := func(source string) []stableFlowPoint {
		rows, err := s.store.stableSupplyDailySeries(ctx, asset, source, from)
		if err != nil {
			slog.Warn("stablecoin signals: supply series", "asset", asset, "source", source, "error", err)
			return nil
		}
		var out []stableFlowPoint
		for _, r := range rows {
			if r.CirculatingNative != nil && *r.CirculatingNative > 0 {
				out = append(out, stableFlowPoint{Day: r.Day, Value: *r.CirculatingNative})
			}
		}
		return out
	}
	iss := points("issuer")
	if stableFlowCovers(iss, stableS4IssuerMinDays, now) {
		return stableFlowInput{Source: "issuer", Points: iss, Now: now}
	}
	agg := points("aggregator")
	if len(agg) < stableS4MedianPoints+1 && len(iss) >= stableS4MedianPoints+1 {
		return stableFlowInput{Source: "issuer", Points: iss, Now: now}
	}
	return stableFlowInput{Source: "aggregator", Points: agg, Now: now}
}

// stableFlowCovers reports whether a series has each of the n days ending on
// its latest day, and its latest day is within stableS4MaxStaleDays UTC days
// of now.
func stableFlowCovers(points []stableFlowPoint, n int, now time.Time) bool {
	if len(points) < n {
		return false
	}
	last, err := time.Parse(time.DateOnly, points[len(points)-1].Day)
	if err != nil || stableFlowDayStale(last, now) {
		return false
	}
	have := make(map[string]bool, len(points))
	for _, p := range points {
		have[p.Day] = true
	}
	for i := 0; i < n; i++ {
		if !have[last.AddDate(0, 0, -i).Format(time.DateOnly)] {
			return false
		}
	}
	return true
}

// healthInput gathers an asset's S13 inputs; the engine adds the facts and
// venue checks.
func (s *stablecoinService) healthInput(def stableAssetDef, state *stableSupplyState) stableHealthInput {
	var h stableHealthInput
	if s.health != nil {
		for _, key := range []string{def.Issuer, "defillama", "rpc_supply", "rpc_controls"} {
			if s.health.stale3x(key) {
				h.StaleSources = append(h.StaleSources, key)
			}
		}
		for _, sm := range s.health.samplers() {
			if sm.Stalled {
				h.StalledSamplers = append(h.StalledSamplers, sm.Name)
			}
		}
	}
	if state != nil {
		for _, r := range state.Assets[def.ID].DataHealth.Reasons {
			if r.Code == "tether_sum_mismatch" {
				h.TetherSumMismatch = true
			}
		}
	}
	return h
}

// signalStates lists every asset's signal states (alert counts).
func (s *stablecoinService) signalStates() []string {
	if s == nil || s.signals == nil {
		return nil
	}
	var out []string
	for _, def := range stableCoreAssets {
		for _, sig := range s.signals.snapshot(def.ID) {
			out = append(out, sig.State)
		}
	}
	return out
}
