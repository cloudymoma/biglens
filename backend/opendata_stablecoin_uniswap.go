package main

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"
)

// Uniswap v3 USDC/USDT 0.01% pool (stablecoin.md §4.3, §5.2): the pool price
// feeds the relative peg (USDT per USDC) next to the exchange quotes. Curve
// and Chainlink are v1.1.

const (
	stableUniswapPool   = "0x3416cf6c708da44db2624d63ea0aaef7113527c6"
	stableUniswapToken0 = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48" // USDC, 6 decimals
	stableUniswapToken1 = "0xdac17f958d2ee523a2206206994597c13d831ec7" // USDT, 6 decimals
	stableUniswapFee    = 100                                          // hundredths of a bip: 0.01%
	stableUniswapDec0   = 6
	stableUniswapDec1   = 6
	stableUniswapPair   = "USDC-USDT-100"

	selUniswapToken0 = "0x0dfe1681" // token0()
	selUniswapToken1 = "0xd21220a7" // token1()
	selUniswapFee    = "0xddca3f43" // fee()
	selUniswapSlot0  = "0x3850c7bd" // slot0()
)

// stableUniswapPrice converts slot0's sqrtPriceX96 to token1 per token0:
// P = (sqrtPriceX96 / 2^96)^2 × 10^(dec0 − dec1).
func stableUniswapPrice(sqrtX96 *big.Int, dec0, dec1 int) float64 {
	f := new(big.Float).SetPrec(256).SetInt(sqrtX96)
	f.Mul(f, f)
	f.Quo(f, new(big.Float).SetPrec(256).SetInt(new(big.Int).Lsh(big.NewInt(1), 192)))
	if d := dec0 - dec1; d != 0 {
		scale := new(big.Float).SetPrec(256).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(stableAbsInt(d))), nil))
		if d > 0 {
			f.Mul(f, scale)
		} else {
			f.Quo(f, scale)
		}
	}
	v, _ := f.Float64()
	return v
}

func stableAbsInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// parseUniswapSlot0 decodes slot0()'s first two words: sqrtPriceX96
// (uint160) and tick (int24, sign-extended).
func parseUniswapSlot0(res string) (*big.Int, int64, error) {
	digits, ok := strings.CutPrefix(strings.TrimSpace(res), "0x")
	if !ok || len(digits) < 7*64 {
		return nil, 0, errors.New("slot0: short result")
	}
	sqrt, ok := new(big.Int).SetString(digits[:64], 16)
	if !ok || sqrt.Sign() <= 0 || sqrt.BitLen() > 160 {
		return nil, 0, errors.New("slot0: bad sqrtPriceX96")
	}
	t, ok := new(big.Int).SetString(digits[64:128], 16)
	if !ok {
		return nil, 0, errors.New("slot0: bad tick")
	}
	if t.Bit(255) == 1 { // two's complement
		t.Sub(t, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	if !t.IsInt64() || t.Int64() < -887272 || t.Int64() > 887272 {
		return nil, 0, errors.New("slot0: tick out of range")
	}
	return sqrt, t.Int64(), nil
}

// stableUniswapReading is one slot0 read of the pool.
type stableUniswapReading struct {
	Price        float64 // USDT per USDC
	SqrtPriceX96 string  // decimal
	Tick         int64
	ReadAt       time.Time
	Code         string // "" ok, else an RPC error code
}

func stableUniswapCall(ctx context.Context, rpcs []string, data string) (string, string) {
	return evmRPCFailover(ctx, rpcs, func(actx context.Context, u string) (string, string) {
		res, err := evmRPC(actx, u, "eth_call", map[string]string{"to": stableUniswapPool, "data": data}, "latest")
		if err != nil {
			return "", evmRPCErrCode(err)
		}
		return res, ""
	})
}

// readStableUniswap reads slot0 (one eth_call) over the Ethereum RPCs.
func readStableUniswap(ctx context.Context, rpcs func(string) []string, at time.Time) stableUniswapReading {
	ctx, cancel := context.WithTimeout(ctx, stableTimeoutSmall)
	defer cancel()
	r := stableUniswapReading{ReadAt: at}
	res, code := stableUniswapCall(ctx, rpcs("eth"), selUniswapSlot0)
	if code != "" {
		r.Code = code
		return r
	}
	sqrt, tick, err := parseUniswapSlot0(res)
	if err != nil {
		r.Code = "bad_response"
		return r
	}
	r.Price, r.SqrtPriceX96, r.Tick = stableUniswapPrice(sqrt, stableUniswapDec0, stableUniswapDec1), sqrt.String(), tick
	return r
}

// checkStableUniswapPool confirms the pool is still USDC/USDT at the 0.01%
// fee tier (token0, token1, fee); "" when it is.
func checkStableUniswapPool(ctx context.Context, rpcs func(string) []string) string {
	ctx, cancel := context.WithTimeout(ctx, stableTimeoutSmall)
	defer cancel()
	want := map[string]*big.Int{
		selUniswapToken0: hexAddrInt(stableUniswapToken0),
		selUniswapToken1: hexAddrInt(stableUniswapToken1),
		selUniswapFee:    big.NewInt(stableUniswapFee),
	}
	for _, sel := range []string{selUniswapToken0, selUniswapToken1, selUniswapFee} {
		res, code := stableUniswapCall(ctx, rpcs("eth"), sel)
		if code != "" {
			return code
		}
		got, ok := parseHexBigInt(res)
		if !ok || got.Cmp(want[sel]) != 0 {
			return "bad_response"
		}
	}
	return ""
}

func hexAddrInt(addr string) *big.Int {
	v, _ := new(big.Int).SetString(strings.TrimPrefix(addr, "0x"), 16)
	return v
}

// quote presents the pool price as a venue quote of the relative peg: bid =
// ask = the pool price, timestamped at the read.
func (r stableUniswapReading) quote() stableVenueQuote {
	q := stableVenueQuote{Venue: "uniswap", Pair: stableUniswapPair, Base: "USDC", Quote: "USDT", TS: r.ReadAt, FetchedAt: r.ReadAt, Code: r.Code}
	if r.Code == "" {
		q.Bid, q.Ask = r.Price, r.Price
	}
	return q
}
