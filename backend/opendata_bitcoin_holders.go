package main

import (
	"fmt"
	"maps"

	"cloud.google.com/go/civil"
)

const btcHolderSchemaVersion = 1

// utxoState maps creation day ("YYYY-MM-DD") to unspent satoshis.
type utxoState map[string]int64

func (u utxoState) clone() utxoState {
	out := make(utxoState, len(u))
	maps.Copy(out, u)
	return out
}

func (u utxoState) totalSats() int64 {
	var sum int64
	for _, v := range u {
		sum += v
	}
	return sum
}

// applyDay updates UTXO state u in-place for day t.
// Forward (t):  U <- U - S_t + C_t * e_t
// Backward (t): U <- U + S_t - C_t * e_t
func applyDay(u utxoState, day string, spent map[string]int64, createdSats int64, forward bool) {
	if forward {
		for c, s := range spent {
			if s == 0 {
				continue
			}
			u[c] -= s
			if u[c] == 0 {
				delete(u, c)
			}
		}
		if createdSats != 0 {
			u[day] += createdSats
			if u[day] == 0 {
				delete(u, day)
			}
		}
		return
	}
	if createdSats != 0 {
		u[day] -= createdSats
		if u[day] == 0 {
			delete(u, day)
		}
	}
	for c, s := range spent {
		if s == 0 {
			continue
		}
		u[c] += s
		if u[c] == 0 {
			delete(u, c)
		}
	}
}

// initFromBaseline constructs the initial UTXO state at T0 - 1 day from the
// baseline (which covers blocks with timestamp < T0) and subtracts cross-boundary
// outputs created in a block dated >= T0 that were already spent in a block dated
// < T0 (U_init = baseline - sum_{c >= T0} S_{T0-1}[c] * e_c).
func initFromBaseline(baseline map[string]int64, t0 string, crossBoundary map[string]int64) utxoState {
	u := make(utxoState, len(baseline))
	for c, sats := range baseline {
		if sats != 0 {
			u[c] = sats
		}
	}
	for c, sats := range crossBoundary {
		if c >= t0 && sats != 0 {
			u[c] -= sats
			if u[c] == 0 {
				delete(u, c)
			}
		}
	}
	return u
}

// checkSupplyIdentity verifies the exact satoshi conservation identity:
// sum(U_t) - sum(U_{t-1}) = coinbase_sats_t - fees_sats_t - opret_value_sats_t.
func checkSupplyIdentity(prevSum, nextSum, coinbase, fees, opret int64) error {
	actualDelta := nextSum - prevSum
	expectedDelta := coinbase - fees - opret
	if actualDelta != expectedDelta {
		return fmt.Errorf("supply_identity_mismatch: delta_u=%d want=%d (coinbase=%d fees=%d opret=%d)",
			actualDelta, expectedDelta, coinbase, fees, opret)
	}
	return nil
}

// checkIssuanceBound verifies that claimed issuance (coinbase_sats - fees_sats)
// does not exceed scheduled issuance (sum of per-block subsidies). An underclaim
// (claimed < scheduled) is returned as a non-fatal warning.
func checkIssuanceBound(claimed, scheduled int64) (string, error) {
	if claimed > scheduled {
		return "", fmt.Errorf("issuance_bound_exceeded: claimed=%d > scheduled=%d", claimed, scheduled)
	}
	if claimed < scheduled {
		return fmt.Sprintf("underclaimed_issuance: claimed=%d < scheduled=%d sats", claimed, scheduled), nil
	}
	return "", nil
}

// checkBucketSigns verifies that after applying day t:
//   - every bucket c <= t has u[c] >= 0
//   - every bucket c > t + 1 has u[c] == 0
//
// Bucket c == t + 1 may be negative transiently on day t when a midnight
// timestamp inversion causes a block dated t to spend an output created in a
// parent block dated t + 1.
func checkBucketSigns(u utxoState, day string) error {
	d, err := civil.ParseDate(day)
	if err != nil {
		return fmt.Errorf("invalid day %q: %w", day, err)
	}
	plusOne := d.AddDays(1).String()
	for c, sats := range u {
		if sats == 0 {
			continue
		}
		if c <= day && sats < 0 {
			return fmt.Errorf("negative_bucket: bucket %s has %d sats after day %s", c, sats, day)
		}
		if c > plusOne {
			return fmt.Errorf("future_bucket_exceeded: bucket %s has %d sats after day %s (> %s)", c, sats, day, plusOne)
		}
	}
	return nil
}

// holderMetrics computes a day's holder metrics (supply, LTH/STH supply, HODL
// waves, and price-gated realized cap, realized price, LTH/STH realized prices,
// MVRV, and supply in profit) from UTXO state u.
func holderMetrics(day string, gen int64, u utxoState, prices map[string]float64, priceComplete bool) BtcHolderDay {
	d, _ := civil.ParseDate(day)
	bandSats := make(map[string]int64, len(btcAgeBands))
	var (
		supplySats int64
		lthSats    int64
		sthSats    int64
	)

	for cDay, sats := range u {
		if sats == 0 {
			continue
		}
		supplySats += sats
		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = d.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		if age >= 155 {
			lthSats += sats
		} else {
			sthSats += sats
		}
		bandSats[btcAgeBand(age)] += sats
	}

	waves := make(map[string]float64, len(btcAgeBands))
	for _, b := range btcAgeBands {
		if supplySats > 0 {
			waves[b] = float64(bandSats[b]) / float64(supplySats) * 100.0
		} else {
			waves[b] = 0
		}
	}

	supplyBTC := float64(supplySats) / 1e8
	out := BtcHolderDay{
		Date:               day,
		BaselineGeneration: gen,
		SupplySats:         supplySats,
		SupplyBTC:          supplyBTC,
		LTHSupplySats:      lthSats,
		STHSupplySats:      sthSats,
		HODLWavesPct:       waves,
	}

	closeT := prices[day]
	if !priceComplete || closeT <= 0 {
		out.Blocked = "price_history_incomplete"
		return out
	}

	var (
		realizedCapUSD float64
		lthCapUSD      float64
		sthCapUSD      float64
		profitSats     int64
	)
	for cDay, sats := range u {
		if sats == 0 {
			continue
		}
		// Coins created before earliest priced day (2010-08-18) have pc == 0 per §6.4.
		pc := prices[cDay]
		btc := float64(sats) / 1e8
		valUSD := btc * pc
		realizedCapUSD += valUSD

		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = d.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		if age >= 155 {
			lthCapUSD += valUSD
		} else {
			sthCapUSD += valUSD
		}
		if pc < closeT {
			profitSats += sats
		}
	}

	out.RealizedCapUSD = &realizedCapUSD
	if supplyBTC > 0 {
		rp := realizedCapUSD / supplyBTC
		out.RealizedPriceUSD = &rp
	}
	if lthSats > 0 {
		lthRP := lthCapUSD / (float64(lthSats) / 1e8)
		out.LTHRealizedPrice = &lthRP
	}
	if sthSats > 0 {
		sthRP := sthCapUSD / (float64(sthSats) / 1e8)
		out.STHRealizedPrice = &sthRP
	}
	if realizedCapUSD > 0 && supplyBTC > 0 {
		mvrv := (supplyBTC * closeT) / realizedCapUSD
		out.MVRV = &mvrv
	}
	if supplySats > 0 {
		profPct := float64(profitSats) / float64(supplySats) * 100.0
		out.SupplyInProfitPct = &profPct
	}
	return out
}
