package main

// L1-vs-L2 cost ladder for the Ethereum family (gas_fee_design.md §10):
// the full cost of an ETH transfer and a USDC transfer on Ethereum, Arbitrum
// One, Optimism and Base, split into L2 execution and L1 data fee. OP Stack
// L1 fees come from GasPriceOracle.getL1Fee over representative unsigned
// transactions; Arbitrum's from NodeInterface.gasEstimateL1Component.

import (
	"context"
	"encoding/hex"
	"fmt"
)

const (
	l2KindL1       = "l1"
	l2KindOPStack  = "opstack"
	l2KindArbitrum = "arbitrum"

	l2Recipient = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"
	// transfer(l2Recipient, 1 USDC)
	usdcTransferCalldata = "a9059cbb000000000000000000000000d8da6bf26964af9d7eed9e03e53415d37aa96045" +
		"00000000000000000000000000000000000000000000000000000000000f4240"
)

type l2Action struct {
	Label  string
	Gas    float64
	Approx bool
}

// Token transfer gas varies with storage state; ~65k is the usual figure.
var l2Actions = []l2Action{
	{Label: "ETH transfer", Gas: 21000},
	{Label: "USDC transfer", Gas: 65000, Approx: true},
}

type l2ChainConfig struct {
	ID, Name, Kind, RPC string
	// OP Stack: RLP-encoded unsigned EIP-1559 tx per action (hex), priced by
	// getL1Fee. Both currently sit at Fjord's minimum size, so the exact
	// bytes barely move the fee.
	UnsignedTx []string
	// Arbitrum: destination and calldata (hex) per action.
	ArbTo, ArbData []string
}

var l2Chains = []l2ChainConfig{
	{ID: "eth", Name: "Ethereum", Kind: l2KindL1, RPC: "https://ethereum-rpc.publicnode.com"},
	{ID: "arb", Name: "Arbitrum One", Kind: l2KindArbitrum, RPC: "https://arb1.arbitrum.io/rpc",
		ArbTo:   []string{l2Recipient, "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"},
		ArbData: []string{"", usdcTransferCalldata}},
	{ID: "op", Name: "Optimism", Kind: l2KindOPStack, RPC: "https://mainnet.optimism.io",
		UnsignedTx: []string{
			"02ed0a2a830f42408402faf08082520894d8da6bf26964af9d7eed9e03e53415d37aa9604587038d7ea4c6800080c0",
			"02f86b0a2a830f42408402faf08082fde8940b2c639c533813f4aa9d7837caf62653d097ff8580b844" + usdcTransferCalldata + "c0",
		}},
	{ID: "base", Name: "Base", Kind: l2KindOPStack, RPC: "https://mainnet.base.org",
		UnsignedTx: []string{
			"02ef8221052a830f42408402faf08082520894d8da6bf26964af9d7eed9e03e53415d37aa9604587038d7ea4c6800080c0",
			"02f86d8221052a830f42408402faf08082fde894833589fcd6edb6e08f4c7c32d4f71b54bda0291380b844" + usdcTransferCalldata + "c0",
		}},
}

// l2Quote holds one chain's prices in wei, one entry per l2Actions item.
type l2Quote struct {
	GasPriceWei float64
	ExecWei     []float64
	L1Wei       []float64
}

func fetchL2Quote(ctx context.Context, c l2ChainConfig) (*l2Quote, error) {
	q := &l2Quote{}
	if c.Kind == l2KindArbitrum {
		for i, a := range l2Actions {
			data, err := hex.DecodeString(c.ArbData[i])
			if err != nil {
				return nil, fmt.Errorf("%s calldata: %w", c.ID, err)
			}
			gasForL1, baseFee, err := arbL1Component(ctx, c.RPC, c.ArbTo[i], data)
			if err != nil {
				return nil, err
			}
			q.GasPriceWei = baseFee
			q.ExecWei = append(q.ExecWei, a.Gas*baseFee)
			q.L1Wei = append(q.L1Wei, gasForL1*baseFee)
		}
		return q, nil
	}
	gasPrice, err := evmGasPrice(ctx, c.RPC)
	if err != nil {
		return nil, err
	}
	q.GasPriceWei = gasPrice
	for i, a := range l2Actions {
		l1 := 0.0
		if c.Kind == l2KindOPStack {
			tx, err := hex.DecodeString(c.UnsignedTx[i])
			if err != nil {
				return nil, fmt.Errorf("%s unsigned tx: %w", c.ID, err)
			}
			if l1, err = opStackL1Fee(ctx, c.RPC, tx); err != nil {
				return nil, err
			}
		}
		q.ExecWei = append(q.ExecWei, a.Gas*gasPrice)
		q.L1Wei = append(q.L1Wei, l1)
	}
	return q, nil
}

type L2ActionCost struct {
	Label      string   `json:"label"`
	Approx     bool     `json:"approx"`
	ExecETH    float64  `json:"exec_eth"`
	L1ETH      float64  `json:"l1_eth"`
	TotalETH   float64  `json:"total_eth"`
	TotalUSD   *float64 `json:"total_usd"`
	L1SharePct float64  `json:"l1_share_pct"`
	SavingsPct *float64 `json:"savings_pct"`
}

type L2LadderRow struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	GasPriceGwei float64        `json:"gas_price_gwei"`
	Actions      []L2ActionCost `json:"actions"`
	Error        string         `json:"error,omitempty"`
}

type L2Ladder struct {
	Rows []L2LadderRow `json:"rows"`
	Note string        `json:"note"`
}

// l2LadderFrom prices every chain in l2Chains order; a chain without a quote
// gets an error row, and savings are only computed against a priced L1 row.
func l2LadderFrom(quotes map[string]*l2Quote, errs map[string]error, ethUSD *float64) L2Ladder {
	l1 := quotes["eth"]
	ladder := L2Ladder{
		Rows: make([]L2LadderRow, 0, len(l2Chains)),
		Note: "Total = L2 execution + L1 data fee (GasPriceOracle on OP Stack, NodeInterface on Arbitrum); token transfer gas is approximate",
	}
	for _, c := range l2Chains {
		row := L2LadderRow{ID: c.ID, Name: c.Name, Kind: c.Kind, Actions: []L2ActionCost{}}
		q, ok := quotes[c.ID]
		if !ok {
			row.Error = "no quote"
			if err := errs[c.ID]; err != nil {
				row.Error = err.Error()
			}
			ladder.Rows = append(ladder.Rows, row)
			continue
		}
		row.GasPriceGwei = q.GasPriceWei / 1e9
		for i, a := range l2Actions {
			total := q.ExecWei[i] + q.L1Wei[i]
			cost := L2ActionCost{
				Label:    a.Label,
				Approx:   a.Approx,
				ExecETH:  q.ExecWei[i] / 1e18,
				L1ETH:    q.L1Wei[i] / 1e18,
				TotalETH: total / 1e18,
			}
			if total > 0 {
				cost.L1SharePct = q.L1Wei[i] / total * 100
			}
			if ethUSD != nil {
				usd := total / 1e18 * *ethUSD
				cost.TotalUSD = &usd
			}
			if l1 != nil && c.ID != "eth" {
				if ref := l1.ExecWei[i] + l1.L1Wei[i]; ref > 0 {
					saving := (1 - total/ref) * 100
					cost.SavingsPct = &saving
				}
			}
			row.Actions = append(row.Actions, cost)
		}
		ladder.Rows = append(ladder.Rows, row)
	}
	return ladder
}
