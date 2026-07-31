package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestEnginePreEnabledToolsInFreshSession verifies that a brand-new session
// has management tools (show_tool, allow_tool, deny_tool) pre-enabled and
// thus available in the schema sent to the LLM.
func TestEnginePreEnabledToolsInFreshSession(t *testing.T) {
	adapter := &recordingAdapter{}
	tools := NewToolRegistry()

	// Register show_tool as a management tool
	tools.Register(Tool{
		Schema: ToolSchema{
			Name:        "show_tool",
			Description: "Show tool details",
		},
		Tags: []string{"tool", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Tool info", nil
		},
	})

	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 4, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	// Fresh session — management tools should be pre-enabled
	session, err := sm.GetOrCreate(context.Background(), "testns", "fresh-session")
	if err != nil {
		t.Fatal(err)
	}
	_ = sm.Save(context.Background(), "testns", session)

	// Run a turn.
	eventCh, err := conv.Execute(context.Background(), "fresh-session", "show me tools")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
		}
	}

	// Check what schemas the adapter saw.
	adapter.mu.Lock()
	sawTools := adapter.lastTools
	adapter.mu.Unlock()

	if len(sawTools) == 0 {
		t.Fatal("adapter received ZERO tool schemas — pre-enabled management tools are missing from fresh session")
	}

	foundShowTool := false
	for _, ts := range sawTools {
		if ts.Name == "show_tool" {
			foundShowTool = true
			break
		}
	}
	if !foundShowTool {
		t.Fatalf("adapter did NOT receive show_tool schema — got: %v", toolNames(sawTools))
	}
}

// TestEngineExecutePreEnabledToolInFreshSession verifies that when the LLM
// calls show_tool (a pre-enabled management tool) in a fresh session, the
// call succeeds.
func TestEngineExecutePreEnabledToolInFreshSession(t *testing.T) {
	adapter := &fakeAdapter{
		responses: []LLMResponse{
			{
				Message: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID: "call-1", Name: "show_tool", Arguments: `{"name":"read_file"}`,
					}},
				},
				FinishReason: "tool_calls",
			},
			{
				Message:      Message{Role: RoleAssistant, Content: "done showing"},
				FinishReason: "stop",
			},
		},
	}
	tools := NewToolRegistry()

	// Register show_tool as a management tool
	tools.Register(Tool{
		Schema: ToolSchema{
			Name:        "show_tool",
			Description: "Show tool details and enable",
		},
		Tags: []string{"tool", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Tool: read_file\nStatus: enabled", nil
		},
	})

	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 4, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	// Fresh session — show_tool pre-enabled
	session, err := sm.GetOrCreate(context.Background(), "testns", "fresh-exec")
	if err != nil {
		t.Fatal(err)
	}
	_ = sm.Save(context.Background(), "testns", session)

	// Execute — the LLM will try to call show_tool.
	session, reply, err := executeSync(conv, context.Background(), "fresh-exec", "read_file details")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if reply != "done showing" {
		t.Fatalf("expected final reply %q, got %q", "done showing", reply)
	}

	// Verify that show_tool was actually executed and its result is in the session.
	var sawToolResult bool
	for _, m := range session.Messages() {
		if m.Role == RoleTool && m.Name == "show_tool" {
			sawToolResult = true
			if strings.Contains(m.Content, "denied") {
				t.Fatalf("show_tool returned 'denied' error: %q", m.Content)
			}
			if !strings.Contains(m.Content, "read_file") {
				t.Fatalf("expected show_tool result to contain tool name, got: %q", m.Content)
			}
		}
	}
	if !sawToolResult {
		t.Fatal("show_tool tool result not found in session — tool was likely rejected")
	}
}

// recordingAdapter records the last schemas it received and returns a
// simple response.
type recordingAdapter struct {
	mu        sync.Mutex
	lastMsgs  []Message
	lastTools []ToolSchema
	calls     int
}

func (a *recordingAdapter) Complete(_ context.Context, msgs []Message, tools []ToolSchema, _ CompleteOptions, _ func(string)) (*LLMResponse, error) {
	a.mu.Lock()
	a.lastMsgs = msgs
	a.lastTools = tools
	a.calls++
	a.mu.Unlock()
	return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "I have tools."}, FinishReason: "stop"}, nil
}


func toolNames(schemas []ToolSchema) []string {
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	return names
}
