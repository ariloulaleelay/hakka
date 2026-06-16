package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/you/hakka/agent"
)

// TestOpenAIStreamToolCallAccumulatedDeltas reproduces the bug where
// streaming tool call deltas (incremental chunks per index) are appended
// as separate tool calls instead of being accumulated by Index.
//
// OpenAI sends tool calls incrementally:
//   chunk 1: {index:0, id:"call_1", function:{name:"get_weather", arguments:""}}
//   chunk 2: {index:0, function:{arguments:"{\"loc\":"}}
//   chunk 3: {index:0, function:{arguments:"ation\": \"NYC\"}"}}
//
// The bug: each chunk creates a new agent.ToolCall (3 total) instead of
// one accumulated call. The first has Arguments="" (empty), which causes
// "missing field 'arguments'" when sent back to the API.
func TestOpenAIStreamToolCallAccumulatedDeltas(t *testing.T) {
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

		// Chunk 1: ID + name (arguments start empty)
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
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Chunk 2: partial arguments delta
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    nil,
								"function": map[string]any{
									"name":      nil,
									"arguments": `{"location": "`,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Chunk 3: remaining arguments
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"function": map[string]any{
									"arguments": `NYC"}`,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Chunk 4: finish reason
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		})
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

	if len(toolCalls) != 1 {
		t.Fatalf("expected exactly 1 accumulated tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("expected ID 'call_1', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected Name 'get_weather', got %q", tc.Name)
	}
	if tc.Arguments != `{"location": "NYC"}` {
		t.Errorf("expected Arguments '{\"location\": \"NYC\"}', got %q", tc.Arguments)
	}

	// Verify arguments are valid JSON
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["location"] != "NYC" {
		t.Fatalf("expected location=NYC, got %v", args)
	}
}

// TestOpenAIStreamMultipleToolCallsAccumulated verifies that multiple
// parallel tool calls (different indices) are correctly accumulated.
func TestOpenAIStreamMultipleToolCallsAccumulated(t *testing.T) {
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

		// Both tool calls start simultaneously
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_A",
								"type":  "function",
								"function": map[string]any{
									"name":      "get_weather",
									"arguments": "",
								},
							},
							map[string]any{
								"index": 1,
								"id":    "call_B",
								"type":  "function",
								"function": map[string]any{
									"name":      "get_time",
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Argument deltas for both
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"function": map[string]any{
									"arguments": `{"city": "`,
								},
							},
							map[string]any{
								"index": 1,
								"function": map[string]any{
									"arguments": `{"tz": "`,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Remaining arguments for both
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"function": map[string]any{
									"arguments": `Paris"}`,
								},
							},
							map[string]any{
								"index": 1,
								"function": map[string]any{
									"arguments": `UTC"}`,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Finish
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		})
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
		t.Fatalf("expected exactly 2 accumulated tool calls, got %d", len(toolCalls))
	}

	// First tool call
	tc0 := toolCalls[0]
	if tc0.ID != "call_A" || tc0.Name != "get_weather" {
		t.Errorf("tool call 0: ID=%q Name=%q", tc0.ID, tc0.Name)
	}
	if tc0.Arguments != `{"city": "Paris"}` {
		t.Errorf("tool call 0 args: got %q", tc0.Arguments)
	}

	// Second tool call
	tc1 := toolCalls[1]
	if tc1.ID != "call_B" || tc1.Name != "get_time" {
		t.Errorf("tool call 1: ID=%q Name=%q", tc1.ID, tc1.Name)
	}
	if tc1.Arguments != `{"tz": "UTC"}` {
		t.Errorf("tool call 1 args: got %q", tc1.Arguments)
	}
}

// TestOpenAIStreamToolCallEOFWithoutFinishReason tests the EOF path:
// the stream ends with io.EOF (no finish_reason) but has accumulated
// tool calls. This exercises the second flush point in Stream().
func TestOpenAIStreamToolCallEOFWithoutFinishReason(t *testing.T) {
	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Single chunk with ID+name+args, no finish_reason
		payload := map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_eof",
								"type":  "function",
								"function": map[string]any{
									"name":      "search",
									"arguments": `{"q":"test"}`,
								},
							},
						},
					},
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
		// No [DONE] message — server just closes the connection
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

	if len(toolCalls) != 1 {
		t.Fatalf("expected exactly 1 tool call via EOF path, got %d", len(toolCalls))
	}
	if toolCalls[0].ID != "call_eof" {
		t.Errorf("expected ID 'call_eof', got %q", toolCalls[0].ID)
	}
	if toolCalls[0].Name != "search" {
		t.Errorf("expected Name 'search', got %q", toolCalls[0].Name)
	}
}

// TestOpenAIStreamToolCallManyChunks tests extreme argument fragmentation:
// arguments spread across many small chunks to verify pure concatenation.
func TestOpenAIStreamToolCallManyChunks(t *testing.T) {
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

		// Initial chunk
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_long",
								"type":  "function",
								"function": map[string]any{
									"name":      "write_file",
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// 6 tiny argument chunks
		chunks := []string{`{"`, `pat`, `h":`, ` "`, `/tmp/`, `f"}`}
		for _, c := range chunks {
			emit(map[string]any{
				"choices": []any{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []any{
								map[string]any{
									"index": 0,
									"function": map[string]any{
										"arguments": c,
									},
								},
							},
						},
						"finish_reason": nil,
					},
				},
			})
		}

		// Finish
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		})
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

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.Name != "write_file" {
		t.Errorf("expected Name 'write_file', got %q", tc.Name)
	}
	if tc.Arguments != `{"path": "/tmp/f"}` {
		t.Errorf("expected Arguments '{\"path\": \"/tmp/f\"}', got %q", tc.Arguments)
	}

	// Verify valid JSON
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v - got: %q", err, tc.Arguments)
	}
	if args["path"] != "/tmp/f" {
		t.Fatalf("expected path=/tmp/f, got %v", args)
	}
}

// TestOpenAIStreamToolCallEmptyArgsReplaced verifies that when all
// argument deltas are empty strings, the final tool call gets "{}"
// instead of an empty string (which would cause "missing field 'arguments'").
func TestOpenAIStreamToolCallEmptyArgsReplaced(t *testing.T) {
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

		// ID + name with empty arguments
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_empty",
								"type":  "function",
								"function": map[string]any{
									"name":      "noop",
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Another chunk with empty arguments (should be skipped)
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"function": map[string]any{
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Finish
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		})
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

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.Arguments != "{}" {
		t.Errorf("expected Arguments '{}' (empty replaced), got %q", tc.Arguments)
	}

	// Must be valid JSON
	var v any
	if err := json.Unmarshal([]byte(tc.Arguments), &v); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
}

// TestOpenAIStreamToolCallInterleavedText verifies that text deltas
// and tool call deltas can appear in the same chunk without interfering.
func TestOpenAIStreamToolCallInterleavedText(t *testing.T) {
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

		// Text + first tool call chunk
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"content": "Let me ",
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"id":    "call_x",
								"type":  "function",
								"function": map[string]any{
									"name":      "search",
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// More text + argument delta
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"content": "look that up. ",
						"tool_calls": []any{
							map[string]any{
								"index": 0,
								"function": map[string]any{
									"arguments": `{"q":"hello"}`,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		})

		// Finish
		emit(map[string]any{
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		})
	})

	resultCh, err := adapter.Stream(context.Background(), nil, nil, agent.CompleteOptions{})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}

	var textBuf string
	var toolCalls []agent.ToolCall
	for s := range resultCh {
		textBuf += s.Delta
		if len(s.ToolCalls) > 0 {
			toolCalls = s.ToolCalls
		}
		if s.Err != nil {
			t.Fatalf("stream err: %v", s.Err)
		}
	}

	if textBuf != "Let me look that up. " {
		t.Errorf("expected text 'Let me look that up. ', got %q", textBuf)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "search" || toolCalls[0].Arguments != `{"q":"hello"}` {
		t.Errorf("tool call: Name=%q Args=%q", toolCalls[0].Name, toolCalls[0].Arguments)
	}
}
