package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

type OpenAIAdapter struct {
	Client *openai.Client
	Model  string
	Config OpenAIConfig
}

func NewOpenAIAdapter(client *openai.Client, model string, cfg OpenAIConfig) *OpenAIAdapter {
	if model == "" {
		model = openai.GPT4oMini
	}
	return &OpenAIAdapter{Client: client, Model: model, Config: cfg}
}

// ===== Encode (hakka → OpenAI) =====

func toOpenAIMessages(messages []agent.Message) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == agent.RoleAssistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		openAIMsg := openai.ChatCompletionMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		for _, tc := range msg.ToolCalls {
			openAIMsg.ToolCalls = append(openAIMsg.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
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
	for _, td := range tools {
		params := json.RawMessage("{}")
		if td.Parameters != nil {
			if b, err := json.Marshal(td.Parameters); err == nil {
				params = b
			}
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        td.Name,
				Description: td.Description,
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

// ===== LLMAdapter =====

func (ad *OpenAIAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if onDelta != nil {
		return ad.completeStream(ctx, msgs, tools, opts, onDelta)
	}
	return ad.completeNonStream(ctx, msgs, tools, opts)
}

func (ad *OpenAIAdapter) completeNonStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	var raw RawUsage
	ctx = ContextWithCapturedUsage(ctx, &raw)

	req := ad.buildRequest(msgs, tools, opts)
	resp, err := retryOpenAI(ctx, ad.Config.RetryPolicy(), req, func() (openai.ChatCompletionResponse, error) {
		return ad.Client.CreateChatCompletion(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	dumpDebugJSON(ad.Config.DebugDir(), "openai", "resp", resp)
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: empty response")
	}
	choice := resp.Choices[0]

	cost := ad.Config.ResolveCost(raw)

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
	for _, tc := range choice.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
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

func (ad *OpenAIAdapter) completeStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	var raw RawUsage
	ctx = ContextWithCapturedUsage(ctx, &raw)

	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := retryOpenAI(ctx, ad.Config.RetryPolicy(), req, func() (*openai.ChatCompletionStream, error) {
		return ad.Client.CreateChatCompletionStream(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	accum := newIndexAccumulator()
	var pendingUsage *agent.Usage
	var finishReason string
	var textBuf strings.Builder

	for {
		resp, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			cost := ad.Config.ResolveCost(raw)
			if pendingUsage != nil {
				if cost > 0 {
					pendingUsage.Cost = cost
				}
				pendingUsage.PromptCacheHitTokens = raw.promptCacheHit
				pendingUsage.PromptCacheMissTokens = raw.promptCacheMiss
			}
			return &agent.LLMResponse{
				Message: agent.Message{
					Role:      agent.RoleAssistant,
					Content:   textBuf.String(),
					ToolCalls: accum.flush(),
				},
				FinishReason: finishReason,
				Usage:        pendingUsage,
			}, nil
		}
		if recvErr != nil {
			return nil, recvErr
		}

		dumpDebugJSON(ad.Config.DebugDir(), "openai", "stream_resp", resp)

		if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
			cost := ad.Config.ResolveCost(raw)
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
			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				accum.add(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
			if delta := choice.Delta.Content; delta != "" {
				textBuf.WriteString(delta)
				onDelta(delta)
			}
			if fr := string(choice.FinishReason); fr != "" {
				finishReason = fr
			}
		}
	}
}

// ===== Retry =====

func retryOpenAI[T any](
	ctx context.Context,
	cfg agent.RetryConfig,
	req openai.ChatCompletionRequest,
	fn func() (T, error),
) (T, error) {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 20
	}
	baseDelay := cfg.BaseDelay
	if baseDelay == 0 {
		baseDelay = 250 * time.Millisecond
	}
	maxDelay := cfg.MaxDelay
	if maxDelay == 0 {
		maxDelay = 10 * time.Second
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
		delay := min(time.Duration(float64(baseDelay)*math.Pow(backoffFactor, float64(attempt))), maxDelay)
		if sleepErr := sleepOpenAIRetry(ctx, delay); sleepErr != nil {
			var zero T
			return zero, sleepErr
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

func sleepOpenAIRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
