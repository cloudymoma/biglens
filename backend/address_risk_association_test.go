package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	assocTarget = "0x1111111111111111111111111111111111111111"
	ofacAddr    = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	mewAddr     = "0x2222222222222222222222222222222222222222"
	frozenAddr  = "0x3333333333333333333333333333333333333333"
	thawedAddr  = "0x4444444444444444444444444444444444444444"
	usdtAddr    = "0xdac17f958d2ee523a2206206994597c13d831ec7"
	wbtcAddr    = "0x2260fac5e5542a773aa44fbcfedf7c193bc2c599"
	fakeToken   = "0x9999999999999999999999999999999999999999"
)

var testPool = riskPool{ofacAddr: {"ofac"}, mewAddr: {"mew_darklist"}, frozenAddr: {"stablecoin"}}

func row(hash, from, to, value, ts string) etherscanRow {
	return etherscanRow{Hash: hash, From: from, To: to, Value: value, IsError: "0", TimeStamp: ts}
}

func TestFormatUnits(t *testing.T) {
	for _, tt := range []struct {
		n    string
		dec  int
		want string
	}{
		{"1500000", 6, "1.5"}, {"1", 18, "0.000000000000000001"}, {"250000000", 8, "2.5"}, {"42", 0, "42"}, {"1000000", 6, "1"},
	} {
		n, _ := new(big.Int).SetString(tt.n, 10)
		if got := formatUnits(n, tt.dec); got != tt.want {
			t.Errorf("formatUnits(%s, %d) = %q, want %q", tt.n, tt.dec, got, tt.want)
		}
	}
}

func TestAssociationPoisoningFilters(t *testing.T) {
	tokentx := []etherscanRow{
		// Zero-value transferFrom "from" an OFAC address on the real USDT contract: poisoning.
		{Hash: "0xz", From: ofacAddr, To: assocTarget, Value: "0", TimeStamp: "1700000000", ContractAddress: usdtAddr},
		// Counterfeit token contract claiming a transfer from OFAC.
		{Hash: "0xf", From: ofacAddr, To: assocTarget, Value: "5000000", TimeStamp: "1700000001", ContractAddress: fakeToken},
	}
	txlist := []etherscanRow{
		{Hash: "0xe", From: assocTarget, To: ofacAddr, Value: "1000", IsError: "1", TimeStamp: "1700000002"}, // failed call
		row("0xself", assocTarget, assocTarget, "1", "1700000003"),
		row("0xzero", riskZeroAddress, assocTarget, "1", "1700000004"),
		row("0xthaw", thawedAddr, assocTarget, "1", "1700000005"), // unfrozen addresses are not in the pool
	}
	clues, scope := associationClues(assocTarget, txlist, tokentx, nil, testPool, "2026-09-26T12:00:00Z")
	if len(clues) != 0 {
		t.Errorf("clues = %+v, want none", clues)
	}
	if scope.TxList.N != 4 || scope.TokenTx.N != 2 || scope.TxListInternal.N != 0 || scope.Hops != 1 ||
		strings.Join(scope.TokenAllowlist, ",") != "USDT,USDC,DAI,WETH,WBTC" {
		t.Errorf("scope = %+v", scope)
	}
}

func TestAssociationRealInboundUSDT(t *testing.T) {
	tokentx := []etherscanRow{{Hash: "0xreal", From: ofacAddr, To: assocTarget, Value: "2500000", TimeStamp: "1700000000", ContractAddress: usdtAddr}}
	clues, _ := associationClues(assocTarget, nil, tokentx, nil, testPool, "2026-09-26T12:00:00Z")
	if len(clues) != 1 {
		t.Fatalf("clues = %+v", clues)
	}
	c := clues[0]
	a := c.Association
	if c.Severity != sevAssociation || c.Code != "association" || c.Source != "etherscan" || a.Direction != "in" ||
		a.Amount != "2.5 USDT" || a.TxCount != 1 || strings.Join(a.Channels, ",") != "tokentx" ||
		c.Title != "Received from "+ofacAddr || !strings.Contains(c.Detail, riskPoisoningNote) ||
		c.RefURL != "https://etherscan.io/tx/0xreal" || *c.ObservedAt != "2023-11-14T22:13:20Z" {
		t.Errorf("clue = %+v / %+v", c, a)
	}
}

// One counterparty → one clue; a tx seen in txlist and txlistinternal
// counts once, with both channels.
func TestAssociationMergesPerCounterparty(t *testing.T) {
	txlist := []etherscanRow{
		row("0xa", assocTarget, mewAddr, "100", "1700000000"),
		row("0xb", assocTarget, mewAddr, "200", "1700000100"),
	}
	internal := []etherscanRow{row("0xb", mewAddr, assocTarget, "50", "1700000100")} // refund inside tx 0xb
	clues, _ := associationClues(assocTarget, txlist, nil, internal, testPool, "x")
	if len(clues) != 1 {
		t.Fatalf("clues = %+v", clues)
	}
	a := clues[0].Association
	if a.TxCount != 2 || strings.Join(a.Channels, ",") != "txlist,internal" || a.Direction != "both" ||
		clues[0].Title != "Sent to and received from "+mewAddr {
		t.Errorf("association = %+v title %q", a, clues[0].Title)
	}
}

func TestAssociationInternalCounterparties(t *testing.T) {
	tests := []struct {
		name    string
		row     etherscanRow
		wantCP  string
		wantDir string
	}{
		{"flagged contract pays target (call)", row("0x1", frozenAddr, assocTarget, "10", "1"), frozenAddr, "in"},
		{"target creates a flagged address with ETH", etherscanRow{Hash: "0x2", From: assocTarget, To: "", ContractAddress: ofacAddr, Value: "10", IsError: "0", TimeStamp: "2"}, ofacAddr, "out"},
		{"flagged contract self-destructs to target", row("0x3", ofacAddr, assocTarget, "10", "3"), ofacAddr, "in"},
		{"target created by a flagged factory with ETH", etherscanRow{Hash: "0x4", From: mewAddr, To: "", ContractAddress: assocTarget, Value: "10", IsError: "0", TimeStamp: "4"}, mewAddr, "in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clues, _ := associationClues(assocTarget, nil, nil, []etherscanRow{tt.row}, testPool, "x")
			if len(clues) != 1 || clues[0].Association.Counterparty != tt.wantCP || clues[0].Association.Direction != tt.wantDir ||
				strings.Join(clues[0].Association.Channels, ",") != "internal" {
				t.Errorf("clues = %+v", clues)
			}
		})
	}
	failed := row("0x5", frozenAddr, assocTarget, "10", "5")
	failed.IsError = "1"
	if clues, _ := associationClues(assocTarget, nil, nil, []etherscanRow{failed, row("0x6", frozenAddr, assocTarget, "0", "6")}, testPool, "x"); len(clues) != 0 {
		t.Errorf("failed or zero-value internal rows made clues: %+v", clues)
	}
}

// Decimals come from the allowlist (WBTC has 8), never from Etherscan.
func TestAssociationAllowlistDecimals(t *testing.T) {
	tokentx := []etherscanRow{{Hash: "0xw", From: assocTarget, To: frozenAddr, Value: "150000000", TimeStamp: "1", ContractAddress: strings.ToUpper(wbtcAddr[:2]) + wbtcAddr[2:]}}
	clues, _ := associationClues(assocTarget, nil, tokentx, nil, testPool, "x")
	if len(clues) != 1 || clues[0].Association.Amount != "1.5 WBTC" || clues[0].Association.Direction != "out" ||
		strings.Contains(clues[0].Detail, riskPoisoningNote) {
		t.Errorf("clues = %+v", clues)
	}
}

func TestAssociationOrderAndTruncation(t *testing.T) {
	pool := riskPool{ofacAddr: {"ofac"}}
	var txlist []etherscanRow
	for i := 0; i < 205; i++ {
		cp := fmt.Sprintf("0x%040x", 0x1000+i)
		pool[cp] = []string{"mew_darklist"}
		txlist = append(txlist, row("0xm"+cp, assocTarget, cp, "1", "1"))
	}
	txlist = append(txlist, row("0xo", assocTarget, ofacAddr, "1", "1"))
	clues, scope := associationClues(assocTarget, txlist, nil, nil, pool, "x")
	if len(clues) != riskMaxAssociations || !scope.Truncated {
		t.Fatalf("len %d truncated %v", len(clues), scope.Truncated)
	}
	if clues[0].Association.Counterparty != ofacAddr {
		t.Errorf("OFAC counterparty must sort first, got %s", clues[0].Association.Counterparty)
	}
}

func TestAssociationScopeOldest(t *testing.T) {
	txlist := []etherscanRow{row("0x1", assocTarget, mewAddr, "1", "1700000100"), row("0x2", assocTarget, mewAddr, "1", "1700000000")}
	_, scope := associationClues(assocTarget, txlist, nil, nil, testPool, "x")
	if scope.TxList.N != 2 || scope.TxList.OldestAt != "2023-11-14T22:13:20Z" || scope.TokenTx.OldestAt != "" {
		t.Errorf("scope = %+v", scope)
	}
}

func TestRiskPoolEntries(t *testing.T) {
	s := newTestRiskStore(t)
	ctx := context.Background()
	s.replaceList(ctx, "ofac", []listEntry{{Address: ofacAddr}}, "h", time.Now())
	s.replaceList(ctx, "mew_darklist", []listEntry{{Address: mewAddr}, {Address: ofacAddr}}, "h", time.Now())
	s.insertStablecoinEvents(ctx, []stablecoinEvent{
		{TxHash: "0xf", LogIndex: 1, Token: "USDT", Action: "freeze", Address: frozenAddr, BlockNumber: 1, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xg", LogIndex: 1, Token: "USDC", Action: "freeze", Address: frozenAddr, BlockNumber: 2, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xt", LogIndex: 1, Token: "USDC", Action: "freeze", Address: thawedAddr, BlockNumber: 3, BlockTime: "2026-09-01T00:00:00Z"},
		{TxHash: "0xu", LogIndex: 1, Token: "USDC", Action: "unfreeze", Address: thawedAddr, BlockNumber: 4, BlockTime: "2026-09-02T00:00:00Z"},
	}, "2026-08-27", "2026-09-25", time.Now())
	pool, err := s.riskPoolEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pool[ofacAddr], ",") != "ofac,mew_darklist" || strings.Join(pool[frozenAddr], ",") != "stablecoin" || len(pool[thawedAddr]) != 0 {
		t.Errorf("pool = %v", pool)
	}
}

// Etherscan lists are newest first and txlist is read before internal: within
// one timestamp the first row seen (the outer transaction) stays the latest,
// not an internal refund from the same block.
func TestAssociationSameTimestampKeepsFirstRow(t *testing.T) {
	txlist := []etherscanRow{row("0xouter", assocTarget, mewAddr, "3000000000000000000", "1700000100")}
	internal := []etherscanRow{row("0xrefund", mewAddr, assocTarget, "1000000000000000000", "1700000100")}
	clues, _ := associationClues(assocTarget, txlist, nil, internal, testPool, "x")
	if len(clues) != 1 || clues[0].Association.TxHash != "0xouter" || clues[0].Association.Amount != "3 ETH" {
		t.Errorf("clues = %+v", clues)
	}
}

// No parsable timestamp: observed_at is null (spec §8.3), never 1970, and the
// clue still links its transaction.
func TestAssociationUnparsableTimestamp(t *testing.T) {
	clues, _ := associationClues(assocTarget, []etherscanRow{row("0xa", assocTarget, mewAddr, "1", "")}, nil, nil, testPool, "x")
	if len(clues) != 1 || clues[0].ObservedAt != nil || clues[0].Association.TxHash != "0xa" || clues[0].RefURL != "https://etherscan.io/tx/0xa" {
		t.Errorf("clues = %+v", clues)
	}
}
