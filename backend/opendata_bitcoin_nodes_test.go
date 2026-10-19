package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAggregateBitnodesNetworkMix(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "bitnodes_latest.json"))
	if err != nil {
		t.Fatalf("read bitnodes_latest.json: %v", err)
	}
	var snap btcBitnodesSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal bitnodes_latest.json: %v", err)
	}

	agg := aggregateBitnodes(&snap)
	if agg.Reachable != 300 {
		t.Errorf("Reachable = %d, want 300", agg.Reachable)
	}
	if agg.Network.Tor != 140 {
		t.Errorf("Network.Tor = %d, want 140", agg.Network.Tor)
	}
	if agg.Network.I2P != 55 {
		t.Errorf("Network.I2P = %d, want 55", agg.Network.I2P)
	}
	if agg.Network.IPv4 != 88 {
		t.Errorf("Network.IPv4 = %d, want 88", agg.Network.IPv4)
	}
	if agg.Network.IPv6 != 15 {
		t.Errorf("Network.IPv6 = %d, want 15", agg.Network.IPv6)
	}
	if agg.Network.CJDNS != 2 {
		t.Errorf("Network.CJDNS = %d, want 2", agg.Network.CJDNS)
	}
	if agg.Clearnet != 103 {
		t.Errorf("Clearnet = %d, want 103 (88 IPv4 + 15 IPv6)", agg.Clearnet)
	}
	if agg.ClientCounts["Bitcoin Core"] != 250 {
		t.Errorf("Bitcoin Core count = %d, want 250", agg.ClientCounts["Bitcoin Core"])
	}
	if agg.ClientCounts["Bitcoin Knots"] != 48 {
		t.Errorf("Bitcoin Knots count = %d, want 48", agg.ClientCounts["Bitcoin Knots"])
	}
	if agg.ClientCounts["btcd"] != 2 {
		t.Errorf("btcd count = %d, want 2", agg.ClientCounts["btcd"])
	}
	if agg.CoreVersionCount["28.x"] != 140 || agg.CoreVersionCount["27.x"] != 80 || agg.CoreVersionCount["26.x"] != 30 {
		t.Errorf("CoreVersionCount = %+v, want 28.x:140 27.x:80 26.x:30", agg.CoreVersionCount)
	}
}

type hostRewriteRoundTripper struct {
	handler http.Handler
}

func (rt hostRewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func TestBitnodesRedirect(t *testing.T) {
	fixtureRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "bitnodes_latest.json"))
	if err != nil {
		t.Fatalf("read bitnodes_latest.json: %v", err)
	}

	origTransport := btcHTTPClient.Transport
	origURL := btcBitnodesLatestURL
	defer func() {
		btcHTTPClient.Transport = origTransport
		btcBitnodesLatestURL = origURL
	}()

	// Case 1: 302 from bitnodes.io to btcnodes.io is followed once and succeeds.
	var sawCompatHeader atomic.Bool
	btcHTTPClient.Transport = hostRewriteRoundTripper{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Host {
			case "bitnodes.io":
				w.Header().Set("Location", "https://btcnodes.io/api/v1/snapshots/latest/")
				w.Header().Set("x-bitnodes-route", "compatibility-backend")
				w.WriteHeader(http.StatusFound)
			case "btcnodes.io":
				sawCompatHeader.Store(true)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(fixtureRaw)
			default:
				http.NotFound(w, r)
			}
		}),
	}
	btcBitnodesLatestURL = "https://bitnodes.io/api/v1/snapshots/latest/"
	snap, code := fetchBitnodesSnapshot(context.Background())
	if code != "" || snap == nil || len(snap.Nodes) != 300 || !sawCompatHeader.Load() {
		t.Fatalf("expected 302 to btcnodes.io to succeed; got code=%q snap=%v", code, snap != nil)
	}

	// Case 2: 302 to an unallowlisted host -> bad_response.
	btcHTTPClient.Transport = hostRewriteRoundTripper{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://evil.example.com/api/v1/snapshots/latest/")
			w.WriteHeader(http.StatusFound)
		}),
	}
	if _, code := fetchBitnodesSnapshot(context.Background()); code != "bad_response" {
		t.Errorf("redirect to non-allowlisted host: code = %q, want bad_response", code)
	}

	// Case 3: Second redirect (even between allowlisted hosts) -> bad_response.
	btcHTTPClient.Transport = hostRewriteRoundTripper{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/step2") {
				w.Header().Set("Location", "https://bitnodes.io/step3")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Location", "https://btcnodes.io/step2")
			w.WriteHeader(http.StatusFound)
		}),
	}
	if _, code := fetchBitnodesSnapshot(context.Background()); code != "bad_response" {
		t.Errorf("second redirect: code = %q, want bad_response", code)
	}
}

func TestMergeJoinIPs(t *testing.T) {
	ranges := []dbipRangeRow{
		{
			Start:   netip.MustParseAddr("10.0.0.10"),
			End:     netip.MustParseAddr("10.0.0.20"),
			Country: "US",
			ASN:     15169,
			Org:     "Google LLC",
		},
		{
			Start:   netip.MustParseAddr("88.198.0.0"),
			End:     netip.MustParseAddr("88.198.0.255"),
			Country: "DE",
			ASN:     24940,
			Org:     "Hetzner Online GmbH",
		},
		{
			Start:   netip.MustParseAddr("2a01:4f8::"),
			End:     netip.MustParseAddr("2a01:4f8:ffff:ffff:ffff:ffff:ffff:ffff"),
			Country: "DE",
			ASN:     24940,
			Org:     "Hetzner Online GmbH",
		},
	}

	ips := []netip.Addr{
		netip.MustParseAddr("10.0.0.9"),    // below first range (unresolved)
		netip.MustParseAddr("10.0.0.10"),   // lower boundary of range 1
		netip.MustParseAddr("10.0.0.20"),   // upper boundary of range 1
		netip.MustParseAddr("10.0.0.21"),   // gap between range 1 and range 2 (unresolved)
		netip.MustParseAddr("88.198.0.42"), // inside range 2
		netip.MustParseAddr("2001:db8::1"), // IPv6 before range 3 (unresolved)
		netip.MustParseAddr("2a01:4f8::1"), // IPv6 lower boundary
		netip.MustParseAddr("2c00::1"),     // IPv6 after last range (unresolved)
	}
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })

	hits := mergeJoinIPs(ips, slices.Values(ranges))
	if len(hits) != 4 {
		t.Fatalf("len(hits) = %d, want 4; hits=%+v", len(hits), hits)
	}
	if h := hits[netip.MustParseAddr("10.0.0.10")]; h.Country != "US" || h.ASN != 15169 {
		t.Errorf("10.0.0.10 hit = %+v, want US / 15169", h)
	}
	if h := hits[netip.MustParseAddr("10.0.0.20")]; h.Country != "US" || h.ASN != 15169 {
		t.Errorf("10.0.0.20 hit = %+v, want US / 15169", h)
	}
	if h := hits[netip.MustParseAddr("88.198.0.42")]; h.Country != "DE" || h.ASN != 24940 {
		t.Errorf("88.198.0.42 hit = %+v, want DE / 24940", h)
	}
	if h := hits[netip.MustParseAddr("2a01:4f8::1")]; h.Country != "DE" || h.ASN != 24940 {
		t.Errorf("2a01:4f8::1 hit = %+v, want DE / 24940", h)
	}
	if _, ok := hits[netip.MustParseAddr("10.0.0.9")]; ok {
		t.Errorf("10.0.0.9 should be unresolved")
	}
	if _, ok := hits[netip.MustParseAddr("10.0.0.21")]; ok {
		t.Errorf("10.0.0.21 should be unresolved")
	}
}

func TestNodeConcentration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "bitnodes_latest.json"))
	if err != nil {
		t.Fatalf("read bitnodes_latest.json: %v", err)
	}
	var snap btcBitnodesSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	agg := aggregateBitnodes(&snap)

	cPath := filepath.Join("testdata", "bitcoin", "dbip_country_sample.csv.gz")
	aPath := filepath.Join("testdata", "bitcoin", "dbip_asn_sample.csv.gz")
	geoHits := make(map[netip.Addr]geoHit, len(agg.SortedIPs))
	mergeJoinIPsInto(geoHits, agg.SortedIPs, streamDBIPCountryGz(cPath))
	mergeJoinIPsInto(geoHits, agg.SortedIPs, streamDBIPASNGz(aPath))

	dist := computeNodeDistribution(agg, geoHits)
	// Clearnet = 103 (88 IPv4 + 15 IPv6). 2 IPs in 198.51.100.x are unresolved.
	// Resolved = 101:
	//   AS24940 Hetzner (DE, cloud): 30 IPv4 + 8 IPv6 = 38
	//   AS63949 Akamai/Linode (US, cloud): 25 IPv4 + 7 IPv6 = 32
	//   AS16276 OVH (FR, cloud): 15 IPv4 = 15
	//   AS15169 Google Cloud (US, cloud): 10 IPv4 = 10
	//   AS7922 Comcast (US, ISP): 6 IPv4 = 6
	// Countries: US = 32 + 10 + 6 = 48, DE = 38, FR = 15 (Unknown = 2).
	//   48/101 <= 50%, 48+38 = 86 > 50% -> Country Nakamoto = 2.
	// ASNs: 38 + 32 = 70 > 50% of 101 -> ASN Nakamoto = 2.
	// Cloud nodes: 38 + 32 + 15 + 10 = 95 / 103 = 92.233%.
	if dist.Unresolved != 2 {
		t.Errorf("Unresolved = %d, want 2", dist.Unresolved)
	}
	if dist.CountryNakamoto.Value != 2 {
		t.Errorf("CountryNakamoto.Value = %v, want 2", dist.CountryNakamoto.Value)
	}
	if dist.ASNNakamoto.Value != 2 {
		t.Errorf("ASNNakamoto.Value = %v, want 2", dist.ASNNakamoto.Value)
	}
	wantCloud := 95.0 / 103.0 * 100.0
	if math.Abs(dist.CloudShare.Value-wantCloud) > 0.05 {
		t.Errorf("CloudShare.Value = %.2f, want %.2f", dist.CloudShare.Value, wantCloud)
	}
	if dist.ASNHHI.Value <= 2000 {
		t.Errorf("ASNHHI.Value = %.2f, want > 2000", dist.ASNHHI.Value)
	}

	// Principle 10: every concentration metric carries denominator, unit, coverage_pct, and method.
	for name, m := range map[string]BtcConcentrationMetric{
		"CloudShare":      dist.CloudShare,
		"ASNNakamoto":     dist.ASNNakamoto,
		"CountryNakamoto": dist.CountryNakamoto,
		"ASNHHI":          dist.ASNHHI,
	} {
		if m.Denominator != 103 {
			t.Errorf("%s.Denominator = %d, want 103", name, m.Denominator)
		}
		if m.Unit != "clearnet nodes" {
			t.Errorf("%s.Unit = %q, want 'clearnet nodes'", name, m.Unit)
		}
		wantCov := 103.0 / 300.0 * 100.0
		if math.Abs(m.CoveragePct-wantCov) > 0.05 {
			t.Errorf("%s.CoveragePct = %.2f, want %.2f", name, m.CoveragePct, wantCov)
		}
		if m.Method == "" {
			t.Errorf("%s.Method is empty", name)
		}
	}
}

func TestDBIPMonthlyRefresh(t *testing.T) {
	countryRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "dbip_country_sample.csv.gz"))
	if err != nil {
		t.Fatalf("read dbip_country_sample.csv.gz: %v", err)
	}
	asnRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "dbip_asn_sample.csv.gz"))
	if err != nil {
		t.Fatalf("read dbip_asn_sample.csv.gz: %v", err)
	}

	var countryHits, asnHits atomic.Int32
	var failNext atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "dbip-country-lite-"):
			countryHits.Add(1)
			_, _ = w.Write(countryRaw)
		case strings.Contains(r.URL.Path, "dbip-asn-lite-"):
			asnHits.Add(1)
			_, _ = w.Write(asnRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origURL := btcDBIPBaseURL
	btcDBIPBaseURL = srv.URL
	defer func() { btcDBIPBaseURL = origURL }()

	geoDir := t.TempDir()
	c1, a1, err := dbipFiles(context.Background(), geoDir, "2026-10")
	if err != nil {
		t.Fatalf("first dbipFiles(2026-10): %v", err)
	}
	if countryHits.Load() != 1 || asnHits.Load() != 1 {
		t.Fatalf("downloads = %d/%d, want 1/1", countryHits.Load(), asnHits.Load())
	}

	// Second call within the same month must not re-download.
	c2, a2, err := dbipFiles(context.Background(), geoDir, "2026-10")
	if err != nil {
		t.Fatalf("second dbipFiles(2026-10): %v", err)
	}
	if c1 != c2 || a1 != a2 {
		t.Errorf("paths changed within same month: %s/%s vs %s/%s", c1, a1, c2, a2)
	}
	if countryHits.Load() != 1 || asnHits.Load() != 1 {
		t.Errorf("re-downloaded within same month: %d/%d, want 1/1", countryHits.Load(), asnHits.Load())
	}

	// Next month download fails -> falls back to the previous month's files.
	failNext.Store(true)
	c3, a3, err := dbipFiles(context.Background(), geoDir, "2026-11")
	if err != nil {
		t.Fatalf("dbipFiles(2026-11) with upstream failure should keep previous files, got err: %v", err)
	}
	if c3 != c1 || a3 != a1 {
		t.Errorf("fallback paths = %s / %s, want %s / %s", c3, a3, c1, a1)
	}
}

func TestBitnodesPersisted(t *testing.T) {
	bitRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "bitnodes_latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	countryRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "dbip_country_sample.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	asnRaw, err := os.ReadFile(filepath.Join("testdata", "bitcoin", "dbip_asn_sample.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}

	var bitDown atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bitnodes":
			if bitDown.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write(bitRaw)
		case strings.Contains(r.URL.Path, "dbip-country-lite-"):
			_, _ = w.Write(countryRaw)
		case strings.Contains(r.URL.Path, "dbip-asn-lite-"):
			_, _ = w.Write(asnRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBit := btcBitnodesLatestURL
	origDBIP := btcDBIPBaseURL
	btcBitnodesLatestURL = srv.URL + "/bitnodes"
	btcDBIPBaseURL = srv.URL
	defer func() {
		btcBitnodesLatestURL = origBit
		btcDBIPBaseURL = origDBIP
	}()

	store := openTestRiskStore(t)
	geoDir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	settings := (BitcoinConfig{}).settings()

	ns1 := newBtcNodesSyncer(store, settings, geoDir, func() time.Time { return now })
	nodes1, bitErr, geoErr := ns1.syncOnce(context.Background())
	if bitErr != "" || geoErr != "" || nodes1 == nil {
		t.Fatalf("ns1.syncOnce: bitErr=%q geoErr=%q nodes=%v", bitErr, geoErr, nodes1)
	}
	if nodes1.Reachable != 300 || nodes1.Clearnet != 103 {
		t.Fatalf("nodes1 = %+v", nodes1)
	}

	// Simulate server restart while Bitnodes is down: new syncer loads persisted snapshot from SQLite.
	bitDown.Store(true)
	svc := &bitcoinService{
		settings: settings,
		store:    store,
		now:      func() time.Time { return now.Add(1 * time.Hour) },
	}
	ns2 := newBtcNodesSyncer(store, settings, geoDir, svc.now)
	svc.nodes = ns2

	got, bitSt, dbipSt := ns2.get(context.Background(), svc)
	if got == nil || got.Reachable != 300 || got.Clearnet != 103 {
		t.Fatalf("expected persisted snapshot from btc_nodes_snapshot, got %+v", got)
	}
	if bitSt.Status != "ok" || dbipSt.Status != "ok" {
		t.Errorf("statuses = %+v / %+v, want ok / ok", bitSt, dbipSt)
	}
}

func TestClassifyBitnodesClientUnicodeNoPanic(t *testing.T) {
	// 'Ⱥ' (U+023A, 2 bytes in UTF-8) expands to 'ⱥ' (U+2C65, 3 bytes) under strings.ToLower.
	cases := []struct {
		ua        string
		wantCli   string
		wantMajor string
	}{
		{
			ua:        strings.Repeat("Ⱥ", 12) + "/Satoshi:28.1.0/",
			wantCli:   "Bitcoin Core",
			wantMajor: "28.x",
		},
		{
			ua:        strings.Repeat("Ⱥ", 20) + "satoshi:",
			wantCli:   "Bitcoin Core",
			wantMajor: "Other",
		},
		{
			ua:        "/İİİ/Satoshi:0.21.1(custom)/",
			wantCli:   "Bitcoin Core",
			wantMajor: "0.21.x",
		},
	}
	for _, tc := range cases {
		cli, major := classifyBitnodesClient(tc.ua)
		if cli != tc.wantCli || major != tc.wantMajor {
			t.Errorf("classifyBitnodesClient(%q) = (%q, %q), want (%q, %q)", tc.ua, cli, major, tc.wantCli, tc.wantMajor)
		}
	}
}

func TestBitnodesColdStartFailBackoff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	origBit := btcBitnodesLatestURL
	btcBitnodesLatestURL = srv.URL + "/bitnodes"
	defer func() { btcBitnodesLatestURL = origBit }()

	store := openTestRiskStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	settings := (BitcoinConfig{}).settings()
	svc := &bitcoinService{
		settings: settings,
		store:    store,
		now:      func() time.Time { return now },
	}
	ns := newBtcNodesSyncer(store, settings, t.TempDir(), svc.now)
	svc.nodes = ns

	// First cold-start call hits upstream and fails.
	got1, bitSt1, _ := ns.get(context.Background(), svc)
	if got1 != nil || bitSt1.Status != "error" {
		t.Fatalf("first call: got=%v status=%+v", got1, bitSt1)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits after first call = %d, want 1", hits.Load())
	}

	// Second call 1 minute later must back off without hitting upstream again.
	now = now.Add(1 * time.Minute)
	got2, bitSt2, _ := ns.get(context.Background(), svc)
	if got2 != nil || bitSt2.Status != "error" {
		t.Fatalf("second call: got=%v status=%+v", got2, bitSt2)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits during backoff = %d, want 1", hits.Load())
	}
}
