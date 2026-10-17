package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/civil"
)

func openTestRiskStore(t *testing.T) *riskStore {
	t.Helper()
	s, err := openRiskStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBtcPriceBootstrapBlockchain(t *testing.T) {
	raw, err := os.ReadFile("testdata/bitcoin/blockchain_market_price.json")
	if err != nil {
		t.Fatal(err)
	}
	bcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer bcSrv.Close()

	store := openTestRiskStore(t)
	settings := BitcoinConfig{}.settings()
	settings.Providers["coinbase"] = false
	settings.Providers["kraken"] = false
	syncer := newBtcPriceSyncer(store, settings, func() time.Time {
		return time.Date(2015, 7, 25, 12, 0, 0, 0, time.UTC)
	})
	syncer.blockchainURL = bcSrv.URL

	syncer.touch()
	syncer.syncOnce(context.Background())

	closes := syncer.closes("2010-08-01", "2010-08-31")
	if _, ok := closes["2010-08-17"]; ok {
		t.Errorf("zero-price day before 2010-08-18 should not be stored")
	}
	if closes["2010-08-18"] <= 0 {
		t.Errorf("2010-08-18 close = %v, want > 0", closes["2010-08-18"])
	}
}

func TestBtcPriceBootstrapFallbackCoinMetrics(t *testing.T) {
	cmRaw, err := os.ReadFile("testdata/bitcoin/coinmetrics_asset_metrics.json")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("blockchain_com disabled", func(t *testing.T) {
		var bcCalls int32
		bcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&bcCalls, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer bcSrv.Close()

		cmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Strip next_page_url so it finishes in one page.
			var page map[string]any
			_ = json.Unmarshal(cmRaw, &page)
			delete(page, "next_page_url")
			delete(page, "next_page_token")
			_ = json.NewEncoder(w).Encode(page)
		}))
		defer cmSrv.Close()

		store := openTestRiskStore(t)
		settings := BitcoinConfig{}.settings()
		settings.Providers["blockchain_com"] = false
		settings.Providers["coinbase"] = false
		settings.Providers["kraken"] = false
		syncer := newBtcPriceSyncer(store, settings, func() time.Time {
			return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		})
		syncer.blockchainURL = bcSrv.URL
		syncer.coinmetricsURL = cmSrv.URL

		syncer.touch()
		syncer.syncOnce(context.Background())

		if atomic.LoadInt32(&bcCalls) != 0 {
			t.Errorf("blockchain.com called %d times when disabled", bcCalls)
		}
		closes := syncer.closes("2026-10-01", "2026-10-16")
		if len(closes) == 0 {
			t.Fatal("expected Coin Metrics fallback prices to be stored")
		}
	})

	t.Run("blockchain_com 502", func(t *testing.T) {
		bcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer bcSrv.Close()

		cmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var page map[string]any
			_ = json.Unmarshal(cmRaw, &page)
			delete(page, "next_page_url")
			delete(page, "next_page_token")
			_ = json.NewEncoder(w).Encode(page)
		}))
		defer cmSrv.Close()

		store := openTestRiskStore(t)
		settings := BitcoinConfig{}.settings()
		settings.Providers["coinbase"] = false
		settings.Providers["kraken"] = false
		syncer := newBtcPriceSyncer(store, settings, func() time.Time {
			return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		})
		syncer.blockchainURL = bcSrv.URL
		syncer.coinmetricsURL = cmSrv.URL

		syncer.touch()
		syncer.syncOnce(context.Background())

		cov := syncer.priceCoverage()
		if cov.Sources["coinmetrics"] == 0 {
			t.Errorf("expected coinmetrics rows after blockchain.com 502, got %+v", cov.Sources)
		}
	})
}

func TestBtcPriceCoinbasePaging(t *testing.T) {
	var pages int32
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pages, 1)
		startStr := r.URL.Query().Get("start")
		endStr := r.URL.Query().Get("end")
		startT, err1 := time.Parse(time.RFC3339, startStr)
		endT, err2 := time.Parse(time.RFC3339, endStr)
		if err1 != nil || err2 != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var rows [][]float64
		for d := startT; !d.After(endT); d = d.AddDate(0, 0, 1) {
			rows = append(rows, []float64{float64(d.Unix()), 60000, 62000, 60500, 61000, 1234})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer cbSrv.Close()

	store := openTestRiskStore(t)
	// Pre-seed bootstrap through 2025-06-30 so Coinbase syncs ~470 days (2 pages of <= 300 days).
	seed := []btcPriceDay{
		{Day: "2010-08-18", Close: 0.07, Source: "blockchain_com"},
		{Day: "2025-06-30", Close: 60000, Source: "coinbase"},
	}
	if err := store.upsertBtcPrices(context.Background(), seed, time.Now()); err != nil {
		t.Fatal(err)
	}

	settings := BitcoinConfig{}.settings()
	settings.Providers["blockchain_com"] = false
	settings.Providers["coinmetrics"] = false
	syncer := newBtcPriceSyncer(store, settings, func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	})
	syncer.coinbaseCandlesURL = cbSrv.URL

	syncer.touch()
	syncer.syncOnce(context.Background())

	if atomic.LoadInt32(&pages) < 2 {
		t.Errorf("Coinbase pages = %d, want >= 2 for >300-day span", pages)
	}
	closes := syncer.closes("2025-07-01", "2026-10-15")
	if len(closes) < 470 {
		t.Errorf("stored closes = %d, want >= 470", len(closes))
	}
}

func TestBtcPriceDailyIncrement(t *testing.T) {
	var lastStart string
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastStart = r.URL.Query().Get("start")
		t0, _ := time.Parse(time.RFC3339, lastStart)
		_ = json.NewEncoder(w).Encode([][]float64{
			{float64(t0.Unix()), 80000, 83000, 81000, 82064, 500},
		})
	}))
	defer cbSrv.Close()

	store := openTestRiskStore(t)
	seed := []btcPriceDay{
		{Day: "2010-08-18", Close: 0.07, Source: "blockchain_com"},
		{Day: "2026-10-14", Close: 81000, Source: "coinbase"},
	}
	if err := store.upsertBtcPrices(context.Background(), seed, time.Now()); err != nil {
		t.Fatal(err)
	}

	settings := BitcoinConfig{}.settings()
	syncer := newBtcPriceSyncer(store, settings, func() time.Time {
		return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	})
	syncer.coinbaseCandlesURL = cbSrv.URL

	syncer.touch()
	syncer.syncOnce(context.Background())

	if lastStart != "2026-10-15T00:00:00Z" {
		t.Errorf("incremental start = %q, want 2026-10-15T00:00:00Z", lastStart)
	}
	if got := syncer.closes("2026-10-15", "2026-10-15")["2026-10-15"]; got != 82064 {
		t.Errorf("2026-10-15 close = %v, want 82064", got)
	}
}

func TestBtcPriceKrakenFallback(t *testing.T) {
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer cbSrv.Close()

	krRaw, err := os.ReadFile("testdata/bitcoin/kraken_ohlc.json")
	if err != nil {
		t.Fatal(err)
	}
	krSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(krRaw)
	}))
	defer krSrv.Close()

	store := openTestRiskStore(t)
	seed := []btcPriceDay{
		{Day: "2010-08-18", Close: 0.07, Source: "blockchain_com"},
		{Day: "2026-10-05", Close: 62000, Source: "coinbase"},
	}
	if err := store.upsertBtcPrices(context.Background(), seed, time.Now()); err != nil {
		t.Fatal(err)
	}

	settings := BitcoinConfig{}.settings()
	syncer := newBtcPriceSyncer(store, settings, func() time.Time {
		return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	})
	syncer.coinbaseCandlesURL = cbSrv.URL
	syncer.krakenOHLCURL = krSrv.URL

	syncer.touch()
	syncer.syncOnce(context.Background())

	cov := syncer.priceCoverage()
	if cov.Sources["kraken"] == 0 {
		t.Errorf("expected kraken fallback rows, got sources = %v", cov.Sources)
	}
}

func TestBtcPriceLazyStart(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	store := openTestRiskStore(t)
	syncer := newBtcPriceSyncer(store, BitcoinConfig{}.settings(), time.Now)
	syncer.blockchainURL = srv.URL
	syncer.coinbaseCandlesURL = srv.URL

	// Before touch(), syncOnce must be a no-op.
	syncer.syncOnce(context.Background())
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("calls before touch() = %d, want 0", calls)
	}

	syncer.touch()
	syncer.syncOnce(context.Background())
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatalf("expected HTTP calls after touch()")
	}
}

func TestBtcPriceCoverage(t *testing.T) {
	start := civil.Date{Year: 2010, Month: 8, Day: 18}
	end := civil.Date{Year: 2015, Month: 7, Day: 25}

	t.Run("complete", func(t *testing.T) {
		store := openTestRiskStore(t)
		var rows []btcPriceDay
		for d := start; !d.After(end); d = d.AddDays(1) {
			rows = append(rows, btcPriceDay{Day: d.String(), Close: 100, Source: "blockchain_com"})
		}
		if err := store.upsertBtcPrices(context.Background(), rows, time.Now()); err != nil {
			t.Fatal(err)
		}
		syncer := newBtcPriceSyncer(store, BitcoinConfig{}.settings(), func() time.Time {
			return end.AddDays(1).In(time.UTC)
		})
		cov := syncer.priceCoverage()
		if !cov.Complete || cov.MissingDays != 0 || len(cov.MissingRanges) != 0 {
			t.Errorf("complete coverage = %+v", cov)
		}
	})

	t.Run("missing bootstrap 2010-08-18..2015-07-19", func(t *testing.T) {
		store := openTestRiskStore(t)
		var rows []btcPriceDay
		cbStart := civil.Date{Year: 2015, Month: 7, Day: 20}
		for d := cbStart; !d.After(end); d = d.AddDays(1) {
			rows = append(rows, btcPriceDay{Day: d.String(), Close: 280, Source: "coinbase"})
		}
		if err := store.upsertBtcPrices(context.Background(), rows, time.Now()); err != nil {
			t.Fatal(err)
		}
		syncer := newBtcPriceSyncer(store, BitcoinConfig{}.settings(), func() time.Time {
			return end.AddDays(1).In(time.UTC)
		})
		cov := syncer.priceCoverage()
		if cov.Complete {
			t.Fatalf("expected incomplete when 2010-08-18..2015-07-19 is missing")
		}
		if len(cov.MissingRanges) != 1 || cov.MissingRanges[0].From != "2010-08-18" || cov.MissingRanges[0].To != "2015-07-19" {
			t.Errorf("MissingRanges = %+v, want [2010-08-18..2015-07-19]", cov.MissingRanges)
		}
	})

	t.Run("single missing day", func(t *testing.T) {
		store := openTestRiskStore(t)
		var rows []btcPriceDay
		for d := start; !d.After(end); d = d.AddDays(1) {
			if d.String() == "2013-04-10" {
				continue
			}
			rows = append(rows, btcPriceDay{Day: d.String(), Close: 100, Source: "blockchain_com"})
		}
		if err := store.upsertBtcPrices(context.Background(), rows, time.Now()); err != nil {
			t.Fatal(err)
		}
		syncer := newBtcPriceSyncer(store, BitcoinConfig{}.settings(), func() time.Time {
			return end.AddDays(1).In(time.UTC)
		})
		cov := syncer.priceCoverage()
		if cov.Complete || cov.MissingDays != 1 {
			t.Fatalf("expected 1 missing day, got %+v", cov)
		}
		if len(cov.MissingRanges) != 1 || cov.MissingRanges[0].From != "2013-04-10" || cov.MissingRanges[0].To != "2013-04-10" {
			t.Errorf("MissingRanges = %+v, want [2013-04-10..2013-04-10]", cov.MissingRanges)
		}
	})
}
