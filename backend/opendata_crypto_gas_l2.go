package main

// L1-vs-L2 cost ladder for the Ethereum family (gas_fee_design.md §10–§11):
// the full cost of an ETH transfer and (when today's calibration measured it)
// a USDC transfer on Ethereum, Arbitrum One, Optimism and Base, split into L2
// execution and L1 data fee. Sample transactions are built at runtime; OP
// Stack L1 fees come from GasPriceOracle.getL1Fee, Arbitrum's from
// NodeInterface.gasEstimateL1Component.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	l2KindL1       = "l1"
	l2KindOPStack  = "opstack"
	l2KindArbitrum = "arbitrum"

	l2ActionNative = "native"
	l2ActionUSDC   = "usdc"

	l2Recipient = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"
	// ethTransferGas is the protocol's intrinsic gas for a plain ETH transfer.
	ethTransferGas = 21_000
)

type l2Action struct {
	Kind, Label, Source string
	Gas                 uint64
}

// l2ActionsFor prices the ETH transfer always and the USDC transfer only when
// today's calibration measured its gas — never a guessed figure.
func l2ActionsFor(cal *GasCalibration) []l2Action {
	actions := []l2Action{{Kind: l2ActionNative, Label: "ETH transfer", Gas: ethTransferGas, Source: "protocol intrinsic gas"}}
	if cal != nil {
		actions = append(actions, l2Action{Kind: l2ActionUSDC, Label: "USDC transfer",
			Gas: uint64(cal.USDCTransferGas), Source: "median on Ethereum, last 6h"})
	}
	return actions
}

// l2ChainConfig holds on-chain facts (chain ID, USDC contract) that never
// drift; endpoints come from conf.yaml (Task 4).
type l2ChainConfig struct {
	ID, Name, Kind string
	RPCs           []string
	ChainID        uint64
	USDC           string
}

var l2Chains = []l2ChainConfig{
	{ID: "eth", Name: "Ethereum", Kind: l2KindL1, RPCs: []string{"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org"},
		ChainID: 1, USDC: ethUSDCAddress},
	{ID: "arb", Name: "Arbitrum One", Kind: l2KindArbitrum, RPCs: []string{"https://arb1.arbitrum.io/rpc"},
		ChainID: 42161, USDC: "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"},
	{ID: "op", Name: "Optimism", Kind: l2KindOPStack, RPCs: []string{"https://mainnet.optimism.io"},
		ChainID: 10, USDC: "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"},
	{ID: "base", Name: "Base", Kind: l2KindOPStack, RPCs: []string{"https://mainnet.base.org"},
		ChainID: 8453, USDC: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"},
}

// actionCall returns the destination, value and calldata of a on chain c.
func (c l2ChainConfig) actionCall(a l2Action) (to []byte, value uint64, data []byte) {
	recipient := mustHexAddress(l2Recipient)
	if a.Kind == l2ActionUSDC {
		return mustHexAddress(c.USDC), 0, erc20TransferCalldata(recipient, sampleTxUSDCAmount)
	}
	return recipient, sampleTxEthValue, nil
}

// l2Quote holds a chain's gas price and per-action L1 data fee (wei); the
// execution fee is gas × price, computed at assembly.
type l2Quote struct {
	GasPriceWei float64
	L1Wei       []float64
}

func fetchL2QuoteFrom(ctx context.Context, url string, c l2ChainConfig, actions []l2Action) (*l2Quote, error) {
	q := &l2Quote{}
	if c.Kind == l2KindArbitrum {
		for _, a := range actions {
			to, _, data := c.actionCall(a)
			gasForL1, baseFee, err := arbL1Component(ctx, url, "0x"+hex.EncodeToString(to), data)
			if err != nil {
				return nil, err
			}
			q.GasPriceWei = baseFee
			q.L1Wei = append(q.L1Wei, gasForL1*baseFee)
		}
		return q, nil
	}
	gasPrice, err := evmGasPrice(ctx, url)
	if err != nil {
		return nil, err
	}
	q.GasPriceWei = gasPrice
	for _, a := range actions {
		l1 := 0.0
		if c.Kind == l2KindOPStack {
			to, value, data := c.actionCall(a)
			if l1, err = opStackL1Fee(ctx, url, unsignedEIP1559(c.ChainID, a.Gas, to, value, data)); err != nil {
				return nil, err
			}
		}
		q.L1Wei = append(q.L1Wei, l1)
	}
	return q, nil
}

// fetchL2Quote tries the chain's RPCs in order and returns the first quote.
func fetchL2Quote(ctx context.Context, c l2ChainConfig, actions []l2Action) (*l2Quote, error) {
	var errs []error
	for _, url := range c.RPCs {
		q, err := fetchL2QuoteFrom(ctx, url, c, actions)
		if err == nil {
			return q, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("%s: no RPC endpoint configured", c.ID)
	}
	return nil, errors.Join(errs...)
}

type L2ActionCost struct {
	Label      string   `json:"label"`
	Gas        uint64   `json:"gas"`
	Source     string   `json:"source"`
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
func l2LadderFrom(quotes map[string]*l2Quote, errs map[string]error, actions []l2Action, ethUSD *float64) L2Ladder {
	l1 := quotes["eth"]
	note := "Total = L2 execution + L1 data fee (GasPriceOracle on OP Stack, NodeInterface on Arbitrum)"
	if len(actions) == 1 {
		note += "; USDC transfer hidden until today's gas calibration succeeds"
	}
	ladder := L2Ladder{Rows: make([]L2LadderRow, 0, len(l2Chains)), Note: note}
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
		for i, a := range actions {
			exec := float64(a.Gas) * q.GasPriceWei
			total := exec + q.L1Wei[i]
			cost := L2ActionCost{
				Label: a.Label, Gas: a.Gas, Source: a.Source,
				ExecETH: exec / 1e18, L1ETH: q.L1Wei[i] / 1e18, TotalETH: total / 1e18,
			}
			if total > 0 {
				cost.L1SharePct = q.L1Wei[i] / total * 100
			}
			if ethUSD != nil {
				usd := total / 1e18 * *ethUSD
				cost.TotalUSD = &usd
			}
			if l1 != nil && c.ID != "eth" {
				if ref := float64(a.Gas)*l1.GasPriceWei + l1.L1Wei[i]; ref > 0 {
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
