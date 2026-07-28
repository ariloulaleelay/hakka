package agent

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreCRUD(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	s := NewSession("testns", "")
	if err := store.Put(ctx, "testns", s); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, ok, err := store.Get(ctx, "testns", s.SessionID())
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if got.SessionID() != s.SessionID() {
		t.Fatalf("id mismatch")
	}

	if err := store.Delete(ctx, "testns", s.SessionID()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "testns", s.SessionID()); ok {
		t.Fatal("expected miss after delete")
	}
}

func TestMemoryStoreMissing(t *testing.T) {
	store := NewMemoryStore()
	_, ok, err := store.Get(context.Background(), "testns", "nope")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatal("expected miss")
	}
}

func TestMemoryStoreList_OrderedByUpdatedAt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now().Truncate(time.Second)

	// Create sessions with different creation times
	// List returns most recently updated first.
	s1 := NewSession("testns", "")
	s1.Update(func(d *SessionData) { d.CreatedAt = now.Add(-2 * time.Hour) }) // oldest
	if err := store.Put(ctx, "testns", s1); err != nil {
		t.Fatalf("put s1: %v", err)
	}

	s2 := NewSession("testns", "")
	s2.Update(func(d *SessionData) { d.CreatedAt = now.Add(-1 * time.Hour) }) // middle
	if err := store.Put(ctx, "testns", s2); err != nil {
		t.Fatalf("put s2: %v", err)
	}

	s3 := NewSession("testns", "")
	s3.Update(func(d *SessionData) { d.CreatedAt = now }) // newest
	if err := store.Put(ctx, "testns", s3); err != nil {
		t.Fatalf("put s3: %v", err)
	}

	// All three were Put in order (s1, s2, s3), so UpdatedAt for
	// s3 is the most recent — it should appear first.
	sessions, err := store.List(ctx, "testns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	if sessions[0].SessionID() != s3.SessionID() {
		t.Fatalf("expected first session to be s3 (most recently updated), got ID=%q", sessions[0].SessionID())
	}
	if sessions[1].SessionID() != s2.SessionID() {
		t.Fatalf("expected second session to be s2, got ID=%q", sessions[1].SessionID())
	}
	if sessions[2].SessionID() != s1.SessionID() {
		t.Fatalf("expected third session to be s1, got ID=%q", sessions[2].SessionID())
	}

	// Now update s1 (re-Put with a new message) — it should move to the front
	s1.Append(Message{Role: RoleUser, Content: "new message"})
	if err := store.Put(ctx, "testns", s1); err != nil {
		t.Fatalf("put s1 again: %v", err)
	}
	sessions, err = store.List(ctx, "testns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if sessions[0].SessionID() != s1.SessionID() {
		t.Fatalf("expected first session to be s1 (most recently updated), got ID=%q", sessions[0].SessionID())
	}
}
