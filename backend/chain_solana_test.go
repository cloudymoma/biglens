package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseSolanaAddress(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{
			name: "system program 32 leading zero bytes",
			in:   "11111111111111111111111111111111",
			want: "11111111111111111111111111111111",
		},
		{
			name: "43-char mainnet holder (Circle frozen wallet)",
			in:   "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN",
			want: "depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN",
		},
		{
			name: "44-char official USDT mint with surrounding whitespace",
			in:   "  Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB\n",
			want: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB",
		},
		{
			name:    "25-byte TRON address rejected on Solana",
			in:      "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
			wantErr: "enter a Solana address (32–44 base58 characters)",
		},
		{
			name:    "25-byte BTC P2PKH rejected on Solana",
			in:      "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			wantErr: "enter a Solana address (32–44 base58 characters)",
		},
		{
			name:    "invalid base58 character 0 rejected",
			in:      "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNY0",
			wantErr: "enter a Solana address (32–44 base58 characters)",
		},
		{
			name:    "too short rejected",
			in:      "1111111111111111111111111111111",
			wantErr: "enter a Solana address (32–44 base58 characters)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn, err := parseChainAddress("sol", tt.in)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parseChainAddress(sol, %q) err = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChainAddress(sol, %q) unexpected err: %v", tt.in, err)
			}
			if got != tt.want || warn {
				t.Errorf("parseChainAddress(sol, %q) = (%q, %v), want (%q, false)", tt.in, got, warn, tt.want)
			}
		})
	}
}

func TestDetectFamilySolanaVsBTCTron(t *testing.T) {
	// Construct 32-byte keys whose Base58 representations start with '1', '3', and 'T'
	// (including a 34-char 'T...' key) to verify byte-length disambiguation (S7).
	solStartsWith1 := "11111111111111111111111111111111"
	solStartsWithT := "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	var solStartsWith3, sol34StartsWithT string
	for b := 0; b < 256; b++ {
		var k [32]byte
		k[0] = byte(b)
		k[31] = 1
		enc := base58Encode(k[:])
		if solStartsWith3 == "" && strings.HasPrefix(enc, "3") {
			solStartsWith3 = enc
		}
		var k34 [32]byte
		// 7 leading zero bytes ('1's) won't start with T; instead test non-zero prefix with 32 bytes.
		k34[7] = byte(b)
		k34[31] = 1
		enc34 := base58Encode(k34[:])
		if sol34StartsWithT == "" && strings.HasPrefix(enc34, "T") {
			sol34StartsWithT = enc34
		}
	}
	if solStartsWith3 == "" {
		t.Fatalf("failed to generate 32-byte Solana key starting with '3'")
	}

	cases := []struct {
		in   string
		want chainFamily
	}{
		{solStartsWith1, familySol},
		{solStartsWith3, familySol},
		{solStartsWithT, familySol},
		{"depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN", familySol},
		{"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", familyTron},
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", familyBTC},
		{"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy", familyBTC},
		{"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", familyBTC},
	}
	for _, tc := range cases {
		fam, ok := detectFamily(tc.in)
		if !ok || fam != tc.want {
			t.Errorf("detectFamily(%q) = (%q, %v), want (%q, true)", tc.in, fam, ok, tc.want)
		}
	}
}

func TestDeriveSolanaATAAndMetadataPDA(t *testing.T) {
	// Ground-truth Circle frozen Solana USDC account verified live on mainnet (2026-10-09):
	// owner = depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN
	// mint  = EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v (USDC)
	// ATA   = ERbwSojYctddRjySVYiBw9TXgqAWw2nTeA2zQR9oB95i
	ata, err := deriveSolanaATA(
		"depMwrdSqn5y9fDkdotP4iGxTdxSaEHVE6QjnbcEmjN",
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
	)
	if err != nil {
		t.Fatalf("deriveSolanaATA unexpected err: %v", err)
	}
	if want := "ERbwSojYctddRjySVYiBw9TXgqAWw2nTeA2zQR9oB95i"; ata != want {
		t.Fatalf("deriveSolanaATA = %q, want %q", ata, want)
	}

	// Verify Metaplex Metadata PDA derivation yields valid 32-byte off-curve Solana addresses.
	for _, mint := range []string{
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB",
	} {
		pda, err := deriveSolanaMetadataPDA(mint)
		if err != nil {
			t.Fatalf("deriveSolanaMetadataPDA(%q) err: %v", mint, err)
		}
		raw, err := base58Decode(pda)
		if err != nil || len(raw) != 32 {
			t.Fatalf("deriveSolanaMetadataPDA(%q) = %q, decode err=%v len=%d", mint, pda, err, len(raw))
		}
		var b32 [32]byte
		copy(b32[:], raw)
		if isEd25519OnCurve(b32) {
			t.Fatalf("PDA %q for mint %q unexpectedly lies on Ed25519 curve", pda, mint)
		}
	}

	// Build a realistic Metaplex Metadata v1 Borsh payload with NUL (\x00) padding (S8).
	var buf []byte
	buf = append(buf, 4)                   // Key::MetadataV1
	buf = append(buf, make([]byte, 32)...) // update_authority
	buf = append(buf, make([]byte, 32)...) // mint
	paddedName := "USD Coin" + strings.Repeat("\x00", 24)
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(paddedName)))
	buf = append(buf, u32[:]...)
	buf = append(buf, []byte(paddedName)...)
	paddedSym := "USDC" + strings.Repeat("\x00", 6)
	binary.LittleEndian.PutUint32(u32[:], uint32(len(paddedSym)))
	buf = append(buf, u32[:]...)
	buf = append(buf, []byte(paddedSym)...)

	if got := parseMetaplexSymbol(buf); got != "USDC" {
		t.Errorf("parseMetaplexSymbol = %q, want %q", got, "USDC")
	}
	if got := parseMetaplexSymbol(buf[:10]); got != "" {
		t.Errorf("parseMetaplexSymbol(truncated) = %q, want empty", got)
	}
}
