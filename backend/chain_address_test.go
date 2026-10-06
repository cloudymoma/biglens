package main

import "testing"

func TestParseChainAddress(t *testing.T) {
	tests := []struct {
		name     string
		chain    string
		in       string
		want     string
		wantWarn bool
		wantErr  string
	}{
		// EVM chains (eth, arb, op, base)
		{
			name:  "eth valid checksummed",
			chain: "eth",
			in:    "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
			want:  "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed",
		},
		{
			name:     "eth mixed case bad eip55 warns",
			chain:    "eth",
			in:       "0x5aaeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
			want:     "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed",
			wantWarn: true,
		},
		{
			name:  "arb valid lowercase",
			chain: "arb",
			in:    "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9",
			want:  "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9",
		},
		{
			name:  "arb valid checksummed",
			chain: "arb",
			in:    "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9",
			want:  "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9",
		},
		{
			name:  "op valid checksummed",
			chain: "op",
			in:    "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85",
			want:  "0x0b2c639c533813f4aa9d7837caf62653d097ff85",
		},
		{
			name:  "op valid lowercase",
			chain: "op",
			in:    "0x94b008aa00579c1307b0ef2c499ad98a8ce58e58",
			want:  "0x94b008aa00579c1307b0ef2c499ad98a8ce58e58",
		},
		{
			name:  "base valid checksummed",
			chain: "base",
			in:    "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
			want:  "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
		},
		{
			name:  "base valid lowercase",
			chain: "base",
			in:    "0xfde4c96c8593536e31f229ea8f37b2ada2699bb2",
			want:  "0xfde4c96c8593536e31f229ea8f37b2ada2699bb2",
		},
		{
			name:    "evm wrong length",
			chain:   "eth",
			in:      "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beae",
			wantErr: "enter a 0x address (42 characters)",
		},
		{
			name:    "evm bad charset",
			chain:   "arb",
			in:      "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaez",
			wantErr: "enter a 0x address (42 characters)",
		},

		// TRON
		{
			name:  "tron valid USDT contract",
			chain: "tron",
			in:    "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
			want:  "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		},
		{
			name:  "tron valid second address with whitespace",
			chain: "tron",
			in:    "  TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7\n",
			want:  "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7",
		},
		{
			name:    "tron wrong length",
			chain:   "tron",
			in:      "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6",
			wantErr: "enter a TRON address (T…, 34 characters)",
		},
		{
			name:    "tron bad base58 char",
			chain:   "tron",
			in:      "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj0O",
			wantErr: "enter a TRON address (T…, 34 characters)",
		},
		{
			name:    "tron bad checksum",
			chain:   "tron",
			in:      "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6s",
			wantErr: "enter a TRON address (T…, 34 characters)",
		},

		// Bitcoin (BIP-173 / BIP-350 official test vectors + Base58Check P2PKH/P2SH)
		{
			name:  "btc BIP-173 v0 P2WPKH uppercase normalized to lowercase",
			chain: "btc",
			in:    "BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4",
			want:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
		},
		{
			name:  "btc BIP-350 v1 P2TR Taproot bech32m",
			chain: "btc",
			in:    "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			want:  "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
		},
		{
			name:  "btc base58 P2PKH genesis address",
			chain: "btc",
			in:    "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			want:  "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		},
		{
			name:  "btc base58 P2SH address",
			chain: "btc",
			in:    "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			want:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
		},
		{
			name:    "btc BIP-350 v1 Taproot with bech32 checksum rejected",
			chain:   "btc",
			in:      "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq5zuyut",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc BIP-350 v0 with bech32m checksum rejected",
			chain:   "btc",
			in:      "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kemeawh",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc BIP-173 bad checksum",
			chain:   "btc",
			in:      "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t5",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc mixed case bech32 rejected",
			chain:   "btc",
			in:      "bc1Qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc base58 bad checksum",
			chain:   "btc",
			in:      "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNb",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc wrong length",
			chain:   "btc",
			in:      "bc1qshort",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
		{
			name:    "btc bad charset",
			chain:   "btc",
			in:      "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf0O",
			wantErr: "enter a Bitcoin address (bc1…, 1… or 3…)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn, err := parseChainAddress(tt.chain, tt.in)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want || warn != tt.wantWarn {
				t.Errorf("got (%q, %v), want (%q, %v)", got, warn, tt.want, tt.wantWarn)
			}
		})
	}
}

func TestBech32OfficialVectors(t *testing.T) {
	valid := []struct {
		addr    string
		wantHRP string
		wantVer int
		wantLen int
	}{
		{"BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4", "bc", 0, 20},
		{"tb1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3q0sl5k7", "tb", 0, 32},
		{"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", "bc", 1, 32},
		{"tb1pqqqqp399et2xygdj5xreqhjjvcmzhxw4aywxecjdzew6hylgvsesf3hn0c", "tb", 1, 32},
		{"bc1pw508d6qejxtdg4y5r3zarvary0c5xw7kw508d6qejxtdg4y5r3zarvary0c5xw7kt5nd6y", "bc", 1, 40},
		{"BC1SW50QGDZ25J", "bc", 16, 2},
		{"bc1zw508d6qejxtdg4y5r3zarvaryvaxxpcs", "bc", 2, 16},
	}
	for _, v := range valid {
		hrp, ver, prog, err := bech32Decode(v.addr)
		if err != nil {
			t.Errorf("bech32Decode(%q) unexpected err: %v", v.addr, err)
			continue
		}
		if hrp != v.wantHRP || ver != v.wantVer || len(prog) != v.wantLen {
			t.Errorf("bech32Decode(%q) = (%q, %d, len=%d), want (%q, %d, len=%d)",
				v.addr, hrp, ver, len(prog), v.wantHRP, v.wantVer, v.wantLen)
		}
	}

	invalid := []string{
		"tc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq5zuyut",
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq5zuyut", // bech32 checksum for v1
		"bc1zw508d6qejxtdg4y5r3zarvaryvg6kdaj",                           // BIP-173 v2 with bech32 checksum (invalid under BIP-350)
		"tb1z0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqglt7rf", // bech32 checksum for v2
		"BC1S0XLXVLHEMJA6C4DQV22UAPCTQUPFHLXM9H8Z3K2E72Q4K9HCZ7VQ54WELL", // bech32 checksum for v16
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kemeawh",                     // bech32m checksum for v0
		"tb1q0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq24jc47", // bech32m checksum for v0
		"bc1p38j9r5y49hruaue7wxjce0updqjuyyx0kh56v8s25huc6995vvpql3jow4", // invalid char
		"BC130XLXVLHEMJA6C4DQV22UAPCTQUPFHLXM9H8Z3K2E72Q4K9HCZ7VQ7ZWS8R", // invalid witness version 17
		"bc1pw5dgrnzv", // program < 2 bytes
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7v8n0nx0muaewav253zgeav", // program > 40 bytes
		"BC1QR508D6QEJXTDG4Y5R3ZARVARYV98GJ9P",                                         // v0 program len 16 != 20,32
		"tb1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3q0sL5k7",               // mixed case
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7v07qwwzcrf",             // zero padding > 4 bits
		"tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vpggkg4j",               // non-zero padding
		"bc1gmk9yu", // empty data
	}
	for _, inv := range invalid {
		if _, _, _, err := bech32Decode(inv); err == nil {
			t.Errorf("bech32Decode(%q) succeeded, want error", inv)
		}
	}
}

func TestTronHexBase58RoundTrip(t *testing.T) {
	const (
		wantHex    = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
		wantBase58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	)
	gotBase58, err := tronHexToBase58(wantHex)
	if err != nil {
		t.Fatalf("tronHexToBase58(%q) err: %v", wantHex, err)
	}
	if gotBase58 != wantBase58 {
		t.Fatalf("tronHexToBase58(%q) = %q, want %q", wantHex, gotBase58, wantBase58)
	}

	// Also accept "41"-prefixed or "0x"-prefixed hex forms.
	for _, in := range []string{"0x" + wantHex, "41" + wantHex} {
		b58, err := tronHexToBase58(in)
		if err != nil || b58 != wantBase58 {
			t.Errorf("tronHexToBase58(%q) = (%q, %v), want (%q, nil)", in, b58, err, wantBase58)
		}
	}

	gotHex, err := tronBase58ToHex(wantBase58)
	if err != nil {
		t.Fatalf("tronBase58ToHex(%q) err: %v", wantBase58, err)
	}
	if gotHex != wantHex {
		t.Fatalf("tronBase58ToHex(%q) = %q, want %q", wantBase58, gotHex, wantHex)
	}
}

func TestDetectFamily(t *testing.T) {
	tests := []struct {
		in      string
		wantFam chainFamily
		wantOK  bool
	}{
		{"0xdac17f958d2ee523a2206206994597c13d831ec7", familyEVM, true},
		{"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", familyTron, true},
		{"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", familyBTC, true},
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", familyBTC, true},
		{"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy", familyBTC, true},
		{"not-an-address", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		fam, ok := detectFamily(tt.in)
		if fam != tt.wantFam || ok != tt.wantOK {
			t.Errorf("detectFamily(%q) = (%q, %v), want (%q, %v)", tt.in, fam, ok, tt.wantFam, tt.wantOK)
		}
	}
}
