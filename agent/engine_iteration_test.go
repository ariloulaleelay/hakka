package agent

import (
	"context"
	"encoding/json"
	"testing"
)

type loopFakeAdapter struct {
	responses []Message
	callCount int
}

func (a *loopFakeAdapter) Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions, _ func(string)) (*LLMResponse, error) {
	if a.callCount >= len(a.responses) {
		return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "fallback"}}, nil
	}
	resp := a.responses[a.callCount]
	a.callCount++
	return &LLMResponse{Message: resp}, nil
}

func TestMaxToolIterations_IsPerTurn(t *testing.T) {
	adapter := &loopFakeAdapter{
		responses: []Message{
			// Turn 1
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_1", Name: "dummy", Arguments: "{}"},
			}},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_2", Name: "dummy", Arguments: "{}"},
			}},
			{Role: RoleAssistant, Content: "Done with turn 1"},
			// Turn 2
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_3", Name: "dummy", Arguments: "{}"},
			}},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_4", Name: "dummy", Arguments: "{}"},
			}},
			{Role: RoleAssistant, Content: "Done with turn 2"},
		},
	}

	sm := NewSessionManager(NewMemoryStore(), "")
	tr := NewToolRegistry()
	tr.Register(Tool{
		Schema:  ToolSchema{Name: "dummy", Description: "dummy"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) { return `{"status": "ok"}`, nil },
	})
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 3}
	conv := NewConversation(sm, router, tr, "testns", cfg)

	_, reply, err := executeSync(conv, context.Background(), "session1", "Turn 1")
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}
	if reply != "Done with turn 1" {
		t.Fatalf("Expected 'Done with turn 1', got %q", reply)
	}

	_, reply, err = executeSync(conv, context.Background(), "session1", "Turn 2")
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}
	if reply != "Done with turn 2" {
		t.Fatalf("Expected 'Done with turn 2', got %q", reply)
	}

	// Third turn - hit iteration limit
	adapter.responses = append(adapter.responses,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_5", Name: "dummy", Arguments: "{}"}}},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_6", Name: "dummy", Arguments: "{}"}}},
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_7", Name: "dummy", Arguments: "{}"}}},
		Message{Role: RoleAssistant, Content: "Done with turn 3"},
	)
	_, _, err = executeSync(conv, context.Background(), "session1", "Turn 3")
	if err != ErrMaxIterations {
		t.Fatalf("Expected ErrMaxIterations, got: %v", err)
	}
}
