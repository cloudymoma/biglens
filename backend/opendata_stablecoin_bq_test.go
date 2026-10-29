package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"golang.org/x/crypto/sha3"
)

func keccakTopic(sig string) string {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(sig))
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

func TestStableBQTopicsMatchKeccak(t *testing.T) {
	want := map[string]string{
		"Transfer(address,address,uint256)":     topicERC20Transfer,
		"Issue(uint256)":                        topicUSDTIssue,
		"Redeem(uint256)":                       topicUSDTRedeem,
		"Deprecate(address)":                    topicUSDTDeprecate,
		"Params(uint256,uint256)":               topicUSDTParams,
		"Pause()":                               topicStablePause,
		"Unpause()":                             topicStableUnpause,
		"Mint(address,address,uint256)":         topicFiatTokenMint,
		"Burn(address,uint256)":                 topicFiatTokenBurn,
		"MinterConfigured(address,uint256)":     topicFiatTokenMinterConfigured,
		"MinterRemoved(address)":                topicFiatTokenMinterRemoved,
		"MasterMinterChanged(address)":          topicFiatTokenMasterMinterChanged,
		"BlacklisterChanged(address)":           topicFiatTokenBlacklisterChanged,
		"PauserChanged(address)":                topicFiatTokenPauserChanged,
		"RescuerChanged(address)":               topicFiatTokenRescuerChanged,
		"OwnershipTransferred(address,address)": topicOwnershipTransferred,
		"Upgraded(address)":                     topicProxyUpgraded,
		"AdminChanged(address,address)":         topicProxyAdminChanged,
	}
	for sig, pinned := range want {
		if got := keccakTopic(sig); got != pinned {
			t.Errorf("keccak(%q) = %s, pinned = %s", sig, got, pinned)
		}
	}
}

func TestStableSQLResultTypesMatchStructs(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		typ  reflect.Type
	}{
		{"stable_eth transfers", stableEthTransfersSQL(), reflect.TypeFor[stableEthTransferAggRow]()},
		{"stable_eth logs", stableEthLogsSQL(), reflect.TypeFor[stableEthLogRow]()},
		{"stable_tron transfers", stableTronTransfersSQL(), reflect.TypeFor[stableTronTransferAggRow]()},
		{"stable_tron events", stableTronEventsSQL(), reflect.TypeFor[stableTronLogRow]()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < tc.typ.NumField(); i++ {
				f := tc.typ.Field(i)
				tag := f.Tag.Get("bigquery")
				if tag == "" || tag == "-" {
					t.Fatalf("struct %s field %s missing bigquery tag", tc.typ.Name(), f.Name)
				}
				aliasRe := regexp.MustCompile(`(?i)\b(?:AS\s+` + regexp.QuoteMeta(tag) + `|` + regexp.QuoteMeta(tag) + `\b)`)
				if !aliasRe.MatchString(tc.sql) {
					t.Errorf("struct %s field %s (bigquery:%q) not projected in SQL", tc.typ.Name(), f.Name, tag)
				}
				if f.Type.Kind() == reflect.Int64 {
					defRe := regexp.MustCompile(`(?im)([^\n,]+?)\s+AS\s+` + regexp.QuoteMeta(tag) + `\b`)
					matches := defRe.FindAllStringSubmatch(tc.sql, -1)
					for _, m := range matches {
						expr := strings.TrimSpace(m[1])
						if strings.Contains(expr, "/") && !strings.HasPrefix(strings.ToUpper(expr), "CAST(") {
							t.Errorf("int64 field %s (bigquery:%q) uses float division '/' in SQL expression %q",
								f.Name, tag, expr)
						}
					}
				}
			}
		})
	}
}

type fakeStableEthSource struct {
	mu            sync.Mutex
	ready         bool
	transfers     []stableEthTransferAggRow
	logs          []stableEthLogRow
	dryA          int64
	dryB          int64
	billedA       int64
	billedB       int64
	errA          error
	errB          error
	readyCalls    int
	dryRunCalls   int
	fetchCalls    int
	lastMaxBytesA int64
	lastMaxBytesB int64
	blockFetch    chan struct{}
	fetchHit      chan struct{}
}

func (f *fakeStableEthSource) Ready(_ context.Context, _ civil.Date) (bool, error) {
	f.mu.Lock()
	f.readyCalls++
	f.mu.Unlock()
	return f.ready, nil
}
func (f *fakeStableEthSource) DryRunTransfers(_ context.Context, _, _ civil.Date) (int64, error) {
	f.mu.Lock()
	f.dryRunCalls++
	d := f.dryA
	f.mu.Unlock()
	if d > 0 {
		return d, nil
	}
	return 1 << 30, nil
}
func (f *fakeStableEthSource) DryRunLogs(_ context.Context, _, _ civil.Date) (int64, error) {
	f.mu.Lock()
	f.dryRunCalls++
	d := f.dryB
	f.mu.Unlock()
	if d > 0 {
		return d, nil
	}
	return 1 << 30, nil
}
func (f *fakeStableEthSource) FetchTransfers(_ context.Context, _, _ civil.Date, maxBytes int64) ([]stableEthTransferAggRow, int64, error) {
	f.mu.Lock()
	f.fetchCalls++
	f.lastMaxBytesA = maxBytes
	hit := f.fetchHit
	blk := f.blockFetch
	f.mu.Unlock()
	if hit != nil {
		close(hit)
	}
	if blk != nil {
		<-blk
	}
	return f.transfers, f.billedA, f.errA
}
func (f *fakeStableEthSource) FetchLogs(_ context.Context, _, _ civil.Date, maxBytes int64) ([]stableEthLogRow, int64, error) {
	f.mu.Lock()
	f.fetchCalls++
	f.lastMaxBytesB = maxBytes
	f.mu.Unlock()
	return f.logs, f.billedB, f.errB
}

func TestStableEthJobAndSilentZeroGuard(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	d0 := civil.Date{Year: 2026, Month: 10, Day: 18}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 19}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	// Ethereum USDT Issue emits Issue(uint256) without a Transfer from 0x0 (§10.2, Appendix E.2).
	issueHex := "0x" + strings.Repeat("0", 50) + "038d7ea4c68000" // 1,000,000,000.000000 (1e15 raw)
	newPauserTopic := "0x0000000000000000000000004914f61d25e5c567143774b76edbf4d5109a8566"
	src := &fakeStableEthSource{
		ready: true,
		transfers: []stableEthTransferAggRow{
			{
				Day: "2026-10-18", Token: usdtContract, Transfers: 150_000, ZeroValue: 1200,
				VolumeRaw: "5000000000000000", Senders: 40_000, Receivers: 45_000,
				Ge1MCount: 120, Ge1MVolumeRaw: "2000000000000000", MintedRaw: "0", BurnedRaw: "0",
				TopTransfers: []stableEthTopTransferBQ{
					{
						TxHash:    "0x" + strings.Repeat("a", 64),
						LogIndex:  3,
						TS:        "2026-10-18T10:00:00Z",
						Src:       "0x" + strings.Repeat("1", 40),
						Dst:       "0x" + strings.Repeat("2", 40),
						AmountRaw: "50000000000000",
					},
				},
			},
			{
				Day: "2026-10-18", Token: usdcContract, Transfers: 90_000, ZeroValue: 500,
				VolumeRaw: "3000000000000000", Senders: 25_000, Receivers: 28_000,
				Ge1MCount: 80, Ge1MVolumeRaw: "1200000000000000", MintedRaw: "200000000000000", BurnedRaw: "100000000000000",
			},
			{
				Day: "2026-10-18", Token: eurcContract, Transfers: 500, ZeroValue: 2,
				VolumeRaw: "20000000000000", Senders: 180, Receivers: 210,
				Ge1MCount: 2, Ge1MVolumeRaw: "5000000000000", MintedRaw: "1000000000000", BurnedRaw: "0",
			},
		},
		logs: []stableEthLogRow{
			{
				Day: "2026-10-18", TxHash: "0x626a2bebdd028485d5def5b6b2a67fcb602db2530b4e949bc1ac776f89b0d317",
				LogIndex: 1, BlockNumber: 21000001, TS: "2026-10-18T14:00:00Z",
				Address: usdtContract, Topic0: topicUSDTIssue, Data: issueHex,
			},
			{
				Day: "2026-10-18", TxHash: "0x" + strings.Repeat("b", 64),
				LogIndex: 2, BlockNumber: 21000050, TS: "2026-10-18T15:00:00Z",
				Address: usdcContract, Topic0: topicFiatTokenPauserChanged, Topic1: newPauserTopic,
			},
		},
		billedA: 500 << 20,
		billedB: 400 << 20,
	}

	job := &stableEthJob{store: store, src: src, minUSDTTransfers: stableEthUSDTMinDailyTransfers}
	res, billed, err := job.Run(ctx, d0, d1, 10<<30)
	if err != nil {
		t.Fatalf("stable_eth Run failed: %v", err)
	}
	if billed != 900<<20 {
		t.Errorf("billed = %d, want %d", billed, 900<<20)
	}
	if err := job.Store(ctx, store, res, "2026-10-18", "2026-10-18", now); err != nil {
		t.Fatalf("stable_eth Store failed: %v", err)
	}

	days, err := store.stableOnchainDaily(ctx, "usdt", "2026-10-18")
	if err != nil || len(days) != 1 {
		t.Fatalf("stableOnchainDaily(usdt) = %+v, %v; want 1 row", days, err)
	}
	if days[0].MintedNative == nil || *days[0].MintedNative != 1_000_000_000 {
		t.Errorf("ETH USDT MintedNative from Issue log = %v, want 1e9", days[0].MintedNative)
	}
	lt, err := store.stableLargeTransfers(ctx, "usdt", "2026-10-18", 20)
	if err != nil || len(lt) != 1 || lt[0].AmountNative != 50_000_000 {
		t.Errorf("stableLargeTransfers(usdt) = %+v, %v", lt, err)
	}
	se, err := store.stableSupplyEvents(ctx, "usdt", "2026-10-18", 20)
	if err != nil || len(se) != 1 || se[0].Kind != "issue" || se[0].AmountNative != 1_000_000_000 {
		t.Errorf("stableSupplyEvents(usdt) = %+v, %v", se, err)
	}
	ce, err := store.stableControlEvents(ctx, "usdc", 20)
	if err != nil || len(ce) != 1 || ce[0].Source != "bigquery" || ce[0].Field != "pauser" || ce[0].TxHash == "" {
		t.Errorf("stableControlEvents(usdc) = %+v, %v", ce, err)
	}

	// Silent-zero guard: drop USDT transfers below 10,000 -> fails.
	src.transfers[0].Transfers = 5_000
	if _, _, err := job.Run(ctx, d0, d1, 10<<30); err == nil || !strings.Contains(err.Error(), "suspicious empty result") {
		t.Errorf("expected silent-zero guard error when USDT transfers < 10,000, got %v", err)
	}
}

type fakeStableTronSource struct {
	ready         bool
	transfers     []stableTronTransferAggRow
	logs          []stableTronLogRow
	dryA          int64
	dryB          int64
	billedA       int64
	billedB       int64
	lastMaxBytesA int64
	lastMaxBytesB int64
}

func (f *fakeStableTronSource) Ready(_ context.Context, _ civil.Date) (bool, error) {
	return f.ready, nil
}
func (f *fakeStableTronSource) DryRunTransfers(_ context.Context, _, _ civil.Date) (int64, error) {
	if f.dryA > 0 {
		return f.dryA, nil
	}
	return 1 << 30, nil
}
func (f *fakeStableTronSource) DryRunEvents(_ context.Context, _, _ civil.Date) (int64, error) {
	if f.dryB > 0 {
		return f.dryB, nil
	}
	return 1 << 30, nil
}
func (f *fakeStableTronSource) FetchTransfers(_ context.Context, _, _ civil.Date, maxBytes int64) ([]stableTronTransferAggRow, int64, error) {
	f.lastMaxBytesA = maxBytes
	return f.transfers, f.billedA, nil
}
func (f *fakeStableTronSource) FetchEvents(_ context.Context, _, _ civil.Date, maxBytes int64) ([]stableTronLogRow, int64, error) {
	f.lastMaxBytesB = maxBytes
	return f.logs, f.billedB, nil
}

func TestStableTronJobReconciliationAndOverflowGuard(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	d0 := civil.Date{Year: 2026, Month: 10, Day: 18}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 19}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	// TRON USDT emits Transfer(0x0 -> owner, 1e15) AND Issue(1e15) in the same tx (§10.3, Appendix E.2).
	issueHex := "0x" + strings.Repeat("0", 50) + "038d7ea4c68000" // 1e15 raw = 1,000,000,000 USDT
	src := &fakeStableTronSource{
		ready: true,
		transfers: []stableTronTransferAggRow{
			{
				Day: "2026-10-18", Transfers: 1_800_000, ZeroValue: 5000,
				VolumeRaw: "12000000000000000", Senders: 450_000, Receivers: 500_000,
				Ge1MCount: 300, Ge1MVolumeRaw: "4000000000000000",
				MintedRaw: "1000000000000000", BurnedRaw: "0", OverflowRows: 0,
				TopTransfers: []stableTronTopTransferBQ{
					{
						TxHash:    strings.Repeat("c", 64),
						LogIndex:  0,
						TS:        "2026-10-18T11:00:00Z",
						SrcHex20:  "0fa695d6b065707cb4e0ef73b751c93347682bf2",
						DstHex20:  strings.Repeat("3", 40),
						AmountRaw: "100000000000000",
					},
				},
			},
		},
		logs: []stableTronLogRow{
			{
				Day: "2026-10-18", TxHash: "8330cbe9378786736b45caeefdb0f39d321def9486ce6d40f9ad2307349a8d26",
				LogIndex: 1, BlockNumber: 65000000, TS: "2026-10-18T09:00:00Z",
				Topic0: topicUSDTIssue, Data: issueHex,
			},
		},
		billedA: 600 << 20,
		billedB: 200 << 20,
	}

	job := &stableTronJob{store: store, src: src, minTransfers: stableTronUSDTMinDailyTransfers}
	res, _, err := job.Run(ctx, d0, d1, 10<<30)
	if err != nil {
		t.Fatalf("stable_tron Run failed: %v", err)
	}
	if err := job.Store(ctx, store, res, "2026-10-18", "2026-10-18", now); err != nil {
		t.Fatalf("stable_tron Store failed: %v", err)
	}

	days, err := store.stableOnchainDaily(ctx, "usdt", "2026-10-18")
	if err != nil || len(days) != 1 {
		t.Fatalf("stableOnchainDaily(usdt, tron) = %+v, %v", days, err)
	}
	if days[0].MintedNative == nil || *days[0].MintedNative != 1_000_000_000 {
		t.Errorf("TRON USDT MintedNative = %v, want 1e9 (reconciled, not double-counted)", days[0].MintedNative)
	}

	// Mismatched Issue vs Transfer from 0x0 -> supply_event_mismatch, nothing stored.
	src.logs[0].Data = "0x" + strings.Repeat("0", 50) + "071afd498d0000" // 2e15 != 1e15
	if _, _, err := job.Run(ctx, d0, d1, 10<<30); err == nil || !strings.Contains(err.Error(), "supply_event_mismatch") {
		t.Errorf("expected supply_event_mismatch, got %v", err)
	}
	src.logs[0].Data = issueHex

	// Bit-63 overflow row -> amount_overflow, nothing stored.
	src.transfers[0].OverflowRows = 1
	if _, _, err := job.Run(ctx, d0, d1, 10<<30); err == nil || !strings.Contains(err.Error(), "amount_overflow") {
		t.Errorf("expected amount_overflow, got %v", err)
	}
}

func TestStableBQJobsDisabledByDefaultAndOnchainHandler(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	th, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)

	// Default config: Jobs=false, no sync jobs wired.
	if th.stable.settings.Jobs {
		t.Fatal("expected stablecoin.bigquery.jobs to default to false")
	}
	if fns := stablecoinSyncJobs(th.stable.settings, th.stable.store, &BQClient{}); len(fns) != 0 {
		t.Fatalf("stablecoinSyncJobs with jobs=false returned %d funcs, want 0", len(fns))
	}

	// Stop flag and resume CLI.
	flags := newStableStopFlags(th.stable.store)
	if err := flags.recordFailure(ctx, stableEthJobID, syncFailureRecord{
		At:          clock,
		Message:     "supply_event_mismatch",
		BilledBytes: 50 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	if err := flags.stop(ctx, stableEthJobID, "stopped after 4 billed failures"); err != nil {
		t.Fatal(err)
	}
	if err := stableResumeJob(ctx, th.stable.store, stableEthJobID); err != nil {
		t.Fatal(err)
	}
	if _, stopped, _ := flags.stopped(ctx, stableEthJobID); stopped {
		t.Error("expected stopped flag to be cleared by stableResumeJob")
	}

	// /onchain before any rows: data_available=false.
	recCold := stableGet(t, th.StablecoinOnchain, "/api/opendata/stablecoin/onchain?asset=usdt&days=30")
	if recCold.Code != http.StatusOK {
		t.Fatalf("/onchain status = %d, want 200", recCold.Code)
	}
	onCold := decodeStable[StableOnchainResponse](t, recCold)
	if onCold.DataAvailable {
		t.Errorf("empty store /onchain DataAvailable = true, want false")
	}

	// Seed one on-chain day and verify /onchain and /meta report data_available=true.
	if err := th.stable.store.upsertStableOnchainDaily(ctx, []stableOnchainDailyRow{
		{
			Day: "2026-10-18", Asset: "usdt", Chain: "eth",
			Transfers: 150_000, ZeroValue: 1200, VolumeRaw: "5000000000000000",
			Senders: 40_000, Receivers: 45_000, Ge1MCount: 120, Ge1MVolumeRaw: "2000000000000000",
			MintedRaw: "1000000000000000", BurnedRaw: "0", SyncedAt: clock,
		},
	}); err != nil {
		t.Fatal(err)
	}
	th.cache = NewCache(time.Minute)
	onWarm := decodeStable[StableOnchainResponse](t, stableGet(t, th.StablecoinOnchain, "/api/opendata/stablecoin/onchain?asset=usdt&days=30"))
	if !onWarm.DataAvailable || len(onWarm.Daily) != 1 || onWarm.Daily[0].Transfers != 150_000 {
		t.Errorf("/onchain after seed = %+v, want DataAvailable=true and 1 daily row", onWarm)
	}
	meta := decodeStable[StableMeta](t, stableGet(t, th.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	if !meta.BigQuery.DataAvailable {
		t.Errorf("/meta BigQuery.DataAvailable = false, want true when rows exist")
	}
	if bad := stableGet(t, th.StablecoinOnchain, "/api/opendata/stablecoin/onchain?asset=doge"); bad.Code != http.StatusBadRequest {
		t.Errorf("/onchain invalid asset = %d, want 400", bad.Code)
	}
}

func stableBQPost(t *testing.T, fn http.HandlerFunc, target, remoteAddr, contentType, actionHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodPost, target, rd)
	req.Host = "localhost:1983"
	req.RemoteAddr = remoteAddr
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if actionHeader != "" {
		req.Header.Set("X-BigLens-Action", actionHeader)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestStableBQEstimateRunAndAbuseGuards(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)
	th, _, _ := newStablePegTestHandler(t, StablecoinConfig{}, &clock)

	// Seed cursor to 2026-10-17 so the next window is a single day [2026-10-18, 2026-10-19).
	if err := ensureSyncRow(ctx, th.stable.store.db, stableEthJobID); err != nil {
		t.Fatal(err)
	}
	if err := updateSyncWatermarksTx(ctx, th.stable.store.db, stableEthJobID, "2026-07-21", "2026-10-17", 10, clock); err != nil {
		t.Fatal(err)
	}

	issueHex := "0x" + strings.Repeat("0", 50) + "038d7ea4c68000"
	ethSrc := &fakeStableEthSource{
		ready:   true,
		dryA:    600 << 20,
		dryB:    400 << 20,
		billedA: 500 << 20,
		billedB: 400 << 20,
		transfers: []stableEthTransferAggRow{
			{
				Day: "2026-10-18", Token: usdtContract, Transfers: 150_000, ZeroValue: 1200,
				VolumeRaw: "5000000000000000", Senders: 40_000, Receivers: 45_000,
				Ge1MCount: 120, Ge1MVolumeRaw: "2000000000000000", MintedRaw: "0", BurnedRaw: "0",
			},
			{
				Day: "2026-10-18", Token: usdcContract, Transfers: 90_000, ZeroValue: 500,
				VolumeRaw: "3000000000000000", Senders: 25_000, Receivers: 28_000,
				Ge1MCount: 80, Ge1MVolumeRaw: "1200000000000000", MintedRaw: "200000000000000", BurnedRaw: "100000000000000",
			},
			{
				Day: "2026-10-18", Token: eurcContract, Transfers: 500, ZeroValue: 2,
				VolumeRaw: "20000000000000", Senders: 180, Receivers: 210,
				Ge1MCount: 2, Ge1MVolumeRaw: "5000000000000", MintedRaw: "1000000000000", BurnedRaw: "0",
			},
		},
		logs: []stableEthLogRow{
			{
				Day: "2026-10-18", TxHash: "0x626a2bebdd028485d5def5b6b2a67fcb602db2530b4e949bc1ac776f89b0d317",
				LogIndex: 1, BlockNumber: 21000001, TS: "2026-10-18T14:00:00Z",
				Address: usdtContract, Topic0: topicUSDTIssue, Data: issueHex,
			},
		},
	}
	tronSrc := &fakeStableTronSource{ready: true}
	th.stable.bqRunner = newStableBQRunner(th.stable.settings, th.stable.store, ethSrc, tronSrc, func() time.Time { return clock })

	// 1. Abuse guards: non-loopback -> 403 (even with X-Forwarded-For), missing header or content-type -> 400.
	reqNonLoop := httptest.NewRequest(http.MethodPost, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", nil)
	reqNonLoop.RemoteAddr = "192.168.1.25:54321"
	reqNonLoop.Header.Set("X-Forwarded-For", "127.0.0.1")
	reqNonLoop.Header.Set("Content-Type", "application/json")
	reqNonLoop.Header.Set("X-BigLens-Action", "stablecoin-bq")
	recNonLoop := httptest.NewRecorder()
	th.StablecoinBQEstimate(recNonLoop, reqNonLoop)
	if recNonLoop.Code != http.StatusForbidden {
		t.Fatalf("non-loopback estimate = %d, want 403", recNonLoop.Code)
	}
	// DNS-rebinding guard: loopback RemoteAddr with a non-loopback Host or Origin -> 403.
	reqRebind := httptest.NewRequest(http.MethodPost, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", nil)
	reqRebind.RemoteAddr = "127.0.0.1:54321"
	reqRebind.Host = "evil.example:1983"
	reqRebind.Header.Set("Content-Type", "application/json")
	reqRebind.Header.Set("X-BigLens-Action", "stablecoin-bq")
	recRebind := httptest.NewRecorder()
	th.StablecoinBQEstimate(recRebind, reqRebind)
	if recRebind.Code != http.StatusForbidden {
		t.Fatalf("DNS-rebound Host estimate = %d, want 403", recRebind.Code)
	}
	if rec := stableBQPost(t, th.StablecoinBQEstimate, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", "127.0.0.1:1234", "application/json", "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing action header = %d, want 400", rec.Code)
	}
	if rec := stableBQPost(t, th.StablecoinBQEstimate, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", "[::1]:1234", "text/plain", "stablecoin-bq", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong content-type = %d, want 400", rec.Code)
	}
	if rec := stableBQPost(t, th.StablecoinBQEstimate, "/api/opendata/stablecoin/bigquery/estimate?job=invalid", "127.0.0.1:1234", "application/json", "stablecoin-bq", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid job = %d, want 400", rec.Code)
	}

	// 2. Schedule off: next_check_at is null, next_new_day_at is next 00:00 UTC.
	meta0 := decodeStable[StableMeta](t, stableGet(t, th.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	ethSt0 := meta0.BigQuery.Statuses[stableEthJobID]
	if ethSt0.ScheduleEnabled {
		t.Errorf("schedule_enabled = true, want false when jobs=false")
	}
	if ethSt0.NextCheckAt != nil {
		t.Errorf("next_check_at with schedule off = %v, want nil", *ethSt0.NextCheckAt)
	}
	if ethSt0.NextNewDayAt != "2026-10-20T00:00:00Z" {
		t.Errorf("next_new_day_at = %q, want 2026-10-20T00:00:00Z", ethSt0.NextNewDayAt)
	}

	// 3. Estimate path makes ONLY dry-run calls (zero Ready and zero Fetch calls).
	recEst := stableBQPost(t, th.StablecoinBQEstimate, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", "{}")
	if recEst.Code != http.StatusOK {
		t.Fatalf("estimate status = %d (%s), want 200", recEst.Code, recEst.Body.String())
	}
	est := decodeStable[StableBQEstimateResponse](t, recEst)
	wantEstBytes := int64((600 + 400) << 20)
	if est.WindowFrom != "2026-10-18" || est.WindowTo != "2026-10-18" || est.EstimatedBytes != wantEstBytes || !est.FitsBudget || est.NothingToDo {
		t.Fatalf("estimate = %+v, want window 2026-10-18..2026-10-18, bytes=%d, fits_budget=true, nothing_to_do=false", est, wantEstBytes)
	}
	if est.PricingNote != "Estimated at $6.25/TiB; BigQuery bills actual bytes scanned." {
		t.Errorf("PricingNote = %q, want trailing period", est.PricingNote)
	}
	if ethSrc.readyCalls != 0 || ethSrc.fetchCalls != 0 || ethSrc.dryRunCalls != 2 {
		t.Fatalf("estimate source calls: ready=%d fetch=%d dryRun=%d, want 0, 0, 2", ethSrc.readyCalls, ethSrc.fetchCalls, ethSrc.dryRunCalls)
	}

	// 4. "Estimate changed" (> +10%) gets 409 and runs nothing.
	recChanged := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 500000000}`)
	if recChanged.Code != http.StatusConflict || !strings.Contains(recChanged.Body.String(), "estimate changed") {
		t.Fatalf("estimate changed run = %d (%s), want 409 estimate changed", recChanged.Code, recChanged.Body.String())
	}
	if ethSrc.fetchCalls != 0 {
		t.Fatalf("fetchCalls after 409 estimate changed = %d, want 0", ethSrc.fetchCalls)
	}

	// 5. Stopped job gets 409 with stop reason and runs nothing.
	flags := newStableStopFlags(th.stable.store)
	if err := flags.stop(ctx, stableEthJobID, "stopped after 4 billed failures"); err != nil {
		t.Fatal(err)
	}
	recStopped := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 1048576000}`)
	if recStopped.Code != http.StatusConflict || !strings.Contains(recStopped.Body.String(), "stopped after 4 billed failures") {
		t.Fatalf("stopped job run = %d (%s), want 409 with stop reason", recStopped.Code, recStopped.Body.String())
	}
	if err := stableResumeJob(ctx, th.stable.store, stableEthJobID); err != nil {
		t.Fatal(err)
	}

	// 6. Budget refusal: when estimate exceeds budget, run is refused (409) and no query is made.
	ethSrc.dryA = 600 << 30 // 600 GiB > 512 GiB default budget
	recOver := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 650000000000}`)
	if recOver.Code != http.StatusConflict {
		t.Fatalf("over-budget run = %d (%s), want 409", recOver.Code, recOver.Body.String())
	}
	if ethSrc.fetchCalls != 0 {
		t.Fatalf("fetchCalls on over-budget run = %d, want 0", ethSrc.fetchCalls)
	}
	ethSrc.dryA = 600 << 20
	_ = th.stable.store.setSyncError(ctx, stableEthJobID, "")

	// 7. Manual run works with schedule off; concurrent second run gets 409; run within 5 min gets 429; status updates after completion.
	ethSrc.blockFetch = make(chan struct{})
	ethSrc.fetchHit = make(chan struct{})
	recRun := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 1048576000}`)
	if recRun.Code != http.StatusAccepted {
		t.Fatalf("manual run status = %d (%s), want 202", recRun.Code, recRun.Body.String())
	}
	<-ethSrc.fetchHit

	// While in flight: /meta reports running=true, and a second concurrent run gets 409.
	metaRunning := decodeStable[StableMeta](t, stableGet(t, th.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	if !metaRunning.BigQuery.Statuses[stableEthJobID].Running {
		t.Errorf("/meta while in flight: Running = false, want true")
	}
	recConcurrent := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 1048576000}`)
	if recConcurrent.Code != http.StatusConflict {
		t.Fatalf("concurrent run = %d, want 409", recConcurrent.Code)
	}

	close(ethSrc.blockFetch)
	th.stable.bqRunner.Wait()

	// Within 5 minutes: gets 429.
	clock = clock.Add(2 * time.Minute)
	recRateLimit := stableBQPost(t, th.StablecoinBQRun, "/api/opendata/stablecoin/bigquery/run?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", `{"confirmed_estimate_bytes": 1048576000}`)
	if recRateLimit.Code != http.StatusTooManyRequests {
		t.Fatalf("run within 5m = %d, want 429", recRateLimit.Code)
	}

	// Status fields after manual run completion.
	metaDone := decodeStable[StableMeta](t, stableGet(t, th.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	stDone := metaDone.BigQuery.Statuses[stableEthJobID]
	wantBilled := int64(900 << 20)
	if stDone.Running || stDone.SyncedThrough != "2026-10-18" || stDone.LastRunTrigger != "manual" || stDone.LastRunResult != "ok" {
		t.Fatalf("status after manual run = %+v, want running=false, synced_through=2026-10-18, trigger=manual, result=ok", stDone)
	}
	if stDone.LastRunBytesBilled == nil || *stDone.LastRunBytesBilled != wantBilled {
		t.Fatalf("LastRunBytesBilled = %v, want %d", stDone.LastRunBytesBilled, wantBilled)
	}
	if metaDone.BigQuery.BudgetUsedBytes != wantBilled || metaDone.BigQuery.BudgetLimitBytes != 512<<30 {
		t.Fatalf("budget_used_bytes=%d budget_limit_bytes=%d, want %d and %d",
			metaDone.BigQuery.BudgetUsedBytes, metaDone.BigQuery.BudgetLimitBytes, wantBilled, int64(512<<30))
	}

	// Now synced through yesterday (2026-10-18): estimate reports nothing_to_do=true.
	recEstDone := stableBQPost(t, th.StablecoinBQEstimate, "/api/opendata/stablecoin/bigquery/estimate?job=stable_eth", "127.0.0.1:1234", "application/json", "stablecoin-bq", "{}")
	estDone := decodeStable[StableBQEstimateResponse](t, recEstDone)
	if !estDone.NothingToDo {
		t.Errorf("estimate after catching up through yesterday: NothingToDo = false, want true")
	}

	// 8. Schedule on: next_check_at equals last tick + 1 h.
	th.stable.settings.Jobs = true
	th.stable.bqRunner.settings.Jobs = true
	tickTime := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	clock = tickTime
	th.stable.bqRunner.recordLoopTick(tickTime)
	th.cache = NewCache(time.Minute)
	metaSched := decodeStable[StableMeta](t, stableGet(t, th.StablecoinMeta, "/api/opendata/stablecoin/meta"))
	stSched := metaSched.BigQuery.Statuses[stableEthJobID]
	if !stSched.ScheduleEnabled || stSched.NextCheckAt == nil || *stSched.NextCheckAt != "2026-10-19T15:00:00Z" {
		t.Fatalf("schedule on status = %+v, want ScheduleEnabled=true and NextCheckAt=2026-10-19T15:00:00Z", stSched)
	}
}

func TestStableBQSyncersUseDryRunSizedCap(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 19, 13, 21, 0, 0, time.UTC)

	const estTotal int64 = 3_456_789_012
	wantMin := int64(math.Ceil(float64(estTotal) * 1.25))

	ethSrc := &fakeStableEthSource{
		ready:   true,
		dryA:    3_000_000_000,
		dryB:    456_789_012,
		billedA: 2_800_000_000,
		billedB: 400_000_000,
		transfers: []stableEthTransferAggRow{
			{Day: "2026-10-18", Token: usdtContract, Transfers: 150_000, VolumeRaw: "1000000000000"},
			{Day: "2026-10-18", Token: usdcContract, Transfers: 90_000, VolumeRaw: "1000000000000"},
			{Day: "2026-10-18", Token: eurcContract, Transfers: 500, VolumeRaw: "1000000000"},
		},
	}
	tronSrc := &fakeStableTronSource{
		ready:   true,
		dryA:    3_000_000_000,
		dryB:    456_789_012,
		billedA: 2_800_000_000,
		billedB: 400_000_000,
		transfers: []stableTronTransferAggRow{
			{Day: "2026-10-18", Transfers: 1_800_000, VolumeRaw: "1000000000000"},
		},
	}

	for _, jobID := range []string{stableEthJobID, stableTronJobID} {
		if err := ensureSyncRow(ctx, store.db, jobID); err != nil {
			t.Fatal(err)
		}
		if err := updateSyncWatermarksTx(ctx, store.db, jobID, "2026-07-21", "2026-10-17", 10, now); err != nil {
			t.Fatal(err)
		}
	}

	settings := (StablecoinConfig{BigQuery: StablecoinBigQueryConfig{Jobs: true}}).settings()
	runner := newStableBQRunner(settings, store, ethSrc, tronSrc, func() time.Time { return now })
	runner.executeSync(ctx, stableEthJobID, "scheduled")
	runner.executeSync(ctx, stableTronJobID, "scheduled")

	for _, tc := range []struct {
		job  string
		gotA int64
		gotB int64
	}{
		{stableEthJobID, ethSrc.lastMaxBytesA, ethSrc.lastMaxBytesB},
		{stableTronJobID, tronSrc.lastMaxBytesA, tronSrc.lastMaxBytesB},
	} {
		if tc.gotA < wantMin || tc.gotA < 1<<30 {
			t.Errorf("%s query maxBytes = %d, want >= %d (est*1.25) and >= %d (1 GiB)",
				tc.job, tc.gotA, wantMin, int64(1<<30))
		}
		wantRem := tc.gotA - 2_800_000_000
		if tc.gotB != wantRem {
			t.Errorf("%s second query maxBytes = %d, want remaining cap %d", tc.job, tc.gotB, wantRem)
		}
	}

	// Small dry-run estimate (< 1 GiB) still gets at least the 1 GiB floor.
	ethSrc.dryA, ethSrc.dryB = 200<<20, 100<<20
	ethSrc.billedA, ethSrc.billedB = 150<<20, 50<<20
	now2 := now.Add(24 * time.Hour)
	runner2 := newStableBQRunner(settings, store, ethSrc, tronSrc, func() time.Time { return now2 })
	runner2.executeSync(ctx, stableEthJobID, "scheduled")
	if ethSrc.lastMaxBytesA != 1<<30 {
		t.Errorf("small dry-run maxBytes = %d, want 1 GiB floor (%d)", ethSrc.lastMaxBytesA, int64(1<<30))
	}
}
