package main

import (
	"strings"
	"testing"
)

func TestRegistryInvariants(t *testing.T) {
	wantChains := []string{"eth", "arb", "op", "base", "tron", "btc", "sol"}
	if len(chains) != len(wantChains) {
		t.Fatalf("len(chains) = %d, want %d", len(chains), len(wantChains))
	}
	for _, id := range wantChains {
		info, ok := chains[id]
		if !ok {
			t.Fatalf("missing chain %q", id)
		}
		if info.ID != id {
			t.Errorf("chain %q has ID %q", id, info.ID)
		}
		if info.Label == "" || info.Family == "" || info.TxURL == "" || info.AddressURL == "" {
			t.Errorf("chain %q has empty metadata: %+v", id, info)
		}
	}
	if chains["base"].HasOracle {
		t.Errorf("base HasOracle = true, want false")
	}
	if !chains["eth"].HasOracle || !chains["arb"].HasOracle || !chains["op"].HasOracle {
		t.Errorf("eth/arb/op should have HasOracle = true")
	}
	if chains["btc"].GoPlusChainID != "" {
		t.Errorf("btc GoPlusChainID = %q, want empty", chains["btc"].GoPlusChainID)
	}
	if chains["sol"].GoPlusChainID != "" || chains["sol"].HasOracle {
		t.Errorf("sol metadata mismatch: %+v", chains["sol"])
	}

	// Every non-native-coin (asset, network) pair in payAssets must have tokensFor results.
	for asset, nets := range payAssets {
		for _, net := range nets {
			if !validPair(asset, net) {
				t.Errorf("validPair(%q, %q) = false, want true", asset, net)
			}
			if asset == "ETH" || asset == "TRX" || asset == "BTC" || asset == "SOL" {
				continue
			}
			toks := tokensFor(asset, net)
			if len(toks) == 0 {
				t.Errorf("tokensFor(%q, %q) returned empty", asset, net)
			}
			// Native tier must precede bridged tier.
			seenBridged := false
			for _, tok := range toks {
				if tok.Tier == tierBridged {
					seenBridged = true
				}
				if tok.Tier == tierNative && seenBridged {
					t.Errorf("tokensFor(%q, %q) has native after bridged: %+v", asset, net, toks)
				}
			}
		}
	}
	for _, must := range [][2]string{{"BTC", "btc"}, {"SOL", "sol"}, {"USDT", "sol"}, {"USDC", "sol"}} {
		if !validPair(must[0], must[1]) {
			t.Errorf("validPair(%q, %q) = false, want true", must[0], must[1])
		}
	}
	if validPair("USDC", "tron") {
		t.Errorf("validPair(USDC, tron) = true, want false")
	}
	if validPair("USDT", "btc") {
		t.Errorf("validPair(USDT, btc) = true, want false")
	}

	// Contract addresses must be valid, globally unique (by network+contract and across registry),
	// EVM contracts must be lowercase, and each token network must have at least one native token.
	seenContract := map[string]bool{}
	nativeByNet := map[string]int{}
	for _, tok := range registryTokens {
		info, ok := chains[tok.Network]
		if !ok {
			t.Fatalf("token %+v references unknown network", tok)
		}
		norm, warn, err := parseChainAddress(tok.Network, tok.Contract)
		if err != nil || warn {
			t.Errorf("token %+v invalid contract: norm=%q warn=%v err=%v", tok, norm, warn, err)
		}
		if info.Family == familyEVM && tok.Contract != strings.ToLower(tok.Contract) {
			t.Errorf("EVM token contract %q is not lowercase", tok.Contract)
		}
		key := tok.Network + ":" + tok.Contract
		if seenContract[key] {
			t.Errorf("duplicate contract %s", key)
		}
		seenContract[key] = true
		if tok.Tier == tierNative {
			nativeByNet[tok.Network]++
		}
		got, ok := lookupRegistryToken(tok.Network, tok.Contract)
		if !ok || got.Contract != tok.Contract {
			t.Errorf("lookupRegistryToken(%q, %q) = (%+v, %v)", tok.Network, tok.Contract, got, ok)
		}
		if info.Family == familyEVM {
			if _, ok := lookupRegistryToken(tok.Network, strings.ToUpper(tok.Contract[2:])[:0]+"0x"+strings.ToUpper(tok.Contract[2:])); !ok {
				t.Errorf("lookupRegistryToken should match uppercase EVM contract on %s", tok.Network)
			}
		}
	}
	for _, net := range []string{"eth", "arb", "op", "base", "tron", "sol"} {
		if nativeByNet[net] == 0 {
			t.Errorf("network %q has no native tier token in registryTokens", net)
		}
	}
}
