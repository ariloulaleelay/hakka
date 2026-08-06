package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// testSessionStore is a minimal in-memory store for testing.
type testSessionStore struct {
	mu       sync.Mutex
	sessions map[string]agent.SessionData // key = "namespace:id"
}

func newTestStore() *testSessionStore {
	return &testSessionStore{sessions: make(map[string]agent.SessionData)}
}

func (ts *testSessionStore) Get(_ context.Context, namespace, id string) (*agent.Session, bool, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	d, ok := ts.sessions[namespace+":"+id]
	if !ok {
		return nil, false, nil
	}
	cp := d.DeepCopy()
	return agent.NewSessionFromData(&cp), true, nil
}

func (ts *testSessionStore) Put(_ context.Context, namespace string, s *agent.Session) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	data := s.Read()
	data.Namespace = namespace
	ts.sessions[namespace+":"+s.SessionID()] = data
	return nil
}

func (ts *testSessionStore) Delete(_ context.Context, namespace, id string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.sessions, namespace+":"+id)
	return nil
}

func (ts *testSessionStore) List(_ context.Context, namespace string) ([]*agent.Session, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	prefix := namespace + ":"
	var out []*agent.Session
	for key, d := range ts.sessions {
		if strings.HasPrefix(key, prefix) {
			cp := d.DeepCopy()
			out = append(out, agent.NewSessionFromData(&cp))
		}
	}
	return out, nil
}

func (ts *testSessionStore) AppendMessages(_ context.Context, namespace, id string, msgs []agent.Message, deltaTokens int, deltaCost float64) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	d, ok := ts.sessions[namespace+":"+id]
	if !ok {
		return fmt.Errorf("session %q not found in namespace %q", id, namespace)
	}
	d.Messages = append(d.Messages, msgs...)
	d.TotalTokens += deltaTokens
	d.TotalCost += deltaCost
	d.UpdatedAt = time.Now()
	ts.sessions[namespace+":"+id] = d
	return nil
}

func (ts *testSessionStore) PatchMeta(_ context.Context, namespace, id string, patch *agent.SessionMetaPatch) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	d, ok := ts.sessions[namespace+":"+id]
	if !ok {
		return fmt.Errorf("session %q not found in namespace %q", id, namespace)
	}
	if patch == nil {
		return nil
	}
	if patch.Name != nil {
		d.Name = *patch.Name
	}
	if patch.Model != nil {
		d.Model = *patch.Model
	}
	if patch.CompactSoftLimit != nil {
		d.CompactSoftLimit = *patch.CompactSoftLimit
	}
	if patch.ClientCWD != nil {
		d.ClientCWD = *patch.ClientCWD
	}
	if patch.EstimatedContextTokens != nil {
		d.EstimatedContextTokens = *patch.EstimatedContextTokens
	}
	if patch.EnabledTools != nil {
		d.EnabledTools = patch.EnabledTools
	}
	if patch.BlockedTools != nil {
		d.BlockedTools = patch.BlockedTools
	}
	if patch.ActiveSkills != nil {
		d.ActiveSkills = patch.ActiveSkills
	}
	d.UpdatedAt = time.Now()
	ts.sessions[namespace+":"+id] = d
	return nil
}

var _ agent.SessionStore = (*testSessionStore)(nil)

// ctxWithNS returns a context with the given namespace set.
func ctxWithNS(ns string) context.Context {
	return event.ContextWithNamespace(context.Background(), ns)
}

// newTestSessions creates a SessionManager backed by a fresh test session
// store and inserts some test sessions into the given namespace.
func newTestSessions(t *testing.T, ns string, count int) *agent.SessionManager {
	t.Helper()
	sm := agent.NewSessionManager(newTestStore(), "test-system-prompt")
	for i := 0; i < count; i++ {
		s, err := sm.CreateWithID(context.Background(), ns, "")
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		s.Append(agent.Message{Role: agent.RoleUser, Content: fmt.Sprintf("user message %d", i)})
		s.Append(agent.Message{Role: agent.RoleAssistant, Content: fmt.Sprintf("assistant reply %d", i)})
		_ = sm.Save(context.Background(), ns, s)
	}
	return sm
}

func TestSessionList(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 3)
	tool := SessionList(sm)

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{})
	if !strings.Contains(res, "3 session(s)") {
		t.Fatalf("expected '3 session(s)' in result, got: %q", res)
	}
	// Each session should appear
	if !strings.Contains(res, "user message 0") {
		t.Fatalf("expected first session summary in result, got: %q", res)
	}
}

func TestSessionList_Empty(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionList(sm)

	res := runPlainCtx(t, ctxWithNS("empty-ns"), tool.Handler, map[string]any{})
	if !strings.Contains(res, "0 session(s)") && !strings.Contains(res, "no sessions") && !strings.Contains(res, "No sessions") {
		t.Fatalf("expected empty message, got: %q", res)
	}
}

func TestSessionList_RespectsNamespace(t *testing.T) {
	// Sessions in namespace "alfa" should not appear when listing "beta".
	sm := agent.NewSessionManager(newTestStore(), "test")

	s1, _ := sm.CreateWithID(context.Background(), "alfa", "")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "alfa msg"})
	_ = sm.Save(context.Background(), "alfa", s1)

	s2, _ := sm.CreateWithID(context.Background(), "beta", "")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "beta msg"})
	_ = sm.Save(context.Background(), "beta", s2)

	// List from "alfa" context.
	tool := SessionList(sm)
	ctx := ctxWithNS("alfa")
	res := runPlainCtx(t, ctx, tool.Handler, map[string]any{})
	if !strings.Contains(res, "alfa msg") {
		t.Fatalf("expected 'alfa msg', got: %q", res)
	}
	if strings.Contains(res, "beta msg") {
		t.Fatalf("should not see beta sessions from alfa context, got: %q", res)
	}
}

func TestSessionRename(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 1)
	tool := SessionRename(sm)

	sessions, _ := sm.List(context.Background(), ns)
	sessionID := sessions[0].SessionID()

	// Try renaming with just the full session ID
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": sessionID, "name": "my-session"})
	if !strings.Contains(res, "renamed to") {
		t.Fatalf("expected rename confirmation, got: %q", res)
	}

	s, _, _ := sm.Store.Get(context.Background(), ns, sessionID)
	if s.SessionName() != "my-session" {
		t.Fatalf("expected name 'my-session', got %q", s.SessionName())
	}
}

func TestSessionRename_MissingArgs(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionRename(sm)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"session_id": "some-id"})
	if !strings.Contains(errMsg, "name") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing name, got: %q", errMsg)
	}
}

func TestSessionRename_NotFound(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionRename(sm)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"session_id": "no-such-id", "name": "newname"})
	if !strings.Contains(errMsg, "not found") && !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected session lookup error, got: %q", errMsg)
	}
}

func TestSessionInfo(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 1)
	tool := SessionInfo(sm)

	sessions, _ := sm.List(context.Background(), ns)
	sessionID := sessions[0].SessionID()

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": sessionID})
	if !strings.Contains(res, sessionID) {
		t.Fatalf("expected session ID in output, got: %q", res)
	}
	if !strings.Contains(res, "Messages") {
		t.Fatalf("expected Messages count, got: %q", res)
	}
	if !strings.Contains(res, "Created") {
		t.Fatalf("expected Created time, got: %q", res)
	}
	if !strings.Contains(res, "user message 0") {
		t.Fatalf("expected first message in output, got: %q", res)
	}
}

func TestSessionInfo_NotFound(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionInfo(sm)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"session_id": "no-such-id"})
	if !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected 'no session matching' error, got: %q", errMsg)
	}
}

func TestSessionRead(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 1)
	tool := SessionRead(sm)

	sessions, _ := sm.List(context.Background(), ns)
	sessionID := sessions[0].SessionID()

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": sessionID})
	if !strings.Contains(res, "user message 0") {
		t.Fatalf("expected 'user message 0' in output, got: %q", res)
	}
	if !strings.Contains(res, "assistant reply 0") {
		t.Fatalf("expected 'assistant reply 0' in output, got: %q", res)
	}
	if !strings.Contains(res, "user") {
		t.Fatalf("expected role labels, got: %q", res)
	}
}

func TestSessionRead_NotFound(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionRead(sm)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"session_id": "no-such-id"})
	if !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected 'no session matching' error, got: %q", errMsg)
	}
}

func TestSessionRead_LimitMessages(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "test")
	ctx := context.Background()

	s, _ := sm.CreateWithID(ctx, ns, "")
	for i := 0; i < 10; i++ {
		s.Append(agent.Message{Role: agent.RoleUser, Content: fmt.Sprintf("msg %d", i)})
	}
	_ = sm.Save(ctx, ns, s)

	tool := SessionRead(sm)
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": s.SessionID(), "max_messages": 3})
	// Should show recent messages (last 3)
	if !strings.Contains(res, "msg 7") {
		t.Fatalf("expected 'msg 7' in recent output, got: %q", res)
	}
	if !strings.Contains(res, "msg 9") {
		t.Fatalf("expected 'msg 9' in recent output, got: %q", res)
	}
	// Should indicate truncation
	if !strings.Contains(res, "TRUNCATED") && !strings.Contains(res, "omitted") {
		t.Fatalf("expected truncation indicator, got: %q", res)
	}
}

func TestSessionSearch(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "test")
	ctx := context.Background()

	s1, _ := sm.CreateWithID(ctx, ns, "")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "the quick brown fox"})
	_ = sm.Save(ctx, ns, s1)

	s2, _ := sm.CreateWithID(ctx, ns, "")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "jumps over the lazy dog"})
	_ = sm.Save(ctx, ns, s2)

	s3, _ := sm.CreateWithID(ctx, ns, "")
	s3.Append(agent.Message{Role: agent.RoleUser, Content: "the fox is quick"})
	_ = sm.Save(ctx, ns, s3)

	tool := SessionSearch(sm)
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"pattern": "fox"})
	if !strings.Contains(res, "quick brown fox") {
		t.Fatalf("expected session with 'quick brown fox', got: %q", res)
	}
	if !strings.Contains(res, "fox is quick") {
		t.Fatalf("expected session with 'fox is quick', got: %q", res)
	}
	if strings.Contains(res, "lazy dog") {
		t.Fatalf("should not match 'lazy dog', got: %q", res)
	}
	// Should show at least 2 matches
	if !strings.Contains(res, "2") && !strings.Contains(res, "2 match") {
		t.Fatalf("expected match count, got: %q", res)
	}
}

func TestSessionSearch_NoMatch(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 2)
	tool := SessionSearch(sm)

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"pattern": "zzzznooooomatch"})
	if !strings.Contains(strings.ToLower(res), "no matches") && !strings.Contains(res, "0 match") {
		t.Fatalf("expected 'no matches' message, got: %q", res)
	}
}

func TestSessionSearch_RespectsNamespace(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")

	s1, _ := sm.CreateWithID(context.Background(), "alfa", "")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "secret-alfa-data"})
	_ = sm.Save(context.Background(), "alfa", s1)

	s2, _ := sm.CreateWithID(context.Background(), "beta", "")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "secret-beta-data"})
	_ = sm.Save(context.Background(), "beta", s2)

	tool := SessionSearch(sm)
	// Search from alfa namespace
	ctx := ctxWithNS("alfa")
	res := runPlainCtx(t, ctx, tool.Handler, map[string]any{"pattern": "secret"})
	if !strings.Contains(res, "secret-alfa") {
		t.Fatalf("expected alfa result, got: %q", res)
	}
	if strings.Contains(res, "secret-beta") {
		t.Fatalf("should not see beta from alfa context, got: %q", res)
	}
}

func TestSessionDeleteTool(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 3)
	tool := SessionDelete(sm)

	sessions, _ := sm.List(context.Background(), ns)
	target := sessions[1].SessionID()

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": target})
	if !strings.Contains(res, "deleted") {
		t.Fatalf("expected delete confirmation, got: %q", res)
	}
	// Verify it's gone
	sessions, _ = sm.List(context.Background(), ns)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions remaining, got %d", len(sessions))
	}
}

func TestSessionDelete_NotFound(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	tool := SessionDelete(sm)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"session_id": "no-such-id"})
	if !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected 'no session matching' error, got: %q", errMsg)
	}
}

func TestSessionCreateTool(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 2)
	tool := SessionCreate(sm)

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{})
	if !strings.Contains(strings.ToLower(res), "created") {
		t.Fatalf("expected creation confirmation, got: %q", res)
	}

	sessions, _ := sm.List(context.Background(), ns)
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions after creation, got %d", len(sessions))
	}
}

func TestSessionSummarize_NoRouter(t *testing.T) {
	ns := "testns"
	sm := newTestSessions(t, ns, 1)
	tool := SessionSummarize(sm, nil)

	sessions, _ := sm.List(context.Background(), ns)

	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{"session_id": sessions[0].SessionID()})
	if !strings.Contains(res, "User messages") && !strings.Contains(res, "message") {
		t.Fatalf("expected a summary of messages, got: %q", res)
	}
}

func TestAllSessionToolSchemas(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "test"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, tools, "ns", agent.EngineConfig{})
	toolset := []agent.Tool{
		SessionList(sm),
		SessionRename(sm),
		SessionInfo(sm),
		SessionRead(sm),
		SessionSearch(sm),
		SessionDelete(sm),
		SessionCreate(sm),
		SessionAskQuestion(sm, conv),
	}

	names := map[string]bool{}
	for _, tool := range toolset {
		if tool.Schema.Name == "" {
			t.Fatal("tool has empty name")
		}
		if names[tool.Schema.Name] {
			t.Fatalf("duplicate tool name: %s", tool.Schema.Name)
		}
		names[tool.Schema.Name] = true
		if tool.Handler == nil {
			t.Fatalf("tool %s has nil handler", tool.Schema.Name)
		}
	}
}

// runPlainCtx is like runPlain but uses a custom context.
func runPlainCtx(t *testing.T, ctx context.Context, h func(context.Context, json.RawMessage) (string, error), args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(ctx, raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	return res
}

// Verify that session tools respect namespace isolation when no namespace
// is in context (should error).
func TestSessionList_NoNamespaceInContext(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	// Create a session directly (no context namespace)
	s, _ := sm.CreateWithID(context.Background(), "some-ns", "")
	s.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	_ = sm.Save(context.Background(), "some-ns", s)

	tool := SessionList(sm)
	// Plain context (no namespace) — should error
	ctx := context.Background()
	errMsg := runErrCtx(t, ctx, tool.Handler, map[string]any{})
	if !strings.Contains(errMsg, "namespace not available") {
		t.Fatalf("expected 'namespace not available' error, got: %q", errMsg)
	}
}

func TestSessionRead_RespectsNamespace(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")

	s1, _ := sm.CreateWithID(context.Background(), "alfa", "")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "alfa-secret"})
	_ = sm.Save(context.Background(), "alfa", s1)

	// Try reading from "beta" context — should fail because session doesn't
	// exist in the "beta" namespace.
	ctx := ctxWithNS("beta")
	tool := SessionRead(sm)

	errMsg := runErrCtx(t, ctx, tool.Handler, map[string]any{"session_id": s1.SessionID()})
	if !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected 'no session matching' when reading from wrong namespace, got: %q", errMsg)
	}
}

func runErrCtx(t *testing.T, ctx context.Context, h func(context.Context, json.RawMessage) (string, error), args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(ctx, raw)
	if err != nil {
		return err.Error()
	}
	if strings.HasPrefix(res, "Error: ") {
		return strings.TrimPrefix(res, "Error: ")
	}
	t.Fatalf("expected error result, got: %q", res)
	return ""
}

// Test tool timeout ensures session tools don't hang (they should be instant).
func TestSessionTools_TimeoutSafety(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "test")
	ctx := context.Background()

	s, _ := sm.CreateWithID(ctx, "ns", "")
	s.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	_ = sm.Save(ctx, "ns", s)

	timeoutCtx, cancel := context.WithTimeout(ctxWithNS("ns"), 2*time.Second)
	defer cancel()

	tools := []agent.Tool{
		SessionList(sm),
		SessionInfo(sm),
		SessionRead(sm),
	}
	for _, tool := range tools {
		raw, _ := json.Marshal(map[string]any{"session_id": s.SessionID()})
		_, err := tool.Handler(timeoutCtx, raw)
		if err != nil {
			t.Fatalf("tool %s failed: %v", tool.Schema.Name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// session_ask_question tests
// ---------------------------------------------------------------------------

// askAdapter is a minimal LLMAdapter that records the messages it receives
// and returns a canned response.
type askAdapter struct {
	t             *testing.T
	wantToolCalls bool // if true, message list should NOT contain tool calls
	response      string
}

func (a *askAdapter) Complete(_ context.Context, msgs []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	if a.wantToolCalls {
		// Verify that no assistant message contains tool calls
		for _, m := range msgs {
			if m.Role == agent.RoleAssistant && len(m.ToolCalls) > 0 {
				a.t.Errorf("expected tool calls to be stripped, got tool call in assistant msg: %+v", m)
			}
			if m.Role == agent.RoleTool {
				a.t.Errorf("expected tool-role messages to be stripped, got: %q", m.Content)
			}
		}
	}
	return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: a.response}}, nil
}

func TestSessionAskQuestion_Basic(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "")
	ctx := context.Background()

	// Create a session with some messages
	s, err := sm.CreateWithID(ctx, ns, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Append(agent.Message{Role: agent.RoleUser, Content: "Hello, what is the capital of France?"})
	s.Append(agent.Message{Role: agent.RoleAssistant, Content: "The capital of France is Paris."})
	s.Append(agent.Message{Role: agent.RoleUser, Content: "What about Germany?"})
	s.Append(agent.Message{Role: agent.RoleAssistant, Content: "The capital of Germany is Berlin."})
	_ = sm.Save(ctx, ns, s)

	// Set up router with mock adapter
	adapter := &askAdapter{t: t, wantToolCalls: false, response: "Based on the conversation, the user is asking about European capitals."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)

	tool := SessionAskQuestion(sm, conv)
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
		"question":   "What is this conversation about?",
	})
	if !strings.Contains(res, "European capitals") {
		t.Fatalf("expected response about European capitals, got: %q", res)
	}
}

func TestSessionAskQuestion_StripsToolCalls(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "")
	ctx := context.Background()

	// Create a session with tool calls
	s, err := sm.CreateWithID(ctx, ns, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Append(agent.Message{Role: agent.RoleUser, Content: "Read the file foo.txt"})
	// Assistant with tool calls
	s.Append(agent.Message{
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{
			{ID: "call1", Name: "read_file", Arguments: `{"path":"foo.txt"}`},
		},
	})
	// Tool result
	s.Append(agent.Message{Role: agent.RoleTool, Content: "file contents", ToolCallID: "call1", Name: "read_file"})
	// Assistant text response
	s.Append(agent.Message{Role: agent.RoleAssistant, Content: "The file foo.txt contains: file contents"})
	_ = sm.Save(ctx, ns, s)

	adapter := &askAdapter{t: t, wantToolCalls: true, response: "The user asked about file contents."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)

	tool := SessionAskQuestion(sm, conv)
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
		"question":   "What happened?",
	})
	if !strings.Contains(res, "file contents") {
		t.Fatalf("expected response about file contents, got: %q", res)
	}
}

func TestSessionAskQuestion_NotFound(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, tools, "ns", agent.EngineConfig{})

	tool := SessionAskQuestion(sm, conv)
	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"session_id": "no-such-id",
		"question":   "test",
	})
	if !strings.Contains(errMsg, "no session matching") {
		t.Fatalf("expected 'no session matching' error, got: %q", errMsg)
	}
}

func TestSessionAskQuestion_MissingQuestion(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "")
	ns := "testns"
	s, _ := sm.CreateWithID(context.Background(), ns, "")
	_ = sm.Save(context.Background(), ns, s)

	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, tools, ns, agent.EngineConfig{})

	tool := SessionAskQuestion(sm, conv)
	errMsg := runErrCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
	})
	if !strings.Contains(errMsg, "question") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing question, got: %q", errMsg)
	}
}

func TestSessionAskQuestion_MissingSessionID(t *testing.T) {
	sm := agent.NewSessionManager(newTestStore(), "")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, tools, "ns", agent.EngineConfig{})

	tool := SessionAskQuestion(sm, conv)
	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"question": "test",
	})
	if !strings.Contains(errMsg, "session_id") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing session_id, got: %q", errMsg)
	}
}

// TestSessionTools_HaveAllTag verifies that all session tools have the
// "all" tag and do NOT have the "developer" tag.
func TestSessionTools_HaveAllTag(t *testing.T) {
	// Collect all session tool constructors
	sm := agent.NewSessionManager(newTestStore(), "test")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: ""})
	router := agent.NewRouter(reg)
	toolReg := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, toolReg, "ns", agent.EngineConfig{})

	tools := []agent.Tool{
		SessionList(sm),
		SessionRename(sm),
		SessionInfo(sm),
		SessionRead(sm),
		SessionSearch(sm),
		SessionSummarize(sm, conv),
		SessionDelete(sm),
		SessionCreate(sm),
		SessionAskQuestion(sm, conv),
	}

	for _, tool := range tools {
		name := tool.Schema.Name
		hasAll := false
		hasDev := false
		for _, tag := range tool.Tags {
			if tag == "all" {
				hasAll = true
			}
			if tag == "developer" {
				hasDev = true
			}
		}
		if !hasAll {
			t.Errorf("session tool %q is missing the %q tag; tags: %v", name, "all", tool.Tags)
		}
		if hasDev {
			t.Errorf("session tool %q should not have the %q tag; tags: %v", name, "developer", tool.Tags)
		}
	}
}

func TestSessionAskQuestion_WithPrefix(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "")
	ctx := context.Background()

	// Create two sessions with different IDs
	s1, _ := sm.CreateWithID(ctx, ns, "")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "session one content"})
	_ = sm.Save(ctx, ns, s1)

	s2, _ := sm.CreateWithID(ctx, ns, "")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "session two content"})
	_ = sm.Save(ctx, ns, s2)

	adapter := &askAdapter{t: t, wantToolCalls: false, response: "about session two"}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, router, tools, ns, agent.EngineConfig{})

	tool := SessionAskQuestion(sm, conv)
	// Use prefix of s2
	prefix := agent.TruncateID(s2.SessionID(), 8)
	res := runPlainCtx(t, ctxWithNS(ns), tool.Handler, map[string]any{
		"session_id": prefix,
		"question":   "What is this about?",
	})
	if !strings.Contains(res, "session two") {
		t.Fatalf("expected response about session two, got: %q", res)
	}
}
