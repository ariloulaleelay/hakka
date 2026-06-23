package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

var reqCounter atomic.Int64

const (
	openAIRetryAttempts  = 20
	openAIRetryBaseDelay = 250 * time.Millisecond
)

// OpenAIAdapter implements agent.LLMAdapter against the OpenAI chat API.
type OpenAIAdapter struct {
	Client      *openai.Client
	Model       string
	LLMDebugDir string // when non-empty, request/response payloads are logged here
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
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        toolDef.Name,
				Description: toolDef.Description,
				Parameters:  toolDef.Parameters,
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

func (ad *OpenAIAdapter) dumpRequest(req openai.ChatCompletionRequest) {
	if ad.LLMDebugDir == "" {
		return
	}
	n := reqCounter.Add(1)
	ts := time.Now().UnixNano()
	if err := os.MkdirAll(ad.LLMDebugDir, 0755); err != nil {
		return
	}
	path := filepath.Join(ad.LLMDebugDir, fmt.Sprintf("openai_%d_%d_req.json", ts, n))
	raw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0644)
}

func (ad *OpenAIAdapter) dumpResponse(resp openai.ChatCompletionResponse) {
	if ad.LLMDebugDir == "" {
		return
	}
	n := reqCounter.Add(1)
	ts := time.Now().UnixNano()
	if err := os.MkdirAll(ad.LLMDebugDir, 0755); err != nil {
		return
	}
	path := filepath.Join(ad.LLMDebugDir, fmt.Sprintf("openai_%d_%d_resp.json", ts, n))
	raw, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0644)
}

func (ad *OpenAIAdapter) dumpStreamResponse(resp openai.ChatCompletionStreamResponse) {
	if ad.LLMDebugDir == "" {
		return
	}
	n := reqCounter.Add(1)
	ts := time.Now().UnixNano()
	if err := os.MkdirAll(ad.LLMDebugDir, 0755); err != nil {
		return
	}
	path := filepath.Join(ad.LLMDebugDir, fmt.Sprintf("openai_%d_%d_stream_resp.json", ts, n))
	raw, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0644)
}

func (ad *OpenAIAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	req := ad.buildRequest(msgs, tools, opts)
	resp, err := retryOpenAI(ctx, req, ad.dumpRequest, func() (openai.ChatCompletionResponse, error) {
		return ad.Client.CreateChatCompletion(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	ad.dumpResponse(resp)
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
	dump func(openai.ChatCompletionRequest),
	fn func() (T, error),
) (T, error) {
	var lastErr error
	for attempt := 0; attempt < openAIRetryAttempts; attempt++ {
		dump(req)
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

// toolCallAccumulator merges streaming delta tool calls by Index.
// OpenAI streams tool calls incrementally — each chunk carries partial
// data for a specific index. We must merge across chunks, not append.
type toolCallAccumulator struct {
	ID        string
	Name      string
	Arguments string
}

// flushToolCalls builds the final agent.ToolCall slice from accumulators
// in index order. Empty Arguments are replaced with "{}".
func flushToolCalls(accum map[int]*toolCallAccumulator) []agent.ToolCall {
	if len(accum) == 0 {
		return nil
	}
	indices := make([]int, 0, len(accum))
	for idx := range accum {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	out := make([]agent.ToolCall, 0, len(indices))
	for _, idx := range indices {
		acc := accum[idx]
		args := acc.Arguments
		if args == "" {
			args = "{}"
		}
		out = append(out, agent.ToolCall{
			ID:        acc.ID,
			Name:      acc.Name,
			Arguments: args,
		})
	}
	return out
}

func (ad *OpenAIAdapter) Stream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := retryOpenAI(ctx, req, ad.dumpRequest, func() (*openai.ChatCompletionStream, error) {
		return ad.Client.CreateChatCompletionStream(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	resultCh := make(chan agent.StreamResult, 16)

	go func() {
		defer close(resultCh)
		defer stream.Close()

		// Accumulate tool calls across deltas. OpenAI streams tool calls
		// as incremental deltas, each chunk carries partial data for a
		// specific tool call Index. We merge by index rather than appending.
		accum := make(map[int]*toolCallAccumulator)
		var pendingUsage *agent.Usage

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				// Stream complete — flush accumulated tool calls if any.
				if toolCalls := flushToolCalls(accum); len(toolCalls) > 0 {
					resultCh <- agent.StreamResult{
						ToolCalls: toolCalls,
						Usage:     pendingUsage,
					}
				} else {
					resultCh <- agent.StreamResult{Done: true, Usage: pendingUsage}
				}
				return
			}
			if err != nil {
				resultCh <- agent.StreamResult{Err: err}
				return
			}

			for _, choice := range resp.Choices {
				// Merge tool call deltas by index.
				for _, tc := range choice.Delta.ToolCalls {
					idx := 0
					if tc.Index != nil {
						idx = *tc.Index
					}
					acc, exists := accum[idx]
					if !exists {
						acc = &toolCallAccumulator{}
						accum[idx] = acc
					}
					if tc.ID != "" {
						acc.ID = tc.ID
					}
					if tc.Function.Name != "" {
						acc.Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						acc.Arguments += tc.Function.Arguments
					}
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

				// Capture usage if present
				if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
					pendingUsage = &agent.Usage{
						PromptTokens:     resp.Usage.PromptTokens,
						CompletionTokens: resp.Usage.CompletionTokens,
						TotalTokens:      resp.Usage.TotalTokens,
					}
				}

				// Finish reason signals end of this assistant turn
				if choice.FinishReason != "" {
					if toolCalls := flushToolCalls(accum); len(toolCalls) > 0 {
						resultCh <- agent.StreamResult{
							ToolCalls: toolCalls,
							Usage:     pendingUsage,
						}
					} else {
						resultCh <- agent.StreamResult{Done: true, Usage: pendingUsage}
					}
					return
				}
			}
		}
	}()

	return resultCh, nil
}

// compile-time check
var _ agent.LLMAdapter = (*OpenAIAdapter)(nil)
