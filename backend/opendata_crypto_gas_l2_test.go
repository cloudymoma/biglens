package main

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

func relAlmost(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func testCalibration() *GasCalibration {
	return &GasCalibration{
		USDTHolder:      TransferProfile{Energy: 64285, Bandwidth: 345, SharePct: 78.6},
		USDTNew:         TransferProfile{Energy: 130285, Bandwidth: 345, SharePct: 21.4},
		USDCTransferGas: 45160,
		USDCSamples:     12000,
		MeasuredAt:      "2026-10-04T08:00:00Z",
	}
}

func TestL2ChainConfigs(t *testing.T) {
	want := map[string]uint64{"eth": 1, "arb": 42161, "op": 10, "base": 8453}
	var ids []string
	for _, c := range l2Chains {
		ids = append(ids, c.ID)
		if c.ChainID != want[c.ID] {
			t.Errorf("%s chain id = %d, want %d", c.ID, c.ChainID, want[c.ID])
		}
		to, value, data := c.actionCall(l2Action{Kind: l2ActionUSDC})
		if hex.EncodeToString(to) != strings.ToLower(strings.TrimPrefix(c.USDC, "0x")) || value != 0 ||
			hex.EncodeToString(data[:4]) != "a9059cbb" {
			t.Errorf("%s: USDC action is not transfer() on the chain's USDC", c.ID)
		}
	}
	if strings.Join(ids, ",") != "eth,arb,op,base" {
		t.Fatalf("chain order = %v", ids)
	}
}

// No calibration, no USDC column: the ladder never prices a guessed gas figure.
func TestL2ActionsFor(t *testing.T) {
	if acts := l2ActionsFor(nil); len(acts) != 1 || acts[0].Gas != 21000 {
		t.Errorf("without calibration: %+v, want only the 21,000-gas ETH transfer", acts)
	}
	acts := l2ActionsFor(testCalibration())
	if len(acts) != 2 || acts[1].Kind != l2ActionUSDC || acts[1].Gas != 45160 || acts[1].Source == "" {
		t.Errorf("with calibration: %+v, want a measured 45,160-gas USDC transfer", acts)
	}
}

func TestFetchL2QuoteOPStack(t *testing.T) {
	srv := rpcServer(t, map[string]string{"eth_gasPrice": "0xf4b63", "eth_call": "0x3ba8f408"}, "")
	c := l2ChainConfig{ID: "op", Kind: l2KindOPStack, RPC: srv.URL, ChainID: 10, USDC: "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"}
	q, err := fetchL2Quote(context.Background(), c, l2ActionsFor(testCalibration()))
	if err != nil {
		t.Fatal(err)
	}
	if q.GasPriceWei != 1002339 || len(q.L1Wei) != 2 || q.L1Wei[0] != 1000928264 {
		t.Errorf("quote = %+v", q)
	}
}

func TestFetchL2QuoteArbitrum(t *testing.T) {
	word := func(n string) string { return strings.Repeat("0", 64-len(n)) + n }
	srv := rpcServer(t, map[string]string{"eth_call": "0x" + word("c8") + word("1314470") + word("183eca")}, "")
	c := l2ChainConfig{ID: "arb", Kind: l2KindArbitrum, RPC: srv.URL, ChainID: 42161, USDC: "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"}
	q, err := fetchL2Quote(context.Background(), c, l2ActionsFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if q.GasPriceWei != 20006000 || q.L1Wei[0] != 200*20006000 {
		t.Errorf("quote = %+v", q)
	}
}

func l2TestQuotes() map[string]*l2Quote {
	return map[string]*l2Quote{
		"eth": {GasPriceWei: 1e9, L1Wei: []float64{0, 0}},
		"op":  {GasPriceWei: 1e6, L1Wei: []float64{1e9, 1e9}},
	}
}

func TestL2LadderFrom(t *testing.T) {
	usd := 2700.0
	actions := l2ActionsFor(testCalibration())
	ladder := l2LadderFrom(l2TestQuotes(), map[string]error{}, actions, &usd)
	byID := map[string]L2LadderRow{}
	for _, r := range ladder.Rows {
		byID[r.ID] = r
	}
	if !relAlmost(byID["eth"].Actions[0].TotalETH, 21000*1e9/1e18) || byID["eth"].Actions[0].SavingsPct != nil {
		t.Errorf("eth row = %+v", byID["eth"])
	}
	usdc := byID["op"].Actions[1] // 45,160 gas × 1e6 wei + 1e9 wei L1
	wantTotal := 45160*1e6 + 1e9
	if usdc.Gas != 45160 || !relAlmost(usdc.TotalETH, wantTotal/1e18) || !relAlmost(usdc.ExecETH, 45160*1e6/1e18) {
		t.Errorf("op USDC = %+v, want execution priced at the measured gas", usdc)
	}
	if usdc.SavingsPct == nil || !relAlmost(*usdc.SavingsPct, (1-wantTotal/(45160*1e9))*100) {
		t.Errorf("op USDC savings = %v", usdc.SavingsPct)
	}
	if usdc.TotalUSD == nil || !relAlmost(*usdc.TotalUSD, wantTotal/1e18*2700) || usdc.Source == "" {
		t.Errorf("op USDC usd/source = %v / %q", usdc.TotalUSD, usdc.Source)
	}
}

func TestL2LadderFromWithoutUSDC(t *testing.T) {
	ladder := l2LadderFrom(l2TestQuotes(), map[string]error{}, l2ActionsFor(nil), nil)
	for _, r := range ladder.Rows {
		if r.Error == "" && len(r.Actions) != 1 {
			t.Errorf("%s has %d actions, want only the ETH transfer", r.ID, len(r.Actions))
		}
	}
	if !strings.Contains(ladder.Note, "USDC") {
		t.Errorf("note %q must say why the USDC column is missing", ladder.Note)
	}
}

func TestL2LadderFromPartialFailure(t *testing.T) {
	ladder := l2LadderFrom(l2TestQuotes(), map[string]error{"arb": errors.New("evm rpc: 429")}, l2ActionsFor(nil), nil)
	for _, r := range ladder.Rows {
		if r.ID == "arb" && (r.Error == "" || len(r.Actions) != 0) {
			t.Errorf("arb = %+v, want only its error", r)
		}
		if (r.ID == "eth" || r.ID == "op") && (r.Error != "" || len(r.Actions) != 1) {
			t.Errorf("%s = %+v, want a priced row", r.ID, r)
		}
	}
}

func TestL2LadderFromWithoutL1(t *testing.T) {
	quotes := l2TestQuotes()
	delete(quotes, "eth")
	ladder := l2LadderFrom(quotes, map[string]error{"eth": errors.New("down")}, l2ActionsFor(nil), nil)
	for _, r := range ladder.Rows {
		for _, a := range r.Actions {
			if a.SavingsPct != nil {
				t.Errorf("%s savings = %v, want nil without the L1 row", r.ID, *a.SavingsPct)
			}
		}
	}
}
