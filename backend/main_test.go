package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Long imports extend their own deadlines via http.ResponseController; that
// only works if every middleware wrapper exposes Unwrap(). Without it the
// extension fails silently and the server-wide 60s WriteTimeout kills the
// request mid-import.
func TestStatusWriterSupportsDeadlineExtension(t *testing.T) {
	done := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK} // as logMW wraps
		done <- http.NewResponseController(sw).SetWriteDeadline(time.Now().Add(time.Minute))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-done; err != nil {
		t.Errorf("SetWriteDeadline through statusWriter failed: %v", err)
	}
}
