package agent

import (
	"context"
	"testing"
)

func TestSessionManagerCreatesWithGivenID(t *testing.T) {
	sm := NewSessionManager(nil, "sys")
	s, err := sm.CreateWithID(context.Background(), "testns", "fixed-id")
	if err != nil {
		t.Fatalf("get/create: %v", err)
	}
	if s.SessionID() != "fixed-id" {
		t.Fatalf("expected id 'fixed-id', got %q", s.SessionID())
	}
	if s.Read().Namespace != "testns" {
		t.Fatalf("expected namespace 'testns', got %q", s.Read().Namespace)
	}
	if s.Read().SystemPrompt != "sys" {
		t.Fatalf("system prompt not propagated")
	}
}

func TestSessionManagerReusesExisting(t *testing.T) {
	sm := NewSessionManager(nil, "")
	ctx := context.Background()

	a, _ := sm.CreateWithID(ctx, "testns", "")
	if err := a.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "marker"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(ctx, "testns", a); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := sm.Get(ctx, "testns", a.SessionID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if b.SessionID() != a.SessionID() {
		t.Fatal("expected same session ID")
	}
	if len(b.Messages()) != 1 || b.Messages()[0].Content != "marker" {
		t.Fatalf("messages not retained: %+v", b.Messages())
	}
}

func TestSessionManagerDrop(t *testing.T) {
	sm := NewSessionManager(nil, "")
	ctx := context.Background()
	s, _ := sm.CreateWithID(ctx, "testns", "to-drop")
	if err := sm.Drop(ctx, "testns", s.SessionID()); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// next CreateWithID with the same id should create a brand new session
	again, _ := sm.CreateWithID(ctx, "testns", "to-drop")
	if len(again.Messages()) != 0 {
		t.Fatal("dropped session leaked messages")
	}
}
