package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/ariloulaleelay/hakka/agent"
)

// defaultUserAgent is the default User-Agent header sent with every request.
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type httpGetArgs struct {
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	MaxBytes int               `json:"max_bytes"`
}

// isHTMLContent checks if the content type or the body itself indicates HTML.
func isHTMLContent(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml") {
		return true
	}
	// If no Content-Type header, sniff the body for HTML signatures
	if contentType == "" {
		trimmed := strings.TrimSpace(string(body))
		if strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") ||
			strings.HasPrefix(trimmed, "<!DOCTYPE html") {
			return true
		}
		// Check for common HTML tags very early in the content
		if len(trimmed) > 0 && trimmed[0] == '<' {
			if strings.Contains(trimmed[:min(len(trimmed), 100)], "<") &&
				strings.Contains(trimmed[:min(len(trimmed), 100)], ">") {
				return true
			}
		}
	}
	return false
}

// HTTPGet fetches a URL with GET and returns a human-readable plain-text
// summary: status line, selected headers, and the body. HTML responses are
// automatically converted to Markdown for easier LLM consumption.
func HTTPGet() agent.Tool {
	return NewTool("http_get", "Perform an HTTP GET and return status, headers, and body. HTML content is automatically converted to Markdown for easier LLM reading.").
		StringParam("url", "", true).
		ObjectParam("headers", "Optional extra request headers.", false).
		IntParam("max_bytes", "Max body bytes to include (default 64KB).", false).
		Tags("network", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.URL == "" {
				return ""
			}
			return `url="` + params.URL + `"`
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args httpGetArgs
			if err := unmarshalToolArgs(raw, "http_get", &args); err != nil {
				return "", err
			}
			if args.MaxBytes <= 0 {
				args.MaxBytes = 64 * 1024
			}
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
			if err != nil {
				return "", err
			}
			// Default Mozilla User-Agent; individual headers can override it.
			httpReq.Header.Set("User-Agent", defaultUserAgent)
			for key, value := range args.Headers {
				httpReq.Header.Set(key, value)
			}

			// Create a fresh client per request to avoid HTTP/2 connection
			// multiplexing issues when the tool is called concurrently.
			// A shared http.Transport serialises connections per host when
			// the default DialContext is used, causing both timeouts and
			// empty response bodies under concurrent requests to the same host.
			// An explicit DialContext avoids this serialisation.
			dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
			tr := &http.Transport{
				DialContext: dialer.DialContext,
			}
			httpClient := &http.Client{
				Timeout:   30 * time.Second,
				Transport: tr,
			}
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(args.MaxBytes)+1))
			truncated := len(body) > args.MaxBytes
			if truncated {
				body = body[:args.MaxBytes]
			}

			// Always convert HTML to Markdown
			bodyStr := string(body)
			if isHTMLContent(resp.Header.Get("Content-Type"), body) {
				md, err := htmltomarkdown.ConvertString(bodyStr)
				if err == nil {
					bodyStr = strings.TrimSpace(md)
				}
				// On conversion error, fall through to returning the raw body
			}

			// Build plain-text output
			var b strings.Builder
			fmt.Fprintf(&b, "Status: %d\n", resp.StatusCode)
			// Include key headers
			for _, h := range []string{"Content-Type", "Content-Length"} {
				if v := resp.Header.Get(h); v != "" {
					fmt.Fprintf(&b, "%s: %s\n", h, v)
				}
			}
			if truncated {
				// Use Content-Length from response headers if available (set by real servers).
				omitted := 0
				if resp.ContentLength > 0 {
					omitted = int(resp.ContentLength) - args.MaxBytes
				}
				if omitted > 0 {
					fmt.Fprintf(&b, "\n%s\n\n[TRUNCATED: %d bytes omitted]\n", bodyStr, omitted)
				} else {
					fmt.Fprintf(&b, "\n%s\n\n[TRUNCATED]\n", bodyStr)
				}
			} else {
				fmt.Fprintf(&b, "\n%s\n", bodyStr)
			}
			return b.String(), nil
		}).
		Build()
}
