package main

import (
	"math"
	"sort"
	"time"
)

// Consensus price (stablecoin.md §5.2–5.3). Venue quotes become consensus
// inputs (direct, same-venue derived, cross-derived), pass the exclusion
// filters, and are combined as a weighted median with every venue's group
// of inputs capped at 50% of the total weight. A consensus needs inputs from
// at least 2 distinct venues; with exactly 2 it is the mean of the two venue
// prices.

const (
	stablePegMaxAge       = 300 * time.Second
	stablePegMaxSpreadBps = 50.0
	stablePegMinVolUSD    = 50_000.0
	stableVenueCap        = 0.5
)

// StableQuote is one market or consensus input with its filter outcome.
type StableQuote struct {
	Venue     string   `json:"venue"`
	Pair      string   `json:"pair"`
	Numeraire string   `json:"numeraire"` // unit of the prices: USD | EUR | USDT
	Kind      string   `json:"kind"`      // market | direct | derived (same venue) | cross (cross-venue) | inverse | dex
	Legs      []string `json:"legs,omitempty"`
	Bid       *float64 `json:"bid"`
	Ask       *float64 `json:"ask"`
	Mid       *float64 `json:"mid"`
	SpreadBps *float64 `json:"spread_bps"`
	VolUSD    *float64 `json:"vol_usd"` // 24h volume in USD; a derived input's is its smaller leg's
	// AgeSeconds counts from the venue's own quote time (fetch time when the
	// venue gives none) to the sample.
	AgeSeconds *int64   `json:"age_seconds"`
	Weight     *float64 `json:"weight"` // share of the consensus weight after the venue cap (0–1)
	Included   bool     `json:"included"`
	// ExcludeReason: stale | wide_spread | low_volume | leg_excluded |
	// fx_unavailable | bad_response | disabled | paused | a fetch code.
	ExcludeReason string `json:"exclude_reason,omitempty"`
}

// StablePeg is one asset's consensus in its numéraire.
type StablePeg struct {
	Asset     string   `json:"asset"`
	Numeraire string   `json:"numeraire"`
	State     string   `json:"state"`     // ok | single_venue | unknown
	Consensus *float64 `json:"consensus"` // null with fewer than 2 distinct venues
	DevBps    *float64 `json:"dev_bps"`   // (consensus − 1) × 1e4
	DispBps   *float64 `json:"disp_bps"`  // (max − min of included mids) × 1e4
	// SingleVenuePrice is shown, labeled "single venue", only when the
	// consensus is null.
	SingleVenuePrice *float64 `json:"single_venue_price"`
	InputsUsed       int      `json:"inputs_used"`
	VenuesUsed       int      `json:"venues_used"`
	Venues           []string `json:"venues"`
	// EURC only: venues with an included direct EUR-quoted input, whether a
	// second such venue exists (the S1 warning cap in M4 depends on it), and
	// whether the cross input shares a venue with the FX consensus.
	DirectEURVenues      *int          `json:"direct_eur_venues,omitempty"`
	SecondDirectEURVenue *bool         `json:"second_direct_eur_venue,omitempty"`
	PartiallyDependent   *bool         `json:"partially_dependent,omitempty"`
	Quotes               []StableQuote `json:"quotes"`
	AsOf                 string        `json:"as_of"`
}

// StableRelativePeg is USDT per USDC across exchanges and Uniswap; it does
// not feed any consensus.
type StableRelativePeg struct {
	Pair           string        `json:"pair"` // USDC/USDT: price of USDC in USDT
	Median         *float64      `json:"median"`
	DevBps         *float64      `json:"dev_bps"`
	ExchangeMedian *float64      `json:"exchange_median"`
	Uniswap        *float64      `json:"uniswap"`
	InputsUsed     int           `json:"inputs_used"`
	Quotes         []StableQuote `json:"quotes"`
	AsOf           string        `json:"as_of"`
}

// StableFX is the EUR/USD consensus used for EURC (§5.3).
type StableFX struct {
	EURUSD *float64      `json:"eur_usd"` // median of the included Kraken and Bitstamp mids
	Quotes []StableQuote `json:"quotes"`
	// VolumeRate converts EUR volumes to USD for the volume filter: the
	// consensus, else the latest ECB reference rate.
	VolumeRate   *float64 `json:"volume_rate"`
	VolumeSource string   `json:"volume_source,omitempty"` // consensus | ecb
}

type stablePegInputs struct {
	Quotes    []stableVenueQuote
	Uniswap   *stableUniswapReading
	ECBEURUSD *float64 // latest ECB reference rate (volume-filter fallback only)
	Now       time.Time
}

type stablePegResult struct {
	AsOf     time.Time
	Assets   map[string]StablePeg
	Relative StableRelativePeg
	FX       StableFX
	Markets  []StableQuote // every raw market, evaluated on its own (venue table)
}

// stablePegInput is an included consensus input.
type stablePegInput struct {
	venue  string
	mid    float64
	weight float64 // vol_usd
	idx    int     // position in the asset's quote list
}

// stableEvalMarket evaluates one raw venue quote. volUSD converts the base
// volume (nil skips the volume filter, e.g. Uniswap; a function returning
// nil means the conversion is unavailable).
func stableEvalMarket(raw stableVenueQuote, kind, numeraire string, now time.Time, checkSpread bool, volUSD func(raw stableVenueQuote, mid float64) (*float64, string)) StableQuote {
	q := StableQuote{Venue: raw.Venue, Pair: raw.Pair, Numeraire: numeraire, Kind: kind}
	if raw.Code != "" {
		q.ExcludeReason = raw.Code
		return q
	}
	bid, ask := raw.Bid, raw.Ask
	mid := (bid + ask) / 2
	q.Bid, q.Ask, q.Mid = stableF(bid), stableF(ask), stableF(mid)
	ts := raw.TS
	if ts.IsZero() {
		ts = raw.FetchedAt
	}
	age := now.Sub(ts)
	secs := int64(math.Round(age.Seconds()))
	q.AgeSeconds = &secs
	reason := ""
	if checkSpread {
		q.SpreadBps = stableF((ask - bid) / mid * 1e4)
	}
	if volUSD != nil {
		v, why := volUSD(raw, mid)
		q.VolUSD = v
		if v == nil && why != "" {
			reason = why
		}
	}
	switch {
	case age > stablePegMaxAge:
		q.ExcludeReason = "stale"
	case -age > stablePegMaxAge: // a quote time in the future
		q.ExcludeReason = "bad_response"
	case checkSpread && *q.SpreadBps > stablePegMaxSpreadBps:
		q.ExcludeReason = "wide_spread"
	case reason != "":
		q.ExcludeReason = reason
	case volUSD != nil && (q.VolUSD == nil || *q.VolUSD < stablePegMinVolUSD):
		q.ExcludeReason = "low_volume"
	default:
		q.Included = true
	}
	return q
}

// stableVolUSD converts a market's 24h volume to USD. USD and USD-stable
// quote currencies count at par; EUR uses the volume rate.
func stableVolUSD(eurRate *float64) func(raw stableVenueQuote, mid float64) (*float64, string) {
	return func(raw stableVenueQuote, mid float64) (*float64, string) {
		if raw.VolQuote != nil && raw.Quote != "EUR" {
			return stableF(*raw.VolQuote), ""
		}
		if raw.VolBase == nil {
			return nil, ""
		}
		v := *raw.VolBase * mid
		if raw.Quote == "EUR" {
			if eurRate == nil {
				return nil, "fx_unavailable"
			}
			v *= *eurRate
		}
		return stableF(v), ""
	}
}

// stableDerive combines two same-venue legs into num ÷ den. The derived
// input is excluded when either leg is, weighted by the smaller leg volume,
// and its bid/ask take the unfavorable side of each leg.
func stableDerive(num, den StableQuote, pair, numeraire string) StableQuote {
	q := StableQuote{Venue: num.Venue, Pair: pair, Numeraire: numeraire, Kind: "derived",
		Legs: []string{num.Venue + "|" + num.Pair, den.Venue + "|" + den.Pair}}
	if num.Mid != nil && den.Mid != nil {
		q.Mid = stableF(*num.Mid / *den.Mid)
		q.Bid = stableF(*num.Bid / *den.Ask)
		q.Ask = stableF(*num.Ask / *den.Bid)
		q.SpreadBps = stableF((*q.Ask - *q.Bid) / *q.Mid * 1e4)
	}
	if num.VolUSD != nil && den.VolUSD != nil {
		q.VolUSD = stableF(math.Min(*num.VolUSD, *den.VolUSD))
	}
	if num.AgeSeconds != nil && den.AgeSeconds != nil {
		a := max(*num.AgeSeconds, *den.AgeSeconds)
		q.AgeSeconds = &a
	}
	switch {
	case !num.Included || !den.Included:
		q.ExcludeReason = "leg_excluded"
	case *q.SpreadBps > stablePegMaxSpreadBps:
		q.ExcludeReason = "wide_spread"
	default:
		q.Included = true
	}
	return q
}

// stableMedian is the plain median (mean of the middle two for an even count).
func stableMedian(vs []float64) float64 {
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

type stableWV struct{ v, w float64 }

// stableWeightedMedian: the value where the cumulative weight first exceeds
// half of the total; when it lands exactly on half between two values, their
// mean (§5.2 tie rule).
func stableWeightedMedian(xs []stableWV) float64 {
	s := append([]stableWV(nil), xs...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].v < s[j].v })
	var total float64
	for _, x := range s {
		total += x.w
	}
	half, eps := total/2, total*1e-9
	var cum float64
	for i, x := range s {
		cum += x.w
		if math.Abs(cum-half) <= eps && i+1 < len(s) {
			return (x.v + s[i+1].v) / 2
		}
		if cum > half {
			return x.v
		}
	}
	return s[len(s)-1].v
}

// stableAggregate combines included inputs. It returns the state, the
// consensus (≥ 2 venues), the single-venue price (1 venue), the venues and
// each input's display weight (share of the capped total).
func stableAggregate(inputs []stablePegInput) (string, *float64, *float64, []string, []float64) {
	weights := make([]float64, len(inputs))
	groups := map[string][]int{}
	var venues []string
	for i, in := range inputs {
		if _, seen := groups[in.venue]; !seen {
			venues = append(venues, in.venue)
		}
		groups[in.venue] = append(groups[in.venue], i)
	}
	sort.Strings(venues)
	groupPrice := func(v string) float64 {
		var xs []stableWV
		for _, i := range groups[v] {
			xs = append(xs, stableWV{inputs[i].mid, inputs[i].weight})
		}
		return stableWeightedMedian(xs)
	}
	shareWithin := func(v string, groupShare float64) {
		var sum float64
		for _, i := range groups[v] {
			sum += inputs[i].weight
		}
		for _, i := range groups[v] {
			weights[i] = groupShare * inputs[i].weight / sum
		}
	}
	switch len(venues) {
	case 0:
		return "unknown", nil, nil, venues, weights
	case 1:
		shareWithin(venues[0], 1)
		return "single_venue", nil, stableF(groupPrice(venues[0])), venues, weights
	case 2:
		shareWithin(venues[0], stableVenueCap)
		shareWithin(venues[1], stableVenueCap)
		return "ok", stableF((groupPrice(venues[0]) + groupPrice(venues[1])) / 2), nil, venues, weights
	}
	// ≥ 3 venues: cap the one group above 50% at the others' combined weight.
	capped := make([]float64, len(inputs))
	sums := map[string]float64{}
	var total float64
	for i, in := range inputs {
		capped[i] = in.weight
		sums[in.venue] += in.weight
		total += in.weight
	}
	for v, s := range sums {
		if others := total - s; s > others {
			for _, i := range groups[v] {
				capped[i] *= others / s
			}
		}
	}
	var xs []stableWV
	var cappedTotal float64
	for i, in := range inputs {
		xs = append(xs, stableWV{in.mid, capped[i]})
		cappedTotal += capped[i]
	}
	for i := range inputs {
		weights[i] = capped[i] / cappedTotal
	}
	return "ok", stableF(stableWeightedMedian(xs)), nil, venues, weights
}

// stableFinishPeg fills the aggregate fields of one asset from its quotes.
func stableFinishPeg(p *StablePeg) {
	var inputs []stablePegInput
	minMid, maxMid := math.Inf(1), math.Inf(-1)
	for i, q := range p.Quotes {
		if !q.Included {
			continue
		}
		w := 0.0
		if q.VolUSD != nil {
			w = *q.VolUSD
		}
		inputs = append(inputs, stablePegInput{venue: q.Venue, mid: *q.Mid, weight: w, idx: i})
		minMid, maxMid = math.Min(minMid, *q.Mid), math.Max(maxMid, *q.Mid)
	}
	state, consensus, single, venues, weights := stableAggregate(inputs)
	for k, in := range inputs {
		p.Quotes[in.idx].Weight = stableF(weights[k])
	}
	p.State, p.Consensus, p.SingleVenuePrice, p.Venues = state, consensus, single, venues
	if p.Venues == nil {
		p.Venues = []string{}
	}
	p.InputsUsed, p.VenuesUsed = len(inputs), len(venues)
	if consensus != nil {
		p.DevBps = stableF((*consensus - 1) * 1e4)
	}
	if len(inputs) >= 2 {
		p.DispBps = stableF((maxMid - minMid) * 1e4)
	}
}

// stableComputePeg runs the whole §5.2–5.3 computation on one sample.
func stableComputePeg(in stablePegInputs) stablePegResult {
	now := in.Now
	res := stablePegResult{AsOf: now, Assets: map[string]StablePeg{}}
	raw := map[string]stableVenueQuote{}
	for _, q := range in.Quotes {
		raw[q.Venue+"|"+q.Pair] = q
	}
	get := func(venue, pair string) stableVenueQuote {
		if q, ok := raw[venue+"|"+pair]; ok {
			return q
		}
		base, quote, _ := stableVenuePair(venue, pair)
		return stableVenueQuote{Venue: venue, Pair: pair, Base: base, Quote: quote, FetchedAt: now, Code: "disabled"}
	}
	usdVol := stableVolUSD(nil)

	// EUR/USD first: EURC volumes and the cross input depend on it.
	var fxMids []float64
	for _, k := range [][2]string{{"kraken", "ZEURZUSD"}, {"bitstamp", "eurusd"}} {
		q := stableEvalMarket(get(k[0], k[1]), "direct", "USD", now, true, usdVol)
		res.FX.Quotes = append(res.FX.Quotes, q)
		if q.Included {
			fxMids = append(fxMids, *q.Mid)
		}
	}
	if len(fxMids) > 0 {
		res.FX.EURUSD = stableF(stableMedian(fxMids))
		res.FX.VolumeRate, res.FX.VolumeSource = res.FX.EURUSD, "consensus"
	} else if in.ECBEURUSD != nil {
		res.FX.VolumeRate, res.FX.VolumeSource = stableF(*in.ECBEURUSD), "ecb"
	}
	vol := stableVolUSD(res.FX.VolumeRate)

	// Every raw market on its own, for the venue table.
	for _, p := range stableVenuePairs {
		res.Markets = append(res.Markets, stableEvalMarket(get(p.Venue, p.Pair), "market", p.Quote, now, true, vol))
	}
	var uni StableQuote
	if in.Uniswap != nil {
		uni = stableEvalMarket(in.Uniswap.quote(), "dex", "USDT", now, false, nil)
	} else {
		uni = StableQuote{Venue: "uniswap", Pair: stableUniswapPair, Numeraire: "USDT", Kind: "dex", ExcludeReason: "disabled"}
	}
	uniMarket := uni
	uniMarket.Kind = "market"
	res.Markets = append(res.Markets, uniMarket)

	market := func(venue, pair string) StableQuote {
		return stableEvalMarket(get(venue, pair), "direct", "", now, true, vol)
	}
	direct := func(venue, pair, numeraire string) StableQuote {
		q := market(venue, pair)
		q.Numeraire = numeraire
		return q
	}

	// USDT in USD: direct inputs only.
	usdt := StablePeg{Asset: "usdt", Numeraire: "USD", AsOf: stableFmt(now)}
	usdt.Quotes = []StableQuote{direct("coinbase", "USDT-USD", "USD"), direct("kraken", "USDTZUSD", "USD"), direct("bitstamp", "usdtusd", "USD")}
	stableFinishPeg(&usdt)
	res.Assets["usdt"] = usdt

	// USDC in USD: Kraken and Bitstamp direct; Coinbase USDT-USD ÷ USDT-USDC.
	cbUSDTUSD, cbUSDTUSDC := market("coinbase", "USDT-USD"), market("coinbase", "USDT-USDC")
	usdc := StablePeg{Asset: "usdc", Numeraire: "USD", AsOf: stableFmt(now)}
	usdc.Quotes = []StableQuote{direct("kraken", "USDCUSD", "USD"), direct("bitstamp", "usdcusd", "USD"),
		stableDerive(cbUSDTUSD, cbUSDTUSDC, "USDC-USD (USDT-USD ÷ USDT-USDC)", "USD")}
	stableFinishPeg(&usdc)
	res.Assets["usdc"] = usdc

	// EURC in EUR (§5.3): Kraken and Bitstamp direct; Kraken EURCUSD ÷
	// ZEURZUSD; Coinbase EURC-USDC × P_USDC ÷ FX.
	eurc := StablePeg{Asset: "eurc", Numeraire: "EUR", AsOf: stableFmt(now)}
	krEURCUSD, krEURUSD := market("kraken", "EURCUSD"), market("kraken", "ZEURZUSD")
	cbEURC := market("coinbase", "EURC-USDC")
	cross := StableQuote{Venue: "coinbase", Pair: "EURC-EUR (EURC-USDC × USDC consensus ÷ EUR/USD)", Numeraire: "EUR", Kind: "cross",
		Legs: []string{"coinbase|EURC-USDC", "consensus|usdc", "fx|eur_usd"}, VolUSD: cbEURC.VolUSD, AgeSeconds: cbEURC.AgeSeconds}
	if cbEURC.Mid != nil && usdc.Consensus != nil && res.FX.EURUSD != nil {
		k := *usdc.Consensus / *res.FX.EURUSD
		cross.Mid, cross.Bid, cross.Ask = stableF(*cbEURC.Mid*k), stableF(*cbEURC.Bid*k), stableF(*cbEURC.Ask*k)
		cross.SpreadBps = cbEURC.SpreadBps
	}
	if cbEURC.Included && usdc.Consensus != nil && res.FX.EURUSD != nil {
		cross.Included = true
	} else {
		cross.ExcludeReason = "leg_excluded"
	}
	eurc.Quotes = []StableQuote{direct("kraken", "EURCEUR", "EUR"), direct("bitstamp", "eurceur", "EUR"),
		stableDerive(krEURCUSD, krEURUSD, "EURC-EUR (EURCUSD ÷ ZEURZUSD)", "EUR"), cross}
	stableFinishPeg(&eurc)
	directEUR := map[string]bool{}
	for _, q := range eurc.Quotes {
		if q.Included && q.Kind == "direct" {
			directEUR[q.Venue] = true
		}
	}
	n := len(directEUR)
	second := n >= 2
	dependent := false
	if cross.Included {
		for _, fq := range res.FX.Quotes {
			for _, v := range eurc.Venues {
				if fq.Included && fq.Venue == v && v != cross.Venue {
					dependent = true
				}
			}
		}
	}
	eurc.DirectEURVenues, eurc.SecondDirectEURVenue, eurc.PartiallyDependent = &n, &second, &dependent
	res.Assets["eurc"] = eurc

	// Relative peg: USDT per USDC.
	rel := StableRelativePeg{Pair: "USDC/USDT", AsOf: stableFmt(now)}
	inv := StableQuote{Venue: "coinbase", Pair: "1/USDT-USDC", Numeraire: "USDT", Kind: "inverse", Legs: []string{"coinbase|USDT-USDC"},
		VolUSD: cbUSDTUSDC.VolUSD, AgeSeconds: cbUSDTUSDC.AgeSeconds, Included: cbUSDTUSDC.Included, ExcludeReason: cbUSDTUSDC.ExcludeReason}
	if cbUSDTUSDC.Mid != nil {
		inv.Mid, inv.Bid, inv.Ask = stableF(1 / *cbUSDTUSDC.Mid), stableF(1 / *cbUSDTUSDC.Ask), stableF(1 / *cbUSDTUSDC.Bid)
		inv.SpreadBps = stableF((*inv.Ask - *inv.Bid) / *inv.Mid * 1e4)
	}
	rel.Quotes = []StableQuote{direct("okx", "USDC-USDT", "USDT"), direct("binance", "USDCUSDT", "USDT"), inv, uni}
	var all, exchanges []float64
	for _, q := range rel.Quotes {
		if !q.Included {
			continue
		}
		all = append(all, *q.Mid)
		if q.Kind == "dex" {
			rel.Uniswap = stableF(*q.Mid)
		} else {
			exchanges = append(exchanges, *q.Mid)
		}
	}
	rel.InputsUsed = len(all)
	if len(all) > 0 {
		rel.Median = stableF(stableMedian(all))
		rel.DevBps = stableF((*rel.Median - 1) * 1e4)
	}
	if len(exchanges) > 0 {
		rel.ExchangeMedian = stableF(stableMedian(exchanges))
	}
	res.Relative = rel
	return res
}
