package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestRiskStore(t *testing.T) *riskStore {
	t.Helper()
	s, err := openRiskStore(filepath.Join(t.TempDir(), "sub", "t.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenRiskStoreCreatesDirAndIsReopenable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "security.db")
	s, err := openRiskStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s.Close()
	// Second open must not fail on "table already exists".
	s2, err := openRiskStore(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("db file missing: %v", err)
	}
}

func TestOpenRiskStoreUnwritablePathFails(t *testing.T) {
	if _, err := openRiskStore("/proc/definitely/not/writable/security.db"); err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

func TestReplaceListAndHits(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	entries := []listEntry{
		{Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		{Address: "0x1111111111111111111111111111111111111111", Label: "tagged USDT"},
	}
	if err := s.replaceList(ctx, "ofac", entries, "h1", now); err != nil {
		t.Fatal(err)
	}
	hits, err := s.listHits(ctx, "0x098b716b8aaf21512996dc57eb0615e2383e2f96")
	if err != nil || len(hits) != 1 || hits[0].Source != "ofac" {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
	st, _ := s.getSyncState(ctx, "ofac")
	if st.RowCount != 2 || st.ContentHash != "h1" || st.LastOKAt != "2026-09-26T06:00:00Z" || st.UpstreamChangedAt != "2026-09-26T06:00:00Z" {
		t.Errorf("sync state after first replace = %+v", st)
	}

	// Same content later: upstream_changed_at must not move, last_ok_at must.
	later := now.Add(6 * time.Hour)
	if err := s.replaceList(ctx, "ofac", entries, "h1", later); err != nil {
		t.Fatal(err)
	}
	st, _ = s.getSyncState(ctx, "ofac")
	if st.UpstreamChangedAt != "2026-09-26T06:00:00Z" || st.LastOKAt != "2026-09-26T12:00:00Z" {
		t.Errorf("unchanged content moved upstream_changed_at: %+v", st)
	}

	// Replace drops rows that disappeared upstream (e.g. OFAC delisting).
	if err := s.replaceList(ctx, "ofac", entries[1:], "h2", later); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.listHits(ctx, "0x098b716b8aaf21512996dc57eb0615e2383e2f96")
	if len(hits) != 0 {
		t.Errorf("delisted address still hits: %+v", hits)
	}
}

// Lookups read while the syncer replaces a list; under WAL a reader must see
// either the old or the new snapshot, never the half-deleted table.
func TestRiskStoreConcurrentReadDuringReplace(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	target := "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	entries := []listEntry{{Address: target}}
	for i := 0; i < 200; i++ {
		entries = append(entries, listEntry{Address: fmt.Sprintf("0x%040x", i+1)})
	}
	if err := s.replaceList(ctx, "ofac", entries, "h0", time.Now()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			hits, err := s.listHits(ctx, target)
			if err != nil || len(hits) != 1 {
				errs <- fmt.Errorf("hits = %v, err = %v", hits, err)
				return
			}
		}
	}()
	for i := 1; i <= 20; i++ {
		if err := s.replaceList(ctx, "ofac", entries, fmt.Sprintf("h%d", i), time.Now()); err != nil {
			t.Fatalf("replace %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("reader saw a half-replaced list: %v", err)
	default:
	}
}

func TestReplaceListDuplicateAddressFailsLoudly(t *testing.T) {
	s := newTestRiskStore(t)
	dup := []listEntry{{Address: "0xabababababababababababababababababababab"}, {Address: "0xabababababababababababababababababababab"}}
	if err := s.replaceList(context.Background(), "mew_darklist", dup, "h", time.Now()); err == nil {
		t.Fatal("expected PK violation; callers must dedupe first (spec §6)")
	}
}

func TestRejectAndErrorBookkeeping(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	s.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x2222222222222222222222222222222222222222"}}, "h1", now)

	if err := s.markListRejected(ctx, "mew_darklist", "shrank 652→3", "h9", 3); err != nil {
		t.Fatal(err)
	}
	st, _ := s.getSyncState(ctx, "mew_darklist")
	if st.PendingHash != "h9" || st.PendingCount != 3 || st.LastError != "shrank 652→3" || st.LastOKAt != "2026-09-26T06:00:00Z" {
		t.Errorf("reject bookkeeping wrong (last_ok_at must not move): %+v", st)
	}
	// A successful replace clears pending and last_error.
	s.replaceList(ctx, "mew_darklist", []listEntry{{Address: "0x3333333333333333333333333333333333333333"}}, "h9", now.Add(time.Hour))
	st, _ = s.getSyncState(ctx, "mew_darklist")
	if st.PendingHash != "" || st.PendingCount != 0 || st.LastError != "" {
		t.Errorf("replace did not clear pending/error: %+v", st)
	}
	if err := s.setSyncError(ctx, "ofac", "network_error"); err != nil {
		t.Fatal(err)
	}
	st, _ = s.getSyncState(ctx, "ofac")
	if st.LastError != "network_error" || st.LastOKAt != "" {
		t.Errorf("setSyncError on fresh source: %+v", st)
	}
}

func ev(tx string, logIndex int64, token, action, addr string, block int64) stablecoinEvent {
	return stablecoinEvent{TxHash: tx, LogIndex: logIndex, Token: token, Action: action, Address: addr,
		BlockNumber: block, BlockTime: fmt.Sprintf("2026-09-%02dT00:00:00Z", 1+block%28)}
}

func TestInsertStablecoinEventsIdempotentAndWatermarks(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	a := "0x1111111111111111111111111111111111111111"
	batch := []stablecoinEvent{ev("0xt1", 1, "USDT", "freeze", a, 10), ev("0xt2", 5, "USDC", "freeze", a, 11)}

	// First write from an empty state: coverage_from must become the date,
	// not stay '' (the MIN() trap).
	if err := s.insertStablecoinEvents(ctx, batch, "2026-08-27", "2026-09-25", now); err != nil {
		t.Fatal(err)
	}
	// Same events again (overlapping windows) + a forward-only update.
	if err := s.insertStablecoinEvents(ctx, batch, "", "2026-09-26", now); err != nil {
		t.Fatal(err)
	}
	st, _ := s.getSyncState(ctx, stablecoinSourceID)
	if st.RowCount != 2 || st.CoverageFrom != "2026-08-27" || st.Cursor != "2026-09-26" || st.LastOKAt != "2026-09-26T06:00:00Z" {
		t.Fatalf("after overlap: %+v", st)
	}
	// Watermarks never move backwards: a later-starting from and an older cursor are ignored.
	if err := s.insertStablecoinEvents(ctx, nil, "2026-09-01", "2026-09-10", now); err != nil {
		t.Fatal(err)
	}
	st, _ = s.getSyncState(ctx, stablecoinSourceID)
	if st.CoverageFrom != "2026-08-27" || st.Cursor != "2026-09-26" {
		t.Errorf("watermarks moved backwards: %+v", st)
	}
	// A backfill batch extends coverage_from only.
	if err := s.insertStablecoinEvents(ctx, nil, "2026-01-01", "", now); err != nil {
		t.Fatal(err)
	}
	st, _ = s.getSyncState(ctx, stablecoinSourceID)
	if st.CoverageFrom != "2026-01-01" || st.Cursor != "2026-09-26" {
		t.Errorf("backfill batch: %+v", st)
	}
}

// The backfill CLI and the service may write at the same time (spec §7.3).
func TestInsertStablecoinEventsConcurrentWatermarks(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(i int) { // service: forward cursor
			defer wg.Done()
			errs <- s.insertStablecoinEvents(ctx, nil, "", fmt.Sprintf("2026-09-%02d", 10+i), time.Now())
		}(i)
		go func(i int) { // backfill: older coverage_from
			defer wg.Done()
			errs <- s.insertStablecoinEvents(ctx, nil, fmt.Sprintf("202%d-01-01", i), "", time.Now())
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.getSyncState(ctx, stablecoinSourceID)
	if st.CoverageFrom != "2020-01-01" || st.Cursor != "2026-09-19" {
		t.Errorf("watermarks = %s..%s, want 2020-01-01..2026-09-19", st.CoverageFrom, st.Cursor)
	}
}

func TestStablecoinStates(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	refrozen := "0x1111111111111111111111111111111111111111"
	sameBlock := "0x2222222222222222222222222222222222222222"
	unfrozen := "0x3333333333333333333333333333333333333333"
	destroyOnly := "0x4444444444444444444444444444444444444444"
	destroyedThenUnfrozen := "0x5555555555555555555555555555555555555555"
	twoTokens := "0xe07bd590e1198666230932bad5db3dbbe21e7d57"
	events := []stablecoinEvent{
		ev("0xa1", 1, "USDT", "freeze", refrozen, 1), ev("0xa2", 1, "USDT", "unfreeze", refrozen, 2), ev("0xa3", 1, "USDT", "freeze", refrozen, 3),
		// Same block 26049478: log_index decides (real case, spec §5.1).
		ev("0xb1", 515, "USDC", "unfreeze", sameBlock, 26049478), ev("0xb2", 107, "USDC", "freeze", sameBlock, 26049478),
		ev("0xc1", 1, "USDC", "unfreeze", unfrozen, 5),
		// Frozen before the 30-day window, destroyed inside it: only the destroy row exists.
		ev("0xd1", 1, "USDT", "destroy", destroyOnly, 6),
		ev("0xe1", 1, "USDT", "freeze", destroyedThenUnfrozen, 7), ev("0xe2", 1, "USDT", "destroy", destroyedThenUnfrozen, 8), ev("0xe3", 1, "USDT", "unfreeze", destroyedThenUnfrozen, 9),
		ev("0xf1", 1, "USDC", "freeze", twoTokens, 20), ev("0xf2", 1, "USDT", "freeze", twoTokens, 21),
	}
	if err := s.insertStablecoinEvents(ctx, events, "2026-08-27", "2026-09-25", time.Now()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		addr string
		want string // token:action, ordered by token
	}{
		{refrozen, "USDT:freeze"},
		{sameBlock, "USDC:unfreeze"},
		{unfrozen, "USDC:unfreeze"},
		{destroyOnly, "USDT:destroy"},
		{destroyedThenUnfrozen, "USDT:unfreeze"},
		{twoTokens, "USDC:freeze,USDT:freeze"},
		{"0x9999999999999999999999999999999999999999", ""},
	}
	for _, tt := range tests {
		states, err := s.stablecoinStates(ctx, tt.addr)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, st := range states {
			got = append(got, st.Token+":"+st.Action)
		}
		if strings.Join(got, ",") != tt.want {
			t.Errorf("%s: states = %v, want %s", tt.addr, got, tt.want)
		}
	}
	all, _ := s.stablecoinStates(ctx, "")
	if len(all) != 7 {
		t.Errorf("all states = %d rows, want 7 (one per token+address)", len(all))
	}
}

func TestDestroyedTotals(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	a, b := "0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222"
	e1, e2, e3 := ev("0xd1", 1, "USDT", "destroy", a, 1), ev("0xd2", 1, "USDT", "destroy", a, 2), ev("0xd3", 1, "USDT", "destroy", b, 3)
	e1.Amount, e2.Amount, e3.Amount = "1640000459211", "99999999999999999999", "1"
	s.insertStablecoinEvents(ctx, []stablecoinEvent{e1, e2, e3}, "2026-08-27", "2026-09-25", time.Now())
	got, err := s.destroyedTotals(ctx, a)
	if err != nil || got["USDT"].String() != "100000001640000459210" {
		t.Errorf("per-address total = %v, %v (must not overflow int64)", got, err)
	}
	all, _ := s.destroyedTotals(ctx, "")
	if all["USDT"].String() != "100000001640000459211" {
		t.Errorf("all total = %v", all)
	}
}

func TestRiskPoolForAddressesByChain(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	const (
		evmOFAC      = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
		evmMEW       = "0x2222222222222222222222222222222222222222"
		evmFrozen    = "0x3333333333333333333333333333333333333333"
		evmLook      = "0x4444444444444444444444444444444444444444"
		tronOFAC     = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
		tronFrozen   = "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7"
		tronUnfrozen = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
		tronLook     = "TM9gdoJo11111111111111111111Zjj4Yx"
	)
	s.replaceList(ctx, "ofac", []listEntry{{Address: evmOFAC}, {Address: tronOFAC}}, "h", time.Now())
	s.replaceList(ctx, "mew_darklist", []listEntry{{Address: evmMEW}, {Address: tronOFAC}}, "h", time.Now())
	s.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xf", LogIndex: 1, Token: "USDT", Action: "freeze", Address: evmFrozen, BlockNumber: 1, BlockTime: "2026-09-01T00:00:00Z"},
	}, "2026-08-27", "2026-09-25", time.Now())
	if err := s.insertTronStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xtf1", LogIndex: 1, Token: "USDT", Action: "freeze", Address: tronFrozen, BlockNumber: 10, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xtf2", LogIndex: 1, Token: "USDT", Action: "freeze", Address: tronUnfrozen, BlockNumber: 11, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xtu2", LogIndex: 2, Token: "USDT", Action: "unfreeze", Address: tronUnfrozen, BlockNumber: 12, BlockTime: "2026-09-02T00:00:00Z"},
	}, "2026-08-27", "2026-09-25", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.applyScamBatch(ctx, scamEthSourceID, "eth", []scamDayBatch{{
		Day: "2026-09-25",
		Lookalikes: []lookalikeRow{
			{Lookalike: evmLook, Imitated: evmOFAC, Hits: 5, Victims: 2, FirstSeen: "2026-09-01", LastSeen: "2026-09-25"},
		},
	}}, "2026-08-27", "2026-09-25", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.applyScamBatch(ctx, scamTronSourceID, "tron", []scamDayBatch{{
		Day: "2026-09-25",
		Lookalikes: []lookalikeRow{
			{Lookalike: tronLook, Imitated: tronOFAC, Hits: 3, Victims: 1, FirstSeen: "2026-09-01", LastSeen: "2026-09-25"},
		},
	}}, "2026-08-27", "2026-09-25", time.Now()); err != nil {
		t.Fatal(err)
	}

	addrs := []string{evmOFAC, evmMEW, evmFrozen, evmLook, tronOFAC, tronFrozen, tronUnfrozen, tronLook}

	ethPool, err := s.riskPoolForAddresses(ctx, "eth", addrs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ethPool[evmOFAC], ",") != "ofac" || strings.Join(ethPool[evmMEW], ",") != "mew_darklist" ||
		strings.Join(ethPool[evmFrozen], ",") != "stablecoin" || strings.Join(ethPool[evmLook], ",") != "scam_lookalikes" {
		t.Errorf("eth pool = %v", ethPool)
	}

	arbPool, err := s.riskPoolForAddresses(ctx, "arb", addrs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(arbPool[evmOFAC], ",") != "ofac" || strings.Join(arbPool[evmMEW], ",") != "mew_darklist" ||
		len(arbPool[evmFrozen]) != 0 || strings.Join(arbPool[evmLook], ",") != "scam_lookalikes" {
		t.Errorf("arb pool = %v (must include ETH lookalikes but not Ethereum stablecoin freeze)", arbPool)
	}

	tronPool, err := s.riskPoolForAddresses(ctx, "tron", addrs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tronPool[tronOFAC], ",") != "ofac" ||
		strings.Join(tronPool[tronFrozen], ",") != "tron_stablecoin" ||
		len(tronPool[tronUnfrozen]) != 0 ||
		strings.Join(tronPool[tronLook], ",") != "scam_lookalikes" ||
		len(tronPool[evmMEW]) != 0 || len(tronPool[evmFrozen]) != 0 {
		t.Errorf("tron pool = %v", tronPool)
	}
}
