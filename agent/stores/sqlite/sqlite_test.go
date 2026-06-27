package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ariloulaleelay/hakka/agent"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hakka.db")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Basic round-trip (existing tests)
// ---------------------------------------------------------------------------

func TestSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"

	sess := agent.NewSession(ns, "you are tested")
	sess.Append(agent.Message{Role: agent.RoleUser, Content: "hi"})
	sess.Append(agent.Message{Role: agent.RoleAssistant, Content: "hello"})
	sess.SetModel("gpt4")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.Get(ctx, ns, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}
	if got.SystemPrompt != "you are tested" {
		t.Fatalf("prompt: %q", got.SystemPrompt)
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "hello" {
		t.Fatalf("messages: %+v", got.Messages)
	}
	if got.GetModel() != "gpt4" {
		t.Fatalf("model: %q", got.GetModel())
	}
}

func TestSQLiteUpsert(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"

	sess := agent.NewSession(ns, "p")
	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("put: %v", err)
	}
	sess.Append(agent.Message{Role: agent.RoleUser, Content: "ping"})
	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("put2: %v", err)
	}
	got, ok, _ := s.Get(ctx, ns, sess.ID)
	if !ok || len(got.Messages) != 1 {
		t.Fatalf("expected single message after upsert, got %+v", got)
	}
}

func TestSQLiteMissing(t *testing.T) {
	s := newStore(t)
	_, ok, err := s.Get(context.Background(), "testns", "no-such-id")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestSQLiteTokenUsageRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"

	sess := agent.NewSession(ns, "p")
	sess.AddTokenUsage(42)
	sess.AddTokenUsage(100)

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, _, _ := s.Get(ctx, ns, sess.ID)
	if got.TotalTokenUsage() != 142 {
		t.Fatalf("expected 142 total tokens, got %d", got.TotalTokenUsage())
	}
}

func TestSQLiteDelete(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"
	sess := agent.NewSession(ns, "p")
	_ = s.Put(ctx, ns, sess)
	if err := s.Delete(ctx, ns, sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, _ := s.Get(ctx, ns, sess.ID)
	if ok {
		t.Fatal("expected session gone")
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestList_returns_empty_when_namespace_has_no_sessions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	sessions, err := s.List(ctx, "empty-ns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestList_returns_sessions_in_update_order(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "ordered-ns"

	t1 := agent.NewSession(ns, "first")
	t2 := agent.NewSession(ns, "second")

	if err := s.Put(ctx, ns, t1); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := s.Put(ctx, ns, t2); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	// Both sessions were inserted at different times (now), so the most
	// recently inserted (t2) should be first when ordering by updated_at DESC.
	sessions, err := s.List(ctx, ns)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != t2.ID {
		t.Fatalf("expected first session to be %q (most recently updated), got %q", t2.ID, sessions[0].ID)
	}
	if sessions[1].ID != t1.ID {
		t.Fatalf("expected second session to be %q, got %q", t1.ID, sessions[1].ID)
	}

	// Update t1 (re-put) — it should now be first
	t1.Append(agent.Message{Role: agent.RoleUser, Content: "new"})
	if err := s.Put(ctx, ns, t1); err != nil {
		t.Fatalf("Put first updated: %v", err)
	}
	sessions, err = s.List(ctx, ns)
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if sessions[0].ID != t1.ID {
		t.Fatalf("expected first session to be %q (most recently updated), got %q", t1.ID, sessions[0].ID)
	}
}

func TestList_does_not_see_sessions_from_other_namespaces(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	s1 := agent.NewSession("ns1", "p1")
	s2 := agent.NewSession("ns2", "p2")

	if err := s.Put(ctx, "ns1", s1); err != nil {
		t.Fatalf("Put ns1: %v", err)
	}
	if err := s.Put(ctx, "ns2", s2); err != nil {
		t.Fatalf("Put ns2: %v", err)
	}

	got, err := s.List(ctx, "ns1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 session in ns1, got %d", len(got))
	}
	if got[0].ID != s1.ID {
		t.Fatalf("expected session %q, got %q", s1.ID, got[0].ID)
	}
}

// ---------------------------------------------------------------------------
// EnabledTools round-trip
// ---------------------------------------------------------------------------

func TestEnabledTools_are_preserved_across_Put_and_Get(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "tools-ns"

	sess := agent.NewSession(ns, "you are a tool user")
	sess.EnableTool("read_file")
	sess.EnableTool("search")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, ns, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}

	if !got.IsToolEnabled("read_file") {
		t.Errorf("expected read_file to be enabled")
	}
	if !got.IsToolEnabled("search") {
		t.Errorf("expected search to be enabled")
	}
	if got.IsToolEnabled("write_file") {
		t.Errorf("expected write_file to NOT be enabled")
	}
}

func TestEnabledTools_backward_compat_with_broken_json_yields_nil(t *testing.T) {
	// Given a session with broken enabled_tools JSON in the database
	// (simulating data written by an older or corrupted version)
	ctx := context.Background()
	s := newStore(t)
	ns := "backward-ns"
	sess := agent.NewSession(ns, "backward compat")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Corrupt the enabled_tools column directly
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET enabled_tools = 'not-valid-json' WHERE id = ?`, sess.ID)
	if err != nil {
		t.Fatalf("corrupt enabled_tools: %v", err)
	}

	// When we read the session
	got, ok, err := s.Get(ctx, ns, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}

	// Then EnabledTools should be nil (backward compat fallback)
	if got.EnabledTools != nil {
		t.Errorf("expected nil EnabledTools for broken JSON, got %v", got.EnabledTools)
	}
}

// ---------------------------------------------------------------------------
// Get — corrupted data
// ---------------------------------------------------------------------------

func TestGet_returns_error_for_corrupted_messages_JSON(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "corrupt-msg"
	sess := agent.NewSession(ns, "p")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Overwrite messages with invalid JSON
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET messages = '{broken json' WHERE id = ?`, sess.ID)
	if err != nil {
		t.Fatalf("corrupt messages: %v", err)
	}

	// When we read the session, Get should return an error
	_, _, err = s.Get(ctx, ns, sess.ID)
	if err == nil {
		t.Fatal("expected error for corrupted messages, got nil")
	}
}

func TestGet_returns_error_for_corrupted_created_at(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "corrupt-ca"
	sess := agent.NewSession(ns, "p")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Overwrite created_at with an unparseable value
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET created_at = 'not-a-date' WHERE id = ?`, sess.ID)
	if err != nil {
		t.Fatalf("corrupt created_at: %v", err)
	}

	// When we read the session, Get should return an error
	_, _, err = s.Get(ctx, ns, sess.ID)
	if err == nil {
		t.Fatal("expected error for corrupted created_at, got nil")
	}
}

// ---------------------------------------------------------------------------
// runMigration — idempotency
// ---------------------------------------------------------------------------

func TestOpen_is_idempotent_on_the_same_database_file(t *testing.T) {
	// Given a database that was already opened and migrated once
	path := filepath.Join(t.TempDir(), "idempotent.db")

	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = st1.Close()

	// When we open the same file again (migrations re-run against
	// existing columns — "duplicate column" errors)
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = st2.Close()

	// Then both opens succeeded — duplicate column errors were silently
	// ignored by runMigration
}
