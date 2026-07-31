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

func newDeepSeekTestAdapter(t *testing.T, handler http.HandlerFunc) *DeepSeekAdapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewDeepSeekAdapter(srv.Client(), srv.URL, "test-model", NewDeepSeekConfig("", agent.RetryConfig{}, agent.Pricing{}))
}

func TestDeepSeekCompleteText(t *testing.T) {
	var captured map[string]any
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path: got %q, want /chat/completions", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}
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
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	// System prompt must be forwarded as a system message.
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages: %+v", captured["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("system not forwarded: %+v", first)
	}
	if captured["model"] != "test-model" {
		t.Fatalf("model: %v", captured["model"])
	}
}

func TestDeepSeekCompleteToolCall(t *testing.T) {
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":1,\"b\":2}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "add"}},
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "add" || tc.ID != "call_1" {
		t.Fatalf("tool call: %+v", tc)
	}
	var args map[string]float64
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	if args["a"] != 1 || args["b"] != 2 {
		t.Fatalf("args: %+v", args)
	}
}

func TestDeepSeekCompleteReasoningContent(t *testing.T) {
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"final answer","reasoning_content":"thinking..."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	rc, _ := resp.Message.ProviderMetadata["reasoning_content"].(string)
	if rc != "thinking..." {
		t.Fatalf("reasoning_content: %q", rc)
	}
}

// TestDeepSeekSyntheticReasoningForProviderSwitch verifies that an assistant
// message with tool calls but NO ProviderMetadata (e.g. it was produced by
// another provider like Claude) still gets a reasoning_content field — an
// empty string backup — because DeepSeek 400s on tool-call turns that omit
// the field entirely.
func TestDeepSeekSyntheticReasoningForProviderSwitch(t *testing.T) {
	var captured map[string]any
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"call_2","type":"function","function":{"name":"add","arguments":"{\"a\":1,\"b\":2}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{}
		}`))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "compute 1+2"},
			// Assistant turn from another provider: tool calls, no metadata.
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call_prev", Name: "add", Arguments: `{"a":1,"b":2}`}}},
			{Role: agent.RoleTool, ToolCallID: "call_prev", Content: `{"sum":3}`},
		}, []agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	msgs, _ := captured["messages"].([]any)
	var seen bool
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		seen = true
		rc, ok := mm["reasoning_content"]
		if !ok {
			t.Fatalf("assistant tool-call turn missing reasoning_content: %+v", mm)
		}
		if rc != "" {
			t.Fatalf("expected synthetic empty reasoning_content, got %q", rc)
		}
	}
	if !seen {
		t.Fatalf("no assistant message in outbound: %+v", msgs)
	}
}

// TestDeepSeekReplaysStoredReasoning verifies that reasoning_content captured
// on an earlier DeepSeek tool-call turn is passed back verbatim.
func TestDeepSeekReplaysStoredReasoning(t *testing.T) {
	var captured map[string]any
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{}
		}`))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "compute 1+2"},
			{
				Role:             agent.RoleAssistant,
				ToolCalls:        []agent.ToolCall{{ID: "call_prev", Name: "add", Arguments: `{"a":1,"b":2}`}},
				ProviderMetadata: map[string]any{"reasoning_content": "step by step"},
			},
			{Role: agent.RoleTool, ToolCallID: "call_prev", Content: `{"sum":3}`},
		}, []agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	msgs, _ := captured["messages"].([]any)
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		rc, ok := mm["reasoning_content"].(string)
		if !ok || rc != "step by step" {
			t.Fatalf("stored reasoning_content not replayed: %+v", mm)
		}
	}
}

// TestDeepSeekOmitsReasoningForPlainAssistant verifies that assistant turns
// without tool calls do NOT carry reasoning_content — DeepSeek ignores it
// there, and omitting it saves context.
func TestDeepSeekOmitsReasoningForPlainAssistant(t *testing.T) {
	var captured map[string]any
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{}
		}`))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "hello"},
			{
				Role:             agent.RoleAssistant,
				Content:          "previous reply",
				ProviderMetadata: map[string]any{"reasoning_content": "old chain of thought"},
			},
			{Role: agent.RoleUser, Content: "again"},
		}, nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	msgs, _ := captured["messages"].([]any)
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		if _, ok := mm["reasoning_content"]; ok {
			t.Fatalf("reasoning_content should be omitted for non-tool assistant turn: %+v", mm)
		}
	}
}

// TestDeepSeekMergesExtraAndPerReqExtras verifies that static config Extra
// (e.g. thinking) and per-request Extra (e.g. reasoning_effort) are merged
// into the outbound JSON body via the instrumented transport.
func TestDeepSeekMergesExtraAndPerReqExtras(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()

	transport := WrapTransport(srv.Client().Transport, map[string]any{"thinking": map[string]any{"type": "enabled"}}, "")
	client := &http.Client{Transport: transport}
	adapter := NewDeepSeekAdapter(client, srv.URL, "test-model", NewDeepSeekConfig("", agent.RetryConfig{}, agent.Pricing{}))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil,
		agent.CompleteOptions{Extra: map[string]any{"reasoning_effort": "high"}}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	thinking, _ := captured["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking not merged: %+v", captured["thinking"])
	}
	if captured["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort not merged: %+v", captured["reasoning_effort"])
	}
}

func TestDeepSeekStreamTextAndReasoning(t *testing.T) {
	var deltas []string
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["stream"] != true {
			t.Errorf("stream not set: %v", req["stream"])
		}
		if req["stream_options"] == nil {
			t.Errorf("stream_options missing: %v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"choices":[{"delta":{"reasoning_content":"think"}}]}`)
		writeSSE(`{"choices":[{"delta":{"content":"hel"}}]}`)
		writeSSE(`{"choices":[{"delta":{"content":"lo"}}]}`)
		writeSSE(`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":2}}`)
		writeSSE("[DONE]")
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{},
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Message.Content != "hello" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if got := strings.Join(deltas, ""); got != "hello" {
		t.Fatalf("deltas: %q", got)
	}
	rc, _ := resp.Message.ProviderMetadata["reasoning_content"].(string)
	if rc != "think" {
		t.Fatalf("streamed reasoning_content: %q", rc)
	}
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if resp.Usage.PromptCacheHitTokens != 2 || resp.Usage.PromptCacheMissTokens != 2 {
		t.Fatalf("cache usage: %+v", resp.Usage)
	}
}

func TestDeepSeekStreamToolCall(t *testing.T) {
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"add","arguments":""}}]}}]}`)
		writeSSE(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1"}}]}}]}`)
		writeSSE(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"b\":2}"}}]}}]}`)
		writeSSE(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		writeSSE("[DONE]")
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
	if tc.Name != "add" || tc.ID != "call_1" {
		t.Fatalf("tool call: %+v", tc)
	}
	var args map[string]float64
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	if args["a"] != 1 || args["b"] != 2 {
		t.Fatalf("args: %+v", args)
	}
}

func TestDeepSeekCompleteRetries429(t *testing.T) {
	var requests int32
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	})

	adapter.Config = NewDeepSeekConfig(
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

func TestDeepSeekStreamRetries429(t *testing.T) {
	var requests int32
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"recovered"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	adapter.Config = NewDeepSeekConfig(
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

func TestDeepSeekHTTPError(t *testing.T) {
	adapter := newDeepSeekTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	})
	_, err := adapter.Complete(context.Background(), nil, nil, agent.CompleteOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 error, got %v", err)
	}
}

var _ agent.LLMAdapter = (*DeepSeekAdapter)(nil)
