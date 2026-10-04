package main

// Builds the sample transactions the L1-vs-L2 ladder prices (gas_fee_design.md
// §11): a minimal RLP encoder, EIP-1559 unsigned transactions and ERC-20
// transfer calldata, replacing the hex blobs Phase 3 shipped. Only the three
// RLP shapes we need are supported, so there is no runtime type switch.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// Placeholder fields of the sample transactions. They only affect the encoded
// length (and the L1 fee is currently at Fjord's minimum size anyway), so they
// are fixed rather than measured.
const (
	sampleTxNonce          = 42
	sampleTxMaxPriorityFee = 1_000_000
	sampleTxMaxFee         = 50_000_000
	sampleTxEthValue       = 1_000_000_000_000_000 // 0.001 ETH
	sampleTxUSDCAmount     = 1_000_000             // 1 USDC (6 decimals)
)

func uintBytes(x uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], x)
	return bytes.TrimLeft(buf[:], "\x00")
}

func rlpHeader(offset byte, n int) []byte {
	if n < 56 {
		return []byte{offset + byte(n)}
	}
	lenBytes := uintBytes(uint64(n))
	return append([]byte{offset + 55 + byte(len(lenBytes))}, lenBytes...)
}

func rlpBytes(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return []byte{b[0]}
	}
	return append(rlpHeader(0x80, len(b)), b...)
}

func rlpUint(x uint64) []byte { return rlpBytes(uintBytes(x)) }

func rlpList(items ...[]byte) []byte {
	payload := bytes.Join(items, nil)
	return append(rlpHeader(0xc0, len(payload)), payload...)
}

// unsignedEIP1559 returns 0x02 || rlp([chainId, nonce, maxPriorityFee, maxFee,
// gas, to, value, data, accessList]).
func unsignedEIP1559(chainID, gas uint64, to []byte, value uint64, data []byte) []byte {
	return append([]byte{0x02}, rlpList(
		rlpUint(chainID), rlpUint(sampleTxNonce), rlpUint(sampleTxMaxPriorityFee), rlpUint(sampleTxMaxFee),
		rlpUint(gas), rlpBytes(to), rlpUint(value), rlpBytes(data), rlpList(),
	)...)
}

// erc20TransferCalldata encodes transfer(recipient, amount).
func erc20TransferCalldata(recipient []byte, amount uint64) []byte {
	out := make([]byte, 4+32+32)
	copy(out, []byte{0xa9, 0x05, 0x9c, 0xbb})
	copy(out[4+12:], recipient)
	binary.BigEndian.PutUint64(out[4+32+24:], amount)
	return out
}

// mustHexAddress parses a package-level constant address; a typo there is a
// programming error and must stop the process at startup.
func mustHexAddress(s string) []byte {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 20 {
		panic(fmt.Sprintf("bad constant address %q", s))
	}
	return b
}

// opStackSampleTxs returns the ETH-transfer and USDC-transfer sample txs for
// an OP Stack chain as hex (transitional until Task 3 builds them per action).
func opStackSampleTxs(chainID uint64, usdc string) []string {
	recipient := mustHexAddress(l2Recipient)
	return []string{
		hex.EncodeToString(unsignedEIP1559(chainID, 21000, recipient, sampleTxEthValue, nil)),
		hex.EncodeToString(unsignedEIP1559(chainID, 65000, mustHexAddress(usdc), 0,
			erc20TransferCalldata(recipient, sampleTxUSDCAmount))),
	}
}
