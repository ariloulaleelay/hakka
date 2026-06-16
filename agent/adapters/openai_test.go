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

	openai "github.com/sashabaranov/go-openai"

	"github.com/you/hakka/agent"
)

func newOpenAITestAdapter(t *testing.T, handler http.HandlerFunc) (*OpenAIAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = srv.URL
	return NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model"), srv
}

func TestOpenAICompleteText(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"test-model"`) {
			t.Errorf("model not in request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"hi back"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
		}`))
	})
	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "hi back" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestOpenAICompleteRetries429(t *testing.T) {
	var requests int32
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	})

	resp, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.CompleteOptions{})
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

func TestOpenAICompleteToolCall(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
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
		[]agent.ToolSchema{{Name: "add"}}, agent.CompleteOptions{})
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

func TestOpenAIStream(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, frag := range []string{"hel", "lo"} {
			payload := map[string]any{
				"choices": []any{
					map[string]any{
						"delta": map[string]any{"content": frag},
						"finish_reason": nil,
					},
				},
			}
			b, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		// Final frame with finish_reason
		payload := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{"content": ""},
					"finish_reason": "stop",
				},
			},
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
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

func TestOpenAIStreamToolCall(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id": "x",
								"function": map[string]any{
									"name": "add",
									"arguments": "{}",
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	resultCh, err := adapter.Stream(context.Background(), nil, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	var toolCalls []any
	for s := range resultCh {
		if len(s.ToolCalls) > 0 {
			toolCalls = append(toolCalls, s.ToolCalls)
		}
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}
	if len(toolCalls) == 0 {
		t.Fatal("expected tool calls in stream result")
	}
}
