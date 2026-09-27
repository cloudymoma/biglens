package main

// Manual backfill of USDT/USDC freeze history (spec §7.3):
//
//	cd /opt/biglens/backend && sudo -u biglens ./biglens-server --address-risk-backfill [--since 2017-11-28 | --since-days N] [--yes]
//
// Without --yes it only prints the dry-run estimate (free). It first closes a
// forward gap after the cursor (31-day chunks), then fills whole years from
// coverage_from back to --since, newest first; every batch commits its events
// and watermark together, so a failed run resumes where it stopped.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/civil"
)

// backfillMinMaxBytes floors MaxBytesBilled: 0 would mean "no limit".
const backfillMinMaxBytes = 64 << 20

type backfillOpts struct {
	Since civil.Date
	Yes   bool
}

type backfillBatch struct {
	Label              string
	Start, End         civil.Date // [Start, End)
	NewFrom, NewCursor string
}

// parseBackfillOpts validates the flags; --since and --since-days exclude
// each other and nothing earlier than the first USDT freeze is queried.
func parseBackfillOpts(since string, sinceDays int, yes bool, now time.Time) (backfillOpts, error) {
	if since != "" && sinceDays != 0 {
		return backfillOpts{}, errors.New("--since and --since-days are mutually exclusive")
	}
	if sinceDays < 0 {
		return backfillOpts{}, errors.New("--since-days must be positive")
	}
	d := stablecoinFirstDate
	if since != "" {
		p, err := civil.ParseDate(since)
		if err != nil {
			return backfillOpts{}, fmt.Errorf("--since must be YYYY-MM-DD: %q", since)
		}
		d = p
	}
	if sinceDays > 0 {
		d = civil.DateOf(now.UTC()).AddDays(-sinceDays)
	}
	if d.Before(stablecoinFirstDate) {
		d = stablecoinFirstDate
	}
	return backfillOpts{Since: d, Yes: yes}, nil
}

func laterDate(a, b civil.Date) civil.Date {
	if a.After(b) {
		return a
	}
	return b
}

func earlierDate(a, b civil.Date) civil.Date {
	if a.Before(b) {
		return a
	}
	return b
}

// planBackfill lists batches newest first. end is safeEnd (exclusive).
func planBackfill(st syncState, since, end civil.Date) ([]backfillBatch, error) {
	var out []backfillBatch
	var from civil.Date
	if st.Cursor == "" {
		first := laterDate(since, civil.Date{Year: end.AddDays(-1).Year, Month: time.January, Day: 1})
		if !first.Before(end) {
			return nil, nil
		}
		out = append(out, backfillBatch{Label: strconv.Itoa(first.Year), Start: first, End: end,
			NewFrom: first.String(), NewCursor: end.AddDays(-1).String()})
		from = first
	} else {
		cur, err := civil.ParseDate(st.Cursor)
		if err != nil {
			return nil, fmt.Errorf("bad cursor %q", st.Cursor)
		}
		from = cur.AddDays(1)
		if st.CoverageFrom != "" {
			if from, err = civil.ParseDate(st.CoverageFrom); err != nil {
				return nil, fmt.Errorf("bad coverage_from %q", st.CoverageFrom)
			}
		}
		for s := cur.AddDays(1); s.Before(end); {
			e := earlierDate(s.AddDays(stablecoinMaxWindowDays), end)
			out = append(out, backfillBatch{Label: "forward gap", Start: s, End: e, NewCursor: e.AddDays(-1).String()})
			s = e
		}
	}
	for from.After(since) {
		start := laterDate(since, civil.Date{Year: from.AddDays(-1).Year, Month: time.January, Day: 1})
		out = append(out, backfillBatch{Label: strconv.Itoa(start.Year), Start: start, End: from, NewFrom: start.String()})
		from = start
	}
	return out, nil
}

// runAddressRiskBackfill prints progress to out (stdout: slog goes to a file
// the operator does not see).
func runAddressRiskBackfill(ctx context.Context, src stablecoinSource, store *riskStore, opts backfillOpts, now time.Time, out io.Writer) error {
	end, ok, err := safeEnd(ctx, src, now)
	if err != nil {
		return fmt.Errorf("BigQuery block check: %w", err)
	}
	if !ok {
		return errors.New("BigQuery has not exported recent blocks yet; retry in an hour")
	}
	st, err := store.getSyncState(ctx, stablecoinSourceID)
	if err != nil {
		return err
	}
	batches, err := planBackfill(st, opts.Since, end)
	if err != nil {
		return err
	}
	if len(batches) == 0 {
		fmt.Fprintf(out, "Nothing to backfill: coverage is %s to %s.\n", st.CoverageFrom, st.Cursor)
		return nil
	}
	sizes := make([]int64, len(batches))
	var total int64
	for i, b := range batches {
		n, err := src.DryRun(ctx, b.Start, b.End)
		if err != nil {
			return fmt.Errorf("dry run %s: %w", b.Label, err)
		}
		sizes[i], total = n, total+n
		fmt.Fprintf(out, "%-12s %s to %s  %8.1f GB  ~$%.2f\n", b.Label, b.Start, b.End.AddDays(-1), float64(n)/1e9, bytesToUSD(n))
	}
	fmt.Fprintf(out, "Total: %.1f GB, ~$%.2f (dry-run estimate; actual billing is usually lower)\n", float64(total)/1e9, bytesToUSD(total))
	if !opts.Yes {
		fmt.Fprintln(out, "Dry run only — nothing was billed. Re-run with --yes to backfill.")
		return nil
	}
	for i, b := range batches {
		maxBytes := max(sizes[i]+sizes[i]/10, backfillMinMaxBytes) // dry-run × 1.1
		events, billed, err := src.Fetch(ctx, b.Start, b.End, maxBytes)
		if err != nil {
			return fmt.Errorf("%s: %w (re-run to resume)", b.Label, err)
		}
		if err := store.insertStablecoinEvents(ctx, events, b.NewFrom, b.NewCursor, now); err != nil {
			return fmt.Errorf("%s: store: %w", b.Label, err)
		}
		fmt.Fprintf(out, "%-12s done: %d events, %.1f GB billed\n", b.Label, len(events), float64(billed)/1e9)
	}
	fmt.Fprintln(out, "Backfill complete.")
	return nil
}

// runBackfillCLI is the --address-risk-backfill entry point; it returns the
// process exit code. Only the config, BigQuery client and store are created.
func runBackfillCLI(ctx context.Context, cfg *Config, src stablecoinSource, since string, sinceDays int, yes bool) int {
	opts, err := parseBackfillOpts(since, sinceDays, yes, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	store, err := openRiskStore(cfg.AddressRisk.dbPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: open", cfg.AddressRisk.dbPath()+":", err)
		return 1
	}
	defer store.Close()
	if err := runAddressRiskBackfill(ctx, src, store, opts, time.Now(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
