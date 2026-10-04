package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/sha3"
)

// A wrong selector makes eth_call revert and only shows up in production, so
// derive each one from its Solidity signature.
func TestEvmSelectors(t *testing.T) {
	for sig, want := range map[string]string{
		"getL1Fee(bytes)": selGetL1Fee,
		"gasEstimateL1Component(address,bool,bytes)": selGasEstimateL1Component,
	} {
		h := sha3.NewLegacyKeccak256()
		h.Write([]byte(sig))
		if got := hex.EncodeToString(h.Sum(nil)[:4]); got != want {
			t.Errorf("selector(%s) = %s, constant is %s", sig, got, want)
		}
	}
}

func TestEncodeGetL1Fee(t *testing.T) {
	got := encodeGetL1Fee([]byte{0x02, 0xaa})
	want := "0x49948e0e" +
		"0000000000000000000000000000000000000000000000000000000000000020" + // offset
		"0000000000000000000000000000000000000000000000000000000000000002" + // length
		"02aa" + strings.Repeat("0", 60) // data, right-padded to 32 bytes
	if got != want {
		t.Errorf("encodeGetL1Fee:\n got %s\nwant %s", got, want)
	}
}

func TestEncodeGasEstimateL1Component(t *testing.T) {
	got, err := encodeGasEstimateL1Component("0xAf88d065e77c8cC2239327C5EDb3A432268e5831", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "0x77d488a2" +
		strings.Repeat("0", 24) + "af88d065e77c8cc2239327c5edb3a432268e5831" + // address
		strings.Repeat("0", 64) + // contractCreation = false
		"0000000000000000000000000000000000000000000000000000000000000060" + // offset of data
		strings.Repeat("0", 64) // data length 0
	if got != want {
		t.Errorf("encodeGasEstimateL1Component:\n got %s\nwant %s", got, want)
	}
	if _, err := encodeGasEstimateL1Component("0x1234", nil); err == nil {
		t.Error("a short address must be rejected")
	}
}

func TestHexToFloat(t *testing.T) {
	if v, err := hexToFloat("0xf4b63"); err != nil || v != 1002339 {
		t.Errorf("hexToFloat(0xf4b63) = %v, %v; want 1002339", v, err)
	}
	for _, bad := range []string{"", "0x", "f4b63", "0xzz"} {
		if _, err := hexToFloat(bad); err == nil {
			t.Errorf("hexToFloat(%q): expected an error", bad)
		}
	}
}

// rpcServer answers each JSON-RPC method with a canned result (or error).
func rpcServer(t *testing.T, results map[string]string, rpcErr string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "biglens/1.0" {
			t.Errorf("User-Agent = %q, want biglens/1.0 (mainnet.base.org rejects default agents)", ua)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		json.Unmarshal(body, &req)
		if req.Params == nil {
			t.Errorf("%s sent null params; JSON-RPC needs an array", req.Method)
		}
		if rpcErr != "" {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"` + rpcErr + `"}}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + results[req.Method] + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEvmGasPriceAndL1Fee(t *testing.T) {
	srv := rpcServer(t, map[string]string{"eth_gasPrice": "0xf4b63", "eth_call": "0x3ba8f408"}, "")
	if v, err := evmGasPrice(context.Background(), srv.URL); err != nil || v != 1002339 {
		t.Errorf("gas price = %v, %v", v, err)
	}
	if v, err := opStackL1Fee(context.Background(), srv.URL, []byte{0x02}); err != nil || v != 1000928264 {
		t.Errorf("l1 fee = %v, %v", v, err)
	}
}

func TestArbL1Component(t *testing.T) {
	word := func(n string) string { return strings.Repeat("0", 64-len(n)) + n }
	srv := rpcServer(t, map[string]string{"eth_call": "0x" + word("c8") + word("1314470") + word("183eca")}, "")
	gas, base, err := arbL1Component(context.Background(), srv.URL, "0xd8da6bf26964af9d7eed9e03e53415d37aa96045", nil)
	if err != nil || gas != 200 || base != 20006000 {
		t.Errorf("got gas=%v base=%v err=%v; want 200, 20006000", gas, base, err)
	}
}

func TestFetchL2QuoteRPCError(t *testing.T) {
	srv := rpcServer(t, nil, "rate limited")
	if _, err := evmGasPrice(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the JSON-RPC error message", err)
	}
}

func TestEvmRPCHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := evmGasPrice(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want an error naming status 403", err)
	}
}
