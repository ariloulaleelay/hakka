package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

const (
	openAIRetryAttempts  = 20
	openAIRetryBaseDelay = 250 * time.Millisecond
)

// OpenAIAdapter implements agent.LLMAdapter against the OpenAI chat API.
type OpenAIAdapter struct {
	Client      *openai.Client
	Model       string
	LLMDebugDir string // when non-empty, request/response payloads are logged here

	// Extra carries static body fields to inject into every LLM request.
	// Values may contain placeholders like "$session_id" that are resolved
	// at request time using values from CompleteOptions.
	Extra   map[string]any
	Pricing agent.Pricing // per-token pricing for cost calculation when provider doesn't return cost

	// RetryConfig controls the retry policy for HTTP 429 rate-limit errors.
	// Zero values use sensible defaults.
	RetryConfig agent.RetryConfig
}

// ---------------------------------------------------------------------------
// instrumentedTransport — HTTP middleware that observes and optionally
// modifies outgoing LLM API requests.
//
// It wraps the HTTP transport used by the go-openai client, providing:
//   - Debug dump: when debugDir is set, the final wire request body is
//     written to a JSON file for inspection.
//   - Extra injection: when extra fields are configured (static + per-request),
//     they are merged into the JSON request body before forwarding.
//   - Placeholder resolution: "$session_id" in string extra values is
//     replaced with the actual session ID at request time.
//   - Response usage capture: cost, prompt_cache_hit_tokens, and
//     prompt_cache_miss_tokens are extracted from streaming and non-streaming
//     responses for cost calculation.
//
// Unlike the old extraRoundTripper, instrumentedTransport always dumps the
// request body when debugDir is non-empty, regardless of whether extra fields
// are configured. This ensures --llm-debug works for all models, not just
// those with extra body fields.
// ---------------------------------------------------------------------------

type ctxKey string

const (
	ctxExtraPerReq    ctxKey = "extra_per_req"
	ctxSessionID      ctxKey = "session_id"
	ctxCapturedUsage  ctxKey = "captured_usage" // *RawUsage to receive extracted usage extras
)

// ContextWithCapturedUsage stores a *RawUsage pointer that the transport will
// populate with cost, prompt_cache_hit_tokens, and prompt_cache_miss_tokens
// extracted from the provider response.
func ContextWithCapturedUsage(ctx context.Context, ptr *RawUsage) context.Context {
	return context.WithValue(ctx, ctxCapturedUsage, ptr)
}

// instrumentedTransport wraps an http.RoundTripper and injects extra fields
// into the JSON request body before forwarding. It also captures cost
// from response bodies and dumps request/response payloads when debugDir
// is configured.
type instrumentedTransport struct {
	base       http.RoundTripper
	extra      map[string]any // static extras from config (may contain placeholders)
	debugDir   string         // when non-empty, dump final wire body here
}

// usageCaptureBody wraps an io.ReadCloser (SSE response body for streaming)
// and scans for "usage" objects containing cost and cache token fields,
// storing the last seen usage extras into the provided RawUsage pointer.
type usageCaptureBody struct {
	io.ReadCloser
	raw       *RawUsage
	remainder []byte
}

func (b *usageCaptureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		data := append(b.remainder, p[:n]...)
		b.remainder = nil

		// Process complete SSE lines
		for {
			idx := bytes.IndexByte(data, '\n')
			if idx < 0 {
				// Incomplete line — save for next read
				b.remainder = append(b.remainder[:0], data...)
				break
			}
			line := string(data[:idx])
			data = data[idx+1:]

			// Check for SSE data line containing usage
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimSpace(line[6:])
				raw := extractRawUsage([]byte(payload))
				// Capture any usage data — standard tokens, cost, or cache tokens.
				if raw.totalTokens > 0 || raw.cost > 0 || raw.promptCacheHit > 0 || raw.promptCacheMiss > 0 {
					*b.raw = raw
					slog.Debug("usage_capture_body: captured usage from SSE",
						"prompt_tokens", raw.promptTokens,
						"completion_tokens", raw.completionTokens,
						"total_tokens", raw.totalTokens,
						"cost", raw.cost,
						"cache_hit", raw.promptCacheHit,
						"cache_miss", raw.promptCacheMiss)
				}
			}
		}
	}
	return n, err
}

// RoundTrip intercepts the request, merges extras into the body, and forwards.
// For non-streaming responses, it reads the full body to extract cost.
// For streaming responses, it wraps the body to capture cost from SSE events.
func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	perReqExtra, _ := req.Context().Value(ctxExtraPerReq).(map[string]any)
	sessionID, _ := req.Context().Value(ctxSessionID).(string)

	merged := mergeExtra(t.extra, perReqExtra, sessionID)

	// Read the existing JSON body — we need it even without extras for debug dump
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()

	// If no extras and no debugDir, just forward the original request
	if len(merged) == 0 && t.debugDir == "" {
		req.Body = io.NopCloser(bytes.NewReader(body))
		resp, err := t.base.RoundTrip(req)
		return t.handleResponse(req, resp, err)
	}

	var newBody []byte

	if len(merged) > 0 {
		// Merge extra fields into the JSON body
		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			return nil, err
		}
		for k, v := range merged {
			bodyMap[k] = v
		}
		newBody, err = json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}
	} else {
		// No extras, but debugDir is set — use original body
		newBody = body
	}

	// Dump final wire body to debug directory if configured
	if t.debugDir != "" {
		n := reqCounter.Add(1)
		ts := time.Now().UnixNano()
		dumpPath := filepath.Join(t.debugDir, fmt.Sprintf("openai_wire_%d_%d_body.json", ts, n))
		_ = os.MkdirAll(t.debugDir, 0755)
		_ = os.WriteFile(dumpPath, newBody, 0644)
	}

	// Clone request with new body
	newReq := req.Clone(req.Context())
	newReq.Body = io.NopCloser(bytes.NewReader(newBody))
	newReq.ContentLength = int64(len(newBody))
	resp, err := t.base.RoundTrip(newReq)
	return t.handleResponse(newReq, resp, err)
}

// handleResponse processes the HTTP response to extract usage extras (cost, cache tokens).
func (t *instrumentedTransport) handleResponse(req *http.Request, resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp == nil {
		return resp, err
	}
	rawPtr, _ := req.Context().Value(ctxCapturedUsage).(*RawUsage)
	if rawPtr == nil {
		slog.Debug("openai_handle_response: no captured usage pointer in context")
		return resp, nil
	}

	// For streaming responses, wrap body to capture usage extras from SSE events
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		slog.Debug("openai_handle_response: streaming response, wrapping body for usage capture")
		resp.Body = &usageCaptureBody{ReadCloser: resp.Body, raw: rawPtr}
		return resp, nil
	}

	// For non-streaming responses, read full body and extract usage extras
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}

	*rawPtr = extractRawUsage(body)
	slog.Debug("openai_handle_response: non-streaming, extracted usage",
		"prompt_tokens", rawPtr.promptTokens,
		"completion_tokens", rawPtr.completionTokens,
		"total_tokens", rawPtr.totalTokens,
		"cost", rawPtr.cost,
		"cache_hit", rawPtr.promptCacheHit,
		"cache_miss", rawPtr.promptCacheMiss)

	// Reconstruct response body
	newResp := *resp
	newResp.Body = io.NopCloser(bytes.NewReader(body))
	return &newResp, nil
}

// mergeExtra combines static extras (with placeholder resolution) and
// per-request extras. Per-request extras take precedence.
func mergeExtra(static, perReq map[string]any, sessionID string) map[string]any {
	result := make(map[string]any, len(static)+len(perReq))
	for k, v := range static {
		result[k] = resolvePlaceholder(v, sessionID)
	}
	for k, v := range perReq {
		result[k] = v
	}
	return result
}

// resolvePlaceholder replaces "$session_id" in string values with the
// actual session ID. Non-string values are returned unchanged.
func resolvePlaceholder(v any, sessionID string) any {
	if s, ok := v.(string); ok {
		return strings.ReplaceAll(s, "$session_id", sessionID)
	}
	return v
}

// ContextWithExtras returns a context annotated with per-request extras
// and session ID for the instrumentedTransport to pick up.
func ContextWithExtras(ctx context.Context, sessionID string, perReqExtra map[string]any) context.Context {
	if sessionID != "" {
		ctx = context.WithValue(ctx, ctxSessionID, sessionID)
	}
	if len(perReqExtra) > 0 {
		ctx = context.WithValue(ctx, ctxExtraPerReq, perReqExtra)
	}
	return ctx
}

// WrapTransport wraps an http.RoundTripper with body dump, extra-field
// injection, and response usage capture.
//
// When debugDir is non-empty, the request body is dumped to a JSON file
// in that directory — this works regardless of whether extras are configured.
//
// When extra is non-empty, body fields are merged with extra injection.
//
// Usage capture (cost, cache tokens from response) is always active via
// context — the transport checks context for captured usage pointers at
// runtime.
func WrapTransport(base http.RoundTripper, extra map[string]any, debugDir string) http.RoundTripper {
	return &instrumentedTransport{base: base, extra: extra, debugDir: debugDir}
}

func NewOpenAIAdapter(client *openai.Client, model string) *OpenAIAdapter {
	if model == "" {
		model = openai.GPT4oMini
	}
	return &OpenAIAdapter{Client: client, Model: model}
}

func toOpenAIMessages(messages []agent.Message) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, msg := range messages {
		// Skip assistant messages with empty content AND no tool calls.
		// OpenAI requires that every assistant message has either content
		// or tool_calls set — sending an empty message causes a 400 error.
		// This mirrors the filtering Anthropic's appendAssistantBlocks and
		// Gemini's appendGeminiModelParts already do.
		if msg.Role == agent.RoleAssistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		openAIMsg := openai.ChatCompletionMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		for _, toolCall := range msg.ToolCalls {
			openAIMsg.ToolCalls = append(openAIMsg.ToolCalls, openai.ToolCall{
				ID:   toolCall.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      toolCall.Name,
					Arguments: toolCall.Arguments,
				},
			})
		}
		if rc, ok := msg.ProviderMetadata["reasoning_content"].(string); ok {
			openAIMsg.ReasoningContent = rc
		}
		out = append(out, openAIMsg)
	}
	return out
}

func toOpenAITools(tools []agent.ToolSchema) []openai.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.Tool, 0, len(tools))
	for _, toolDef := range tools {
		params := json.RawMessage("{}")
		if toolDef.Parameters != nil {
			if b, err := json.Marshal(toolDef.Parameters); err == nil {
				params = b
			}
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        toolDef.Name,
				Description: toolDef.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

func (ad *OpenAIAdapter) buildRequest(msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) openai.ChatCompletionRequest {
	req := openai.ChatCompletionRequest{
		Model:    ad.Model,
		Messages: toOpenAIMessages(msgs),
		Tools:    toOpenAITools(tools),
	}
	if opts.Temperature != nil {
		req.Temperature = *opts.Temperature
	}
	if opts.MaxTokens != nil {
		req.MaxTokens = *opts.MaxTokens
	}
	return req
}

func (ad *OpenAIAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	// Inject extras into context for the transport to pick up.
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	// Set up usage extras capture via transport
	var raw RawUsage
	ctx = ContextWithCapturedUsage(ctx, &raw)

	req := ad.buildRequest(msgs, tools, opts)
	resp, err := retryOpenAI(ctx, ad.RetryConfig, req, func() (openai.ChatCompletionResponse, error) {
		return ad.Client.CreateChatCompletion(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	dumpDebugJSON(ad.LLMDebugDir, "openai", "resp", resp)
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: empty response")
	}
	choice := resp.Choices[0]

	// Determine cost: use provider's cost if available, otherwise calculate from pricing
	cost := raw.cost
	if cost == 0 && (ad.Pricing.Input != 0 || ad.Pricing.Output != 0) {
		cost = calculateCostFromPricing(ad.Pricing, raw)
		slog.Debug("openai_complete: calculated cost from pricing",
			"input", ad.Pricing.Input, "output", ad.Pricing.Output,
			"cache_hit_input", ad.Pricing.CacheHitInput,
			"prompt_tokens", raw.promptTokens,
			"completion_tokens", raw.completionTokens,
			"prompt_cache_hit", raw.promptCacheHit,
			"prompt_cache_miss", raw.promptCacheMiss,
			"cost", cost)
	} else {
		slog.Debug("openai_complete: using provider cost",
			"cost", cost, "pricing_configured", ad.Pricing.Input != 0 || ad.Pricing.Output != 0)
	}

	out := &agent.LLMResponse{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: choice.Message.Content,
		},
		FinishReason: string(choice.FinishReason),
		Usage: &agent.Usage{
			PromptTokens:          resp.Usage.PromptTokens,
			CompletionTokens:      resp.Usage.CompletionTokens,
			TotalTokens:           resp.Usage.TotalTokens,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  cost,
		},
	}
	for _, toolCall := range choice.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}
	if rc := choice.Message.ReasoningContent; len(rc) > 0 {
		if out.Message.ProviderMetadata == nil {
			out.Message.ProviderMetadata = make(map[string]any)
		}
		out.Message.ProviderMetadata["reasoning_content"] = rc
	}
	return out, nil
}

// retryOpenAI is the retry loop shared by both Complete and Stream paths.
// It calls fn, detects rate-limit errors (HTTP 429), applies exponential backoff
// using cfg, and respects context cancellation.
func retryOpenAI[T any](
	ctx context.Context,
	cfg agent.RetryConfig,
	req openai.ChatCompletionRequest,
	fn func() (T, error),
) (T, error) {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = openAIRetryAttempts
	}
	baseDelay := cfg.BaseDelay
	if baseDelay == 0 {
		baseDelay = openAIRetryBaseDelay
	}
	maxDelay := cfg.MaxDelay
	if maxDelay == 0 {
		maxDelay = 10000 * time.Millisecond
	}
	backoffFactor := cfg.BackoffFactor
	if backoffFactor == 0 {
		backoffFactor = 1.5
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isOpenAIRateLimitError(err) || attempt == maxAttempts-1 {
			var zero T
			return zero, err
		}
		if err := sleepOpenAIRetry(ctx, attempt, baseDelay, maxDelay, backoffFactor); err != nil {
			var zero T
			return zero, err
		}
	}
	var zero T
	return zero, lastErr
}

func isOpenAIRateLimitError(err error) bool {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusTooManyRequests {
		return true
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) && reqErr.HTTPStatusCode == http.StatusTooManyRequests {
		return true
	}
	return false
}

func sleepOpenAIRetry(ctx context.Context, attempt int, baseDelay, maxDelay time.Duration, backoffFactor float64) error {
	delay := min(time.Duration(float64(baseDelay)*math.Pow(backoffFactor, float64(attempt))), maxDelay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (ad *OpenAIAdapter) Stream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	// Inject extras into context for the transport to pick up.
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	// Set up usage extras capture via transport (captured from SSE events)
	var raw RawUsage
	ctx = ContextWithCapturedUsage(ctx, &raw)

	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := retryOpenAI(ctx, ad.RetryConfig, req, func() (*openai.ChatCompletionStream, error) {
		return ad.Client.CreateChatCompletionStream(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	resultCh := make(chan agent.StreamResult, 16)

	go func() {
		defer close(resultCh)
		defer stream.Close()

		accum := newIndexAccumulator()
		var pendingUsage *agent.Usage
		var finishReason string

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				// Stream complete — determine cost: provider's cost or pricing fallback
				cost := raw.cost
				if cost == 0 && (ad.Pricing.Input != 0 || ad.Pricing.Output != 0) {
					cost = calculateCostFromPricing(ad.Pricing, raw)
					slog.Debug("openai_stream_eof: calculated cost from pricing",
						"input", ad.Pricing.Input, "output", ad.Pricing.Output,
						"cache_hit_input", ad.Pricing.CacheHitInput,
						"prompt_tokens", raw.promptTokens,
						"completion_tokens", raw.completionTokens,
						"cost", cost)
				} else {
					slog.Debug("openai_stream_eof: using provider cost",
						"cost", cost,
						"pricing_configured", ad.Pricing.Input != 0 || ad.Pricing.Output != 0)
				}
				if pendingUsage != nil {
					if cost > 0 {
						pendingUsage.Cost = cost
					}
					pendingUsage.PromptCacheHitTokens = raw.promptCacheHit
					pendingUsage.PromptCacheMissTokens = raw.promptCacheMiss
				}
				sendStreamFinal(resultCh, accum.flush(), pendingUsage, finishReason)
				return
			}
			if err != nil {
				resultCh <- agent.StreamResult{Err: err}
				return
			}

			dumpDebugJSON(ad.LLMDebugDir, "openai", "stream_resp", resp)

			// Capture usage if present. With stream_options: {"include_usage": true},
			// the usage arrives as a separate SSE event AFTER the finish_reason chunk
			// (no choices in this event), so we must capture it outside the choices loop.
			if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
				cost := raw.cost
				if cost == 0 && (ad.Pricing.Input != 0 || ad.Pricing.Output != 0) {
					cost = calculateCostFromPricing(ad.Pricing, raw)
					slog.Debug("openai_stream_usage: calculated cost from pricing",
						"input", ad.Pricing.Input, "output", ad.Pricing.Output,
						"cache_hit_input", ad.Pricing.CacheHitInput,
						"prompt_tokens", raw.promptTokens,
						"completion_tokens", raw.completionTokens,
						"prompt_cache_hit", raw.promptCacheHit,
						"prompt_cache_miss", raw.promptCacheMiss,
						"cost", cost)
				} else {
					slog.Debug("openai_stream_usage: using provider cost",
						"cost", cost,
						"pricing_configured", ad.Pricing.Input != 0 || ad.Pricing.Output != 0)
				}
				pendingUsage = &agent.Usage{
					PromptTokens:          resp.Usage.PromptTokens,
					CompletionTokens:      resp.Usage.CompletionTokens,
					TotalTokens:           resp.Usage.TotalTokens,
					PromptCacheHitTokens:  raw.promptCacheHit,
					PromptCacheMissTokens: raw.promptCacheMiss,
					Cost:                  cost,
				}
			}

			for _, choice := range resp.Choices {
				// Merge tool call deltas by index using the shared accumulator.
				for _, tc := range choice.Delta.ToolCalls {
					idx := 0
					if tc.Index != nil {
						idx = *tc.Index
					}
					accum.add(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
				}

				// Emit text delta if present
				if delta := choice.Delta.Content; delta != "" {
					select {
					case <-ctx.Done():
						resultCh <- agent.StreamResult{Err: ctx.Err()}
						return
					case resultCh <- agent.StreamResult{Delta: delta}:
					}
				}

				// Capture finish_reason from the last delta chunk. The choice
				// may carry a non-empty finish_reason in the final chunk before
				// the usage-only SSE event.
				if string(choice.FinishReason) != "" {
					finishReason = string(choice.FinishReason)
				}

				// Do NOT return on finish_reason — the usage SSE event comes AFTER
				// the finish_reason chunk when IncludeUsage is set. Continue reading
				// until io.EOF to capture usage, then finalize.
			}
		}
	}()

	return resultCh, nil
}

// compile-time check
var _ agent.LLMAdapter = (*OpenAIAdapter)(nil)
