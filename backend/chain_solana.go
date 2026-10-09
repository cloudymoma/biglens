package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"
)

const (
	solanaTokenProgramID           = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	solanaToken2022ProgramID       = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	solanaAssociatedTokenProgramID = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
	solanaMetadataProgramID        = "metaqbxxUerdq28cj1RbAWkYQm3ybzjb6a8bt518x1s"
)

var (
	// ed25519P = 2^255 - 19
	ed25519P = func() *big.Int {
		p := new(big.Int).Lsh(big.NewInt(1), 255)
		return p.Sub(p, big.NewInt(19))
	}()
	// ed25519D = -121665 / 121666 mod p
	ed25519D = func() *big.Int {
		inv := new(big.Int).ModInverse(big.NewInt(121666), ed25519P)
		d := new(big.Int).Mul(big.NewInt(-121665), inv)
		return d.Mod(d, ed25519P)
	}()
	ed25519HalfP = new(big.Int).Rsh(new(big.Int).Sub(ed25519P, big.NewInt(1)), 1)
)

// isEd25519OnCurve returns true iff key is the canonical little-endian encoding
// of a valid point on the Ed25519 curve (-x^2 + y^2 = 1 + d*x^2*y^2 mod 2^255-19).
func isEd25519OnCurve(key [32]byte) bool {
	var yLE [32]byte
	copy(yLE[:], key[:])
	sign := (yLE[31] >> 7) & 1
	yLE[31] &= 0x7f

	// Convert little-endian y to big-endian for math/big.
	var yBE [32]byte
	for i := 0; i < 32; i++ {
		yBE[31-i] = yLE[i]
	}
	y := new(big.Int).SetBytes(yBE[:])
	if y.Cmp(ed25519P) >= 0 {
		return false
	}

	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, ed25519P)

	// u = y^2 - 1 mod p
	u := new(big.Int).Sub(y2, big.NewInt(1))
	u.Mod(u, ed25519P)
	if u.Sign() == 0 {
		// x == 0; canonical encoding requires sign == 0.
		return sign == 0
	}

	// v = d * y^2 + 1 mod p
	v := new(big.Int).Mul(ed25519D, y2)
	v.Add(v, big.NewInt(1))
	v.Mod(v, ed25519P)
	vInv := new(big.Int).ModInverse(v, ed25519P)
	if vInv == nil {
		return false
	}

	x2 := new(big.Int).Mul(u, vInv)
	x2.Mod(x2, ed25519P)

	// x^2 is a non-zero square mod p iff (x^2)^((p-1)/2) == 1 mod p.
	legendre := new(big.Int).Exp(x2, ed25519HalfP, ed25519P)
	return legendre.Cmp(big.NewInt(1)) == 0
}

func decodeSolanaPubkeyBytes(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := base58Decode(strings.TrimSpace(s))
	if err != nil || len(raw) != 32 {
		return out, errBadSolAddress
	}
	copy(out[:], raw)
	return out, nil
}

// findSolanaProgramAddress derives the canonical off-curve Program Derived Address
// for seeds and programID using SHA-256(seeds..., [bump], programID, "ProgramDerivedAddress").
func findSolanaProgramAddress(seeds [][]byte, programID string) (string, error) {
	prog, err := decodeSolanaPubkeyBytes(programID)
	if err != nil {
		return "", err
	}
	for bump := 255; bump >= 0; bump-- {
		h := sha256.New()
		for _, seed := range seeds {
			h.Write(seed)
		}
		h.Write([]byte{byte(bump)})
		h.Write(prog[:])
		h.Write([]byte("ProgramDerivedAddress"))
		var sum [32]byte
		h.Sum(sum[:0])
		if !isEd25519OnCurve(sum) {
			return base58Encode(sum[:]), nil
		}
	}
	return "", errors.New("failed to derive Solana PDA")
}

// deriveSolanaATA derives the canonical Associated Token Account address for an
// owner wallet and SPL Token mint (classic Tokenkeg program).
func deriveSolanaATA(owner, mint string) (string, error) {
	ownerKey, err := decodeSolanaPubkeyBytes(owner)
	if err != nil {
		return "", err
	}
	tokenProgKey, err := decodeSolanaPubkeyBytes(solanaTokenProgramID)
	if err != nil {
		return "", err
	}
	mintKey, err := decodeSolanaPubkeyBytes(mint)
	if err != nil {
		return "", err
	}
	return findSolanaProgramAddress(
		[][]byte{ownerKey[:], tokenProgKey[:], mintKey[:]},
		solanaAssociatedTokenProgramID,
	)
}

// deriveSolanaMetadataPDA derives the Metaplex Token Metadata account address
// PDA(["metadata", MetadataProgram, mint], MetadataProgram).
func deriveSolanaMetadataPDA(mint string) (string, error) {
	metaProgKey, err := decodeSolanaPubkeyBytes(solanaMetadataProgramID)
	if err != nil {
		return "", err
	}
	mintKey, err := decodeSolanaPubkeyBytes(mint)
	if err != nil {
		return "", err
	}
	return findSolanaProgramAddress(
		[][]byte{[]byte("metadata"), metaProgKey[:], mintKey[:]},
		solanaMetadataProgramID,
	)
}

// parseMetaplexSymbol decodes the Borsh `symbol` string from a Metaplex Token
// Metadata v1 account buffer and strips trailing NUL (\x00) padding (S8).
func parseMetaplexSymbol(raw []byte) string {
	const headerLen = 1 + 32 + 32 // key (1) + update_authority (32) + mint (32)
	if len(raw) < headerLen+4 {
		return ""
	}
	pos := headerLen
	nameLen := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += 4
	if nameLen < 0 || nameLen > 512 || len(raw) < pos+nameLen+4 {
		return ""
	}
	pos += nameLen
	symLen := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += 4
	if symLen < 0 || symLen > 128 || len(raw) < pos+symLen {
		return ""
	}
	sym := strings.TrimRight(string(raw[pos:pos+symLen]), "\x00")
	return strings.TrimSpace(sym)
}
