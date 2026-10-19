package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseECBFixtures(t *testing.T) {
	fx, err := parseECBSeries(readStableFixture(t, "ecb_exr_usd.json"), stableECBEURUSDDims)
	if err != nil {
		t.Fatalf("EXR: %v", err)
	}
	want := []stableRateObs{{"2026-10-14", 1.1195}, {"2026-10-15", 1.1208}, {"2026-10-16", 1.1253}}
	if len(fx) != len(want) {
		t.Fatalf("EXR obs = %+v", fx)
	}
	for i := range want {
		if fx[i] != want[i] {
			t.Errorf("EXR[%d] = %+v, want %+v", i, fx[i], want[i])
		}
	}
	estr, err := parseECBSeries(readStableFixture(t, "ecb_estr.json"), stableECBESTRDims)
	if err != nil {
		t.Fatalf("ESTR: %v", err)
	}
	if len(estr) != 3 || estr[2] != (stableRateObs{"2026-10-16", 2.441}) {
		t.Errorf("ESTR obs = %+v", estr)
	}
	// Response-shape validation: the €STR document is not an EUR/USD series.
	if _, err := parseECBSeries(readStableFixture(t, "ecb_estr.json"), stableECBEURUSDDims); err == nil {
		t.Error("EXR parser accepted the €STR series")
	}
	for name, body := range map[string]string{
		"not json":      `<html>`,
		"no datasets":   `{"dataSets": [], "structure": {}}`,
		"null obs only": strings.Replace(string(readStableFixture(t, "ecb_exr_usd.json")), `1.1195,`, `null,`, 1),
	} {
		if name == "null obs only" && !strings.Contains(body, `null,`) {
			t.Fatal("fixture edit did not apply (test bug)")
		}
		got, err := parseECBSeries([]byte(body), stableECBEURUSDDims)
		if name == "null obs only" {
			// A null observation is skipped, not read as 0.
			if err != nil || len(got) != 2 {
				t.Errorf("%s: %+v, %v (want 2 obs)", name, got, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseTreasuryBillCSV(t *testing.T) {
	obs, err := parseTreasuryBillCSV(readStableFixture(t, "treasury_bill_rates.csv"))
	if err != nil {
		t.Fatal(err)
	}
	want := []stableRateObs{{"2026-10-09", 4.16}, {"2026-10-13", 4.19}, {"2026-10-14", 4.20}, {"2026-10-15", 4.20}, {"2026-10-16", 4.18}}
	if len(obs) != len(want) {
		t.Fatalf("obs = %+v", obs)
	}
	for i := range want {
		if obs[i] != want[i] {
			t.Errorf("obs[%d] = %+v, want %+v (oldest first)", i, obs[i], want[i])
		}
	}
	header := "Date,\"13 WEEKS BANK DISCOUNT\",\"13 WEEKS COUPON EQUIVALENT\"\n"
	if got, err := parseTreasuryBillCSV([]byte(header + "10/16/2026,4.08,N/A\n10/15/2026,4.10,4.20\n")); err != nil || len(got) != 1 {
		t.Errorf("N/A row must be skipped: %+v, %v", got, err)
	}
	for name, body := range map[string]string{
		"missing column": "Date,\"4 WEEKS BANK DISCOUNT\"\n10/16/2026,3.95\n",
		"bad date":       header + "2026-10-16,4.08,4.18\n",
		"no rows":        header,
		"html":           "<html><body>maintenance</body></html>",
	} {
		if _, err := parseTreasuryBillCSV([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFetchRatesFromStub(t *testing.T) {
	exr := readStableFixture(t, "ecb_exr_usd.json")
	estr := readStableFixture(t, "ecb_estr.json")
	csv := readStableFixture(t, "treasury_bill_rates.csv")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ecb/EXR/D.USD.EUR.SP00.A"):
			w.Write(exr)
		case strings.HasPrefix(r.URL.Path, "/ecb/EST/B.EU000A2X2A25.WT"):
			w.Write(estr)
		case strings.HasPrefix(r.URL.Path, "/ust/2026/all"):
			if r.URL.Query().Get("type") != "daily_treasury_bill_rates" || r.URL.Query().Get("_format") != "csv" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write(csv)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	ep := stableEndpoints{ECBBase: srv.URL + "/ecb", TreasuryBase: srv.URL + "/ust"}
	if obs, code := fetchECBEURUSD(ctx, ep, 10); code != "" || len(obs) != 3 {
		t.Errorf("EURUSD: %v %q", obs, code)
	}
	if obs, code := fetchECBESTR(ctx, ep, 10); code != "" || len(obs) != 3 {
		t.Errorf("ESTR: %v %q", obs, code)
	}
	if obs, code := fetchTreasury13W(ctx, ep, 2026); code != "" || len(obs) != 5 {
		t.Errorf("Treasury: %v %q", obs, code)
	}
	if _, code := fetchTreasury13W(ctx, ep, 2025); code != "not_found" {
		t.Errorf("Treasury 2025 on the stub: code %q, want not_found", code)
	}
}
