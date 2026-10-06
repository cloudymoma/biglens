package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	flagTestEVMReal   = "0x123400000000000000000000000000000000abcd"
	flagTestEVMMimic  = "0x123499999999999999999999999999999999abcd"
	flagTestEVMOther  = "0x9999000000000000000000000000000000001111"
	flagTestTronReal  = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	flagTestTronMimic = "TR7NH1111111111111111111111111Lj6t"
)

func TestFlagZeroValuePoisoningIsWarning(t *testing.T) {
	txs := []payTx{
		// Newer: 0-value transfer from lookalike
		{
			TxHash:       "0x03",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:02:00Z",
			Amount:       "0",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        102,
		},
		// Middle: 0-value transfer from unrelated stranger (negative case: zero_value only, not lookalike)
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "0",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMOther,
			Block:        101,
		},
		// Older: real incoming from trusted counterparty
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "500",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Contains(txs[0].Flags, "zero_value") || !slices.Contains(txs[0].Flags, "lookalike") {
		t.Fatalf("txs[0] (mimic 0-value) flags = %v, want [zero_value lookalike]", txs[0].Flags)
	}
	if !slices.Equal(txs[1].Flags, []string{"zero_value"}) {
		t.Fatalf("txs[1] (stranger 0-value) flags = %v, want [zero_value]", txs[1].Flags)
	}
	if len(txs[2].Flags) != 0 {
		t.Fatalf("txs[2] (real incoming) flags = %v, want empty", txs[2].Flags)
	}
}

func TestFlagForgedZeroValueOutgoingIsPoisoning(t *testing.T) {
	txs := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		// Forged transferFrom(user, lookalike, 0) appears as outgoing 0-value
		{
			TxHash:       "0x02",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "0",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Contains(txs[1].Flags, "zero_value") || !slices.Contains(txs[1].Flags, "lookalike") {
		t.Fatalf("forged 0-value outgoing flags = %v, want zero_value + lookalike", txs[1].Flags)
	}
	if slices.Contains(txs[1].Flags, "sent_to_lookalike") {
		t.Fatalf("forged 0-value outgoing must NOT be flagged sent_to_lookalike: %v", txs[1].Flags)
	}
}

func TestFlagForgedCounterfeitOutgoingIsPoisoning(t *testing.T) {
	txs := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		// Fake token contract emits Transfer(user, mimic, 50000) without user signature
		{
			TxHash:       "0x02",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "50000",
			TokenTier:    tierCounterfeit,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
		// Subsequent real payment to flagTestEVMReal must not think flagTestEVMMimic was trusted
		{
			TxHash:       "0x03",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:02:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        102,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Contains(txs[1].Flags, "counterfeit_token") || !slices.Contains(txs[1].Flags, "lookalike") {
		t.Fatalf("forged counterfeit outgoing flags = %v, want counterfeit_token + lookalike", txs[1].Flags)
	}
	if slices.Contains(txs[1].Flags, "sent_to_lookalike") {
		t.Fatalf("forged counterfeit outgoing must NOT have sent_to_lookalike: %v", txs[1].Flags)
	}
	if len(txs[2].Flags) != 0 {
		t.Fatalf("subsequent genuine outgoing to real counterparty flags = %v, want empty", txs[2].Flags)
	}
}

func TestFlagDustFromLookalikeIsPoisoning(t *testing.T) {
	txs := []payTx{
		{
			TxHash:       "tx1",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "250",
			TokenTier:    tierNative,
			Counterparty: flagTestTronReal,
			Block:        100,
		},
		{
			TxHash:       "tx2",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "0.001",
			TokenTier:    tierNative,
			Counterparty: flagTestTronMimic,
			Block:        101,
		},
	}

	applyFlags(txs, "USDT", familyTron)

	if !slices.Contains(txs[1].Flags, "dust") || !slices.Contains(txs[1].Flags, "lookalike") {
		t.Fatalf("dust from lookalike flags = %v, want [dust lookalike]", txs[1].Flags)
	}
}

func TestFlagDustFromStrangerIsInfoOnly(t *testing.T) {
	stranger := "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	strangerMimic := "T9yD1999999999999999999999999xuWwb"
	txs := []payTx{
		{
			TxHash:       "tx1",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "0.5",
			TokenTier:    tierNative,
			Counterparty: stranger,
			Block:        100,
		},
		// Dust sender must not enter trust set, so a later lookalike of the dust sender is not flagged lookalike
		{
			TxHash:       "tx2",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "10",
			TokenTier:    tierNative,
			Counterparty: strangerMimic,
			Block:        101,
		},
	}

	applyFlags(txs, "USDT", familyTron)

	if !slices.Equal(txs[0].Flags, []string{"dust"}) {
		t.Fatalf("dust from stranger flags = %v, want [dust]", txs[0].Flags)
	}
	if len(txs[1].Flags) != 0 {
		t.Fatalf("later transfer after dust stranger flags = %v, want empty", txs[1].Flags)
	}
}

func TestFlagLookalikeIgnoresCounterfeitAsRealCounterparty(t *testing.T) {
	txs := []payTx{
		// Attacker sends counterfeit token first, before real counterparty appears
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "1000",
			TokenTier:    tierCounterfeit,
			Counterparty: flagTestEVMMimic,
			Block:        100,
		},
		// Real customer with same 4+4 sends genuine USDT later
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "500",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        101,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Equal(txs[0].Flags, []string{"counterfeit_token"}) {
		t.Fatalf("counterfeit tx flags = %v, want [counterfeit_token]", txs[0].Flags)
	}
	if len(txs[1].Flags) != 0 {
		t.Fatalf("real counterparty after counterfeit must not be flagged lookalike, got %v", txs[1].Flags)
	}
}

func TestFlagOneUSDTFromLookalikeDoesNotPoisonTrust(t *testing.T) {
	// Review P0-2 scenario:
	// 1. Real customer A sends 100 USDT
	// 2. Lookalike A' sends 1.00 USDT (non-dust, attempting to enter trust set)
	// 3. User pays real customer A 100 USDT
	txs := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "1.00",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
		{
			TxHash:       "0x03",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:02:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        102,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if len(txs[0].Flags) != 0 {
		t.Fatalf("real customer A inbound flags = %v, want empty", txs[0].Flags)
	}
	if !slices.Equal(txs[1].Flags, []string{"lookalike"}) {
		t.Fatalf("lookalike A' 1.00 USDT inbound flags = %v, want [lookalike]", txs[1].Flags)
	}
	if len(txs[2].Flags) != 0 {
		t.Fatalf("user payment to real customer A flags = %v, want empty (trust must not be poisoned)", txs[2].Flags)
	}
}

func TestFlagOutgoingRecipientOutranksInboundSender(t *testing.T) {
	// A' first appears as inbound after low-trust A (so A' is flagged lookalike and tainted),
	// then user actively sends genuine funds to A' -> A' upgrades to trustHigh,
	// preserving earlier flags on A' and adding no new flags on subsequent A' transactions.
	txs := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "10",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "50",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
		{
			TxHash:       "0x03",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:02:00Z",
			Amount:       "200",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        102,
		},
		{
			TxHash:       "0x04",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:03:00Z",
			Amount:       "300",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        103,
		},
		{
			TxHash:       "0x05",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:04:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        104,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Contains(txs[1].Flags, "lookalike") {
		t.Fatalf("earlier inbound from A' must keep lookalike flag, got %v", txs[1].Flags)
	}
	if len(txs[3].Flags) != 0 {
		t.Fatalf("subsequent inbound from high-trust A' must have no flags, got %v", txs[3].Flags)
	}
	if len(txs[4].Flags) != 0 {
		t.Fatalf("subsequent outbound to high-trust A' must have no flags, got %v", txs[4].Flags)
	}
}

func TestFlagSentToLookalikeOnlyComparesEarlierCounterparties(t *testing.T) {
	// Case 1: Outgoing to A' BEFORE A ever appears -> NOT flagged sent_to_lookalike.
	earlyOut := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        100,
		},
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        101,
		},
	}
	applyFlags(earlyOut, "USDT", familyEVM)
	if len(earlyOut[0].Flags) != 0 {
		t.Fatalf("outgoing before lookalike counterparty appeared must have no flags, got %v", earlyOut[0].Flags)
	}

	// Case 2: Outgoing to A' AFTER A is already in trust -> flagged sent_to_lookalike.
	lateOut := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		{
			TxHash:       "0x02",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
	}
	applyFlags(lateOut, "USDT", familyEVM)
	if !slices.Equal(lateOut[1].Flags, []string{"sent_to_lookalike"}) {
		t.Fatalf("outgoing to lookalike after real counterparty flags = %v, want [sent_to_lookalike]", lateOut[1].Flags)
	}
}

func TestFlagScanIsChronologicalRegardlessOfInputOrder(t *testing.T) {
	asc := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
		},
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "0",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
		{
			TxHash:       "0x03",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:02:00Z",
			Amount:       "50",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        102,
		},
	}
	desc := []payTx{asc[2], asc[1], asc[0]}

	applyFlags(asc, "USDT", familyEVM)
	applyFlags(desc, "USDT", familyEVM)

	// Input slice order must not be mutated.
	if desc[0].TxHash != "0x03" || desc[1].TxHash != "0x02" || desc[2].TxHash != "0x01" {
		t.Fatalf("applyFlags reordered input slice: %+v", desc)
	}
	for i := range asc {
		gotDesc := desc[len(desc)-1-i].Flags
		if !slices.Equal(asc[i].Flags, gotDesc) {
			t.Fatalf("tx %s flags differ between asc (%v) and desc (%v)", asc[i].TxHash, asc[i].Flags, gotDesc)
		}
	}
}

func TestFlagFailedOutgoing(t *testing.T) {
	txs := []payTx{
		{
			TxHash:       "0x01",
			Direction:    "out",
			Timestamp:    "2026-10-06T12:00:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMReal,
			Block:        100,
			Failed:       true,
		},
		// Failed outgoing recipient must NOT enter trust set
		{
			TxHash:       "0x02",
			Direction:    "in",
			Timestamp:    "2026-10-06T12:01:00Z",
			Amount:       "100",
			TokenTier:    tierNative,
			Counterparty: flagTestEVMMimic,
			Block:        101,
		},
	}

	applyFlags(txs, "USDT", familyEVM)

	if !slices.Equal(txs[0].Flags, []string{"failed"}) {
		t.Fatalf("failed outgoing flags = %v, want [failed]", txs[0].Flags)
	}
	if len(txs[1].Flags) != 0 {
		t.Fatalf("recipient of failed outgoing must not enter trust, got flags %v on later tx", txs[1].Flags)
	}
}

func TestLookalikeTronCaseSensitive(t *testing.T) {
	if !lookalike(flagTestTronReal, flagTestTronMimic, familyTron) {
		t.Fatalf("expected TRON lookalike match between %s and %s", flagTestTronReal, flagTestTronMimic)
	}
	// Same address is not a lookalike of itself
	if lookalike(flagTestTronReal, flagTestTronReal, familyTron) {
		t.Fatalf("identical TRON address must not be lookalike")
	}
	// Differing case in first 4 chars after 'T' must NOT match on TRON
	diffCase := "Tr7nh1111111111111111111111111Lj6t"
	if lookalike(flagTestTronReal, diffCase, familyTron) {
		t.Fatalf("case-different TRON prefix %s vs %s must not match", flagTestTronReal, diffCase)
	}
}

func TestLookalikeEVMCaseInsensitive(t *testing.T) {
	upperMimic := "0x123499999999999999999999999999999999ABCD"
	if !lookalike(flagTestEVMReal, upperMimic, familyEVM) {
		t.Fatalf("expected EVM case-insensitive lookalike match between %s and %s", flagTestEVMReal, upperMimic)
	}
	// Identical address with different hex casing must NOT be a lookalike of itself
	upperReal := "0x123400000000000000000000000000000000ABCD"
	if lookalike(flagTestEVMReal, upperReal, familyEVM) {
		t.Fatalf("same EVM address in different case must not be lookalike")
	}
}

func TestNormalizeMimicSymbol(t *testing.T) {
	usdtCases := []string{
		"USD₮0",
		"USD\u0422", // Cyrillic Т
		"U S D T",
		"US\u200bDT",
		"usdt.e",
	}
	for _, in := range usdtCases {
		if got := normalizeMimicSymbol(in); got != "USDT" {
			t.Errorf("normalizeMimicSymbol(%q) = %q, want USDT", in, got)
		}
	}

	usdcCases := []string{
		"USDC.e",
		"\ufeffUSDC",
		"ＵＳＤＣ0",
	}
	for _, in := range usdcCases {
		if got := normalizeMimicSymbol(in); got != "USDC" {
			t.Errorf("normalizeMimicSymbol(%q) = %q, want USDC", in, got)
		}
	}

	nonMatching := []string{"USDTX", "USDS", "DAI", "WETH"}
	for _, in := range nonMatching {
		got := normalizeMimicSymbol(in)
		if got == "USDT" || got == "USDC" {
			t.Errorf("normalizeMimicSymbol(%q) = %q, must not match USDT/USDC", in, got)
		}
	}
}

type queryCountingConnector struct {
	dsn       string
	drv       driver.Driver
	listCalls *atomic.Int32
}

func (c *queryCountingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &queryCountingConn{Conn: conn, listCalls: c.listCalls}, nil
}

func (c *queryCountingConnector) Driver() driver.Driver { return c.drv }

type queryCountingConn struct {
	driver.Conn
	listCalls *atomic.Int32
}

func (c *queryCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM list_entries") {
		c.listCalls.Add(1)
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

func TestApplyLocalHits(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)

	const (
		addrOFAC     = "0x1111111111111111111111111111111111111111"
		addrFrozen   = "0x2222222222222222222222222222222222222222"
		addrUnfrozen = "0x3333333333333333333333333333333333333333"
		addrClean    = "0x4444444444444444444444444444444444444444"
	)

	if err := store.replaceList(ctx, "ofac", []listEntry{{Address: addrOFAC, Label: "SDN"}}, "h1", time.Now()); err != nil {
		t.Fatalf("replaceList: %v", err)
	}
	if err := store.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xf1", LogIndex: 0, Token: "USDT", Action: "freeze", Address: addrFrozen, BlockNumber: 10, BlockTime: "2026-10-01T00:00:00Z"},
		{TxHash: "0xf2", LogIndex: 0, Token: "USDC", Action: "freeze", Address: addrUnfrozen, BlockNumber: 11, BlockTime: "2026-10-01T00:00:00Z"},
		{TxHash: "0xf3", LogIndex: 1, Token: "USDC", Action: "unfreeze", Address: addrUnfrozen, BlockNumber: 12, BlockTime: "2026-10-02T00:00:00Z"},
	}, "2026-10-01", "2026-10-05", time.Now()); err != nil {
		t.Fatalf("insertStablecoinEvents: %v", err)
	}

	// Wrap store.db with queryCountingConnector to assert a single batch query against list_entries.
	var listCalls atomic.Int32
	dsn := "file:" + filepath.Join(t.TempDir(), "count.db") + "?_pragma=journal_mode(WAL)"
	countingDB := sql.OpenDB(&queryCountingConnector{
		dsn:       dsn,
		drv:       store.db.Driver(),
		listCalls: &listCalls,
	})
	defer countingDB.Close()
	if _, err := countingDB.Exec(riskSchema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	countingStore := &riskStore{db: countingDB}
	if err := countingStore.replaceList(ctx, "ofac", []listEntry{{Address: addrOFAC, Label: "SDN"}}, "h1", time.Now()); err != nil {
		t.Fatalf("replaceList countingStore: %v", err)
	}
	if err := countingStore.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xf1", LogIndex: 0, Token: "USDT", Action: "freeze", Address: addrFrozen, BlockNumber: 10, BlockTime: "2026-10-01T00:00:00Z"},
		{TxHash: "0xf2", LogIndex: 0, Token: "USDC", Action: "freeze", Address: addrUnfrozen, BlockNumber: 11, BlockTime: "2026-10-01T00:00:00Z"},
		{TxHash: "0xf3", LogIndex: 1, Token: "USDC", Action: "unfreeze", Address: addrUnfrozen, BlockNumber: 12, BlockTime: "2026-10-02T00:00:00Z"},
	}, "2026-10-01", "2026-10-05", time.Now()); err != nil {
		t.Fatalf("insertStablecoinEvents countingStore: %v", err)
	}

	listCalls.Store(0)
	txs := []payTx{
		{TxHash: "0x1", Counterparty: addrOFAC, Flags: []string{}},
		{TxHash: "0x2", Counterparty: addrOFAC, Flags: []string{}}, // duplicate counterparty
		{TxHash: "0x3", Counterparty: addrFrozen, Flags: []string{}},
		{TxHash: "0x4", Counterparty: addrUnfrozen, Flags: []string{}},
		{TxHash: "0x5", Counterparty: addrClean, Flags: []string{}},
	}

	if err := applyLocalHits(ctx, countingStore, "eth", txs); err != nil {
		t.Fatalf("applyLocalHits(eth): %v", err)
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("list_entries queries = %d, want 1 batch query", got)
	}

	// OFAC hit on both txs[0] and txs[1]
	for _, i := range []int{0, 1} {
		if !slices.Contains(txs[i].Flags, "counterparty_listed") || !reflect.DeepEqual(txs[i].CounterpartyHits, []string{"ofac"}) {
			t.Fatalf("txs[%d] = flags %v hits %v, want counterparty_listed + [ofac]", i, txs[i].Flags, txs[i].CounterpartyHits)
		}
	}
	// Currently frozen hit on txs[2]
	if !slices.Contains(txs[2].Flags, "counterparty_listed") || !reflect.DeepEqual(txs[2].CounterpartyHits, []string{"stablecoin"}) {
		t.Fatalf("txs[2] (frozen) = flags %v hits %v, want counterparty_listed + [stablecoin]", txs[2].Flags, txs[2].CounterpartyHits)
	}
	// Unfrozen must NOT be hit
	if slices.Contains(txs[3].Flags, "counterparty_listed") || len(txs[3].CounterpartyHits) != 0 {
		t.Fatalf("txs[3] (unfrozen) = flags %v hits %v, want no hit", txs[3].Flags, txs[3].CounterpartyHits)
	}
	// Clean must NOT be hit
	if slices.Contains(txs[4].Flags, "counterparty_listed") || len(txs[4].CounterpartyHits) != 0 {
		t.Fatalf("txs[4] (clean) = flags %v hits %v, want no hit", txs[4].Flags, txs[4].CounterpartyHits)
	}

	// Nil store: returns error and does not add flags
	nilTxs := []payTx{{TxHash: "0x1", Counterparty: addrOFAC, Flags: []string{}}}
	if err := applyLocalHits(ctx, nil, "eth", nilTxs); err == nil {
		t.Fatalf("applyLocalHits(nil store) expected error, got nil")
	}
	if len(nilTxs[0].Flags) != 0 || len(nilTxs[0].CounterpartyHits) != 0 {
		t.Fatalf("nil store must not tag txs, got %+v", nilTxs[0])
	}
}
