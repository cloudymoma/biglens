package main

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var (
	btcDBIPBaseURL      = "https://download.db-ip.com/free"
	btcDBIPMinRows      = 50
	btcDBIPMaxBodyBytes = int64(32 << 20) // 32 MiB cap per .csv.gz file
)

type geoHit struct {
	Country string
	ASN     uint32
	Org     string
}

type dbipRangeRow struct {
	Start   netip.Addr
	End     netip.Addr
	Country string
	ASN     uint32
	Org     string
}

// mergeJoinIPs performs a single-pass streaming merge-join of sortedIPs (sorted
// in netip.Addr.Compare order: all IPv4 ascending, then all IPv6 ascending)
// against a sorted stream of IP ranges. No range table is held in memory.
func mergeJoinIPs(sortedIPs []netip.Addr, rows iter.Seq[dbipRangeRow]) map[netip.Addr]geoHit {
	hits := make(map[netip.Addr]geoHit, len(sortedIPs))
	mergeJoinIPsInto(hits, sortedIPs, rows)
	return hits
}

func mergeJoinIPsInto(hits map[netip.Addr]geoHit, sortedIPs []netip.Addr, rows iter.Seq[dbipRangeRow]) {
	if len(sortedIPs) == 0 || rows == nil {
		return
	}
	idx := 0
	n := len(sortedIPs)
	for r := range rows {
		if !r.Start.IsValid() || !r.End.IsValid() {
			continue
		}
		start := r.Start.Unmap()
		end := r.End.Unmap()
		// Skip ranges of a different address family if start > end
		if start.Compare(end) > 0 {
			continue
		}
		// Advance past any target IPs strictly below the current range start.
		for idx < n && sortedIPs[idx].Unmap().Compare(start) < 0 {
			idx++
		}
		if idx >= n {
			break
		}
		// Match all target IPs that fall within [start, end].
		for idx < n && sortedIPs[idx].Unmap().Compare(end) <= 0 {
			ip := sortedIPs[idx]
			cur := hits[ip]
			if r.Country != "" {
				cur.Country = r.Country
			}
			if r.ASN > 0 {
				cur.ASN = r.ASN
			}
			if r.Org != "" {
				cur.Org = r.Org
			}
			hits[ip] = cur
			idx++
		}
		if idx >= n {
			break
		}
	}
}

func streamDBIPCountryGz(path string) iter.Seq[dbipRangeRow] {
	return func(yield func(dbipRangeRow) bool) {
		f, err := os.Open(path)
		if err != nil {
			slog.Warn("dbip country open failed", "path", path, "error", err)
			return
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			slog.Warn("dbip country gzip reader failed", "path", path, "error", err)
			return
		}
		defer gz.Close()
		cr := csv.NewReader(gz)
		cr.FieldsPerRecord = -1
		cr.ReuseRecord = true
		var parseErrs int
		defer func() {
			if parseErrs > 0 {
				slog.Warn("dbip country stream parse errors", "path", path, "errors", parseErrs)
			}
		}()
		for {
			rec, err := cr.Read()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					parseErrs++
				}
				return
			}
			if len(rec) < 3 {
				parseErrs++
				continue
			}
			start, err1 := netip.ParseAddr(strings.TrimSpace(rec[0]))
			end, err2 := netip.ParseAddr(strings.TrimSpace(rec[1]))
			if err1 != nil || err2 != nil {
				parseErrs++
				continue
			}
			row := dbipRangeRow{
				Start:   start.Unmap(),
				End:     end.Unmap(),
				Country: strings.ToUpper(strings.TrimSpace(rec[2])),
			}
			if !yield(row) {
				return
			}
		}
	}
}

func streamDBIPASNGz(path string) iter.Seq[dbipRangeRow] {
	return func(yield func(dbipRangeRow) bool) {
		f, err := os.Open(path)
		if err != nil {
			slog.Warn("dbip asn open failed", "path", path, "error", err)
			return
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			slog.Warn("dbip asn gzip reader failed", "path", path, "error", err)
			return
		}
		defer gz.Close()
		cr := csv.NewReader(gz)
		cr.FieldsPerRecord = -1
		cr.ReuseRecord = true
		var parseErrs int
		defer func() {
			if parseErrs > 0 {
				slog.Warn("dbip asn stream parse errors", "path", path, "errors", parseErrs)
			}
		}()
		for {
			rec, err := cr.Read()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					parseErrs++
				}
				return
			}
			if len(rec) < 4 {
				parseErrs++
				continue
			}
			start, err1 := netip.ParseAddr(strings.TrimSpace(rec[0]))
			end, err2 := netip.ParseAddr(strings.TrimSpace(rec[1]))
			if err1 != nil || err2 != nil {
				parseErrs++
				continue
			}
			asn64, err := strconv.ParseUint(strings.TrimSpace(rec[2]), 10, 32)
			if err != nil {
				parseErrs++
				continue
			}
			if asn64 == 0 {
				continue
			}
			row := dbipRangeRow{
				Start: start.Unmap(),
				End:   end.Unmap(),
				ASN:   uint32(asn64),
				Org:   strings.TrimSpace(rec[3]),
			}
			if !yield(row) {
				return
			}
		}
	}
}

func validateDBIPGzFile(path string, minCols int, minRows int) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() < 64 {
		return fmt.Errorf("dbip file %s too small (%d B)", filepath.Base(path), st.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	cr := csv.NewReader(gz)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	rows := 0
	for rows < minRows {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(rec) < minCols {
			return fmt.Errorf("dbip column count = %d, want >= %d", len(rec), minCols)
		}
		rows++
	}
	if rows < minRows {
		return fmt.Errorf("dbip row count = %d, want >= %d", rows, minRows)
	}
	return nil
}

func latestExistingDBIPFile(geoDir, kind string) string {
	pattern := filepath.Join(geoDir, fmt.Sprintf("dbip-%s-lite-*.csv.gz", kind))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	slices.Sort(matches)
	return matches[len(matches)-1]
}

func ensureSingleDBIPFile(ctx context.Context, geoDir, kind, month string, minCols int) (string, error) {
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		return "", err
	}
	filename := fmt.Sprintf("dbip-%s-lite-%s.csv.gz", kind, month)
	targetPath := filepath.Join(geoDir, filename)
	if st, err := os.Stat(targetPath); err == nil && st.Size() > 0 {
		return targetPath, nil
	}

	rawURL := strings.TrimRight(btcDBIPBaseURL, "/") + "/" + filename
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", err
	}
	req.Header.Set("User-Agent", btcUserAgent)
	resp, err := btcHTTPClient.Do(req)
	if err != nil {
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", fmt.Errorf("dbip %s status %d", kind, resp.StatusCode)
	}

	tmpPath := targetPath + ".tmp"
	_ = os.Remove(tmpPath)
	outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", err
	}
	written, copyErr := io.Copy(outFile, io.LimitReader(resp.Body, btcDBIPMaxBodyBytes))
	closeErr := outFile.Close()
	if written >= btcDBIPMaxBodyBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("dbip %s exceeds size limit (%d bytes)", kind, btcDBIPMaxBodyBytes)
	}
	if copyErr != nil || closeErr != nil || written <= 0 {
		_ = os.Remove(tmpPath)
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", fmt.Errorf("dbip %s write failed", kind)
	}

	if err := validateDBIPGzFile(tmpPath, minCols, btcDBIPMinRows); err != nil {
		_ = os.Remove(tmpPath)
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		if prev := latestExistingDBIPFile(geoDir, kind); prev != "" {
			return prev, nil
		}
		return "", err
	}
	return targetPath, nil
}

// dbipFiles ensures that dbip-country-lite-YYYY-MM.csv.gz and
// dbip-asn-lite-YYYY-MM.csv.gz exist in geoDir (default "data/geo"),
// downloading once per month via a temp file + sanity check + atomic rename
// and falling back to the previous month's file on failure.
func dbipFiles(ctx context.Context, geoDir, month string) (countryPath, asnPath string, err error) {
	if geoDir == "" {
		geoDir = filepath.Join("data", "geo")
	}
	countryPath, err = ensureSingleDBIPFile(ctx, geoDir, "country", month, 3)
	if err != nil {
		return "", "", err
	}
	asnPath, err = ensureSingleDBIPFile(ctx, geoDir, "asn", month, 4)
	if err != nil {
		return "", "", err
	}
	return countryPath, asnPath, nil
}

var btcCountryNames = map[string]string{
	"US": "United States of America",
	"DE": "Germany",
	"FR": "France",
	"NL": "Netherlands",
	"FI": "Finland",
	"CA": "Canada",
	"GB": "United Kingdom",
	"CH": "Switzerland",
	"JP": "Japan",
	"SG": "Singapore",
	"AU": "Australia",
	"SE": "Sweden",
	"AT": "Austria",
	"IE": "Ireland",
	"NO": "Norway",
	"PL": "Poland",
	"CZ": "Czechia",
	"IT": "Italy",
	"ES": "Spain",
	"BR": "Brazil",
	"KR": "South Korea",
	"HK": "Hong Kong",
	"TW": "Taiwan",
	"RU": "Russia",
	"UA": "Ukraine",
	"LT": "Lithuania",
	"RO": "Romania",
	"BG": "Bulgaria",
	"BE": "Belgium",
	"DK": "Denmark",
	"PT": "Portugal",
	"ZA": "South Africa",
	"IN": "India",
	"MX": "Mexico",
	"AR": "Argentina",
	"CL": "Chile",
	"NZ": "New Zealand",
	"IS": "Iceland",
}

func btcCountryName(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if name, ok := btcCountryNames[code]; ok {
		return name
	}
	return code
}
