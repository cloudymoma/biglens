package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	maxTime      time.Time
	dryBytes     int64
	billed       int64
	queryCalls   []fakeScamQueryCall
	ethPoison    []ethPoisonRow
	ethFake      []ethFakeTokenRow
	ethDeployers []fakeTokenDeployerBQRow
	tronPoison   []tronPoisonRow
	tronEvents   []tronStablecoinRow
	btcRBF       []btcRBFRow
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
	case scamEthFakeTokenDeployersSQL:
		idx := 0
		err := scan(func(dst any) bool {
			if idx >= len(f.ethDeployers) {
				return false
			}
			*(dst.(*fakeTokenDeployerBQRow)) = f.ethDeployers[idx]
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

	runner := healthyScamRunner("2026-10-05")
	runner.billed = 1234
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
// block (spent_output_index NULL, arbitrary sequence; legacy had none).
// Counting it would add coinbase txs to the denominator and miscount many of
// them as RBF-signalling, so R5 must skip inputs that spend nothing.
func TestScamBTCRBFSQLExcludesCoinbaseInputs(t *testing.T) {
	where := scamBTCRBFSQL[strings.Index(scamBTCRBFSQL, "WHERE"):]
	if !strings.Contains(where, "AND spent_output_index IS NOT NULL") {
		t.Errorf("R5 scamBTCRBFSQL must exclude coinbase inputs (spent_output_index IS NOT NULL) in WHERE:\n%s", scamBTCRBFSQL)
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
		ethDeployer1  = "0x80d04079ffc53eb14edbeeb37279d126f45552df"
		tronLookalike = "TTZAH6Yw11111111111111111111jk9747"
		tronImitated  = "TTZAHWEr22222222222222222222jk9747"
		usdtHex20     = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	)

	runner := &fakeScamBQRunner{
		billed: 2 << 30,
		ethPoison: []ethPoisonRow{
			{Day: "2026-10-05", Lookalike: ethLookalike1, Imitated: ethImitated1, Hits: 12, Victims: 9, Transfers: 812345, ZeroValue: 260000},
		},
		ethFake: []ethFakeTokenRow{
			{Day: "2026-10-05", Contract: ethFake1, Symbol: "USDT", Transfers: 45, Recipients: 30},
		},
		ethDeployers: []fakeTokenDeployerBQRow{
			{Creator: ethDeployer1, Contracts: 4394, FirstSeen: "2026-05-21", LastSeen: "2026-08-27"},
		},
		tronPoison: []tronPoisonRow{
			{Day: "2026-10-05", Lookalike58: tronLookalike, Imitated58: tronImitated, Hits: 7, Victims: 6, Candidates: 137386, Transfers: 2345678},
		},
		tronEvents: []tronStablecoinRow{
			{Day: "2026-10-05", USDTLogs: 5200000, TxHash: "t_f1", LogIndex: 1, BlockNumber: 100, BlockTime: "2026-10-05T01:00:00Z", Action: "freeze", AddressHex20: usdtHex20},
			{Day: "2026-10-05", USDTLogs: 5200000, TxHash: "t_f2", LogIndex: 2, BlockNumber: 101, BlockTime: "2026-10-05T02:00:00Z", Action: "freeze", AddressHex20: usdtHex20},
			{Day: "2026-10-05", USDTLogs: 5200000, TxHash: "t_u1", LogIndex: 3, BlockNumber: 102, BlockTime: "2026-10-05T03:00:00Z", Action: "unfreeze", AddressHex20: usdtHex20},
			{Day: "2026-10-05", USDTLogs: 5200000, TxHash: "t_d1", LogIndex: 4, BlockNumber: 103, BlockTime: "2026-10-05T04:00:00Z", Action: "destroy", AddressHex20: usdtHex20, AmountHex: "05f5e100"},
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

	// Verify the serial-deployer snapshot stored with scam_eth.
	deps, err := store.fakeTokenDeployerHits(ctx, []string{ethDeployer1})
	if err != nil || deps[ethDeployer1].Contracts != 4394 || deps[ethDeployer1].FirstSeen != "2026-05-21" || deps[ethDeployer1].LastSeen != "2026-08-27" {
		t.Fatalf("eth deployer = %+v, err = %v", deps, err)
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

// healthyScamRunner returns a fake runner in which every rule scanned a
// plausible amount of data on day, for tests unrelated to the guards.
func healthyScamRunner(day string) *fakeScamBQRunner {
	return &fakeScamBQRunner{
		billed:       1 << 30,
		ethPoison:    []ethPoisonRow{{Day: day, Transfers: 750000, ZeroValue: 300000}},
		ethDeployers: []fakeTokenDeployerBQRow{{Creator: "0x80d04079ffc53eb14edbeeb37279d126f45552df", Contracts: 4394, FirstSeen: "2026-05-21", LastSeen: "2026-08-27"}},
		tronPoison:   []tronPoisonRow{{Day: day, Transfers: 2100000, Candidates: 130000}},
		tronEvents:   []tronStablecoinRow{{Day: day, USDTLogs: 5000000}},
		btcRBF:       []btcRBFRow{{Day: day, Txs: 400000, RBFSignalTxs: 100000}},
	}
}

func scamJobByID(t *testing.T, runner scamBQRunner, id string) *bqDailyJob {
	t.Helper()
	for _, j := range newScamRadarJobsWithRunner(runner) {
		if j.ID() == id {
			return j
		}
	}
	t.Fatalf("no Scam Radar job %q", id)
	return nil
}

func wantSuspicious(t *testing.T, err error, wantSubstrs ...string) {
	t.Helper()
	var se *suspiciousResultError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a suspicious-result error", err)
	}
	for _, s := range wantSubstrs {
		if !strings.Contains(se.Error(), s) {
			t.Errorf("error %q does not mention %q", se.Error(), s)
		}
	}
}

// R1 must report how many official USDT/USDC transfers it scanned per day,
// and how many of them were zero-value (what the poisoning predicate pairs
// on), including days without any poisoning match (LEFT JOIN onto the
// per-day totals, like R3's day_cands), so the guards can tell "no
// poisoning" from "a filter or the q = '0' predicate matched nothing".
func TestScamEthPoisonSQLReportsDailyOfficialTransfers(t *testing.T) {
	for _, frag := range []string{
		"SELECT day, COUNT(*) AS transfers, COUNTIF(q = '0') AS zero_value FROM t GROUP BY day",
		"FROM day_transfers dt\nLEFT JOIN matched m USING (day)",
		"dt.transfers AS transfers",
		"dt.zero_value AS zero_value",
		"IFNULL(m.lookalike, '') AS lookalike",
	} {
		if !strings.Contains(scamEthPoisonSQL, frag) {
			t.Errorf("R1 scamEthPoisonSQL missing %q:\n%s", frag, scamEthPoisonSQL)
		}
	}
}

// R3 must report how many official TRON USDT Transfer logs it scanned per day,
// including days without dust candidates.
func TestScamTronPoisonSQLReportsDailyUSDTTransfers(t *testing.T) {
	for _, frag := range []string{
		"SELECT day, COUNT(*) AS transfers FROM t GROUP BY day",
		"FROM day_transfers dt\nLEFT JOIN day_cands dc USING (day)\nLEFT JOIN matched m USING (day)",
		"IFNULL(dc.candidates, 0) AS candidates",
		"dt.transfers AS transfers",
	} {
		if !strings.Contains(scamTronPoisonSQL, frag) {
			t.Errorf("R3 scamTronPoisonSQL missing %q:\n%s", frag, scamTronPoisonSQL)
		}
	}
}

func TestScamEthGuardRejectsDayWithoutOfficialTransfers(t *testing.T) {
	ctx := context.Background()
	start := civil.Date{Year: 2026, Month: time.October, Day: 4}
	end := start.AddDays(2)

	// 2026-10-04 is absent: the per-day CTE saw no official transfer at all.
	runner := &fakeScamBQRunner{billed: 7 << 30, ethPoison: []ethPoisonRow{{Day: "2026-10-05", Transfers: 812345, ZeroValue: 250000}}}
	_, billed, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, start, end, dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-04:", "0 official USDT/USDC transfers scanned", "upstream schema or format change?")
	if billed != 7<<30 {
		t.Errorf("billed = %d, want R1's %d so the billed back-off applies", billed, int64(7<<30))
	}
	if len(runner.queryCalls) != 1 || runner.queryCalls[0].sql != scamEthPoisonSQL {
		t.Errorf("guard must fail before R2 runs; query calls = %d", len(runner.queryCalls))
	}

	// An explicit zero is just as suspicious.
	runner = &fakeScamBQRunner{billed: 1, ethPoison: []ethPoisonRow{{Day: "2026-10-04", Transfers: 0}, {Day: "2026-10-05", Transfers: 9, ZeroValue: 1}}}
	_, _, err = scamJobByID(t, runner, scamEthSourceID).Run(ctx, start, end, dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "2026-10-04")

	// Days with official transfers but no poisoning match are normal.
	runner = healthyScamRunner("2026-10-04")
	runner.ethPoison = []ethPoisonRow{{Day: "2026-10-04", Transfers: 700000, ZeroValue: 200000}, {Day: "2026-10-05", Transfers: 9, ZeroValue: 1}}
	res, _, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, start, end, dailySyncIncrementalMaxBytes)
	if err != nil {
		t.Fatalf("healthy days without matches: %v", err)
	}
	if len(res.Days) != 2 || len(res.Days[0].Lookalikes) != 0 || res.Days[0].Stats["eth_poison_hits"] != 0 {
		t.Errorf("healthy no-match days = %+v, want two zero-hit days", res.Days)
	}
}

// Zero-value transfers on official USDT/USDC happen every day (that is what
// poisoners send). Official transfers without a single zero-value one mean
// the q = '0' predicate stopped matching (e.g. quantity changed format), so
// R1 would silently find no poisoning.
func TestScamEthGuardRejectsDayWithoutZeroValueTransfers(t *testing.T) {
	ctx := context.Background()
	start := civil.Date{Year: 2026, Month: time.October, Day: 4}
	runner := &fakeScamBQRunner{billed: 6 << 30, ethPoison: []ethPoisonRow{
		{Day: "2026-10-04", Transfers: 700000, ZeroValue: 0},
		{Day: "2026-10-05", Transfers: 690000, ZeroValue: 210000},
	}}
	_, billed, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, start, start.AddDays(2), dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-04:", "0 zero-value official USDT/USDC transfers scanned")
	if billed != 6<<30 {
		t.Errorf("billed = %d, want R1's %d", billed, int64(6<<30))
	}
	if len(runner.queryCalls) != 1 {
		t.Errorf("guard must fail before R2 runs; query calls = %d", len(runner.queryCalls))
	}
}

func TestScamTronGuardRejectsDayWithoutUSDTTransfers(t *testing.T) {
	ctx := context.Background()
	start := civil.Date{Year: 2026, Month: time.October, Day: 5}
	runner := &fakeScamBQRunner{billed: 5 << 30, tronPoison: []tronPoisonRow{{Day: "2026-10-05", Transfers: 0}}}
	_, billed, err := scamJobByID(t, runner, scamTronSourceID).Run(ctx, start, start.AddDays(1), dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-05:", "0 official TRON USDT Transfer logs scanned")
	if billed != 5<<30 {
		t.Errorf("billed = %d, want %d", billed, int64(5<<30))
	}

	runner = &fakeScamBQRunner{billed: 1, tronPoison: []tronPoisonRow{{Day: "2026-10-05", Transfers: 2000000, Candidates: 120000}}}
	if _, _, err := scamJobByID(t, runner, scamTronSourceID).Run(ctx, start, start.AddDays(1), dailySyncIncrementalMaxBytes); err != nil {
		t.Errorf("a day with USDT transfers and dust candidates must pass (no 4+4 match is fine): %v", err)
	}
}

// TRON dust poisoning yields dust candidates every day; USDT transfers
// without a single one mean the dust or pairing predicate stopped matching
// (e.g. data changed encoding), so R3 would silently find nothing.
func TestScamTronGuardRejectsDayWithoutDustCandidates(t *testing.T) {
	ctx := context.Background()
	start := civil.Date{Year: 2026, Month: time.October, Day: 4}
	runner := &fakeScamBQRunner{billed: 4 << 30, tronPoison: []tronPoisonRow{
		{Day: "2026-10-04", Transfers: 2000000, Candidates: 110000},
		{Day: "2026-10-05", Transfers: 2100000, Candidates: 0},
	}}
	_, billed, err := scamJobByID(t, runner, scamTronSourceID).Run(ctx, start, start.AddDays(2), dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-05:", "0 TRON USDT dust-poisoning candidates found")
	if billed != 4<<30 {
		t.Errorf("billed = %d, want %d", billed, int64(4<<30))
	}
}

func TestScamBTCGuardRejectsDayWithoutTransactions(t *testing.T) {
	ctx := context.Background()
	start := civil.Date{Year: 2026, Month: time.October, Day: 4}
	runner := &fakeScamBQRunner{billed: 1 << 28, btcRBF: []btcRBFRow{{Day: "2026-10-05", Txs: 420000, RBFSignalTxs: 90000}}}
	_, _, err := scamJobByID(t, runner, scamBTCSourceID).Run(ctx, start, start.AddDays(2), dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-04:", "0 Bitcoin transactions scanned")
}

// R4 reports, per day, how many official TRON USDT logs it scanned (any
// topic) next to the rare blacklist events, so a broken address/removed
// filter is caught even on days without events.
func TestTronStablecoinSQLReportsDailyUSDTLogs(t *testing.T) {
	for _, frag := range []string{
		"SELECT FORMAT_DATE('%Y-%m-%d', DATE(block_timestamp)) AS day, COUNT(*) AS usdt_logs\n  FROM usdt\n  GROUP BY 1",
		"FROM day_logs d\nLEFT JOIN ev e USING (day)",
		"d.usdt_logs AS usdt_logs",
		"IFNULL(e.tx_hash, '') AS tx_hash",
		"IFNULL(e.log_index, 0) AS log_index",
	} {
		if !strings.Contains(tronStablecoinSQL, frag) {
			t.Errorf("R4 tronStablecoinSQL missing %q:\n%s", frag, tronStablecoinSQL)
		}
	}
	// The topic filter applies to the events only, never to the log count.
	from, to := strings.Index(tronStablecoinSQL, "usdt AS ("), strings.Index(tronStablecoinSQL, "day_logs AS (")
	if from < 0 || to < from {
		t.Fatalf("R4 tronStablecoinSQL needs a usdt CTE followed by day_logs:\n%s", tronStablecoinSQL)
	}
	if usdt := tronStablecoinSQL[from:to]; strings.Contains(usdt, "topics[SAFE_OFFSET(0)] IN") {
		t.Errorf("the usdt CTE must not filter on topics:\n%s", usdt)
	}
}

// A day without TRON USDT blacklist events is normal, but a day without any
// official TRON USDT log means the scan matched nothing: fail for every
// window size.
func TestTronStablecoinGuardRejectsDayWithoutUSDTLogs(t *testing.T) {
	ctx := context.Background()
	end := civil.Date{Year: 2026, Month: time.October, Day: 6}

	// 1-day window, day absent from the result.
	job := scamJobByID(t, &fakeScamBQRunner{billed: 3 << 30}, tronStablecoinSourceID)
	_, billed, err := job.Run(ctx, end.AddDays(-1), end, dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-05:", "0 official TRON USDT logs scanned")
	if billed != 3<<30 {
		t.Errorf("billed = %d, want %d", billed, int64(3<<30))
	}

	// 3-day window, one day with an explicit zero.
	runner := &fakeScamBQRunner{billed: 1, tronEvents: []tronStablecoinRow{
		{Day: "2026-10-03", USDTLogs: 4800000}, {Day: "2026-10-04", USDTLogs: 0}, {Day: "2026-10-05", USDTLogs: 5100000},
	}}
	_, _, err = scamJobByID(t, runner, tronStablecoinSourceID).Run(ctx, end.AddDays(-3), end, dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for 2026-10-04:")

	// A day with official logs but no blacklist event is stored as a
	// zero-event day.
	runner = &fakeScamBQRunner{billed: 1, tronEvents: []tronStablecoinRow{{Day: "2026-10-05", USDTLogs: 5000000}}}
	res, _, err := scamJobByID(t, runner, tronStablecoinSourceID).Run(ctx, end.AddDays(-1), end, dailySyncIncrementalMaxBytes)
	if err != nil || len(res.TronEvents) != 0 || res.Rows != 0 || len(res.Days) != 1 || res.Days[0].Stats["tron_usdt_freezes"] != 0 {
		t.Errorf("day without events = %+v, %v; want a zero-event day", res, err)
	}

	// Events arrive as extra rows of their day; every day still needs logs.
	runner = &fakeScamBQRunner{billed: 1, tronEvents: []tronStablecoinRow{
		{Day: "2026-10-04", USDTLogs: 4900000},
		{Day: "2026-10-05", USDTLogs: 5000000, TxHash: "t_f1", LogIndex: 1, BlockNumber: 100, BlockTime: "2026-10-05T01:00:00Z",
			Action: "freeze", AddressHex20: "a614f803b6fd780986a42c78ec9c7f77e6ded13c"},
	}}
	res, _, err = scamJobByID(t, runner, tronStablecoinSourceID).Run(ctx, end.AddDays(-2), end, dailySyncIncrementalMaxBytes)
	if err != nil || len(res.TronEvents) != 1 || res.Days[1].Stats["tron_usdt_freezes"] != 1 {
		t.Errorf("window with one event = %+v, %v; want the freeze kept", res, err)
	}
}

// End to end through the syncer: a suspicious run stores nothing and keeps
// the cursor, and the next healthy run (after the billed back-off) stores
// the day normally.
func TestScamRadarSuspiciousRunKeepsCursorUntilHealthy(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := at(6, 0)
	runner := &fakeScamBQRunner{maxTime: now, billed: 2 << 30}
	if err := store.applyScamBatch(ctx, scamBTCSourceID, "btc", nil, "2026-08-27", "2026-09-24", at(5, 0)); err != nil {
		t.Fatal(err)
	}
	syncer := &dailyBQSyncer{store: store, job: scamJobByID(t, runner, scamBTCSourceID),
		now: func() time.Time { return now }, initialDays: 30, retentionDays: 90}

	syncer.syncOnce(ctx)
	st, _ := store.getSyncState(ctx, scamBTCSourceID)
	if st.Cursor != "2026-09-24" || !strings.HasPrefix(st.LastError, "suspicious empty result for 2026-09-25:") {
		t.Fatalf("sync_state after suspicious run = %+v", st)
	}
	var n int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scam_daily_stats WHERE day = '2026-09-25'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("suspicious run stored %d stats rows (err %v), want 0", n, err)
	}

	now = now.Add(stablecoinBilledRetry)
	runner.maxTime = now
	runner.btcRBF = []btcRBFRow{{Day: "2026-09-25", Txs: 400000, RBFSignalTxs: 90000}}
	syncer.syncOnce(ctx)
	st, _ = store.getSyncState(ctx, scamBTCSourceID)
	if st.Cursor != "2026-09-25" || st.LastError != "" {
		t.Fatalf("sync_state after healthy rerun = %+v", st)
	}
	var txs float64
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM scam_daily_stats WHERE day = '2026-09-25' AND metric = 'btc_txs'`).Scan(&txs); err != nil || txs != 400000 {
		t.Fatalf("btc_txs = %v (err %v), want 400000", txs, err)
	}
}

// R2 reads the SQD tokens_latest view (one row per contract) and returns each
// counterfeit's creator so serial deployers can be linked to their tokens.
func TestScamEthFakeTokenSQLUsesTokensLatestWithCreator(t *testing.T) {
	if scamEthTokensTable != "`bigquery-public-data.crypto_ethereum.tokens_latest`" {
		t.Errorf("scamEthTokensTable = %s, want the tokens_latest view", scamEthTokensTable)
	}
	for _, frag := range []string{
		"FROM " + scamEthTokensTable,
		"ANY_VALUE(LOWER(creator_address)) AS creator",
		"IFNULL(tok.creator, '') AS creator",
		"GROUP BY 1, 2, 3, 4",
	} {
		if !strings.Contains(scamEthFakeTokenSQL, frag) {
			t.Errorf("R2 scamEthFakeTokenSQL missing %q:\n%s", frag, scamEthFakeTokenSQL)
		}
	}
}

func TestScamEthStoresFakeTokenCreator(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	const (
		fake    = "0xdead000000000000000000000000000000000001"
		creator = "0x80d04079ffc53eb14edbeeb37279d126f45552df"
	)
	day := civil.Date{Year: 2026, Month: time.October, Day: 5}
	runner := healthyScamRunner(day.String())
	runner.ethFake = []ethFakeTokenRow{{Day: day.String(), Contract: fake, Symbol: "USDT", Creator: "0x" + strings.ToUpper(creator[2:]), Transfers: 4, Recipients: 3}}
	job := scamJobByID(t, runner, scamEthSourceID)
	res, _, err := job.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := job.Store(ctx, store, res, day.String(), day.String(), time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Store: %v", err)
	}
	var got string
	if err := store.db.QueryRowContext(ctx, `SELECT creator FROM scam_fake_tokens WHERE chain = 'eth' AND contract = ?`, fake).Scan(&got); err != nil || got != creator {
		t.Fatalf("stored creator = %q (err %v), want %q", got, err, creator)
	}

	// A malformed creator means the column changed format: fail loudly.
	runner.ethFake[0].Creator = "80d04079ffc53eb14edbeeb37279d126f45552df"
	if _, _, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
		t.Fatal("Run with a malformed creator: want an error")
	}
}

// The serial-deployer corpus is a full daily snapshot of tokens_latest:
// creators of at least scamFakeTokenDeployerMinContracts counterfeit-symbol
// contracts (same symbol rule as R2) where counterfeit-symbol tokens make up
// at least 1/scamFakeTokenDeployerMinShareDenom of all distinct tokens
// created by that address, excluding the official USDT/USDC contracts,
// contracts without a known creator, and general-purpose factory contracts.
func TestScamEthFakeTokenDeployersSQL(t *testing.T) {
	if scamFakeTokenDeployerMinContracts != 3 {
		t.Errorf("scamFakeTokenDeployerMinContracts = %d, want 3", scamFakeTokenDeployerMinContracts)
	}
	if scamFakeTokenDeployerMinShareDenom != 5 {
		t.Errorf("scamFakeTokenDeployerMinShareDenom = %d, want 5 (20%% counterfeit share floor)", scamFakeTokenDeployerMinShareDenom)
	}
	for _, frag := range []string{
		"WITH by_creator AS (",
		"FROM " + scamEthTokensTable,
		counterfeitSymbolPredicate,
		"LOWER(address) NOT IN ('" + usdtContract + "', '" + usdcContract + "')",
		"creator_address IS NOT NULL",
		"COUNT(DISTINCT IF(is_counterfeit, address, NULL)) AS contracts",
		"FROM by_creator",
		"GROUP BY 1",
		fmt.Sprintf("HAVING contracts >= %d", scamFakeTokenDeployerMinContracts),
		fmt.Sprintf("AND contracts * %d >= COUNT(DISTINCT address)", scamFakeTokenDeployerMinShareDenom),
	} {
		if !strings.Contains(scamEthFakeTokenDeployersSQL, frag) {
			t.Errorf("scamEthFakeTokenDeployersSQL missing %q:\n%s", frag, scamEthFakeTokenDeployersSQL)
		}
	}
	if !strings.Contains(scamEthFakeTokenSQL, counterfeitSymbolPredicate) {
		t.Errorf("R2 must share counterfeitSymbolPredicate with the deployer corpus:\n%s", scamEthFakeTokenSQL)
	}
}

// allow_zero_candidates lets operators bypass the per-day candidate /
// zero-value predicate floor if an upstream change legitimately zeroes it out,
// while still enforcing the hard table-health floor (dayTransfers >= 1).
func TestScamRadarAllowZeroCandidatesEscapeHatch(t *testing.T) {
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: time.October, Day: 5}
	d := day.String()

	// TRON: 0 dust candidates on a non-empty day fails by default, passes with
	// allowZeroCandidates, and still fails when the whole day has 0 transfers.
	runner := healthyScamRunner(d)
	runner.tronPoison = []tronPoisonRow{{Day: d, Transfers: 4_000_000, Candidates: 0, Hits: 0, Victims: 0}}
	tronJob := scamJobByID(t, runner, scamTronSourceID)
	if _, _, err := tronJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
		t.Fatal("tron 0 candidates without allowZeroCandidates: want error")
	}
	tronJob.allowZeroCandidates = true
	if _, _, err := tronJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err != nil {
		t.Fatalf("tron 0 candidates with allowZeroCandidates: unexpected error: %v", err)
	}
	runner.tronPoison = []tronPoisonRow{{Day: d, Transfers: 0, Candidates: 0, Hits: 0, Victims: 0}}
	if _, _, err := tronJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
		t.Fatal("tron 0 total transfers must still fail even with allowZeroCandidates")
	}

	// ETH: 0 zero-value transfers on a non-empty day fails by default, passes
	// with allowZeroCandidates, and still fails when Transfers == 0.
	runner = healthyScamRunner(d)
	runner.ethPoison = []ethPoisonRow{{Day: d, Transfers: 900_000, ZeroValue: 0, Hits: 0, Victims: 0}}
	ethJob := scamJobByID(t, runner, scamEthSourceID)
	if _, _, err := ethJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
		t.Fatal("eth 0 zero-value without allowZeroCandidates: want error")
	}
	ethJob.allowZeroCandidates = true
	if _, _, err := ethJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err != nil {
		t.Fatalf("eth 0 zero-value with allowZeroCandidates: unexpected error: %v", err)
	}
	runner.ethPoison = []ethPoisonRow{{Day: d, Transfers: 0, ZeroValue: 0, Hits: 0, Victims: 0}}
	if _, _, err := ethJob.Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
		t.Fatal("eth 0 total transfers must still fail even with allowZeroCandidates")
	}
}

func TestScamEthGuardRejectsEmptyDeployerSnapshot(t *testing.T) {
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: time.October, Day: 5}
	runner := healthyScamRunner(day.String())
	runner.ethDeployers = nil
	runner.billed = 3 << 30
	_, billed, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes)
	wantSuspicious(t, err, "suspicious empty result for crypto_ethereum.tokens_latest:", "0 serial counterfeit USDT/USDC deployers found")
	if billed != 3*(3<<30) {
		t.Errorf("billed = %d, want the three scam_eth queries (%d)", billed, int64(3*(3<<30)))
	}
	if n := len(runner.queryCalls); n != 3 || runner.queryCalls[2].sql != scamEthFakeTokenDeployersSQL {
		t.Errorf("query calls = %d, want R1, R2, deployers", n)
	}
}

func TestScamEthRejectsMalformedDeployerRows(t *testing.T) {
	ctx := context.Background()
	day := civil.Date{Year: 2026, Month: time.October, Day: 5}
	const creator = "0x80d04079ffc53eb14edbeeb37279d126f45552df"
	for name, bad := range map[string]fakeTokenDeployerBQRow{
		"bad address":     {Creator: "80d04079ffc53eb14edbeeb37279d126f45552df", Contracts: 5, FirstSeen: "2026-05-21", LastSeen: "2026-08-27"},
		"below the floor": {Creator: creator, Contracts: 2, FirstSeen: "2026-05-21", LastSeen: "2026-08-27"},
		"bad first_seen":  {Creator: creator, Contracts: 5, FirstSeen: "", LastSeen: "2026-08-27"},
		"bad last_seen":   {Creator: creator, Contracts: 5, FirstSeen: "2026-05-21", LastSeen: "2026-08-27T00:00:00Z"},
	} {
		runner := healthyScamRunner(day.String())
		runner.ethDeployers = []fakeTokenDeployerBQRow{bad}
		if _, _, err := scamJobByID(t, runner, scamEthSourceID).Run(ctx, day, day.AddDays(1), dailySyncIncrementalMaxBytes); err == nil {
			t.Errorf("%s: Run = nil error, want the row rejected", name)
		}
	}
}
