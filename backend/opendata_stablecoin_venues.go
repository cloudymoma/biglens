package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Exchange adapters for the peg sampler (stablecoin.md §4.2): Coinbase,
// Kraken, Bitstamp, OKX and Binance spot tickers. Every response is
// shape-checked; a quote that fails the check carries bad_response and is
// never used. Prices are read from the book (bid/ask).

// stableVenuePairs is every venue market the peg sampler reads.
var stableVenuePairs = []struct{ Venue, Pair, Base, Quote string }{
	{"coinbase", "USDT-USD", "USDT", "USD"},
	{"coinbase", "USDT-USDC", "USDT", "USDC"},
	{"coinbase", "EURC-USDC", "EURC", "USDC"},
	{"kraken", "USDTZUSD", "USDT", "USD"},
	{"kraken", "USDCUSD", "USDC", "USD"},
	{"kraken", "EURCEUR", "EURC", "EUR"},
	{"kraken", "EURCUSD", "EURC", "USD"},
	{"kraken", "ZEURZUSD", "EUR", "USD"},
	{"bitstamp", "usdtusd", "USDT", "USD"},
	{"bitstamp", "usdcusd", "USDC", "USD"},
	{"bitstamp", "eurceur", "EURC", "EUR"},
	{"bitstamp", "eurusd", "EUR", "USD"},
	{"okx", "USDC-USDT", "USDC", "USDT"},
	{"binance", "USDCUSDT", "USDC", "USDT"},
}

// stableKrakenRequest is the one batched Kraken call; Kraken renames some
// result keys (USDTUSD → USDTZUSD, EURUSD → ZEURZUSD), so results are mapped
// by the returned key.
const stableKrakenRequest = "USDTUSD,USDCUSD,EURCEUR,EURCUSD,EURUSD"

// stableBinancePause: HTTP 451 or 403 from Binance pauses the provider for
// 24 h (geo-blocking, mirroring the Bitcoin dashboard).
const stableBinancePause = 24 * time.Hour

// stableVenueQuote is one ticker as read from a venue, before any filter.
type stableVenueQuote struct {
	Venue, Pair string
	Base, Quote string
	Bid, Ask    float64
	Last        *float64
	VolBase     *float64  // 24h volume in base units
	VolQuote    *float64  // 24h volume in quote units, when the venue reports it
	TS          time.Time // the venue's own ticker time (Bitstamp, OKX, Binance); zero when it gives none
	// LastTrade is Coinbase's ticker time: its last trade, not a book time.
	LastTrade time.Time
	FetchedAt time.Time
	// Code is "" for a usable quote, else why not: a stableFetch code,
	// bad_response (shape check), disabled (provider off or product not
	// listed) or paused (Binance after a geo-block).
	Code string
}

func stableVenuePair(venue, pair string) (base, quote string, ok bool) {
	for _, p := range stableVenuePairs {
		if p.Venue == venue && p.Pair == pair {
			return p.Base, p.Quote, true
		}
	}
	return "", "", false
}

// stableBook validates a bid/ask pair: both positive, not crossed.
func stableBook(bid, ask stableNum) (float64, float64, error) {
	if !bid.Set || !ask.Set || !(bid.V > 0) || !(ask.V > 0) {
		return 0, 0, errors.New("missing or non-positive bid/ask")
	}
	if ask.V < bid.V {
		return 0, 0, errors.New("crossed book")
	}
	return bid.V, ask.V, nil
}

// decodeStableObject decodes a JSON object (a list or scalar is rejected:
// Bitstamp answers an unknown pair with the full ticker list).
func decodeStableObject(body []byte, v any) error {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "{") {
		return errors.New("not a JSON object")
	}
	return json.Unmarshal(body, v)
}

func newStableQuote(venue, pair string, at time.Time) (stableVenueQuote, error) {
	base, quote, ok := stableVenuePair(venue, pair)
	if !ok {
		return stableVenueQuote{}, fmt.Errorf("unknown %s pair %q", venue, pair)
	}
	return stableVenueQuote{Venue: venue, Pair: pair, Base: base, Quote: quote, FetchedAt: at}, nil
}

// --- parsers ---

func parseCoinbaseTicker(body []byte, product string, at time.Time) (stableVenueQuote, error) {
	q, err := newStableQuote("coinbase", product, at)
	if err != nil {
		return q, err
	}
	var raw struct {
		Bid    stableNum `json:"bid"`
		Ask    stableNum `json:"ask"`
		Price  stableNum `json:"price"`  // last trade
		Volume stableNum `json:"volume"` // base units, 24 h
		Time   string    `json:"time"`   // last trade time
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return q, err
	}
	if q.Bid, q.Ask, err = stableBook(raw.Bid, raw.Ask); err != nil {
		return q, err
	}
	if !raw.Volume.Set || raw.Volume.V < 0 {
		return q, errors.New("missing volume")
	}
	ts, err := time.Parse(time.RFC3339Nano, raw.Time)
	if err != nil {
		return q, errors.New("bad time")
	}
	q.Last, q.VolBase, q.LastTrade = raw.Price.ptr(), raw.Volume.ptr(), ts.UTC()
	return q, nil
}

// parseKrakenTicker returns one quote per Kraken pair in stableVenuePairs;
// a pair missing from the result or malformed carries bad_response. The
// whole response fails on a non-empty error list or an empty result.
func parseKrakenTicker(body []byte, at time.Time) ([]stableVenueQuote, error) {
	var raw struct {
		Error  []string                   `json:"error"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Error) > 0 {
		return nil, errors.New("kraken error list")
	}
	if len(raw.Result) == 0 {
		return nil, errors.New("empty result")
	}
	var out []stableVenueQuote
	for _, p := range stableVenuePairs {
		if p.Venue != "kraken" {
			continue
		}
		q, _ := newStableQuote("kraken", p.Pair, at)
		entry, ok := raw.Result[p.Pair]
		if !ok {
			q.Code = "bad_response"
			out = append(out, q)
			continue
		}
		var t struct {
			A []stableNum `json:"a"` // [price, whole lot volume, lot volume]
			B []stableNum `json:"b"`
			C []stableNum `json:"c"` // [last trade price, lot volume]
			V []stableNum `json:"v"` // [today, last 24 h]
		}
		var err error
		if json.Unmarshal(entry, &t) != nil || len(t.A) == 0 || len(t.B) == 0 || len(t.V) < 2 || !t.V[1].Set || t.V[1].V < 0 {
			err = errors.New("malformed pair")
		} else {
			q.Bid, q.Ask, err = stableBook(t.B[0], t.A[0])
		}
		if err != nil {
			q.Code, q.Bid, q.Ask = "bad_response", 0, 0
			out = append(out, q)
			continue
		}
		if len(t.C) > 0 {
			q.Last = t.C[0].ptr()
		}
		q.VolBase = t.V[1].ptr()
		out = append(out, q)
	}
	return out, nil
}

func parseBitstampTicker(body []byte, pair string, at time.Time) (stableVenueQuote, error) {
	q, err := newStableQuote("bitstamp", pair, at)
	if err != nil {
		return q, err
	}
	var raw struct {
		Timestamp stableNum `json:"timestamp"` // unix seconds
		Bid       stableNum `json:"bid"`
		Ask       stableNum `json:"ask"`
		Last      stableNum `json:"last"`
		Volume    stableNum `json:"volume"` // base units
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return q, err
	}
	if q.Bid, q.Ask, err = stableBook(raw.Bid, raw.Ask); err != nil {
		return q, err
	}
	if !raw.Timestamp.Set || raw.Timestamp.V <= 0 || !raw.Volume.Set || raw.Volume.V < 0 {
		return q, errors.New("missing timestamp or volume")
	}
	q.Last, q.VolBase, q.TS = raw.Last.ptr(), raw.Volume.ptr(), time.Unix(int64(raw.Timestamp.V), 0).UTC()
	return q, nil
}

func parseOKXTicker(body []byte, inst string, at time.Time) (stableVenueQuote, error) {
	q, err := newStableQuote("okx", inst, at)
	if err != nil {
		return q, err
	}
	var raw struct {
		Code string `json:"code"`
		Data []struct {
			InstID    string    `json:"instId"`
			Last      stableNum `json:"last"`
			AskPx     stableNum `json:"askPx"`
			BidPx     stableNum `json:"bidPx"`
			VolCcy24h stableNum `json:"volCcy24h"` // SPOT: quote currency
			Vol24h    stableNum `json:"vol24h"`    // base currency
			TS        stableNum `json:"ts"`        // ms
		} `json:"data"`
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return q, err
	}
	if raw.Code != "0" || len(raw.Data) != 1 || raw.Data[0].InstID != inst {
		return q, errors.New("unexpected envelope")
	}
	d := raw.Data[0]
	if q.Bid, q.Ask, err = stableBook(d.BidPx, d.AskPx); err != nil {
		return q, err
	}
	if !d.TS.Set || d.TS.V <= 0 || !d.VolCcy24h.Set || d.VolCcy24h.V < 0 {
		return q, errors.New("missing ts or volume")
	}
	q.Last, q.VolBase, q.VolQuote, q.TS = d.Last.ptr(), d.Vol24h.ptr(), d.VolCcy24h.ptr(), time.UnixMilli(int64(d.TS.V)).UTC()
	return q, nil
}

func parseBinanceTicker(body []byte, symbol string, at time.Time) (stableVenueQuote, error) {
	q, err := newStableQuote("binance", symbol, at)
	if err != nil {
		return q, err
	}
	var raw struct {
		Symbol      string    `json:"symbol"`
		BidPrice    stableNum `json:"bidPrice"`
		AskPrice    stableNum `json:"askPrice"`
		LastPrice   stableNum `json:"lastPrice"`
		Volume      stableNum `json:"volume"`
		QuoteVolume stableNum `json:"quoteVolume"`
		CloseTime   stableNum `json:"closeTime"` // ms
	}
	if err := decodeStableObject(body, &raw); err != nil {
		return q, err
	}
	if raw.Symbol != symbol {
		return q, errors.New("unexpected symbol")
	}
	if q.Bid, q.Ask, err = stableBook(raw.BidPrice, raw.AskPrice); err != nil {
		return q, err
	}
	if !raw.CloseTime.Set || raw.CloseTime.V <= 0 || !raw.QuoteVolume.Set || raw.QuoteVolume.V < 0 {
		return q, errors.New("missing closeTime or quoteVolume")
	}
	q.Last, q.VolBase, q.VolQuote, q.TS = raw.LastPrice.ptr(), raw.Volume.ptr(), raw.QuoteVolume.ptr(), time.UnixMilli(int64(raw.CloseTime.V)).UTC()
	return q, nil
}

// --- fetching ---

// stableVenueFetcher reads the venue tickers; it keeps the Binance pause.
type stableVenueFetcher struct {
	ep  stableEndpoints
	now func() time.Time
	// onFetch reports every upstream call (provider health); may be nil.
	onFetch func(provider string, took time.Duration, code string)

	mu           sync.Mutex
	binanceUntil time.Time
}

func newStableVenueFetcher(ep stableEndpoints, now func() time.Time) *stableVenueFetcher {
	return &stableVenueFetcher{ep: ep, now: now}
}

func (f *stableVenueFetcher) get(ctx context.Context, provider, rawURL string) ([]byte, string) {
	t0 := time.Now()
	body, code := stableFetch(ctx, rawURL, stableTimeoutSmall, stableMaxBodySmall)
	if f.onFetch != nil {
		f.onFetch(provider, time.Since(t0), code)
	}
	return body, code
}

func (f *stableVenueFetcher) fetchCoinbase(ctx context.Context, product string) stableVenueQuote {
	at := f.now().UTC()
	body, code := f.get(ctx, "coinbase", f.ep.CoinbaseBase+"/products/"+url.PathEscape(product)+"/ticker")
	if code == "not_found" {
		code = "disabled" // the product is not listed: not an outage
	}
	return stableQuoteOrCode("coinbase", product, at, code, func() (stableVenueQuote, error) { return parseCoinbaseTicker(body, product, at) })
}

func (f *stableVenueFetcher) fetchKraken(ctx context.Context) []stableVenueQuote {
	at := f.now().UTC()
	body, code := f.get(ctx, "kraken", f.ep.KrakenBase+"/0/public/Ticker?pair="+stableKrakenRequest)
	var qs []stableVenueQuote
	if code == "" {
		var err error
		if qs, err = parseKrakenTicker(body, at); err != nil {
			code = "bad_response"
		}
	}
	if code != "" {
		qs = qs[:0]
		for _, p := range stableVenuePairs {
			if p.Venue == "kraken" {
				q, _ := newStableQuote("kraken", p.Pair, at)
				q.Code = code
				qs = append(qs, q)
			}
		}
	}
	return qs
}

func (f *stableVenueFetcher) fetchBitstamp(ctx context.Context, pair string) stableVenueQuote {
	at := f.now().UTC()
	body, code := f.get(ctx, "bitstamp", f.ep.BitstampBase+"/api/v2/ticker/"+url.PathEscape(pair)+"/")
	return stableQuoteOrCode("bitstamp", pair, at, code, func() (stableVenueQuote, error) { return parseBitstampTicker(body, pair, at) })
}

func (f *stableVenueFetcher) fetchOKX(ctx context.Context, inst string) stableVenueQuote {
	at := f.now().UTC()
	body, code := f.get(ctx, "okx", f.ep.OKXBase+"/api/v5/market/ticker?instId="+url.QueryEscape(inst))
	return stableQuoteOrCode("okx", inst, at, code, func() (stableVenueQuote, error) { return parseOKXTicker(body, inst, at) })
}

func (f *stableVenueFetcher) fetchBinance(ctx context.Context, symbol string) stableVenueQuote {
	at := f.now().UTC()
	f.mu.Lock()
	paused := at.Before(f.binanceUntil)
	f.mu.Unlock()
	if paused {
		q, _ := newStableQuote("binance", symbol, at)
		q.Code = "paused"
		return q
	}
	body, code := f.get(ctx, "binance", f.ep.BinanceBase+"/api/v3/ticker/24hr?symbol="+url.QueryEscape(symbol))
	if code == "geo_blocked" || code == "blocked" {
		f.mu.Lock()
		f.binanceUntil = at.Add(stableBinancePause)
		f.mu.Unlock()
	}
	return stableQuoteOrCode("binance", symbol, at, code, func() (stableVenueQuote, error) { return parseBinanceTicker(body, symbol, at) })
}

// binancePausedUntil reports the end of the current Binance pause (zero if none).
func (f *stableVenueFetcher) binancePausedUntil() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.binanceUntil
}

func stableQuoteOrCode(venue, pair string, at time.Time, code string, parse func() (stableVenueQuote, error)) stableVenueQuote {
	if code == "" {
		q, err := parse()
		if err == nil {
			return q
		}
		code = "bad_response"
	}
	q, _ := newStableQuote(venue, pair, at)
	q.Code = code
	return q
}

// fetchAll reads every pair in stableVenuePairs from the enabled providers
// concurrently. Pairs of a disabled provider come back with Code disabled
// and no upstream call.
func (f *stableVenueFetcher) fetchAll(ctx context.Context, enabled map[string]bool) []stableVenueQuote {
	var (
		mu  sync.Mutex
		out []stableVenueQuote
		g   errgroup.Group
	)
	add := func(qs ...stableVenueQuote) {
		mu.Lock()
		out = append(out, qs...)
		mu.Unlock()
	}
	krakenDone := false
	for _, p := range stableVenuePairs {
		p := p
		if !enabled[p.Venue] {
			q, _ := newStableQuote(p.Venue, p.Pair, f.now().UTC())
			q.Code = "disabled"
			add(q)
			continue
		}
		switch p.Venue {
		case "kraken":
			if !krakenDone {
				krakenDone = true
				g.Go(func() error { add(f.fetchKraken(ctx)...); return nil })
			}
		case "coinbase":
			g.Go(func() error { add(f.fetchCoinbase(ctx, p.Pair)); return nil })
		case "bitstamp":
			g.Go(func() error { add(f.fetchBitstamp(ctx, p.Pair)); return nil })
		case "okx":
			g.Go(func() error { add(f.fetchOKX(ctx, p.Pair)); return nil })
		case "binance":
			g.Go(func() error { add(f.fetchBinance(ctx, p.Pair)); return nil })
		}
	}
	_ = g.Wait()
	// Report in stableVenuePairs order.
	order := map[string]int{}
	for i, p := range stableVenuePairs {
		order[p.Venue+"|"+p.Pair] = i
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].Venue+"|"+out[i].Pair] < order[out[j].Venue+"|"+out[j].Pair] })
	return out
}
