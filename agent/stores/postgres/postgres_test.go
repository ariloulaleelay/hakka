package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// pgDSN returns a PostgreSQL DSN from the TEST_POSTGRES_DSN env var,
// or skips the test if not set.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping PostgreSQL tests")
	}
	return dsn
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(pgDSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		// Clean up all test data.
		ctx := context.Background()
		_, _ = s.db.ExecContext(ctx, `DELETE FROM messages`)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions`)
		_ = s.Close()
	})
	return s
}

// ---------------------------------------------------------------------------
// Basic round-trip
// ---------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
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
	got, ok, err := s.Get(ctx, ns, sess.SessionID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}
	if got.Read().SystemPrompt != "you are tested" {
		t.Fatalf("prompt: %q", got.Read().SystemPrompt)
	}
	if len(got.Messages()) != 2 || got.Messages()[1].Content != "hello" {
		t.Fatalf("messages: %+v", got.Messages())
	}
	if got.GetModel() != "gpt4" {
		t.Fatalf("model: %q", got.GetModel())
	}
}

func TestUpsert(t *testing.T) {
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
	got, ok, _ := s.Get(ctx, ns, sess.SessionID())
	if !ok || len(got.Messages()) != 1 {
		t.Fatalf("expected single message after upsert, got %+v", got)
	}
}

func TestMissing(t *testing.T) {
	s := newStore(t)
	_, ok, err := s.Get(context.Background(), "testns", "no-such-id")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"
	sess := agent.NewSession(ns, "p")
	_ = s.Put(ctx, ns, sess)

	if err := s.Delete(ctx, ns, sess.SessionID()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, _ := s.Get(ctx, ns, sess.SessionID())
	if ok {
		t.Fatal("expected session gone")
	}

	// Verify messages were also deleted (CASCADE).
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_ns = $1 AND session_id = $2`,
		ns, sess.SessionID()).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 messages after delete, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

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

	sessions, err := s.List(ctx, ns)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].SessionID() != t2.SessionID() {
		t.Fatalf("expected first session to be %q (most recently updated), got %q",
			t2.SessionID(), sessions[0].SessionID())
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
	if got[0].SessionID() != s1.SessionID() {
		t.Fatalf("expected session %q, got %q", s1.SessionID(), got[0].SessionID())
	}
}

// ---------------------------------------------------------------------------
// Messages with tool calls
// ---------------------------------------------------------------------------

func TestMessages_with_tool_calls_round_trip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "tc-ns"

	sess := agent.NewSession(ns, "prompt")
	sess.Append(agent.Message{
		Role:    agent.RoleAssistant,
		Content: "let me check",
		ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"/tmp/x"}`},
		},
	})
	sess.Append(agent.Message{
		Role:       agent.RoleTool,
		Content:    "file contents here",
		ToolCallID: "call_1",
	})

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, ns, sess.SessionID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("not found")
	}

	msgs := got.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool call not preserved: %+v", msgs[0].ToolCalls)
	}
	if msgs[1].ToolCallID != "call_1" {
		t.Fatalf("tool_call_id not preserved: %q", msgs[1].ToolCallID)
	}
}

// ---------------------------------------------------------------------------
// Store implements SessionStore
// ---------------------------------------------------------------------------

func TestStore_implements_SessionStore(t *testing.T) {
	s := newStore(t)
	var _ agent.SessionStore = s
}
