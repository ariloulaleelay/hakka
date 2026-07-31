package adapters

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

func newAnthropicTestAdapter(t *testing.T, handler http.HandlerFunc) *AnthropicAdapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewAnthropicAdapter(srv.Client(), srv.URL, "test-model", NewAnthropicConfig("", agent.RetryConfig{}, agent.Pricing{}, "2023-06-01", 0))
}

func TestAnthropicCompleteText(t *testing.T) {
	var captured map[string]any
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1","role":"assistant",
			"content":[{"type":"text","text":"hello there"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":4,"output_tokens":5}
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
	if captured["system"] != "be brief" {
		t.Fatalf("system not forwarded: %v", captured["system"])
	}
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestAnthropicToolRoundTrip(t *testing.T) {
	var seenHistory []map[string]any
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			seenHistory = append(seenHistory, m.(map[string]any))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1","role":"assistant",
			"content":[{"type":"tool_use","id":"toolu_1","name":"add","input":{"a":1,"b":2}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "compute 1+2"},
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "toolu_prev", Name: "add", Arguments: `{"a":1,"b":2}`}}},
			{Role: agent.RoleTool, ToolCallID: "toolu_prev", Content: `{"sum":3}`},
		}, []agent.ToolSchema{{Name: "add", Parameters: map[string]any{"type": "object"}}}, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "add" {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.Message.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("id: %q", resp.Message.ToolCalls[0].ID)
	}

	foundToolResult := false
	for _, m := range seenHistory {
		if m["role"] != "user" {
			continue
		}
		blocks, _ := m["content"].([]any)
		for _, b := range blocks {
			bm := b.(map[string]any)
			if bm["type"] == "tool_result" && bm["tool_use_id"] == "toolu_prev" {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Fatalf("tool_result block missing in outbound: %+v", seenHistory)
	}
}

func TestAnthropicStream(t *testing.T) {
	var reqBody map[string]any
	var deltas []string
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"id":"msg_1","role":"assistant","usage":{"input_tokens":10,"output_tokens":0}}}`)
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if reqBody["stream"] != true {
		t.Fatalf("stream not set in request: %v", reqBody["stream"])
	}
	if resp.Message.Content != "Hello world" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Fatalf("deltas: %q", got)
	}
	if resp.FinishReason != "end_turn" {
		t.Fatalf("finish_reason: %q", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestAnthropicStreamToolCall(t *testing.T) {
	var toolCalls []agent.ToolCall
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"usage":{"input_tokens":8,"output_tokens":0}}}`)
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"add","input":{}}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1,\"b\":2}"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":12}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "add"}},
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	toolCalls = resp.Message.ToolCalls
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls: %+v", toolCalls)
	}
	tc := toolCalls[0]
	if tc.Name != "add" || tc.ID != "toolu_1" {
		t.Fatalf("tool call: %+v", tc)
	}
	var args map[string]float64
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	if args["a"] != 1 || args["b"] != 2 {
		t.Fatalf("args: %+v", args)
	}
}

// TestAnthropicStreamToolCallWithInputJSONDelta reproduces the bug where
// incremental input_json_delta events carrying tool arguments are silently
// dropped. The Anthropic API sends:
//
//	content_block_start  → {type:"tool_use", id:"toolu_1", name:"read_file", input:{}}
//	content_block_delta  → {type:"input_json_delta", partial_json:"{\"path\": \""}
//	content_block_delta  → {type:"input_json_delta", partial_json:"agent/file.go\"}"}
//	content_block_stop
//	message_delta        → {stop_reason:"tool_use"}
//	message_stop
//
// The bug: only content_block_start is captured (input:{}), the
// input_json_delta fragments are dropped, so the tool call ends up with
// Arguments="{}" instead of '{"path": "agent/file.go"}'.
func TestAnthropicStreamToolCallWithInputJSONDelta(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`)
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \""}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"agent/file.go\"}"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":7}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "read file"}},
		[]agent.ToolSchema{{Name: "read_file"}}, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Arguments != `{"path": "agent/file.go"}` {
		t.Fatalf("args: %q, want {\"path\": \"agent/file.go\"}", tc.Arguments)
	}
}

// TestAnthropicStreamToolCallMultipleInputJSONDeltas verifies that
// multiple concurrent tool calls with their own input_json_delta
// fragments are accumulated independently by block index.
func TestAnthropicStreamToolCallMultipleInputJSONDeltas(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`)
		// tool_use 0
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_0","name":"get_weather","input":{}}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		// tool_use 1
		writeSSE(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_time","input":{}}}`)
		writeSSE(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`)
		writeSSE(`{"type":"content_block_stop","index":1}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":12}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "weather and time"}},
		[]agent.ToolSchema{{Name: "get_weather"}, {Name: "get_time"}}, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	first := resp.Message.ToolCalls[0]
	if first.Name != "get_weather" || first.Arguments != `{"city":"Paris"}` {
		t.Fatalf("first: %+v", first)
	}
	second := resp.Message.ToolCalls[1]
	if second.Name != "get_time" || second.Arguments != `{"tz":"UTC"}` {
		t.Fatalf("second: %+v", second)
	}
}

func TestAnthropicCompleteRetries429(t *testing.T) {
	var requests int32
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1","role":"assistant",
			"content":[{"type":"text","text":"recovered"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	})

	adapter.Config = NewAnthropicConfig(
		adapter.Config.DebugDir(),
		agent.RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Millisecond,
			MaxDelay:      50 * time.Millisecond,
			BackoffFactor: 1.0,
		},
		agent.Pricing{},
		adapter.Config.Version,
		adapter.Config.MaxTokens,
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

func TestAnthropicStreamRetries429(t *testing.T) {
	var requests int32
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`)
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"recovered"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	adapter.Config = NewAnthropicConfig(
		adapter.Config.DebugDir(),
		agent.RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Millisecond,
			MaxDelay:      50 * time.Millisecond,
			BackoffFactor: 1.0,
		},
		agent.Pricing{},
		adapter.Config.Version,
		adapter.Config.MaxTokens,
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

func TestAnthropicStreamErrorEvent(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
	})
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{},
		func(string) {})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("expected overloaded error, got %v", err)
	}
}

// TestAnthropicStreamCostUsesFullUsage verifies that streamed cost is
// computed from BOTH input (message_start) and output (message_delta)
// tokens — the message_delta event only carries output_tokens, so pricing
// computed there alone would undercount.
func TestAnthropicStreamCostUsesFullUsage(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(payload string) { _, _ = w.Write([]byte("data: " + payload + "\n\n")) }
		writeSSE(`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}`)
		writeSSE(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		writeSSE(`{"type":"content_block_stop","index":0}`)
		writeSSE(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":50}}`)
		writeSSE(`{"type":"message_stop"}`)
	})

	adapter.Config = NewAnthropicConfig(
		adapter.Config.DebugDir(),
		adapter.Config.RetryPolicy(),
		agent.Pricing{Input: 1e-6, Output: 2e-6},
		adapter.Config.Version,
		adapter.Config.MaxTokens,
	)

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{},
		func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// 100 input * 1e-6 + 50 output * 2e-6 = 1e-4 + 1e-4 = 2e-4
	want := 100*1e-6 + 50*2e-6
	if math.Abs(resp.Usage.Cost-want) > 1e-12 {
		t.Fatalf("cost: got %v, want %v (usage %+v)", resp.Usage.Cost, want, resp.Usage)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	})
	_, err := adapter.Complete(context.Background(), nil, nil, agent.CompleteOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 error, got %v", err)
	}
}
