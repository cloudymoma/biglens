package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/option"
)

// --- scripted BigQuery fake (jobs.query fast path) ---

// scriptedBQ answers each query a real *bigquery.Client sends through the
// jobs.query fast path with the result its respond func picks for the SQL,
// and records SQL, labels and the byte cap of every request.
type scriptedBQ struct {
	respond func(sql string) bqScript

	mu   sync.Mutex
	reqs []scriptedBQRequest
}

type bqScript struct {
	cols   []bqCol
	rows   [][]any // REST-encoded cells; nil is SQL NULL
	status int     // non-zero: fail the query with this HTTP status
	errMsg string
}

type bqCol struct{ name, typ string }

type scriptedBQRequest struct {
	sql      string
	labels   map[string]string
	maxBytes string
}

func (f *scriptedBQ) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/queries") {
		http.Error(w, "scripted BigQuery only serves jobs.query, got "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		return
	}
	var req struct {
		Query              string            `json:"query"`
		Labels             map[string]string `json:"labels"`
		MaximumBytesBilled string            `json:"maximumBytesBilled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, scriptedBQRequest{sql: req.Query, labels: req.Labels, maxBytes: req.MaximumBytesBilled})
	f.mu.Unlock()

	s := f.respond(req.Query)
	w.Header().Set("Content-Type", "application/json")
	if s.status != 0 {
		w.WriteHeader(s.status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": s.status, "message": s.errMsg,
			"errors": []map[string]string{{"reason": "invalidQuery", "message": s.errMsg}},
		}})
		return
	}
	cols := s.cols
	if len(cols) == 0 {
		cols = []bqCol{{"date", "STRING"}}
	}
	fields := make([]map[string]string, len(cols))
	for i, c := range cols {
		fields[i] = map[string]string{"name": c.name, "type": c.typ, "mode": "NULLABLE"}
	}
	rows := make([]map[string]any, len(s.rows))
	for i, row := range s.rows {
		cells := make([]map[string]any, len(row))
		for j, v := range row {
			cells[j] = map[string]any{"v": v}
		}
		rows[i] = map[string]any{"f": cells}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"kind":         "bigquery#queryResponse",
		"jobReference": map[string]string{"projectId": "test-project", "jobId": "scripted-job", "location": "US"},
		"jobComplete":  true,
		"schema":       map[string]any{"fields": fields},
		"rows":         rows,
		"totalRows":    strconv.Itoa(len(rows)),
	})
}

func (f *scriptedBQ) sent() []scriptedBQRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scriptedBQRequest(nil), f.reqs...)
}

func newScriptedBQClient(t *testing.T, f *scriptedBQ) *bigquery.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := bigquery.NewClient(context.Background(), "test-project",
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func newScriptedBQHandler(t *testing.T, f *scriptedBQ) *APIHandler {
	t.Helper()
	return &APIHandler{bq: &BQClient{client: newScriptedBQClient(t, f)}, cache: NewCache(time.Minute)}
}

// cacheTTLLeft returns how long key has left in c (0 when absent).
func cacheTTLLeft(c *Cache, key string) time.Duration {
	v, ok := c.store.Load(key)
	if !ok {
		return 0
	}
	return time.Until(v.(*cacheEntry).expiresAt)
}

func ttlNear(got, want time.Duration) bool {
	d := got - want
	return d > -10*time.Second && d < 10*time.Second
}

// --- readiness probe ---

func TestCryptoReadySQL(t *testing.T) {
	for chain, table := range map[string]string{
		"btc": "bigquery-public-data.crypto_bitcoin.transactions",
		"eth": "bigquery-public-data.crypto_ethereum.transactions",
	} {
		sql, err := cryptoReadySQL(chain)
		if err != nil {
			t.Fatalf("cryptoReadySQL(%q): %v", chain, err)
		}
		for _, w := range []string{table, "MAX(block_timestamp) AS t",
			"block_timestamp >= TIMESTAMP(@end_date)",
			"block_timestamp < TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL 2 HOUR)"} {
			if !strings.Contains(sql, w) {
				t.Errorf("%s probe missing %q:\n%s", chain, w, sql)
			}
		}
	}
	if _, err := cryptoReadySQL("doge"); err == nil {
		t.Error("expected an error for an unknown chain")
	}
}

// Ingestion is sequential: any row at/after end proves the day before end
// is complete; NULL (no row yet) does not.
func TestCryptoDayComplete(t *testing.T) {
	end := civil.Date{Year: 2026, Month: 10, Day: 13}
	at := func(h, m int) bigquery.NullTimestamp {
		return bigquery.NullTimestamp{Timestamp: time.Date(2026, 10, 13, h, m, 0, 0, time.UTC), Valid: true}
	}
	tests := []struct {
		name string
		t    bigquery.NullTimestamp
		want bool
	}{
		{"no row yet", bigquery.NullTimestamp{}, false},
		{"row exactly at end", at(0, 0), true},
		{"row after end", at(0, 7), true},
		{"row before end", bigquery.NullTimestamp{Timestamp: time.Date(2026, 10, 12, 23, 59, 0, 0, time.UTC), Valid: true}, false},
	}
	for _, tt := range tests {
		if got := cryptoDayComplete(tt.t, end); got != tt.want {
			t.Errorf("%s: cryptoDayComplete = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A partial response (a query failed) keeps its 2 minutes; a response whose
// newest day is not confirmed complete is cached 5 minutes; a complete one
// until the next daily rollover.
func TestCryptoResponseTTL(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 25, 0, 0, time.UTC)
	_, end := cryptoWindowAt(30, now)
	tests := []struct {
		name              string
		partial, complete bool
		want              time.Duration
	}{
		{"complete", false, true, cryptoTTL(end, now)},
		{"latest day still ingesting", false, false, cryptoIncompleteTTL},
		{"partial and complete", true, true, cryptoPartialTTL},
		{"partial and incomplete", true, false, cryptoPartialTTL},
	}
	for _, tt := range tests {
		if got := cryptoResponseTTL(end, now, tt.partial, tt.complete); got != tt.want {
			t.Errorf("%s: ttl = %v, want %v", tt.name, got, tt.want)
		}
	}
	if cryptoIncompleteTTL != 5*time.Minute {
		t.Errorf("cryptoIncompleteTTL = %v, want 5m", cryptoIncompleteTTL)
	}
	if cryptoTTL(end, now) <= cryptoIncompleteTTL {
		t.Fatalf("test setup: rollover TTL %v must exceed the incomplete TTL", cryptoTTL(end, now))
	}
}

// probeScript answers readiness probes per table: tables in notReady get
// NULL, tables in failing get an error, the rest a block at end + 5 min.
// Every other query returns an empty result.
func probeScript(end civil.Date, notReady, failing map[string]bool) func(string) bqScript {
	ts := strconv.FormatInt(end.In(time.UTC).Add(5*time.Minute).UnixMicro(), 10)
	return func(sql string) bqScript {
		if !strings.Contains(sql, "MAX(block_timestamp) AS t") {
			return bqScript{}
		}
		for table := range failing {
			if strings.Contains(sql, table) {
				return bqScript{status: http.StatusBadRequest, errMsg: "Access Denied: Table " + table}
			}
		}
		for table := range notReady {
			if strings.Contains(sql, table) {
				return bqScript{cols: []bqCol{{"t", "TIMESTAMP"}}, rows: [][]any{{nil}}}
			}
		}
		return bqScript{cols: []bqCol{{"t", "TIMESTAMP"}}, rows: [][]any{{ts}}}
	}
}

type cryptoDailyEndpoint struct {
	name    string
	handler func(h *APIHandler) http.HandlerFunc
	query   string
	key     string // cache key of the response, %s = end
	chains  []string
}

var cryptoDailyEndpoints = []cryptoDailyEndpoint{
	{"pulse", func(h *APIHandler) http.HandlerFunc { return h.CryptoPulse }, "days=7", "opendata:crypto:pulse:7:%s", []string{"btc", "eth"}},
	{"fees", func(h *APIHandler) http.HandlerFunc { return h.CryptoFees }, "days=7", "opendata:crypto:fees:7:%s", []string{"btc", "eth"}},
	{"whales btc", func(h *APIHandler) http.HandlerFunc { return h.CryptoWhales }, "days=7&chain=btc", "opendata:crypto:whales:btc:7:%s", []string{"btc"}},
	{"whales eth", func(h *APIHandler) http.HandlerFunc { return h.CryptoWhales }, "days=7&chain=eth", "opendata:crypto:whales:eth:7:%s", []string{"eth"}},
	{"tokens", func(h *APIHandler) http.HandlerFunc { return h.CryptoTokens }, "days=7", "opendata:crypto:tokens:7:%s", []string{"eth"}},
	{"mining", func(h *APIHandler) http.HandlerFunc { return h.CryptoMining }, "days=7", "opendata:crypto:mining:7:%s", []string{"btc"}},
}

var cryptoProbeTables = map[string]string{
	"btc": "crypto_bitcoin.transactions",
	"eth": "crypto_ethereum.transactions",
}

func getDaily(t *testing.T, h *APIHandler, ep cryptoDailyEndpoint) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	ep.handler(h)(w, httptest.NewRequest(http.MethodGet, "/api/opendata/crypto/x?"+ep.query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d (%s), want 200", ep.name, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode: %v", ep.name, err)
	}
	return body
}

// Every daily dashboard probes the chains it reads; a complete latest day is
// flagged and cached until the rollover, and the probe is a small labelled
// job.
func TestCryptoDailyEndpointsLatestDayComplete(t *testing.T) {
	for _, ep := range cryptoDailyEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			_, end := cryptoWindowAt(7, time.Now().UTC())
			fake := &scriptedBQ{respond: probeScript(end, nil, nil)}
			h := newScriptedBQHandler(t, fake)

			body := getDaily(t, h, ep)
			if body["last_day_complete"] != true {
				t.Errorf("last_day_complete = %v, want true", body["last_day_complete"])
			}
			if body["last_day_probe_failed"] != nil {
				t.Errorf("last_day_probe_failed = %v, want omitted when complete", body["last_day_probe_failed"])
			}
			key := strings.Replace(ep.key, "%s", end.String(), 1)
			if got, want := cacheTTLLeft(h.cache, key), cryptoTTL(end, time.Now().UTC()); !ttlNear(got, want) {
				t.Errorf("response TTL = %v, want ~%v (until the rollover)", got, want)
			}

			probed := map[string]bool{}
			for _, r := range fake.sent() {
				if !strings.Contains(r.sql, "MAX(block_timestamp) AS t") {
					continue
				}
				for chain, table := range cryptoProbeTables {
					if strings.Contains(r.sql, table) {
						probed[chain] = true
					}
				}
				if r.maxBytes != strconv.FormatInt(cryptoReadyMaxBytesBilled, 10) {
					t.Errorf("probe maximumBytesBilled = %q, want %d", r.maxBytes, cryptoReadyMaxBytesBilled)
				}
				if r.labels["app"] != "biglens" {
					t.Errorf("probe labels = %v, want app=biglens", r.labels)
				}
			}
			for _, c := range ep.chains {
				if !probed[c] {
					t.Errorf("%s chain was not probed (probed: %v)", c, probed)
				}
			}
			if cryptoReadyMaxBytesBilled != 256<<20 {
				t.Errorf("probe cap = %d, want 256 MiB", cryptoReadyMaxBytesBilled)
			}
		})
	}
}

// A chain whose latest day has no row at/after end yet (or whose probe
// fails) marks the response incomplete and caches it only 5 minutes.
func TestCryptoDailyEndpointsLatestDayIncomplete(t *testing.T) {
	for _, mode := range []string{"null", "probe error"} {
		for _, ep := range cryptoDailyEndpoints {
			t.Run(mode+"/"+ep.name, func(t *testing.T) {
				_, end := cryptoWindowAt(7, time.Now().UTC())
				bad := map[string]bool{cryptoProbeTables[ep.chains[0]]: true}
				var fake *scriptedBQ
				if mode == "null" {
					fake = &scriptedBQ{respond: probeScript(end, bad, nil)}
				} else {
					fake = &scriptedBQ{respond: probeScript(end, nil, bad)}
				}
				h := newScriptedBQHandler(t, fake)

				body := getDaily(t, h, ep)
				if body["last_day_complete"] != false {
					t.Errorf("last_day_complete = %v, want false", body["last_day_complete"])
				}
				if mode == "probe error" {
					if body["last_day_probe_failed"] != true {
						t.Errorf("last_day_probe_failed = %v, want true on probe error", body["last_day_probe_failed"])
					}
				} else if body["last_day_probe_failed"] != nil {
					t.Errorf("last_day_probe_failed = %v, want omitted when probe succeeded with NULL", body["last_day_probe_failed"])
				}
				if strings.Contains(body2string(body), "Access Denied") {
					t.Errorf("a probe error must not reach the client: %v", body)
				}
				key := strings.Replace(ep.key, "%s", end.String(), 1)
				if got := cacheTTLLeft(h.cache, key); !ttlNear(got, cryptoIncompleteTTL) {
					t.Errorf("response TTL = %v, want ~%v", got, cryptoIncompleteTTL)
				}
			})
		}
	}
}

func body2string(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Shared BTC sub-results (/pulse+/fees blocks, /fees→/mining coinbase) must
// not outlive an incomplete latest day either.
func TestCryptoSharedBtcCachesFollowReadiness(t *testing.T) {
	_, end := cryptoWindowAt(7, time.Now().UTC())
	start := end.AddDays(-7)
	fake := &scriptedBQ{respond: probeScript(end, map[string]bool{"crypto_bitcoin.transactions": true}, nil)}
	h := newScriptedBQHandler(t, fake)

	getDaily(t, h, cryptoDailyEndpoints[1]) // /fees writes btc_blocks and btc_coinbase
	for _, key := range []string{
		"opendata:crypto:q:btc_blocks:" + start.String() + ":" + end.String(),
		"opendata:crypto:q:btc_coinbase:" + start.String() + ":" + end.String(),
	} {
		if got := cacheTTLLeft(h.cache, key); !ttlNear(got, cryptoIncompleteTTL) {
			t.Errorf("%s TTL = %v, want ~%v while BTC's latest day is incomplete", key, got, cryptoIncompleteTTL)
		}
	}
}

// One probe per (chain, end) serves every endpoint for 5 minutes.
func TestCryptoReadinessProbeIsCached(t *testing.T) {
	_, end := cryptoWindowAt(7, time.Now().UTC())
	fake := &scriptedBQ{respond: probeScript(end, map[string]bool{"crypto_bitcoin.transactions": true}, nil)}
	h := newScriptedBQHandler(t, fake)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if h.cryptoChainReadyStatus(ctx, "btc", end).complete {
			t.Fatal("btc must not be ready")
		}
	}
	probes := 0
	for _, r := range fake.sent() {
		if strings.Contains(r.sql, "MAX(block_timestamp) AS t") {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("ran %d probes, want 1 (cached per chain and end)", probes)
	}
	key := "opendata:crypto:ready:btc:" + end.String()
	if got := cacheTTLLeft(h.cache, key); !ttlNear(got, cryptoIncompleteTTL) {
		t.Errorf("probe cache TTL = %v, want ~%v", got, cryptoIncompleteTTL)
	}
}
