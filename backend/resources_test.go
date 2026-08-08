package main

import "testing"

func TestValidResourceProject(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"my-project-123", true},
		{"abcdef", true},
		{"a12345", true},
		{"Bad-Caps", false},        // uppercase not allowed
		{"1starts-with-digit", false},
		{"ab", false},              // too short (min 6 chars total)
		{"has_underscore", false},
		{"ends-with-hyphen-", false},
		{"", false},
		{"proj; DROP TABLE", false},
	}
	for _, c := range cases {
		if got := validResourceProject(c.in); got != c.ok {
			t.Errorf("validResourceProject(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}
