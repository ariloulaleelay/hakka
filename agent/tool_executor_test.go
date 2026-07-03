package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestToolUsageEnablesTool verifies that when the LLM uses a tool that is
// allowed but disabled, the tool gets auto-enabled so it appears in the
// API "tools" section for subsequent iterations.
func TestToolUsageEnablesTool(t *testing.T) {
	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{
			Name:        "greet",
			Description: "Say hello",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
				},
			},
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("test", "system prompt")
	// greet is allowed (not denied) but NOT enabled initially
	if session.IsToolEnabled("greet") {
		t.Fatal("greet should NOT be enabled initially")
	}
	if !session.IsToolAllowed("greet") {
		t.Fatal("greet should be allowed initially")
	}

	events := make(chan event.EngineEvent, 10)
	executor := newToolExecutor(tools, nil, Hooks{})

	// Simulate LLM calling the tool
	calls := []ToolCall{
		{ID: "call-1", Name: "greet", Arguments: `{"name":"world"}`},
	}
	executor.ExecuteToolCalls(context.Background(), session, calls, events, Hooks{})

	// After execution, greet should be enabled
	if !session.IsToolEnabled("greet") {
		t.Fatal("tool should be enabled after the LLM uses it, but it is not")
	}
}

// TestToolUsageEnablesTool_VerifySchemas verifies that after tool usage,
// the tool appears in SchemasForSession (which drives the API "tools"
// section), whereas before usage it was absent.
func TestToolUsageEnablesTool_VerifySchemas(t *testing.T) {
	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{
			Name:        "greet",
			Description: "Say hello",
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("test", "system prompt")
	// greet is NOT enabled initially
	beforeSchemas := tools.SchemasForSession(session)
	for _, s := range beforeSchemas {
		if s.Name == "greet" {
			t.Fatal("greet should NOT be in schemas before it is used")
		}
	}

	events := make(chan event.EngineEvent, 10)
	executor := newToolExecutor(tools, nil, Hooks{})
	calls := []ToolCall{
		{ID: "call-1", Name: "greet", Arguments: `{}`},
	}
	executor.ExecuteToolCalls(context.Background(), session, calls, events, Hooks{})

	// After execution, greet should appear in schemas
	afterSchemas := tools.SchemasForSession(session)
	found := false
	for _, s := range afterSchemas {
		if s.Name == "greet" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("tool should appear in SchemasForSession after the LLM uses it, but it does not")
	}
}
