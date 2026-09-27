package main

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/sha3"
)

// The topic0 constants must be keccak256 of the event signatures; a typo
// would make the sync silently return zero rows forever.
func TestStablecoinTopicsMatchSignatures(t *testing.T) {
	for sig, topic := range map[string]string{
		"AddedBlackList(address)":              topicUSDTAddedBlackList,
		"RemovedBlackList(address)":            topicUSDTRemovedBlackList,
		"DestroyedBlackFunds(address,uint256)": topicUSDTDestroyedBlackFunds,
		"Blacklisted(address)":                 topicUSDCBlacklisted,
		"UnBlacklisted(address)":               topicUSDCUnBlacklisted,
	} {
		h := sha3.NewLegacyKeccak256()
		h.Write([]byte(sig))
		if got := "0x" + hex.EncodeToString(h.Sum(nil)); got != topic {
			t.Errorf("%s: keccak = %s, constant = %s", sig, got, topic)
		}
	}
}

func TestStablecoinSQLShape(t *testing.T) {
	for _, want := range []string{
		"block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)", // half-open, parameterized
		"'" + usdtContract + "', '" + usdcContract + "'",
		topicUSDTAddedBlackList, topicUSDTRemovedBlackList, topicUSDTDestroyedBlackFunds, topicUSDCBlacklisted, topicUSDCUnBlacklisted,
		"SUBSTR(data, 67, 64)",
		"bigquery-public-data.crypto_ethereum.logs",
	} {
		if !strings.Contains(stablecoinSQL, want) {
			t.Errorf("stablecoinSQL missing %q", want)
		}
	}
	if strings.Contains(stablecoinSQL, "ORDER BY") {
		t.Error("ORDER BY adds cost and nothing else")
	}
	if !strings.Contains(blockCheckSQL, "TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)") {
		t.Error("block check must bound the partition scan to 2 hours")
	}
}

func TestDecodeStablecoinRow(t *testing.T) {
	addr := "0x098b716b8aaf21512996dc57eb0615e2383e2f96"
	// Real DestroyedBlackFunds amount 0x17e7bb229cb ≈ 1.64M USDT (spec §5.1).
	amountHex := strings.Repeat("0", 64-11) + "17e7bb229cb"
	tests := []struct {
		name       string
		row        stablecoinRow
		wantAmount string
		wantErr    bool
	}{
		{"usdt freeze", stablecoinRow{Token: "USDT", Action: "freeze", Address: addr}, "", false},
		{"usdc unfreeze", stablecoinRow{Token: "USDC", Action: "unfreeze", Address: addr}, "", false},
		{"usdt destroy amount", stablecoinRow{Token: "USDT", Action: "destroy", Address: addr, AmountHex: amountHex}, "1642752780747", false},
		{"destroy without amount", stablecoinRow{Token: "USDT", Action: "destroy", Address: addr}, "", true},
		{"uppercase address rejected", stablecoinRow{Token: "USDT", Action: "freeze", Address: strings.ToUpper(addr)}, "", true},
		{"short address", stablecoinRow{Token: "USDT", Action: "freeze", Address: "0x1234"}, "", true},
		{"unknown token", stablecoinRow{Token: "DAI", Action: "freeze", Address: addr}, "", true},
		{"unknown action", stablecoinRow{Token: "USDT", Action: "pause", Address: addr}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := decodeStablecoinRow(tt.row)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if e.Amount != tt.wantAmount {
				t.Errorf("amount = %q, want %q", e.Amount, tt.wantAmount)
			}
		})
	}
}

func TestFormatTokenAmount(t *testing.T) {
	n, _ := new(big.Int).SetString("1640000459211", 10)
	if got := formatTokenAmount(n, 6); got != "1640000.46" {
		t.Errorf("USDT = %q", got)
	}
	if got := formatTokenAmount(big.NewInt(0), 6); got != "0.00" {
		t.Errorf("zero = %q", got)
	}
}
