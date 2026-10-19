package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rates clients (stablecoin.md §4.4): ECB EUR/USD reference rate and €STR
// (SDMX-JSON), and the US Treasury daily bill-rate CSV (13-week coupon
// equivalent). ECB reuse requires attribution ("Source: ECB").

type stableRateObs struct {
	Day   string // YYYY-MM-DD
	Value float64
}

// Expected series dimensions; a response for another series is rejected.
var (
	stableECBEURUSDDims = map[string]string{"FREQ": "D", "CURRENCY": "USD", "CURRENCY_DENOM": "EUR", "EXR_TYPE": "SP00", "EXR_SUFFIX": "A"}
	stableECBESTRDims   = map[string]string{"BENCHMARK_ITEM": "EU000A2X2A25", "DATA_TYPE_EST": "WT"}
)

// parseECBSeries parses an SDMX-JSON document holding exactly one series and
// checks its series dimensions against want. Null observations are skipped.
func parseECBSeries(body []byte, want map[string]string) ([]stableRateObs, error) {
	type dimValue struct {
		ID string `json:"id"`
	}
	type dimension struct {
		ID     string     `json:"id"`
		Values []dimValue `json:"values"`
	}
	var raw struct {
		DataSets []struct {
			Series map[string]struct {
				Observations map[string][]json.RawMessage `json:"observations"`
			} `json:"series"`
		} `json:"dataSets"`
		Structure struct {
			Dimensions struct {
				Series      []dimension `json:"series"`
				Observation []dimension `json:"observation"`
			} `json:"dimensions"`
		} `json:"structure"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw.DataSets) != 1 || len(raw.DataSets[0].Series) != 1 {
		return nil, errors.New("want exactly one dataset with one series")
	}
	var seriesKey string
	var obs map[string][]json.RawMessage
	for k, s := range raw.DataSets[0].Series {
		seriesKey, obs = k, s.Observations
	}
	// The series key holds one index per series dimension ("0:0:0:0:0").
	idx := strings.Split(seriesKey, ":")
	dims := raw.Structure.Dimensions.Series
	if len(idx) != len(dims) {
		return nil, errors.New("series key does not match the series dimensions")
	}
	got := make(map[string]string, len(dims))
	for i, d := range dims {
		n, err := strconv.Atoi(idx[i])
		if err != nil || n < 0 || n >= len(d.Values) {
			return nil, errors.New("bad series key")
		}
		got[d.ID] = d.Values[n].ID
	}
	for k, v := range want {
		if got[k] != v {
			return nil, fmt.Errorf("series dimension %s = %q, want %q", k, got[k], v)
		}
	}
	if len(raw.Structure.Dimensions.Observation) != 1 || raw.Structure.Dimensions.Observation[0].ID != "TIME_PERIOD" {
		return nil, errors.New("missing TIME_PERIOD observation dimension")
	}
	periods := raw.Structure.Dimensions.Observation[0].Values
	var out []stableRateObs
	for k, vals := range obs {
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 || n >= len(periods) {
			return nil, errors.New("bad observation index")
		}
		day := periods[n].ID
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return nil, fmt.Errorf("bad TIME_PERIOD %q", day)
		}
		if len(vals) == 0 {
			continue
		}
		var v *float64
		if err := json.Unmarshal(vals[0], &v); err != nil {
			return nil, fmt.Errorf("bad observation value for %s", day)
		}
		if v == nil {
			continue
		}
		out = append(out, stableRateObs{Day: day, Value: *v})
	}
	if len(out) == 0 {
		return nil, errors.New("no observations")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

const treasury13WColumn = "13 WEEKS COUPON EQUIVALENT"

// parseTreasuryBillCSV returns the 13-week coupon-equivalent yields (percent)
// oldest first. Rows without a value (N/A, empty) are skipped.
func parseTreasuryBillCSV(body []byte) ([]stableRateObs, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	dateCol, valCol := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")) {
		case "Date":
			dateCol = i
		case treasury13WColumn:
			valCol = i
		}
	}
	if dateCol < 0 || valCol < 0 {
		return nil, errors.New("missing Date or 13-week coupon-equivalent column")
	}
	var out []stableRateObs
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row: %w", err)
		}
		if len(rec) <= max(dateCol, valCol) {
			return nil, errors.New("short row")
		}
		d, err := time.Parse("01/02/2006", strings.TrimSpace(rec[dateCol]))
		if err != nil {
			return nil, fmt.Errorf("bad date %q", rec[dateCol])
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rec[valCol]), 64)
		if err != nil {
			continue // N/A or empty: no published value that day
		}
		out = append(out, stableRateObs{Day: d.Format(time.DateOnly), Value: v})
	}
	if len(out) == 0 {
		return nil, errors.New("no rows")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

func fetchECBSeries(ctx context.Context, ep stableEndpoints, path string, n int, dims map[string]string) ([]stableRateObs, string) {
	u := fmt.Sprintf("%s/%s?lastNObservations=%d&format=jsondata", ep.ECBBase, path, max(n, 1))
	body, code := stableFetch(ctx, u, stableTimeoutSmall, stableMaxBodySmall)
	if code != "" {
		return nil, code
	}
	obs, err := parseECBSeries(body, dims)
	if err != nil {
		return nil, "bad_response"
	}
	return obs, ""
}

// fetchECBEURUSD returns the last n ECB EUR/USD reference rates (USD per EUR).
func fetchECBEURUSD(ctx context.Context, ep stableEndpoints, n int) ([]stableRateObs, string) {
	return fetchECBSeries(ctx, ep, "EXR/D.USD.EUR.SP00.A", n, stableECBEURUSDDims)
}

// fetchECBESTR returns the last n €STR fixings (percent per year).
func fetchECBESTR(ctx context.Context, ep stableEndpoints, n int) ([]stableRateObs, string) {
	return fetchECBSeries(ctx, ep, "EST/B.EU000A2X2A25.WT", n, stableECBESTRDims)
}

// fetchTreasury13W returns one calendar year of 13-week bill yields (percent).
func fetchTreasury13W(ctx context.Context, ep stableEndpoints, year int) ([]stableRateObs, string) {
	u := fmt.Sprintf("%s/%d/all?type=daily_treasury_bill_rates&field_tdr_date_value=%d&page&_format=csv", ep.TreasuryBase, year, year)
	body, code := stableFetch(ctx, u, stableTimeoutSmall, stableMaxBodySmall)
	if code != "" {
		return nil, code
	}
	obs, err := parseTreasuryBillCSV(body)
	if err != nil {
		return nil, "bad_response"
	}
	return obs, ""
}
