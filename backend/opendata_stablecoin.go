package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"
)

// stablecoinService holds the Stablecoins dashboard state (stablecoin.md §8.2).
// It is nil when the dashboard is disabled; every stablecoin route then
// answers 404 (§9).
type stablecoinService struct {
	settings stableSettings
	facts    stableFacts
	now      func() time.Time
}

func newStablecoinService(cfg *Config) *stablecoinService {
	var sc StablecoinConfig
	if cfg != nil {
		sc = cfg.Stablecoin
	}
	s := sc.settings()
	if !s.Enabled {
		return nil
	}
	return &stablecoinService{settings: s, facts: loadStableFacts(s.FactsFile, os.ReadFile), now: time.Now}
}

// providerEnabled reports whether a provider key may be called; false means
// the source is never called (§4, §8.6).
func (s *stablecoinService) providerEnabled(key string) bool {
	if s == nil {
		return false
	}
	return s.settings.Providers[key]
}

// stableAvailable writes 404 and returns false when the dashboard is disabled.
func (h *APIHandler) stableAvailable(w http.ResponseWriter) bool {
	if h == nil || h.stable == nil {
		writeError(w, "stablecoin dashboard disabled", http.StatusNotFound)
		return false
	}
	return true
}

// stableAssetDef describes one core asset (§2.1).
type stableAssetDef struct {
	ID        string // usdt | usdc | eurc (API whitelist values)
	Symbol    string
	Numeraire string // USD | EUR: every peg and supply metric of the asset is in this unit
	LlamaID   string // DefiLlama stablecoin id
	LlamaPeg  string // DefiLlama pegType key
	Issuer    string // tether | circle
}

var stableCoreAssets = []stableAssetDef{
	{ID: "usdt", Symbol: "USDT", Numeraire: "USD", LlamaID: "1", LlamaPeg: "peggedUSD", Issuer: "tether"},
	{ID: "usdc", Symbol: "USDC", Numeraire: "USD", LlamaID: "2", LlamaPeg: "peggedUSD", Issuer: "circle"},
	{ID: "eurc", Symbol: "EURC", Numeraire: "EUR", LlamaID: "50", LlamaPeg: "peggedEUR", Issuer: "circle"},
}

func stableAssetByID(id string) (stableAssetDef, bool) {
	for _, a := range stableCoreAssets {
		if a.ID == id {
			return a, true
		}
	}
	return stableAssetDef{}, false
}

// stableEndpoints are the upstream base URLs; tests and the verify CLI stub
// them with httptest servers.
type stableEndpoints struct {
	LlamaBase    string
	CircleURL    string
	TetherURL    string
	ECBBase      string
	TreasuryBase string
}

func defaultStableEndpoints() stableEndpoints {
	return stableEndpoints{
		LlamaBase:    "https://stablecoins.llama.fi",
		CircleURL:    "https://api.circle.com/v1/stablecoins",
		TetherURL:    "https://app.tether.to/transparency.json",
		ECBBase:      "https://data-api.ecb.europa.eu/service/data",
		TreasuryBase: "https://home.treasury.gov/resource-center/data-chart-center/interest-rates/daily-treasury-rates.csv",
	}
}

// Body caps and per-call timeouts (§8.4). DefiLlama's list is ~550 KB and a
// full chart ~470 KB; the issuer and rate documents are under 100 KB.
const (
	stableMaxBodyLarge int64 = 8 << 20
	stableMaxBodySmall int64 = 1 << 20
	stableTimeoutLarge       = 20 * time.Second
	stableTimeoutSmall       = 10 * time.Second
)

var errStableRedirect = errors.New("redirect blocked")

// stableHTTPClient never follows redirects: the verified endpoints answer
// directly, so a redirect is treated as a changed response (bad_response).
var stableHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errStableRedirect
	},
}

func stableMapStatus(code int) string {
	switch {
	case code == http.StatusOK:
		return ""
	case code == http.StatusTooManyRequests:
		return "rate_limited"
	case code == http.StatusUnavailableForLegalReasons:
		return "geo_blocked"
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "blocked"
	case code == http.StatusNotFound:
		return "not_found"
	case code >= 500:
		return "5xx"
	default:
		return "bad_response"
	}
}

// stableFetch GETs rawURL within timeout and returns at most maxBody bytes,
// or an error code (never an upstream message or URL): rate_limited,
// geo_blocked, blocked, not_found, 5xx, bad_response, timeout, network_error.
func stableFetch(ctx context.Context, rawURL string, timeout time.Duration, maxBody int64) ([]byte, string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "bad_response"
	}
	req.Header.Set("User-Agent", btcUserAgent)
	resp, err := stableHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, errStableRedirect) {
			return nil, "bad_response"
		}
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if code := stableMapStatus(resp.StatusCode); code != "" {
		return nil, code
	}
	b, err := readCapped(resp.Body, maxBody)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, "timeout"
		}
		return nil, "bad_response"
	}
	return b, ""
}

// stableLastGood keeps the last successful value of one source, so a failed
// fetch serves it with stale: true instead of nothing (§8.4).
type stableLastGood[T any] struct {
	mu  sync.Mutex
	val T
	at  time.Time
	ok  bool
}

// stableResult is the outcome of one fetch after the last-good fallback.
type stableResult[T any] struct {
	Value T
	Has   bool      // Value is usable (fresh, or last-good when Stale)
	Stale bool      // Value comes from an earlier successful fetch
	Code  string    // "" when this fetch succeeded, else its error code
	AsOf  time.Time // fetch time of Value
}

func (l *stableLastGood[T]) resolve(v T, code string, now time.Time) stableResult[T] {
	l.mu.Lock()
	defer l.mu.Unlock()
	if code == "" {
		l.val, l.at, l.ok = v, now, true
		return stableResult[T]{Value: v, Has: true, AsOf: now}
	}
	if l.ok {
		return stableResult[T]{Value: l.val, Has: true, Stale: true, Code: code, AsOf: l.at}
	}
	return stableResult[T]{Code: code}
}
