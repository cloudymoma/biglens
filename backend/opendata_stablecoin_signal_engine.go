package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Signal engine (stablecoin.md §6, §8.5): it runs after every peg, supply
// and controls sample and on a watchdog tick, applies each signal's policy,
// and writes the current state (stable_signal_state) and every state change
// (stable_signal_events). Handlers read its in-memory copy.

// stableSignalInputs is everything one evaluation reads.
type stableSignalInputs struct {
	Now time.Time
	// Peg is the latest peg sample; the sampled signals advance only when
	// it is newer than the last one consumed.
	Peg        *stablePegResult
	PegStalled bool // the peg sampler missed 3 intervals (§13 Operations)
	// Supply is the latest supply view; S12's aggregator half advances only
	// when it is newer than the last one consumed.
	Supply          *stableSupplyState
	Controls        map[string]stableControlState
	ControlEvents   []stableControlEvent // the last 7 days are enough
	ControlsEnabled bool
	Facts           stableFacts
	Tether          *StableTetherPoint
	Flows           map[string]stableFlowInput   // by asset
	Health          map[string]stableHealthInput // by asset (S13; FactsStale is set by the engine)
}

// stableSignalRecord is one asset × signal row.
type stableSignalRecord struct {
	Asset, Signal          string
	State, Reason, Summary string
	Since, AsOf, UpdatedAt time.Time
	Inputs                 map[string]any
	Hyst                   stableHyst
}

// stableSignalEvent is one state change.
type stableSignalEvent struct {
	TS              time.Time
	Asset, Signal   string
	From, To        string
	Reason, Summary string
	Inputs          map[string]any
}

type stableSignalEngine struct {
	store *riskStore
	now   func() time.Time

	mu         sync.Mutex
	loaded     bool
	recs       map[string]*stableSignalRecord // asset|signal
	pegAsOf    time.Time                      // newest peg sample consumed
	supplyAsOf time.Time                      // newest supply view consumed
}

func newStableSignalEngine(store *riskStore, now func() time.Time) *stableSignalEngine {
	return &stableSignalEngine{store: store, now: now, recs: map[string]*stableSignalRecord{}}
}

// apply evaluates every signal of every core asset on in and persists the
// result.
func (e *stableSignalEngine) apply(ctx context.Context, in stableSignalInputs) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.loaded {
		if e.store == nil {
			e.loaded = true
		} else if recs, err := e.store.stableSignalStates(ctx); err != nil {
			slog.Warn("stablecoin signals: load state", "error", err)
		} else {
			for k, r := range recs {
				e.recs[k] = r
			}
			e.loaded = true
		}
	}
	now := in.Now
	newPeg := in.Peg != nil && in.Peg.AsOf.After(e.pegAsOf)
	if newPeg {
		e.pegAsOf = in.Peg.AsOf
	}
	newSupply := in.Supply != nil && in.Supply.AsOf.After(e.supplyAsOf)
	if newSupply {
		e.supplyAsOf = in.Supply.AsOf
	}
	var events []stableSignalEvent
	set := func(asset, id, state, reason string, raw stableRaw, hyst *stableHyst) {
		key := asset + "|" + id
		rec, ok := e.recs[key]
		if !ok {
			rec = &stableSignalRecord{Asset: asset, Signal: id, State: state, Since: now}
			e.recs[key] = rec
		}
		if rec.State != state {
			events = append(events, stableSignalEvent{TS: now, Asset: asset, Signal: id, From: rec.State, To: state, Reason: reason,
				Summary: raw.Summary, Inputs: raw.Inputs})
			rec.State, rec.Since = state, now
		}
		rec.Reason, rec.Summary, rec.Inputs, rec.UpdatedAt = reason, raw.Summary, raw.Inputs, now
		if !raw.AsOf.IsZero() {
			rec.AsOf = raw.AsOf
		}
		if rec.Inputs == nil {
			rec.Inputs = map[string]any{}
		}
		if hyst != nil {
			rec.Hyst = *hyst
		}
	}
	level := func(asset, id string, raw stableRaw) {
		state, reason := stableRawState(raw)
		set(asset, id, state, reason, raw, nil)
	}
	sampled := func(asset, id string, raw stableRaw, thr [4]float64) {
		rec := e.recs[asset+"|"+id]
		var h stableHyst
		if rec != nil {
			h = rec.Hyst
		}
		h, state, reason := stableSampledStep(h, raw, thr, in.Peg.AsOf)
		set(asset, id, state, reason, raw, &h)
	}
	// placeholder keeps a signal that has had no sample yet at unknown.
	placeholder := func(asset, id, reason string) {
		if _, ok := e.recs[asset+"|"+id]; !ok {
			set(asset, id, stableStateUnknown, reason, newStableRaw(), nil)
		}
	}
	for _, def := range stableCoreAssets {
		asset := def.ID
		facts := in.Facts.Assets[asset]
		s6, factsStale := stableS6Raw(facts, now)
		var supply *StableSupplyAsset
		if in.Supply != nil {
			if a, ok := in.Supply.Assets[asset]; ok {
				supply = &a
			}
		}

		// Sampled: S1, S2, S3, S13 advance on a new peg sample.
		s3NA := stableS3Raw(asset, StableRelativePeg{})
		if newPeg {
			p := in.Peg.Assets[asset]
			sampled(asset, "S1", stableS1Raw(p), stableS1Thr)
			sampled(asset, "S2", stableS2Raw(p), stableS2Thr)
			if s3NA.NA {
				level(asset, "S3", s3NA)
			} else {
				sampled(asset, "S3", stableS3Raw(asset, in.Peg.Relative), stableS3Thr)
			}
			h := in.Health[asset]
			h.FactsStale, h.VenuesFailing = factsStale, stableVenuesFailing(in.Peg)
			sampled(asset, "S13", stableS13Raw(h), [4]float64{})
		}
		for _, id := range []string{"S1", "S2", "S3", "S13"} {
			if id == "S3" && s3NA.NA {
				level(asset, "S3", s3NA)
				continue
			}
			placeholder(asset, id, stableReasonWarmingUp)
		}
		if in.PegStalled {
			stalled := "peg sampler stalled: no sample since " + stableFmt(e.pegAsOf)
			for _, id := range []string{"S1", "S2", "S3"} {
				if rec := e.recs[asset+"|"+id]; rec.State != stableStateNA {
					raw := newStableRaw()
					raw.Summary = stalled
					set(asset, id, stableStateUnknown, stalled, raw, nil)
				}
			}
			if rec := e.recs[asset+"|S13"]; stableLevelOf(rec.State) < stableLevelWatch {
				raw := newStableRaw()
				raw.Summary, raw.Inputs = stalled, map[string]any{"stalled_samplers": []string{"peg"}}
				set(asset, "S13", stableStateWatch, "", raw, nil)
			}
		}

		// Confirmed: S12 (aggregator half confirmed here) and S8.
		e.applyS12(asset, supply, newSupply, set)
		level(asset, "S8", stableS8Raw(asset, in.Controls, in.ControlEvents, in.ControlsEnabled, now))

		// Level-triggered.
		flow := in.Flows[asset]
		if flow.Now.IsZero() {
			flow.Now = now
		}
		level(asset, "S4", stableS4Raw(flow))
		level(asset, "S5", stableS5Raw(asset, in.Tether))
		level(asset, "S6", s6)
		level(asset, "S10", stableS10Raw(supply))
		level(asset, "S11", stableS11Raw(facts))
	}
	if e.store == nil || !e.loaded {
		return nil
	}
	recs := make([]stableSignalRecord, 0, len(e.recs))
	for _, r := range e.recs {
		recs = append(recs, *r)
	}
	return e.store.saveStableSignals(ctx, recs, events)
}

// applyS12 combines the confirmed aggregator comparison with the on-chain
// comparison, which its sampler already confirms. Until the aggregator half
// is confirmed S12 is warming up, unless the on-chain half is at warning.
func (e *stableSignalEngine) applyS12(asset string, supply *StableSupplyAsset, newSupply bool,
	set func(asset, id, state, reason string, raw stableRaw, hyst *stableHyst)) {
	rec := e.recs[asset+"|S12"]
	var h stableHyst
	if rec != nil {
		h = rec.Hyst
	}
	if supply == nil {
		if rec == nil {
			set(asset, "S12", stableStateUnknown, stableReasonWarmingUp, newStableRaw(), &h)
		}
		return
	}
	agg, onchain := stableS12AggRaw(*supply), stableS12OnchainRaw(*supply)
	aggLevel, ok := h.Level, h.Confirmed
	if newSupply {
		h, aggLevel, ok = stableConfirmStep(h, agg.Level)
	}
	raw := newStableRaw()
	for k, v := range agg.Inputs {
		raw.Inputs[k] = v
	}
	for k, v := range onchain.Inputs {
		raw.Inputs[k] = v
	}
	raw.Inputs["aggregator_confirmed_level"] = nil
	if ok && aggLevel >= 0 {
		raw.Inputs["aggregator_confirmed_level"] = stableLevelNames[aggLevel]
	}
	raw.AsOf = supply.issuerAt
	var parts []string
	for _, s := range []string{agg.Summary, onchain.Summary} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	raw.Summary = joinNonEmpty(parts, "; ")
	switch {
	case onchain.Level == stableLevelWarning:
		raw.Level = stableLevelWarning
	case !ok:
		raw = stableUnknownRaw(raw, stableReasonWarmingUp)
	case aggLevel < 0 && onchain.Level < 0:
		raw = stableUnknownRaw(raw, "no comparison: "+agg.Reason+"; "+onchain.Reason)
	default:
		raw.Level = max(aggLevel, onchain.Level)
	}
	state, reason := stableRawState(raw)
	set(asset, "S12", state, reason, raw, &h)
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// stableLevelOf ranks a state (unknown and n/a are −1).
func stableLevelOf(state string) int {
	switch state {
	case stableStateNormal, stableStateInfo:
		return stableLevelNormal
	case stableStateWatch:
		return stableLevelWatch
	case stableStateWarning:
		return stableLevelWarning
	case stableStateCritical:
		return stableLevelCritical
	}
	return stableLevelUnknown
}

// snapshot returns an asset's signals in catalog order; a signal without a
// record is unknown (warming up).
func (e *stableSignalEngine) snapshot(asset string) []StableSignal {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]StableSignal, 0, len(stableSignalDefs))
	for _, def := range stableSignalDefs {
		s := StableSignal{ID: def.ID, Key: def.Key, Dimension: def.Dimension, Policy: def.Policy, Rule: stableSignalRuleFor(def, asset),
			State: stableStateUnknown, Reason: stableReasonWarmingUp, Inputs: map[string]any{}}
		if r := e.recs[asset+"|"+def.ID]; r != nil {
			s.State, s.Reason, s.Summary, s.Since, s.AsOf = r.State, r.Reason, r.Summary, stableFmt(r.Since), stableFmt(r.AsOf)
			if r.Inputs != nil {
				s.Inputs = r.Inputs
			}
		}
		out = append(out, s)
	}
	return out
}

// worst is an asset's summary state (§6.1).
func (e *stableSignalEngine) worst(asset string) string {
	var states []string
	for _, s := range e.snapshot(asset) {
		states = append(states, s.State)
	}
	return stableWorstState(states)
}

// --- store ---

func (s *riskStore) saveStableSignals(ctx context.Context, recs []stableSignalRecord, events []stableSignalEvent) error {
	if err := s.ensureStableSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rule := map[string]string{}
	for _, d := range stableSignalDefs {
		rule[d.ID] = d.Rule
	}
	for _, r := range recs {
		inputs, err := json.Marshal(r.Inputs)
		if err != nil {
			inputs = []byte("{}")
		}
		hyst, err := json.Marshal(r.Hyst)
		if err != nil {
			hyst = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO stable_signal_state
(asset, signal, state, reason, rule, summary, since, as_of, inputs_json, hyst_json, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.Asset, r.Signal, r.State, r.Reason, rule[r.Signal], r.Summary, stableTS(r.Since), stableNullTime(r.AsOf), string(inputs), string(hyst),
			stableTS(r.UpdatedAt)); err != nil {
			return err
		}
	}
	for _, ev := range events {
		inputs, err := json.Marshal(ev.Inputs)
		if err != nil {
			inputs = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO stable_signal_events
(ts, asset, signal, from_state, to_state, reason, summary, inputs_json) VALUES (?,?,?,?,?,?,?,?)`,
			stableTSNano(ev.TS), ev.Asset, ev.Signal, ev.From, ev.To, ev.Reason, ev.Summary, string(inputs)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stableTSNano is a fixed-width UTC timestamp, so text order is time order
// even within one second.
func stableTSNano(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func (s *riskStore) stableSignalStates(ctx context.Context) (map[string]*stableSignalRecord, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT asset, signal, state, reason, summary, since, as_of, inputs_json, hyst_json, updated_at FROM stable_signal_state`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]*stableSignalRecord{}
	for rs.Next() {
		var r stableSignalRecord
		var since, updated, inputs, hyst string
		var asOf sql.NullString
		if err := rs.Scan(&r.Asset, &r.Signal, &r.State, &r.Reason, &r.Summary, &since, &asOf, &inputs, &hyst, &updated); err != nil {
			return nil, err
		}
		r.Since, r.AsOf, r.UpdatedAt = parseStableTS(since), parseStableTS(asOf.String), parseStableTS(updated)
		if err := json.Unmarshal([]byte(inputs), &r.Inputs); err != nil || r.Inputs == nil {
			r.Inputs = map[string]any{}
		}
		_ = json.Unmarshal([]byte(hyst), &r.Hyst)
		out[r.Asset+"|"+r.Signal] = &r
	}
	return out, rs.Err()
}

// stableSignalEvents returns state changes, newest first ("" asset: all).
func (s *riskStore) stableSignalEvents(ctx context.Context, asset string, limit int) ([]stableSignalEvent, error) {
	if err := s.ensureStableSchema(ctx); err != nil {
		return nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT ts, asset, signal, from_state, to_state, reason, summary, inputs_json FROM stable_signal_events
WHERE (? = '' OR asset = ?) ORDER BY ts DESC, asset, signal LIMIT ?`, asset, asset, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []stableSignalEvent{}
	for rs.Next() {
		var ev stableSignalEvent
		var ts, inputs string
		if err := rs.Scan(&ts, &ev.Asset, &ev.Signal, &ev.From, &ev.To, &ev.Reason, &ev.Summary, &inputs); err != nil {
			return nil, err
		}
		ev.TS = parseStableTS(ts)
		if err := json.Unmarshal([]byte(inputs), &ev.Inputs); err != nil || ev.Inputs == nil {
			ev.Inputs = map[string]any{}
		}
		out = append(out, ev)
	}
	return out, rs.Err()
}
