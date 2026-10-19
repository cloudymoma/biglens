package main

import (
	"math"
	"testing"
)

func TestIndicatorSMAReturnsDrawdownPercentile(t *testing.T) {
	if got := smaSeries([]float64{1, 2}, 3); got != nil {
		t.Errorf("smaSeries short = %v, want nil", got)
	}
	sma := smaSeries([]float64{10, 20, 30, 40}, 3)
	if len(sma) != 4 || sma[0] != nil || sma[1] != nil || *sma[2] != 20 || *sma[3] != 30 {
		t.Errorf("smaSeries = %v", sma)
	}

	ret := returnsPct([]float64{100, 110, 99}, 1)
	if len(ret) != 3 || ret[0] != nil || math.Abs(*ret[1]-10) > 1e-9 || math.Abs(*ret[2]+10) > 1e-9 {
		t.Errorf("returnsPct = %v", ret)
	}

	// Constant daily growth factor -> log returns are identical -> stdev = 0 -> RV = 0.
	constGrowth := make([]float64, 35)
	constGrowth[0] = 100
	for i := 1; i < len(constGrowth); i++ {
		constGrowth[i] = constGrowth[i-1] * 1.01
	}
	rv := realizedVol(constGrowth, 30)
	if len(rv) != 35 || rv[29] != nil || rv[30] == nil || math.Abs(*rv[34]) > 1e-9 {
		t.Fatalf("realizedVol constant growth = %v", rv[34])
	}

	// Non-constant alternating log returns (+0.02, -0.02 over 30 days):
	// mean = 0, population stdev = 0.02, annualized RV = 0.02 * sqrt(365) * 100 = 38.209946349...
	altCloses := make([]float64, 31)
	altCloses[0] = 100
	for i := 1; i < len(altCloses); i++ {
		r := 0.02
		if i%2 == 0 {
			r = -0.02
		}
		altCloses[i] = altCloses[i-1] * math.Exp(r)
	}
	altRV := realizedVol(altCloses, 30)
	wantRV := 0.02 * math.Sqrt(365.0) * 100.0
	if len(altRV) != 31 || altRV[29] != nil || altRV[30] == nil || math.Abs(*altRV[30]-wantRV) > 1e-9 {
		t.Fatalf("realizedVol alternating = %v, want %.9f", altRV[30], wantRV)
	}

	dd := drawdownSeries([]float64{1, 2, 1})
	if len(dd) != 3 || dd[0] != 0 || dd[1] != 0 || math.Abs(dd[2]+50) > 1e-9 {
		t.Errorf("drawdownSeries([1,2,1]) = %v, want [0, 0, -50]", dd)
	}

	pctl := percentileRank([]float64{10, 20, 30, 40, 50}, 30)
	if math.Abs(pctl-60) > 1e-9 {
		t.Errorf("percentileRank = %v, want 60", pctl)
	}
}

func TestIndicatorMayerWMA200HalvingPuell(t *testing.T) {
	closes := make([]float64, 205)
	for i := range closes {
		closes[i] = 100
	}
	closes[204] = 200
	mayer := mayerSeries(closes)
	if mayer[198] != nil || mayer[199] == nil || math.Abs(*mayer[199]-1.0) > 1e-9 {
		t.Errorf("mayer[199] = %v, want 1.0", mayer[199])
	}
	wantMayerSpike := 200.0 / 100.5 // 199 days of 100 + 1 day of 200 -> SMA200 = 100.5
	if mayer[204] == nil || math.Abs(*mayer[204]-wantMayerSpike) > 1e-9 {
		t.Errorf("mayer[204] = %v, want %.9f (200 / 100.5)", mayer[204], wantMayerSpike)
	}
	if wma := wma200Series(closes); wma != nil {
		t.Errorf("wma200Series on 205 days = %v, want nil (<1400)", wma)
	}

	dates := []string{"2024-04-20", "2024-04-21", "2026-10-16"}
	overlay := halvingOverlay(dates, []float64{64000, 65280, 82064})
	if len(overlay) == 0 {
		t.Fatal("expected halving overlay cycle")
	}
	last := overlay[len(overlay)-1]
	if last.Epoch != 4 || len(last.Points) != 3 {
		t.Fatalf("epoch 4 overlay = %+v", last)
	}
	if last.Points[0].Day != 0 || math.Abs(last.Points[0].Indexed-1.0) > 1e-9 {
		t.Errorf("day 0 point = %+v", last.Points[0])
	}
	if last.Points[2].Day != 909 {
		t.Errorf("2026-10-16 cycle day = %d, want 909", last.Points[2].Day)
	}

	iss := make([]float64, 366)
	for i := range iss {
		iss[i] = 1000
	}
	iss[365] = 1500
	puell := puellSeries(iss)
	if puell[363] != nil || puell[364] == nil || math.Abs(*puell[364]-1.0) > 1e-9 {
		t.Errorf("puell[364] = %v, want 1.0", puell[364])
	}
	wantPuellSpike := 1500.0 / (365500.0 / 365.0) // 364 days of 1000 + 1 day of 1500
	if puell[365] == nil || math.Abs(*puell[365]-wantPuellSpike) > 1e-9 {
		t.Errorf("puell[365] = %v, want %.9f (1500 / (365500/365))", puell[365], wantPuellSpike)
	}
}

func TestHashpriceImpliedHashrateAndRibbon(t *testing.T) {
	spot := 82000.0
	btcPH, usdPH := hashpricePerPH(3.145, 132_716_002_350_731.3, &spot)
	if math.Abs(btcPH-0.0004767) > 1e-6 {
		t.Errorf("btcPerPHDay = %.7f, want ~0.0004767", btcPH)
	}
	if usdPH == nil || math.Abs(*usdPH-39.09) > 0.02 {
		t.Errorf("usdPerPHDay = %v, want ~39.09", usdPH)
	}

	ehs := impliedHashrateEHs(132_716_002_350_731.3, 144)
	if math.Abs(ehs-950.02) > 0.01 {
		t.Errorf("impliedHashrateEHs = %.4f, want 950.02", ehs)
	}

	// Build a 70-day series where SMA30 was below SMA60 and crosses up in the last 5 days -> "recovery".
	hr := make([]float64, 70)
	for i := 0; i < 40; i++ {
		hr[i] = 600
	}
	for i := 40; i < 55; i++ {
		hr[i] = 450
	}
	for i := 55; i < 70; i++ {
		hr[i] = 750
	}
	rib := ribbonState(hr)
	if rib.State != "recovery" {
		t.Errorf("ribbonState = %+v, want recovery", rib)
	}
}

func TestPoolConcentration(t *testing.T) {
	shares := map[string]int64{
		"PoolA":   30,
		"PoolB":   25,
		"PoolC":   15,
		"PoolD":   10,
		"PoolE":   8,
		"PoolF":   7,
		"Unknown": 5,
	}
	if got := nakamoto(shares, "Unknown"); got != 2 {
		t.Errorf("nakamoto = %d, want 2", got)
	}
	if got := hhi(shares); math.Abs(got-1963) > 1e-6 {
		t.Errorf("hhi = %v, want 1963", got)
	}
}

func TestFundingAndBasisAPR(t *testing.T) {
	cases := []struct {
		rate, hours, want float64
	}{
		{9.527e-5, 8, 10.43},
		{1.2804508e-5, 1, 11.22},
		{1.25e-5, 1, 10.95},
		{5.447e-5, 8, 5.96},
	}
	for _, tc := range cases {
		got := fundingAPR(tc.rate, tc.hours)
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("fundingAPR(%g, %g) = %.4f, want %.2f", tc.rate, tc.hours, got, tc.want)
		}
	}

	// Spot 82,000, future 82,910, 71 days to expiry -> (82910/82000 - 1) * 365 / 71 * 100 = 5.7047%
	b := basisAPR(82910, 82000, 71)
	if math.Abs(b-5.70) > 0.01 {
		t.Errorf("basisAPR = %.4f, want 5.70", b)
	}
}
