package main

// Background sync for the Address Risk snapshot lists. One goroutine, started
// from main (not NewAPIHandler, so tests never start it), ticks hourly and
// refreshes any list whose last successful fetch is older than 6 hours.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	riskListMaxBytes = 10 << 20 // largest list today is ~121 KB
	riskListEvery    = 6 * time.Hour
)

// upstreamErrCode maps a transport error to the spec's error enum. It never
// returns err.Error(): Go's *url.Error embeds the full URL, which would leak
// API keys (Etherscan, M3) and looked-up addresses into responses and logs.
func upstreamErrCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return "timeout"
	}
	return "network_error"
}

type riskListFile struct{ URL, Label string }

type riskListSource struct {
	ID    string
	Files []riskListFile
	Parse func(body []byte, label string) ([]listEntry, int, error)
}

func parseOFACBody(body []byte, label string) ([]listEntry, int, error) {
	e, skipped := parseOFACText(body, label)
	return e, skipped, nil
}

func parseMEWBody(body []byte, _ string) ([]listEntry, int, error) {
	return parseMEWDarklist(body)
}

// riskListSources: OFAC covers ETH, TRX, XBT plus the addresses OFAC tags as
// USDT/USDC across EVM, TRON, and Bitcoin (~900 addresses as of 2026-10-05).
var riskListSources = []riskListSource{
	{ID: "ofac", Parse: parseOFACBody, Files: []riskListFile{
		{URL: "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_ETH.txt"},
		{URL: "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_TRX.txt", Label: "tagged TRX"},
		{URL: "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_XBT.txt", Label: "tagged XBT"},
		{URL: "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_USDT.txt", Label: "tagged USDT"},
		{URL: "https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_USDC.txt", Label: "tagged USDC"},
	}},
	{ID: "mew_darklist", Parse: parseMEWBody, Files: []riskListFile{
		{URL: "https://raw.githubusercontent.com/MyEtherWallet/ethereum-lists/master/src/addresses/addresses-darklist.json"},
	}},
}

type riskListSyncer struct {
	store    *riskStore
	client   *http.Client
	now      func() time.Time
	sources  []riskListSource
	onChange func() // called after a list is replaced; may be nil
}

func newRiskListSyncer(store *riskStore) *riskListSyncer {
	return &riskListSyncer{
		store:   store,
		client:  &http.Client{Timeout: 30 * time.Second},
		now:     time.Now,
		sources: riskListSources,
	}
}

// readCapped reads at most max bytes and fails if the body is larger or the
// read errors. io.LimitReader alone returns a clean EOF at the cap, which
// would let a truncated list replace the local copy.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("body exceeds %d bytes", max)
	}
	return b, nil
}

// fetchListFile returns the body or an error-enum string (never a URL).
func (s *riskListSyncer) fetchListFile(ctx context.Context, url string) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "bad_request"
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	body, err := readCapped(resp.Body, riskListMaxBytes)
	if err != nil {
		return nil, "bad_response"
	}
	return body, ""
}

// syncSource fetches every file of one source, merges them, applies the
// anti-wipe guard and replaces the snapshot. Any file failure keeps old rows.
func (s *riskListSyncer) syncSource(ctx context.Context, src riskListSource) error {
	var all []listEntry
	h := sha256.New()
	skipped := 0
	for _, f := range src.Files {
		body, code := s.fetchListFile(ctx, f.URL)
		if code != "" {
			s.store.setSyncError(ctx, src.ID, code)
			return fmt.Errorf("%s: %s", src.ID, code)
		}
		entries, sk, err := src.Parse(body, f.Label)
		if err != nil {
			s.store.setSyncError(ctx, src.ID, "bad_response")
			return fmt.Errorf("%s: parse: %w", src.ID, err)
		}
		skipped += sk
		h.Write(body)
		h.Write([]byte{0})
		all = append(all, entries...)
	}
	merged := mergeListEntries(all)
	hash := hex.EncodeToString(h.Sum(nil))
	prev, err := s.store.getSyncState(ctx, src.ID)
	if err != nil {
		return err
	}
	if prev.PendingHash == "" && prev.ContentHash == hash && prev.RowCount > 0 {
		return s.store.markListUnchanged(ctx, src.ID, s.now())
	}
	if ok, reason := decideListUpdate(prev, len(merged), hash); !ok {
		s.store.markListRejected(ctx, src.ID, reason, hash, len(merged))
		return fmt.Errorf("%s: %s", src.ID, reason)
	}
	if err := s.store.replaceList(ctx, src.ID, merged, hash, s.now()); err != nil {
		return err
	}
	if s.onChange != nil {
		s.onChange()
	}
	slog.Info("address_risk sync", "source", src.ID, "rows", len(merged), "skipped", skipped)
	return nil
}

// syncDue refreshes sources never fetched or last fetched > 6 h ago. A
// rejected shrink leaves last_ok_at untouched, so it is retried each tick.
func (s *riskListSyncer) syncDue(ctx context.Context) {
	for _, src := range s.sources {
		st, err := s.store.getSyncState(ctx, src.ID)
		if err != nil {
			slog.Error("address_risk sync state", "source", src.ID, "error", err)
			continue
		}
		if st.LastOKAt != "" {
			if t, err := time.Parse(time.RFC3339, st.LastOKAt); err == nil && s.now().Sub(t) < riskListEvery {
				continue
			}
		}
		s.runOne(ctx, src)
	}
}

// runOne isolates a panic in one source so it becomes last_error instead of
// killing the process (there is no recover middleware anywhere).
func (s *riskListSyncer) runOne(ctx context.Context, src riskListSource) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("address_risk sync panic", "source", src.ID, "panic", r)
			s.store.setSyncError(context.Background(), src.ID, fmt.Sprint("panic: ", r))
		}
	}()
	if err := s.syncSource(ctx, src); err != nil {
		slog.Warn("address_risk sync failed", "source", src.ID, "error", err)
	}
}

// runAddressRiskSync runs every job now and then on each tick, in order, in
// one goroutine. Each job recovers its own panics (runOne, syncOnce).
func runAddressRiskSync(ctx context.Context, tick time.Duration, jobs ...func(context.Context)) {
	runAll := func() {
		for _, job := range jobs {
			job(ctx)
		}
	}
	runAll()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runAll()
		}
	}
}
