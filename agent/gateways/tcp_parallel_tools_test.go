package gateways

// This file documents and reproduces a real bug.
//
// Symptom (as reported by users with Anthropic and OpenAI adapters):
//
//   When the model emits multiple tool calls in a single response, the
//   client (Neovim) sees the `tool ... start` events but eventually the
//   request fails with "context canceled" and the matching tool `ok`/`err`
//   frames are missing.
//
// Root cause:
//
//   `Conversation.runToolsConcurrently` fans out one goroutine per tool
//   call and fires the `OnToolCall` / `OnToolResult` hooks from each
//   goroutine in parallel. The TCP/WebSocket gateways install hooks that
//   funnel every event through a single shared `flush` closure which
//   wraps an unprotected `bufio.Writer` + `json.Encoder`.
//
//   Two goroutines hitting that closure concurrently race on the buffered
//   writer. The race garbles the bytes on the wire (so the client never
//   sees a well-formed result frame) and one of the encoders observes a
//   write error, sets `broken=true` and calls `cancel()` on the request
//   context. The engine is still in its tool loop; the *next* call to the
//   LLM adapter sees `ctx.Done()` and returns `context canceled` to the
//   caller. That is exactly the reported failure mode.
//
// The reproducer below drives the *real* engine with a fake adapter that
// returns N parallel tool calls in a single assistant turn, and a fake
// gateway "flush" that writes to a single shared buffer (mirroring the
// production gateway). Run it with the race detector:
//
//     go test ./agent/gateways/ -run TestEngineParallelToolCalls -race
//
// On the buggy code this test reports a data race and/or torn JSON. With
// the fix in place (hook invocations serialised inside the engine) the
// race detector is happy.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// parallelToolAdapter returns a single assistant turn that asks for N
// parallel tool calls, then a final text reply. It mirrors what
// Anthropic/OpenAI do when a model decides to fan out tools.
type parallelToolAdapter struct {
	fanout int
	calls  int
}

func (a *parallelToolAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	a.calls++
	if a.calls == 1 {
		tc := make([]agent.ToolCall, a.fanout)
		for i := range tc {
			tc[i] = agent.ToolCall{
				ID:        idFor(i),
				Name:      "noop",
				Arguments: `{}`,
			}
		}
		return &agent.LLMResponse{
			Message:      agent.Message{Role: agent.RoleAssistant, ToolCalls: tc},
			FinishReason: "tool_calls",
		}, nil
	}
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *parallelToolAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
}

func idFor(i int) string {
	return "call-" + string(rune('a'+i))
}

// executeSyncConv is a helper that consumes a Conversation.Execute event
// channel and returns the session, final reply, and error.
func executeSyncConv(conv *agent.Conversation, ctx context.Context, sessionID, input string) (*agent.Session, string, error) {
	eventCh, err := conv.Execute(ctx, sessionID, input)
	if err != nil {
		return nil, "", err
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				return nil, "", te.Err
			}
			session, lookupErr := conv.Sessions.GetOrCreate(ctx, conv.Namespace, sessionID)
			if lookupErr != nil {
				return nil, te.Reply, lookupErr
			}
			return session, te.Reply, nil
		}
	}
	return nil, "", nil
}

// TestEngineParallelToolCalls drives the engine end-to-end with the same
// gateway hooks the TCP gateway installs. It reproduces the original
// failure mode: concurrent goroutines writing through a single
// json.Encoder produce a data race (caught by -race) and torn frames
// (caught by the JSON re-decoder below).
func TestEngineParallelToolCalls(t *testing.T) {
	const fanout = 16

	adapter := &parallelToolAdapter{fanout: fanout}
	sm := agent.NewSessionManager(nil, "")
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "noop"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok":true}`, nil
		},
	})

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	flush := func(v FrameResponse) error {
		return enc.Encode(v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	var broken bool
	emit := func(r FrameResponse) {
		if broken {
			return
		}
		if err := flush(r); err != nil {
			broken = true
			cancel()
		}
	}

	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Hooks: agent.Hooks{
			OnToolCall: func(_ string, call agent.ToolCall) {
				emit(FrameResponse{SessionID: "sess-1", Event: "tool", Tool: call.Name, Status: "start", ExecSnippet: call.ExecSnippet})
			},
			OnToolResult: func(_ string, call agent.ToolCall, res event.ToolResult) {
				status := "ok"
				if res.IsError() {
					status = "err"
				}
				emit(FrameResponse{SessionID: "sess-1", Event: "tool", Tool: call.Name, Status: status, ExecSnippet: call.ExecSnippet})
			},
			OnLLMResponse: func(_ string, resp *agent.LLMResponse) {
				if resp == nil || resp.Usage == nil {
					return
				}
				emit(FrameResponse{
					SessionID: "sess-1",
					Event:     "meta",
					Data: map[string]any{
						"prompt_tokens":     resp.Usage.PromptTokens,
						"completion_tokens": resp.Usage.CompletionTokens,
						"total_tokens":      resp.Usage.TotalTokens,
					},
				})
			},
		},
	}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)

	_, reply, err := executeSyncConv(conv, ctx, "", "go")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if reply != "done" {
		t.Fatalf("expected final reply %q, got: %q", "done", reply)
	}
	if ctx.Err() != nil {
		t.Fatalf("request context was cancelled during the run: %v", ctx.Err())
	}

	var (
		starts  int
		results int
	)
	dec := json.NewDecoder(&buf)
	for {
		var frame FrameResponse
		err := dec.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("torn JSON frame on the wire: %v", err)
		}
		switch frame.Status {
		case "start":
			starts++
		case "ok", "err":
			results++
		}
	}
	if starts != fanout {
		t.Fatalf("expected %d tool start frames, got %d", fanout, starts)
	}
	if results != fanout {
		t.Fatalf("expected %d tool result frames, got %d (this is the user-reported missing-result bug)", fanout, results)
	}
}
