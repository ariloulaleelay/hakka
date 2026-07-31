package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

type AnthropicAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
	Model      string
	Config     AnthropicConfig
}

func NewAnthropicAdapter(client *http.Client, baseURL, model string, cfg AnthropicConfig) *AnthropicAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &AnthropicAdapter{
		HTTPClient: client,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		Config:     cfg,
	}
}

// ===== Protocol types =====

type anthMessage struct {
	Role    string      `json:"role"`
	Content []anthBlock `json:"content"`
}

type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthRequest struct {
	Model     string        `json:"model"`
	System    string        `json:"system,omitempty"`
	Messages  []anthMessage `json:"messages"`
	Tools     []anthTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream,omitempty"`
}

type anthResponse struct {
	ID         string      `json:"id"`
	Role       string      `json:"role"`
	Content    []anthBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ===== Encode (hakka → Anthropic) =====

func toAnthropicSystem(system, content string) string {
	if system == "" {
		return content
	}
	return system + "\n\n" + content
}

func appendUserBlock(messages []anthMessage, content string) []anthMessage {
	block := anthBlock{Type: "text", Text: content}
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, block)
		return messages
	}
	return append(messages, anthMessage{Role: "user", Content: []anthBlock{block}})
}

func appendAssistantBlocks(messages []anthMessage, content string, toolCalls []agent.ToolCall) []anthMessage {
	blocks := textAndToolBlocks(content, toolCalls)
	if len(blocks) == 0 {
		return messages
	}
	if len(messages) > 0 && messages[len(messages)-1].Role == "assistant" {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, blocks...)
		return messages
	}
	return append(messages, anthMessage{Role: "assistant", Content: blocks})
}

func textAndToolBlocks(content string, toolCalls []agent.ToolCall) []anthBlock {
	var blocks []anthBlock
	if content != "" {
		blocks = append(blocks, anthBlock{Type: "text", Text: content})
	}
	for _, tc := range toolCalls {
		args := tc.Arguments
		if args == "" {
			args = "{}"
		}
		blocks = append(blocks, anthBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: json.RawMessage(args),
		})
	}
	return blocks
}

func appendToolResultBlock(messages []anthMessage, toolCallID, content string) []anthMessage {
	block := anthBlock{
		Type:      "tool_result",
		ToolUseID: toolCallID,
		Content:   content,
	}
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, block)
		return messages
	}
	return append(messages, anthMessage{Role: "user", Content: []anthBlock{block}})
}

func toAnthropic(history []agent.Message) (system string, messages []anthMessage) {
	for _, msg := range history {
		switch msg.Role {
		case agent.RoleSystem:
			system = toAnthropicSystem(system, msg.Content)
		case agent.RoleUser:
			messages = appendUserBlock(messages, msg.Content)
		case agent.RoleAssistant:
			messages = appendAssistantBlocks(messages, msg.Content, msg.ToolCalls)
		case agent.RoleTool:
			messages = appendToolResultBlock(messages, msg.ToolCallID, msg.Content)
		}
	}
	return
}

func toAnthropicTools(tools []agent.ToolSchema) []anthTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthTool, 0, len(tools))
	for _, td := range tools {
		schema := td.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, anthTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: schema,
		})
	}
	return out
}

func (ad *AnthropicAdapter) buildRequest(history []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) anthRequest {
	system, msgs := toAnthropic(history)
	req := anthRequest{
		Model:     ad.Model,
		System:    system,
		Messages:  msgs,
		Tools:     toAnthropicTools(tools),
		MaxTokens: ad.Config.MaxTokens,
	}
	if opts.MaxTokens != nil {
		req.MaxTokens = *opts.MaxTokens
	}
	return req
}

// ===== Decode (Anthropic → hakka) =====

func (ad *AnthropicAdapter) parseResponse(rawBody []byte) (*agent.LLMResponse, error) {
	raw := extractRawUsage(rawBody)

	var resp anthResponse
	if err := json.Unmarshal(rawBody, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	out := &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant},
		FinishReason: resp.StopReason,
		Usage: &agent.Usage{
			PromptTokens:          resp.Usage.InputTokens,
			CompletionTokens:      resp.Usage.OutputTokens,
			TotalTokens:           resp.Usage.InputTokens + resp.Usage.OutputTokens,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  ad.Config.ResolveCost(raw),
		},
	}
	var text strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args := string(block.Input)
			if args == "" || !json.Valid(block.Input) {
				args = "{}"
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	out.Message.Content = text.String()
	return out, nil
}

// ===== LLMAdapter =====

func (ad *AnthropicAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if onDelta != nil {
		return ad.completeStream(ctx, msgs, tools, opts, onDelta)
	}
	return ad.completeNonStream(ctx, msgs, tools, opts)
}

func (ad *AnthropicAdapter) completeNonStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	rawBody, err := doJSONPostWithRetry(ctx, ad.HTTPClient, ad.messagesURL(),
		ad.buildRequest(msgs, tools, opts), ad.Config.RetryPolicy(),
		"anthropic", ad.headers(), ad.Config.DebugDir())
	if err != nil {
		return nil, err
	}
	return ad.parseResponse(rawBody)
}

func (ad *AnthropicAdapter) messagesURL() string {
	return ad.BaseURL + "/messages"
}

func (ad *AnthropicAdapter) headers() map[string]string {
	h := map[string]string{}
	if ad.Config.Version != "" {
		h["anthropic-version"] = ad.Config.Version
	}
	return h
}

// ===== Stream =====

func (ad *AnthropicAdapter) completeStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	req := ad.buildRequest(msgs, tools, opts)
	req.Stream = true

	body, err := doStreamPostWithRetry(ctx, ad.HTTPClient, ad.messagesURL(), req,
		ad.Config.RetryPolicy(), "anthropic", ad.headers(), ad.Config.DebugDir())
	if err != nil {
		return nil, err
	}
	defer body.Close()

	accum := newIndexAccumulator()
	var textBuf strings.Builder
	var pendingUsage *agent.Usage
	var finishReason string

	// input tokens arrive in message_start; output tokens in message_delta.
	var inputTokens int

	scanErr := scanSSE(body, func(payload string) error {
		evt, err := ad.parseSSEPayload(payload)
		if err != nil {
			return err
		}
		if evt == nil {
			return nil
		}
		// A single event may carry several fields (e.g. message_delta has
		// both usage and finish_reason), so handle each independently.
		if evt.InputTokens != nil {
			inputTokens = *evt.InputTokens
		}
		if evt.Delta != "" {
			textBuf.WriteString(evt.Delta)
			onDelta(evt.Delta)
		}
		if evt.ToolCall != nil {
			accum.add(evt.BlockIndex, evt.ToolCall.ID, evt.ToolCall.Name, "")
		}
		if evt.PartialJSON != "" {
			accum.add(evt.BlockIndex, "", "", evt.PartialJSON)
		}
		if evt.Usage != nil {
			pendingUsage = evt.Usage
		}
		if evt.FinishReason != "" {
			finishReason = evt.FinishReason
		}
		if evt.Done {
			return nil
		}
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}

	if pendingUsage == nil {
		pendingUsage = &agent.Usage{}
	}
	if inputTokens > 0 && pendingUsage.PromptTokens == 0 {
		pendingUsage.PromptTokens = inputTokens
	}
	// Total is always the sum of input + output for Anthropic; recompute it
	// since prompt tokens arrive in message_start and output in message_delta.
	if pendingUsage.CompletionTokens > 0 || pendingUsage.PromptTokens > 0 {
		pendingUsage.TotalTokens = pendingUsage.PromptTokens + pendingUsage.CompletionTokens
	}
	// Recompute cost from the final token counts: message_delta only carried
	// output_tokens, so pricing computed there would miss the input tokens.
	pendingUsage.Cost = ad.Config.ResolveCost(RawUsage{
		promptTokens:     pendingUsage.PromptTokens,
		completionTokens: pendingUsage.CompletionTokens,
		promptCacheHit:   pendingUsage.PromptCacheHitTokens,
		promptCacheMiss:  pendingUsage.PromptCacheMissTokens,
	})

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

type parsedAnthEvent struct {
	Delta        string
	PartialJSON  string
	BlockIndex   int
	ToolCall     *agent.ToolCall
	Done         bool
	Usage        *agent.Usage
	FinishReason string
	InputTokens  *int
}

func (ad *AnthropicAdapter) parseSSEPayload(payload string) (*parsedAnthEvent, error) {
	var base struct {
		Type    string `json:"type"`
		Message struct {
			Usage struct {
				InputTokens int `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &base); err != nil {
		return nil, nil
	}

	switch base.Type {
	case "message_start":
		if base.Message.Usage.InputTokens > 0 {
			tokens := base.Message.Usage.InputTokens
			return &parsedAnthEvent{InputTokens: &tokens}, nil
		}
		return nil, nil

	case "content_block_start":
		var evt struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return nil, nil
		}
		if evt.ContentBlock.Type == "tool_use" {
			return &parsedAnthEvent{
				BlockIndex: evt.Index,
				ToolCall: &agent.ToolCall{
					ID:   evt.ContentBlock.ID,
					Name: evt.ContentBlock.Name,
				},
			}, nil
		}

	case "content_block_delta":
		var evt struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return nil, nil
		}
		switch evt.Delta.Type {
		case "text_delta":
			if evt.Delta.Text != "" {
				return &parsedAnthEvent{Delta: evt.Delta.Text}, nil
			}
		case "input_json_delta":
			if evt.Delta.PartialJSON != "" {
				return &parsedAnthEvent{PartialJSON: evt.Delta.PartialJSON, BlockIndex: evt.Index}, nil
			}
		}

	case "message_delta":
		var evt struct {
			Delta struct {
				StopReason   string `json:"stop_reason"`
				StopSequence string `json:"stop_sequence"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return nil, nil
		}
		raw := extractRawUsage([]byte(payload))
		return &parsedAnthEvent{
			FinishReason: evt.Delta.StopReason,
			Usage: &agent.Usage{
				PromptTokens:          0, // filled from message_start
				CompletionTokens:      evt.Usage.OutputTokens,
				TotalTokens:           evt.Usage.OutputTokens,
				PromptCacheHitTokens:  raw.promptCacheHit,
				PromptCacheMissTokens: raw.promptCacheMiss,
				Cost:                  ad.Config.ResolveCost(raw),
			},
		}, nil

	case "message_stop":
		return &parsedAnthEvent{Done: true}, nil

	case "error":
		if base.Error != nil {
			return nil, fmt.Errorf("anthropic: %s: %s", base.Error.Type, base.Error.Message)
		}
	}

	return nil, nil
}

var _ agent.LLMAdapter = (*AnthropicAdapter)(nil)
