package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// DeepSeekAdapter is a dedicated adapter for DeepSeek's OpenAI-compatible
// Chat Completions API (POST https://api.deepseek.com/chat/completions).
//
// It is implemented with a raw HTTP client (no go-openai SDK dependency)
// and differs from the generic OpenAI adapter in two ways:
//
//  1. reasoning_content (chain-of-thought) is captured in both non-stream
//     and stream responses and surfaced via Message.ProviderMetadata under
//     the "reasoning_content" key.
//
//  2. Assistant turns that produced tool calls MUST send reasoning_content
//     back to the API — DeepSeek returns a 400 error otherwise. When such a
//     turn was produced by another provider (e.g. switching mid-conversation
//     from Claude, which stores no reasoning_content), an empty string is
//     sent as a synthetic backup. Plain assistant turns (no tool calls)
//     omit the field entirely — DeepSeek ignores it there, so we save
//     context by not sending it.
type DeepSeekAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
	Model      string
	Config     DeepSeekConfig
}

func NewDeepSeekAdapter(client *http.Client, baseURL, model string, cfg DeepSeekConfig) *DeepSeekAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	if model == "" {
		model = "deepseek-chat"
	}
	return &DeepSeekAdapter{
		HTTPClient: client,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		Config:     cfg,
	}
}

// ===== Protocol types =====

type deepSeekMessage struct {
	Role             string             `json:"role"`
	Content          string             `json:"content,omitempty"`
	ReasoningContent *string            `json:"reasoning_content,omitempty"`
	Name             string             `json:"name,omitempty"`
	ToolCallID       string             `json:"tool_call_id,omitempty"`
	ToolCalls        []deepSeekToolCall `json:"tool_calls,omitempty"`
}

type deepSeekToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function deepSeekFnCall `json:"function"`
}

type deepSeekFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type deepSeekTool struct {
	Type     string         `json:"type"`
	Function deepSeekFnDecl `json:"function"`
}

type deepSeekFnDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type deepSeekStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type deepSeekRequest struct {
	Model         string                 `json:"model"`
	Messages      []deepSeekMessage      `json:"messages"`
	Temperature   *float32               `json:"temperature,omitempty"`
	MaxTokens     *int                   `json:"max_tokens,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	StreamOptions *deepSeekStreamOptions `json:"stream_options,omitempty"`
	Tools         []deepSeekTool         `json:"tools,omitempty"`
}

type deepSeekResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role             string             `json:"role"`
			Content          string             `json:"content"`
			ReasoningContent string             `json:"reasoning_content"`
			ToolCalls        []deepSeekToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type deepSeekStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ===== Encode (hakka → DeepSeek) =====

func toDeepSeekMessages(messages []agent.Message) []deepSeekMessage {
	out := make([]deepSeekMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == agent.RoleAssistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		dsMsg := deepSeekMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		if msg.Role == agent.RoleAssistant && len(msg.ToolCalls) > 0 {
			// DeepSeek requires reasoning_content on assistant turns that made
			// tool calls. Fall back to an empty string when the turn came from
			// another provider (no stored reasoning_content) — the field must
			// still be present to avoid a 400.
			rc := ""
			if v, ok := msg.ProviderMetadata["reasoning_content"].(string); ok {
				rc = v
			}
			dsMsg.ReasoningContent = &rc
		}
		for _, tc := range msg.ToolCalls {
			dsMsg.ToolCalls = append(dsMsg.ToolCalls, deepSeekToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: deepSeekFnCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out = append(out, dsMsg)
	}
	return out
}

func toDeepSeekTools(tools []agent.ToolSchema) []deepSeekTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]deepSeekTool, 0, len(tools))
	for _, td := range tools {
		params := json.RawMessage("{}")
		if td.Parameters != nil {
			if b, err := json.Marshal(td.Parameters); err == nil {
				params = b
			}
		}
		out = append(out, deepSeekTool{
			Type: "function",
			Function: deepSeekFnDecl{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

func (ad *DeepSeekAdapter) buildRequest(msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) deepSeekRequest {
	req := deepSeekRequest{
		Model:    ad.Model,
		Messages: toDeepSeekMessages(msgs),
		Tools:    toDeepSeekTools(tools),
	}
	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}
	if opts.MaxTokens != nil {
		req.MaxTokens = opts.MaxTokens
	}
	return req
}

// ===== Decode (DeepSeek → hakka) =====

func (ad *DeepSeekAdapter) parseResponse(rawBody []byte) (*agent.LLMResponse, error) {
	raw := extractRawUsage(rawBody)

	var resp deepSeekResponse
	if err := json.Unmarshal(rawBody, &resp); err != nil {
		return nil, fmt.Errorf("deepseek: decode response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("deepseek: empty response")
	}
	choice := resp.Choices[0]

	out := &agent.LLMResponse{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: choice.Message.Content,
		},
		FinishReason: choice.FinishReason,
		Usage: &agent.Usage{
			PromptTokens:          resp.Usage.PromptTokens,
			CompletionTokens:      resp.Usage.CompletionTokens,
			TotalTokens:           resp.Usage.TotalTokens,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  ad.Config.ResolveCost(raw),
		},
	}
	if rc := choice.Message.ReasoningContent; rc != "" {
		if out.Message.ProviderMetadata == nil {
			out.Message.ProviderMetadata = make(map[string]any)
		}
		out.Message.ProviderMetadata["reasoning_content"] = rc
	}
	for _, tc := range choice.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

// ===== LLMAdapter =====

func (ad *DeepSeekAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if onDelta != nil {
		return ad.completeStream(ctx, msgs, tools, opts, onDelta)
	}
	return ad.completeNonStream(ctx, msgs, tools, opts)
}

func (ad *DeepSeekAdapter) completeNonStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	var rawBody json.RawMessage
	err := retryOnRateLimit(ctx, ad.Config.RetryPolicy(), func() error {
		return doJSONPost(ctx, ad.HTTPClient, ad.endpoint(),
			ad.buildRequest(msgs, tools, opts), &rawBody,
			"deepseek", nil, ad.Config.DebugDir())
	})
	if err != nil {
		return nil, err
	}
	return ad.parseResponse(rawBody)
}

func (ad *DeepSeekAdapter) completeStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if opts.SessionID != "" || len(opts.Extra) > 0 {
		ctx = ContextWithExtras(ctx, opts.SessionID, opts.Extra)
	}

	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true
	req.StreamOptions = &deepSeekStreamOptions{IncludeUsage: true}

	body, err := ad.openStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	accum := newIndexAccumulator()
	var textBuf, reasoningBuf strings.Builder
	var pendingUsage *agent.Usage
	var finishReason string
	var raw RawUsage

	scanErr := scanSSE(body, func(payload string) error {
		var chunk deepSeekStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Tolerate non-JSON payloads (e.g. keepalive comments).
			return nil
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
			raw = extractRawUsage([]byte(payload))
			pendingUsage = &agent.Usage{
				PromptTokens:          chunk.Usage.PromptTokens,
				CompletionTokens:      chunk.Usage.CompletionTokens,
				TotalTokens:           chunk.Usage.TotalTokens,
				PromptCacheHitTokens:  raw.promptCacheHit,
				PromptCacheMissTokens: raw.promptCacheMiss,
				Cost:                  ad.Config.ResolveCost(raw),
			}
		}
		for _, choice := range chunk.Choices {
			if fr := choice.FinishReason; fr != "" {
				finishReason = fr
			}
			delta := choice.Delta
			if delta.Content != "" {
				textBuf.WriteString(delta.Content)
				onDelta(delta.Content)
			}
			if delta.ReasoningContent != "" {
				reasoningBuf.WriteString(delta.ReasoningContent)
			}
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				accum.add(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
		}
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}

	out := &agent.LLMResponse{
		Message: agent.Message{
			Role:      agent.RoleAssistant,
			Content:   textBuf.String(),
			ToolCalls: accum.flush(),
		},
		FinishReason: finishReason,
		Usage:        pendingUsage,
	}
	if reasoningBuf.Len() > 0 {
		out.Message.ProviderMetadata = map[string]any{"reasoning_content": reasoningBuf.String()}
	}
	return out, nil
}

func (ad *DeepSeekAdapter) openStream(ctx context.Context, req deepSeekRequest) (io.ReadCloser, error) {
	var body io.ReadCloser
	err := retryOnRateLimit(ctx, ad.Config.RetryPolicy(), func() error {
		var err error
		body, err = doStreamPost(ctx, ad.HTTPClient, ad.endpoint(), req, "deepseek", nil, ad.Config.DebugDir())
		return err
	})
	return body, err
}

func (ad *DeepSeekAdapter) endpoint() string {
	return ad.BaseURL + "/chat/completions"
}

var _ agent.LLMAdapter = (*DeepSeekAdapter)(nil)
