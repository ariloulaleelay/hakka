package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/ariloulaleelay/hakka/agent"
)

// GeminiAdapter speaks the Google Generative Language v1beta API.
//
//
// Model example:
//
//	gemini-3.1-pro-preview
type GeminiAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
	Model      string
	Pricing    agent.Pricing
}

func NewGeminiAdapter(client *http.Client, baseURL, model string) *GeminiAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &GeminiAdapter{
		HTTPClient: client,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
	}
}

// --- protocol structs -------------------------------------------------------

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
	Role  string       `json:"role"` // user|model
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

// --- conversion -------------------------------------------------------------

// appendGeminiSystem adds text to the system instruction.
func appendGeminiSystem(sys *geminiSystemInstruction, content string) *geminiSystemInstruction {
	if sys == nil {
		sys = &geminiSystemInstruction{}
	}
	sys.Parts = append(sys.Parts, geminiPart{Text: content})
	return sys
}

// appendGeminiUserPart adds a user text part, merging with the last user content block.
func appendGeminiUserPart(contents []geminiContent, content string) []geminiContent {
	part := geminiPart{Text: content}
	if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, part)
		return contents
	}
	return append(contents, geminiContent{
		Role:  "user",
		Parts: []geminiPart{part},
	})
}

// appendGeminiModelParts adds an assistant's text and function-call parts.
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

// geminiTextAndToolParts builds gemini parts from text and tool calls.
func geminiTextAndToolParts(content string, toolCalls []agent.ToolCall, signatures []string) []geminiPart {
	var parts []geminiPart
	if content != "" {
		parts = append(parts, geminiPart{Text: content})
	}
	for idx, toolCall := range toolCalls {
		var args map[string]any
		if toolCall.Arguments != "" {
			_ = json.Unmarshal([]byte(toolCall.Arguments), &args)
		}
		part := geminiPart{
			FunctionCall: &geminiFnCall{Name: toolCall.Name, Args: args},
		}
		if idx < len(signatures) {
			part.ThoughtSignature = signatures[idx]
		}
		parts = append(parts, part)
	}
	return parts
}

// appendGeminiToolResult adds a function-response part for tool results.
func appendGeminiToolResult(contents []geminiContent, name, content string) []geminiContent {
	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil || result == nil {
		result = map[string]any{"result": content}
	}
	part := geminiPart{
		FunctionResponse: &geminiFnResponse{Name: name, Response: result},
	}
	if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, part)
		return contents
	}
	return append(contents, geminiContent{
		Role:  "user",
		Parts: []geminiPart{part},
	})
}

// toGemini maps hakka history -> Gemini system instruction + contents.
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
	for _, toolDef := range tools {
		decls = append(decls, geminiFnDecl{
			Name:        toolDef.Name,
			Description: toolDef.Description,
			Parameters:  toolDef.Parameters,
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

// --- request building -------------------------------------------------------

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

// --- endpoint URLs ----------------------------------------------------------

func (ad *GeminiAdapter) endpoint() string {
	return fmt.Sprintf("%s/models/%s:generateContent", ad.BaseURL, ad.Model)
}

func (ad *GeminiAdapter) streamEndpoint() string {
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", ad.BaseURL, ad.Model)
}

// --- Complete ---------------------------------------------------------------

func (ad *GeminiAdapter) Complete(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (*agent.LLMResponse, error) {
	// First decode into raw JSON to extract cost
	var rawBody json.RawMessage
	if err := doJSONPost(ctx, ad.HTTPClient, ad.endpoint(), ad.buildRequest(msgs, tools, opts), &rawBody, "gemini", nil); err != nil {
		return nil, err
	}

	raw := extractRawUsage(rawBody)

	var geminiResp geminiResponse
	if err := json.Unmarshal(rawBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}
	if len(geminiResp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates")
	}

	// Determine cost: use provider's cost if available, otherwise calculate from pricing
	cost := raw.cost
	if cost == 0 && (ad.Pricing.Input != 0 || ad.Pricing.Output != 0) {
		cost = calculateCostFromPricing(ad.Pricing, raw)
	}

	out := &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant},
		FinishReason: geminiResp.Candidates[0].FinishReason,
		Usage: &agent.Usage{
			PromptTokens:          geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens:      geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:           geminiResp.UsageMetadata.TotalTokenCount,
			PromptCacheHitTokens:  raw.promptCacheHit,
			PromptCacheMissTokens: raw.promptCacheMiss,
			Cost:                  cost,
		},
	}
	var text strings.Builder
	var sigs []string
	for _, part := range geminiResp.Candidates[0].Content.Parts {
		if part.Text != "" {
			text.WriteString(part.Text)
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
			sigs = append(sigs, part.ThoughtSignature)
		}
	}
	out.Message.Content = text.String()
	if len(sigs) > 0 {
		out.Message.ProviderMetadata = map[string]any{"gemini_signatures": sigs}
	}
	return out, nil
}

// --- Stream -----------------------------------------------------------------

func (ad *GeminiAdapter) Stream(ctx context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	body, err := doStreamPost(ctx, ad.HTTPClient, ad.streamEndpoint(), ad.buildRequest(msgs, tools, opts), "gemini", nil)
	if err != nil {
		return nil, err
	}

	resultCh := make(chan agent.StreamResult, 16)

	go func() {
		defer close(resultCh)
		defer body.Close()

		accum := newAppendAccumulator()
		var pendingUsage *agent.Usage

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			var geminiResp geminiResponse
			if err := json.Unmarshal([]byte(payload), &geminiResp); err != nil {
				continue
			}
			for _, candidate := range geminiResp.Candidates {
				for _, part := range candidate.Content.Parts {
					if part.FunctionCall != nil {
						args, _ := json.Marshal(part.FunctionCall.Args)
						if len(args) == 0 {
							args = []byte("{}")
						}
						accum.add(agent.ToolCall{
							ID:        uuid.NewString(),
							Name:      part.FunctionCall.Name,
							Arguments: string(args),
						})
						continue
					}
					if part.Text != "" {
						select {
						case <-ctx.Done():
							resultCh <- agent.StreamResult{Err: ctx.Err()}
							return
						case resultCh <- agent.StreamResult{Delta: part.Text}:
						}
					}
				}

				// Check finish reason
				if candidate.FinishReason != "" {
					if geminiResp.UsageMetadata.TotalTokenCount > 0 {
						raw := extractRawUsage([]byte(payload))
						cost := raw.cost
						if cost == 0 && (ad.Pricing.Input != 0 || ad.Pricing.Output != 0) {
							cost = calculateCostFromPricing(ad.Pricing, raw)
						}
						pendingUsage = &agent.Usage{
							PromptTokens:          geminiResp.UsageMetadata.PromptTokenCount,
							CompletionTokens:      geminiResp.UsageMetadata.CandidatesTokenCount,
							TotalTokens:           geminiResp.UsageMetadata.TotalTokenCount,
							PromptCacheHitTokens:  raw.promptCacheHit,
							PromptCacheMissTokens: raw.promptCacheMiss,
							Cost:                  cost,
						}
					}
					sendStreamFinal(resultCh, accum.flush(), pendingUsage)
					return
				}
			}
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			resultCh <- agent.StreamResult{Err: err}
			return
		}
		// Stream ended without finish reason — normal completion
		sendStreamFinal(resultCh, accum.flush(), pendingUsage)
	}()

	return resultCh, nil
}

var _ agent.LLMAdapter = (*GeminiAdapter)(nil)
