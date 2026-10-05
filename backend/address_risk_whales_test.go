package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func whalesFixture() *CryptoWhalesData {
	return &CryptoWhalesData{Days: 30, Chain: "eth", Threshold: 1000,
		Largest: []WhaleTx{
			{Hash: "0xh1", From: ofacAddr[:2] + strings.ToUpper(ofacAddr[2:]), To: mewAddr, Amount: 5000},
			{Hash: "0xh2", From: "0x5555555555555555555555555555555555555555", To: "", Amount: 2000}, // contract creation
		},
		TopReceivers:  []WhaleAddress{{Address: frozenAddr, Total: 9000, TxCount: 3}, {Address: "0x6666666666666666666666666666666666666666", Total: 10}},
		Trend:         []WhaleTrendRow{},
		Concentration: []ConcentrationRow{},
	}
}

func whalesRiskStore(t *testing.T) *riskStore {
	t.Helper()
	s := newTestRiskStore(t)
	ctx := context.Background()
	s.replaceList(ctx, "ofac", []listEntry{{Address: ofacAddr}}, "h", time.Now())
	s.replaceList(ctx, "mew_darklist", []listEntry{{Address: mewAddr}}, "h", time.Now())
	s.insertStablecoinEvents(ctx, []stablecoinEvent{{TxHash: "0xf", LogIndex: 1, Token: "USDT", Action: "freeze",
		Address: frozenAddr, BlockNumber: 1, BlockTime: "2026-09-01T00:00:00Z"}}, "2026-08-27", "2026-09-25", time.Now())
	return s
}

func TestTagWhales(t *testing.T) {
	svc := newAddressRiskService(whalesRiskStore(t), nil)
	orig := whalesFixture()
	got := svc.tagWhales(context.Background(), orig)
	if strings.Join(got.Largest[0].FromRisk, ",") != "ofac" || strings.Join(got.Largest[0].ToRisk, ",") != "mew_darklist" ||
		got.Largest[1].FromRisk != nil || got.Largest[1].ToRisk != nil ||
		strings.Join(got.TopReceivers[0].Risk, ",") != "stablecoin" || got.TopReceivers[1].Risk != nil ||
		got.FreezeCoverageFrom != "2026-08-27" {
		t.Errorf("tagged = %+v / %+v (coverage=%q)", got.Largest, got.TopReceivers, got.FreezeCoverageFrom)
	}
	if orig.Largest[0].FromRisk != nil || orig.TopReceivers[0].Risk != nil || orig.FreezeCoverageFrom != "" {
		t.Error("tagging wrote into the original (cached) slices")
	}
}

// Without the risk DB, or for BTC, Whales must answer exactly as before.
func TestTagWhalesUntouched(t *testing.T) {
	d := whalesFixture()
	if got := newAddressRiskService(nil, nil).tagWhales(context.Background(), d); got != d {
		t.Error("store unavailable: response must be the original object")
	}
	var nilSvc *addressRiskService
	if got := nilSvc.tagWhales(context.Background(), d); got != d {
		t.Error("no risk service: response must be the original object")
	}
	btc := whalesFixture()
	btc.Chain = "btc"
	if got := newAddressRiskService(whalesRiskStore(t), nil).tagWhales(context.Background(), btc); got != btc {
		t.Error("BTC must not be tagged")
	}
}

// Two concurrent ETH whales requests served from the cache, under -race: the
// cached object must stay untagged and both responses carry the badges.
func TestCryptoWhalesBadgesConcurrentCacheUntouched(t *testing.T) {
	cached := whalesFixture()
	h := &APIHandler{cache: NewCache(time.Minute), risk: newAddressRiskService(whalesRiskStore(t), nil)}
	h.cache.Set("opendata:crypto:whales:eth:30", cached)
	var wg sync.WaitGroup
	bodies := make([]string, 2)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.CryptoWhales(rec, httptest.NewRequest("GET", "/api/opendata/crypto/whales?days=30&chain=eth", nil))
			bodies[i] = rec.Body.String()
		}()
	}
	wg.Wait()
	for _, b := range bodies {
		var d CryptoWhalesData
		if err := json.Unmarshal([]byte(b), &d); err != nil || strings.Join(d.Largest[0].FromRisk, ",") != "ofac" {
			t.Errorf("response = %s", b)
		}
	}
	if cached.Largest[0].FromRisk != nil || cached.TopReceivers[0].Risk != nil {
		t.Error("the cached whales object was mutated")
	}
}

// Without badges the JSON must not grow new keys (omitempty).
func TestCryptoWhalesNoRiskDBSameJSON(t *testing.T) {
	cached := whalesFixture()
	h := &APIHandler{cache: NewCache(time.Minute)}
	h.cache.Set("opendata:crypto:whales:eth:30", cached)
	rec := httptest.NewRecorder()
	h.CryptoWhales(rec, httptest.NewRequest("GET", "/api/opendata/crypto/whales?days=30&chain=eth", nil))
	want, _ := json.Marshal(cached)
	if strings.TrimSpace(rec.Body.String()) != string(want) || strings.Contains(rec.Body.String(), "risk") {
		t.Errorf("body = %s", rec.Body.String())
	}
}
