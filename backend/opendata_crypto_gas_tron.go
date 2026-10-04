package main

// TRON energy price history from TronGrid's keyless getenergyprices: the
// chain's governance-set burn price per unit of energy, which feeds TRON's
// all-time record card (gas_fee_design.md §4.1). Callers cache it for 24h.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	tronEnergyPricesURL = "https://api.trongrid.io/wallet/getenergyprices"
	tronHTTPClient      = &http.Client{Timeout: 5 * time.Second}
)

const tronFetchTimeout = 4 * time.Second

// tronPricePoint is one governance price change; a zero At is the genesis default.
type tronPricePoint struct {
	At  time.Time
	Sun int64
}

// parseTronEnergyPrices parses "ms:sun,ms:sun,..." (ms 0 = genesis).
func parseTronEnergyPrices(s string) ([]tronPricePoint, error) {
	if s == "" {
		return nil, fmt.Errorf("tron energy prices: empty history")
	}
	var pts []tronPricePoint
	for _, pair := range strings.Split(s, ",") {
		msStr, sunStr, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("tron energy prices: bad entry %q", pair)
		}
		ms, err := strconv.ParseInt(msStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tron energy prices: bad time in %q: %w", pair, err)
		}
		sun, err := strconv.ParseInt(sunStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tron energy prices: bad price in %q: %w", pair, err)
		}
		p := tronPricePoint{Sun: sun}
		if ms != 0 {
			p.At = time.UnixMilli(ms).UTC()
		}
		pts = append(pts, p)
	}
	return pts, nil
}

func fetchTronEnergyPrices(ctx context.Context) ([]tronPricePoint, error) {
	ctx, cancel := context.WithTimeout(ctx, tronFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tronEnergyPricesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("tron energy prices request: %w", err)
	}
	resp, err := tronHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tron energy prices fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tron energy prices fetch: upstream status %d", resp.StatusCode)
	}
	var body struct {
		Prices string `json:"prices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("tron energy prices decode: %w", err)
	}
	return parseTronEnergyPrices(body.Prices)
}

func tronTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// tronAllTime reports the first time each extreme was set and the latest price.
func tronAllTime(points []tronPricePoint) GasAllTime {
	ath, atl, cur := points[0], points[0], points[len(points)-1]
	for _, p := range points[1:] {
		if p.Sun > ath.Sun {
			ath = p
		}
		if p.Sun < atl.Sun {
			atl = p
		}
	}
	current := float64(cur.Sun)
	return GasAllTime{
		Unit:         "sun",
		AthValue:     float64(ath.Sun),
		AthTime:      tronTime(ath.At),
		AtlValue:     float64(atl.Sun),
		AtlTime:      tronTime(atl.At),
		CurrentValue: &current,
		CurrentTime:  tronTime(cur.At),
	}
}
