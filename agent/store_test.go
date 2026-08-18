package agent

import (
	"context"
	"fmt"
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
	if err := s1.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "new message"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
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

// ---------------------------------------------------------------------------
// AppendMessages + PatchMeta tests
// ---------------------------------------------------------------------------

// runAppendPatchTests exercises AppendMessages and PatchMeta on any SessionStore.
func runAppendPatchTests(t *testing.T, store SessionStore) {
	t.Helper()
	ctx := context.Background()
	ns := "testns"

	// Create a fresh session.
	s := NewSession(ns, "you are a helpful assistant")
	if err := store.Put(ctx, ns, s); err != nil {
		t.Fatalf("put: %v", err)
	}
	sid := s.SessionID()

	// --- AppendMessages: add messages + token/cost deltas ---

	msg1 := Message{Role: RoleUser, Content: "hello", ID: "m1"}
	msg2 := Message{Role: RoleAssistant, Content: "hi there", ID: "m2"}
	err := store.AppendMessages(ctx, ns, sid, []Message{msg1, msg2}, 50, 0.001)
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Read back and verify messages.
	got, ok, err := store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get after AppendMessages: ok=%v err=%v", ok, err)
	}
	msgs := got.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("msg[0].Content = %q, want %q", msgs[0].Content, "hello")
	}
	if msgs[1].Content != "hi there" {
		t.Fatalf("msg[1].Content = %q, want %q", msgs[1].Content, "hi there")
	}
	if got.TotalTokenUsage() != 50 {
		t.Fatalf("TotalTokens = %d, want 50", got.TotalTokenUsage())
	}
	if got.TotalCost() != 0.001 {
		t.Fatalf("TotalCost = %f, want 0.001", got.TotalCost())
	}
	oldUpdatedAt := got.Read().UpdatedAt

	// --- AppendMoreMessages: append another batch ---

	msg3 := Message{Role: RoleUser, Content: "second turn", ID: "m3"}
	err = store.AppendMessages(ctx, ns, sid, []Message{msg3}, 30, 0.002)
	if err != nil {
		t.Fatalf("AppendMessages (#2): %v", err)
	}

	got, ok, err = store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get after AppendMessages #2: ok=%v err=%v", ok, err)
	}
	msgs = got.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[2].Content != "second turn" {
		t.Fatalf("msg[2].Content = %q, want %q", msgs[2].Content, "second turn")
	}
	if got.TotalTokenUsage() != 80 {
		t.Fatalf("TotalTokens = %d, want 80", got.TotalTokenUsage())
	}
	if got.TotalCost() != 0.003 {
		t.Fatalf("TotalCost = %f, want 0.003", got.TotalCost())
	}
	if !got.Read().UpdatedAt.After(oldUpdatedAt) {
		t.Fatal("expected UpdatedAt to advance after AppendMessages")
	}

	// --- PatchMeta: update name ---

	newName := "my-renamed-session"
	err = store.PatchMeta(ctx, ns, sid, &SessionMetaPatch{Name: &newName})
	if err != nil {
		t.Fatalf("PatchMeta name: %v", err)
	}

	got, ok, err = store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get after PatchMeta: ok=%v err=%v", ok, err)
	}
	if got.SessionName() != newName {
		t.Fatalf("SessionName() = %q, want %q", got.SessionName(), newName)
	}
	// Messages should not be affected.
	if len(got.Messages()) != 3 {
		t.Fatalf("expected 3 messages after PatchMeta, got %d", len(got.Messages()))
	}

	// --- PatchMeta: update model ---

	newModel := "gpt-5"
	err = store.PatchMeta(ctx, ns, sid, &SessionMetaPatch{Model: &newModel})
	if err != nil {
		t.Fatalf("PatchMeta model: %v", err)
	}
	got, ok, err = store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get after PatchMeta model: ok=%v err=%v", ok, err)
	}
	if got.GetModel() != newModel {
		t.Fatalf("Model = %q, want %q", got.GetModel(), newModel)
	}
	// Name should still be there (not overwritten).
	if got.SessionName() != newName {
		t.Fatalf("SessionName() = %q after model patch, want %q", got.SessionName(), newName)
	}

	// --- PatchMeta: multiple fields at once ---

	newName2 := "another-name"
	newCWD := "/tmp/test"
	err = store.PatchMeta(ctx, ns, sid, &SessionMetaPatch{
		Name:      &newName2,
		ClientCWD: &newCWD,
	})
	if err != nil {
		t.Fatalf("PatchMeta multi: %v", err)
	}
	got, ok, err = store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get after multi PatchMeta: ok=%v err=%v", ok, err)
	}
	if got.SessionName() != newName2 {
		t.Fatalf("SessionName() = %q, want %q", got.SessionName(), newName2)
	}
	if got.Read().ClientCWD != newCWD {
		t.Fatalf("ClientCWD = %q, want %q", got.Read().ClientCWD, newCWD)
	}
	// Model should survive.
	if got.GetModel() != newModel {
		t.Fatalf("Model = %q, want %q", got.GetModel(), newModel)
	}

	// --- PatchMeta: nil patch (no-op, should not error) ---
	err = store.PatchMeta(ctx, ns, sid, nil)
	if err != nil {
		t.Fatalf("PatchMeta nil: %v", err)
	}

	// --- PatchMeta: empty patch (no fields set) ---
	err = store.PatchMeta(ctx, ns, sid, &SessionMetaPatch{})
	if err != nil {
		t.Fatalf("PatchMeta empty: %v", err)
	}

	// --- PatchMeta on non-existent session ---
	err = store.PatchMeta(ctx, ns, "nosuchid", &SessionMetaPatch{Name: &newName})
	if err == nil {
		t.Fatal("expected error patching non-existent session")
	}

	// --- AppendMessages on non-existent session ---
	err = store.AppendMessages(ctx, ns, "nosuchid", []Message{msg1}, 0, 0)
	if err == nil {
		t.Fatal("expected error appending to non-existent session")
	}
}

func TestMemoryStore_AppendMessages_PatchMeta(t *testing.T) {
	runAppendPatchTests(t, NewMemoryStore())
}

// TestAppendMessagesAndPatchMetaNoRace verifies that AppendMessages and
// PatchMeta can be called independently without overwriting each other's
// changes (unlike Put which overwrites everything).
func TestAppendMessagesAndPatchMetaNoRace(t *testing.T) {
	ctx := context.Background()
	ns := "testns"
	store := NewMemoryStore()

	s := NewSession(ns, "you are helpful")
	if err := store.Put(ctx, ns, s); err != nil {
		t.Fatalf("put: %v", err)
	}
	sid := s.SessionID()

	// Simulate: LLM calls session_rename (PatchMeta for name), then
	// turn runner saves messages (AppendMessages).
	newName := "renamed-by-llm"
	if err := store.PatchMeta(ctx, ns, sid, &SessionMetaPatch{Name: &newName}); err != nil {
		t.Fatalf("PatchMeta: %v", err)
	}

	// Turn runner appends messages (should NOT overwrite name).
	if err := store.AppendMessages(ctx, ns, sid,
		[]Message{{Role: RoleUser, Content: "turn msg", ID: "t1"}}, 10, 0); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Verify both changes survived.
	got, ok, err := store.Get(ctx, ns, sid)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.SessionName() != newName {
		t.Fatalf("name = %q, want %q — PatchMeta change was overwritten!", got.SessionName(), newName)
	}
	if len(got.Messages()) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got.Messages()))
	}
}

// TestMemoryStoreInterface verifies MemoryStore satisfies the SessionStore interface.
func TestMemoryStoreInterface(t *testing.T) {
	var _ SessionStore = NewMemoryStore()
	// compile-time check — if AppendMessages/PatchMeta are added, this verifies
	// MemoryStore implements them.
}

// TestMemoryStore_AppendMessages_UpdatesUpdatedAt verifies that AppendMessages
// bumps updated_at even without messages.
func TestMemoryStore_AppendMessages_UpdatesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewSession("ns", "")
	if err := store.Put(ctx, "ns", s); err != nil {
		t.Fatalf("put: %v", err)
	}
	sid := s.SessionID()

	oldUA := s.Read().UpdatedAt

	// Append with no messages but with token delta — should still bump updated_at.
	if err := store.AppendMessages(ctx, "ns", sid, nil, 100, 0.01); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	got, _, _ := store.Get(ctx, "ns", sid)
	newUA := got.Read().UpdatedAt
	if !newUA.After(oldUA) {
		t.Fatal("expected UpdatedAt to advance after AppendMessages")
	}
}

// TestAppendMessagesWithDeltaZero verifies AppendMessages works with zero deltas
// and empty messages (e.g., when LLM returns just text with no tool calls).
func TestAppendMessagesWithDeltaZero(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewSession("ns", "")
	if err := store.Put(ctx, "ns", s); err != nil {
		t.Fatalf("put: %v", err)
	}
	sid := s.SessionID()

	if err := store.AppendMessages(ctx, "ns", sid,
		[]Message{{Role: RoleAssistant, Content: "just text", ID: "a1"}}, 0, 0); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	got, _, _ := store.Get(ctx, "ns", sid)
	if len(got.Messages()) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got.Messages()))
	}
	if got.TotalTokenUsage() != 0 {
		t.Fatalf("TotalTokens = %d, want 0", got.TotalTokenUsage())
	}
}

// TestMemoryStoreList_IncludesAppendedMessages verifies List returns sessions
// with messages appended via AppendMessages (not just Put).
func TestMemoryStoreList_IncludesAppendedMessages(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	ns := "listtest"

	for i := 0; i < 3; i++ {
		s := NewSession(ns, "")
		if err := store.Put(ctx, ns, s); err != nil {
			t.Fatalf("put: %v", err)
		}
		// Append messages to each.
		if err := store.AppendMessages(ctx, ns, s.SessionID(),
			[]Message{{
				ID:      fmt.Sprintf("m%d", i),
				Role:    RoleUser,
				Content: fmt.Sprintf("msg %d", i),
			}}, 10, 0); err != nil {
			t.Fatalf("AppendMessages: %v", err)
		}
	}

	sessions, err := store.List(ctx, ns)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}
	for _, s := range sessions {
		if len(s.Messages()) != 1 {
			t.Fatalf("session %s: expected 1 message, got %d", s.SessionID(), len(s.Messages()))
		}
	}
}
