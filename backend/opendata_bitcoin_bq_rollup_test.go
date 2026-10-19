package main

import (
	"context"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

func btcNullTS(t time.Time) bigquery.NullTimestamp {
	return bigquery.NullTimestamp{Timestamp: t, Valid: true}
}

type fakeRollupQuerySource struct {
	readyAtCalls []time.Time
	readyResult  bool
	readyErr     error
	blocksRows   []btcBlockRow
	blocksBilled int64
	blocksErr    error
	rollupRows   []btcRollupRawRow
	rollupBilled int64
	rollupErr    error
	a2Calls      int
	a1Calls      int
}

func (f *fakeRollupQuerySource) ReadyAt(_ context.Context, at time.Time) (bool, error) {
	f.readyAtCalls = append(f.readyAtCalls, at)
	return f.readyResult, f.readyErr
}

func (f *fakeRollupQuerySource) DryRunBlocks(_ context.Context, _, _ civil.Date) (int64, error) {
	return 40 << 20, nil
}

func (f *fakeRollupQuerySource) DryRunRollup(_ context.Context, _, _ civil.Date) (int64, error) {
	return 600 << 20, nil
}

func (f *fakeRollupQuerySource) FetchBlocks(_ context.Context, _, _ civil.Date, _ int64) ([]btcBlockRow, int64, error) {
	f.a2Calls++
	return f.blocksRows, f.blocksBilled, f.blocksErr
}

func (f *fakeRollupQuerySource) FetchRollup(_ context.Context, _, _ civil.Date, _ int64) ([]btcRollupRawRow, int64, error) {
	f.a1Calls++
	return f.rollupRows, f.rollupBilled, f.rollupErr
}

func validTestRollupRow(date string, coinbaseTxs int64) btcRollupRawRow {
	feeQ := make([]float64, 101)
	feeQ[10] = 1.5
	feeQ[25] = 2.0
	feeQ[50] = 4.0
	feeQ[75] = 8.0
	feeQ[90] = 15.0
	return btcRollupRawRow{
		Date:              date,
		Txs:               450_000,
		CoinbaseTxs:       coinbaseTxs,
		Vbytes:            220_000_000,
		Bytes:             380_000_000,
		WitnessBytes:      213_000_000,
		FeeQ:              feeQ,
		FeesSats:          303_128_842,
		CoinbaseSats:      45_303_128_842,
		CreatedSats:       120_000_000_000_000,
		OpretValueSats:    0,
		VbPayment:         100_000_000,
		VbInscriptionLike: 50_000_000,
		VbRunes:           30_000_000,
		VbOpReturn:        10_000_000,
		OpretOutputs:      25_000,
		Runestones:        18_000,
		OutP2TR:           200_000,
		OutP2WPKH:         400_000,
		OutAnchor:         500,
		OutAll:            900_000,
		InP2TR:            180_000,
		InP2WPKH:          350_000,
		InAnchor:          100,
		InAll:             700_000,
		ActiveAddresses:   680_000,
	}
}

func TestBtcRollupSQL(t *testing.T) {
	sql := btcRollupSQL()
	mustContain := []string{
		"DIV(4 * (size - virtual_size), 3) AS witness_bytes",
		"CAST(SUM(witness_bytes) AS INT64) AS witness_bytes",
		"CAST(SUM(IF(is_coinbase, 0, fee)) AS INT64) AS fees_sats",
		"CAST(SUM(IF(is_coinbase, (SELECT SUM(o.value) FROM UNNEST(outs) o), 0)) AS INT64) AS coinbase_sats",
		"CAST(SUM((SELECT SUM(IF(o.type != 'nulldata', o.value, 0)) FROM UNNEST(outs) o)) AS INT64) AS created_sats",
		"CAST(SUM((SELECT SUM(IF(o.type = 'nulldata', o.value, 0)) FROM UNNEST(outs) o)) AS INT64) AS opret_value_sats",
		"WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)",
		"o.type = 'anchor'",
		"t = 'anchor'",
		"WHEN tr_in = input_count AND 4 * (size - virtual_size) / 3 / input_count >= @inscription_wb_per_input THEN 'inscription_like'",
		"o.type NOT IN ('nulldata', 'anchor')",
		"COUNTIF(t IS NOT NULL)",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("btcRollupSQL missing %q", want)
		}
	}
	// Runes branch must precede generic op_return branch in CASE.
	runesIdx := strings.Index(sql, "THEN 'runes'")
	opretIdx := strings.Index(sql, "THEN 'op_return'")
	if runesIdx < 0 || opretIdx < 0 || runesIdx >= opretIdx {
		t.Errorf("expected 'runes' branch (%d) before 'op_return' branch (%d)", runesIdx, opretIdx)
	}
}

func TestBtcSQLResultTypesMatchStructs(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		typ  reflect.Type
	}{
		{"A1 rollup", btcRollupSQL(), reflect.TypeFor[btcRollupRawRow]()},
		{"A2 blocks", btcBlocksSQL(), reflect.TypeFor[btcBlockRow]()},
		{"B coinage", btcCoinAgeSQL(), reflect.TypeFor[btcCoinAgeRawRow]()},
		{"C utxo_baseline", btcUTXOBaselineSQL(), reflect.TypeFor[btcUTXOBaselineRow]()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < tc.typ.NumField(); i++ {
				f := tc.typ.Field(i)
				tag := f.Tag.Get("bigquery")
				if tag == "" || tag == "-" {
					t.Fatalf("struct %s field %s missing bigquery tag", tc.typ.Name(), f.Name)
				}
				// Verify column is projected in the SQL (as `AS <tag>` or bare `<tag>`).
				aliasRe := regexp.MustCompile(`(?i)\b(?:AS\s+` + regexp.QuoteMeta(tag) + `|` + regexp.QuoteMeta(tag) + `\b)`)
				if !aliasRe.MatchString(tc.sql) {
					t.Errorf("struct %s field %s (bigquery:%q) not projected in SQL", tc.typ.Name(), f.Name, tag)
				}
				// If the Go field is int64, ensure no expression aliased `AS <tag>` in any CTE or SELECT
				// uses BigQuery's float division operator `/` without an outer CAST(... AS INT64).
				if f.Type.Kind() == reflect.Int64 {
					defRe := regexp.MustCompile(`(?im)([^\n,]+?)\s+AS\s+` + regexp.QuoteMeta(tag) + `\b`)
					matches := defRe.FindAllStringSubmatch(tc.sql, -1)
					for _, m := range matches {
						expr := strings.TrimSpace(m[1])
						if strings.Contains(expr, "/") && !strings.HasPrefix(strings.ToUpper(expr), "CAST(") {
							t.Errorf("int64 field %s (bigquery:%q) uses float division '/' in SQL expression %q (will return FLOAT64 in BigQuery and fail decoding into int64)",
								f.Name, tag, expr)
						}
					}
				}
			}
		})
	}
}

func TestBtcBlocksSQL(t *testing.T) {
	sql := btcBlocksSQL()
	mustContain := []string{
		"number AS height",
		"FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', timestamp) AS ts",
		"FORMAT_DATE('%Y-%m-%d', DATE(timestamp)) AS date",
		"bits",
		"IF(DIV(number, 210000) >= 64, 0, 5000000000 >> DIV(number, 210000)) AS subsidy_sats",
		"TIMESTAMP_SUB(TIMESTAMP(@start_date), INTERVAL @slack_hours HOUR)",
		"TIMESTAMP_ADD(TIMESTAMP(@end_date), INTERVAL @slack_hours HOUR)",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("btcBlocksSQL missing %q", want)
		}
	}
	if strings.Contains(sql, "65535") || strings.Contains(sql, "POW(256") {
		t.Error("btcBlocksSQL must not decode difficulty in SQL")
	}
}

func TestBtcBlocksContiguityWithSlack(t *testing.T) {
	d0 := civil.Date{Year: 2026, Month: 10, Day: 14}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 15}

	t.Run("v3-review stall case: block dated 2h after window end sits 30 heights below h_max", func(t *testing.T) {
		// Heights 1000..1040: block 1010 is forward-dated to 2026-10-15T02:00:00Z (2h after d1),
		// while blocks 1011..1040 are dated 2026-10-14 (30 heights above 1010).
		// Because A2 fetches with 6h slack, block 1010 is included in a2Rows, so contiguity passes!
		var rows []btcBlockRow
		for h := int64(1000); h <= 1040; h++ {
			date := "2026-10-14"
			ts := "2026-10-14T23:50:00Z"
			if h == 1010 {
				date = "2026-10-15"
				ts = "2026-10-15T02:00:00Z"
			}
			rows = append(rows, btcBlockRow{
				Height:           h,
				TS:               ts,
				Date:             date,
				Bits:             "17021ef0",
				TransactionCount: 2500,
				SubsidySats:      312_500_000,
			})
		}
		cov, err := validateBtcBlocksA2(d0, d1, rows, nil, nil)
		if err != nil {
			t.Fatalf("v3 stall case failed unexpectedly: %v", err)
		}
		if cov != 1000 {
			t.Errorf("coverageStart = %d, want 1000", cov)
		}
	})

	t.Run("back-dated block at window start passes and holes below coverage_start are ignored", func(t *testing.T) {
		// Suppose padded block 995 (dated d-1) and 999 (dated d-1) are in A2 with a hole at 996..998,
		// while in-window blocks start at 1000..1005 (contiguous) and block 1002 is back-dated to d-1.
		rows := []btcBlockRow{
			{Height: 995, TS: "2026-10-13T19:00:00Z", Date: "2026-10-13", Bits: "17021ef0", SubsidySats: 312_500_000},
			{Height: 999, TS: "2026-10-13T23:55:00Z", Date: "2026-10-13", Bits: "17021ef0", SubsidySats: 312_500_000},
			{Height: 1000, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
			{Height: 1001, TS: "2026-10-14T00:10:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
			{Height: 1002, TS: "2026-10-13T23:58:00Z", Date: "2026-10-13", Bits: "17021ef0", SubsidySats: 312_500_000},
			{Height: 1003, TS: "2026-10-14T00:20:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
		}
		cov, err := validateBtcBlocksA2(d0, d1, rows, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cov != 1000 {
			t.Errorf("coverageStart = %d, want 1000", cov)
		}
	})

	t.Run("true interior hole fails", func(t *testing.T) {
		rows := []btcBlockRow{
			{Height: 1000, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
			// height 1001 missing!
			{Height: 1002, TS: "2026-10-14T00:25:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
		}
		_, err := validateBtcBlocksA2(d0, d1, rows, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "contiguity_hole at height 1001") {
			t.Fatalf("expected contiguity_hole at 1001, got %v", err)
		}
	})
}

func TestBtcBlocksDuplicateHeight(t *testing.T) {
	d0 := civil.Date{Year: 2026, Month: 10, Day: 14}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 15}
	rows := []btcBlockRow{
		{Height: 1000, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
		{Height: 1000, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", SubsidySats: 312_500_000},
	}
	_, err := validateBtcBlocksA2(d0, d1, rows, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate block height 1000") {
		t.Fatalf("expected duplicate block height error, got %v", err)
	}
}

func TestBtcInversionWarning(t *testing.T) {
	d0 := civil.Date{Year: 2026, Month: 10, Day: 14}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 15}
	// Span of 15 heights and > 1h inversion logs a warning and does NOT fail.
	var rows []btcBlockRow
	for h := int64(1000); h <= 1020; h++ {
		ts := "2026-10-14T12:00:00Z"
		if h == 1000 {
			ts = "2026-10-14T14:00:00Z"
		} else if h == 1015 {
			ts = "2026-10-14T12:30:00Z" // 1.5h earlier and 15 heights after 1000
		}
		rows = append(rows, btcBlockRow{
			Height:      h,
			TS:          ts,
			Date:        "2026-10-14",
			Bits:        "17021ef0",
			SubsidySats: 312_500_000,
		})
	}
	if _, err := validateBtcBlocksA2(d0, d1, rows, nil, nil); err != nil {
		t.Fatalf("large inversion warning must not fail guard: %v", err)
	}
}

func TestBtcRollupA2First(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	src := &fakeRollupQuerySource{
		readyResult:  true,
		blocksBilled: 40 << 20,
		blocksRows: []btcBlockRow{
			{Height: 1000, TS: "2026-09-25T00:05:00Z", Date: "2026-09-25", Bits: "17021ef0", SubsidySats: 312_500_000},
			// Missing 1001!
			{Height: 1002, TS: "2026-09-25T00:25:00Z", Date: "2026-09-25", Bits: "17021ef0", SubsidySats: 312_500_000},
		},
	}
	job := &btcOnchainJob{store: store, src: src}
	flags := newBtcStopFlags(store)
	syncer := &dailyBQSyncer{
		store:         store,
		job:           job,
		now:           func() time.Time { return at(6, 0) },
		initialDays:   1,
		stopFlags:     flags,
		failurePolicy: btcJobFailurePolicy(btcOnchainJobID),
	}

	syncer.syncOnce(ctx)

	if src.a2Calls != 1 || src.a1Calls != 0 {
		t.Fatalf("a2Calls=%d a1Calls=%d, want 1 and 0 (must fail before A1 is billed)", src.a2Calls, src.a1Calls)
	}
	recs, _ := flags.failures(ctx, btcOnchainJobID)
	if len(recs) != 1 || recs[0].BilledBytes != 40<<20 {
		t.Fatalf("failures = %+v, want 1 failure with A2 bytes (%d) only", recs, int64(40<<20))
	}
}

func TestBtcRollupReadiness(t *testing.T) {
	ctx := context.Background()
	src := &fakeRollupQuerySource{readyResult: true}
	job := &btcOnchainJob{src: src}
	end := civil.Date{Year: 2026, Month: 10, Day: 15}

	ok, err := job.Ready(ctx, end)
	if err != nil || !ok {
		t.Fatalf("Ready = %v, %v; want true, nil", ok, err)
	}
	wantAt := time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	if len(src.readyAtCalls) != 1 || !src.readyAtCalls[0].Equal(wantAt) {
		t.Fatalf("Ready probed %v, want %v (end + 6h)", src.readyAtCalls, wantAt)
	}
}

func TestBtcReadinessSlack(t *testing.T) {
	end := civil.Date{Year: 2026, Month: 10, Day: 15}
	probeAt := dayStart(end).Add(btcInversionSlack) // 2026-10-15T06:00:00Z

	// A forward-dated block at end + 30 min does NOT satisfy end + 6h readiness.
	early := end.In(time.UTC).Add(30 * time.Minute)
	if cryptoTimestampComplete(
		btcNullTS(early),
		probeAt,
	) {
		t.Fatal("expected row at end+30m not to satisfy end+6h readiness")
	}
	// A row at end + 6h satisfies readiness.
	if !cryptoTimestampComplete(
		btcNullTS(probeAt),
		probeAt,
	) {
		t.Fatal("expected row at end+6h to satisfy readiness")
	}
}

func TestBtcRollupGuards(t *testing.T) {
	d0 := civil.Date{Year: 2026, Month: 10, Day: 14}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 15}
	d2 := civil.Date{Year: 2026, Month: 10, Day: 16}

	baseBlocks := []btcBlockRow{
		{Height: 1000, TS: "2026-10-14T10:00:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
		{Height: 1001, TS: "2026-10-14T10:10:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
	}

	t.Run("txs floor", func(t *testing.T) {
		r := validTestRollupRow("2026-10-14", 2)
		r.Txs = 99_999
		_, err := buildBtcOnchainDays(d0, d1, baseBlocks, []btcRollupRawRow{r}, 100_000, 100_000)
		if err == nil || !strings.Contains(err.Error(), "non-coinbase transactions") {
			t.Fatalf("expected txs floor error, got %v", err)
		}
	})

	t.Run("label floors out_p2tr and opret_outputs", func(t *testing.T) {
		r := validTestRollupRow("2026-10-14", 2)
		r.OutP2TR = 0
		_, err := buildBtcOnchainDays(d0, d1, baseBlocks, []btcRollupRawRow{r}, 100_000, 100_000)
		if err == nil || !strings.Contains(err.Error(), "out_p2tr") {
			t.Fatalf("expected out_p2tr floor error, got %v", err)
		}
		r2 := validTestRollupRow("2026-10-14", 2)
		r2.OpretOutputs = 0
		_, err = buildBtcOnchainDays(d0, d1, baseBlocks, []btcRollupRawRow{r2}, 100_000, 100_000)
		if err == nil || !strings.Contains(err.Error(), "op_return outputs") {
			t.Fatalf("expected op_return floor error, got %v", err)
		}
	})

	t.Run("coinbase_txs != blocks", func(t *testing.T) {
		r := validTestRollupRow("2026-10-14", 3) // 3 != 2 blocks
		_, err := buildBtcOnchainDays(d0, d1, baseBlocks, []btcRollupRawRow{r}, 100_000, 100_000)
		if err == nil || !strings.Contains(err.Error(), "coinbase/block count mismatch") {
			t.Fatalf("expected coinbase/block mismatch error, got %v", err)
		}
	})

	t.Run("missing day", func(t *testing.T) {
		r := validTestRollupRow("2026-10-14", 2)
		// Window is [d0, d2) (2 days), but only d0 is present.
		_, err := buildBtcOnchainDays(d0, d2, baseBlocks, []btcRollupRawRow{r}, 100_000, 100_000)
		if err == nil || !strings.Contains(err.Error(), "2026-10-15") {
			t.Fatalf("expected missing day 2026-10-15 error, got %v", err)
		}
	})

	t.Run("midnight inversion (height 1000 dated d+1, height 1001 dated d) passes every guard", func(t *testing.T) {
		invBlocks := []btcBlockRow{
			{Height: 999, TS: "2026-10-14T23:55:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
			{Height: 1000, TS: "2026-10-15T00:02:00Z", Date: "2026-10-15", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
			{Height: 1001, TS: "2026-10-14T23:59:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
			{Height: 1002, TS: "2026-10-15T00:12:00Z", Date: "2026-10-15", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
		}
		if _, err := validateBtcBlocksA2(d0, d2, invBlocks, nil, nil); err != nil {
			t.Fatalf("validateBtcBlocksA2 rejected midnight inversion: %v", err)
		}
		r0 := validTestRollupRow("2026-10-14", 2) // heights 999 and 1001
		r1 := validTestRollupRow("2026-10-15", 2) // heights 1000 and 1002
		days, err := buildBtcOnchainDays(d0, d2, invBlocks, []btcRollupRawRow{r0, r1}, 100_000, 100_000)
		if err != nil || len(days) != 2 {
			t.Fatalf("buildBtcOnchainDays rejected midnight inversion: len=%d err=%v", len(days), err)
		}
	})
}

func TestBtcRollupDerived(t *testing.T) {
	d0 := civil.Date{Year: 2024, Month: 4, Day: 20}
	d1 := civil.Date{Year: 2024, Month: 4, Day: 21}

	// Build 144 blocks on halving day: 1 block at 839,999 (625,000,000 sats) + 143 blocks at 840,000+ (312,500,000 sats),
	// all with bits "17021ef0" (difficulty 132,716,002,350,731.3 -> 144 blocks => 950.02 EH/s),
	// and 1 empty block (transaction_count = 1).
	var blocks []btcBlockRow
	for i := int64(0); i < 144; i++ {
		h := 839_999 + i
		txCount := int64(2500)
		if i == 5 {
			txCount = 1 // empty block
		}
		blocks = append(blocks, btcBlockRow{
			Height:           h,
			TS:               "2024-04-20T12:00:00Z",
			Date:             "2024-04-20",
			Bits:             "17021ef0",
			TransactionCount: txCount,
			SubsidySats:      btcSubsidySats(h),
		})
	}
	r := validTestRollupRow("2024-04-20", 144)
	days, err := buildBtcOnchainDays(d0, d1, blocks, []btcRollupRawRow{r}, 100_000, 100_000)
	if err != nil || len(days) != 1 {
		t.Fatalf("buildBtcOnchainDays: %v", err)
	}
	got := days[0]
	wantSched := int64(625_000_000 + 143*312_500_000) // 45,312,500,000 sats
	if got.ScheduledIssuanceSats != wantSched {
		t.Errorf("ScheduledIssuanceSats = %d, want %d", got.ScheduledIssuanceSats, wantSched)
	}
	wantClaimed := r.CoinbaseSats - r.FeesSats
	if got.ClaimedIssuanceSats != wantClaimed {
		t.Errorf("ClaimedIssuanceSats = %d, want %d", got.ClaimedIssuanceSats, wantClaimed)
	}
	if got.EmptyBlocks != 1 {
		t.Errorf("EmptyBlocks = %d, want 1", got.EmptyBlocks)
	}
	if math.Abs(got.ImpliedHashrateEHs-950.02) > 0.05 {
		t.Errorf("ImpliedHashrateEHs = %.4f, want ~950.02", got.ImpliedHashrateEHs)
	}
	if got.FeeP50SatVB != 4.0 || got.FeeP90SatVB != 15.0 {
		t.Errorf("fee percentiles p50=%.2f p90=%.2f, want 4.0 and 15.0", got.FeeP50SatVB, got.FeeP90SatVB)
	}
}

func TestBtcRollupStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestRiskStore(t)
	now := time.Date(2026, 10, 15, 6, 30, 0, 0, time.UTC)

	d0 := civil.Date{Year: 2026, Month: 10, Day: 14}
	d1 := civil.Date{Year: 2026, Month: 10, Day: 15}
	blocks := []btcBlockRow{
		{Height: 971_548, TS: "2026-10-14T00:05:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
		{Height: 971_549, TS: "2026-10-14T00:15:00Z", Date: "2026-10-14", Bits: "17021ef0", TransactionCount: 2500, SubsidySats: 312_500_000},
	}
	r := validTestRollupRow("2026-10-14", 2)
	days, err := buildBtcOnchainDays(d0, d1, blocks, []btcRollupRawRow{r}, 100_000, 100_000)
	if err != nil {
		t.Fatal(err)
	}

	payload := btcRollupPayload{
		CoverageStart: 971_548,
		Blocks:        blocks,
		Days:          days,
	}
	if err := store.applyBtcRollupBatch(ctx, payload, "2026-10-14", "2026-10-14", now); err != nil {
		t.Fatal(err)
	}

	cov, ok, err := store.getBtcBlocksCoverageStart(ctx)
	if err != nil || !ok || cov != 971_548 {
		t.Errorf("coverageStart = %d, %v, %v; want 971548", cov, ok, err)
	}
	rs := newBtcReadStore(store, (BitcoinConfig{}).settings(), func() time.Time { return now })
	loaded, err := rs.onchainDays(ctx, "2026-10-14", "2026-10-14")
	if err != nil || len(loaded) != 1 {
		t.Fatalf("onchainDays = %+v, %v; want 1 row", loaded, err)
	}
	if loaded[0].FeesSats != r.FeesSats || loaded[0].OutAnchor != 500 || loaded[0].VbRunes != 30_000_000 {
		t.Errorf("round-trip row mismatch: %+v", loaded[0])
	}
}
