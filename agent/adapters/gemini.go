package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/ariloulaleelay/hakka/agent"
)

type GeminiAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
	Model      string
	Config     GeminiConfig
}

func NewGeminiAdapter(client *http.Client, baseURL, model string, cfg GeminiConfig) *GeminiAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &GeminiAdapter{
		HTTPClient: client,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		Config:     cfg,
	}
}

// ===== Protocol types =====

type geminiPart struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *geminiFnCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFnResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

type geminiFnCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFnResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiFnDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFnDecl `json:"functionDeclarations"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig         `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	Temperature     *float32 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// ===== Encode (hakka → Gemini) =====

func appendGeminiSystem(sys *geminiSystemInstruction, content string) *geminiSystemInstruction {
	if sys == nil {
		sys = &geminiSystemInstruction{}
	}
	sys.Parts = append(sys.Parts, geminiPart{Text: content})
	return sys
}

func appendGeminiUserPart(contents []geminiContent, content string) []geminiContent {
	part := geminiPart{Text: content}
	if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, part)
		return contents
	}
	return append(contents, geminiContent{Role: "user", Parts: []geminiPart{part}})
}

func appendGeminiModelParts(contents []geminiContent, content string, toolCalls []agent.ToolCall, signatures []string) []geminiContent {
	parts := geminiTextAndToolParts(content, toolCalls, signatures)
	if len(parts) == 0 {
		return contents
	}
	if len(contents) > 0 && contents[len(contents)-1].Role == "model" {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, parts...)
		return contents
	}
	return append(contents, geminiContent{Role: "model", Parts: parts})
}

func geminiTextAndToolParts(content string, toolCalls []agent.ToolCall, signatures []string) []geminiPart {
	var parts []geminiPart
	if content != "" {
		parts = append(parts, geminiPart{Text: content})
	}
	for idx, tc := range toolCalls {
		var args map[string]any
		if tc.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		part := geminiPart{FunctionCall: &geminiFnCall{Name: tc.Name, Args: args}}
		if idx < len(signatures) && signatures[idx] != "" {
			part.ThoughtSignature = signatures[idx]
		} else {
			// Gemini 3 enforces thought_signature on every functionCall
			// part. When history was created by another provider (no
			// stored signatures), use the sanctioned synthetic fallback.
			part.ThoughtSignature = "skip_thought_signature_validator"
		}
		parts = append(parts, part)
	}
	return parts
}

func appendGeminiToolResult(contents []geminiContent, name, content string) []geminiContent {
	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil || result == nil {
		result = map[string]any{"result": content}
	}
	part := geminiPart{FunctionResponse: &geminiFnResponse{Name: name, Response: result}}
	if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, part)
		return contents
	}
	return append(contents, geminiContent{Role: "user", Parts: []geminiPart{part}})
}

func toGemini(history []agent.Message) (sys *geminiSystemInstruction, contents []geminiContent) {
	for _, msg := range history {
		switch msg.Role {
		case agent.RoleSystem:
			sys = appendGeminiSystem(sys, msg.Content)
		case agent.RoleUser:
			contents = appendGeminiUserPart(contents, msg.Content)
		case agent.RoleAssistant:
			var sigs []string
			if s, ok := msg.ProviderMetadata["gemini_signatures"].([]string); ok {
				sigs = s
			}
			contents = appendGeminiModelParts(contents, msg.Content, msg.ToolCalls, sigs)
		case agent.RoleTool:
			contents = appendGeminiToolResult(contents, msg.Name, msg.Content)
		}
	}
	return
}

func toGeminiTools(tools []agent.ToolSchema) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]geminiFnDecl, 0, len(tools))
	for _, td := range tools {
		decls = append(decls, geminiFnDecl{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

func (ad *GeminiAdapter) buildRequest(history []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) geminiRequest {
	sys, contents := toGemini(history)
	req := geminiRequest{
		Contents:          contents,
		Tools:             toGeminiTools(tools),
		SystemInstruction: sys,
	}
	if opts.Temperature != nil || opts.MaxTokens != nil {
		req.GenerationConfig = &geminiGenConfig{
			Temperature:     opts.Temperature,
			MaxOutputTokens: opts.MaxTokens,
		}
	}
	return req
}

// ===== Decode (Gemini → hakka) =====

// appendGeminiResponse merges one GenerateContentResponse chunk (non-stream
// or a single SSE chunk) into the accumulating LLMResponse. rawChunk is the
// raw JSON bytes of the chunk, used for cache/cost extraction.
func (ad *GeminiAdapter) appendGeminiResponse(out *agent.LLMResponse, resp geminiResponse, rawChunk []byte) {
	if len(resp.Candidates) == 0 {
		return
	}
	candidate := resp.Candidates[0]
	if candidate.FinishReason != "" {
		out.FinishReason = candidate.FinishReason
	}
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			out.Message.Content += part.Text
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			if len(args) == 0 {
				args = []byte("{}")
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, agent.ToolCall{
				ID:        uuid.NewString(),
				Name:      part.FunctionCall.Name,
				Arguments: string(args),
			})
			if out.Message.ProviderMetadata == nil {
				out.Message.ProviderMetadata = map[string]any{}
			}
			sigs, _ := out.Message.ProviderMetadata["gemini_signatures"].([]string)
			out.Message.ProviderMetadata["gemini_signatures"] = append(sigs, part.ThoughtSignature)
		}
	}
	if resp.UsageMetadata.TotalTokenCount > 0 {
		raw := extractRawUsage(rawChunk)
		out.Usage = &agent.Usage{
			PromptTokens:          resp.UsageMetadata.PromptTokenCount,
			CompletionTokens:      resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:           resp.UsageMetadata.TotalTokenCount,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  ad.Config.ResolveCost(raw),
		}
	}
}

func (ad *GeminiAdapter) parseResponse(rawBody []byte) (*agent.LLMResponse, error) {
	var resp geminiResponse
	if err := json.Unmarshal(rawBody, &resp); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates")
	}

	out := &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant},
		FinishReason: resp.Candidates[0].FinishReason,
	}
	ad.appendGeminiResponse(out, resp, rawBody)
	return out, nil
}

// ===== LLMAdapter =====

func (ad *GeminiAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if onDelta != nil {
		return ad.completeStream(ctx, msgs, tools, opts, onDelta)
	}
	return ad.completeNonStream(ctx, msgs, tools, opts)
}

func (ad *GeminiAdapter) completeNonStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	rawBody, err := doJSONPostWithRetry(ctx, ad.HTTPClient, ad.endpoint(),
		ad.buildRequest(msgs, tools, opts), ad.Config.RetryPolicy(),
		"gemini", nil, ad.Config.DebugDir())
	if err != nil {
		return nil, err
	}
	return ad.parseResponse(rawBody)
}

func (ad *GeminiAdapter) completeStream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	body, err := doStreamPostWithRetry(ctx, ad.HTTPClient, ad.streamEndpoint(),
		ad.buildRequest(msgs, tools, opts), ad.Config.RetryPolicy(),
		"gemini", nil, ad.Config.DebugDir())
	if err != nil {
		return nil, err
	}
	defer body.Close()

	out := &agent.LLMResponse{
		Message: agent.Message{Role: agent.RoleAssistant},
	}

	scanErr := scanSSE(body, func(payload string) error {
		var chunk geminiResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Tolerate non-JSON payloads (keepalives, etc).
			return nil
		}
		if len(chunk.Candidates) == 0 {
			return nil
		}
		for _, part := range chunk.Candidates[0].Content.Parts {
			if part.Text != "" {
				onDelta(part.Text)
			}
		}
		ad.appendGeminiResponse(out, chunk, []byte(payload))
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}
	return out, nil
}

func (ad *GeminiAdapter) endpoint() string {
	return fmt.Sprintf("%s/models/%s:generateContent", ad.BaseURL, ad.Model)
}

func (ad *GeminiAdapter) streamEndpoint() string {
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", ad.BaseURL, ad.Model)
}

var _ agent.LLMAdapter = (*GeminiAdapter)(nil)
