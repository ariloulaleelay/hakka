package agent

import (
	"context"
	"testing"
)

func TestSessionManagerCreatesWithGivenID(t *testing.T) {
	sm := NewSessionManager(nil, "sys")
	s, err := sm.GetOrCreate(context.Background(), "testns", "fixed-id")
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

	a, _ := sm.GetOrCreate(ctx, "testns", "")
	a.Append(Message{Role: RoleUser, Content: "marker"})
	if err := sm.Save(ctx, "testns", a); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := sm.GetOrCreate(ctx, "testns", a.SessionID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if b != a {
		t.Fatal("expected same session pointer from memory store")
	}
	if len(b.AllMessages()) != 1 || b.AllMessages()[0].Content != "marker" {
		t.Fatalf("messages not retained: %+v", b.AllMessages())
	}
}

func TestSessionManagerDrop(t *testing.T) {
	sm := NewSessionManager(nil, "")
	ctx := context.Background()
	s, _ := sm.GetOrCreate(ctx, "testns", "to-drop")
	if err := sm.Drop(ctx, "testns", s.SessionID()); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// next GetOrCreate with the same id should create a brand new session
	again, _ := sm.GetOrCreate(ctx, "testns", "to-drop")
	if len(again.AllMessages()) != 0 {
		t.Fatal("dropped session leaked messages")
	}
}
