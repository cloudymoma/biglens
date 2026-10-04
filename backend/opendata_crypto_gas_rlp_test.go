package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// RLP rules from the Ethereum yellow paper, appendix B.
func TestRLP(t *testing.T) {
	long := strings.Repeat("a", 56)
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"single low byte is itself", rlpBytes([]byte{0x7f}), "7f"},
		{"single high byte gets a prefix", rlpBytes([]byte{0x80}), "8180"},
		{"empty string", rlpBytes(nil), "80"},
		{"zero is the empty string", rlpUint(0), "80"},
		{"1024", rlpUint(1024), "820400"},
		{"56-byte string uses a length-of-length", rlpBytes([]byte(long)), "b838" + hex.EncodeToString([]byte(long))},
		{"empty list", rlpList(), "c0"},
		{"nested", rlpList(rlpUint(1), rlpList()), "c201c0"},
	}
	for _, tt := range tests {
		if got := hex.EncodeToString(tt.got); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

// Golden values: the Phase 3 constants, produced by an independent encoder
// and accepted by the live GasPriceOracle. The rewrite must reproduce them
// byte for byte, or the L1 fee would silently change.
func TestUnsignedEIP1559Golden(t *testing.T) {
	recipient := mustHexAddress(l2Recipient)
	usdcCall := erc20TransferCalldata(recipient, sampleTxUSDCAmount)
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"op eth", unsignedEIP1559(10, 21000, recipient, sampleTxEthValue, nil),
			"02ed0a2a830f42408402faf08082520894d8da6bf26964af9d7eed9e03e53415d37aa9604587038d7ea4c6800080c0"},
		{"op usdc", unsignedEIP1559(10, 65000, mustHexAddress("0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"), 0, usdcCall),
			"02f86b0a2a830f42408402faf08082fde8940b2c639c533813f4aa9d7837caf62653d097ff8580b844" +
				"a9059cbb000000000000000000000000d8da6bf26964af9d7eed9e03e53415d37aa96045" +
				"00000000000000000000000000000000000000000000000000000000000f4240c0"},
		{"base eth", unsignedEIP1559(8453, 21000, recipient, sampleTxEthValue, nil),
			"02ef8221052a830f42408402faf08082520894d8da6bf26964af9d7eed9e03e53415d37aa9604587038d7ea4c6800080c0"},
		{"base usdc", unsignedEIP1559(8453, 65000, mustHexAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"), 0, usdcCall),
			"02f86d8221052a830f42408402faf08082fde894833589fcd6edb6e08f4c7c32d4f71b54bda0291380b844" +
				"a9059cbb000000000000000000000000d8da6bf26964af9d7eed9e03e53415d37aa96045" +
				"00000000000000000000000000000000000000000000000000000000000f4240c0"},
	}
	for _, tt := range tests {
		if got := hex.EncodeToString(tt.got); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestErc20TransferCalldata(t *testing.T) {
	got := hex.EncodeToString(erc20TransferCalldata(mustHexAddress(l2Recipient), 1_000_000))
	want := "a9059cbb000000000000000000000000d8da6bf26964af9d7eed9e03e53415d37aa96045" +
		"00000000000000000000000000000000000000000000000000000000000f4240"
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestMustHexAddressPanicsOnBadInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a malformed constant address must fail loudly at startup")
		}
	}()
	mustHexAddress("0x1234")
}
