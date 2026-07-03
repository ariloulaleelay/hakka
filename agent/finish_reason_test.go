package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestFinishReason_StoredOnMessage proves that the LLM's FinishReason is
// recorded on the assistant Message in session history after a turn.
func TestFinishReason_StoredOnMessage(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "hello back"},
			FinishReason: "stop",
		},
	})
	session, _, err := executeSync(conv, context.Background(), "fr-test-1", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Find the assistant message and check FinishReason.
	lastMsg := session.Messages()[len(session.Messages())-1]
	if lastMsg.Role != RoleAssistant {
		t.Fatalf("expected last message to be assistant, got %s", lastMsg.Role)
	}
	if lastMsg.FinishReason == "" {
		t.Fatal("BUG CONFIRMED: FinishReason is empty on assistant message — not being stored")
	}
	if lastMsg.FinishReason != "stop" {
		t.Fatalf("expected FinishReason=%q, got %q", "stop", lastMsg.FinishReason)
	}
	t.Logf("OK: FinishReason=%q on assistant message", lastMsg.FinishReason)
}

// TestFinishReason_ToolCallRound proves that after a tool call round,
// the tool-call assistant message carries a FinishReason (e.g. "tool_calls"),
// and the follow-up text response carries another (e.g. "stop").
func TestFinishReason_ToolCallRound(t *testing.T) {
	conv, _, _, tools := newTestComponents(t, []LLMResponse{
		{
			Message: Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID: "call-1", Name: "echo_tool", Arguments: `{"message":"hi"}`,
				}},
			},
			FinishReason: "tool_calls",
		},
		{
			Message:      Message{Role: RoleAssistant, Content: "done"},
			FinishReason: "stop",
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{
			Name:        "echo_tool",
			Description: "Echoes input",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			return "echoed", nil
		},
	})

	session, _, err := executeSyncWithTools(t, conv, context.Background(), "fr-test-2", "echo hi", "echo_tool")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	msgs := session.Messages()

	// Collect assistant messages.
	var assistantMsgs []Message
	for _, m := range msgs {
		if m.Role == RoleAssistant {
			assistantMsgs = append(assistantMsgs, m)
		}
	}
	if len(assistantMsgs) < 2 {
		t.Fatalf("expected at least 2 assistant messages, got %d", len(assistantMsgs))
	}

	// First assistant message should have tool calls and "tool_calls" finish reason.
	toolCallMsg := assistantMsgs[0]
	if len(toolCallMsg.ToolCalls) == 0 {
		t.Fatal("expected first assistant message to have tool calls")
	}
	if toolCallMsg.FinishReason == "" {
		t.Fatal("BUG CONFIRMED: FinishReason missing on tool-call assistant message")
	}
	t.Logf("OK: tool-call assistant FinishReason=%q", toolCallMsg.FinishReason)

	// Second assistant message should have text and "stop" finish reason.
	textMsg := assistantMsgs[1]
	if textMsg.Content == "" {
		t.Fatal("expected second assistant message to have content")
	}
	if textMsg.FinishReason == "" {
		t.Fatal("BUG CONFIRMED: FinishReason missing on text-response assistant message")
	}
	t.Logf("OK: text-response assistant FinishReason=%q", textMsg.FinishReason)
}

// TestFinishReason_StreamPath proves that FinishReason is also stored
// when using the streaming path (StreamSession.Execute).
func TestFinishReason_StreamPath(t *testing.T) {
	_, streamer, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "stream hello"},
			FinishReason: "stop",
		},
	})
	eventCh, err := streamer.Execute(context.Background(), "fr-stream-test", "hi")
	if err != nil {
		t.Fatalf("StreamSession.Execute: %v", err)
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
		}
	}

	session, err := streamer.conv.sessions.GetOrCreate(context.Background(), "testns", "fr-stream-test")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	lastMsg := session.Messages()[len(session.Messages())-1]
	if lastMsg.Role != RoleAssistant {
		t.Fatalf("expected last message to be assistant, got %s", lastMsg.Role)
	}
	if lastMsg.FinishReason == "" {
		t.Fatal("BUG CONFIRMED: FinishReason is empty on streaming assistant message")
	}
	t.Logf("OK: streaming FinishReason=%q", lastMsg.FinishReason)
}
