package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func saveGasEndpoints(t *testing.T) {
	t.Helper()
	urls := []*string{&mempoolFeesURL, &mempoolStatsURL, &mempoolProjectedURL, &mempoolRecentURL,
		&tronEnergyPricesURL, &tronBandwidthPricesURL, &tronTxByIDURL, &btcSpotURL, &spotURLFormat}
	saved := make([]string, len(urls))
	for i, p := range urls {
		saved[i] = *p
	}
	chains := append([]l2ChainConfig(nil), l2Chains...)
	t.Cleanup(func() {
		for i, p := range urls {
			*p = saved[i]
		}
		l2Chains = chains
	})
}

func TestApplyCryptoGasConfig(t *testing.T) {
	saveGasEndpoints(t)
	applyCryptoGasConfig(CryptoGasConfig{
		MempoolBaseURL:  "https://mempool.example/",
		TronGridBaseURL: "https://tron.example",
		CoinbaseBaseURL: "https://cb.example",
		EthRPCURLs:      []string{"https://eth-a.example", "https://eth-b.example"},
	})
	checks := []struct{ got, want string }{
		{mempoolFeesURL, "https://mempool.example/api/v1/fees/precise"},
		{mempoolStatsURL, "https://mempool.example/api/mempool"},
		{mempoolProjectedURL, "https://mempool.example/api/v1/fees/mempool-blocks"},
		{mempoolRecentURL, "https://mempool.example/api/v1/blocks"},
		{tronEnergyPricesURL, "https://tron.example/wallet/getenergyprices"},
		{tronBandwidthPricesURL, "https://tron.example/wallet/getbandwidthprices"},
		{tronTxByIDURL, "https://tron.example/wallet/gettransactionbyid"},
		{btcSpotURL, "https://cb.example/v2/prices/BTC-USD/spot"},
		{spotURLFormat, "https://cb.example/v2/prices/%s-USD/spot"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("endpoint = %q, want %q", c.got, c.want)
		}
	}
	for _, c := range l2Chains {
		switch c.ID {
		case "eth":
			if len(c.RPCs) != 2 || c.RPCs[0] != "https://eth-a.example" {
				t.Errorf("eth rpcs = %v", c.RPCs)
			}
		case "arb":
			if len(c.RPCs) != 1 || c.RPCs[0] != "https://arb1.arbitrum.io/rpc" {
				t.Errorf("arb rpcs = %v, want the default", c.RPCs)
			}
		}
	}
}

func TestFetchL2QuoteFallsBackToNextRPC(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	up := rpcServer(t, map[string]string{"eth_gasPrice": "0x3b9aca00"}, "")
	c := l2ChainConfig{ID: "eth", Kind: l2KindL1, RPCs: []string{down.URL, up.URL}, ChainID: 1, USDC: ethUSDCAddress}
	q, err := fetchL2Quote(context.Background(), c, l2ActionsFor(nil))
	if err != nil || q.GasPriceWei != 1e9 {
		t.Fatalf("got %+v, %v; want the second RPC's quote", q, err)
	}
	c.RPCs = []string{down.URL, down.URL}
	if _, err := fetchL2Quote(context.Background(), c, l2ActionsFor(nil)); err == nil || strings.Count(err.Error(), "502") != 2 {
		t.Errorf("err = %v, want both endpoints' errors", err)
	}
}
