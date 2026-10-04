package main

// Minimal Ethereum JSON-RPC client for the L1-vs-L2 cost ladder
// (gas_fee_design.md §10): eth_gasPrice plus two eth_call reads — OP Stack's
// GasPriceOracle.getL1Fee(bytes) and Arbitrum's
// NodeInterface.gasEstimateL1Component(address,bool,bytes). ABI encoding is
// hand-written for exactly these two signatures; tests derive the selectors
// from keccak so a typo cannot reach production.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

var evmHTTPClient = &http.Client{Timeout: 5 * time.Second}

const (
	evmRPCTimeout = 4 * time.Second
	// mainnet.base.org rejects default HTTP client user agents.
	evmUserAgent = "biglens/1.0"

	opGasPriceOracle          = "0x420000000000000000000000000000000000000F"
	arbNodeInterface          = "0x00000000000000000000000000000000000000C8"
	selGetL1Fee               = "49948e0e" // getL1Fee(bytes)
	selGasEstimateL1Component = "77d488a2" // gasEstimateL1Component(address,bool,bytes)
)

// evmRPC sends one JSON-RPC request and returns its hex result.
func evmRPC(ctx context.Context, url, method string, params ...any) (string, error) {
	if params == nil {
		params = []any{}
	}
	ctx, cancel := context.WithTimeout(ctx, evmRPCTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return "", fmt.Errorf("evm rpc %s encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("evm rpc %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", evmUserAgent)
	resp, err := evmHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("evm rpc %s %s: %w", url, method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("evm rpc %s %s: upstream status %d", url, method, resp.StatusCode)
	}
	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("evm rpc %s %s decode: %w", url, method, err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("evm rpc %s %s: %d %s", url, method, out.Error.Code, out.Error.Message)
	}
	if !strings.HasPrefix(out.Result, "0x") {
		return "", fmt.Errorf("evm rpc %s %s: unexpected result %q", url, method, out.Result)
	}
	return out.Result, nil
}

func hexToFloat(h string) (float64, error) {
	digits, ok := strings.CutPrefix(h, "0x")
	if !ok || digits == "" {
		return 0, fmt.Errorf("bad hex quantity %q", h)
	}
	n, ok := new(big.Int).SetString(digits, 16)
	if !ok {
		return 0, fmt.Errorf("bad hex quantity %q", h)
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	return f, nil
}

func abiWord(n int) string { return fmt.Sprintf("%064x", n) }

// abiBytes encodes a dynamic bytes value: length word + right-padded data.
func abiBytes(b []byte) string {
	padded := make([]byte, (len(b)+31)/32*32)
	copy(padded, b)
	return abiWord(len(b)) + hex.EncodeToString(padded)
}

func encodeGetL1Fee(unsignedTx []byte) string {
	return "0x" + selGetL1Fee + abiWord(32) + abiBytes(unsignedTx)
}

func encodeGasEstimateL1Component(to string, data []byte) (string, error) {
	addr := strings.ToLower(strings.TrimPrefix(to, "0x"))
	if len(addr) != 40 {
		return "", fmt.Errorf("bad address %q", to)
	}
	// (address to, bool contractCreation = false, bytes data at offset 3 words)
	return "0x" + selGasEstimateL1Component + strings.Repeat("0", 24) + addr + abiWord(0) + abiWord(96) + abiBytes(data), nil
}

func evmGasPrice(ctx context.Context, url string) (float64, error) {
	r, err := evmRPC(ctx, url, "eth_gasPrice")
	if err != nil {
		return 0, err
	}
	return hexToFloat(r)
}

// opStackL1Fee returns the L1 data fee (wei) an OP Stack chain would charge
// for unsignedTx.
func opStackL1Fee(ctx context.Context, url string, unsignedTx []byte) (float64, error) {
	call := map[string]string{"to": opGasPriceOracle, "data": encodeGetL1Fee(unsignedTx)}
	r, err := evmRPC(ctx, url, "eth_call", call, "latest")
	if err != nil {
		return 0, err
	}
	return hexToFloat(r)
}

// arbL1Component returns the extra L2 gas Arbitrum charges for posting the
// transaction to L1, and the L2 base fee (wei) it is priced at.
func arbL1Component(ctx context.Context, url, to string, data []byte) (gasForL1, baseFeeWei float64, err error) {
	input, err := encodeGasEstimateL1Component(to, data)
	if err != nil {
		return 0, 0, err
	}
	call := map[string]string{"to": arbNodeInterface, "data": input}
	r, err := evmRPC(ctx, url, "eth_call", call, "latest")
	if err != nil {
		return 0, 0, err
	}
	words := strings.TrimPrefix(r, "0x")
	if len(words) < 128 {
		return 0, 0, fmt.Errorf("gasEstimateL1Component: short result %q", r)
	}
	if gasForL1, err = hexToFloat("0x" + words[:64]); err != nil {
		return 0, 0, err
	}
	if baseFeeWei, err = hexToFloat("0x" + words[64:128]); err != nil {
		return 0, 0, err
	}
	return gasForL1, baseFeeWei, nil
}
