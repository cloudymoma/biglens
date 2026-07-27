package main

import (
	"regexp"
	"testing"
)

// industryThemes is compiled into a BigQuery regex; both Go and BigQuery use
// RE2, so validating the pattern with Go's regexp proves it is safe and has
// the intended semantics before it ever reaches a paid query.
func TestIndustryThemesWellFormed(t *testing.T) {
	want := []string{"finance", "retail", "biomedical", "education"}
	if len(industryThemes) != len(want) {
		t.Fatalf("industryThemes has %d keys, want %d", len(industryThemes), len(want))
	}
	nameRe := regexp.MustCompile(`^[A-Z0-9_]+$`)
	for _, k := range want {
		themes, ok := industryThemes[k]
		if !ok {
			t.Fatalf("missing industry %q", k)
		}
		if len(themes) == 0 {
			t.Fatalf("industry %q has no themes", k)
		}
		for _, th := range themes {
			if !nameRe.MatchString(th) {
				t.Errorf("industry %q theme %q is not regex-safe [A-Z0-9_]", k, th)
			}
		}
	}
}

func TestIndustryKeysSorted(t *testing.T) {
	keys := industryKeys()
	if len(keys) != 4 {
		t.Fatalf("got %d keys, want 4", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys not sorted: %v", keys)
		}
	}
}

// V2Themes is `NAME,offset;NAME,offset;...`. The pattern must anchor each
// match to an entry start ((?:^|;)) and give prefix semantics so theme
// families (TAX_DISEASE_*) match their root name.
func TestIndustryThemeRegex(t *testing.T) {
	re := regexp.MustCompile(industryThemeRegex([]string{"ECON_STOCKMARKET", "TAX_DISEASE"}))
	cases := []struct {
		in   string
		want bool
	}{
		{"ECON_STOCKMARKET,123;OTHER,4", true},
		{"OTHER,4;ECON_STOCKMARKET,123", true},
		{"TAX_DISEASE_COVID19,55", true},
		{"XECON_STOCKMARKET,1", false},
		{"UNRELATED,9", false},
		{"", false},
	}
	for _, tt := range cases {
		if got := re.MatchString(tt.in); got != tt.want {
			t.Errorf("MatchString(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
