package main

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

// relAlmost compares ETH-sized amounts (~1e-9) where an absolute tolerance
// would accept anything.
func relAlmost(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// The representative unsigned transactions must really be EIP-1559 txs for
// the right chain and recipient, or getL1Fee prices something else.
func TestL2ChainConfigs(t *testing.T) {
	var ids []string
	for _, c := range l2Chains {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "eth,arb,op,base" {
		t.Fatalf("chain order = %v, want eth,arb,op,base", ids)
	}
	usdc := map[string]string{
		"op":   "0b2c639c533813f4aa9d7837caf62653d097ff85",
		"base": "833589fcd6edb6e08f4c7c32d4f71b54bda02913",
	}
	for _, c := range l2Chains {
		switch c.Kind {
		case l2KindOPStack:
			if len(c.UnsignedTx) != len(l2Actions) {
				t.Fatalf("%s: %d txs, want one per action", c.ID, len(c.UnsignedTx))
			}
			for _, tx := range c.UnsignedTx {
				if b, err := hex.DecodeString(tx); err != nil || b[0] != 0x02 {
					t.Errorf("%s: tx %q is not an EIP-1559 (type 0x02) payload", c.ID, tx[:8])
				}
			}
			if !strings.Contains(c.UnsignedTx[0], strings.TrimPrefix(l2Recipient, "0x")) {
				t.Errorf("%s: ETH transfer does not pay the recipient", c.ID)
			}
			if !strings.Contains(c.UnsignedTx[1], usdc[c.ID]) || !strings.Contains(c.UnsignedTx[1], "a9059cbb") {
				t.Errorf("%s: USDC transfer is not transfer() on the chain's USDC", c.ID)
			}
		case l2KindArbitrum:
			if len(c.ArbTo) != len(l2Actions) || len(c.ArbData) != len(l2Actions) {
				t.Fatalf("arb: want one destination and calldata per action")
			}
		}
	}
}

func TestFetchL2QuoteOPStack(t *testing.T) {
	srv := rpcServer(t, map[string]string{"eth_gasPrice": "0xf4b63", "eth_call": "0x3ba8f408"}, "")
	q, err := fetchL2Quote(context.Background(), l2ChainConfig{ID: "op", Kind: l2KindOPStack, RPC: srv.URL, UnsignedTx: []string{"02aa", "02bb"}})
	if err != nil {
		t.Fatal(err)
	}
	if q.GasPriceWei != 1002339 || q.ExecWei[0] != 21000*1002339 || q.ExecWei[1] != 65000*1002339 || q.L1Wei[0] != 1000928264 {
		t.Errorf("quote = %+v", q)
	}
}

func TestFetchL2QuoteArbitrum(t *testing.T) {
	word := func(n string) string { return strings.Repeat("0", 64-len(n)) + n }
	srv := rpcServer(t, map[string]string{"eth_call": "0x" + word("c8") + word("1314470") + word("183eca")}, "")
	c := l2ChainConfig{ID: "arb", Kind: l2KindArbitrum, RPC: srv.URL, ArbTo: []string{l2Recipient, l2Recipient}, ArbData: []string{"", "a9059cbb"}}
	q, err := fetchL2Quote(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if q.GasPriceWei != 20006000 || q.ExecWei[0] != 21000*20006000 || q.L1Wei[0] != 200*20006000 {
		t.Errorf("quote = %+v", q)
	}
}

func TestFetchL2QuoteL1(t *testing.T) {
	srv := rpcServer(t, map[string]string{"eth_gasPrice": "0x3b9aca00"}, "")
	q, err := fetchL2Quote(context.Background(), l2ChainConfig{ID: "eth", Kind: l2KindL1, RPC: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if q.ExecWei[0] != 21000*1e9 || q.L1Wei[0] != 0 || q.L1Wei[1] != 0 {
		t.Errorf("quote = %+v", q)
	}
}

func l2TestQuotes() map[string]*l2Quote {
	return map[string]*l2Quote{
		"eth": {GasPriceWei: 1e9, ExecWei: []float64{21000e9, 65000e9}, L1Wei: []float64{0, 0}},
		"op":  {GasPriceWei: 1e6, ExecWei: []float64{21000e6, 65000e6}, L1Wei: []float64{1e9, 1e9}},
	}
}

func TestL2LadderFrom(t *testing.T) {
	usd := 2700.0
	ladder := l2LadderFrom(l2TestQuotes(), map[string]error{}, &usd)
	byID := map[string]L2LadderRow{}
	for _, r := range ladder.Rows {
		byID[r.ID] = r
	}
	eth, op := byID["eth"], byID["op"]
	if eth.GasPriceGwei != 1 || eth.Actions[0].SavingsPct != nil || !relAlmost(eth.Actions[0].TotalETH, 2.1e-5) {
		t.Errorf("eth row = %+v", eth)
	}
	a := op.Actions[0] // 21000 gas × 1e6 wei + 1e9 wei L1 = 2.2e10 wei
	if !relAlmost(a.TotalETH, 2.2e-8) || !relAlmost(a.L1ETH, 1e-9) || !relAlmost(a.L1SharePct, 1e9/2.2e10*100) {
		t.Errorf("op ETH transfer = %+v", a)
	}
	if a.SavingsPct == nil || !relAlmost(*a.SavingsPct, (1-2.2e10/2.1e13)*100) {
		t.Errorf("op savings = %v", a.SavingsPct)
	}
	if a.TotalUSD == nil || !relAlmost(*a.TotalUSD, 2.2e-8*2700) {
		t.Errorf("op USD = %v", a.TotalUSD)
	}
	if !op.Actions[1].Approx || op.Actions[0].Approx {
		t.Error("only the token transfer uses an approximate gas figure")
	}
	if ladder.Note == "" {
		t.Error("the ladder must say where the L1 fee comes from")
	}
}

// One L2 failing must leave the other rows intact and visible in order.
func TestL2LadderFromPartialFailure(t *testing.T) {
	ladder := l2LadderFrom(l2TestQuotes(), map[string]error{"arb": errors.New("evm rpc: 429")}, nil)
	if len(ladder.Rows) != len(l2Chains) {
		t.Fatalf("got %d rows, want %d", len(ladder.Rows), len(l2Chains))
	}
	for _, r := range ladder.Rows {
		switch r.ID {
		case "arb":
			if r.Error == "" || len(r.Actions) != 0 {
				t.Errorf("arb = %+v, want only its error", r)
			}
		case "eth", "op":
			if r.Error != "" || len(r.Actions) != len(l2Actions) {
				t.Errorf("%s = %+v, want a priced row", r.ID, r)
			}
		}
	}
}

// Without an L1 reference there is nothing to save against: no NaN, no 100%.
func TestL2LadderFromWithoutL1(t *testing.T) {
	quotes := l2TestQuotes()
	delete(quotes, "eth")
	ladder := l2LadderFrom(quotes, map[string]error{"eth": errors.New("down")}, nil)
	for _, r := range ladder.Rows {
		for _, a := range r.Actions {
			if a.SavingsPct != nil {
				t.Errorf("%s %s savings = %v, want nil without the L1 row", r.ID, a.Label, *a.SavingsPct)
			}
		}
	}
}
