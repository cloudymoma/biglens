package main

import (
	"slices"
	"sort"
	"strings"
)

type chainFamily string

const (
	familyEVM  chainFamily = "evm"
	familyTron chainFamily = "tron"
	familyBTC  chainFamily = "btc"
	familySol  chainFamily = "sol"
)

type chainInfo struct {
	ID            string // eth | arb | op | base | tron | btc | sol
	Label         string // "Ethereum", "Arbitrum One", "Optimism", "Base", "TRON", "Bitcoin", "Solana"
	Family        chainFamily
	GoPlusChainID string // "1","42161","10","8453","tron"; "" = not covered by GoPlus
	HasOracle     bool   // Chainalysis sanctions oracle deployed (eth/arb/op; base=false, verified 2026-10-05)
	TxURL         string // fmt template, e.g. "https://arbiscan.io/tx/%s"
	AddressURL    string // fmt template
}

var chains = map[string]chainInfo{
	"eth": {
		ID:            "eth",
		Label:         "Ethereum",
		Family:        familyEVM,
		GoPlusChainID: "1",
		HasOracle:     true,
		TxURL:         "https://etherscan.io/tx/%s",
		AddressURL:    "https://etherscan.io/address/%s",
	},
	"arb": {
		ID:            "arb",
		Label:         "Arbitrum One",
		Family:        familyEVM,
		GoPlusChainID: "42161",
		HasOracle:     true,
		TxURL:         "https://arbiscan.io/tx/%s",
		AddressURL:    "https://arbiscan.io/address/%s",
	},
	"op": {
		ID:            "op",
		Label:         "Optimism",
		Family:        familyEVM,
		GoPlusChainID: "10",
		HasOracle:     true,
		TxURL:         "https://optimistic.etherscan.io/tx/%s",
		AddressURL:    "https://optimistic.etherscan.io/address/%s",
	},
	"base": {
		ID:            "base",
		Label:         "Base",
		Family:        familyEVM,
		GoPlusChainID: "8453",
		HasOracle:     false,
		TxURL:         "https://basescan.org/tx/%s",
		AddressURL:    "https://basescan.org/address/%s",
	},
	"tron": {
		ID:            "tron",
		Label:         "TRON",
		Family:        familyTron,
		GoPlusChainID: "tron",
		HasOracle:     false,
		TxURL:         "https://tronscan.org/#/transaction/%s",
		AddressURL:    "https://tronscan.org/#/address/%s",
	},
	"btc": {
		ID:            "btc",
		Label:         "Bitcoin",
		Family:        familyBTC,
		GoPlusChainID: "",
		HasOracle:     false,
		TxURL:         "https://mempool.space/tx/%s",
		AddressURL:    "https://mempool.space/address/%s",
	},
	"sol": {
		ID:            "sol",
		Label:         "Solana",
		Family:        familySol,
		GoPlusChainID: "",
		HasOracle:     false,
		TxURL:         "https://solscan.io/tx/%s",
		AddressURL:    "https://solscan.io/account/%s",
	},
}

type tokenTier string

const (
	tierNative      tokenTier = "native"
	tierBridged     tokenTier = "bridged"
	tierCounterfeit tokenTier = "counterfeit"
	tierOther       tokenTier = "other"
)

type registryToken struct {
	Asset     string // USDT | USDC
	Network   string // chain ID
	Contract  string // EVM lowercase hex; TRON/Solana base58
	Decimals  int
	Tier      tokenTier // native | bridged
	Label     string    // "USD₮0", "USDC.e", "Bridged USDT" …
	FreezeSel string    // "e47d6060" isBlackListed | "fe575a87" isBlacklisted | "fbac3951" isBlocked | "spl-account-state" | ""
}

// registryTokens is the canonical, code-only token registry (verified on-chain
// via symbol(), totalSupply(), and blacklist selector calls on 2026-10-05 /
// Solana mint & ATA state verified on 2026-10-09).
// Never expose this table in conf.yaml.
var registryTokens = []registryToken{
	// USDT
	{Asset: "USDT", Network: "eth", Contract: "0xdac17f958d2ee523a2206206994597c13d831ec7", Decimals: 6, Tier: tierNative, Label: "USDT", FreezeSel: "e47d6060"},
	{Asset: "USDT", Network: "tron", Contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Decimals: 6, Tier: tierNative, Label: "USDT", FreezeSel: "e47d6060"},
	{Asset: "USDT", Network: "arb", Contract: "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9", Decimals: 6, Tier: tierNative, Label: "USD₮0", FreezeSel: "fbac3951"},
	{Asset: "USDT", Network: "op", Contract: "0x01bff41798a0bcf287b996046ca68b395dbc1071", Decimals: 6, Tier: tierNative, Label: "USD₮0", FreezeSel: "fbac3951"},
	{Asset: "USDT", Network: "op", Contract: "0x94b008aa00579c1307b0ef2c499ad98a8ce58e58", Decimals: 6, Tier: tierBridged, Label: "Bridged USDT", FreezeSel: ""},
	{Asset: "USDT", Network: "base", Contract: "0xfde4c96c8593536e31f229ea8f37b2ada2699bb2", Decimals: 6, Tier: tierBridged, Label: "Bridged USDT", FreezeSel: ""},
	{Asset: "USDT", Network: "sol", Contract: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", Decimals: 6, Tier: tierNative, Label: "USDT", FreezeSel: "spl-account-state"},

	// USDC
	{Asset: "USDC", Network: "eth", Contract: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Decimals: 6, Tier: tierNative, Label: "USDC", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "arb", Contract: "0xaf88d065e77c8cc2239327c5edb3a432268e5831", Decimals: 6, Tier: tierNative, Label: "USDC", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "arb", Contract: "0xff970a61a04b1ca14834a43f5de4533ebddb5cc8", Decimals: 6, Tier: tierBridged, Label: "USDC.e", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "op", Contract: "0x0b2c639c533813f4aa9d7837caf62653d097ff85", Decimals: 6, Tier: tierNative, Label: "USDC", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "op", Contract: "0x7f5c764cbc14f9669b88837ca1490cca17c31607", Decimals: 6, Tier: tierBridged, Label: "USDC.e", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "base", Contract: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Decimals: 6, Tier: tierNative, Label: "USDC", FreezeSel: "fe575a87"},
	{Asset: "USDC", Network: "sol", Contract: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", Decimals: 6, Tier: tierNative, Label: "USDC", FreezeSel: "spl-account-state"},
}

var payAssets = map[string][]string{
	"USDT": {"tron", "eth", "arb", "op", "base", "sol"},
	"USDC": {"eth", "arb", "op", "base", "sol"},
	"ETH":  {"eth", "arb", "op", "base"},
	"TRX":  {"tron"},
	"BTC":  {"btc"},
	"SOL":  {"sol"},
}

func tokensFor(asset, network string) []registryToken {
	var out []registryToken
	for _, t := range registryTokens {
		if t.Asset == asset && t.Network == network {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tier == out[j].Tier {
			return false
		}
		return out[i].Tier == tierNative
	})
	return out
}

func lookupRegistryToken(network, contract string) (registryToken, bool) {
	c := strings.TrimSpace(contract)
	info, ok := chains[network]
	if !ok {
		return registryToken{}, false
	}
	if info.Family == familyEVM {
		c = strings.ToLower(c)
	}
	for _, t := range registryTokens {
		if t.Network == network && t.Contract == c {
			return t, true
		}
	}
	return registryToken{}, false
}

func validPair(asset, network string) bool {
	return slices.Contains(payAssets[asset], network)
}
