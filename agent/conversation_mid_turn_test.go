package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestConversationMidTurnEnableTool_EndToEnd verifies that when the LLM
// calls enable_tool for a tool and then that tool in the next iteration,
// the tool is actually executed (not rejected as "disabled").
//
// This test goes through the full Conversation.Execute path with a
// copyBackStore (simulating SQLiteStore's deep-copy behavior). The
// enable_tool handler uses context-injected session (the new architecture),
// so it mutates the engine's session in-place without fetching from store.
func TestConversationMidTurnEnableTool_EndToEnd(t *testing.T) {
	store := newCopyBackStore()
	sm := NewSessionManager(store, "sys")
	tools := NewToolRegistry()

	var testToolExecuted bool
	var sessionID string

	tools.Register(Tool{
		Schema: ToolSchema{Name: "test_tool"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			testToolExecuted = true
			return "test_tool result", nil
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "enable_tool", Description: "Enable a tool"},
		Tags:   []string{"tool"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Name string `json:"name"` }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			// Use context-injected session — this is the NEW approach.
			// The engine injects the session before every tool invocation.
			sess, ok := event.SessionViewFromContext(ctx).(SessionToolEditor)
			if !ok || sess == nil {
				return "", fmt.Errorf("enable_tool: no session in context")
			}
			sess.EnableTool(args.Name)
			return "enabled " + args.Name, nil
		},
	})

	adapter := &fakeAdapter{
		responses: []LLMResponse{
			{
				Message: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID: "call-enable", Name: "enable_tool", Arguments: `{"name":"test_tool"}`,
					}},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID: "call-test", Name: "test_tool", Arguments: `{}`,
					}},
				},
				FinishReason: "tool_calls",
			},
			{
				Message:      Message{Role: RoleAssistant, Content: "done"},
				FinishReason: "stop",
			},
		},
	}

	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	// Create session first so we can capture its ID
	s, err := sm.GetOrCreate(context.Background(), "testns", "")
	if err != nil {
		t.Fatal(err)
	}
	sessionID = s.SessionID()
	if err := sm.Save(context.Background(), "testns", s); err != nil {
		t.Fatal(err)
	}

	eventCh, err := conv.Execute(context.Background(), sessionID, "enable test_tool and use it")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var reply string
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
			reply = te.Reply
		}
	}

	if reply != "done" {
		t.Fatalf("expected reply %q, got %q", "done", reply)
	}

	if !testToolExecuted {
		savedSession, _ := sm.GetOrCreate(context.Background(), "testns", sessionID)
		for _, m := range savedSession.AllMessages() {
			if m.Role == RoleTool && m.Name == "test_tool" {
				if strings.Contains(m.Content, "disabled") {
					t.Fatal("BUG: test_tool was rejected as disabled — context injection failed")
				}
			}
		}
		t.Fatal("test_tool was never executed — the tool loop didn't reach it or it was silently dropped")
	}
}

// TestConversationMidTurnEnableTool_ContextInjection_Run verifies that
// with context injection, the turn runner works correctly without any
// reloadFn, even with a deep-copy store.
func TestConversationMidTurnEnableTool_ContextInjection_Run(t *testing.T) {
	store := newCopyBackStore()
	sm := NewSessionManager(store, "sys")
	tools := NewToolRegistry()

	var testToolExecuted bool
	sessionID := captureSessionID(t, sm, "testns")

	tools.Register(Tool{
		Schema: ToolSchema{Name: "test_tool"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			testToolExecuted = true
			return "test_tool result", nil
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "enable_tool", Description: "Enable a tool"},
		Tags:   []string{"tool"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Name string `json:"name"` }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			sess, ok := event.SessionViewFromContext(ctx).(SessionToolEditor)
			if !ok || sess == nil {
				return "", fmt.Errorf("enable_tool: no session in context")
			}
			sess.EnableTool(args.Name)
			return "enabled " + args.Name, nil
		},
	})

	adapter := &fakeAdapter{
		responses: []LLMResponse{
			{
				Message: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID: "call-enable", Name: "enable_tool", Arguments: `{"name":"test_tool"}`,
					}},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID: "call-test", Name: "test_tool", Arguments: `{}`,
					}},
				},
				FinishReason: "tool_calls",
			},
			{
				Message:      Message{Role: RoleAssistant, Content: "done"},
				FinishReason: "stop",
			},
		},
	}

	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)

	engineCfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	toolExec := newToolExecutor(tools, nil, engineCfg.Hooks)
	rr := newTurnRunner(tools, toolExec, engineCfg, router, testLogger(t))

	session, err := sm.GetOrCreate(context.Background(), "testns", sessionID)
	if err != nil {
		t.Fatal(err)
	}

	callCount := 0
	eventCh := make(chan event.EngineEvent, 256)
	step := func(ctx context.Context, msgs []Message, schemas []ToolSchema, ev eventSender) (*llmStepResult, error) {
		callCount++
		switch callCount {
		case 1:
			return &llmStepResult{
				toolCalls: []ToolCall{{
					ID: "call-enable", Name: "enable_tool", Arguments: `{"name":"test_tool"}`,
				}},
			}, nil
		case 2:
			return &llmStepResult{
				toolCalls: []ToolCall{{
					ID: "call-test", Name: "test_tool", Arguments: `{}`,
				}},
			}, nil
		default:
			return &llmStepResult{content: "done"}, nil
		}
	}

	saveFn := func(ctx context.Context, s SessionView) error {
		return sm.Save(ctx, "testns", s)
	}

	reply, err := rr.run(context.Background(), session, eventCh, step, saveFn)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if reply != "done" {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if !testToolExecuted {
		t.Fatal("test_tool should have been executed — context injection should have enabled it")
	}
}

// captureSessionID creates a session and returns its ID.
func captureSessionID(t *testing.T, sm *SessionManager, ns string) string {
	t.Helper()
	s, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(context.Background(), ns, s); err != nil {
		t.Fatal(err)
	}
	return s.SessionID()
}
