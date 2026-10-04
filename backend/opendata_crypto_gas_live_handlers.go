package main

// GET /api/opendata/crypto/gas-live — the BTC and TRON live bars
// (gas_fee_design.md §9). Polled every 30s by the frontend while a live bar
// is visible. Sources are cached independently (mempool.space 30s, TronGrid
// prices and Coinbase spots 5m); a failure is never cached and only blanks
// its own bar, and a missing spot only drops the USD figures.
// The L1-vs-L2 ladder is cached per chain for 60s (nine public RPC calls per
// refresh), so one rate-limited chain only blanks its own row.

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	gasLiveBtcKey   = "opendata:crypto:gaslive:btc"
	gasLiveTronKey  = "opendata:crypto:gaslive:tron_prices"
	gasLiveSpotKey  = "opendata:crypto:spot:%s"
	gasLiveL2Key    = "opendata:crypto:gaslive:l2:%s"
	gasLiveBtcTTL   = 30 * time.Second
	gasLivePriceTTL = 5 * time.Minute
	gasLiveL2TTL    = time.Minute
)

type GasLiveData struct {
	AsOf      string    `json:"as_of"`
	BTC       *BtcLive  `json:"btc"`
	BTCError  string    `json:"btc_error,omitempty"`
	Tron      *TronLive `json:"tron"`
	TronError string    `json:"tron_error,omitempty"`
	L2        L2Ladder  `json:"l2"`
}

func buildGasLive(now time.Time, btc *btcLiveRaw, btcErr error, tron *tronLiveRaw, tronErr error, btcUSD, trxUSD *float64) GasLiveData {
	d := GasLiveData{AsOf: now.UTC().Format(time.RFC3339)}
	if btcErr != nil {
		d.BTCError = btcErr.Error()
	} else {
		live := btcLiveFrom(*btc, btcUSD)
		d.BTC = &live
	}
	if tronErr != nil {
		d.TronError = tronErr.Error()
	} else {
		live := tronLiveFrom(*tron, trxUSD)
		d.Tron = &live
	}
	return d
}

func (h *APIHandler) gasLiveBtc(r *http.Request) (*btcLiveRaw, error) {
	v, err := h.cachedFetch(gasLiveBtcKey, gasLiveBtcTTL, func() (any, error) {
		ctx, cancel := gasFetchContext(r)
		defer cancel()
		return fetchBtcLiveRaw(ctx)
	})
	if err != nil {
		return nil, err
	}
	return v.(*btcLiveRaw), nil
}

func (h *APIHandler) gasLiveTron(r *http.Request) (*tronLiveRaw, error) {
	v, err := h.cachedFetch(gasLiveTronKey, gasLivePriceTTL, func() (any, error) {
		ctx, cancel := gasFetchContext(r)
		defer cancel()
		return fetchTronLiveRaw(ctx)
	})
	if err != nil {
		return nil, err
	}
	return v.(*tronLiveRaw), nil
}

// gasSpotUSD returns base's USD spot, or nil (logged) when Coinbase fails;
// the bars then show native amounts only.
func (h *APIHandler) gasSpotUSD(r *http.Request, base string) *float64 {
	v, err := h.cachedFetch(fmt.Sprintf(gasLiveSpotKey, base), gasLivePriceTTL, func() (any, error) {
		ctx, cancel := gasFetchContext(r)
		defer cancel()
		return fetchSpot(ctx, base)
	})
	if err != nil {
		slog.Warn("gas live: spot price unavailable", "base", base, "err", err)
		return nil
	}
	price := v.(*CryptoSpotData).PriceUSD
	return &price
}

func (h *APIHandler) gasL2Quotes(r *http.Request) (map[string]*l2Quote, map[string]error) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		quotes = make(map[string]*l2Quote, len(l2Chains))
		errs   = make(map[string]error)
	)
	for _, c := range l2Chains {
		wg.Go(func() {
			v, err := h.cachedFetch(fmt.Sprintf(gasLiveL2Key, c.ID), gasLiveL2TTL, func() (any, error) {
				ctx, cancel := gasFetchContext(r)
				defer cancel()
				return fetchL2Quote(ctx, c)
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[c.ID] = err
				return
			}
			quotes[c.ID] = v.(*l2Quote)
		})
	}
	wg.Wait()
	return quotes, errs
}

func (h *APIHandler) CryptoGasLive(w http.ResponseWriter, r *http.Request) {
	var (
		wg                     sync.WaitGroup
		btc                    *btcLiveRaw
		btcErr                 error
		tron                   *tronLiveRaw
		tronErr                error
		btcUSD, trxUSD, ethUSD *float64
		l2Quotes               map[string]*l2Quote
		l2Errs                 map[string]error
	)
	wg.Go(func() { btc, btcErr = h.gasLiveBtc(r) })
	wg.Go(func() { tron, tronErr = h.gasLiveTron(r) })
	wg.Go(func() { btcUSD = h.gasSpotUSD(r, "BTC") })
	wg.Go(func() { trxUSD = h.gasSpotUSD(r, "TRX") })
	wg.Go(func() { ethUSD = h.gasSpotUSD(r, "ETH") })
	wg.Go(func() { l2Quotes, l2Errs = h.gasL2Quotes(r) })
	wg.Wait()

	data := buildGasLive(time.Now(), btc, btcErr, tron, tronErr, btcUSD, trxUSD)
	data.L2 = l2LadderFrom(l2Quotes, l2Errs, ethUSD)
	writeJSON(w, data)
}
