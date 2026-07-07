package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// Shared HTTP + SSE helpers for adapters that hand-roll HTTP calls
// (Anthropic, Gemini). The OpenAI adapter uses the go-openai library instead.
// ---------------------------------------------------------------------------

// RawUsage carries the raw token and cost fields extracted from a provider
// response JSON body. It is an intermediate representation for parsing
// provider-specific usage shapes before translating into agent.Usage.
type RawUsage struct {
	cost             float64
	promptTokens     int
	completionTokens int
	totalTokens      int
	promptCacheHit   int
	promptCacheMiss  int
}

// extractCostFromUsage parses raw provider response JSON and extracts the
// "cost" field from the "usage" object. Returns 0 if not found or unparseable.
func extractCostFromUsage(body []byte) float64 {
	return extractRawUsage(body).cost
}

// extractRawUsage parses raw provider response JSON and extracts all known
// usage fields: cost, prompt_cache_hit_tokens, prompt_cache_miss_tokens,
// and standard token counts. Returns zero values for missing fields.
func extractRawUsage(body []byte) RawUsage {
	var resp struct {
		Usage struct {
			Cost             float64 `json:"cost"`
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			TotalTokens      int     `json:"total_tokens"`
			PromptCacheHit   int     `json:"prompt_cache_hit_tokens"`
			PromptCacheMiss  int     `json:"prompt_cache_miss_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return RawUsage{}
	}
	return RawUsage{
		cost:             resp.Usage.Cost,
		promptTokens:     resp.Usage.PromptTokens,
		completionTokens: resp.Usage.CompletionTokens,
		totalTokens:      resp.Usage.TotalTokens,
		promptCacheHit:   resp.Usage.PromptCacheHit,
		promptCacheMiss:  resp.Usage.PromptCacheMiss,
	}
}

// calculateCostFromPricing computes the monetary cost from token usage
// using the configured per-token prices. When cache breakdown is available
// (promptCacheHit + promptCacheMiss == promptTokens), cached tokens are
// priced at CacheHitInput and uncached at Input. Otherwise all prompt
// tokens use Input. Returns 0 when pricing is zero-valued (not configured).
func calculateCostFromPricing(pricing agent.Pricing, raw RawUsage) float64 {
	if pricing.Input == 0 && pricing.Output == 0 {
		slog.Debug("pricing_calculate: no pricing configured, cost=0")
		return 0
	}
	var inputCost float64
	if raw.promptCacheHit+raw.promptCacheMiss == raw.promptTokens && raw.promptCacheMiss > 0 {
		// Cache breakdown available — price hit and miss separately
		inputCost = float64(raw.promptCacheMiss)*pricing.Input + float64(raw.promptCacheHit)*pricing.CacheHitInput
		slog.Debug("pricing_calculate: cache breakdown",
			"miss_tokens", raw.promptCacheMiss, "miss_rate", pricing.Input,
			"hit_tokens", raw.promptCacheHit, "hit_rate", pricing.CacheHitInput,
			"input_cost", inputCost)
	} else {
		// No cache breakdown — price all prompt tokens at input rate
		inputCost = float64(raw.promptTokens) * pricing.Input
		slog.Debug("pricing_calculate: no cache breakdown",
			"prompt_tokens", raw.promptTokens, "input_rate", pricing.Input,
			"input_cost", inputCost)
	}
	outputCost := float64(raw.completionTokens) * pricing.Output
	total := inputCost + outputCost
	slog.Debug("pricing_calculate: total",
		"output_tokens", raw.completionTokens, "output_rate", pricing.Output,
		"output_cost", outputCost, "total", total)
	return total
}

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
//
// When debugDir is non-empty, the request body and response body are
// written as JSON files for debugging.
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
		return &errHTTPStatus{
			Prefix: prefix,
			Status: resp.Status,
			Body:   strings.TrimSpace(string(buf)),
		}
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if debugDir != "" {
		dumpDebugJSON(debugDir, prefix, "resp", string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return err
		}
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
//
// When debugDir is non-empty, the request body is written as a JSON file.
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

// ---------------------------------------------------------------------------
// Shared retry helpers for HTTP 429 rate-limit handling.
// ---------------------------------------------------------------------------

// isRateLimitError returns true when err is an *errHTTPStatus with a 429
// status code (Too Many Requests).
func isRateLimitError(err error) bool {
	var httpErr *errHTTPStatus
	if errors.As(err, &httpErr) {
		return strings.HasPrefix(httpErr.Status, "429")
	}
	return false
}

// retryOnRateLimit calls fn, retrying on HTTP 429 errors with exponential
// backoff. cfg controls retry parameters; zero values use defaults from
// agent.DefaultRetryConfig.
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
