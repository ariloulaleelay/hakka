package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Extra map[string]any
}

// ---------------------------------------------------------------------------
// Extra body injection via a custom RoundTripper
//
// The extraRoundTripper wraps the HTTP transport used by the go-openai client.
// It intercepts every outgoing request, reads the JSON body, merges extra
// fields (static + per-request), resolves placeholders, and forwards.
// It also intercepts the response to extract usage.cost when available.
// ---------------------------------------------------------------------------

type ctxKey string

const (
	ctxExtraPerReq  ctxKey = "extra_per_req"
	ctxSessionID    ctxKey = "session_id"
	ctxCostCapture  ctxKey = "cost_capture" // *float64 to receive extracted cost
)

// ContextWithCostCapture stores a *float64 pointer that the transport will
// populate with the cost extracted from the provider response's usage.cost field.
func ContextWithCostCapture(ctx context.Context, ptr *float64) context.Context {
	return context.WithValue(ctx, ctxCostCapture, ptr)
}

// extraRoundTripper wraps an http.RoundTripper and injects extra fields
// into the JSON request body before forwarding. It also captures cost
// from response bodies.
type extraRoundTripper struct {
	base       http.RoundTripper
	extra      map[string]any // static extras from config (may contain placeholders)
	debugDir   string         // when non-empty, dump final wire body here
}

// costCaptureBody wraps an io.ReadCloser (SSE response body for streaming)
// and scans for "usage" objects containing a "cost" field, storing the
// last seen cost value into the provided pointer.
type costCaptureBody struct {
	io.ReadCloser
	cost  *float64
	remainder []byte
}

func (b *costCaptureBody) Read(p []byte) (int, error) {
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
				if c := extractCostFromUsage([]byte(payload)); c > 0 {
					*b.cost = c
				}
			}
		}
	}
	return n, err
}

// RoundTrip intercepts the request, merges extras into the body, and forwards.
// For non-streaming responses, it reads the full body to extract cost.
// For streaming responses, it wraps the body to capture cost from SSE events.
func (t *extraRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	perReqExtra, _ := req.Context().Value(ctxExtraPerReq).(map[string]any)
	sessionID, _ := req.Context().Value(ctxSessionID).(string)

	merged := mergeExtra(t.extra, perReqExtra, sessionID)
	if len(merged) == 0 {
		resp, err := t.base.RoundTrip(req)
		return t.handleResponse(req, resp, err)
	}

	// Read the existing JSON body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()

	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return nil, err
	}

	// Merge extra fields
	for k, v := range merged {
		bodyMap[k] = v
	}

	newBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
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

// handleResponse processes the HTTP response to extract cost if needed.
func (t *extraRoundTripper) handleResponse(req *http.Request, resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp == nil {
		return resp, err
	}
	costPtr, _ := req.Context().Value(ctxCostCapture).(*float64)
	if costPtr == nil {
		return resp, nil
	}

	// For streaming responses, wrap body to capture cost from SSE events
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = &costCaptureBody{ReadCloser: resp.Body, cost: costPtr}
		return resp, nil
	}

	// For non-streaming responses, read full body and extract cost
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}

	*costPtr = extractCostFromUsage(body)

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
// and session ID for the extraRoundTripper to pick up.
func ContextWithExtras(ctx context.Context, sessionID string, perReqExtra map[string]any) context.Context {
	if sessionID != "" {
		ctx = context.WithValue(ctx, ctxSessionID, sessionID)
	}
	if len(perReqExtra) > 0 {
		ctx = context.WithValue(ctx, ctxExtraPerReq, perReqExtra)
	}
	return ctx
}

// WrapTransport wraps an http.RoundTripper with extra-body injection.
// If extras is empty, the base transport is returned unchanged.
// debugDir, when non-empty, enables dumping the final wire body after
// extra injection.
func WrapTransport(base http.RoundTripper, extra map[string]any, debugDir string) http.RoundTripper {
	if len(extra) == 0 {
		return base
	}
	return &extraRoundTripper{base: base, extra: extra, debugDir: debugDir}
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

	// Set up cost capture via transport
	var cost float64
	ctx = ContextWithCostCapture(ctx, &cost)

	req := ad.buildRequest(msgs, tools, opts)
	resp, err := retryOpenAI(ctx, req, func() (openai.ChatCompletionResponse, error) {
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
	out := &agent.LLMResponse{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: choice.Message.Content,
		},
		FinishReason: string(choice.FinishReason),
		Usage: &agent.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			Cost:             cost,
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

// retryOpenAI is the single retry loop shared by both Complete and Stream
// paths. It abstracts the common logic: dump the request for debugging,
// call the provided function, detect rate-limit errors (HTTP 429),
// apply exponential backoff, and respect context cancellation.
//
// The fn closure captures only the API call itself — ctx and req are
// passed explicitly so all retry logic lives in one place.
func retryOpenAI[T any](
	ctx context.Context,
	req openai.ChatCompletionRequest,
	fn func() (T, error),
) (T, error) {
	var lastErr error
	for attempt := 0; attempt < openAIRetryAttempts; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isOpenAIRateLimitError(err) || attempt == openAIRetryAttempts-1 {
			var zero T
			return zero, err
		}
		if err := sleepOpenAIRetry(ctx, attempt); err != nil {
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

func sleepOpenAIRetry(ctx context.Context, attempt int) error {
	delay := min(openAIRetryBaseDelay<<attempt, 10000*time.Millisecond)
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

	// Set up cost capture via transport (captured from SSE events)
	var cost float64
	ctx = ContextWithCostCapture(ctx, &cost)

	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := retryOpenAI(ctx, req, func() (*openai.ChatCompletionStream, error) {
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

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				// Stream complete — attach captured cost before finalizing
				if pendingUsage != nil && cost > 0 {
					pendingUsage.Cost = cost
				}
				sendStreamFinal(resultCh, accum.flush(), pendingUsage)
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
				pendingUsage = &agent.Usage{
					PromptTokens:     resp.Usage.PromptTokens,
					CompletionTokens: resp.Usage.CompletionTokens,
					TotalTokens:      resp.Usage.TotalTokens,
					Cost:             cost,
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
