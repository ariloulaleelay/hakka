package agent

import (
	"context"
	"errors"
	"testing"
)

// captureAppendFailureStore exposes the detached session returned by Get so
// tests can compare it with the store's authoritative state after a failed
// persistence operation.
type captureAppendFailureStore struct {
	SessionStore
	failOnCall int
	calls      int
	loaded     *Session
}

func (s *captureAppendFailureStore) Get(ctx context.Context, namespace, id string) (*Session, bool, error) {
	session, ok, err := s.SessionStore.Get(ctx, namespace, id)
	if ok && err == nil {
		s.loaded = session
	}
	return session, ok, err
}

func (s *captureAppendFailureStore) AppendMessages(ctx context.Context, namespace, id string, msgs []Message, deltaTokens int, deltaCost float64) error {
	s.calls++
	if s.calls == s.failOnCall {
		return errors.New("append failed")
	}
	return s.SessionStore.AppendMessages(ctx, namespace, id, msgs, deltaTokens, deltaCost)
}

func TestExecute_AppendUserMessageFailureDoesNotDivergeSessionState(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	seed := NewSession("testns", "sys")
	seed.Update(func(d *SessionData) { d.ID = "append-user-failure" })
	if err := base.Put(ctx, "testns", seed); err != nil {
		t.Fatal(err)
	}

	store := &captureAppendFailureStore{SessionStore: base, failOnCall: 1}
	sm := NewSessionManager(store, "sys")
	adapter := &fakeAdapter{responses: []LLMResponse{{Message: Message{Role: RoleAssistant, Content: "unused"}, FinishReason: "stop"}}}
	registry := NewRegistry()
	registry.Register("default", adapter)
	conv := NewConversation(sm, NewRouter(registry), NewToolRegistry(), "testns", EngineConfig{Logger: testLogger(t)})

	if _, err := conv.Execute(ctx, seed.SessionID(), "hello"); err == nil {
		t.Fatal("Execute succeeded despite user-message persistence failure")
	}

	if store.loaded == nil {
		t.Fatal("store did not capture the loaded session")
	}
	if got := len(store.loaded.Messages()); got != 0 {
		t.Fatalf("working session has %d messages after failed persistence, want 0", got)
	}
	persisted, ok, err := base.Get(ctx, "testns", seed.SessionID())
	if err != nil || !ok {
		t.Fatalf("loading persisted session: ok=%v err=%v", ok, err)
	}
	if got := len(persisted.Messages()); got != 0 {
		t.Fatalf("persisted session has %d messages, want 0", got)
	}
}

func TestExecute_AppendAssistantMessagesFailureDoesNotDivergeSessionState(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	seed := NewSession("testns", "sys")
	seed.Update(func(d *SessionData) { d.ID = "append-assistant-failure" })
	if err := base.Put(ctx, "testns", seed); err != nil {
		t.Fatal(err)
	}

	// The first append is the user message. The second is the first
	// assistant/tool response group produced by the turn runner.
	store := &captureAppendFailureStore{SessionStore: base, failOnCall: 2}
	sm := NewSessionManager(store, "sys")
	adapter := &fakeAdapter{responses: []LLMResponse{{Message: Message{Role: RoleAssistant, Content: "hello"}, FinishReason: "stop"}}}
	registry := NewRegistry()
	registry.Register("default", adapter)
	conv := NewConversation(sm, NewRouter(registry), NewToolRegistry(), "testns", EngineConfig{Logger: testLogger(t)})

	events, err := conv.Execute(ctx, seed.SessionID(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	if store.loaded == nil {
		t.Fatal("store did not capture the loaded session")
	}
	if got := len(store.loaded.Messages()); got != 1 {
		t.Fatalf("working session has %d messages after failed persistence, want 1 committed user message", got)
	}
	persisted, ok, err := base.Get(ctx, "testns", seed.SessionID())
	if err != nil || !ok {
		t.Fatalf("loading persisted session: ok=%v err=%v", ok, err)
	}
	if got := len(persisted.Messages()); got != 1 {
		t.Fatalf("persisted session has %d messages, want 1", got)
	}
}
