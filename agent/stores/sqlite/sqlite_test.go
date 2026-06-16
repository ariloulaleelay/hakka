package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/you/hakka/agent"
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
