package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

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
// Basic round-trip
// ---------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"

	sess := agent.NewSession(ns, "you are tested")
	sess.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "hi"}, agent.Message{Role: agent.RoleAssistant, Content: "hello"}}, 0, 0)
	sess.SetModel(context.Background(), "gpt4")

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
	if err := sess.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "ping"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
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

func TestTokenUsageRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "testns"

	sess := agent.NewSession(ns, "p")
	sess.AddTokenUsage(42)
	sess.AddTokenUsage(100)

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, _, _ := s.Get(ctx, ns, sess.SessionID())
	if got.TotalTokenUsage() != 142 {
		t.Fatalf("expected 142 total tokens, got %d", got.TotalTokenUsage())
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
		`SELECT COUNT(*) FROM messages WHERE session_ns = ? AND session_id = ?`,
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
	if sessions[1].SessionID() != t1.SessionID() {
		t.Fatalf("expected second session to be %q, got %q",
			t1.SessionID(), sessions[1].SessionID())
	}

	// Update t1 (re-put) — it should now be first.
	if err := t1.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "new"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, ns, t1); err != nil {
		t.Fatalf("Put first updated: %v", err)
	}
	sessions, err = s.List(ctx, ns)
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if sessions[0].SessionID() != t1.SessionID() {
		t.Fatalf("expected first session to be %q (most recently updated), got %q",
			t1.SessionID(), sessions[0].SessionID())
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
// EnabledTools round-trip
// ---------------------------------------------------------------------------

func TestEnabledTools_are_preserved_across_Put_and_Get(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "tools-ns"

	sess := agent.NewSession(ns, "you are a tool user")
	sess.EnableTool(context.Background(), "read_file")
	sess.EnableTool(context.Background(), "search")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, ns, sess.SessionID())
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
	ctx := context.Background()
	s := newStore(t)
	ns := "backward-ns"
	sess := agent.NewSession(ns, "backward compat")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET enabled_tools = 'not-valid-json' WHERE id = ?`, sess.SessionID())
	if err != nil {
		t.Fatalf("corrupt enabled_tools: %v", err)
	}

	got, ok, err := s.Get(ctx, ns, sess.SessionID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}
	if got.Read().EnabledTools != nil {
		t.Errorf("expected nil EnabledTools for broken JSON, got %v", got.Read().EnabledTools)
	}
}

// ---------------------------------------------------------------------------
// Corrupted data
// ---------------------------------------------------------------------------

func TestGet_returns_error_for_corrupted_created_at(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "corrupt-ca"
	sess := agent.NewSession(ns, "p")

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET created_at = 'not-a-date' WHERE id = ?`, sess.SessionID())
	if err != nil {
		t.Fatalf("corrupt created_at: %v", err)
	}

	_, _, err = s.Get(ctx, ns, sess.SessionID())
	if err == nil {
		t.Fatal("expected error for corrupted created_at, got nil")
	}
}

// ---------------------------------------------------------------------------
// Idempotent Open
// ---------------------------------------------------------------------------

func TestOpen_is_idempotent_on_the_same_database_file(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")

	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = st1.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = st2.Close()
}

// ---------------------------------------------------------------------------
// Message-level persistence (new split schema)
// ---------------------------------------------------------------------------

func TestMessages_are_stored_in_dedicated_table(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "msg-ns"

	sess := agent.NewSession(ns, "test prompt")
	sess.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "hello"}, agent.Message{Role: agent.RoleAssistant, Content: "world", FinishReason: "stop"}}, 0, 0)

	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify messages table directly.
	rows, err := s.db.QueryContext(ctx,
		`SELECT msg_index, role, content, finish_reason
		 FROM messages WHERE session_ns = ? AND session_id = ?
		 ORDER BY msg_index`, ns, sess.SessionID())
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()

	var msgs []struct {
		idx           int
		role, content string
		finishReason  sql.NullString
	}
	for rows.Next() {
		var m struct {
			idx           int
			role, content string
			finishReason  sql.NullString
		}
		if err := rows.Scan(&m.idx, &m.role, &m.content, &m.finishReason); err != nil {
			t.Fatalf("scan: %v", err)
		}
		msgs = append(msgs, m)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in table, got %d", len(msgs))
	}
	if msgs[1].role != "assistant" || msgs[1].content != "world" {
		t.Fatalf("wrong message: %+v", msgs[1])
	}
	if msgs[1].finishReason.String != "stop" {
		t.Fatalf("expected finish_reason=stop, got %q", msgs[1].finishReason.String)
	}
}

func TestMessages_with_tool_calls_round_trip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "tc-ns"

	sess := agent.NewSession(ns, "prompt")
	sess.AddMessages(context.Background(), []agent.Message{agent.Message{
		Role:    agent.RoleAssistant,
		Content: "let me check",
		ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"/tmp/x"}`},
		},
	}}, 0, 0)
	sess.AddMessages(context.Background(), []agent.Message{agent.Message{
		Role:       agent.RoleTool,
		Content:    "file contents here",
		ToolCallID: "call_1",
	}}, 0, 0)

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

func TestUpsert_replaces_messages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "upsert-msgs"

	sess := agent.NewSession(ns, "p")
	if err := sess.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "v1"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put v1: %v", err)
	}

	// Replace with different messages.
	sess2, _, _ := s.Get(ctx, ns, sess.SessionID())
	// Clear and re-add via Update.
	sess2.Update(func(d *agent.SessionData) {
		d.Messages = []agent.Message{
			{Role: agent.RoleUser, Content: "v2"},
			{Role: agent.RoleAssistant, Content: "response"},
		}
	})
	if err := s.Put(ctx, ns, sess2); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	got, _, _ := s.Get(ctx, ns, sess.SessionID())
	msgs := got.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after upsert, got %d", len(msgs))
	}
	if msgs[0].Content != "v2" || msgs[1].Content != "response" {
		t.Fatalf("wrong messages: %+v", msgs)
	}
}

// ---------------------------------------------------------------------------
// Migration from old schema
// ---------------------------------------------------------------------------

// createOldSchemaDB creates a SQLite database with the old pre-split schema
// (messages as JSON TEXT column in sessions) and populates it with test data.
func createOldSchemaDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer db.Close()

	// Old schema — messages as a TEXT column in sessions.
	oldSchema := `
	CREATE TABLE IF NOT EXISTS sessions (
		namespace     TEXT NOT NULL,
		id            TEXT NOT NULL,
		system_prompt TEXT NOT NULL,
		messages      TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL DEFAULT '',
		client_cwd    TEXT NOT NULL DEFAULT '',
		model         TEXT NOT NULL DEFAULT '',
		total_tokens  INTEGER NOT NULL DEFAULT 0,
		name          TEXT NOT NULL DEFAULT '',
		enabled_tools TEXT NOT NULL DEFAULT '',
		blocked_tools TEXT NOT NULL DEFAULT '',
		compact_soft_limit INTEGER NOT NULL DEFAULT 200000,
		estimated_context_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost    REAL NOT NULL DEFAULT 0,
		active_skills TEXT NOT NULL DEFAULT '[]',
		streaming     INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (namespace, id)
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at);
	`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	// Insert a session with messages as JSON blob.
	messages := []agent.Message{
		{Role: agent.RoleSystem, Content: "sys msg"},
		{Role: agent.RoleUser, Content: "hi there"},
		{Role: agent.RoleAssistant, Content: "hello!", ToolCalls: []agent.ToolCall{
			{ID: "t1", Name: "search", Arguments: `{"q":"test"}`},
		}},
		{Role: agent.RoleTool, Content: "result", ToolCallID: "t1"},
	}
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO sessions (namespace, id, system_prompt, messages, created_at, updated_at, model, total_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"oldns", "old-id-123", "old system prompt",
		string(messagesJSON),
		"2024-01-15T10:00:00Z", "2024-01-15T11:00:00Z",
		"gpt4", 42,
	)
	if err != nil {
		t.Fatalf("insert old session: %v", err)
	}
}

func TestMigration_moves_messages_to_dedicated_table(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	createOldSchemaDB(t, path)

	// Open with new code — should migrate automatically.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (with migration): %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Verify session was migrated.
	got, ok, err := s.Get(ctx, "oldns", "old-id-123")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if !ok {
		t.Fatal("session not found after migration")
	}

	data := got.Read()
	if data.SystemPrompt != "old system prompt" {
		t.Fatalf("wrong system prompt: %q", data.SystemPrompt)
	}
	if data.Model != "gpt4" {
		t.Fatalf("wrong model: %q", data.Model)
	}
	if data.TotalTokens != 42 {
		t.Fatalf("wrong total_tokens: %d", data.TotalTokens)
	}

	// Verify messages were migrated.
	msgs := got.Messages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after migration, got %d", len(msgs))
	}
	if msgs[0].Role != agent.RoleSystem || msgs[0].Content != "sys msg" {
		t.Fatalf("wrong msg[0]: %+v", msgs[0])
	}
	if msgs[1].Role != agent.RoleUser || msgs[1].Content != "hi there" {
		t.Fatalf("wrong msg[1]: %+v", msgs[1])
	}
	if msgs[2].Role != agent.RoleAssistant || msgs[2].Content != "hello!" {
		t.Fatalf("wrong msg[2]: %+v", msgs[2])
	}
	if len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Name != "search" {
		t.Fatalf("tool call not migrated: %+v", msgs[2].ToolCalls)
	}
	if msgs[3].Role != agent.RoleTool || msgs[3].ToolCallID != "t1" {
		t.Fatalf("wrong msg[3]: %+v", msgs[3])
	}

	// Verify messages are in the dedicated table.
	var msgCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_ns = 'oldns' AND session_id = 'old-id-123'`,
	).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 4 {
		t.Fatalf("expected 4 messages in messages table, got %d", msgCount)
	}

	// Verify old messages column is gone from sessions table.
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, coltype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &coltype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "messages" {
			t.Fatal("old 'messages' column still exists in sessions table")
		}
	}
}

func TestMigration_is_idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	createOldSchemaDB(t, path)

	// First open — migrates.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// Second open — should be a no-op (already migrated).
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	ctx := context.Background()
	got, ok, err := s2.Get(ctx, "oldns", "old-id-123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("session not found")
	}
	if len(got.Messages()) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got.Messages()))
	}
}

func TestMigration_empty_database_is_fine(t *testing.T) {
	// A brand-new database never had the old schema — Open should work.
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer s.Close()

	// Just verify we can use it.
	ctx := context.Background()
	sess := agent.NewSession("ns", "test")
	if err := s.Put(ctx, "ns", sess); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _, _ := s.Get(ctx, "ns", sess.SessionID())
	if got == nil {
		t.Fatal("expected session")
	}
}

func TestMigration_database_with_only_new_sessions(t *testing.T) {
	// Test that Open works when sessions table exists but has no messages column.
	// This simulates a DB that was migrated but the messages column hasn't been dropped.
	path := filepath.Join(t.TempDir(), "partial.db")

	// Create DB using OLD code path, then manually drop the messages column
	// to simulate a partially-migrated state.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Create old-style table first.
	oldSchema := `
	CREATE TABLE IF NOT EXISTS sessions (
		namespace     TEXT NOT NULL,
		id            TEXT NOT NULL,
		system_prompt TEXT NOT NULL,
		messages      TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL DEFAULT '',
		client_cwd    TEXT NOT NULL DEFAULT '',
		model         TEXT NOT NULL DEFAULT '',
		total_tokens  INTEGER NOT NULL DEFAULT 0,
		name          TEXT NOT NULL DEFAULT '',
		enabled_tools TEXT NOT NULL DEFAULT '',
		blocked_tools TEXT NOT NULL DEFAULT '',
		compact_soft_limit INTEGER NOT NULL DEFAULT 200000,
		estimated_context_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost    REAL NOT NULL DEFAULT 0,
		active_skills TEXT NOT NULL DEFAULT '[]',
		streaming     INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (namespace, id)
	)`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old: %v", err)
	}

	// Delete the messages column to simulate a partial migration.
	// (SQLite 3.35.0+ supports DROP COLUMN.)
	if _, err := db.Exec(`ALTER TABLE sessions DROP COLUMN messages`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	db.Close()

	// Now open with new code — should work without trying to migrate.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	needsMig, err := s.NeedsMigration(context.Background())
	if err != nil {
		t.Fatalf("NeedsMigration: %v", err)
	}
	if needsMig {
		t.Fatal("expected NeedsMigration=false when messages column is already gone")
	}
}

// Test that the old file-based migration tests from the original sqlite.go
// (runMigration) still work in the sense that duplicate column errors are
// handled gracefully. The new code uses separate tables so it's not needed,
// but we keep backwards compatibility in spirit.
func TestOpen_handles_existing_database_gracefully(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")

	// Create a DB with just the sessions table (new schema, no messages table).
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (namespace TEXT, id TEXT, system_prompt TEXT)`)

	// Also create the messages table to simulate a fully-migrated DB.
	_, err = db.Exec(`CREATE TABLE messages (session_ns TEXT, session_id TEXT, msg_index INTEGER)`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	db.Close()

	// Opening should work.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()
}

// ---------------------------------------------------------------------------
// Verify FK constraint (messages deleted with session)
// ---------------------------------------------------------------------------

func TestDelete_cascades_to_messages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ns := "cascade-ns"

	sess := agent.NewSession(ns, "test")
	sess.AddMessages(context.Background(), []agent.Message{agent.Message{Role: agent.RoleUser, Content: "msg1"}, agent.Message{Role: agent.RoleAssistant, Content: "msg2"}}, 0, 0)
	if err := s.Put(ctx, ns, sess); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify messages exist.
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_ns = ? AND session_id = ?`,
		ns, sess.SessionID()).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 messages before delete, got %d", count)
	}

	// Delete session.
	if err := s.Delete(ctx, ns, sess.SessionID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify messages are gone.
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_ns = ? AND session_id = ?`,
		ns, sess.SessionID()).Scan(&count); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 messages after cascade delete, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Test that NeedsMigration returns false for new databases
// ---------------------------------------------------------------------------

func TestNeedsMigration_new_database_returns_false(t *testing.T) {
	s := newStore(t)
	needs, err := s.NeedsMigration(context.Background())
	if err != nil {
		t.Fatalf("NeedsMigration: %v", err)
	}
	if needs {
		t.Fatal("expected NeedsMigration=false for new database")
	}
}

// ---------------------------------------------------------------------------
// Old migration file removal test — verify the old runMigration doesn't exist
// (it was an internal function in the old sqlite.go that no longer exists)
// ---------------------------------------------------------------------------

func TestStore_implements_SessionStore(t *testing.T) {
	// Compile-time check lives in sqlstore, but we double-check at test time.
	s := newStore(t)
	var _ agent.SessionStore = s
	// If we got here, the assertion holds.
}
