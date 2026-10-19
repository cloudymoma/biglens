package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCryptoTokensLive_CacheAndStaleFallback(t *testing.T) {
	store, err := openRiskStore(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("openRiskStore: %v", err)
	}
	defer store.db.Close()

	var (
		evmHits   atomic.Int32
		tronHits  atomic.Int32
		solHits   atomic.Int32
		llamaHits atomic.Int32
		bsHits    atomic.Int32
	)

	evmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evmHits.Add(1)
		// 10,000,000 * 1e6 = 10,000,000,000,000 = 0x9184e72a000
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x000000000000000000000000000000000000000000000000000009184e72a000"}`))
	}))
	defer evmSrv.Close()

	tronSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tronHits.Add(1)
		// 85,000,000,000 * 1e6 = 85,000,000,000,000,000 = 0x12df50686c0000
		w.Write([]byte(`{"result":{"result":true},"constant_result":["0000000000000000000000000000000000000000000000000012df50686c0000"]}`))
	}))
	defer tronSrv.Close()

	solSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		solHits.Add(1)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":{"data":{"parsed":{"info":{"decimals":6,"supply":"5000000000000000"}}}}}}`))
	}))
	defer solSrv.Close()

	llamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamaHits.Add(1)
		coins := make(map[string]any, len(majorTokenCatalog))
		for _, m := range majorTokenCatalog {
			p := 1.0
			if m.Category == "eth" {
				p = 2500.0
			} else if m.Category == "btc" {
				p = 65000.0
			}
			coins["ethereum:"+m.Address] = map[string]any{"price": p, "symbol": m.Symbol}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"coins": coins})
	}))
	defer llamaSrv.Close()

	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bsHits.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/api/v2/addresses/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"is_scam":false,"reputation":"ok","token":{"holders_count":"4500000","volume_24h":"9500000000.50","circulating_market_cap":"115000000000.00"}}`))
	}))
	defer bsSrv.Close()

	origLlama := defillamaCoinsURL
	origTron := tronGridBaseURL
	defillamaCoinsURL = llamaSrv.URL
	tronGridBaseURL = tronSrv.URL
	defer func() {
		defillamaCoinsURL = origLlama
		tronGridBaseURL = origTron
	}()

	rs := newAddressRiskService(store, []string{evmSrv.URL})
	rs.chainRPCs = map[string][]string{
		"eth":  {evmSrv.URL},
		"arb":  {evmSrv.URL},
		"op":   {evmSrv.URL},
		"base": {evmSrv.URL},
		"sol":  {solSrv.URL},
	}
	rs.blockscoutURLs = map[string]string{"eth": bsSrv.URL}

	h := &APIHandler{
		cache: NewCache(time.Minute),
		risk:  rs,
	}

	// 1. Cold call populates SQLite and returns 14 supply rows + 12 market rows.
	rec1 := httptest.NewRecorder()
	h.CryptoTokensLive(rec1, httptest.NewRequest("GET", "/api/opendata/crypto/tokens/live", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec1.Code, rec1.Body.String())
	}
	var got1 CryptoTokensLiveData
	if err := json.NewDecoder(rec1.Body).Decode(&got1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got1.SupplyMatrix) != len(registryTokens) {
		t.Fatalf("len(SupplyMatrix) = %d, want %d", len(got1.SupplyMatrix), len(registryTokens))
	}
	if len(got1.MarketPulse) != len(majorTokenCatalog) {
		t.Fatalf("len(MarketPulse) = %d, want %d", len(got1.MarketPulse), len(majorTokenCatalog))
	}
	for _, r := range got1.SupplyMatrix {
		if r.Supply <= 0 {
			t.Errorf("expected positive supply for %s/%s, got %+v", r.Asset, r.Network, r)
		}
	}

	// 2. Simulate server restart (fresh in-memory cache) while SQLite cache is still within TTL:
	// zero upstream HTTP requests should be made.
	h.cache = NewCache(time.Minute)
	beforeEVM := evmHits.Load()
	beforeTron := tronHits.Load()
	beforeSol := solHits.Load()
	beforeLlama := llamaHits.Load()
	beforeBS := bsHits.Load()

	rec2 := httptest.NewRecorder()
	h.CryptoTokensLive(rec2, httptest.NewRequest("GET", "/api/opendata/crypto/tokens/live", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("status 2 = %d", rec2.Code)
	}
	if evmHits.Load() != beforeEVM || tronHits.Load() != beforeTron || solHits.Load() != beforeSol || llamaHits.Load() != beforeLlama || bsHits.Load() != beforeBS {
		t.Errorf("expected 0 upstream hits when SQLite cache is fresh, got deltas evm=%d tron=%d sol=%d llama=%d bs=%d",
			evmHits.Load()-beforeEVM, tronHits.Load()-beforeTron, solHits.Load()-beforeSol, llamaHits.Load()-beforeLlama, bsHits.Load()-beforeBS)
	}

	// 3. Age the SQLite cache past TTL and close upstreams to verify stale-on-error fallback.
	past := time.Now().UTC().Add(-20 * time.Minute)
	_ = store.upsertTokenSupplyCache(context.Background(), got1.SupplyMatrix, past)
	_ = store.upsertTokenMarketCache(context.Background(), got1.MarketPulse, past)
	h.cache = NewCache(time.Minute)
	evmSrv.Close()
	tronSrv.Close()
	solSrv.Close()
	llamaSrv.Close()
	bsSrv.Close()

	rec3 := httptest.NewRecorder()
	h.CryptoTokensLive(rec3, httptest.NewRequest("GET", "/api/opendata/crypto/tokens/live", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("status 3 = %d, body = %s", rec3.Code, rec3.Body.String())
	}
	var got3 CryptoTokensLiveData
	if err := json.NewDecoder(rec3.Body).Decode(&got3); err != nil {
		t.Fatalf("decode 3: %v", err)
	}
	if len(got3.Warnings) == 0 {
		t.Errorf("expected warnings when upstreams are down")
	}
	for _, sr := range got3.SupplyMatrix {
		if !sr.Stale || sr.Supply <= 0 {
			t.Errorf("expected stale supply preserved on error, got %+v", sr)
		}
	}
}

// TestRefreshMarketPulseNoStablecoinDollarFallback: a missing stablecoin price
// must stay unknown (JSON null, stale) instead of being assumed to be $1, and
// the last-good price from the previous rows must still apply. Asserted on the
// JSON shape so the test is independent of the Go field type.
func TestRefreshMarketPulseNoStablecoinDollarFallback(t *testing.T) {
	// DefiLlama answers but omits every stablecoin; non-stablecoins are priced.
	llamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coins := map[string]any{}
		for _, m := range majorTokenCatalog {
			switch m.Category {
			case "eth":
				coins["ethereum:"+m.Address] = map[string]any{"price": 2500.0}
			case "btc":
				coins["ethereum:"+m.Address] = map[string]any{"price": 65000.0}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"coins": coins})
	}))
	defer llamaSrv.Close()
	bsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"is_scam":false,"reputation":"ok","token":{"holders_count":"10","volume_24h":"1","circulating_market_cap":"1"}}`))
	}))
	defer bsSrv.Close()
	origLlama := defillamaCoinsURL
	defillamaCoinsURL = llamaSrv.URL
	defer func() { defillamaCoinsURL = origLlama }()

	rs := newAddressRiskService(nil, nil)
	rs.blockscoutURLs = map[string]string{"eth": bsSrv.URL}
	h := &APIHandler{cache: NewCache(time.Minute), risk: rs}
	now := time.Now().UTC()

	stableRows := func(rows []TokenMarketRow) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, r := range rows {
			if majorTokenByAddr[strings.ToLower(r.Address)].Category != "stablecoin" {
				continue
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out = append(out, m)
		}
		if len(out) == 0 {
			t.Fatal("catalog has no stablecoin rows")
		}
		return out
	}

	// 1. No previous rows: the price is unknown, so it must be null and stale.
	rows, _ := h.refreshMarketPulse(context.Background(), nil, now)
	for _, m := range stableRows(rows) {
		if m["price_usd"] != nil {
			t.Errorf("%v: price_usd = %v, want null (no $1 assumption)", m["symbol"], m["price_usd"])
		}
		if m["stale"] != true {
			t.Errorf("%v: stale = %v, want true when the price is missing", m["symbol"], m["stale"])
		}
	}

	// 2. Previous rows carry a real price: last-good applies instead of $1.
	var prev []TokenMarketRow
	for _, m := range majorTokenCatalog {
		if m.Category != "stablecoin" {
			continue
		}
		var r TokenMarketRow
		raw := `{"address":"` + m.Address + `","symbol":"` + m.Symbol + `","price_usd":0.9997,"updated_at":"2026-10-19T00:00:00Z"}`
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("prev row: %v", err)
		}
		prev = append(prev, r)
	}
	rows, _ = h.refreshMarketPulse(context.Background(), prev, now)
	for _, m := range stableRows(rows) {
		if m["price_usd"] != 0.9997 {
			t.Errorf("%v: price_usd = %v, want last-good 0.9997", m["symbol"], m["price_usd"])
		}
		if m["stale"] != true || m["updated_at"] != "2026-10-19T00:00:00Z" {
			t.Errorf("%v: stale=%v updated_at=%v, want stale last-good row", m["symbol"], m["stale"], m["updated_at"])
		}
	}
}
