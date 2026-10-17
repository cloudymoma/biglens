package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
)

const (
	btcCoinAgeMinDailyInputs int64 = 100_000
	btcCoinAgeSchemaVersion        = 1
)

// btcBIP30Txids lists the two duplicate coinbase transaction hashes (BIP30).
var btcBIP30Txids = []string{
	"e3bf3d07d4b0375638d5f1db5255fe07ba2c4cb067cd81b84ee974b6585fb468",
	"d5d27987d2a3dfc724e359870c6644b40e497bdc0589a033220fe15429d88599",
}

// btcBIP30OverwrittenTimes lists the exact block_timestamp values of the two
// overwritten BIP30 coinbase transactions (heights 91,722 and 91,812). Filtering
// by block_timestamp instead of block_number avoids scanning the block_number
// column (saves ~11 GiB per coin-age run and ~29 GiB per UTXO baseline).
var btcBIP30OverwrittenTimes = []time.Time{
	time.Date(2010, 11, 14, 8, 37, 28, 0, time.UTC),
	time.Date(2010, 11, 14, 17, 59, 48, 0, time.UTC),
}

// btcAgeBands lists the 12 canonical age bands used by SpentAgeSharesPct and HODLWavesPct.
var btcAgeBands = []string{
	"<1d",
	"1d_1w",
	"1w_1m",
	"1m_3m",
	"3m_6m",
	"6m_12m",
	"1y_2y",
	"2y_3y",
	"3y_5y",
	"5y_7y",
	"7y_10y",
	">10y",
}

func btcAgeBand(ageDays int) string {
	if ageDays < 0 {
		ageDays = 0
	}
	switch {
	case ageDays < 1:
		return "<1d"
	case ageDays < 7:
		return "1d_1w"
	case ageDays < 30:
		return "1w_1m"
	case ageDays < 90:
		return "1m_3m"
	case ageDays < 180:
		return "3m_6m"
	case ageDays < 365:
		return "6m_12m"
	case ageDays < 730:
		return "1y_2y"
	case ageDays < 1095:
		return "2y_3y"
	case ageDays < 1825:
		return "3y_5y"
	case ageDays < 2555:
		return "5y_7y"
	case ageDays < 3650:
		return "7y_10y"
	default:
		return ">10y"
	}
}

func btcCoinAgeSQL() string {
	return `WITH spent AS (
  SELECT transaction_hash AS spend_tx, block_timestamp AS spent_at,
         DATE(block_timestamp) AS spend_day, spent_transaction_hash AS h,
         CAST(value AS INT64) AS sats
  FROM ` + "`bigquery-public-data.crypto_bitcoin.inputs`" + `
  WHERE block_timestamp >= TIMESTAMP(@start_date) AND block_timestamp < TIMESTAMP(@end_date)
    AND spent_transaction_hash IS NOT NULL
), created AS (
  SELECT ` + "`hash`" + ` AS h, DATE(block_timestamp) AS create_day
  FROM ` + "`bigquery-public-data.crypto_bitcoin.transactions`" + `
  WHERE block_timestamp < TIMESTAMP(@end_date)
    AND NOT (` + "`hash`" + ` IN UNNEST(@bip30_txids) AND block_timestamp IN UNNEST(@bip30_overwritten_times))
), j AS (
  SELECT s.spend_tx, s.spent_at, s.spend_day, s.sats, c.create_day
  FROM spent s LEFT JOIN created c USING (h)
), whales AS (
  SELECT spend_tx, create_day, ANY_VALUE(spend_day) AS spend_day, MIN(spent_at) AS spent_at,
         SUM(sats) AS sats_sum, COUNT(*) AS n
  FROM j
  WHERE create_day IS NOT NULL AND DATE_DIFF(spend_day, create_day, DAY) >= 365
  GROUP BY spend_tx, create_day
  HAVING SUM(sats) >= @whale_min_sats
  QUALIFY ROW_NUMBER() OVER (PARTITION BY spend_day ORDER BY sats_sum DESC) <= @whales_per_day
)
SELECT 'matrix' AS kind, FORMAT_DATE('%Y-%m-%d', spend_day) AS spend_day,
       IFNULL(FORMAT_DATE('%Y-%m-%d', create_day), '') AS create_day,
       SUM(sats) AS sats, COUNT(*) AS n,
       CAST(NULL AS STRING) AS spend_tx, CAST(NULL AS TIMESTAMP) AS spent_at
FROM j GROUP BY spend_day, create_day
UNION ALL
SELECT 'totals', FORMAT_DATE('%Y-%m-%d', spend_day), '', SUM(sats), COUNT(*), NULL, NULL
FROM spent GROUP BY spend_day
UNION ALL
SELECT 'whale', FORMAT_DATE('%Y-%m-%d', spend_day), FORMAT_DATE('%Y-%m-%d', create_day),
       sats_sum, n, spend_tx, spent_at
FROM whales`
}

type btcCoinAgeRawRow struct {
	Kind      string                 `bigquery:"kind"`
	SpendDay  string                 `bigquery:"spend_day"`
	CreateDay string                 `bigquery:"create_day"`
	Sats      int64                  `bigquery:"sats"`
	N         int64                  `bigquery:"n"`
	SpendTx   bigquery.NullString    `bigquery:"spend_tx"`
	SpentAt   bigquery.NullTimestamp `bigquery:"spent_at"`
}

type btcSpentMatrixRow struct {
	SpendDay  string `json:"spend_day"`
	CreateDay string `json:"create_day"`
	Sats      int64  `json:"sats"`
	Inputs    int64  `json:"inputs"`
}

type btcCoinAgePayload struct {
	Matrix []btcSpentMatrixRow
	Whales []BtcWhaleRevival
	Days   []BtcCoinageDay
}

type btcCoinAgeQuerySource interface {
	ReadyAt(ctx context.Context, at time.Time) (bool, error)
	DryRunCoinAge(ctx context.Context, start, end civil.Date) (int64, error)
	FetchCoinAge(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcCoinAgeRawRow, int64, error)
}

type bqCoinAgeSource struct {
	bq           *BQClient
	whaleMinSats int64
	whalesPerDay int
}

func (s bqCoinAgeSource) ReadyAt(ctx context.Context, at time.Time) (bool, error) {
	if s.bq == nil || s.bq.client == nil {
		return false, fmt.Errorf("nil bigquery client")
	}
	ts, err := s.bq.cryptoLatestRowTimeAt(ctx, "btc", at)
	if err != nil {
		return false, err
	}
	return cryptoTimestampComplete(ts, at), nil
}

func (s bqCoinAgeSource) params(start, end civil.Date) []bigquery.QueryParameter {
	whaleMin := s.whaleMinSats
	if whaleMin <= 0 {
		whaleMin = 10 * 100_000_000
	}
	whalesPerDay := s.whalesPerDay
	if whalesPerDay <= 0 {
		whalesPerDay = 20
	}
	return []bigquery.QueryParameter{
		{Name: "start_date", Value: start},
		{Name: "end_date", Value: end},
		{Name: "bip30_txids", Value: btcBIP30Txids},
		{Name: "bip30_overwritten_times", Value: btcBIP30OverwrittenTimes},
		{Name: "whale_min_sats", Value: whaleMin},
		{Name: "whales_per_day", Value: int64(whalesPerDay)},
	}
}

func (s bqCoinAgeSource) DryRunCoinAge(ctx context.Context, start, end civil.Date) (int64, error) {
	return btcDryRunQuery(ctx, s.bq.client, btcCoinAgeSQL(), btcCoinageJobID, s.params(start, end))
}

func (s bqCoinAgeSource) FetchCoinAge(ctx context.Context, start, end civil.Date, maxBytes int64) ([]btcCoinAgeRawRow, int64, error) {
	return btcRunQuery[btcCoinAgeRawRow](ctx, s.bq.client, btcCoinAgeSQL(), btcCoinageJobID, s.params(start, end), maxBytes)
}

type btcCoinAgeJob struct {
	store        *riskStore
	src          btcCoinAgeQuerySource
	minInputs    int64
	whaleMinSats int64
	whalesPerDay int
	onChange     func(context.Context)
}

func newBtcCoinAgeJob(store *riskStore, bq *BQClient, settings btcSettings, onChange func(context.Context)) *btcCoinAgeJob {
	return &btcCoinAgeJob{
		store: store,
		src: bqCoinAgeSource{
			bq:           bq,
			whaleMinSats: settings.WhaleMinSats,
			whalesPerDay: settings.WhalesPerDay,
		},
		minInputs:    btcCoinAgeMinDailyInputs,
		whaleMinSats: settings.WhaleMinSats,
		whalesPerDay: settings.WhalesPerDay,
		onChange:     onChange,
	}
}

func (j *btcCoinAgeJob) ID() string { return btcCoinageJobID }

func (j *btcCoinAgeJob) Ready(ctx context.Context, end civil.Date) (bool, error) {
	probeAt := dayStart(end).Add(btcInversionSlack)
	return j.src.ReadyAt(ctx, probeAt)
}

func (j *btcCoinAgeJob) DryRun(ctx context.Context, start, end civil.Date) (int64, error) {
	return j.src.DryRunCoinAge(ctx, start, end)
}

func (j *btcCoinAgeJob) Run(ctx context.Context, start, end civil.Date, maxBytes int64) (dailyResult, int64, error) {
	rawRows, billed, err := j.src.FetchCoinAge(ctx, start, end, maxBytes)
	if err != nil {
		return dailyResult{}, billed, err
	}
	minInputs := j.minInputs
	if minInputs <= 0 {
		minInputs = btcCoinAgeMinDailyInputs
	}
	whaleMin := j.whaleMinSats
	if whaleMin <= 0 {
		whaleMin = 10 * 100_000_000
	}
	whalesPerDay := j.whalesPerDay
	if whalesPerDay <= 0 {
		whalesPerDay = 20
	}
	payload, err := buildBtcCoinAgePayload(start, end, rawRows, minInputs, whaleMin, whalesPerDay)
	if err != nil {
		return dailyResult{}, billed, err
	}
	return dailyResult{
		Rows:    len(payload.Days),
		Payload: payload,
	}, billed, nil
}

func (j *btcCoinAgeJob) Store(ctx context.Context, s *riskStore, r dailyResult, newFrom, cursor string, now time.Time) error {
	payload, ok := r.Payload.(btcCoinAgePayload)
	if !ok {
		return fmt.Errorf("btc_coinage: unexpected payload %T", r.Payload)
	}
	if err := s.applyBtcCoinAgeBatch(ctx, payload, newFrom, cursor, now); err != nil {
		return err
	}
	if j.onChange != nil {
		j.onChange(ctx)
	}
	return nil
}

func buildBtcCoinAgePayload(start, end civil.Date, rawRows []btcCoinAgeRawRow, minInputs, whaleMinSats int64, whalesPerDay int) (btcCoinAgePayload, error) {
	type dayTotals struct {
		sats int64
		n    int64
		ok   bool
	}
	totalsByDay := make(map[string]dayTotals)
	matrixAllByDay := make(map[string][]btcCoinAgeRawRow)
	whalesByDay := make(map[string][]BtcWhaleRevival)

	for _, r := range rawRows {
		switch r.Kind {
		case "totals":
			totalsByDay[r.SpendDay] = dayTotals{sats: r.Sats, n: r.N, ok: true}
		case "matrix":
			matrixAllByDay[r.SpendDay] = append(matrixAllByDay[r.SpendDay], r)
		case "whale":
			if !r.SpendTx.Valid || r.SpendTx.StringVal == "" || r.CreateDay == "" {
				continue
			}
			if r.Sats < whaleMinSats {
				continue
			}
			spentAt := ""
			if r.SpentAt.Valid {
				spentAt = r.SpentAt.Timestamp.UTC().Format(time.RFC3339)
			}
			var ageDays int
			if sd, err1 := civil.ParseDate(r.SpendDay); err1 == nil {
				if cd, err2 := civil.ParseDate(r.CreateDay); err2 == nil {
					ageDays = sd.DaysSince(cd)
				}
			}
			if ageDays < 365 {
				continue
			}
			whalesByDay[r.SpendDay] = append(whalesByDay[r.SpendDay], BtcWhaleRevival{
				SpendTx:   r.SpendTx.StringVal,
				CreateDay: r.CreateDay,
				SpendDay:  r.SpendDay,
				SpentAt:   spentAt,
				AgeDays:   ageDays,
				Sats:      r.Sats,
				BTC:       float64(r.Sats) / 1e8,
				Inputs:    r.N,
			})
		}
	}

	var (
		matrixOut []btcSpentMatrixRow
		whalesOut []BtcWhaleRevival
		daysOut   []BtcCoinageDay
	)

	for cur := start; cur.Before(end); cur = cur.AddDays(1) {
		d := cur.String()
		tot, ok := totalsByDay[d]
		if !ok {
			return btcCoinAgePayload{}, suspiciousEmpty(d, "coin-age totals row")
		}
		if tot.n < minInputs {
			return btcCoinAgePayload{}, suspiciousEmpty(d, "spent inputs")
		}

		mRows := matrixAllByDay[d]
		var (
			sumN       int64
			sumSats    int64
			unmatchedN int64
			dayMatrix  []btcSpentMatrixRow
		)
		for _, mr := range mRows {
			sumN += mr.N
			sumSats += mr.Sats
			if mr.CreateDay == "" {
				unmatchedN += mr.N
				continue
			}
			cd, err := civil.ParseDate(mr.CreateDay)
			if err != nil {
				return btcCoinAgePayload{}, &suspiciousResultError{
					msg: fmt.Sprintf("suspicious invalid create_day %q on spend_day %s", mr.CreateDay, d),
				}
			}
			// Negative-age rows with create_day == spend_day + 1 are legitimate
			// midnight timestamp inversions and are kept; create_day >= spend_day + 2 is impossible.
			if cd.After(cur.AddDays(1)) {
				return btcCoinAgePayload{}, &suspiciousResultError{
					msg: fmt.Sprintf("suspicious future create_day %s on spend_day %s (> +1 day)", mr.CreateDay, d),
				}
			}
			dayMatrix = append(dayMatrix, btcSpentMatrixRow{
				SpendDay:  d,
				CreateDay: mr.CreateDay,
				Sats:      mr.Sats,
				Inputs:    mr.N,
			})
		}

		// Exact integer equality guard: catches double counting from duplicate creation txids.
		if sumN != tot.n || sumSats != tot.sats {
			return btcCoinAgePayload{}, &suspiciousResultError{
				msg: fmt.Sprintf("suspicious coin-age double counting / totals mismatch on %s: matrix_n=%d totals_n=%d matrix_sats=%d totals_sats=%d",
					d, sumN, tot.n, sumSats, tot.sats),
			}
		}

		// Unmatched inputs <= 0.01% of totals.n (unmatchedN * 10000 <= tot.n).
		if unmatchedN*10_000 > tot.n {
			return btcCoinAgePayload{}, &suspiciousResultError{
				msg: fmt.Sprintf("suspicious unmatched inputs on %s: %d of %d (> 0.01%%)", d, unmatchedN, tot.n),
			}
		}

		slices.SortFunc(dayMatrix, func(a, b btcSpentMatrixRow) int {
			if a.CreateDay < b.CreateDay {
				return -1
			}
			if a.CreateDay > b.CreateDay {
				return 1
			}
			return 0
		})
		matrixOut = append(matrixOut, dayMatrix...)

		spentMap := make(map[string]int64, len(dayMatrix))
		for _, mr := range dayMatrix {
			spentMap[mr.CreateDay] += mr.Sats
		}
		daysOut = append(daysOut, coinageMetrics(d, spentMap, nil, false))

		dayWhales := whalesByDay[d]
		slices.SortFunc(dayWhales, func(a, b BtcWhaleRevival) int {
			if a.Sats != b.Sats {
				if a.Sats > b.Sats {
					return -1
				}
				return 1
			}
			if a.SpendTx < b.SpendTx {
				return -1
			}
			if a.SpendTx > b.SpendTx {
				return 1
			}
			return 0
		})
		if whalesPerDay > 0 && len(dayWhales) > whalesPerDay {
			dayWhales = dayWhales[:whalesPerDay]
		}
		whalesOut = append(whalesOut, dayWhales...)
	}

	return btcCoinAgePayload{
		Matrix: matrixOut,
		Whales: whalesOut,
		Days:   daysOut,
	}, nil
}

// coinageMetrics computes a day's coin-age metrics (CDD, dormancy, revived supply,
// spent-age shares, and price-gated SOPR variants) from integer satoshi spends.
func coinageMetrics(spendDay string, spent map[string]int64, prices map[string]float64, priceComplete bool) BtcCoinageDay {
	sd, _ := civil.ParseDate(spendDay)
	bandSats := make(map[string]int64, len(btcAgeBands))
	var (
		totalSpent int64
		cddSatDays int64
		revived1y  int64
		revived2y  int64
		revived5y  int64
	)

	for cDay, sats := range spent {
		if sats <= 0 {
			continue
		}
		totalSpent += sats
		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = sd.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		cddSatDays += sats * int64(age)
		if age >= 365 {
			revived1y += sats
		}
		if age >= 730 {
			revived2y += sats
		}
		if age >= 1825 {
			revived5y += sats
		}
		bandSats[btcAgeBand(age)] += sats
	}

	shares := make(map[string]float64, len(btcAgeBands))
	for _, b := range btcAgeBands {
		if totalSpent > 0 {
			shares[b] = float64(bandSats[b]) / float64(totalSpent) * 100.0
		} else {
			shares[b] = 0
		}
	}

	var dormancy float64
	if totalSpent > 0 {
		dormancy = float64(cddSatDays) / float64(totalSpent)
	}

	out := BtcCoinageDay{
		Date:              spendDay,
		SpentSats:         totalSpent,
		CDDSatDays:        cddSatDays,
		CDDBTCDays:        float64(cddSatDays) / 1e8,
		DormancyDays:      dormancy,
		Revived1ySats:     revived1y,
		Revived2ySats:     revived2y,
		Revived5ySats:     revived5y,
		SpentAgeSharesPct: shares,
	}

	closeT := prices[spendDay]
	if !priceComplete || closeT <= 0 {
		out.Blocked = "price_history_incomplete"
		return out
	}

	var (
		allNum, allDen float64
		adjNum, adjDen float64
		sthNum, sthDen float64
		lthNum, lthDen float64
	)
	for cDay, sats := range spent {
		if sats <= 0 {
			continue
		}
		pc := prices[cDay]
		if pc <= 0 {
			// Rows with p_c = 0 (pre-2010-08-18) are excluded per §10.3.
			continue
		}
		cd, err := civil.ParseDate(cDay)
		age := 0
		if err == nil {
			age = sd.DaysSince(cd)
		}
		if age < 0 {
			age = 0
		}
		valCreated := float64(sats) * pc
		valSpent := float64(sats) * closeT
		allNum += valSpent
		allDen += valCreated
		if age >= 1 {
			adjNum += valSpent
			adjDen += valCreated
		}
		if age < 155 {
			sthNum += valSpent
			sthDen += valCreated
		} else {
			lthNum += valSpent
			lthDen += valCreated
		}
	}

	if allDen > 0 {
		v := allNum / allDen
		out.SOPR = &v
	}
	if adjDen > 0 {
		v := adjNum / adjDen
		out.SOPRAdjusted = &v
	}
	if sthDen > 0 {
		v := sthNum / sthDen
		out.SOPRSTH = &v
	}
	if lthDen > 0 {
		v := lthNum / lthDen
		out.SOPRLTH = &v
	}
	return out
}
