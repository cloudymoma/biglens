package main

import (
	"reflect"
	"testing"
)

func TestParseOFACText(t *testing.T) {
	body := []byte("0x098B716B8Aaf21512996dC57EB0615e2383E2f96\n" +
		"  0x0330070fd38ec3bb94f58fa55d40368271e9e54a  \r\n" +
		"\n# comment line\n" +
		"TXYZtronAddressNotEvm1234567890\n" + // TRON lines in the USDT file must be dropped
		"0x1234\n")
	got, skipped := parseOFACText(body, "tagged USDT")
	want := []listEntry{
		{Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96", Label: "tagged USDT"},
		{Address: "0x0330070fd38ec3bb94f58fa55d40368271e9e54a", Label: "tagged USDT"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %+v, want %+v", got, want)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (tron + short)", skipped)
	}
}

func TestParseMEWDarklist(t *testing.T) {
	body := []byte(`[
	 {"address":"0xAbAbabababababababababababababababababAB","comment":"phishing site a","date":"2017-07-18"},
	 {"address":"0xabababababababababababababababababababab ","comment":"","date":"7/19/17"},
	 {"address":"0xabababababababababababababababababababab","comment":"phishing site b","date":""},
	 {"address":"not-an-address","comment":"x","date":""}
	]`)
	got, skipped, err := parseMEWDarklist(body)
	if err != nil {
		t.Fatal(err)
	}
	// Raw rows only; merging is mergeListEntries' job.
	if len(got) != 3 || skipped != 1 {
		t.Fatalf("len = %d skipped = %d, want 3 and 1: %+v", len(got), skipped, got)
	}
	if got[1].ListedAt != "2017-07-19" {
		t.Errorf("M/D/YY date not normalized: %q", got[1].ListedAt)
	}
	if _, _, err := parseMEWDarklist([]byte(`{"not":"an array"}`)); err == nil {
		t.Error("expected error for non-array JSON")
	}
}

func TestNormalizeListDate(t *testing.T) {
	for in, want := range map[string]string{
		"2017-07-18": "2017-07-18", "7/19/17": "2017-07-19", "12/1/20": "2020-12-01",
		"": "", "yesterday": "", "2017/07/18": "",
	} {
		if got := normalizeListDate(in); got != want {
			t.Errorf("normalizeListDate(%q) = %q, want %q", in, got, want)
		}
	}
}

// MEW has 715 rows but only 652 distinct addresses; duplicates carry
// different comments that must both survive (spec §6).
func TestMergeListEntries(t *testing.T) {
	in := []listEntry{
		{Address: "0xbb", Label: "b"},
		{Address: "0xaa", Label: "phishing site a", ListedAt: "2017-07-18"},
		{Address: "0xaa", Label: "", ListedAt: "2017-07-19"},
		{Address: "0xaa", Label: "phishing site b", ListedAt: ""},
		{Address: "0xaa", Label: "phishing site a", ListedAt: "2017-06-01"},
	}
	got := mergeListEntries(in)
	want := []listEntry{
		{Address: "0xaa", Label: "phishing site a; phishing site b", ListedAt: "2017-06-01"},
		{Address: "0xbb", Label: "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merge = %+v, want %+v", got, want)
	}
}

func TestDecideListUpdate(t *testing.T) {
	tests := []struct {
		name       string
		prev       syncState
		newCount   int
		newHash    string
		wantAccept bool
		wantReason string
	}{
		{"first sync", syncState{}, 120, "h1", true, ""},
		{"empty upstream rejected", syncState{RowCount: 120}, 0, "h0", false, "upstream list empty"},
		{"empty on first sync rejected too", syncState{}, 0, "h0", false, "upstream list empty"},
		{"small shrink accepted", syncState{RowCount: 124}, 120, "h2", true, ""},
		{"exactly half accepted", syncState{RowCount: 100}, 50, "h2", true, ""},
		{"big shrink rejected", syncState{RowCount: 652}, 3, "h3", false, "upstream shrank 652→3; kept previous snapshot"},
		{"same big shrink seen again accepted", syncState{RowCount: 652, PendingHash: "h3", PendingCount: 3}, 3, "h3", true, ""},
		{"different big shrink rejected again", syncState{RowCount: 652, PendingHash: "h3", PendingCount: 3}, 4, "h4", false, "upstream shrank 652→4; kept previous snapshot"},
		{"tiny lists exempt from shrink guard", syncState{RowCount: 19}, 2, "h5", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accept, reason := decideListUpdate(tt.prev, tt.newCount, tt.newHash)
			if accept != tt.wantAccept || reason != tt.wantReason {
				t.Errorf("got (%v, %q), want (%v, %q)", accept, reason, tt.wantAccept, tt.wantReason)
			}
		})
	}
}
