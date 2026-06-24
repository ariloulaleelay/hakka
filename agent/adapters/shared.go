package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Shared HTTP + SSE helpers for adapters that hand-roll HTTP calls
// (Anthropic, Gemini). The OpenAI adapter uses the go-openai library instead.
// ---------------------------------------------------------------------------

// errHTTPStatus is returned when the provider returns an HTTP error.
type errHTTPStatus struct {
	Prefix string // used in error message, e.g. "anthropic" or "gemini"
	Status string // e.g. "400 Bad Request"
	Body   string
}

func (e *errHTTPStatus) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Prefix, e.Status, e.Body)
}

// doJSONPost marshals body to JSON, POSTs it to the given URL with
// Content-Type: application/json, checks the HTTP status (>=400 is an error),
// and decodes the response body into result (if result is non-nil).
//
// extraHeaders are additional HTTP headers to set on the request (e.g.
// "anthropic-version").
//
// On HTTP-level errors returns *errHTTPStatus with the given prefix so
// callers can identify the provider. On transport errors returns the
// underlying Go error unwrapped.
func doJSONPost(ctx context.Context, client *http.Client, url string, body any, result any, prefix string, extraHeaders map[string]string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		return &errHTTPStatus{
			Prefix: prefix,
			Status: resp.Status,
			Body:   strings.TrimSpace(string(buf)),
		}
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

// doStreamPost is the streaming counterpart of doJSONPost. It marshals the
// body, POSTs to url with extraHeaders, checks for HTTP errors (>=400), and
// returns the response body on success. The caller must close the body.
//
// Used by Anthropic and Gemini adapters for their streaming endpoints where
// the response body is read incrementally (SSE) rather than decoded in one
// shot.
func doStreamPost(ctx context.Context, client *http.Client, url string, body any, prefix string, extraHeaders map[string]string) (io.ReadCloser, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &errHTTPStatus{
			Prefix: prefix,
			Status: resp.Status,
			Body:   strings.TrimSpace(string(buf)),
		}
	}
	return resp.Body, nil
}

// scanSSELines reads lines from body, filters for "data:" SSE events,
// and calls onPayload for each non-empty, non-[DONE] payload.
// Returns the first error from onPayload (which may be a sentinel like
// agent.ErrToolCallRequested) or a scanner error.
func scanSSELines(ctx context.Context, body io.Reader, onPayload func(string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if err := onPayload(payload); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
