package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// ============================================================================
// OpenAI Responses adapter (POST /v1/responses)
//
// This adapter implements the OpenAI Responses API, an evolution of Chat
// Completions. It operates in a **stateless** mode: every Complete() call
// sends the full conversation history as input items. No previous_response_id
// chaining is used; hakka manages all conversation state.
//
// Key differences from the Chat Completions adapter:
//   - Endpoint: /v1/responses instead of /v1/chat/completions
//   - Input:  "input" array of typed Items instead of "messages"
//   - Output: "output" array of typed Items instead of "choices"
//   - Tools:  flat {type, name, description, parameters} (no nested "function")
//   - System prompts: top-level "instructions" field
//   - State:   store:false (stateless)
//   - Streaming: SSE with event: lines (not just data:)
// ============================================================================

// OpenAIResponsesConfig holds configuration for the OpenAI Responses adapter.
type OpenAIResponsesConfig struct {
	debugDir string
	retryCfg agent.RetryConfig
	pricing  agent.Pricing
	Quota    *agent.QuotaConfig
}

func NewOpenAIResponsesConfig(debugDir string, retryCfg agent.RetryConfig, pricing agent.Pricing) OpenAIResponsesConfig {
	return OpenAIResponsesConfig{debugDir: debugDir, retryCfg: retryCfg, pricing: pricing}
}

func (c OpenAIResponsesConfig) DebugDir() string               { return c.debugDir }
func (c OpenAIResponsesConfig) RetryPolicy() agent.RetryConfig { return c.retryCfg }
func (c OpenAIResponsesConfig) ResolveCost(raw RawUsage) float64 {
	if raw.cost > 0 {
		return raw.cost
	}
	if c.pricing.Input != 0 || c.pricing.Output != 0 {
		return calculateCostFromPricing(c.pricing, raw)
	}
	return 0
}

// OpenAIResponsesAdapter implements agent.LLMAdapter for the Responses API.
type OpenAIResponsesAdapter struct {
	Client  *http.Client
	BaseURL string
	Model   string
	Config  OpenAIResponsesConfig
}

// NewOpenAIResponsesAdapter creates a new Responses API adapter.
func NewOpenAIResponsesAdapter(client *http.Client, baseURL string, model string, cfg OpenAIResponsesConfig) *OpenAIResponsesAdapter {
	return &OpenAIResponsesAdapter{
		Client:  client,
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Model:   model,
		Config:  cfg,
	}
}

// ============================================================================
// LLMAdapter
// ============================================================================

func (ad *OpenAIResponsesAdapter) Complete(
	ctx context.Context,
	msgs []agent.Message,
	tools []agent.ToolSchema,
	opts agent.CompleteOptions,
	onDelta func(string),
) (*agent.LLMResponse, error) {
	if onDelta != nil {
		return ad.completeStream(ctx, msgs, tools, opts, onDelta)
	}
	return ad.completeNonStream(ctx, msgs, tools, opts)
}

// ============================================================================
// Non-streaming
// ============================================================================

func (ad *OpenAIResponsesAdapter) completeNonStream(
	ctx context.Context,
	msgs []agent.Message,
	tools []agent.ToolSchema,
	opts agent.CompleteOptions,
) (*agent.LLMResponse, error) {
	req := ad.buildRequest(msgs, tools, opts)
	req["stream"] = false

	respBody, err := doJSONPostWithRetry(ctx, ad.Client, ad.BaseURL+"/responses", req, ad.Config.retryCfg, "openai-responses", nil, ad.Config.debugDir)
	if err != nil {
		return nil, err
	}

	return ad.parseResponse(respBody)
}

// ============================================================================
// Streaming
// ============================================================================

func (ad *OpenAIResponsesAdapter) completeStream(
	ctx context.Context,
	msgs []agent.Message,
	tools []agent.ToolSchema,
	opts agent.CompleteOptions,
	onDelta func(string),
) (*agent.LLMResponse, error) {
	req := ad.buildRequest(msgs, tools, opts)
	req["stream"] = true

	body, err := doStreamPostWithRetry(ctx, ad.Client, ad.BaseURL+"/responses", req, ad.Config.retryCfg, "openai-responses", nil, ad.Config.debugDir)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	return ad.parseStream(body, onDelta)
}

// ============================================================================
// Request building
// ============================================================================

func (ad *OpenAIResponsesAdapter) buildRequest(msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) map[string]any {
	req := map[string]any{
		"store": false,
	}
	req["model"] = ad.Model

	// Extract system/developer messages → instructions
	instructions := toResponsesInstructions(msgs)
	if instructions != "" {
		req["instructions"] = instructions
	}

	// Convert messages → input items (with tool call expansion)
	req["input"] = toResponsesInput(msgs)

	// Convert tools
	if len(tools) > 0 {
		req["tools"] = toResponsesTools(tools)
	}

	if opts.Temperature != nil {
		req["temperature"] = *opts.Temperature
	}
	if opts.MaxTokens != nil {
		req["max_output_tokens"] = *opts.MaxTokens
	}

	return req
}

// toResponsesInstructions extracts system/developer messages from the message
// list and returns a concatenated instructions string.
func toResponsesInstructions(msgs []agent.Message) string {
	var parts []string
	for _, m := range msgs {
		if m.Role == agent.RoleSystem || m.Role == "developer" {
			if m.Content != "" {
				parts = append(parts, m.Content)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// toResponsesInput converts hakka messages to the Responses API "input" array.
// System/developer messages are skipped (they go into "instructions").
// Assistant tool_calls are expanded into separate function_call items.
func toResponsesInput(msgs []agent.Message) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		if m.Role == agent.RoleSystem || m.Role == "developer" {
			continue
		}
		switch m.Role {
		case agent.RoleUser:
			out = append(out, map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": m.Content},
				},
			})
		case agent.RoleAssistant:
			// Expand tool calls as separate items
			for _, tc := range m.ToolCalls {
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": tc.Arguments,
				})
			}
			// Text content as message item
			if m.Content != "" {
				out = append(out, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": m.Content},
					},
				})
			}
		case agent.RoleTool:
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
		}
	}
	return out
}

// toResponsesTools converts hakka ToolSchema to Responses API tool definitions.
// In Responses, tools are flat: {type, name, description, parameters}
// (no nested "function" key like Chat Completions).
func toResponsesTools(tools []agent.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, td := range tools {
		tool := map[string]any{
			"type": "function",
			"name": td.Name,
		}
		if td.Description != "" {
			tool["description"] = td.Description
		}
		if td.Parameters != nil {
			tool["parameters"] = td.Parameters
		}
		out = append(out, tool)
	}
	return out
}

// ============================================================================
// Response parsing (non-streaming)
// ============================================================================

type responsesAPIResponse struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Output []responsesItem `json:"output"`
	Usage  *responsesUsage `json:"usage"`
	Error  *responsesError `json:"error"`
}

type responsesItem struct {
	Type      string             `json:"type"`
	ID        string             `json:"id"`
	Role      string             `json:"role"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Content   []responsesContent `json:"content"`
	Summary   []responsesSummary `json:"summary"`
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens         int                     `json:"input_tokens"`
	OutputTokens        int                     `json:"output_tokens"`
	TotalTokens         int                     `json:"total_tokens"`
	InputTokensDetails  *responsesTokensDetails `json:"input_tokens_details"`
	OutputTokensDetails *responsesTokensDetails `json:"output_tokens_details"`
}

type responsesTokensDetails struct {
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (ad *OpenAIResponsesAdapter) parseResponse(body []byte) (*agent.LLMResponse, error) {
	var apiResp responsesAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("openai-responses: parse error: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai-responses: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return ad.itemsToResponse(apiResp.Output, apiResp.Usage), nil
}

func (ad *OpenAIResponsesAdapter) itemsToResponse(items []responsesItem, usage *responsesUsage) *agent.LLMResponse {
	out := &agent.LLMResponse{
		Message: agent.Message{
			Role: agent.RoleAssistant,
		},
		FinishReason: "stop",
	}

	var textParts []string
	var reasoningParts []string

	for _, item := range items {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					textParts = append(textParts, c.Text)
				}
			}
		case "function_call":
			out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		case "reasoning":
			for _, s := range item.Summary {
				if s.Type == "summary_text" {
					reasoningParts = append(reasoningParts, s.Text)
				}
			}
		}
	}

	out.Message.Content = strings.Join(textParts, "")
	if len(out.Message.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}

	if len(reasoningParts) > 0 {
		if out.Message.ProviderMetadata == nil {
			out.Message.ProviderMetadata = make(map[string]any)
		}
		out.Message.ProviderMetadata["reasoning_content"] = strings.Join(reasoningParts, "\n")
	}

	if usage != nil {
		raw := responsesUsageToRaw(usage)
		out.Usage = &agent.Usage{
			PromptTokens:          usage.InputTokens,
			CompletionTokens:      usage.OutputTokens,
			TotalTokens:           usage.TotalTokens,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  ad.Config.ResolveCost(raw),
		}
	}

	return out
}

// responsesUsageToRaw maps Responses API usage fields to a RawUsage for cost calculation.
func responsesUsageToRaw(u *responsesUsage) RawUsage {
	if u == nil {
		return RawUsage{}
	}
	raw := RawUsage{
		promptTokens:     u.InputTokens,
		completionTokens: u.OutputTokens,
		totalTokens:      u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		raw.promptCacheHit = u.InputTokensDetails.CachedTokens
	}
	return raw
}

// ============================================================================
// Streaming SSE parsing
// ============================================================================

// responsesSSEEvent holds a typed SSE event from the Responses streaming API.
type responsesSSEEvent struct {
	Type string `json:"type"`
	Item *struct {
		ID        string             `json:"id"`
		Type      string             `json:"type"`
		CallID    string             `json:"call_id"`
		Name      string             `json:"name"`
		Arguments string             `json:"arguments"`
		Content   []responsesContent `json:"content"`
	} `json:"item"`
	ItemID       string `json:"item_id"`
	OutputIndex  *int   `json:"output_index"`
	ContentIndex *int   `json:"content_index"`
	Delta        string `json:"delta"`
	Text         string `json:"text"`
	Arguments    string `json:"arguments"`
	Response     *struct {
		Output []responsesItem `json:"output"`
		Usage  *responsesUsage `json:"usage"`
	} `json:"response"`
}

func (ad *OpenAIResponsesAdapter) parseStream(body io.Reader, onDelta func(string)) (*agent.LLMResponse, error) {
	var textBuf strings.Builder
	var reasoningBuf strings.Builder
	accum := newIndexAccumulator()
	var pendingUsage *responsesUsage

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip event: lines — we use the type field in the data payload
		if strings.HasPrefix(line, "event:") {
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var evt responsesSSEEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "response.output_text.delta":
			textBuf.WriteString(evt.Delta)
			onDelta(evt.Delta)

		case "response.reasoning_summary_text.delta":
			reasoningBuf.WriteString(evt.Delta)

		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type == "function_call" && evt.OutputIndex != nil {
				accum.add(*evt.OutputIndex, evt.Item.CallID, evt.Item.Name, "")
			}

		case "response.function_call_arguments.delta":
			if evt.OutputIndex != nil {
				accum.add(*evt.OutputIndex, "", "", evt.Delta)
			}

		case "response.completed":
			if evt.Response != nil {
				pendingUsage = evt.Response.Usage
			}

		case "error":
			return nil, fmt.Errorf("openai-responses: stream error: %s", payload)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("openai-responses: stream read: %w", err)
	}

	toolCalls := accum.flush()
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	resp := &agent.LLMResponse{
		Message: agent.Message{
			Role:      agent.RoleAssistant,
			Content:   textBuf.String(),
			ToolCalls: toolCalls,
		},
		FinishReason: finishReason,
	}

	if reasoningBuf.Len() > 0 {
		if resp.Message.ProviderMetadata == nil {
			resp.Message.ProviderMetadata = make(map[string]any)
		}
		resp.Message.ProviderMetadata["reasoning_content"] = reasoningBuf.String()
	}

	if pendingUsage != nil {
		raw := responsesUsageToRaw(pendingUsage)
		resp.Usage = &agent.Usage{
			PromptTokens:          pendingUsage.InputTokens,
			CompletionTokens:      pendingUsage.OutputTokens,
			TotalTokens:           pendingUsage.TotalTokens,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  ad.Config.ResolveCost(raw),
		}
	}

	return resp, nil
}

// Ensure adapter implements the interface.
var _ agent.LLMAdapter = (*OpenAIResponsesAdapter)(nil)
