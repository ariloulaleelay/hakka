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

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

func newOpenAITestAdapter(t *testing.T, handler http.HandlerFunc) (*OpenAIAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = srv.URL
	return NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig("", agent.RetryConfig{}, agent.Pricing{}, nil)), srv
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
		nil, agent.CompleteOptions{}, nil)
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

func TestOpenAIMessagesSkipsEmptyAssistant(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleAssistant, Content: ""}, // empty content, no tool_calls — OpenAI 400 error
		{Role: agent.RoleUser, Content: "world"},
	}
	out := toOpenAIMessages(msgs)
	// The empty assistant message should be filtered out, consistent with
	// how Anthropic's appendAssistantBlocks and Gemini's appendGeminiModelParts
	// skip messages with no text and no tool calls.
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (empty assistant skipped), got %d", len(out))
	}
	if out[0].Role != string(agent.RoleUser) || out[0].Content != "hello" {
		t.Fatalf("first message: %+v", out[0])
	}
	if out[1].Role != string(agent.RoleUser) || out[1].Content != "world" {
		t.Fatalf("second message: %+v", out[1])
	}
	// No assistant message should have empty content AND no tool calls
	for _, m := range out {
		if m.Role == string(agent.RoleAssistant) && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatal("assistant message with empty content and no tool_calls would cause OpenAI 400 error")
		}
	}
}

func TestOpenAICompleteRetriesCustomConfig(t *testing.T) {
	var requests int32
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&requests, 1)
		if attempt < 3 {
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

	// Use a custom retry config with 3 max attempts, tiny delays
	adapter.Config = NewOpenAIConfig(
		adapter.Config.DebugDir(),
		agent.RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Millisecond,
			MaxDelay:      50 * time.Millisecond,
			BackoffFactor: 1.0,
		},
		agent.Pricing{},
		nil,
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
		t.Fatalf("expected 3 requests (max_attempts=3), got %d", got)
	}
}
