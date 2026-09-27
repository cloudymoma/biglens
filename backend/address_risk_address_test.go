package main

import "testing"

func TestParseEthAddress(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		want     string
		wantWarn bool
		wantErr  bool
	}{
		// EIP-55 reference vectors (eips.ethereum.org/EIPS/eip-55)
		{"eip55 vector 1", "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", false, false},
		{"eip55 vector 2", "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359", "0xfb6916095ca1df60bb79ce92ce3ea74c37c5d359", false, false},
		{"eip55 vector 3", "0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb", "0xd1220a0cf47c7b9be7a2e6ba89f429762e7b9adb", false, false},
		{"ronin exploiter checksummed", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", false, false},
		{"all lowercase skips checksum", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", false, false},
		{"all uppercase hex skips checksum", "0x098B716B8AAF21512996DC57EB0615E2383E2F96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", false, false},
		{"surrounding whitespace trimmed", "  0x098b716b8aaf21512996dc57eb0615e2383e2f96\n", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", false, false},
		{"one flipped case is a warning, not an error", "0x098b716B8Aaf21512996dC57EB0615e2383E2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96", true, false},
		{"too short", "0x098b716b8aaf21512996dc57eb0615e2383e2f9", "", false, true},
		{"non-hex char", "0x098b716b8aaf21512996dc57eb0615e2383e2fzz", "", false, true},
		{"missing 0x", "098b716b8aaf21512996dc57eb0615e2383e2f96", "", false, true},
		{"ens name unsupported", "vitalik.eth", "", false, true},
		{"empty", "", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn, err := parseEthAddress(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || warn != tt.wantWarn {
				t.Errorf("got (%q, %v), want (%q, %v)", got, warn, tt.want, tt.wantWarn)
			}
		})
	}
}
