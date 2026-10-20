package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseCircleFixture(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 20, 0, 0, time.UTC)
	snap, err := parseCircleStablecoins(readStableFixture(t, "circle_stablecoins.json"), at)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(snap.Missing) != 0 {
		t.Errorf("Missing = %v", snap.Missing)
	}
	usdc, ok := snap.Coins["USDC"]
	if !ok || len(usdc.Chains) != fxCircleUSDCChains {
		t.Fatalf("USDC = %+v", usdc)
	}
	approxPtr(t, "usdc total", usdc.TotalNative, fxCircleUSDCTotal, 1e-3)
	var eth *circleChain
	for i := range usdc.Chains {
		if usdc.Chains[i].Chain == "ETH" {
			eth = &usdc.Chains[i]
		}
	}
	if eth == nil || eth.AmountNative != fxCircleUSDCEth {
		t.Fatalf("USDC ETH = %+v", eth)
	}
	if want := time.Date(2026, 10, 19, 13, 19, 0, 821_000_000, time.UTC); !eth.UpdatedAt.Equal(want) {
		t.Errorf("USDC ETH updateDate = %v, want %v", eth.UpdatedAt, want)
	}
	if want := time.Date(2026, 10, 19, 13, 19, 1, 697_000_000, time.UTC); !usdc.UpdatedAt.Equal(want) {
		t.Errorf("USDC UpdatedAt (newest chain) = %v, want %v", usdc.UpdatedAt, want)
	}
	eurc := snap.Coins["EURC"]
	if len(eurc.Chains) != fxCircleEURCChains {
		t.Fatalf("EURC chains = %d, want %d", len(eurc.Chains), fxCircleEURCChains)
	}
	approxPtr(t, "eurc total", eurc.TotalNative, fxCircleEURCTotal, 1e-6)
}

func TestParseCircleShape(t *testing.T) {
	at := time.Now()
	// Numbers are accepted as well as strings.
	num := `{"data": [{"symbol": "USDC", "totalAmount": 10.5, "chains": [{"chain": "ETH", "amount": 10.5, "updateDate": "2026-10-19T13:19:00.821Z"}]}]}`
	snap, err := parseCircleStablecoins([]byte(num), at)
	if err != nil || snap.Coins["USDC"].Chains[0].AmountNative != 10.5 {
		t.Fatalf("numeric amounts: %+v, %v", snap, err)
	}
	if len(snap.Missing) != 1 || snap.Missing[0] != "EURC" {
		t.Errorf("Missing = %v, want [EURC]", snap.Missing)
	}
	for name, body := range map[string]string{
		"not json":       `<html>`,
		"no data":        `{"items": []}`,
		"no wanted coin": `{"data": [{"symbol": "PYUSD", "chains": []}]}`,
		"bad amount":     `{"data": [{"symbol": "USDC", "totalAmount": "1", "chains": [{"chain": "ETH", "amount": "lots", "updateDate": "2026-10-19T13:19:00Z"}]}]}`,
		"negative":       `{"data": [{"symbol": "USDC", "totalAmount": "1", "chains": [{"chain": "ETH", "amount": "-1", "updateDate": "2026-10-19T13:19:00Z"}]}]}`,
		"bad date":       `{"data": [{"symbol": "USDC", "totalAmount": "1", "chains": [{"chain": "ETH", "amount": "1", "updateDate": "today"}]}]}`,
		"no chains":      `{"data": [{"symbol": "USDC", "totalAmount": "1", "chains": []}]}`,
		"empty chain id": `{"data": [{"symbol": "USDC", "totalAmount": "1", "chains": [{"chain": "", "amount": "1", "updateDate": "2026-10-19T13:19:00Z"}]}]}`,
	} {
		if _, err := parseCircleStablecoins([]byte(body), at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseTetherFixture(t *testing.T) {
	at := time.Date(2026, 10, 19, 13, 30, 0, 0, time.UTC)
	snap, err := parseTetherTransparency(readStableFixture(t, "tether_transparency.json"), at)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.UpdateID != fxTetherUpdateID || !snap.UpdatedAt.Equal(time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)) {
		t.Errorf("update = %d %v", snap.UpdateID, snap.UpdatedAt)
	}
	approxPtr(t, "total_assets_usd", snap.TotalAssetsUSD, fxTetherAssetsUSD, 1e-3)
	approxPtr(t, "total_liabilities_usd", snap.TotalLiabilitiesUSD, fxTetherLiabilitiesUSD, 1e-3)
	approxPtr(t, "shareholder_equity_usd", snap.ShareholderEquityUSD, fxTetherEquityUSD, 1e-3)
	if len(snap.Chains) != fxTetherChains {
		t.Fatalf("chains = %d, want %d", len(snap.Chains), fxTetherChains)
	}
	byName := map[string]tetherChain{}
	for _, c := range snap.Chains {
		byName[c.Name] = c
	}
	// Mixed JSON types: strings (Ethereum), numbers (Tron), int zeros (BNB), a string quarantine (Solana).
	eth := byName["Ethereum"]
	if eth.TotalAuthorizedNative != fxTetherEthAuthorized || eth.NotIssuedNative != fxTetherEthNotIssued {
		t.Errorf("Ethereum = %+v", eth)
	}
	approx := func(label string, got, want float64) {
		t.Helper()
		approxPtr(t, label, &got, want, 1e-4)
	}
	approx("Ethereum circulating", eth.CirculatingNative, fxTetherEthCirculating)
	approx("Tron circulating", byName["Tron"].CirculatingNative, fxTetherTronCirculating)
	sol := byName["Solana"]
	approx("Solana quarantined", sol.QuarantinedNative, fxTetherSolQuarantined)
	approx("Solana circulating", sol.CirculatingNative, fxTetherSolCirculating)
	if bnb := byName["BNB Smart Chain"]; bnb.TotalAuthorizedNative != 0 || bnb.CirculatingNative != 0 {
		t.Errorf("BNB = %+v", bnb)
	}
	// §5.1 worked check: Σ (totalAuthorized − notIssued − quarantined) = total_liabilities.
	approx("Σ circulating", snap.CirculatingSumNative, fxTetherLiabilitiesUSD)
}

func TestParseTetherShape(t *testing.T) {
	at := time.Now()
	good := `{"data_formatted": [{"id": 1792193400, "iso": "usdt", "total_assets": "110", "total_liabilities": "100", "shareholder_eq": 10,
	  "blockChains": [{"name": "Ethereum", "totalAuthorized": "80", "notIssued": 5, "quarantined": "0"}, {"name": "Tron", "totalAuthorized": 30, "notIssued": "4", "quarantined": 1}]},
	  {"id": 1792193400, "iso": "xaut", "total_assets": "1", "total_liabilities": "1", "shareholder_eq": "0", "blockChains": []}]}`
	snap, err := parseTetherTransparency([]byte(good), at)
	if err != nil || snap.CirculatingSumNative != 100 || len(snap.Chains) != 2 {
		t.Fatalf("synthetic: %+v, %v", snap, err)
	}
	if _, err := parseTetherTransparency([]byte(strings.Replace(good, `"id": 1792193400, "iso": "usdt"`, `"id": "1792193400", "iso": "usdt"`, 1)), at); err != nil {
		t.Errorf("string id rejected: %v", err)
	}
	for name, body := range map[string]string{
		"not json":          `<html>`,
		"no data_formatted": `{"data": {}}`,
		"no usdt":           strings.Replace(good, `"iso": "usdt"`, `"iso": "eurt"`, 1),
		"bad number":        strings.Replace(good, `"totalAuthorized": "80"`, `"totalAuthorized": "eighty"`, 1),
		"missing field":     strings.Replace(good, `"notIssued": 5, `, ``, 1),
		"no chains":         `{"data_formatted": [{"id": 1792193400, "iso": "usdt", "total_assets": "1", "total_liabilities": "1", "shareholder_eq": "0", "blockChains": []}]}`,
		"bad id":            strings.Replace(good, `"id": 1792193400, "iso": "usdt"`, `"id": 0, "iso": "usdt"`, 1),
		"missing liability": strings.Replace(good, `"total_liabilities": "100", `, ``, 1),
	} {
		if _, err := parseTetherTransparency([]byte(body), at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A per-chain sum that no longer matches total_liabilities (> 0.01%) is
	// served with a degraded flag (§6 S12, "Tether sum check"), never blocked.
	snap, err = parseTetherTransparency([]byte(strings.Replace(good, `"total_liabilities": "100"`, `"total_liabilities": "100.02"`, 1)), at)
	if err != nil || !snap.Degraded || snap.DegradedReason != "sum_mismatch" || snap.CirculatingSumNative != 100 || len(snap.Chains) != 2 ||
		snap.TotalLiabilitiesUSD == nil || *snap.TotalLiabilitiesUSD != 100.02 {
		t.Errorf("sum mismatch: %+v, %v (want the data with a degraded flag)", snap, err)
	}
	approxPtr(t, "sum mismatch pct", snap.SumMismatchPct, (100-100.02)/100.02*100, 1e-9)
	snap, err = parseTetherTransparency([]byte(strings.Replace(good, `"total_liabilities": "100"`, `"total_liabilities": "100.005"`, 1)), at)
	if err != nil || snap.Degraded || snap.DegradedReason != "" {
		t.Errorf("sum within 0.01%%: %+v, %v (want not degraded)", snap, err)
	}
}

func TestFetchIssuersFromStub(t *testing.T) {
	circle := readStableFixture(t, "circle_stablecoins.json")
	tether := readStableFixture(t, "tether_transparency.json")
	drift := strings.Replace(string(tether), `"total_liabilities": "`+fxTetherLiabilitiesStr+`"`, `"total_liabilities": "1`+fxTetherLiabilitiesStr+`"`, 1)
	if drift == string(tether) {
		t.Fatal("fixture edit did not apply (test bug)")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/circle":
			w.Write(circle)
		case "/tether":
			w.Write(tether)
		case "/tether-drift":
			w.Write([]byte(drift))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	ep := stableEndpoints{CircleURL: srv.URL + "/circle", TetherURL: srv.URL + "/tether"}
	if snap, code := fetchCircleStablecoins(ctx, ep, time.Now()); code != "" || len(snap.Coins) != 2 {
		t.Errorf("circle: code %q, coins %d", code, len(snap.Coins))
	}
	if snap, code := fetchTetherTransparency(ctx, ep, time.Now()); code != "" || snap.UpdateID != fxTetherUpdateID {
		t.Errorf("tether: code %q, update %d", code, snap.UpdateID)
	}
	ep.TetherURL = srv.URL + "/tether-drift"
	if snap, code := fetchTetherTransparency(ctx, ep, time.Now()); code != "" || !snap.Degraded || len(snap.Chains) != fxTetherChains {
		t.Errorf("tether drift: code %q, degraded %v, %d chains (want the data, flagged degraded)", code, snap.Degraded, len(snap.Chains))
	}
	ep.CircleURL = srv.URL + "/missing"
	if _, code := fetchCircleStablecoins(ctx, ep, time.Now()); code != "not_found" {
		t.Errorf("circle 404: code %q, want not_found", code)
	}
}
