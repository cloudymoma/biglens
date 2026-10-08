package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func TestScamWindowIdempotentAndRollbackOnFailure(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

	const (
		lookalikeAddr = "0x123499999999999999999999999999999999abcd"
		imitatedAddr  = "0x123400000000000000000000000000000000abcd"
		fakeContract  = "0xdeadbeef00000000000000000000000000000001"
	)

	day1 := scamDayBatch{
		Day: "2026-10-05",
		Lookalikes: []lookalikeRow{
			{Lookalike: lookalikeAddr, Imitated: imitatedAddr, Hits: 10, Victims: 5},
		},
		FakeTokens: []fakeTokenRow{
			{Contract: fakeContract, Symbol: "USDT", Transfers: 100, Recipients: 50, FirstSeen: "2026-10-05", LastSeen: "2026-10-05"},
		},
		Stats: map[string]float64{
			"eth_poison_hits":    10,
			"eth_poison_victims": 5,
			"eth_lookalikes":     1,
			"eth_fake_transfers": 100,
			"eth_fake_contracts": 1,
		},
	}

	if err := s.applyScamBatch(ctx, "scam_eth", "eth", []scamDayBatch{day1}, "2026-10-05", "2026-10-05", now); err != nil {
		t.Fatalf("first applyScamBatch: %v", err)
	}

	// Calling the same window a second time (retry simulation) must be rejected with errAlreadyApplied.
	err := s.applyScamBatch(ctx, "scam_eth", "eth", []scamDayBatch{day1}, "2026-10-05", "2026-10-05", now)
	if !errors.Is(err, errAlreadyApplied) {
		t.Fatalf("second applyScamBatch err = %v, want errAlreadyApplied", err)
	}

	// Standalone upsertLookalikes / upsertFakeTokens for day <= cursor must also be rejected.
	if err := s.upsertLookalikes(ctx, "eth", day1.Lookalikes, "2026-10-05"); !errors.Is(err, errAlreadyApplied) {
		t.Fatalf("upsertLookalikes(day <= cursor) err = %v, want errAlreadyApplied", err)
	}
	if err := s.upsertFakeTokens(ctx, "eth", day1.FakeTokens); !errors.Is(err, errAlreadyApplied) {
		t.Fatalf("upsertFakeTokens(day <= cursor) err = %v, want errAlreadyApplied", err)
	}

	// Verify counts were NOT accumulated twice.
	hits, err := s.lookalikeHits(ctx, "eth", []string{lookalikeAddr})
	if err != nil {
		t.Fatalf("lookalikeHits: %v", err)
	}
	if got := hits[lookalikeAddr]; got.Hits != 10 || got.Victims != 5 {
		t.Fatalf("after duplicate apply, lookalike = %+v, want Hits=10 Victims=5", got)
	}

	// Now simulate a write failure mid-transaction on day 2026-10-06:
	// first row updates lookalikeAddr (+7 hits, +3 victims), second row has an empty lookalike address.
	badDay2 := scamDayBatch{
		Day: "2026-10-06",
		Lookalikes: []lookalikeRow{
			{Lookalike: lookalikeAddr, Imitated: imitatedAddr, Hits: 7, Victims: 3},
			{Lookalike: "", Imitated: imitatedAddr, Hits: 1, Victims: 1}, // invalid -> triggers rollback
		},
	}
	if err := s.applyScamBatch(ctx, "scam_eth", "eth", []scamDayBatch{badDay2}, "", "2026-10-06", now); err == nil {
		t.Fatalf("expected error on badDay2, got nil")
	}

	// Both cursor and cumulative counts must remain untouched after rollback.
	st, err := s.getSyncState(ctx, "scam_eth")
	if err != nil {
		t.Fatalf("getSyncState: %v", err)
	}
	if st.Cursor != "2026-10-05" {
		t.Fatalf("cursor after rollback = %q, want 2026-10-05", st.Cursor)
	}
	hits, err = s.lookalikeHits(ctx, "eth", []string{lookalikeAddr})
	if err != nil {
		t.Fatalf("lookalikeHits after rollback: %v", err)
	}
	if got := hits[lookalikeAddr]; got.Hits != 10 || got.Victims != 5 {
		t.Fatalf("lookalike after rollback = %+v, want Hits=10 Victims=5", got)
	}
}

func TestTronStablecoinCaseSensitiveAndUnfreeze(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

	const (
		frozenAddr   = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
		unfrozenAddr = "TH1Lch89TUAD4yaNA3svLQW7pFbbzAPDT5"
	)

	evs := []stablecoinEvent{
		{TxHash: "t1", LogIndex: 0, Token: "USDT", Action: "freeze", Address: frozenAddr, BlockNumber: 100, BlockTime: "2026-10-05T01:00:00Z"},
		{TxHash: "t2", LogIndex: 1, Token: "USDT", Action: "destroy", Address: frozenAddr, Amount: "250000000", BlockNumber: 101, BlockTime: "2026-10-05T02:00:00Z"},
		{TxHash: "t3", LogIndex: 0, Token: "USDT", Action: "freeze", Address: unfrozenAddr, BlockNumber: 102, BlockTime: "2026-10-05T03:00:00Z"},
		{TxHash: "t4", LogIndex: 0, Token: "USDT", Action: "unfreeze", Address: unfrozenAddr, BlockNumber: 103, BlockTime: "2026-10-05T04:00:00Z"},
	}

	if err := s.insertTronStablecoinEvents(ctx, evs, "2026-10-05", "2026-10-05", now); err != nil {
		t.Fatalf("insertTronStablecoinEvents: %v", err)
	}

	// Re-applying the same cursor must return errAlreadyApplied.
	if err := s.insertTronStablecoinEvents(ctx, evs, "2026-10-05", "2026-10-05", now); !errors.Is(err, errAlreadyApplied) {
		t.Fatalf("duplicate insertTronStablecoinEvents err = %v, want errAlreadyApplied", err)
	}

	// Exact case matches.
	states, err := s.tronStablecoinStates(ctx, frozenAddr)
	if err != nil || len(states) != 1 || states[0].Action != "destroy" {
		t.Fatalf("tronStablecoinStates(exact) = %+v, err = %v", states, err)
	}

	// Wrong case must NOT match on TRON.
	lowerStates, err := s.tronStablecoinStates(ctx, strings.ToLower(frozenAddr))
	if err != nil || len(lowerStates) != 0 {
		t.Fatalf("tronStablecoinStates(lowercase) = %+v, err = %v, want empty", lowerStates, err)
	}

	// Frozen then unfrozen -> latest state is unfreeze.
	unfrozenStates, err := s.tronStablecoinStates(ctx, unfrozenAddr)
	if err != nil || len(unfrozenStates) != 1 || unfrozenStates[0].Action != "unfreeze" {
		t.Fatalf("tronStablecoinStates(unfrozen) = %+v, err = %v, want unfreeze", unfrozenStates, err)
	}

	totals, err := s.tronDestroyedTotals(ctx, frozenAddr)
	if err != nil || totals["USDT"] == nil || totals["USDT"].String() != "250000000" {
		t.Fatalf("tronDestroyedTotals = %v, err = %v, want 250000000", totals, err)
	}
}

func TestUpsertLookalikesFakeTokensAndDailyStats(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()

	const (
		lookalike = "0xaaaa99999999999999999999999999999999bbbb"
		imitated1 = "0xaaaa00000000000000000000000000000000bbbb"
		imitated2 = "0xaaaa11111111111111111111111111111111bbbb"
		fakeTok   = "0xcccc00000000000000000000000000000000dddd"
	)

	if err := s.upsertLookalikes(ctx, "eth", []lookalikeRow{
		{Lookalike: lookalike, Imitated: imitated1, Hits: 4, Victims: 2},
	}, "2026-10-05"); err != nil {
		t.Fatalf("upsertLookalikes day 1: %v", err)
	}
	if err := s.upsertLookalikes(ctx, "eth", []lookalikeRow{
		{Lookalike: lookalike, Imitated: imitated2, Hits: 6, Victims: 3},
	}, "2026-10-06"); err != nil {
		t.Fatalf("upsertLookalikes day 2: %v", err)
	}

	hits, err := s.lookalikeHits(ctx, "eth", []string{lookalike})
	if err != nil {
		t.Fatalf("lookalikeHits: %v", err)
	}
	got := hits[lookalike]
	if got.Hits != 10 || got.Victims != 5 || got.FirstSeen != "2026-10-05" || got.LastSeen != "2026-10-06" || got.Imitated != imitated2 {
		t.Fatalf("accumulated lookalike = %+v", got)
	}

	if err := s.upsertFakeTokens(ctx, "eth", []fakeTokenRow{
		{Contract: fakeTok, Symbol: "USDT", Transfers: 10, Recipients: 8, FirstSeen: "2026-10-05", LastSeen: "2026-10-05"},
		{Contract: fakeTok, Symbol: "USD₮", Transfers: 15, Recipients: 12, FirstSeen: "2026-10-06", LastSeen: "2026-10-06"},
	}); err != nil {
		t.Fatalf("upsertFakeTokens: %v", err)
	}
	fset, err := s.fakeTokenSet(ctx, "eth")
	if err != nil || !fset[fakeTok] {
		t.Fatalf("fakeTokenSet = %v, err = %v", fset, err)
	}

	// putDailyStats must overwrite idempotently on rerun.
	if err := s.putDailyStats(ctx, "2026-10-05", map[string]float64{"eth_poison_hits": 100}); err != nil {
		t.Fatalf("putDailyStats 1: %v", err)
	}
	if err := s.putDailyStats(ctx, "2026-10-05", map[string]float64{"eth_poison_hits": 42}); err != nil {
		t.Fatalf("putDailyStats 2: %v", err)
	}
	var val float64
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM scam_daily_stats WHERE day = '2026-10-05' AND metric = 'eth_poison_hits'`).Scan(&val); err != nil {
		t.Fatalf("read daily stat: %v", err)
	}
	if val != 42 {
		t.Fatalf("daily stat value = %v, want 42 (overwritten)", val)
	}
}

func TestPruneScamBoundaries(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()

	// today = 2026-10-08
	// retentionDays = 90 -> cutoff = 2026-07-10 (last_seen < 2026-07-10 deleted; 2026-07-10 kept)
	// stats retention = 400 -> cutoff = 2025-09-03 (day < 2025-09-03 deleted; 2025-09-03 kept)
	today := civil.Date{Year: 2026, Month: time.October, Day: 8}

	if err := s.upsertLookalikes(ctx, "eth", []lookalikeRow{
		{Lookalike: "0x0000000000000000000000000000000000000001", Imitated: "0x0000000000000000000000000000000000000009", Hits: 1, Victims: 1},
	}, "2026-07-09"); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertLookalikes(ctx, "eth", []lookalikeRow{
		{Lookalike: "0x0000000000000000000000000000000000000002", Imitated: "0x0000000000000000000000000000000000000009", Hits: 1, Victims: 1},
	}, "2026-07-10"); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertFakeTokens(ctx, "eth", []fakeTokenRow{
		{Contract: "0x00000000000000000000000000000000000000a1", Symbol: "USDT", Transfers: 1, Recipients: 1, FirstSeen: "2026-07-09", LastSeen: "2026-07-09"},
		{Contract: "0x00000000000000000000000000000000000000a2", Symbol: "USDT", Transfers: 1, Recipients: 1, FirstSeen: "2026-07-10", LastSeen: "2026-07-10"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.putDailyStats(ctx, "2025-09-02", map[string]float64{"btc_txs": 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.putDailyStats(ctx, "2025-09-03", map[string]float64{"btc_txs": 20}); err != nil {
		t.Fatal(err)
	}

	if err := s.pruneScam(ctx, 90, today); err != nil {
		t.Fatalf("pruneScam: %v", err)
	}

	hits, err := s.lookalikeHits(ctx, "eth", []string{
		"0x0000000000000000000000000000000000000001",
		"0x0000000000000000000000000000000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hits["0x0000000000000000000000000000000000000001"]; ok {
		t.Errorf("2026-07-09 lookalike should have been pruned")
	}
	if _, ok := hits["0x0000000000000000000000000000000000000002"]; !ok {
		t.Errorf("2026-07-10 lookalike on boundary should have been kept")
	}

	fset, err := s.fakeTokenSet(ctx, "eth")
	if err != nil {
		t.Fatal(err)
	}
	if fset["0x00000000000000000000000000000000000000a1"] {
		t.Errorf("2026-07-09 fake token should have been pruned")
	}
	if !fset["0x00000000000000000000000000000000000000a2"] {
		t.Errorf("2026-07-10 fake token on boundary should have been kept")
	}

	var statsCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scam_daily_stats`).Scan(&statsCount); err != nil {
		t.Fatal(err)
	}
	if statsCount != 1 {
		t.Errorf("scam_daily_stats count = %d, want 1 (only 2025-09-03 kept)", statsCount)
	}
}

type tableCountingConnector struct {
	dsn   string
	drv   driver.Driver
	table string
	calls *atomic.Int32
}

func (c *tableCountingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &tableCountingConn{Conn: conn, table: c.table, calls: c.calls}, nil
}

func (c *tableCountingConnector) Driver() driver.Driver { return c.drv }

type tableCountingConn struct {
	driver.Conn
	table string
	calls *atomic.Int32
}

func (c *tableCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM "+c.table) {
		c.calls.Add(1)
	}
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, query, args)
	}
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	//nolint:staticcheck
	return c.Conn.(driver.Queryer).Query(query, vals)
}

func TestLookalikeHitsSingleQuery(t *testing.T) {
	ctx := context.Background()
	base := newTestRiskStore(t)

	var calls atomic.Int32
	dsn := "file:" + filepath.Join(t.TempDir(), "lookalike_count.db") + "?_pragma=journal_mode(WAL)"
	countingDB := sql.OpenDB(&tableCountingConnector{
		dsn:   dsn,
		drv:   base.db.Driver(),
		table: "scam_lookalikes",
		calls: &calls,
	})
	defer countingDB.Close()
	if _, err := countingDB.Exec(riskSchema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store := &riskStore{db: countingDB}

	const (
		a1 = "0x1111111111111111111111111111111111111111"
		a2 = "0x2222222222222222222222222222222222222222"
		a3 = "0x3333333333333333333333333333333333333333"
	)
	if err := store.upsertLookalikes(ctx, "eth", []lookalikeRow{
		{Lookalike: a1, Imitated: a3, Hits: 2, Victims: 1},
		{Lookalike: a2, Imitated: a3, Hits: 5, Victims: 4},
	}, "2026-10-05"); err != nil {
		t.Fatal(err)
	}

	calls.Store(0)
	got, err := store.lookalikeHits(ctx, "eth", []string{a1, a2, a3})
	if err != nil {
		t.Fatalf("lookalikeHits: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("scam_lookalikes queries = %d, want 1", calls.Load())
	}
	if len(got) != 2 || got[a1].Hits != 2 || got[a2].Hits != 5 {
		t.Fatalf("lookalikeHits result = %+v", got)
	}
}
