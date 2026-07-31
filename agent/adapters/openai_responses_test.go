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

	"github.com/ariloulaleelay/hakka/agent"
)

func newResponsesTestAdapter(t *testing.T, handler http.HandlerFunc) (*OpenAIResponsesAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	adapter := NewOpenAIResponsesAdapter(
		&http.Client{},
		srv.URL,
		"test-model",
		NewOpenAIResponsesConfig("", agent.RetryConfig{}, agent.Pricing{}),
	)
	return adapter, srv
}

// ===== Non-streaming tests =====

func TestResponsesCompleteText(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"test-model"`) {
			t.Errorf("model not in request: %s", body)
		}
		if !strings.Contains(string(body), `"store":false`) {
			t.Errorf("store not false in request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"type": "message",
					"id": "msg_123",
					"role": "assistant",
					"content": [
						{"type": "output_text", "text": "hi back", "annotations": []}
					]
				}
			],
			"usage": {
				"input_tokens": 10,
				"output_tokens": 5,
				"total_tokens": 15
			}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "hi back" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Fatalf("prompt tokens: got %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Fatalf("completion tokens: got %d, want 5", resp.Usage.CompletionTokens)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish reason: %q", resp.FinishReason)
	}
}

func TestResponsesCompleteToolCall(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"type": "function_call",
					"id": "fc_123",
					"call_id": "call_unLAR8MvFNptuiZK6K6HCy5k",
					"name": "add",
					"arguments": "{\"a\":1,\"b\":2}",
					"status": "completed"
				}
			],
			"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "add 1+2"}},
		[]agent.ToolSchema{{Name: "add", Description: "Add two numbers"}},
		agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "add" {
		t.Fatalf("name: %q", tc.Name)
	}
	if tc.ID != "call_unLAR8MvFNptuiZK6K6HCy5k" {
		t.Fatalf("id: %q", tc.ID)
	}
	var args map[string]float64
	_ = json.Unmarshal([]byte(tc.Arguments), &args)
	if args["a"] != 1 || args["b"] != 2 {
		t.Fatalf("args: %+v", args)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish reason: %q", resp.FinishReason)
	}
}

func TestResponsesCompleteRetries429(t *testing.T) {
	var requests int32
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"type": "message",
					"id": "msg_123",
					"role": "assistant",
					"content": [
						{"type": "output_text", "text": "recovered", "annotations": []}
					]
				}
			],
			"usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
		}`))
	})

	// Set retry config (default is zero = no retry via doJSONPost)
	adapter.Config = NewOpenAIResponsesConfig(
		"",
		agent.DefaultRetryConfig(),
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

func TestResponsesCompleteInstructions(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		instructions, _ := req["instructions"].(string)
		if instructions != "You are helpful" {
			t.Errorf("instructions: %q", instructions)
		}
		// input should NOT contain the system message
		input, _ := json.Marshal(req["input"])
		if strings.Contains(string(input), "You are helpful") {
			t.Errorf("input should not contain instructions: %s", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [{"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "ok", "annotations": []}]}],
			"usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleSystem, Content: "You are helpful"},
			{Role: agent.RoleUser, Content: "hi"},
		},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
}

func TestResponsesCompleteToolResults(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		input, _ := json.Marshal(req["input"])
		inputStr := string(input)
		if !strings.Contains(inputStr, "function_call_output") {
			t.Errorf("input should contain function_call_output: %s", inputStr)
		}
		if !strings.Contains(inputStr, "call_42") {
			t.Errorf("input should contain call_id: %s", inputStr)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [{"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "got it", "annotations": []}]}],
			"usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{
			{Role: agent.RoleUser, Content: "hi"},
			{Role: agent.RoleAssistant, Content: "", ToolCalls: []agent.ToolCall{{ID: "call_42", Name: "echo", Arguments: "{}"}}},
			{Role: agent.RoleTool, Content: "result", ToolCallID: "call_42"},
		},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "got it" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
}

func TestResponsesReasoning(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"type": "reasoning",
					"id": "rs_123",
					"summary": [{"type": "summary_text", "text": "I need to think about this..."}]
				},
				{
					"type": "message",
					"id": "msg_123",
					"role": "assistant",
					"content": [
						{"type": "output_text", "text": "answer", "annotations": []}
					]
				}
			],
			"usage": {"input_tokens": 5, "output_tokens": 10, "total_tokens": 15, "output_tokens_details": {"reasoning_tokens": 7}}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "complex question"}},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "answer" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	rc, ok := resp.Message.ProviderMetadata["reasoning_content"].(string)
	if !ok || rc != "I need to think about this..." {
		t.Fatalf("reasoning_content: %q", rc)
	}
}

// ===== Streaming tests =====

func makeResponsesSSELines(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e)
		b.WriteString("\n")
	}
	return b.String()
}

func (ad *OpenAIResponsesAdapter) testParseStream(t *testing.T, body string, wantText string, wantToolCount int) *agent.LLMResponse {
	t.Helper()
	var gotDeltas []string
	resp, err := ad.parseStream(strings.NewReader(body), func(d string) {
		gotDeltas = append(gotDeltas, d)
	})
	if err != nil {
		t.Fatalf("parseStream: %v", err)
	}
	if resp.Message.Content != wantText {
		t.Fatalf("content: %q, want %q", resp.Message.Content, wantText)
	}
	if len(resp.Message.ToolCalls) != wantToolCount {
		t.Fatalf("tool calls: %d, want %d: %+v", len(resp.Message.ToolCalls), wantToolCount, resp.Message.ToolCalls)
	}
	if got := strings.Join(gotDeltas, ""); got != wantText {
		t.Fatalf("deltas join: %q, want %q", got, wantText)
	}
	return resp
}

func TestResponsesStreamText(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":1,"delta":" world"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":5,"output_tokens":10,"total_tokens":15}}}`,
		`data: [DONE]`,
	)

	resp := adapter.testParseStream(t, body, "hello world", 0)
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish: %q", resp.FinishReason)
	}
}

func TestResponsesStreamToolCall(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc123","name":"add","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"a\":"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"1,\"b\":2}"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`,
		`data: [DONE]`,
	)

	resp := adapter.testParseStream(t, body, "", 1)
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Fatalf("call_id: %q", tc.ID)
	}
	if tc.Name != "add" {
		t.Fatalf("name: %q", tc.Name)
	}
	if tc.Arguments != `{"a":1,"b":2}` {
		t.Fatalf("args: %q", tc.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish: %q (expected tool_calls)", resp.FinishReason)
	}
}

func TestResponsesStreamParallelToolCalls(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		// Two function calls at indices 0 and 1
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_a","type":"function_call","call_id":"call_a","name":"echo","arguments":""}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_b","type":"function_call","call_id":"call_b","name":"add","arguments":""}}`,
		// Arguments for call_a (index 0)
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":0,"delta":"{\"msg\":\"hi\"}"}`,
		// Arguments for call_b (index 1)
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_b","output_index":1,"delta":"{\"a\":1}"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		`data: [DONE]`,
	)

	resp := adapter.testParseStream(t, body, "", 2)
	if resp.Message.ToolCalls[0].Name != "echo" {
		t.Fatalf("tool 0 name: %q", resp.Message.ToolCalls[0].Name)
	}
	if resp.Message.ToolCalls[1].Name != "add" {
		t.Fatalf("tool 1 name: %q", resp.Message.ToolCalls[1].Name)
	}
}

func TestResponsesStreamError(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"error","code":"invalid_request_error","message":"bad input"}`,
	)

	_, err := adapter.parseStream(strings.NewReader(body), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stream error") {
		t.Fatalf("error: %v", err)
	}
}

func TestResponsesStreamInterleavedEventLines(t *testing.T) {
	// SSE may include event: lines; we skip them and use the type in data payload.
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
		`data: [DONE]`,
	}, "\n")

	resp := adapter.testParseStream(t, body, "ok", 0)
	if resp.Usage == nil || resp.Usage.TotalTokens != 2 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestResponsesStreamUsageCachedTokens(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"response.output_text.delta","delta":"x"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":80}}}}`,
		`data: [DONE]`,
	)

	resp := adapter.testParseStream(t, body, "x", 0)
	if resp.Usage.PromptCacheHitTokens != 80 {
		t.Fatalf("cached tokens: %d", resp.Usage.PromptCacheHitTokens)
	}
	if resp.Usage.PromptTokens != 100 {
		t.Fatalf("prompt tokens: %d", resp.Usage.PromptTokens)
	}
}

func TestResponsesStreamReasoning(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"content_index":0,"delta":"I need"}`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"content_index":1,"delta":" to think"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":5,"output_tokens":10,"total_tokens":15}}}`,
		`data: [DONE]`,
	)

	resp, err := adapter.parseStream(strings.NewReader(body), func(d string) {})
	if err != nil {
		t.Fatalf("parseStream: %v", err)
	}
	if resp.Message.Content != "answer" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	rc, ok := resp.Message.ProviderMetadata["reasoning_content"].(string)
	if !ok || rc != "I need to think" {
		t.Fatalf("reasoning_content: %q", rc)
	}
}

func TestResponsesStreamEmptyArgsNormalized(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	body := makeResponsesSSELines(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_x","name":"noargs","arguments":""}}`,
		// No argument deltas at all
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		`data: [DONE]`,
	)

	resp := adapter.testParseStream(t, body, "", 1)
	if resp.Message.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("empty args not normalized: %q", resp.Message.ToolCalls[0].Arguments)
	}
}

// ===== Cost tests =====

func TestResponsesCompleteCost(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"model": "test-model",
			"status": "completed",
			"output": [{"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "hi", "annotations": []}]}],
			"usage": {"input_tokens": 1000, "output_tokens": 500, "total_tokens": 1500, "input_tokens_details": {"cached_tokens": 200}}
		}`))
	})

	// Pricing: $0.01 per 1K input, $0.03 per 1K output, $0.005 per 1K cached
	adapter.Config = NewOpenAIResponsesConfig(
		"",
		agent.RetryConfig{},
		agent.Pricing{Input: 0.01, Output: 0.03, CacheHitInput: 0.005},
	)

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	// Expected: 800 uncached prompt * 0.01/1000 + 200 cached * 0.005/1000 + 500 output * 0.03/1000
	// = 0.008 + 0.001 + 0.015 = 0.024
	if resp.Usage.Cost <= 0 {
		t.Fatalf("cost should be > 0, got %f", resp.Usage.Cost)
	}
	if resp.Usage.PromptCacheHitTokens != 200 {
		t.Fatalf("cached tokens: got %d, want 200", resp.Usage.PromptCacheHitTokens)
	}
}

func TestResponsesCompleteCostNoPricing(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"model": "test-model",
			"status": "completed",
			"output": [{"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "hi", "annotations": []}]}],
			"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
		}`))
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Usage.Cost != 0 {
		t.Fatalf("cost should be 0 without pricing, got %f", resp.Usage.Cost)
	}
}

func TestResponsesStreamCost(t *testing.T) {
	adapter, _ := newResponsesTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected HTTP call")
	})

	adapter.Config = NewOpenAIResponsesConfig(
		"",
		agent.RetryConfig{},
		agent.Pricing{Input: 0.01, Output: 0.03, CacheHitInput: 0.005},
	)

	body := makeResponsesSSELines(
		`data: {"type":"response.output_text.delta","delta":"x"}`,
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500,"input_tokens_details":{"cached_tokens":200}}}}`,
		`data: [DONE]`,
	)

	resp, err := adapter.parseStream(strings.NewReader(body), func(d string) {})
	if err != nil {
		t.Fatalf("parseStream: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	if resp.Usage.Cost <= 0 {
		t.Fatalf("streaming cost should be > 0, got %f", resp.Usage.Cost)
	}
	if resp.Usage.PromptCacheHitTokens != 200 {
		t.Fatalf("cached tokens: got %d, want 200", resp.Usage.PromptCacheHitTokens)
	}
}

// ===== Conversion unit tests =====

func TestToResponsesInstructions(t *testing.T) {
	tests := []struct {
		name    string
		msgs    []agent.Message
		want    string
	}{
		{
			name: "no system",
			msgs: []agent.Message{{Role: agent.RoleUser, Content: "hi"}},
			want: "",
		},
		{
			name: "single system",
			msgs: []agent.Message{
				{Role: agent.RoleSystem, Content: "Be helpful"},
				{Role: agent.RoleUser, Content: "hi"},
			},
			want: "Be helpful",
		},
		{
			name: "multiple systems",
			msgs: []agent.Message{
				{Role: agent.RoleSystem, Content: "First"},
				{Role: agent.RoleSystem, Content: "Second"},
				{Role: agent.RoleUser, Content: "hi"},
			},
			want: "First\n\nSecond",
		},
		{
			name: "developer role",
			msgs: []agent.Message{
				{Role: "developer", Content: "You are dev"},
				{Role: agent.RoleUser, Content: "hi"},
			},
			want: "You are dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toResponsesInstructions(tt.msgs)
			if got != tt.want {
				t.Fatalf("instructions: %q, want %q", got, tt.want)
			}
		})
	}
}
