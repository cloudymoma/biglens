package main

// HTTP handlers for the Crypto Pulse Open Data dashboard:
//
//	GET /api/opendata/crypto/pulse?days=7|30|90|365
//	GET /api/opendata/crypto/fees?days=7|30|90|365     (Task 4)
//	GET /api/opendata/crypto/whales?days=7|30|90&chain=btc|eth (Task 6)
//	GET /api/opendata/crypto/tokens?days=7|30          (Task 8)
//
// Each endpoint is fetched lazily by its tab, cached 10 minutes, and guarded
// by singleflight so concurrent tab opens run one BigQuery round each.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	cryptoDefaultDays = 90
	// cryptoAddressMaxDays caps the heavy distinct-address scans; the 365-day
	// pulse view simply omits the addresses series.
	cryptoAddressMaxDays = 90
)

var cryptoPulseDaysAllowed = []int{7, 30, 90, 365}

var cryptoFlight singleflight.Group

func parseCryptoDays(r *http.Request, def int, allowed []int) (int, error) {
	raw := r.URL.Query().Get("days")
	if raw == "" {
		return def, nil
	}
	d, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid days %q: expected an integer", raw)
	}
	for _, a := range allowed {
		if d == a {
			return d, nil
		}
	}
	return 0, fmt.Errorf("days must be one of %v", allowed)
}

// cryptoWindow returns the half-open [start, end) day window ending at
// today UTC, so every returned day is complete.
func cryptoWindow(days int) (start, end civil.Date) {
	end = civil.DateOf(time.Now().UTC())
	return end.AddDays(-days), end
}

// --- /pulse ---

type CryptoKpi struct {
	Date         string  `json:"date"`
	TxCount      int64   `json:"tx_count"`
	ValueSettled float64 `json:"value_settled"`
	FeesTotal    float64 `json:"fees_total"`
	Blocks       int64   `json:"blocks"`
	FullnessPct  float64 `json:"fullness_pct"`
}

type CryptoChainPulse struct {
	Daily     []CryptoActivityRow `json:"daily"`
	Addresses []CryptoAddressRow  `json:"addresses"`
	Blocks    []CryptoBlockRow    `json:"blocks"`
	Kpi       CryptoKpi           `json:"kpi"`
}

type CryptoPulseData struct {
	Days int              `json:"days"`
	BTC  CryptoChainPulse `json:"btc"`
	ETH  CryptoChainPulse `json:"eth"`
}

// rollupCryptoKpi derives the KPI strip from the latest complete day,
// joining that day's block stats by date.
func rollupCryptoKpi(daily []CryptoActivityRow, blocks []CryptoBlockRow) CryptoKpi {
	if len(daily) == 0 {
		return CryptoKpi{}
	}
	last := daily[len(daily)-1]
	kpi := CryptoKpi{
		Date:         last.Date,
		TxCount:      last.TxCount,
		ValueSettled: last.ValueSettled,
		FeesTotal:    last.FeesTotal,
	}
	for _, b := range blocks {
		if b.Date == last.Date {
			kpi.Blocks = b.Blocks
			kpi.FullnessPct = b.FullnessPct
		}
	}
	return kpi
}

// fetchChainPulse launches the per-chain queries on g, writing into dst.
// The addresses series is skipped beyond cryptoAddressMaxDays (cost cap).
func (h *APIHandler) fetchChainPulse(g *errgroup.Group, ctx context.Context, chain string, start, end civil.Date, days int, dst *CryptoChainPulse) {
	g.Go(func() error {
		rows, err := h.bq.GetCryptoActivity(ctx, chain, start, end)
		if err != nil {
			return fmt.Errorf("%s activity: %w", chain, err)
		}
		if rows != nil {
			dst.Daily = rows
		}
		return nil
	})
	g.Go(func() error {
		rows, err := h.bq.GetCryptoBlockStats(ctx, chain, start, end)
		if err != nil {
			return fmt.Errorf("%s blocks: %w", chain, err)
		}
		if rows != nil {
			dst.Blocks = rows
		}
		return nil
	})
	if days <= cryptoAddressMaxDays {
		g.Go(func() error {
			rows, err := h.bq.GetCryptoActiveAddresses(ctx, chain, start, end)
			if err != nil {
				return fmt.Errorf("%s addresses: %w", chain, err)
			}
			if rows != nil {
				dst.Addresses = rows
			}
			return nil
		})
	}
}

func (h *APIHandler) CryptoPulse(w http.ResponseWriter, r *http.Request) {
	days, err := parseCryptoDays(r, cryptoDefaultDays, cryptoPulseDaysAllowed)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("opendata:crypto:pulse:%d", days)
	if cached, ok := h.cache.Get(key); ok {
		writeJSON(w, cached)
		return
	}

	v, err, _ := cryptoFlight.Do(key, func() (any, error) {
		start, end := cryptoWindow(days)
		empty := func() CryptoChainPulse {
			return CryptoChainPulse{
				Daily:     []CryptoActivityRow{},
				Addresses: []CryptoAddressRow{},
				Blocks:    []CryptoBlockRow{},
			}
		}
		data := CryptoPulseData{Days: days, BTC: empty(), ETH: empty()}

		g, ctx := errgroup.WithContext(r.Context())
		h.fetchChainPulse(g, ctx, "btc", start, end, days, &data.BTC)
		h.fetchChainPulse(g, ctx, "eth", start, end, days, &data.ETH)
		if err := g.Wait(); err != nil {
			return nil, err
		}

		data.BTC.Kpi = rollupCryptoKpi(data.BTC.Daily, data.BTC.Blocks)
		data.ETH.Kpi = rollupCryptoKpi(data.ETH.Daily, data.ETH.Blocks)
		h.cache.Set(key, &data)
		return &data, nil
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}
