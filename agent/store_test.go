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

	got, ok, err := store.Get(ctx, "testns", s.ID)
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if got.ID != s.ID {
		t.Fatalf("id mismatch")
	}

	if err := store.Delete(ctx, "testns", s.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "testns", s.ID); ok {
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

func TestMemoryStoreList_OrderedByCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now().Truncate(time.Second)

	// Create sessions with different creation times
	s1 := NewSession("testns", "")
	s1.CreatedAt = now.Add(-2 * time.Hour) // oldest
	if err := store.Put(ctx, "testns", s1); err != nil {
		t.Fatalf("put s1: %v", err)
	}

	s2 := NewSession("testns", "")
	s2.CreatedAt = now.Add(-1 * time.Hour) // middle
	if err := store.Put(ctx, "testns", s2); err != nil {
		t.Fatalf("put s2: %v", err)
	}

	s3 := NewSession("testns", "")
	s3.CreatedAt = now // newest
	if err := store.Put(ctx, "testns", s3); err != nil {
		t.Fatalf("put s3: %v", err)
	}

	sessions, err := store.List(ctx, "testns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// oldest first
	if sessions[0].ID != s1.ID {
		t.Fatalf("expected first session to be oldest (s1), got ID=%q", sessions[0].ID)
	}
	if sessions[1].ID != s2.ID {
		t.Fatalf("expected second session to be middle (s2), got ID=%q", sessions[1].ID)
	}
	if sessions[2].ID != s3.ID {
		t.Fatalf("expected third session to be newest (s3), got ID=%q", sessions[2].ID)
	}

	// Verify CreatedAt ordering
	if !sessions[0].CreatedAt.Before(sessions[1].CreatedAt) {
		t.Fatal("sessions[0].CreatedAt should be before sessions[1].CreatedAt")
	}
	if !sessions[1].CreatedAt.Before(sessions[2].CreatedAt) {
		t.Fatal("sessions[1].CreatedAt should be before sessions[2].CreatedAt")
	}
}
