package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/option"
)

type fakeScamQueryCall struct {
	sql        string
	start, end civil.Date
	maxBytes   int64
}

type fakeScamBQRunner struct {
	maxTime    time.Time
	dryBytes   int64
	billed     int64
	queryCalls []fakeScamQueryCall
	ethPoison  []ethPoisonRow
	ethFake    []ethFakeTokenRow
	tronPoison []tronPoisonRow
	tronEvents []tronStablecoinRow
	btcRBF     []btcRBFRow
}

func (f *fakeScamBQRunner) MaxBlockTime(_ context.Context, _ string, _ civil.Date) (time.Time, error) {
	return f.maxTime, nil
}

func (f *fakeScamBQRunner) DryRun(_ context.Context, _ string, _, _ civil.Date) (int64, error) {
	return f.dryBytes, nil
}

func (f *fakeScamBQRunner) Query(_ context.Context, sql string, start, end civil.Date, maxBytes int64, scan func(next func(dst any) bool) error) (int64, error) {
	f.queryCalls = append(f.queryCalls, fakeScamQueryCall{sql: sql, start: start, end: end, maxBytes: maxBytes})
	switch sql {
	case scamEthPoisonSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.ethPoison) {
				return false
			}
			*(dst.(*ethPoisonRow)) = f.ethPoison[idx]
			idx++
			return true
		})
		return f.billed, err
	case scamEthFakeTokenSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.ethFake) {
				return false
			}
			*(dst.(*ethFakeTokenRow)) = f.ethFake[idx]
			idx++
			return true
		})
		return f.billed, err
	case scamTronPoisonSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.tronPoison) {
				return false
			}
			*(dst.(*tronPoisonRow)) = f.tronPoison[idx]
			idx++
			return true
		})
		return f.billed, err
	case tronStablecoinSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.tronEvents) {
				return false
			}
			*(dst.(*tronStablecoinRow)) = f.tronEvents[idx]
			idx++
			return true
		})
		return f.billed, err
	case scamBTCRBFSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.btcRBF) {
				return false
			}
			*(dst.(*btcRBFRow)) = f.btcRBF[idx]
			idx++
			return true
		})
		return f.billed, err
	default:
		return 0, nil
	}
}

func TestDecodeTronStablecoinRow(t *testing.T) {
	const (
		usdtHex20  = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
		usdtBase58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	)

	freezeEv, err := decodeTronStablecoinRow(tronStablecoinRow{
		TxHash:       "tx_freeze",
		LogIndex:     1,
		BlockNumber:  65000000,
		BlockTime:    "2026-10-06T01:02:03Z",
		Action:       "freeze",
		AddressHex20: usdtHex20,
	})
	if err != nil {
		t.Fatalf("decode freeze: %v", err)
	}
	if freezeEv.Address != usdtBase58 || freezeEv.Action != "freeze" || freezeEv.Token != "USDT" || freezeEv.Amount != "" {
		t.Errorf("freezeEv = %+v", freezeEv)
	}

	unfreezeEv, err := decodeTronStablecoinRow(tronStablecoinRow{
		TxHash:       "tx_unfreeze",
		LogIndex:     2,
		BlockNumber:  65000001,
		BlockTime:    "2026-10-06T02:02:03Z",
		Action:       "unfreeze",
		AddressHex20: usdtHex20,
	})
	if err != nil {
		t.Fatalf("decode unfreeze: %v", err)
	}
	if unfreezeEv.Address != usdtBase58 || unfreezeEv.Action != "unfreeze" || unfreezeEv.Amount != "" {
		t.Errorf("unfreezeEv = %+v", unfreezeEv)
	}

	destroyEv, err := decodeTronStablecoinRow(tronStablecoinRow{
		TxHash:       "tx_destroy",
		LogIndex:     3,
		BlockNumber:  65000002,
		BlockTime:    "2026-10-06T03:02:03Z",
		Action:       "destroy",
		AddressHex20: usdtHex20,
		AmountHex:    "0000000000000000000000000000000000000000000000000000000005f5e100", // 100,000,000 (100 USDT)
	})
	if err != nil {
		t.Fatalf("decode destroy: %v", err)
	}
	if destroyEv.Address != usdtBase58 || destroyEv.Action != "destroy" || destroyEv.Amount != "100000000" {
		t.Errorf("destroyEv = %+v", destroyEv)
	}

	// Invalid cases must fail.
	if _, err := decodeTronStablecoinRow(tronStablecoinRow{TxHash: "t", Action: "freeze", AddressHex20: "badhex"}); err == nil {
		t.Error("expected error for bad address_hex20")
	}
	if _, err := decodeTronStablecoinRow(tronStablecoinRow{TxHash: "t", Action: "destroy", AddressHex20: usdtHex20, AmountHex: "not_hex"}); err == nil {
		t.Error("expected error for bad destroy amount_hex")
	}
	if _, err := decodeTronStablecoinRow(tronStablecoinRow{TxHash: "t", Action: "mint", AddressHex20: usdtHex20}); err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestScamRadarSQLPartitionFiltersAndMaxBytes(t *testing.T) {
	queries := map[string]string{
		"R1 scamEthPoisonSQL":    scamEthPoisonSQL,
		"R2 scamEthFakeTokenSQL": scamEthFakeTokenSQL,
		"R3 scamTronPoisonSQL":   scamTronPoisonSQL,
		"R4 tronStablecoinSQL":   tronStablecoinSQL,
		"R5 scamBTCRBFSQL":       scamBTCRBFSQL,
	}
	for name, q := range queries {
		if !strings.Contains(q, "block_timestamp >= TIMESTAMP(@start_date)") || !strings.Contains(q, "block_timestamp < TIMESTAMP(@end_date)") {
			t.Errorf("%s missing block_timestamp partition filter:\n%s", name, q)
		}
	}

	runner := &fakeScamBQRunner{billed: 1234}
	jobs := newScamRadarJobsWithRunner(runner)
	start := civil.Date{Year: 2026, Month: time.October, Day: 5}
	end := civil.Date{Year: 2026, Month: time.October, Day: 6}

	const wantCap = int64(42 << 30)
	for _, j := range jobs {
		runner.queryCalls = nil
		if _, _, err := j.Run(context.Background(), start, end, wantCap); err != nil {
			t.Fatalf("%s Run: %v", j.ID(), err)
		}
		if len(runner.queryCalls) == 0 {
			t.Fatalf("%s made 0 query calls", j.ID())
		}
		for _, c := range runner.queryCalls {
			if c.maxBytes != wantCap {
				t.Errorf("%s query maxBytes = %d, want %d", j.ID(), c.maxBytes, wantCap)
			}
		}
	}
}

func TestScamRadarPairingIncludesDay(t *testing.T) {
	if !strings.Contains(scamEthPoisonSQL, "z.day = r.day") {
		t.Errorf("R1 scamEthPoisonSQL must pair on same day (z.day = r.day):\n%s", scamEthPoisonSQL)
	}
	if !strings.Contains(scamTronPoisonSQL, "d.day = r.day") {
		t.Errorf("R3 scamTronPoisonSQL must pair on same day (d.day = r.day):\n%s", scamTronPoisonSQL)
	}
	if !strings.Contains(scamTronPoisonSQL, "CREATE TEMP FUNCTION b58") || !strings.Contains(scamTronPoisonSQL, "CREATE TEMP FUNCTION tron") {
		t.Errorf("R3 scamTronPoisonSQL must include in-query JS UDF b58 and SQL function tron")
	}
}

// The SQD-backed crypto_bitcoin.inputs view carries one coinbase input per
// block (spent_transaction_hash NULL, arbitrary sequence; legacy had none).
// Counting it would add coinbase txs to the denominator and miscount many of
// them as RBF-signalling, so R5 must skip inputs that spend nothing.
func TestScamBTCRBFSQLExcludesCoinbaseInputs(t *testing.T) {
	where := scamBTCRBFSQL[strings.Index(scamBTCRBFSQL, "WHERE"):]
	if !strings.Contains(where, "AND spent_transaction_hash IS NOT NULL") {
		t.Errorf("R5 scamBTCRBFSQL must exclude coinbase inputs (spent_transaction_hash IS NOT NULL) in WHERE:\n%s", scamBTCRBFSQL)
	}
}

func TestTronStablecoinSQLReusesTopicConstants(t *testing.T) {
	for _, topic := range []string{topicUSDTAddedBlackList, topicUSDTRemovedBlackList, topicUSDTDestroyedBlackFunds} {
		if !strings.Contains(tronStablecoinSQL, topic) {
			t.Errorf("tronStablecoinSQL missing topic %s", topic)
		}
	}
	srcBytes, err := os.ReadFile("scam_radar_jobs.go")
	if err != nil {
		t.Fatalf("read scam_radar_jobs.go: %v", err)
	}
	src := string(srcBytes)
	for _, topic := range []string{topicUSDTAddedBlackList, topicUSDTRemovedBlackList, topicUSDTDestroyedBlackFunds} {
		if strings.Contains(src, topic) {
			t.Errorf("scam_radar_jobs.go must reuse topicUSDT* constants rather than repeating literal %s", topic)
		}
	}
}

func TestScamRadarJobsRunAndStoreWithFakeBQ(t *testing.T) {
	store := newTestRiskStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

	const (
		ethLookalike1 = "0x111199999999999999999999999999999999aaaa"
		ethImitated1  = "0x111100000000000000000000000000000000aaaa"
		ethFake1      = "0xdead000000000000000000000000000000000001"
		tronLookalike = "TTZAH6Yw11111111111111111111jk9747"
		tronImitated  = "TTZAHWEr22222222222222222222jk9747"
		usdtHex20     = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	)

	runner := &fakeScamBQRunner{
		billed: 2 << 30,
		ethPoison: []ethPoisonRow{
			{Day: "2026-10-05", Lookalike: ethLookalike1, Imitated: ethImitated1, Hits: 12, Victims: 9},
		},
		ethFake: []ethFakeTokenRow{
			{Day: "2026-10-05", Contract: ethFake1, Symbol: "USDT", Transfers: 45, Recipients: 30},
		},
		tronPoison: []tronPoisonRow{
			{Day: "2026-10-05", Lookalike58: tronLookalike, Imitated58: tronImitated, Hits: 7, Victims: 6, Candidates: 137386},
		},
		tronEvents: []tronStablecoinRow{
			{TxHash: "t_f1", LogIndex: 1, BlockNumber: 100, BlockTime: "2026-10-05T01:00:00Z", Action: "freeze", AddressHex20: usdtHex20},
			{TxHash: "t_f2", LogIndex: 2, BlockNumber: 101, BlockTime: "2026-10-05T02:00:00Z", Action: "freeze", AddressHex20: usdtHex20},
			{TxHash: "t_u1", LogIndex: 3, BlockNumber: 102, BlockTime: "2026-10-05T03:00:00Z", Action: "unfreeze", AddressHex20: usdtHex20},
			{TxHash: "t_d1", LogIndex: 4, BlockNumber: 103, BlockTime: "2026-10-05T04:00:00Z", Action: "destroy", AddressHex20: usdtHex20, AmountHex: "05f5e100"},
		},
		btcRBF: []btcRBFRow{
			{Day: "2026-10-05", Txs: 500000, RBFSignalTxs: 333000},
		},
	}

	jobs := newScamRadarJobsWithRunner(runner)
	start := civil.Date{Year: 2026, Month: time.October, Day: 5}
	end := civil.Date{Year: 2026, Month: time.October, Day: 6}

	for _, j := range jobs {
		res, _, err := j.Run(ctx, start, end, dailySyncIncrementalMaxBytes)
		if err != nil {
			t.Fatalf("%s Run: %v", j.ID(), err)
		}
		if err := j.Store(ctx, store, res, "2026-10-05", "2026-10-05", now); err != nil {
			t.Fatalf("%s Store: %v", j.ID(), err)
		}
	}

	// Verify ETH and TRON lookalikes stored.
	ethHits, err := store.lookalikeHits(ctx, "eth", []string{ethLookalike1})
	if err != nil || ethHits[ethLookalike1].Hits != 12 || ethHits[ethLookalike1].Victims != 9 {
		t.Fatalf("eth lookalike = %+v, err = %v", ethHits, err)
	}
	tronHits, err := store.lookalikeHits(ctx, "tron", []string{tronLookalike})
	if err != nil || tronHits[tronLookalike].Hits != 7 || tronHits[tronLookalike].Victims != 6 {
		t.Fatalf("tron lookalike = %+v, err = %v", tronHits, err)
	}

	// Verify ETH fake token stored.
	fset, err := store.fakeTokenSet(ctx, "eth")
	if err != nil || !fset[ethFake1] {
		t.Fatalf("eth fakeTokenSet = %v, err = %v", fset, err)
	}

	// Verify all 13 scam_daily_stats metrics for 2026-10-05.
	rows, err := store.db.QueryContext(ctx, `SELECT metric, value FROM scam_daily_stats WHERE day = '2026-10-05'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotStats := map[string]float64{}
	for rows.Next() {
		var m string
		var v float64
		if err := rows.Scan(&m, &v); err != nil {
			t.Fatal(err)
		}
		gotStats[m] = v
	}

	wantStats := map[string]float64{
		"eth_poison_hits":     12,
		"eth_poison_victims":  9,
		"eth_lookalikes":      1,
		"eth_fake_transfers":  45,
		"eth_fake_contracts":  1,
		"tron_poison_hits":    7,
		"tron_poison_victims": 6,
		"tron_lookalikes":     1,
		"tron_candidates":     137386,
		"btc_txs":             500000,
		"btc_rbf_txs":         333000,
		"tron_usdt_freezes":   2,
		"tron_usdt_unfreezes": 1,
		"tron_usdt_destroys":  1,
	}
	if !reflect.DeepEqual(gotStats, wantStats) {
		t.Fatalf("scam_daily_stats:\ngot  %+v\nwant %+v", gotStats, wantStats)
	}
}

// fakeJobsInsert stands in for BigQuery's jobs.insert endpoint behind a real
// *bigquery.Client and records each job's configuration. Dry runs succeed
// with a fixed estimate; real queries are rejected (400, not retried) right
// after being recorded, so tests can pin labels and caps without faking the
// job polling protocol.
type fakeJobsInsert struct {
	mu   sync.Mutex
	jobs []fakeInsertedJob
}

type fakeInsertedJob struct {
	labels         map[string]string
	dryRun         bool
	maxBytesBilled string
}

func (f *fakeJobsInsert) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/jobs") {
		http.Error(w, "fake BigQuery only serves jobs.insert, got "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		return
	}
	var req struct {
		JobReference  json.RawMessage `json:"jobReference"`
		Configuration struct {
			Labels map[string]string `json:"labels"`
			DryRun bool              `json:"dryRun"`
			Query  struct {
				MaximumBytesBilled string `json:"maximumBytesBilled"`
			} `json:"query"`
		} `json:"configuration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.jobs = append(f.jobs, fakeInsertedJob{
		labels:         req.Configuration.Labels,
		dryRun:         req.Configuration.DryRun,
		maxBytesBilled: req.Configuration.Query.MaximumBytesBilled,
	})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !req.Configuration.DryRun {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":    http.StatusBadRequest,
			"message": "fake BigQuery does not run real queries",
			"errors":  []map[string]string{{"reason": "invalidQuery", "message": "fake"}},
		}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"jobReference": req.JobReference,
		"status":       map[string]string{"state": "DONE"},
		"statistics": map[string]any{
			"totalBytesProcessed": "1234",
			"query":               map[string]string{"totalBytesProcessed": "1234"},
		},
	})
}

func (f *fakeJobsInsert) recorded() []fakeInsertedJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeInsertedJob(nil), f.jobs...)
}

// Scam Radar jobs must carry the app=biglens label like every other BigLens
// query (so its own spend can be told apart in JOBS dashboards) while keeping
// the caller's MaxBytesBilled exactly as before.
func TestBqScamRunnerJobsCarryAppLabelAndKeepCaps(t *testing.T) {
	fake := &fakeJobsInsert{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, "test-project", option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("bigquery.NewClient: %v", err)
	}
	defer client.Close()
	runner := bqScamRunner{client: client}
	start := civil.Date{Year: 2026, Month: time.October, Day: 5}
	end := start.AddDays(1)

	est, err := runner.DryRun(ctx, scamBTCRBFSQL, start, end)
	if err != nil || est != 1234 {
		t.Fatalf("DryRun = %d, %v; want 1234, nil", est, err)
	}
	const wantCap = int64(42 << 30)
	if _, err := runner.Query(ctx, scamBTCRBFSQL, start, end, wantCap, func(func(dst any) bool) error { return nil }); err == nil {
		t.Fatal("Query: the fake rejects real queries, want an error")
	}

	jobs := fake.recorded()
	if len(jobs) != 2 {
		t.Fatalf("recorded %d jobs, want 2 (dry run + query)", len(jobs))
	}
	for i, j := range jobs {
		if j.labels["app"] != "biglens" {
			t.Errorf("job %d labels = %v, want app=biglens", i, j.labels)
		}
	}
	if !jobs[0].dryRun || jobs[0].maxBytesBilled != "" {
		t.Errorf("dry-run job = %+v, want dryRun without a cap", jobs[0])
	}
	if jobs[1].dryRun || jobs[1].maxBytesBilled != "45097156608" {
		t.Errorf("query job = %+v, want maximumBytesBilled=45097156608 (42 GiB, unchanged)", jobs[1])
	}
}
