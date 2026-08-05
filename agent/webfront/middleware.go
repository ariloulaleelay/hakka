package webfront

import (
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// ETag middleware
// ---------------------------------------------------------------------------

// etagHandler wraps an http.Handler and adds ETag-based caching.
// It computes an ETag from the file's size, lazily cached per path.
// It handles If-None-Match → 304 Not Modified internally, before
// delegating to the inner handler, so the inner handler isn't
// invoked at all on cache hits (saving work, especially gzip).
func etagHandler(fsys webfrontFileSystem, next http.Handler) http.HandlerFunc {
	var mu sync.Mutex
	cache := make(map[string]string)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			next.ServeHTTP(w, r)
			return
		}

		urlPath := r.URL.Path
		if urlPath == "" || urlPath == "/" {
			urlPath = "/index.html"
		}

		etag := getOrComputeETag(&mu, cache, fsys, urlPath)
		if etag == "" {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("ETag", etag)

		// Handle If-None-Match locally so we can return 304 before
		// reaching the gzip handler (which would otherwise set
		// Content-Encoding: gzip on a bodyless 304 response).
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			if etagMatchList(inm, etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		next.ServeHTTP(w, r)
	}
}

// etagMatchList checks whether any ETag in a comma-separated
// If-None-Match list matches the server's ETag (weak comparison).
func etagMatchList(inm, serverETag string) bool {
	// * matches any ETag.
	if strings.TrimSpace(inm) == "*" {
		return true
	}

	// Strip W/ prefix for weak comparison.
	serverVal := strings.TrimPrefix(serverETag, "W/")

	for _, part := range strings.Split(inm, ",") {
		et := strings.TrimSpace(part)
		if et == "*" {
			return true
		}
		// Allow both "x" and W/"x" formats.
		et = strings.TrimPrefix(et, "W/")
		if et == serverVal {
			return true
		}
	}
	return false
}

// getOrComputeETag returns a cached ETag for the given path, or computes
// and caches it. Returns empty string if the file cannot be opened.
func getOrComputeETag(mu *sync.Mutex, cache map[string]string, fsys webfrontFileSystem, urlPath string) string {
	mu.Lock()
	etag, ok := cache[urlPath]
	mu.Unlock()
	if ok {
		return etag
	}

	f, err := fsys.Open(urlPath)
	if err != nil {
		return ""
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return ""
	}

	h := sha256.New()
	fmt.Fprintf(h, "%d.%d", info.Size(), info.ModTime().UnixNano())
	etag = fmt.Sprintf(`"%x"`, h.Sum(nil)[:16])

	mu.Lock()
	cache[urlPath] = etag
	mu.Unlock()

	return etag
}

// ---------------------------------------------------------------------------
// Gzip middleware
// ---------------------------------------------------------------------------

// compressibleExts lists file extensions that benefit from gzip compression.
var compressibleExts = map[string]bool{
	".js":   true,
	".css":  true,
	".html": true,
	".htm":  true,
	".json": true,
	".xml":  true,
	".svg":  true,
	".txt":  true,
	".ico":  true,
}

// gzipHandler wraps an http.Handler and compresses responses with gzip
// when the client advertises Accept-Encoding: gzip and the requested
// resource has a compressible file extension. Already-compressed formats
// (woff2, woff, png, gz, etc.) are passed through uncompressed.
func gzipHandler(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			next.ServeHTTP(w, r)
			return
		}

		ext := path.Ext(r.URL.Path)
		if !compressibleExts[ext] {
			next.ServeHTTP(w, r)
			return
		}

		// Always add Vary: Accept-Encoding for compressible paths so
		// caches know the response may differ based on encoding.
		addVaryAcceptEncoding(w)

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Setting Content-Encoding before delegating signals to
		// Go's http.ServeContent that it should skip Content-Length
		// (since the compressed size differs from the file size).
		w.Header().Set("Content-Encoding", "gzip")

		gw := gzip.NewWriter(w)
		defer gw.Close()

		gzw := &gzipResponseWriter{ResponseWriter: w, Writer: gw}
		next.ServeHTTP(gzw, r)
	}
}

// gzipResponseWriter wraps http.ResponseWriter, replacing writes with
// gzip-compressed writes.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// addVaryAcceptEncoding appends "Accept-Encoding" to the Vary header
// if not already present.
func addVaryAcceptEncoding(w http.ResponseWriter) {
	vary := w.Header().Get("Vary")
	if vary == "" {
		w.Header().Set("Vary", "Accept-Encoding")
	} else if !strings.Contains(vary, "Accept-Encoding") {
		w.Header().Set("Vary", vary+", Accept-Encoding")
	}
}
