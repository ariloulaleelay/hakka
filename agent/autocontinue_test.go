package agent

import (
	"context"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestAutoContinue_DisabledByDefault proves that without hacks, an empty
// stop response finishes the turn normally (returns empty string).
func TestAutoContinue_DisabledByDefault(t *testing.T) {
	// The adapter returns an empty stop response — no content, no tool calls.
	conv, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: ""},
			FinishReason: "stop",
		},
	})
	// Default hacks: IgnoreStopIfNoContent is nil → disabled.
	session, reply, err := executeSync(conv, context.Background(), "auto-test-1", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if reply != "" {
		t.Fatalf("expected empty reply, got %q", reply)
	}
	// Only one LLM call — no autocontinue.
	if len(session.Messages()) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(session.Messages()))
	}
	lastMsg := session.Messages()[len(session.Messages())-1]
	if lastMsg.FinishReason != "stop" {
		t.Fatalf("expected finish_reason=stop, got %q", lastMsg.FinishReason)
	}
}

// TestAutoContinue_EnabledRestartsWithContent proves that when
// ignore_stop_if_no_content is enabled, an empty stop response triggers
// a re-prompt which then produces content.
func TestAutoContinue_EnabledRestartsWithContent(t *testing.T) {
	// Set up a registry with a model profile that has hacks enabled.
	mock := &fakeAdapter{
		responses: []LLMResponse{
			{Message: Message{Role: RoleAssistant, Content: ""}, FinishReason: "stop"},
			{Message: Message{Role: RoleAssistant, Content: "now I have content"}, FinishReason: "stop"},
		},
	}
	sm := NewSessionManager(nil, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", mock)
	// Enable the hack on the model profile.
	trueVal := true
	reg.SetProfileHacks("default", Hacks{IgnoreStopIfNoContent: &trueVal})
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 10, Logger: testLogger(t)}
	ns := "testns"
	conv := NewConversation(sm, router, tools, ns, cfg)

	// Test via non-streaming (Conversation.Execute).
	eventCh, err := conv.Execute(context.Background(), "auto-test-2", "hello")
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
	if reply != "now I have content" {
		t.Fatalf("expected reply %q, got %q", "now I have content", reply)
	}
	if mock.calls != 2 {
		t.Fatalf("expected 2 LLM calls (empty stop + re-prompt), got %d", mock.calls)
	}
}

// TestAutoContinue_ExhaustedRounds proves that after 3 autocontinues,
// the loop finishes even if the provider keeps returning empty stops.
func TestAutoContinue_ExhaustedRounds(t *testing.T) {
	mock := &fakeAdapter{
		responses: []LLMResponse{
			{Message: Message{Role: RoleAssistant, Content: ""}, FinishReason: "stop"},
			{Message: Message{Role: RoleAssistant, Content: ""}, FinishReason: "stop"},
			{Message: Message{Role: RoleAssistant, Content: ""}, FinishReason: "stop"},
			{Message: Message{Role: RoleAssistant, Content: ""}, FinishReason: "stop"}, // 4th call exhausts
		},
	}
	sm := NewSessionManager(nil, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", mock)
	trueVal := true
	reg.SetProfileHacks("default", Hacks{IgnoreStopIfNoContent: &trueVal})
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 10, Logger: testLogger(t)}
	ns := "testns"
	conv := NewConversation(sm, router, tools, ns, cfg)

	eventCh, err := conv.Execute(context.Background(), "auto-test-3", "hello")
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
	// maxAutoContinue=3 means up to 3 re-prompts → 1 original + 3 autocontinues = 4 calls.
	// The 4th empty stop exhausts the limit and finishes the turn.
	if mock.calls != 4 {
		t.Fatalf("expected 4 LLM calls (1 original + 3 autocontinues), got %d", mock.calls)
	}
}

// TestAutoContinue_DoesNotTriggerOnNonEmptyStop proves that the hack
// does NOT trigger when the provider returns actual content.
func TestAutoContinue_DoesNotTriggerOnNonEmptyStop(t *testing.T) {
	mock := &fakeAdapter{
		responses: []LLMResponse{
			{Message: Message{Role: RoleAssistant, Content: "hello back"}, FinishReason: "stop"},
		},
	}
	sm := NewSessionManager(nil, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", mock)
	trueVal := true
	reg.SetProfileHacks("default", Hacks{IgnoreStopIfNoContent: &trueVal})
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 10, Logger: testLogger(t)}
	ns := "testns"
	conv := NewConversation(sm, router, tools, ns, cfg)

	session, reply, err := executeSync(conv, context.Background(), "auto-test-4", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if reply != "hello back" {
		t.Fatalf("expected reply %q, got %q", "hello back", reply)
	}
	if mock.calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", mock.calls)
	}
	_ = session
}
