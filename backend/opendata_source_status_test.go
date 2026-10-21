package main

import (
	"encoding/json"
	"testing"
)

// The Bitcoin and stablecoin dashboards share one source-badge type; the rename
// from SourceStatus must not change its wire format.
func TestSourceStatusJSONShape(t *testing.T) {
	cases := []struct {
		in   SourceStatus
		want string
	}{
		{SourceStatus{ID: "mempool", Label: "mempool.space", Status: "ok"},
			`{"id":"mempool","label":"mempool.space","status":"ok"}`},
		{SourceStatus{ID: "a", Label: "b", Status: "stale", Via: "c", AsOf: "d", Error: "e", License: "f"},
			`{"id":"a","label":"b","status":"stale","via":"c","as_of":"d","error":"e","license":"f"}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("SourceStatus JSON = %s, want %s", got, c.want)
		}
	}
}
