package main

// Applies conf.yaml's crypto_gas section to the Gas Pulse fetchers at startup
// (gas_fee_design.md §11). Runs once before the server listens, so the
// package-level endpoint variables are never written concurrently.

import "strings"

const defaultMempoolBaseURL = "https://mempool.space"

var mempoolBaseURL = defaultMempoolBaseURL

func applyCryptoGasConfig(c CryptoGasConfig) {
	c = c.withDefaults()
	mempool := strings.TrimRight(c.MempoolBaseURL, "/")
	mempoolBaseURL = mempool
	mempoolFeesURL = mempool + "/api/v1/fees/precise"
	mempoolStatsURL = mempool + "/api/mempool"
	mempoolProjectedURL = mempool + "/api/v1/fees/mempool-blocks"
	mempoolRecentURL = mempool + "/api/v1/blocks"

	tron := strings.TrimRight(c.TronGridBaseURL, "/")
	tronGridBaseURL = tron
	tronEnergyPricesURL = tron + "/wallet/getenergyprices"
	tronBandwidthPricesURL = tron + "/wallet/getbandwidthprices"
	tronTxByIDURL = tron + "/wallet/gettransactionbyid"

	coinbase := strings.TrimRight(c.CoinbaseBaseURL, "/")
	btcSpotURL = coinbase + "/v2/prices/BTC-USD/spot"
	spotURLFormat = coinbase + "/v2/prices/%s-USD/spot"

	rpcs := map[string][]string{"eth": c.EthRPCURLs, "arb": c.ArbitrumRPCURLs, "op": c.OptimismRPCURLs, "base": c.BaseRPCURLs}
	for i := range l2Chains {
		l2Chains[i].RPCs = rpcs[l2Chains[i].ID]
	}
}
