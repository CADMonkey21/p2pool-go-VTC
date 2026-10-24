package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func getStatic(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

// With no web/static directory on disk the embedded copy must serve the
// dashboard and its assets, including the MIT notice in main.css.
func TestStaticFSEmbedded(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "web", "static")
	fsys, src := StaticFS(missing)
	if src != "embedded" {
		t.Fatalf("source = %q, want embedded", src)
	}
	h := http.FileServer(fsys)

	code, body := getStatic(t, h, "/")
	if code != 200 || !strings.Contains(body, "n-js/p2pool.js") {
		t.Fatalf("GET / = %d, dashboard page not served", code)
	}
	code, body = getStatic(t, h, "/n-js/p2pool.js")
	if code != 200 || !strings.Contains(body, `api_url = "/api/stats"`) {
		t.Fatalf("GET /n-js/p2pool.js = %d, asset not served", code)
	}
	code, body = getStatic(t, h, "/n-css/main.css")
	if code != 200 || !strings.Contains(body, "License: MIT") {
		t.Fatalf("GET /n-css/main.css = %d, license header missing", code)
	}
	if code, _ = getStatic(t, h, "/img/vertcoin-logo.png"); code != 200 {
		t.Fatalf("GET /img/vertcoin-logo.png = %d", code)
	}
}

// A web/static directory on disk overrides the embedded copy.
func TestStaticFSDiskOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("dev build"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys, src := StaticFS(filepath.Join(dir, "missing"), dir)
	if src != dir {
		t.Fatalf("source = %q, want %q", src, dir)
	}
	if code, body := getStatic(t, http.FileServer(fsys), "/"); code != 200 || body != "dev build" {
		t.Fatalf("GET / = %d %q, want disk copy", code, body)
	}
}
