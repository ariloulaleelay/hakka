package adapters

import (
	"context"
	"errors"
	"io"
	"net/http"
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

func (ad *OpenAIAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	req := ad.buildRequest(msgs, tools, opts)
	resp, err := retryOpenAI(ctx, req, func() (openai.ChatCompletionResponse, error) {
		dumpDebugJSON(ad.LLMDebugDir, "openai", "req", req)
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
	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := retryOpenAI(ctx, req, func() (*openai.ChatCompletionStream, error) {
		dumpDebugJSON(ad.LLMDebugDir, "openai", "req", req)
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
				// Stream complete — flush accumulated tool calls if any.
				sendStreamFinal(resultCh, accum.flush(), pendingUsage)
				return
			}
			if err != nil {
				resultCh <- agent.StreamResult{Err: err}
				return
			}

			dumpDebugJSON(ad.LLMDebugDir, "openai", "stream_resp", resp)

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
					sendStreamFinal(resultCh, accum.flush(), pendingUsage)
					return
				}
			}
		}
	}()

	return resultCh, nil
}

// compile-time check
var _ agent.LLMAdapter = (*OpenAIAdapter)(nil)
