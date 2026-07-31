package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

func newGeminiTestAdapter(t *testing.T, handler http.HandlerFunc) *GeminiAdapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewGeminiAdapter(srv.Client(), srv.URL, "test-model", NewGeminiConfig("", agent.RetryConfig{}, agent.Pricing{}))
}

func TestGeminiCompleteText(t *testing.T) {
	var path string
	var captured map[string]any
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"hello there"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":5,"totalTokenCount":9}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleSystem, Content: "be brief"},
			{Role: agent.RoleUser, Content: "hi"},
		}, nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "hello there" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if !strings.HasSuffix(path, "/models/test-model:generateContent") {
		t.Fatalf("path: %s", path)
	}
	sysInst, _ := captured["systemInstruction"].(map[string]any)
	if sysInst == nil {
		t.Fatalf("missing systemInstruction: %+v", captured)
	}
	parts, _ := sysInst["parts"].([]any)
	if len(parts) == 0 || parts[0].(map[string]any)["text"] != "be brief" {
		t.Fatalf("system not forwarded: %+v", sysInst)
	}
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestGeminiToolCallExtractsSignature(t *testing.T) {
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"name":"add","args":{"a":1,"b":2}},"thoughtSignature":"sig-xyz"}
			]},"finishReason":"STOP"}],
			"usageMetadata":{}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "compute"}},
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	sigs, _ := resp.Message.ProviderMetadata["gemini_signatures"].([]string)
	if len(sigs) != 1 || sigs[0] != "sig-xyz" {
		t.Fatalf("sigs: %+v", resp.Message.ProviderMetadata["gemini_signatures"])
	}
}

func TestGeminiReplaysSignatureAndToolResponse(t *testing.T) {
	var captured map[string]any
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"sum is 3"}]},"finishReason":"STOP"}],
			"usageMetadata":{}
		}`))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "compute"},
			{
				Role:             agent.RoleAssistant,
				ToolCalls:        []agent.ToolCall{{ID: "add", Name: "add", Arguments: `{"a":1,"b":2}`}},
				ProviderMetadata: map[string]any{"gemini_signatures": []string{"sig-xyz"}},
			},
			{Role: agent.RoleTool, Name: "add", ToolCallID: "add", Content: `{"sum":3}`},
		}, []agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	contents, _ := captured["contents"].([]any)
	var seenSig, seenResp bool
	for _, c := range contents {
		cm := c.(map[string]any)
		parts, _ := cm["parts"].([]any)
		for _, p := range parts {
			pm := p.(map[string]any)
			if pm["thoughtSignature"] == "sig-xyz" {
				seenSig = true
			}
			if fr, ok := pm["functionResponse"].(map[string]any); ok {
				if fr["name"] == "add" {
					seenResp = true
				}
			}
		}
	}
	if !seenSig {
		t.Fatalf("thoughtSignature not replayed: %+v", contents)
	}
	if !seenResp {
		t.Fatalf("functionResponse not emitted: %+v", contents)
	}
}

func TestGeminiStream(t *testing.T) {
	var path string
	var reqBody map[string]any
	var deltas []string
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("alt=sse not set: %q", r.URL.RawQuery)
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`)
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]}}]}`)
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":5,"totalTokenCount":9}}`)
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleSystem, Content: "be brief"},
			{Role: agent.RoleUser, Content: "hi"},
		}, nil, agent.CompleteOptions{},
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.HasSuffix(path, "/models/test-model:streamGenerateContent") {
		t.Fatalf("path: %s", path)
	}
	if resp.Message.Content != "Hello world" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Fatalf("deltas: %q", got)
	}
	if resp.FinishReason != "STOP" {
		t.Fatalf("finish_reason: %q", resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	// System prompt must still be forwarded in the streaming request.
	sysInst, _ := reqBody["systemInstruction"].(map[string]any)
	if sysInst == nil {
		t.Fatalf("missing systemInstruction: %+v", reqBody)
	}
}

func TestGeminiStreamToolCall(t *testing.T) {
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"add","args":{"a":1,"b":2}},"thoughtSignature":"sig-xyz"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "add"}},
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "add" {
		t.Fatalf("tool call name: %q", tc.Name)
	}
	var args map[string]float64
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	if args["a"] != 1 || args["b"] != 2 {
		t.Fatalf("args: %+v", args)
	}
	sigs, _ := resp.Message.ProviderMetadata["gemini_signatures"].([]string)
	if len(sigs) != 1 || sigs[0] != "sig-xyz" {
		t.Fatalf("sigs: %+v", resp.Message.ProviderMetadata["gemini_signatures"])
	}
}

func TestGeminiCompleteRetries429(t *testing.T) {
	var requests int32
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"too fast"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`))
	})

	adapter.Config = NewGeminiConfig(
		adapter.Config.DebugDir(),
		agent.RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Millisecond,
			MaxDelay:      50 * time.Millisecond,
			BackoffFactor: 1.0,
		},
		agent.Pricing{},
	)

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete after retry: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
}

func TestGeminiStreamRetries429(t *testing.T) {
	var requests int32
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"too fast"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]}}]}`)
		writeSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	})

	adapter.Config = NewGeminiConfig(
		adapter.Config.DebugDir(),
		agent.RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Millisecond,
			MaxDelay:      50 * time.Millisecond,
			BackoffFactor: 1.0,
		},
		agent.Pricing{},
	)

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream after retry: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
}

// TestGeminiSyntheticSignatureForProviderSwitch verifies that tool-call
// turns created by another provider (no gemini_signatures in metadata)
// get the sanctioned synthetic fallback thoughtSignature. Without this,
// Gemini 3 returns 400 when replaying functionCall parts from history.
func TestGeminiSyntheticSignatureForProviderSwitch(t *testing.T) {
	var captured map[string]any
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"sum is 3"}]},"finishReason":"STOP"}],
			"usageMetadata":{}
		}`))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "compute"},
			{
				Role:      agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "add", Arguments: `{"a":1,"b":2}`}},
				// NO ProviderMetadata — simulates history from DeepSeek/Claude
			},
			{Role: agent.RoleTool, Name: "add", ToolCallID: "call_1", Content: `{"sum":3}`},
		}, []agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	contents, _ := captured["contents"].([]any)
	var found bool
	for _, c := range contents {
		cm := c.(map[string]any)
		parts, _ := cm["parts"].([]any)
		for _, p := range parts {
			pm := p.(map[string]any)
			if fc, ok := pm["functionCall"]; ok && fc != nil {
				found = true
				ts := pm["thoughtSignature"]
				if ts != "skip_thought_signature_validator" {
					t.Fatalf("expected synthetic thoughtSignature, got %v", ts)
				}
			}
		}
	}
	if !found {
		t.Fatalf("functionCall not found in outbound: %+v", contents)
	}
}

func TestGeminiHTTPError(t *testing.T) {
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"too fast"}`))
	})
	_, err := adapter.Complete(context.Background(), nil, nil, agent.CompleteOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected 429 error, got %v", err)
	}
}
