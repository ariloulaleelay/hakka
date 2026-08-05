package webfront

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedFilesExist(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"webfront-dist/index.html"},
	}
	for _, tt := range tests {
		f, err := embeddedFiles.Open(tt.path)
		if err != nil {
			t.Fatalf("embedded file %q not found: %v", tt.path, err)
		}
		f.Close()
	}
}

func TestWeFrontFileSystem(t *testing.T) {
	// Test that our custom filesystem correctly serves index.html.
	fsys := webfrontFileSystem{inner: embeddedFiles}
	f, err := fsys.Open("/index.html")
	if err != nil {
		t.Fatalf("open /index.html via webfrontFileSystem: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 100)
	n, _ := f.Read(buf)
	if n == 0 {
		t.Fatal("index.html is empty")
	}
}

func TestAssetFilesAccessible(t *testing.T) {
	// Verify that asset files are accessible through our filesystem.
	fsys := webfrontFileSystem{inner: embeddedFiles}
	entries, err := fsys.Open("/assets/")
	if err != nil {
		t.Fatalf("open /assets/ via webfrontFileSystem: %v", err)
	}
	defer entries.Close()
}

func TestGatewayNew(t *testing.T) {
	// New requires valid components; these tests are done in main_test.go
	// integration tests. Here we just verify the type is correct.
	var gw *Gateway
	_ = gw
}

func TestGatewayEmptyAddrStart(t *testing.T) {
	// Start with empty addr on a minimal Gateway should be no-op.
	gw := &Gateway{addr: ""}
	if err := gw.Start(nil); err != nil {
		t.Fatalf("Start with empty addr should succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ETag middleware tests
// ---------------------------------------------------------------------------

func TestETagHeaderSet(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := etagHandler(fsys, http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Use /favicon.ico — it exists and doesn't trigger an index.html redirect.
	resp, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag header not set")
	}
	t.Logf("ETag: %s", etag)
}

func TestETagNotModified(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := etagHandler(fsys, http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// First request to get the ETag.
	resp1, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	etag := resp1.Header.Get("ETag")
	resp1.Body.Close()
	if etag == "" {
		t.Fatal("ETag header not set on first request")
	}

	// Second request with matching If-None-Match.
	req, _ := http.NewRequest("GET", srv.URL+"/favicon.ico", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with If-None-Match failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", resp2.StatusCode)
	}

	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Fatalf("expected empty body on 304, got %d bytes", len(body))
	}
}

func TestETagMismatch(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := etagHandler(fsys, http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Request with a non-matching ETag.
	req, _ := http.NewRequest("GET", srv.URL+"/favicon.ico", nil)
	req.Header.Set("If-None-Match", `"bogus-etag"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with bogus If-None-Match failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for mismatched ETag, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Gzip middleware tests
// ---------------------------------------------------------------------------

func TestGzipCompressesJS(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := gzipHandler(http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/assets/index.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	enc := resp.Header.Get("Content-Encoding")
	if enc != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", enc)
	}

	// Verify the body is valid gzip.
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("failed to decompress gzip body: %v", err)
	}
	if len(decompressed) == 0 {
		t.Fatal("decompressed body is empty")
	}
	t.Logf("decompressed size: %d bytes", len(decompressed))
}

func TestGzipNotCompressedWithoutHeader(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := gzipHandler(http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Use favicon.ico — compressible, but no Accept-Encoding header.
	resp, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("should not be gzip-compressed without Accept-Encoding header")
	}
}

func TestGzipVaryHeader(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := gzipHandler(http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Use favicon.ico — compressible, so Vary should be set even
	// without Accept-Encoding.
	resp, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	vary := resp.Header.Get("Vary")
	if !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("expected Vary header to contain Accept-Encoding, got %q", vary)
	}
}

func TestGzipSkipsAlreadyCompressed(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := gzipHandler(http.FileServer(http.FS(fsys)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// .woff2 is already compressed — gzip shouldn't double-compress it.
	req, _ := http.NewRequest("GET", srv.URL+"/assets/KaTeX_Main-Regular.woff2", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("woff2 files should not be double-compressed with gzip")
	}
}

// ---------------------------------------------------------------------------
// Combined ETag + gzip tests
// ---------------------------------------------------------------------------

func TestETagAndGzipCombined(t *testing.T) {
	fsys := webfrontFileSystem{inner: embeddedFiles}
	h := etagHandler(fsys, gzipHandler(http.FileServer(http.FS(fsys))))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// First request: get ETag + gzip.
	req1, _ := http.NewRequest("GET", srv.URL+"/assets/index.js", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	etag := resp1.Header.Get("ETag")
	resp1.Body.Close()
	if etag == "" {
		t.Fatal("ETag not set")
	}
	if resp1.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("Content-Encoding is not gzip")
	}

	// Second request: same ETag, should get 304.
	req2, _ := http.NewRequest("GET", srv.URL+"/assets/index.js", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET with If-None-Match failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", resp2.StatusCode)
	}
}
