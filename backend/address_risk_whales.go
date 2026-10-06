package main

// Local risk badges for Whales & Flow (spec D17, §11.4). ETH addresses in the
// whales response are tagged with the local lists they are on (OFAC, MEW,
// currently frozen USDT/USDC). The whales result is cached and shared, so
// tagging always works on a copy whose slices are cloned too: writing into
// the cached backing arrays would be a data race and would leak badges into
// every later response.

import (
	"context"
	"log/slog"
	"slices"
	"strings"
)

// tagWhales returns d unchanged for BTC, when there is no store, or when the
// pool cannot be read: Whales must work exactly as before without the risk DB.
func (s *addressRiskService) tagWhales(ctx context.Context, d *CryptoWhalesData) *CryptoWhalesData {
	if s == nil || s.store == nil || d.Chain != "eth" {
		return d
	}
	seen := make(map[string]struct{}, len(d.Largest)*2+len(d.TopReceivers))
	addrs := make([]string, 0, len(d.Largest)*2+len(d.TopReceivers))
	addAddr := func(raw string) {
		if raw == "" {
			return
		}
		low := strings.ToLower(raw)
		if _, ok := seen[low]; !ok {
			seen[low] = struct{}{}
			addrs = append(addrs, low)
		}
	}
	for _, tx := range d.Largest {
		addAddr(tx.From)
		addAddr(tx.To)
	}
	for _, r := range d.TopReceivers {
		addAddr(r.Address)
	}
	if len(addrs) == 0 {
		return d
	}
	pool, err := s.store.riskPoolForAddresses(ctx, "eth", addrs)
	if err != nil {
		slog.Warn("address_risk whales badges skipped", "error", err)
		return d
	}
	risk := func(addr string) []string {
		if addr == "" { // ETH contract creations have no "to"
			return nil
		}
		return pool[strings.ToLower(addr)]
	}
	c := *d
	c.Largest = slices.Clone(d.Largest)
	c.TopReceivers = slices.Clone(d.TopReceivers)
	if st, err := s.store.getSyncState(ctx, stablecoinSourceID); err == nil && st.CoverageFrom > stablecoinFirstDay {
		c.FreezeCoverageFrom = st.CoverageFrom
	}
	for i := range c.Largest {
		c.Largest[i].FromRisk = risk(c.Largest[i].From)
		c.Largest[i].ToRisk = risk(c.Largest[i].To)
	}
	for i := range c.TopReceivers {
		c.TopReceivers[i].Risk = risk(c.TopReceivers[i].Address)
	}
	return &c
}
