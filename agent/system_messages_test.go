package agent

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// SystemMessageProvider — environment/context-dependent system messages
// injected into every turn's context prefix.
// ---------------------------------------------------------------------------

func TestBuildCompactContext_WithProviders(t *testing.T) {
	s := NewSession("test", "You are helpful.")
	s.SetClientCWD(context.Background(), "/workspace")
	if err := s.AddMessages(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	provider := SystemMessageProviderFunc(func(session SessionView) []Message {
		return []Message{{Role: RoleSystem, Content: "Web files: /root/" + session.SessionID() + "/files"}}
	})

	result, _, _ := BuildCompactContext(s, 100000, provider)

	var got string
	for _, m := range result {
		if m.Role == RoleSystem && strings.Contains(m.Content, "Web files:") {
			got = m.Content
		}
	}
	want := "Web files: /root/" + s.SessionID() + "/files"
	if got != want {
		t.Fatalf("expected provider message %q in context, got %q", want, got)
	}
}

// Provider messages must come after the CWD message in the prefix so
// environment instructions append to (not precede) working-directory info.
func TestBuildCompactContext_ProviderMessageAfterCWDMessage(t *testing.T) {
	s := NewSession("test", "You are helpful.")
	s.SetClientCWD(context.Background(), "/workspace")
	if err := s.AddMessages(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	provider := SystemMessageProviderFunc(func(SessionView) []Message {
		return []Message{{Role: RoleSystem, Content: "provider-note"}}
	})

	result, _, _ := BuildCompactContext(s, 100000, provider)

	cwdIdx, providerIdx := -1, -1
	for i, m := range result {
		if m.Role == RoleSystem && strings.Contains(m.Content, "/workspace") {
			cwdIdx = i
		}
		if m.Role == RoleSystem && m.Content == "provider-note" {
			providerIdx = i
		}
	}
	if cwdIdx == -1 {
		t.Fatal("expected CWD message in context")
	}
	if providerIdx == -1 {
		t.Fatal("expected provider message in context")
	}
	if providerIdx <= cwdIdx {
		t.Fatalf("provider message must come after CWD message: cwd=%d provider=%d", cwdIdx, providerIdx)
	}
}

// Multiple providers all contribute, in registration order.
func TestBuildCompactContext_MultipleProviders(t *testing.T) {
	s := NewSession("test", "You are helpful.")
	s.SetClientCWD(context.Background(), "")
	if err := s.AddMessages(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	p1 := SystemMessageProviderFunc(func(SessionView) []Message {
		return []Message{{Role: RoleSystem, Content: "first"}}
	})
	p2 := SystemMessageProviderFunc(func(SessionView) []Message {
		return []Message{{Role: RoleSystem, Content: "second"}}
	})

	result, _, _ := BuildCompactContext(s, 100000, p1, p2)

	firstIdx, secondIdx := -1, -1
	for i, m := range result {
		if m.Role == RoleSystem && m.Content == "first" {
			firstIdx = i
		}
		if m.Role == RoleSystem && m.Content == "second" {
			secondIdx = i
		}
	}
	if firstIdx == -1 || secondIdx == -1 {
		t.Fatalf("expected both provider messages, got first=%d second=%d", firstIdx, secondIdx)
	}
	if secondIdx <= firstIdx {
		t.Fatalf("providers must append in order: first=%d second=%d", firstIdx, secondIdx)
	}
}

// Legacy behavior: no providers → unchanged output (no panic, no extras).
func TestBuildCompactContext_WithoutProviders(t *testing.T) {
	s := NewSession("test", "You are helpful.")
	s.SetClientCWD(context.Background(), "")
	if err := s.AddMessages(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	result, _, _ := BuildCompactContext(s, 100000)

	for _, m := range result {
		if m.Content == "provider-note" {
			t.Fatal("unexpected provider message without providers")
		}
	}
	if len(result) == 0 {
		t.Fatal("expected context messages")
	}
}

// Conversation-level: providers registered via AddSystemMessageProvider
// reach the LLM adapter on the next turn.
func TestConversation_AddSystemMessageProvider_InjectsIntoLLMContext(t *testing.T) {
	conv, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "ok"}, FinishReason: "stop"},
	})
	conv.AddSystemMessageProvider(SystemMessageProviderFunc(func(session SessionView) []Message {
		return []Message{{Role: RoleSystem, Content: "provider-note for " + session.SessionID()}}
	}))

	if _, _, err := executeSync(conv, context.Background(), "provider-inject", "hello"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	found := false
	for _, m := range adapter.lastMsgs {
		if m.Role == RoleSystem && strings.HasPrefix(m.Content, "provider-note for ") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected provider message in LLM context after AddSystemMessageProvider")
	}
}
