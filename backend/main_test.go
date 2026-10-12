package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestStaticFileHandlerCacheHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "GraphCanvas-abc123.js"), []byte("export default {}"), 0644); err != nil {
		t.Fatal(err)
	}
	h := staticFileHandler(dir)

	reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
	recRoot := httptest.NewRecorder()
	h.ServeHTTP(recRoot, reqRoot)
	if got := recRoot.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("GET / Cache-Control = %q, want %q", got, "no-cache")
	}

	reqAsset := httptest.NewRequest(http.MethodGet, "/assets/GraphCanvas-abc123.js", nil)
	recAsset := httptest.NewRecorder()
	h.ServeHTTP(recAsset, reqAsset)
	if got := recAsset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("GET /assets/... (200) Cache-Control = %q, want %q", got, "public, max-age=31536000, immutable")
	}

	reqMissing := httptest.NewRequest(http.MethodGet, "/assets/GraphCanvas-stale.js", nil)
	recMissing := httptest.NewRecorder()
	h.ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusNotFound {
		t.Errorf("GET missing asset status = %d, want 404", recMissing.Code)
	}
	if got := recMissing.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET /assets/... (404) Cache-Control = %q, want %q", got, "no-store")
	}
}


