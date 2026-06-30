package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// TestOpenAIStreamCapturesUsageAfterFinishReason reproduces the bug where
// OpenAI's streaming response with `include_usage: true` sends usage in a
// separate SSE event AFTER the finish_reason chunk — but the current code
// returns as soon as it sees finish_reason, never reading the usage event.
//
// OpenAI SSE stream with include_usage:
//
//	data: {"choices":[{"delta":{"content":"Hello"}}]}
//	data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
//	data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}
//	data: [DONE]
//
// The usage event is the third line — it comes AFTER finish_reason. The
// Stream() goroutine must NOT return on finish_reason; it must continue
// reading until io.EOF to capture the final usage event.
func TestOpenAIStreamCapturesUsageAfterFinishReason(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		emit := func(payload map[string]any) {
			b, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}

		// Chunk 1: content delta
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         map[string]any{"content": "Hello"},
					"finish_reason": nil,
				},
			},
		})

		// Chunk 2: finish_reason (empty delta)
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			},
		})

		// Chunk 3: usage event (no choices, just usage)
		emit(map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})

		// [DONE] marker
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})

	resultCh, err := adapter.Stream(context.Background(), nil, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}

	var textBuf strings.Builder
	var capturedUsage *agent.Usage
	for s := range resultCh {
		textBuf.WriteString(s.Delta)
		if s.Usage != nil {
			capturedUsage = s.Usage
		}
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}

	if textBuf.String() != "Hello" {
		t.Errorf("expected text 'Hello', got %q", textBuf.String())
	}

	if capturedUsage == nil {
		t.Fatal("BUG: usage was NOT captured — Stream() returned before reading the usage SSE event")
	}
	if capturedUsage.TotalTokens != 15 {
		t.Errorf("expected TotalTokens=15, got %d", capturedUsage.TotalTokens)
	}
	if capturedUsage.PromptTokens != 10 {
		t.Errorf("expected PromptTokens=10, got %d", capturedUsage.PromptTokens)
	}
	if capturedUsage.CompletionTokens != 5 {
		t.Errorf("expected CompletionTokens=5, got %d", capturedUsage.CompletionTokens)
	}
}

// TestOpenAIStreamToolCallsCapturesUsageAfterFinishReason is the same test
// but for tool_calls finish_reason — the usage SSE event also comes after
// the tool_calls finish_reason chunk.
func TestOpenAIStreamToolCallsCapturesUsageAfterFinishReason(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		emit := func(payload map[string]any) {
			b, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}

		// Chunk with tool_calls + finish_reason
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_1",
								"type":  "function",
								"function": map[string]any{
									"name":      "get_weather",
									"arguments": `{"city":"Paris"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
		})

		// Usage event (comes AFTER finish_reason in the SSE stream)
		emit(map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     20,
				"completion_tokens": 10,
				"total_tokens":      30,
			},
		})

		// [DONE] marker
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})

	resultCh, err := adapter.Stream(context.Background(), nil, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}

	var toolCalls []agent.ToolCall
	var capturedUsage *agent.Usage
	for s := range resultCh {
		if len(s.ToolCalls) > 0 {
			toolCalls = s.ToolCalls
		}
		if s.Usage != nil {
			capturedUsage = s.Usage
		}
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}

	if len(toolCalls) == 0 {
		t.Fatal("expected tool calls")
	}
	if capturedUsage == nil {
		t.Fatal("BUG: usage was NOT captured for tool_calls finish_reason")
	}
	if capturedUsage.TotalTokens != 30 {
		t.Errorf("expected TotalTokens=30, got %d", capturedUsage.TotalTokens)
	}
}

// TestOpenAIStreamExistingTestsStillPass verifies that the existing test
// patterns (no usage event after finish_reason) still work after the fix.
func TestOpenAIStreamExistingTestsStillPass(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, frag := range []string{"hel", "lo"} {
			payload := map[string]any{
				"choices": []any{
					map[string]any{
						"delta":        map[string]any{"content": frag},
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
		// Final frame with finish_reason — no usage event after it
		payload := map[string]any{
			"choices": []any{
				map[string]any{
					"delta":        map[string]any{},
					"finish_reason": "stop",
				},
			},
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
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
