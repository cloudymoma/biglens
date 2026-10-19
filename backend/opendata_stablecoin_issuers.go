package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Issuer clients (stablecoin.md §4.1, §5.1): Circle /v1/stablecoins for USDC
// and EURC, and Tether's transparency JSON for USDT. Both endpoints are
// undocumented, so every field is shape-checked before use.

// stableNum is an amount that upstreams send either as a JSON number or as a
// decimal string (Tether mixes both in one document). null is "absent".
type stableNum struct {
	V   float64
	Set bool
}

func (n *stableNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*n = stableNum{}
		return nil
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return fmt.Errorf("not a number: %s", b)
	}
	*n = stableNum{V: v, Set: true}
	return nil
}

func (n stableNum) ptr() *float64 {
	if !n.Set {
		return nil
	}
	v := n.V
	return &v
}

// --- Circle ---

// circleWantedCoins are the Circle coins in the core set.
var circleWantedCoins = []string{"USDC", "EURC"}

type circleSnapshot struct {
	FetchedAt time.Time
	Coins     map[string]circleCoin // by symbol
	Missing   []string              // wanted coins absent from the response
}

type circleCoin struct {
	Symbol      string
	TotalNative *float64
	Chains      []circleChain
	UpdatedAt   time.Time // newest chain updateDate
}

type circleChain struct {
	Chain        string // Circle chain code (ETH, SOL, BASE, …)
	AmountNative float64
	UpdatedAt    time.Time
}

// parseCircleStablecoins parses /v1/stablecoins. A wanted coin with any
// malformed chain row is rejected; the response fails when no wanted coin is
// usable.
func parseCircleStablecoins(body []byte, fetchedAt time.Time) (circleSnapshot, error) {
	var raw struct {
		Data []struct {
			Symbol      string    `json:"symbol"`
			TotalAmount stableNum `json:"totalAmount"`
			Chains      []struct {
				Chain      string    `json:"chain"`
				Amount     stableNum `json:"amount"`
				UpdateDate string    `json:"updateDate"`
			} `json:"chains"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return circleSnapshot{}, fmt.Errorf("decode: %w", err)
	}
	if raw.Data == nil {
		return circleSnapshot{}, errors.New("no data array")
	}
	out := circleSnapshot{FetchedAt: fetchedAt, Coins: map[string]circleCoin{}}
	for _, want := range circleWantedCoins {
		found := false
		for _, d := range raw.Data {
			if d.Symbol != want {
				continue
			}
			found = true
			if len(d.Chains) == 0 {
				return circleSnapshot{}, fmt.Errorf("%s: no chains", want)
			}
			coin := circleCoin{Symbol: want, TotalNative: d.TotalAmount.ptr()}
			for _, c := range d.Chains {
				if c.Chain == "" || !c.Amount.Set || c.Amount.V < 0 {
					return circleSnapshot{}, fmt.Errorf("%s: malformed chain row %q", want, c.Chain)
				}
				at, err := time.Parse(time.RFC3339Nano, c.UpdateDate)
				if err != nil {
					return circleSnapshot{}, fmt.Errorf("%s/%s: bad updateDate", want, c.Chain)
				}
				at = at.UTC()
				coin.Chains = append(coin.Chains, circleChain{Chain: c.Chain, AmountNative: c.Amount.V, UpdatedAt: at})
				if at.After(coin.UpdatedAt) {
					coin.UpdatedAt = at
				}
			}
			sort.Slice(coin.Chains, func(i, j int) bool { return coin.Chains[i].Chain < coin.Chains[j].Chain })
			out.Coins[want] = coin
			break
		}
		if !found {
			out.Missing = append(out.Missing, want)
		}
	}
	if len(out.Coins) == 0 {
		return circleSnapshot{}, errors.New("no wanted coin in response")
	}
	return out, nil
}

func fetchCircleStablecoins(ctx context.Context, ep stableEndpoints, now time.Time) (circleSnapshot, string) {
	body, code := stableFetch(ctx, ep.CircleURL, stableTimeoutSmall, stableMaxBodySmall)
	if code != "" {
		return circleSnapshot{}, code
	}
	snap, err := parseCircleStablecoins(body, now)
	if err != nil {
		return circleSnapshot{}, "bad_response"
	}
	return snap, ""
}

// --- Tether ---

// errTetherSumMismatch: Σ per-chain circulating no longer equals
// total_liabilities, so the per-chain formula (§5.1) cannot be trusted.
var errTetherSumMismatch = errors.New("per-chain circulating does not sum to total_liabilities")

// tetherSumTolerance is the relative tolerance of the §5.1 sum check (0.01%).
const tetherSumTolerance = 1e-4

type tetherSnapshot struct {
	UpdateID             int64     // Tether "id": the update time, unix seconds
	UpdatedAt            time.Time // UpdateID as a time
	FetchedAt            time.Time
	TotalAssetsUSD       *float64
	TotalLiabilitiesUSD  *float64
	ShareholderEquityUSD *float64
	Chains               []tetherChain
	CirculatingSumNative float64
}

type tetherChain struct {
	Name                  string // Tether chain name (Ethereum, Tron, Solana, …)
	TotalAuthorizedNative float64
	NotIssuedNative       float64
	QuarantinedNative     float64
	CirculatingNative     float64 // totalAuthorized − notIssued − quarantined
}

// parseTetherTransparency parses data_formatted's usdt entry. data.usdt also
// carries legacy chains and a different total, so it is not used (§4.1).
func parseTetherTransparency(body []byte, fetchedAt time.Time) (tetherSnapshot, error) {
	var raw struct {
		DataFormatted []struct {
			ID               stableNum `json:"id"`
			ISO              string    `json:"iso"`
			TotalAssets      stableNum `json:"total_assets"`
			TotalLiabilities stableNum `json:"total_liabilities"`
			ShareholderEq    stableNum `json:"shareholder_eq"`
			BlockChains      []struct {
				Name            string    `json:"name"`
				TotalAuthorized stableNum `json:"totalAuthorized"`
				NotIssued       stableNum `json:"notIssued"`
				Quarantined     stableNum `json:"quarantined"`
			} `json:"blockChains"`
		} `json:"data_formatted"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return tetherSnapshot{}, fmt.Errorf("decode: %w", err)
	}
	for _, e := range raw.DataFormatted {
		if e.ISO != "usdt" {
			continue
		}
		if !e.ID.Set || e.ID.V <= 0 || e.ID.V != math.Trunc(e.ID.V) {
			return tetherSnapshot{}, errors.New("usdt: bad update id")
		}
		if !e.TotalLiabilities.Set || !(e.TotalLiabilities.V > 0) {
			return tetherSnapshot{}, errors.New("usdt: total_liabilities missing")
		}
		if len(e.BlockChains) == 0 {
			return tetherSnapshot{}, errors.New("usdt: no blockChains")
		}
		out := tetherSnapshot{
			UpdateID: int64(e.ID.V), UpdatedAt: time.Unix(int64(e.ID.V), 0).UTC(), FetchedAt: fetchedAt,
			TotalAssetsUSD: e.TotalAssets.ptr(), TotalLiabilitiesUSD: e.TotalLiabilities.ptr(), ShareholderEquityUSD: e.ShareholderEq.ptr(),
		}
		for _, c := range e.BlockChains {
			if c.Name == "" || !c.TotalAuthorized.Set || !c.NotIssued.Set || !c.Quarantined.Set {
				return tetherSnapshot{}, fmt.Errorf("usdt: malformed chain %q", c.Name)
			}
			ch := tetherChain{Name: c.Name, TotalAuthorizedNative: c.TotalAuthorized.V, NotIssuedNative: c.NotIssued.V,
				QuarantinedNative: c.Quarantined.V}
			ch.CirculatingNative = ch.TotalAuthorizedNative - ch.NotIssuedNative - ch.QuarantinedNative
			out.CirculatingSumNative += ch.CirculatingNative
			out.Chains = append(out.Chains, ch)
		}
		if math.Abs(out.CirculatingSumNative-e.TotalLiabilities.V)/e.TotalLiabilities.V > tetherSumTolerance {
			return tetherSnapshot{}, fmt.Errorf("%w: %.2f vs %.2f", errTetherSumMismatch, out.CirculatingSumNative, e.TotalLiabilities.V)
		}
		return out, nil
	}
	return tetherSnapshot{}, errors.New("no usdt entry in data_formatted")
}

func fetchTetherTransparency(ctx context.Context, ep stableEndpoints, now time.Time) (tetherSnapshot, string) {
	body, code := stableFetch(ctx, ep.TetherURL, stableTimeoutSmall, stableMaxBodySmall)
	if code != "" {
		return tetherSnapshot{}, code
	}
	snap, err := parseTetherTransparency(body, now)
	if errors.Is(err, errTetherSumMismatch) {
		return tetherSnapshot{}, "shape_drift"
	}
	if err != nil {
		return tetherSnapshot{}, "bad_response"
	}
	return snap, ""
}
