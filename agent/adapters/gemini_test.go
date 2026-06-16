package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

func newGeminiTestAdapter(t *testing.T, handler http.HandlerFunc) *GeminiAdapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewGeminiAdapter(srv.Client(), srv.URL, "test-model")
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
		}, nil, agent.CompleteOptions{})
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
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{})
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
		}, []agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{})
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
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "alt=sse") {
			t.Errorf("missing alt=sse: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		emit := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if f != nil {
				f.Flush()
			}
		}
		emit(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}`)
		emit(`{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]}}]}`)
	})
	resultCh, err := adapter.Stream(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	var buf strings.Builder
	for s := range resultCh {
		buf.WriteString(s.Delta)
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}
	if buf.String() != "hello" {
		t.Fatalf("buf: %q", buf.String())
	}
}

func TestGeminiStreamToolCall(t *testing.T) {
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"add","args":{}}}]},"finishReason":"STOP"}]}` + "\n\n"))
	})
	resultCh, err := adapter.Stream(context.Background(), nil, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	var toolCalls []agent.ToolCall
	for s := range resultCh {
		if len(s.ToolCalls) > 0 {
			toolCalls = s.ToolCalls
		}
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}
	if len(toolCalls) == 0 {
		t.Fatal("expected tool calls in stream result")
	}
	if toolCalls[0].Name != "add" {
		t.Fatalf("expected tool 'add', got %q", toolCalls[0].Name)
	}
}

func TestGeminiHTTPError(t *testing.T) {
	adapter := newGeminiTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"too fast"}`))
	})
	_, err := adapter.Complete(context.Background(), nil, nil, agent.CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected 429 error, got %v", err)
	}
}
