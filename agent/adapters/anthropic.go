package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// AnthropicAdapter speaks the Anthropic Messages API but is exposed via
// hakka's OpenAI-shaped internal protocol.
type AnthropicAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
	Model      string
	Version    string // anthropic-version header; default "2023-06-01"
	MaxTokens  int    // required by Anthropic; default 1024
}

func NewAnthropicAdapter(client *http.Client, baseURL, model string) *AnthropicAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &AnthropicAdapter{
		HTTPClient: client,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		Version:    "2023-06-01",
		MaxTokens:  1024,
	}
}

// --- protocol structs -------------------------------------------------------

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
	Content   string          `json:"content,omitempty"` // tool_result text
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

// --- conversion -------------------------------------------------------------

// toAnthropicSystem collects system-prompt messages into one string.
func toAnthropicSystem(system string, content string) string {
	if system == "" {
		return content
	}
	return system + "\n\n" + content
}

// appendUserBlock adds a user text block to the message list, merging with
// the last message if it is also a user message.
func appendUserBlock(messages []anthMessage, content string) []anthMessage {
	block := anthBlock{Type: "text", Text: content}
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, block)
		return messages
	}
	return append(messages, anthMessage{Role: "user", Content: []anthBlock{block}})
}

// appendAssistantBlocks adds text and tool_use blocks for an assistant message.
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

// textAndToolBlocks converts assistant text and tool calls into anthropic blocks.
// The Arguments in each tool call are expected to be valid JSON (an invariant
// enforced by ingestion in Complete/Stream). Empty arguments are normalised to
// "{}" — the Anthropic API requires a JSON object for the input field.
func textAndToolBlocks(content string, toolCalls []agent.ToolCall) []anthBlock {
	var blocks []anthBlock
	if content != "" {
		blocks = append(blocks, anthBlock{Type: "text", Text: content})
	}
	for _, toolCall := range toolCalls {
		args := toolCall.Arguments
		if args == "" {
			args = "{}"
		}
		blocks = append(blocks, anthBlock{
			Type:  "tool_use",
			ID:    toolCall.ID,
			Name:  toolCall.Name,
			Input: json.RawMessage(args),
		})
	}
	return blocks
}

// appendToolResultBlock adds a tool_result block, merging with the last user message.
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
	return append(messages, anthMessage{
		Role:    "user",
		Content: []anthBlock{block},
	})
}

// toAnthropic maps hakka history -> Anthropic system + messages.
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
	for _, toolDef := range tools {
		schema := toolDef.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, anthTool{
			Name:        toolDef.Name,
			Description: toolDef.Description,
			InputSchema: schema,
		})
	}
	return out
}

// --- request building -------------------------------------------------------

func (ad *AnthropicAdapter) buildRequest(history []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) anthRequest {
	system, msgs := toAnthropic(history)
	req := anthRequest{
		Model:     ad.Model,
		System:    system,
		Messages:  msgs,
		Tools:     toAnthropicTools(tools),
		MaxTokens: ad.MaxTokens,
	}
	if opts.MaxTokens != nil {
		req.MaxTokens = *opts.MaxTokens
	}
	return req
}

func (ad *AnthropicAdapter) buildStreamRequest(history []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) anthRequest {
	req := ad.buildRequest(history, tools, opts)
	req.Stream = true
	return req
}

func (ad *AnthropicAdapter) messagesURL() string {
	return ad.BaseURL + "/messages"
}

func (ad *AnthropicAdapter) extraHeaders() map[string]string {
	h := map[string]string{}
	if ad.Version != "" {
		h["anthropic-version"] = ad.Version
	}
	return h
}

// --- Complete ---------------------------------------------------------------

func (ad *AnthropicAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	var anthResp anthResponse
	if err := doJSONPost(ctx, ad.HTTPClient, ad.messagesURL(), ad.buildRequest(msgs, tools, opts), &anthResp, "anthropic", ad.extraHeaders()); err != nil {
		return nil, err
	}

	out := &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant},
		FinishReason: anthResp.StopReason,
		Usage: &agent.Usage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
		},
	}
	var text strings.Builder
	for _, block := range anthResp.Content {
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

// --- Stream -----------------------------------------------------------------

func (ad *AnthropicAdapter) Stream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	req := ad.buildStreamRequest(msgs, tools, opts)
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ad.messagesURL(), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range ad.extraHeaders() {
		httpReq.Header.Set(k, v)
	}

	resp, err := ad.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: %s: %s", resp.Status, strings.TrimSpace(string(buf)))
	}

	resultCh := make(chan agent.StreamResult, 16)

	go func() {
		defer close(resultCh)
		defer resp.Body.Close()

		// Accumulate tool calls during stream, keyed by content block index.
		// The Anthropic API streams tool arguments incrementally via
		// input_json_delta events, so we merge fragments by index.
		pendingTools := make(map[int]*agent.ToolCall)
		var pendingUsage *agent.Usage

		err = scanSSELines(ctx, resp.Body, func(payload string) error {
			result, parseErr := ad.parseSSEPayload(payload)
			if parseErr != nil {
				return parseErr
			}
			if result == nil {
				return nil
			}

			if result.Delta != "" {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case resultCh <- agent.StreamResult{Delta: result.Delta}:
				}
			}

			if result.ToolCall != nil {
				// Store tool call by block index. The initial input from
				// content_block_start is an empty stub; real arguments
				// arrive via input_json_delta events.
				if _, exists := pendingTools[result.BlockIndex]; !exists {
					pendingTools[result.BlockIndex] = result.ToolCall
				}
			}

			if result.PartialJSON != "" {
				// Accumulate partial JSON into the tool call at this index.
				idx := result.BlockIndex
				if pt, ok := pendingTools[idx]; ok {
					pt.Arguments += result.PartialJSON
				}
			}

			if result.Done {
				// Build tool calls from accumulated state, sorted by index.
				if len(pendingTools) > 0 {
					calls := make([]agent.ToolCall, 0, len(pendingTools))
					indices := make([]int, 0, len(pendingTools))
					for idx := range pendingTools {
						indices = append(indices, idx)
					}
					sort.Ints(indices)
					for _, idx := range indices {
						tc := *pendingTools[idx]
						args := []byte(tc.Arguments)
						if len(args) == 0 || !json.Valid(args) {
							tc.Arguments = "{}"
						}
						calls = append(calls, tc)
					}
					resultCh <- agent.StreamResult{ToolCalls: calls, Usage: pendingUsage}
				} else {
					resultCh <- agent.StreamResult{Done: true, Usage: pendingUsage}
				}
				return io.EOF
			}

			if result.Usage != nil {
				pendingUsage = result.Usage
			}
			return nil
		})
		if err != nil && err != io.EOF {
			resultCh <- agent.StreamResult{Err: err}
			return
		}
	}()

	return resultCh, nil
}

// parsedAnthEvent holds the parsed result of a single SSE payload.
type parsedAnthEvent struct {
	Delta       string
	PartialJSON string          // incremental JSON from input_json_delta events
	BlockIndex  int             // content block index (for tool calls and partial JSON)
	ToolCall    *agent.ToolCall
	Done        bool
	Usage       *agent.Usage
}

func (ad *AnthropicAdapter) parseSSEPayload(payload string) (*parsedAnthEvent, error) {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &base); err != nil {
		return nil, nil // skip unparseable events
	}

	switch base.Type {
	case "content_block_start":
		var evt struct {
			Index int `json:"index"`
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
					ID:        evt.ContentBlock.ID,
					Name:      evt.ContentBlock.Name,
					Arguments: "", // arguments arrive via input_json_delta events
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
				return &parsedAnthEvent{
					PartialJSON: evt.Delta.PartialJSON,
					BlockIndex:  evt.Index,
				}, nil
			}
		}

	case "message_delta":
		var evt struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return nil, nil
		}
		return &parsedAnthEvent{
			Usage: &agent.Usage{
				PromptTokens:     evt.Usage.InputTokens,
				CompletionTokens: evt.Usage.OutputTokens,
				TotalTokens:      evt.Usage.InputTokens + evt.Usage.OutputTokens,
			},
		}, nil

	case "message_stop":
		return &parsedAnthEvent{Done: true}, nil
	}

	return nil, nil
}

var _ agent.LLMAdapter = (*AnthropicAdapter)(nil)
