package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

var (
	errBadTronAddress = errors.New("enter a TRON address (T…, 34 characters)")
	errBadBTCAddress  = errors.New("enter a Bitcoin address (bc1…, 1… or 3…)")
	errBadSolAddress  = errors.New("enter a Solana address (32–44 base58 characters)")
	errUnknownChain   = errors.New("unknown chain")

	tronShapeRe = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)
	btcShapeRe  = regexp.MustCompile(`^(?:[13][1-9A-HJ-NP-Za-km-z]{24,33}|[bB][cC]1[0-9a-zA-Z]{6,87})$`)
	solShapeRe  = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
)

// parseChainAddress validates raw for chain and returns its canonical form:
// EVM lowercase hex (mixed-case with bad EIP-55 checksum still succeeds with
// warn=true, matching parseEthAddress), TRON exact-case base58, BTC lowercase
// bech32/bech32m or exact-case base58, Solana exact-case 32-byte base58.
func parseChainAddress(chain, raw string) (addr string, warn bool, err error) {
	info, ok := chains[chain]
	if !ok {
		return "", false, errUnknownChain
	}
	s := strings.TrimSpace(raw)
	switch info.Family {
	case familyEVM:
		if !ethAddressRe.MatchString(s) {
			return "", false, errBadAddress
		}
		body := s[2:]
		lower := strings.ToLower(body)
		mixed := body != lower && body != strings.ToUpper(body)
		warn := mixed && !eip55Valid(s)
		return "0x" + lower, warn, nil

	case familyTron:
		if len(s) != 34 || s[0] != 'T' {
			return "", false, errBadTronAddress
		}
		ver, payload, err := base58CheckDecode(s)
		if err != nil || ver != 0x41 || len(payload) != 20 {
			return "", false, errBadTronAddress
		}
		return s, false, nil

	case familyBTC:
		if len(s) == 0 {
			return "", false, errBadBTCAddress
		}
		if strings.HasPrefix(strings.ToLower(s), "bc1") {
			hrp, ver, prog, err := bech32Decode(s)
			if err != nil || hrp != "bc" {
				return "", false, errBadBTCAddress
			}
			switch ver {
			case 0:
				if len(prog) != 20 && len(prog) != 32 {
					return "", false, errBadBTCAddress
				}
			case 1:
				if len(prog) != 32 {
					return "", false, errBadBTCAddress
				}
			default:
				return "", false, errBadBTCAddress
			}
			return strings.ToLower(s), false, nil
		}
		if s[0] == '1' || s[0] == '3' {
			ver, payload, err := base58CheckDecode(s)
			if err != nil || len(payload) != 20 {
				return "", false, errBadBTCAddress
			}
			if (s[0] == '1' && ver == 0x00) || (s[0] == '3' && ver == 0x05) {
				return s, false, nil
			}
		}
		return "", false, errBadBTCAddress

	case familySol:
		if len(s) < 32 || len(s) > 44 {
			return "", false, errBadSolAddress
		}
		b, err := base58Decode(s)
		if err != nil || len(b) != 32 {
			return "", false, errBadSolAddress
		}
		return s, false, nil
	}
	return "", false, errUnknownChain
}

// detectFamily inspects the shape and decoded Base58 byte length of raw (no
// checksum verification) for wrong-chain hints.
func detectFamily(raw string) (chainFamily, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if ethAddressRe.MatchString(s) {
		return familyEVM, true
	}
	if strings.HasPrefix(strings.ToLower(s), "bc1") && btcShapeRe.MatchString(s) {
		return familyBTC, true
	}
	if len(s) >= 25 && len(s) <= 44 {
		if decoded, err := base58Decode(s); err == nil {
			switch len(decoded) {
			case 32:
				if len(s) >= 32 {
					return familySol, true
				}
			case 25:
				switch s[0] {
				case 'T':
					return familyTron, true
				case '1', '3':
					return familyBTC, true
				}
			}
		}
	}
	switch {
	case tronShapeRe.MatchString(s):
		return familyTron, true
	case btcShapeRe.MatchString(s):
		return familyBTC, true
	case solShapeRe.MatchString(s):
		return familySol, true
	default:
		return "", false
	}
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Index = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i, c := range base58Alphabet {
		t[c] = int8(i)
	}
	return t
}()

func base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty base58")
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	n := new(big.Int)
	radix := big.NewInt(58)
	digit := new(big.Int)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if int(c) >= len(base58Index) || base58Index[c] < 0 {
			return nil, errors.New("invalid base58 character")
		}
		digit.SetInt64(int64(base58Index[c]))
		n.Mul(n, radix)
		n.Add(n, digit)
	}
	b := n.Bytes()
	raw := make([]byte, zeros+len(b))
	copy(raw[zeros:], b)
	return raw, nil
}

func base58Encode(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var chars []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		chars = append(chars, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		chars = append(chars, '1')
	}
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}
	return string(chars)
}

func base58CheckDecode(s string) (version byte, payload []byte, err error) {
	raw, err := base58Decode(s)
	if err != nil {
		return 0, nil, err
	}
	if len(raw) < 5 {
		return 0, nil, errors.New("base58check too short")
	}
	body, cksum := raw[:len(raw)-4], raw[len(raw)-4:]
	h1 := sha256.Sum256(body)
	h2 := sha256.Sum256(h1[:])
	if !bytes.Equal(cksum, h2[:4]) {
		return 0, nil, errors.New("base58check checksum mismatch")
	}
	return body[0], body[1:], nil
}

func base58CheckEncode(version byte, payload []byte) string {
	body := make([]byte, 1+len(payload)+4)
	body[0] = version
	copy(body[1:], payload)
	h1 := sha256.Sum256(body[:1+len(payload)])
	h2 := sha256.Sum256(h1[:])
	copy(body[1+len(payload):], h2[:4])
	return base58Encode(body)
}

// tronHexToBase58 converts a 20-byte hex address (with optional "0x" or "41"
// prefix) into its 34-char TRON base58check form.
func tronHexToBase58(hex20 string) (string, error) {
	s := strings.TrimSpace(hex20)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) == 42 && strings.HasPrefix(s, "41") {
		s = s[2:]
	}
	if len(s) != 40 {
		return "", errors.New("invalid tron hex length")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return "", err
	}
	return base58CheckEncode(0x41, b), nil
}

// tronBase58ToHex converts a TRON base58check address into a 20-byte lowercase
// hex string (without the "41" version byte).
func tronBase58ToHex(addr string) (string, error) {
	s := strings.TrimSpace(addr)
	if len(s) != 34 || s[0] != 'T' {
		return "", errBadTronAddress
	}
	ver, payload, err := base58CheckDecode(s)
	if err != nil || ver != 0x41 || len(payload) != 20 {
		return "", errBadTronAddress
	}
	return hex.EncodeToString(payload), nil
}

const (
	bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	bech32Const   = 1
	bech32mConst  = 0x2bc830a3
)

var bech32Index = func() [128]int8 {
	var t [128]int8
	for i := range t {
		t[i] = -1
	}
	for i, c := range bech32Charset {
		t[c] = int8(i)
	}
	return t
}()

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

// bech32Decode decodes a SegWit Bech32 (BIP-173, witness v0) or Bech32m
// (BIP-350, witness v1–v16) address.
func bech32Decode(s string) (hrp string, version int, program []byte, err error) {
	if len(s) < 8 || len(s) > 90 {
		return "", 0, nil, errors.New("invalid bech32 length")
	}
	hasLower, hasUpper := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 33 || c > 126 {
			return "", 0, nil, errors.New("bech32 char out of range")
		}
		if c >= 'a' && c <= 'z' {
			hasLower = true
		} else if c >= 'A' && c <= 'Z' {
			hasUpper = true
		}
	}
	if hasLower && hasUpper {
		return "", 0, nil, errors.New("mixed case bech32")
	}
	lower := strings.ToLower(s)
	pos := strings.LastIndexByte(lower, '1')
	if pos < 1 || pos+7 > len(lower) {
		return "", 0, nil, errors.New("invalid bech32 separator")
	}
	hrp = lower[:pos]
	if hrp != "bc" && hrp != "tb" {
		return "", 0, nil, errors.New("unsupported segwit hrp")
	}
	dataPart := lower[pos+1:]
	data := make([]byte, len(dataPart))
	for i := 0; i < len(dataPart); i++ {
		c := dataPart[i]
		if int(c) >= len(bech32Index) || bech32Index[c] < 0 {
			return "", 0, nil, errors.New("invalid bech32 data char")
		}
		data[i] = byte(bech32Index[c])
	}
	polyIn := make([]byte, 0, len(hrp)*2+1+len(data))
	for i := 0; i < len(hrp); i++ {
		polyIn = append(polyIn, hrp[i]>>5)
	}
	polyIn = append(polyIn, 0)
	for i := 0; i < len(hrp); i++ {
		polyIn = append(polyIn, hrp[i]&31)
	}
	polyIn = append(polyIn, data...)
	pm := bech32Polymod(polyIn)

	version = int(data[0])
	if version > 16 {
		return "", 0, nil, errors.New("invalid witness version")
	}
	if version == 0 && pm != bech32Const {
		return "", 0, nil, errors.New("invalid bech32 checksum for v0")
	}
	if version >= 1 && pm != bech32mConst {
		return "", 0, nil, errors.New("invalid bech32m checksum for v1+")
	}

	payload5 := data[1 : len(data)-6]
	var acc uint32
	var bits uint
	program = make([]byte, 0, len(payload5)*5/8)
	for _, v := range payload5 {
		acc = (acc << 5) | uint32(v)
		bits += 5
		for bits >= 8 {
			bits -= 8
			program = append(program, byte((acc>>bits)&0xff))
		}
	}
	if bits >= 5 || ((acc<<(8-bits))&0xff) != 0 {
		return "", 0, nil, errors.New("invalid segwit padding")
	}
	if len(program) < 2 || len(program) > 40 {
		return "", 0, nil, errors.New("invalid witness program length")
	}
	if version == 0 && len(program) != 20 && len(program) != 32 {
		return "", 0, nil, errors.New("invalid v0 witness program length")
	}
	return hrp, version, program, nil
}
