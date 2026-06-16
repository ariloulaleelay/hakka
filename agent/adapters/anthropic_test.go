package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/you/hakka/agent"
)

func newAnthropicTestAdapter(t *testing.T, handler http.HandlerFunc) *AnthropicAdapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewAnthropicAdapter(srv.Client(), srv.URL, "test-model")
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
		}, nil, agent.CompleteOptions{})
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
		}, []agent.ToolSchema{{Name: "add", Parameters: map[string]any{"type": "object"}}}, agent.CompleteOptions{})
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
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		emit := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if f != nil {
				f.Flush()
			}
		}
		emit(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`)
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`)
		emit(`{"type":"message_stop"}`)
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

func TestAnthropicStreamToolCall(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"add","input":{}}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
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

// TestAnthropicStreamToolCallWithInputJSONDelta reproduces the bug where
// incremental input_json_delta events carrying tool arguments are silently
// dropped. The Anthropic API sends:
//
//   content_block_start  → {type:"tool_use", id:"toolu_1", name:"read_file", input:{}}
//   content_block_delta  → {type:"input_json_delta", partial_json:"{\"path\": \""}
//   content_block_delta  → {type:"input_json_delta", partial_json:"agent/file.go\"}"}
//   content_block_stop
//   message_delta        → {stop_reason:"tool_use"}
//   message_stop
//
// The bug: only content_block_start is captured (input:{}), the
// input_json_delta fragments are dropped, so the tool call ends up with
// Arguments="{}" instead of '{"path": "agent/file.go"}'.
func TestAnthropicStreamToolCallWithInputJSONDelta(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		emit := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if f != nil {
				f.Flush()
			}
		}

		// 1. content_block_start: tool_use with empty input stub
		emit(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`)

		// 2. First input_json_delta: partial JSON fragment
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \""}}`)

		// 3. Second input_json_delta: more JSON
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"agent/file.go\"}"}}`)

		// 4. message_delta with stop_reason
		emit(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":5}}`)

		// 5. message_stop
		emit(`{"type":"message_stop"}`)
	})

	resultCh, err := adapter.Stream(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "read the file"}},
		[]agent.ToolSchema{{Name: "read_file", Parameters: map[string]any{"type": "object"}}},
		agent.CompleteOptions{})
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

	if len(toolCalls) != 1 {
		t.Fatalf("expected exactly 1 tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0]
	if tc.ID != "toolu_1" {
		t.Errorf("expected ID 'toolu_1', got %q", tc.ID)
	}
	if tc.Name != "read_file" {
		t.Errorf("expected Name 'read_file', got %q", tc.Name)
	}
	if tc.Arguments != `{"path": "agent/file.go"}` {
		t.Errorf("expected Arguments '{\"path\": \"agent/file.go\"}', got %q", tc.Arguments)
	}

	// Verify it's valid JSON with the expected value
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["path"] != "agent/file.go" {
		t.Fatalf("expected path=agent/file.go, got %v", args)
	}
}

// TestAnthropicStreamToolCallMultipleInputJSONDeltas verifies that
// multiple concurrent tool calls with their own input_json_delta
// fragments are accumulated independently by block index.
func TestAnthropicStreamToolCallMultipleInputJSONDeltas(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		emit := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if f != nil {
				f.Flush()
			}
		}

		// Two tool calls start at the same time (index 0 and 1)
		emit(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"get_weather","input":{}}}`)
		emit(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"get_time","input":{}}}`)

		// Arguments for both, interleaved
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\": \""}}`)
		emit(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\": \""}}`)
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"Paris\"}"}}`)
		emit(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"UTC\"}"}}`)

		emit(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":20,"output_tokens":10}}`)
		emit(`{"type":"message_stop"}`)
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

	if len(toolCalls) != 2 {
		t.Fatalf("expected exactly 2 tool calls, got %d", len(toolCalls))
	}

	// First tool call: get_weather (index 0, should come first)
	tc0 := toolCalls[0]
	if tc0.ID != "toolu_a" || tc0.Name != "get_weather" {
		t.Errorf("tool call 0: ID=%q Name=%q", tc0.ID, tc0.Name)
	}
	if tc0.Arguments != `{"city": "Paris"}` {
		t.Errorf("tool call 0 args: got %q", tc0.Arguments)
	}

	// Second tool call: get_time (index 1)
	tc1 := toolCalls[1]
	if tc1.ID != "toolu_b" || tc1.Name != "get_time" {
		t.Errorf("tool call 1: ID=%q Name=%q", tc1.ID, tc1.Name)
	}
	if tc1.Arguments != `{"tz": "UTC"}` {
		t.Errorf("tool call 1 args: got %q", tc1.Arguments)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	adapter := newAnthropicTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	})
	_, err := adapter.Complete(context.Background(), nil, nil, agent.CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 error, got %v", err)
	}
}
