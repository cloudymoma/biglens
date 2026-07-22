package main

import (
	"reflect"
	"testing"
)

func TestSplitTrimmed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain list", "a,b", []string{"a", "b"}},
		{"trims spaces and tabs", " a ,\tb\t", []string{"a", "b"}},
		{"trims newlines and carriage returns", "a\r\n,\nb", []string{"a", "b"}},
		{"drops empty parts", ",a,, ,b,", []string{"a", "b"}},
		{"empty input", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitTrimmed(tt.in, ",")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitTrimmed(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
