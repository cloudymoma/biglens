package main

import (
	"context"
	"encoding/hex"
	"net/http"
	"reflect"
	"regexp"
	"strings"
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
	ready     bool
	transfers []stableEthTransferAggRow
	logs      []stableEthLogRow
	billedA   int64
	billedB   int64
	errA      error
	errB      error
}

func (f *fakeStableEthSource) Ready(_ context.Context, _ civil.Date) (bool, error) {
	return f.ready, nil
}
func (f *fakeStableEthSource) DryRunTransfers(_ context.Context, _, _ civil.Date) (int64, error) {
	return 1 << 30, nil
}
func (f *fakeStableEthSource) DryRunLogs(_ context.Context, _, _ civil.Date) (int64, error) {
	return 1 << 30, nil
}
func (f *fakeStableEthSource) FetchTransfers(_ context.Context, _, _ civil.Date, _ int64) ([]stableEthTransferAggRow, int64, error) {
	return f.transfers, f.billedA, f.errA
}
func (f *fakeStableEthSource) FetchLogs(_ context.Context, _, _ civil.Date, _ int64) ([]stableEthLogRow, int64, error) {
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
	ready     bool
	transfers []stableTronTransferAggRow
	logs      []stableTronLogRow
	billedA   int64
	billedB   int64
}

func (f *fakeStableTronSource) Ready(_ context.Context, _ civil.Date) (bool, error) {
	return f.ready, nil
}
func (f *fakeStableTronSource) DryRunTransfers(_ context.Context, _, _ civil.Date) (int64, error) {
	return 1 << 30, nil
}
func (f *fakeStableTronSource) DryRunEvents(_ context.Context, _, _ civil.Date) (int64, error) {
	return 1 << 30, nil
}
func (f *fakeStableTronSource) FetchTransfers(_ context.Context, _, _ civil.Date, _ int64) ([]stableTronTransferAggRow, int64, error) {
	return f.transfers, f.billedA, nil
}
func (f *fakeStableTronSource) FetchEvents(_ context.Context, _, _ civil.Date, _ int64) ([]stableTronLogRow, int64, error) {
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
