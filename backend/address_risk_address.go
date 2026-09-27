package main

import (
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/sha3"
)

var ethAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

var errBadAddress = errors.New("enter a 0x address (42 characters)")

// parseEthAddress trims and validates raw input and returns the lowercase
// form used for lookups and cache keys. A mixed-case address whose EIP-55
// checksum does not match is still accepted, with checksumWarning set.
func parseEthAddress(raw string) (string, bool, error) {
	s := strings.TrimSpace(raw)
	if !ethAddressRe.MatchString(s) {
		return "", false, errBadAddress
	}
	body := s[2:]
	lower := strings.ToLower(body)
	mixed := body != lower && body != strings.ToUpper(body)
	warn := mixed && !eip55Valid(s)
	return "0x" + lower, warn, nil
}

// eip55Valid reports whether addr's letter casing matches its EIP-55 checksum:
// hex letter i must be uppercase iff nibble i of keccak256(lowercase hex) >= 8.
func eip55Valid(addr string) bool {
	body := addr[2:]
	lower := strings.ToLower(body)
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(lower))
	sum := hex.EncodeToString(h.Sum(nil))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c >= '0' && c <= '9' {
			continue
		}
		upper := sum[i] >= '8'
		if upper != (c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
