package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

// errHTTPStatus is returned when the provider returns an HTTP error.
type errHTTPStatus struct {
	Prefix string // e.g. "anthropic" or "gemini"
	Status string // e.g. "400 Bad Request"
	Body   string
}

func (e *errHTTPStatus) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Prefix, e.Status, e.Body)
}

func isRateLimitError(err error) bool {
	var httpErr *errHTTPStatus
	if errors.As(err, &httpErr) {
		return strings.HasPrefix(httpErr.Status, "429")
	}
	return false
}

func retryOnRateLimit(ctx context.Context, cfg agent.RetryConfig, fn func() error) error {
	def := agent.DefaultRetryConfig()
	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = def.MaxAttempts
	}
	baseDelay := cfg.BaseDelay
	if baseDelay == 0 {
		baseDelay = def.BaseDelay
	}
	maxDelay := cfg.MaxDelay
	if maxDelay == 0 {
		maxDelay = def.MaxDelay
	}
	backoffFactor := cfg.BackoffFactor
	if backoffFactor == 0 {
		backoffFactor = def.BackoffFactor
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRateLimitError(err) || attempt == maxAttempts-1 {
			return err
		}
		delay := time.Duration(float64(baseDelay) * math.Pow(backoffFactor, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// doJSONPost marshals body to JSON, POSTs to url, checks status, decodes into result.
func doJSONPost(ctx context.Context, client *http.Client, url string, body any, result any, prefix string, extraHeaders map[string]string, debugDir string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	if debugDir != "" {
		dumpDebugJSON(debugDir, prefix, "req", body)
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
		return &errHTTPStatus{Prefix: prefix, Status: resp.Status, Body: strings.TrimSpace(string(buf))}
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if debugDir != "" {
		dumpDebugJSON(debugDir, prefix, "resp", string(respBody))
	}

	if result != nil {
		return json.Unmarshal(respBody, result)
	}
	return nil
}

// doJSONPostWithRetry wraps doJSONPost with rate-limit retry logic.
func doJSONPostWithRetry(ctx context.Context, client *http.Client, url string, body any, cfg agent.RetryConfig, prefix string, headers map[string]string, debugDir string) ([]byte, error) {
	var rawBody json.RawMessage
	if cfg.IsZero() {
		if err := doJSONPost(ctx, client, url, body, &rawBody, prefix, headers, debugDir); err != nil {
			return nil, err
		}
		return rawBody, nil
	}
	if err := retryOnRateLimit(ctx, cfg, func() error {
		return doJSONPost(ctx, client, url, body, &rawBody, prefix, headers, debugDir)
	}); err != nil {
		return nil, err
	}
	return rawBody, nil
}

// doStreamPost POSTs body to url and returns the response body on success.
// Caller must close the body.
func doStreamPost(ctx context.Context, client *http.Client, url string, body any, prefix string, extraHeaders map[string]string, debugDir string) (io.ReadCloser, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	if debugDir != "" {
		dumpDebugJSON(debugDir, prefix, "stream_req", body)
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
		return nil, &errHTTPStatus{Prefix: prefix, Status: resp.Status, Body: strings.TrimSpace(string(buf))}
	}
	return resp.Body, nil
}

// doStreamPostWithRetry wraps doStreamPost with rate-limit retry logic.
// When the retry config is zero-valued, the request is made once without
// retry (preserving backward compatibility with adapters that historically
// had no retry).
func doStreamPostWithRetry(ctx context.Context, client *http.Client, url string, payload any, cfg agent.RetryConfig, prefix string, headers map[string]string, debugDir string) (io.ReadCloser, error) {
	var body io.ReadCloser
	if cfg.IsZero() {
		var err error
		body, err = doStreamPost(ctx, client, url, payload, prefix, headers, debugDir)
		return body, err
	}
	err := retryOnRateLimit(ctx, cfg, func() error {
		var err error
		body, err = doStreamPost(ctx, client, url, payload, prefix, headers, debugDir)
		return err
	})
	return body, err
}
