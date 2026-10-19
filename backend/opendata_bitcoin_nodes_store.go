package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

const (
	btcNodesRefreshInterval = 6 * time.Hour
	btcNodesFailBackoff     = 6 * time.Hour
)

func saveBtcNodesSnapshot(ctx context.Context, store *riskStore, nodes BtcNodes) error {
	if store == nil || store.db == nil {
		return nil
	}
	if err := store.ensureBtcBQSchema(ctx); err != nil {
		return err
	}
	takenAt := nodes.AsOf
	if takenAt == "" {
		takenAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(nodes)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
INSERT INTO btc_nodes_snapshot (taken_at, payload) VALUES (?, ?)
ON CONFLICT(taken_at) DO UPDATE SET payload = excluded.payload`, takenAt, string(raw))
	if err != nil {
		return err
	}
	_, _ = store.db.ExecContext(ctx, `
DELETE FROM btc_nodes_snapshot
WHERE taken_at NOT IN (
  SELECT taken_at FROM btc_nodes_snapshot ORDER BY taken_at DESC LIMIT 30
)`)
	return nil
}

func loadLatestBtcNodesSnapshot(ctx context.Context, store *riskStore) (*BtcNodes, error) {
	if store == nil || store.db == nil {
		return nil, nil
	}
	if err := store.ensureBtcBQSchema(ctx); err != nil {
		return nil, err
	}
	var takenAt, payload string
	err := store.db.QueryRowContext(ctx, `
SELECT taken_at, payload FROM btc_nodes_snapshot ORDER BY taken_at DESC LIMIT 1`).Scan(&takenAt, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out BtcNodes
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, err
	}
	if out.AsOf == "" {
		out.AsOf = takenAt
	}
	return &out, nil
}

type btcNodesSyncer struct {
	mu       sync.Mutex
	syncMu   sync.Mutex
	store    *riskStore
	settings btcSettings
	geoDir   string
	now      func() time.Time

	activated  bool
	inFlight   bool
	lastSync   time.Time
	lastFail   time.Time
	cached     *BtcNodes
	lastBitErr string
	lastGeoErr string
}

func newBtcNodesSyncer(store *riskStore, settings btcSettings, geoDir string, now func() time.Time) *btcNodesSyncer {
	if now == nil {
		now = time.Now
	}
	return &btcNodesSyncer{
		store:    store,
		settings: settings,
		geoDir:   geoDir,
		now:      now,
	}
}

func (ns *btcNodesSyncer) isActivated() bool {
	if ns == nil {
		return false
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return ns.activated
}

// hourlyTick runs inside the HTTP-only background loop. It stays idle until
// activated by the first /mining request, then refreshes at most every 6 hours.
func (ns *btcNodesSyncer) hourlyTick(ctx context.Context) {
	if ns == nil || !ns.settings.Providers["bitnodes"] {
		return
	}
	now := ns.now().UTC()
	ns.mu.Lock()
	if !ns.activated || ns.inFlight ||
		(!ns.lastSync.IsZero() && now.Sub(ns.lastSync) < btcNodesRefreshInterval) ||
		(!ns.lastFail.IsZero() && now.Sub(ns.lastFail) < btcNodesFailBackoff) {
		ns.mu.Unlock()
		return
	}
	ns.mu.Unlock()
	_, _, _ = ns.syncOnce(ctx)
}

func (ns *btcNodesSyncer) syncOnce(ctx context.Context) (retNodes *BtcNodes, retBitErr string, retGeoErr string) {
	if ns == nil {
		return nil, "disabled", "disabled"
	}
	ns.syncMu.Lock()
	defer ns.syncMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("btc nodes sync panic recovered", "recover", r)
			ns.mu.Lock()
			ns.lastBitErr = "bad_response"
			ns.lastFail = ns.now().UTC()
			retNodes = ns.cached
			ns.mu.Unlock()
			retBitErr = "bad_response"
		}
	}()

	if !ns.settings.Providers["bitnodes"] {
		return nil, "disabled", ""
	}

	now := ns.now().UTC()
	snap, code := fetchBitnodesSnapshot(ctx)
	if code != "" || snap == nil {
		ns.mu.Lock()
		ns.lastBitErr = code
		ns.lastFail = now
		prev := ns.cached
		ns.mu.Unlock()
		if prev == nil && ns.store != nil {
			if persisted, err := loadLatestBtcNodesSnapshot(ctx, ns.store); err == nil && persisted != nil {
				ns.mu.Lock()
				ns.cached = persisted
				prev = persisted
				ns.mu.Unlock()
			}
		}
		return prev, code, ""
	}

	agg := aggregateBitnodes(snap)
	if agg.AsOf == "" {
		agg.AsOf = now.Format(time.RFC3339)
	}

	var geoHits map[netip.Addr]geoHit
	var geoErr string
	if ns.settings.Providers["dbip"] && len(agg.SortedIPs) > 0 {
		month := now.Format("2006-01")
		cPath, aPath, err := dbipFiles(ctx, ns.geoDir, month)
		if err != nil {
			geoErr = "bad_response"
		} else {
			geoHits = make(map[netip.Addr]geoHit, len(agg.SortedIPs))
			mergeJoinIPsInto(geoHits, agg.SortedIPs, streamDBIPCountryGz(cPath))
			mergeJoinIPsInto(geoHits, agg.SortedIPs, streamDBIPASNGz(aPath))
		}
	}

	dist := computeNodeDistribution(agg, geoHits)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = saveBtcNodesSnapshot(saveCtx, ns.store, dist)
	cancel()

	ns.mu.Lock()
	ns.cached = &dist
	ns.lastSync = now
	ns.lastFail = time.Time{}
	ns.lastBitErr = ""
	ns.lastGeoErr = geoErr
	ns.mu.Unlock()

	return &dist, "", geoErr
}

func (ns *btcNodesSyncer) touchAsync() {
	if ns == nil || !ns.settings.Providers["bitnodes"] {
		return
	}
	now := ns.now().UTC()
	ns.mu.Lock()
	ns.activated = true
	if ns.inFlight ||
		(!ns.lastSync.IsZero() && now.Sub(ns.lastSync) < btcNodesRefreshInterval) ||
		(!ns.lastFail.IsZero() && now.Sub(ns.lastFail) < btcNodesFailBackoff) {
		ns.mu.Unlock()
		return
	}
	ns.inFlight = true
	ns.mu.Unlock()

	go func() {
		defer func() {
			ns.mu.Lock()
			ns.inFlight = false
			ns.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _, _ = ns.syncOnce(ctx)
	}()
}

func (ns *btcNodesSyncer) get(ctx context.Context, s *bitcoinService) (*BtcNodes, BtcSourceStatus, BtcSourceStatus) {
	bitMeta := s.providerMeta("bitnodes")
	dbipMeta := s.providerMeta("dbip")
	bitStatus := BtcSourceStatus{
		ID:      bitMeta.ID,
		Label:   bitMeta.Label,
		License: bitMeta.License,
	}
	dbipStatus := BtcSourceStatus{
		ID:      dbipMeta.ID,
		Label:   dbipMeta.Label,
		License: dbipMeta.License,
	}

	if !s.providerEnabled("bitnodes") {
		bitStatus.Status = "disabled"
		if !s.providerEnabled("dbip") {
			dbipStatus.Status = "disabled"
		} else {
			dbipStatus.Status = "ok"
		}
		return nil, bitStatus, dbipStatus
	}
	if !s.providerEnabled("dbip") {
		dbipStatus.Status = "disabled"
	}

	if ns == nil {
		bitStatus.Status = "error"
		bitStatus.Error = "uninitialized"
		return nil, bitStatus, dbipStatus
	}

	ns.mu.Lock()
	ns.activated = true
	cached := ns.cached
	ns.mu.Unlock()

	if cached == nil && ns.store != nil {
		if persisted, err := loadLatestBtcNodesSnapshot(ctx, ns.store); err == nil && persisted != nil {
			ns.mu.Lock()
			ns.cached = persisted
			if t, err := time.Parse(time.RFC3339, persisted.AsOf); err == nil {
				ns.lastSync = t
			}
			cached = persisted
			ns.mu.Unlock()
		}
	}

	if cached != nil {
		ns.mu.Lock()
		bitErr := ns.lastBitErr
		geoErr := ns.lastGeoErr
		ns.mu.Unlock()
		if bitErr != "" {
			bitStatus.Status = "stale"
			bitStatus.Error = bitErr
		} else {
			bitStatus.Status = "ok"
		}
		bitStatus.AsOf = cached.AsOf
		if dbipStatus.Status != "disabled" {
			if geoErr != "" {
				dbipStatus.Status = "stale"
				dbipStatus.Error = geoErr
			} else {
				dbipStatus.Status = "ok"
				dbipStatus.AsOf = cached.AsOf
			}
		}
		return cached, bitStatus, dbipStatus
	}

	// Cold start: no persisted snapshot yet; fetch once synchronously unless in failure backoff.
	now := ns.now().UTC()
	ns.mu.Lock()
	inBackoff := !ns.lastFail.IsZero() && now.Sub(ns.lastFail) < btcNodesFailBackoff
	lastBitErr := ns.lastBitErr
	ns.mu.Unlock()
	if inBackoff {
		if lastBitErr == "" {
			lastBitErr = "bad_response"
		}
		bitStatus.Status = "error"
		bitStatus.Error = lastBitErr
		if dbipStatus.Status != "disabled" {
			dbipStatus.Status = "error"
			dbipStatus.Error = lastBitErr
		}
		return nil, bitStatus, dbipStatus
	}

	nodes, bitErr, geoErr := ns.syncOnce(ctx)
	if nodes != nil {
		if bitErr != "" {
			bitStatus.Status = "stale"
			bitStatus.Error = bitErr
		} else {
			bitStatus.Status = "ok"
		}
		bitStatus.AsOf = nodes.AsOf
		if dbipStatus.Status != "disabled" {
			if geoErr != "" {
				dbipStatus.Status = "error"
				dbipStatus.Error = geoErr
			} else {
				dbipStatus.Status = "ok"
				dbipStatus.AsOf = nodes.AsOf
			}
		}
		return nodes, bitStatus, dbipStatus
	}

	if bitErr == "" {
		bitErr = "bad_response"
	}
	bitStatus.Status = "error"
	bitStatus.Error = bitErr
	if dbipStatus.Status != "disabled" {
		if geoErr != "" {
			dbipStatus.Status = "error"
			dbipStatus.Error = geoErr
		} else {
			dbipStatus.Status = "error"
			dbipStatus.Error = bitErr
		}
	}
	return nil, bitStatus, dbipStatus
}
