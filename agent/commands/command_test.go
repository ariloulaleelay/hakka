package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// fakeAdapter is a minimal adapter for command tests.
type fakeAdapter struct{}

func (f *fakeAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "ok"}}, nil
}

// newCommandComponents builds the trio directly, bypassing Engine.
func newCommandComponents(t *testing.T) (*agent.Conversation, *CommandProcessor, *agent.SessionManager) {
	t.Helper()
	reg := agent.NewRegistry()
	reg.Register("alpha", &fakeAdapter{})
	reg.Register("beta", &fakeAdapter{})
	router := agent.NewRouter(reg)
	sm := agent.NewSessionManager(nil, "")
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := New(sm, conv, "", ns)
	return conv, cmd, sm
}

func params(data map[string]any) json.RawMessage {
	b, _ := json.Marshal(data)
	return b
}

func TestSessionModelPersistsAcrossLookups(t *testing.T) {
	conv, _, sm := newCommandComponents(t)
	sess, _ := sm.CreateWithID(context.Background(), "testns", "sid")
	if _, err := conv.BindSessionModel(context.Background(), sess.SessionID(), "beta"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	sess, _ = sm.Get(context.Background(), "testns", "sid")
	if conv.SessionModel(sess) != "beta" {
		t.Fatalf("not persisted: %q", conv.SessionModel(sess))
	}
}

// --- Session list in_flight tests ---

func TestSessionList_ReportsInFlight(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "session1")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)

	// Without active checker — in_flight should be false
	res := cmd.ExecuteJSON(context.Background(), "session1", "session_list", nil)
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, s := range data.Sessions {
		inflight, ok := s["in_flight"]
		if !ok {
			t.Fatal("expected in_flight field in session list entry")
		}
		if inflight != false {
			t.Fatalf("expected in_flight=false without checker, got %v", inflight)
		}
	}
}

func TestSessionList_InFlightChecker(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "session1")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)

	s2, _ := sm.CreateWithID(context.Background(), "testns", "session-other")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "world"})
	sm.Save(context.Background(), "testns", s2)

	// Set checker: session1 is in flight, session-other is not
	cmd.SetSessionActiveChecker(func(sessionID string) bool {
		return sessionID == "session1"
	})

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_list", nil)
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, s := range data.Sessions {
		id, _ := s["id"].(string)
		inflight, _ := s["in_flight"].(bool)
		switch id {
		case "session1":
			if !inflight {
				t.Errorf("expected session1 to be in_flight=true")
			}
		case "session-other":
			if inflight {
				t.Errorf("expected session-other to be in_flight=false")
			}
		}
	}
}

// --- Session list tests ---

func TestSessionList_ReturnsSessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "session1")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Data == nil {
		t.Fatal("expected data in result")
	}
	var data struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.Sessions) == 0 {
		t.Fatal("expected at least one session")
	}
}

func TestSessionList_HidesEmptySessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "empty-session")
	fullSession, _ := sm.CreateWithID(context.Background(), "testns", "full-session")
	fullSession.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", fullSession)

	res := cmd.ExecuteJSON(context.Background(), "full-session", "session_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Sessions []map[string]any `json:"sessions"`
	}
	json.Unmarshal(res.Data, &data)

	for _, s := range data.Sessions {
		id, _ := s["id"].(string)
		if id == "empty-session" {
			t.Fatal("expected empty session to be hidden from list")
		}
	}
}

func TestSessionList_ShowsActiveEmptySession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "active-empty")

	res := cmd.ExecuteJSON(context.Background(), "active-empty", "session_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Sessions []map[string]any `json:"sessions"`
	}
	json.Unmarshal(res.Data, &data)

	var found bool
	for _, s := range data.Sessions {
		id, _ := s["id"].(string)
		if id == "active-empty" {
			found = true
			if cur, _ := s["current"].(bool); !cur {
				t.Fatal("expected active-empty to be marked as current")
			}
		}
	}
	if !found {
		t.Fatal("expected active empty session to be visible in list")
	}
}

// --- Session create tests ---

// serializingStore wraps a MemoryStore but deep-copies on Put,
// simulating the behaviour of a serialising store like SQLite.
// This exposes bugs where session mutations after CreateWithID are
// never saved back to the store.
type serializingStore struct {
	inner *agent.MemoryStore
}

func (s *serializingStore) Get(ctx context.Context, ns, id string) (*agent.Session, bool, error) {
	return s.inner.Get(ctx, ns, id)
}
func (s *serializingStore) Put(ctx context.Context, ns string, sess *agent.Session) error {
	// Deep-copy to simulate serialisation (like SQLite does)
	data := sess.Read()
	clone := agent.NewSessionFromData(&data)
	return s.inner.Put(ctx, ns, clone)
}
func (s *serializingStore) Delete(ctx context.Context, ns, id string) error {
	return s.inner.Delete(ctx, ns, id)
}
func (s *serializingStore) List(ctx context.Context, ns string) ([]*agent.Session, error) {
	return s.inner.List(ctx, ns)
}

func (s *serializingStore) AppendMessages(ctx context.Context, ns, id string, msgs []agent.Message, deltaTokens int, deltaCost float64) error {
	return s.inner.AppendMessages(ctx, ns, id, msgs, deltaTokens, deltaCost)
}

func (s *serializingStore) PatchMeta(ctx context.Context, ns, id string, patch *agent.SessionMetaPatch) error {
	return s.inner.PatchMeta(ctx, ns, id, patch)
}

func TestSessionCreate_PersistsCWD(t *testing.T) {
	// Use a serialising store so that post-creation mutations
	// (like inheritCWD) don't magically "stick" via pointer sharing.
	store := &serializingStore{inner: agent.NewMemoryStore()}
	sm := agent.NewSessionManager(store, "")
	reg := agent.NewRegistry()
	reg.Register("alpha", &fakeAdapter{})
	reg.Register("beta", &fakeAdapter{})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := New(sm, conv, "", ns)

	oldSession, _ := sm.CreateWithID(context.Background(), "testns", "old-session")
	oldSession.SetClientCWD("/client/project")
	sm.Save(context.Background(), "testns", oldSession)

	createRes := cmd.ExecuteJSON(context.Background(), "old-session", "session_create", nil)
	if createRes.Error != nil {
		t.Fatalf("session_create error: %v", createRes.Error)
	}
	newID := createRes.Session.SessionID()

	// In-memory must be correct (inherited from old session)
	if cwd := createRes.Session.Read().ClientCWD; cwd != "/client/project" {
		t.Fatalf("expected in-memory CWD %q, got %q", "/client/project", cwd)
	}

	// The store must also have the inherited CWD, NOT the process default.
	// With the bug, CreateWithID saves the session with os.Getwd() before
	// inheritCWD runs, and the store is never updated afterwards.
	stored, ok, _ := sm.Store.Get(context.Background(), "testns", newID)
	if !ok {
		t.Fatalf("session %q not found in store", newID)
	}
	if cwd := stored.Read().ClientCWD; cwd != "/client/project" {
		t.Fatalf("expected stored CWD %q, got %q", "/client/project", cwd)
	}
}

func TestSessionCreate_ReturnsNewSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_create", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionSessionCreate {
		t.Fatalf("expected ActionSessionCreate, got %v", res.Action)
	}
	if res.Session == nil {
		t.Fatal("expected new session")
	}
	if res.Session.SessionID() == "session1" {
		t.Fatal("expected a different session ID")
	}
}

// --- get_session tests ---

func TestGetSession_FetchesSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "target-session")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)
	createRes := cmd.ExecuteJSON(context.Background(), "target-session", "session_create", nil)
	newID := createRes.Session.SessionID()

	res := cmd.ExecuteJSON(context.Background(), newID, "get_session", params(map[string]any{"id": "target-session"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionGetSession {
		t.Fatalf("expected ActionGetSession, got %v", res.Action)
	}
	if res.Session == nil || res.Session.SessionID() != "target-session" {
		t.Fatalf("expected session 'target-session', got %v", res.Session)
	}
}

func TestGetSession_NonExistentFails(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "get_session", params(map[string]any{"id": "nonexistent"}))

	if res.Error == nil && !strings.Contains(res.Reply, "no session matching") {
		t.Fatalf("expected error when fetching non-existent session, got: %+v", res)
	}
	if res.Session != nil {
		t.Fatal("expected nil session when fetch fails")
	}
}

func TestGetSession_WithShortID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "target-session")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)
	createRes := cmd.ExecuteJSON(context.Background(), "target-session", "session_create", nil)
	newID := createRes.Session.SessionID()

	prefix := "target-session"[:3]
	res := cmd.ExecuteJSON(context.Background(), newID, "get_session", params(map[string]any{"id": prefix}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session == nil || res.Session.SessionID() != "target-session" {
		t.Fatalf("expected session 'target-session', got: %v", res.Session)
	}
}

func TestGetSession_WithAmbiguousPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "xyz-one")
	sm.CreateWithID(context.Background(), "testns", "xyz-two")

	res := cmd.ExecuteJSON(context.Background(), "xyz-one", "get_session", params(map[string]any{"id": "xyz"}))

	if res.Error == nil && !strings.Contains(res.Reply, "ambiguous") {
		t.Fatalf("expected 'ambiguous' error, got: %+v", res)
	}
}

func TestGetSession_WithEmptyID(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "get_session", nil)
	if !strings.Contains(res.Reply, "specify") {
		t.Fatalf("expected error about missing ID, got: %+v", res)
	}
}

// --- Session delete tests ---

func TestSessionDelete_RemovesTarget(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")
	createRes := cmd.ExecuteJSON(context.Background(), "session1", "session_create", nil)
	newID := createRes.Session.SessionID()

	delRes := cmd.ExecuteJSON(context.Background(), "session1", "session_delete", params(map[string]any{"id": newID}))

	if delRes.Error != nil {
		t.Fatalf("unexpected error: %v", delRes.Error)
	}

	var data struct {
		Deleted string `json:"deleted"`
	}
	json.Unmarshal(delRes.Data, &data)
	if data.Deleted != newID {
		t.Fatalf("expected deleted %q, got %q", newID, data.Deleted)
	}
}

func TestSessionDelete_WithShortID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")
	createRes := cmd.ExecuteJSON(context.Background(), "session1", "session_create", nil)
	newID := createRes.Session.SessionID()
	createRes.Session.Append(agent.Message{Role: agent.RoleUser, Content: "hi"})
	sm.Save(context.Background(), "testns", createRes.Session)

	prefix := newID[:3]
	delRes := cmd.ExecuteJSON(context.Background(), "session1", "session_delete", params(map[string]any{"id": prefix}))

	if delRes.Error != nil {
		t.Fatalf("unexpected error: %v", delRes.Error)
	}
}

func TestSessionDelete_CurrentClearsSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_delete", params(map[string]any{"id": "this"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionClearSession {
		t.Fatalf("expected ActionClearSession, got %v", res.Action)
	}
}

func TestSessionDelete_ExactMatchPreferred(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.CreateWithID(context.Background(), "testns", "abc")
	s2, _ := sm.CreateWithID(context.Background(), "testns", "abcdef")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "msg"})
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "msg"})
	sm.Save(context.Background(), "testns", s1)
	sm.Save(context.Background(), "testns", s2)

	delRes := cmd.ExecuteJSON(context.Background(), "abcdef", "session_delete", params(map[string]any{"id": "abc"}))

	if delRes.Error != nil {
		t.Fatalf("unexpected error: %v", delRes.Error)
	}
	var data struct {
		Deleted string `json:"deleted"`
	}
	json.Unmarshal(delRes.Data, &data)
	if data.Deleted != "abc" {
		t.Fatalf("expected exact match 'abc' to be deleted, got %q", data.Deleted)
	}

	_, ok, _ := sm.Store.Get(context.Background(), "testns", "abcdef")
	if !ok {
		t.Fatal("expected abcdef to still exist")
	}
	_, ok, _ = sm.Store.Get(context.Background(), "testns", "abc")
	if ok {
		t.Fatal("expected abc to be deleted")
	}
}

func TestGetSession_ReturnsDefaultModelAndEstimatedTokens(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "model-test-session")
	// Simulate a turn setting estimated_context_tokens
	session.SetEstimatedContextTokens(12345)
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "model-test-session", "get_session", params(map[string]any{"id": "model-test-session"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Data == nil {
		t.Fatal("expected data in result")
	}
	var data struct {
		Session  map[string]any  `json:"session"`
		Messages []agent.Message `json:"messages"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check model is not empty — should resolve to the registry default ("alpha")
	model, _ := data.Session["model"].(string)
	if model != "alpha" {
		t.Fatalf("expected model 'alpha' (registry default), got %q", model)
	}

	// Check estimated_context_tokens is preserved
	est, _ := data.Session["estimated_context_tokens"].(float64)
	if int(est) != 12345 {
		t.Fatalf("expected estimated_context_tokens=12345, got %v", est)
	}

	// session_info is a separate read path that does NOT apply in-memory
	// defaults (get_session does, for response quality). Since the session
	// was never explicitly given a model, session_info returns the stored
	// value (empty string).
	infoRes := cmd.ExecuteJSON(context.Background(), "model-test-session", "session_info", nil)
	var infoData struct {
		Session map[string]any `json:"session"`
	}
	json.Unmarshal(infoRes.Data, &infoData)
	infoModel, _ := infoData.Session["model"].(string)
	if infoModel != "" {
		t.Fatalf("expected session_info model '' (never persisted), got %q", infoModel)
	}
}

// --- Session info tests ---

func TestSessionInfo_ShowsDetails(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "session1")
	session.SetSessionName("My Session")
	_ = sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_info", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Data == nil {
		t.Fatal("expected data in result")
	}
	var data struct {
		Session map[string]any `json:"session"`
	}
	json.Unmarshal(res.Data, &data)
	if data.Session["name"] != "My Session" {
		t.Fatalf("expected name 'My Session', got %v", data.Session["name"])
	}
	if data.Session["id"] != "session1" {
		t.Fatalf("expected id 'session1', got %v", data.Session["id"])
	}
}

// --- Session rename tests ---

func TestSessionRename_SetsName(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_rename", params(map[string]any{"name": "My Chat"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session == nil || res.Session.SessionName() != "My Chat" {
		t.Fatalf("expected session.Name = %q, got %q", "My Chat", res.Session.SessionName())
	}

	session, _ := sm.Get(context.Background(), "testns", "session1")
	if session.SessionName() != "My Chat" {
		t.Fatalf("expected persisted Name = %q, got %q", "My Chat", session.SessionName())
	}
}

func TestSessionRename_EmptyName(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "session1", "session_rename", params(map[string]any{"name": ""}))
	if !strings.Contains(res.Reply, "empty") {
		t.Fatalf("expected error about empty name, got: %+v", res)
	}
}

// --- Session auto-rename tests ---

func TestSessionAutoRename_NamesSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "session1")
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	session.Append(agent.Message{Role: agent.RoleAssistant, Content: "hi back"})
	sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_autorename", nil)

	if res.Error != nil {
		t.Fatalf("error: %v", res.Error)
	}
	if res.Session == nil || res.Session.SessionName() == "" {
		t.Fatal("expected session to have a name after autorename")
	}
}

func TestSessionAutoRename_WorksEvenWithNoMessages(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_autorename", nil)

	if res.Error != nil {
		t.Fatalf("error: %v", res.Error)
	}
	if res.Session == nil || res.Session.SessionName() == "" {
		t.Fatal("expected session to have a name after autorename")
	}
}

// --- Model tests ---

func TestModelList_ReturnsModels(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "sid")

	res := cmd.ExecuteJSON(context.Background(), "sid", "model_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(res.Data, &data)
	if len(data.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(data.Models))
	}
}

func TestModelSwitch_SetsModel(t *testing.T) {
	conv, cmd, sm := newCommandComponents(t)
	if _, err := sm.CreateWithID(context.Background(), "testns", "sid"); err != nil {
		t.Fatal(err)
	}
	res := cmd.ExecuteJSON(context.Background(), "sid", "model_switch", params(map[string]any{"name": "beta"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if conv.SessionModel(res.Session) != "beta" {
		t.Fatalf("session model not set: %q", conv.SessionModel(res.Session))
	}
}

func TestModelSwitch_UnknownFails(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "model_switch", params(map[string]any{"name": "nope"}))
	if res.Error == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestModelSwitch_EmptyName(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "model_switch", nil)
	if !strings.Contains(res.Reply, "specify") {
		t.Fatalf("expected error about missing name, got: %+v", res)
	}
}

// --- Tool command tests ---

func newCommandComponentsWithTools(t *testing.T) (*agent.Conversation, *CommandProcessor, *agent.SessionManager, *agent.ToolRegistry) {
	t.Helper()
	reg := agent.NewRegistry()
	reg.Register("alpha", &fakeAdapter{})
	reg.Register("beta", &fakeAdapter{})
	router := agent.NewRouter(reg)
	sm := agent.NewSessionManager(nil, "")
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "read_file", Description: "read a file"},
		Tags:   []string{"filesystem", "read"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "write_file", Description: "write a file"},
		Tags:   []string{"filesystem", "write"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "shell", Description: "run a command"},
		Tags:   []string{"exec", "dangerous"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "http_get", Description: "HTTP GET"},
		Tags:   []string{"network"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	ns := "testns"
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := New(sm, conv, "", ns)
	cmd.SetTools(tools)
	return conv, cmd, sm, tools
}

func TestToolList_ReturnsTools(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Tools []map[string]any `json:"tools"`
	}
	json.Unmarshal(res.Data, &data)
	if len(data.Tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(data.Tools))
	}
}

func TestToolAllow_EnablesTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "read_file"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !res.Session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled")
	}
}

func TestToolDeny_DisablesTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "s1")
	session.EnableTool("write_file")
	_ = sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_deny", params(map[string]any{"name": "write_file"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to be disabled")
	}
}

func TestToolAllow_ByTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "#filesystem"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !res.Session.IsToolEnabled("read_file") || !res.Session.IsToolEnabled("write_file") {
		t.Fatal("expected filesystem tools to be enabled via tag")
	}
}

func TestToolDeny_ByTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "s1")
	session.EnableTool("read_file")
	session.EnableTool("shell")
	_ = sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_deny", params(map[string]any{"name": "#filesystem"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled via tag")
	}
	if !res.Session.IsToolEnabled("shell") {
		t.Fatal("expected shell to remain enabled (different tag)")
	}
}

func TestToolAllow_UnknownTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "nonexistent"}))

	if res.Error == nil && !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected error about unknown tool, got: %+v", res)
	}
}

func TestToolAllow_NonexistentTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "#bogus"}))

	if res.Error == nil && !strings.Contains(res.Reply, "no tools") {
		t.Fatalf("expected 'no tools' message, got: %+v", res)
	}
}

func TestToolList_ShowsEnabledStatus(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "s1")
	session.EnableTool("http_get")
	_ = sm.Save(context.Background(), "testns", session)

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_list", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Tools []map[string]any `json:"tools"`
	}
	json.Unmarshal(res.Data, &data)
	var httpGetFound bool
	for _, tl := range data.Tools {
		if tl["name"] == "http_get" {
			httpGetFound = true
			if en, _ := tl["enabled"].(bool); !en {
				t.Fatal("expected http_get to be enabled")
			}
		}
	}
	if !httpGetFound {
		t.Fatal("expected http_get in tool list")
	}
}

func TestToolCommand_PersistsAcrossSessions(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")
	cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "read_file"}))

	session2, _ := sm.CreateWithID(context.Background(), "testns", "s2")
	if session2.IsToolEnabled("read_file") {
		t.Fatal("expected new sessions to have no tools enabled by default")
	}

	session1, _ := sm.Get(context.Background(), "testns", "s1")
	if !session1.IsToolEnabled("read_file") {
		t.Fatal("expected session1 to retain its tool settings")
	}
}

func TestToolCommand_GatewayRestriction(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	restrictedTools := agent.NewToolRegistry()
	restrictedTools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "http_get", Description: "HTTP GET"},
		Tags:   []string{"network"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	cmd.SetTools(restrictedTools)

	sm.CreateWithID(context.Background(), "testns", "tg-session")

	res := cmd.ExecuteJSON(context.Background(), "tg-session", "tool_allow", params(map[string]any{"name": "shell"}))
	if res.Error == nil && !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected error about unknown tool, got: %+v", res)
	}

	res = cmd.ExecuteJSON(context.Background(), "tg-session", "tool_allow", params(map[string]any{"name": "http_get"}))
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	res = cmd.ExecuteJSON(context.Background(), "tg-session", "tool_list", nil)
	var data struct {
		Tools []map[string]any `json:"tools"`
	}
	json.Unmarshal(res.Data, &data)
	for _, tl := range data.Tools {
		if tl["name"] == "read_file" || tl["name"] == "shell" {
			t.Fatal("expected only http_get in restricted tool list")
		}
	}
}

// --- Start command tests ---

func TestStartCommand_CreatesNewSession(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "start", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionSessionCreate {
		t.Fatalf("expected ActionSessionCreate, got %v", res.Action)
	}
	if res.Session == nil {
		t.Fatal("expected a new session")
	}
	if res.Session.SessionID() == "s1" {
		t.Fatal("expected a different session ID")
	}
}

func TestStartCommand_EnablesAllTools(t *testing.T) {
	_, cmd, sm, tools := newCommandComponentsWithTools(t)
	sm.CreateWithID(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "start", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	for _, schema := range tools.Schemas() {
		if !res.Session.IsToolEnabled(schema.Name) {
			t.Fatalf("expected tool %q to be enabled after /start", schema.Name)
		}
	}
}

func TestStartCommand_PreservesCWD(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	oldSession, _ := sm.CreateWithID(context.Background(), "testns", "old-session")
	oldSession.SetClientCWD("/client/project")
	sm.Save(context.Background(), "testns", oldSession)

	res := cmd.ExecuteJSON(context.Background(), "old-session", "start", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session.Read().ClientCWD != "/client/project" {
		t.Fatalf("expected ClientCWD = %q, got %q", "/client/project", res.Session.Read().ClientCWD)
	}
}

// --- CWD set tests ---

func TestCWDSet_SetsSessionCWD(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sess, _ := sm.CreateWithID(context.Background(), "testns", "sid")
	sess.SetClientCWD("/old/path")

	res := cmd.ExecuteJSON(context.Background(), "sid", "cwd_set", params(map[string]any{"cwd": "/new/path"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	got, _ := sm.Get(context.Background(), "testns", "sid")
	if got.Read().ClientCWD != "/new/path" {
		t.Fatalf("expected CWD /new/path, got %q", got.Read().ClientCWD)
	}
}

func TestCWDSet_MissingPath(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "cwd_set", nil)
	if !strings.Contains(res.Reply, "specify") {
		t.Fatalf("expected error about missing path, got: %+v", res)
	}
}

// --- Continue command tests ---

func TestContinue_ReturnsContinueAction(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "continue", nil)
	if !res.Handled {
		t.Fatal("expected continue to be handled")
	}
	if res.Action != ActionContinue {
		t.Fatalf("expected ActionContinue, got %v", res.Action)
	}
}

// --- Compact command tests ---

func TestCompact_ReadsCurrentValue(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "sid")

	res := cmd.ExecuteJSON(context.Background(), "sid", "compact", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	var data struct {
		Limit int `json:"compact_soft_limit"`
	}
	json.Unmarshal(res.Data, &data)
	if data.Limit < 0 {
		t.Fatalf("expected non-negative limit, got %d", data.Limit)
	}
}

func TestCompact_SetsValue(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.CreateWithID(context.Background(), "testns", "sid")

	res := cmd.ExecuteJSON(context.Background(), "sid", "compact", params(map[string]any{"n": 50000}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	session, _ := sm.Get(context.Background(), "testns", "sid")
	if session.GetCompactSoftLimit() != 50000 {
		t.Fatalf("expected compact limit 50000, got %d", session.GetCompactSoftLimit())
	}
}

// --- Help command tests ---

// --- Session metadata consistency tests ---
//
// These tests verify that get_session, session_info, and session_create
// all return the same canonical session metadata format with client_cwd,
// created_at, updated_at, short_id, etc.

func TestSessionMetadata_ConsistentAcrossGetSessionAndSessionInfo(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "meta-test")
	session.SetSessionName("Meta Test")
	session.SetClientCWD("/home/test/project")
	session.SetEstimatedContextTokens(54321)
	session.SetModel("beta") // explicit model so both paths agree
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hi"})
	sm.Save(context.Background(), "testns", session)

	// get_session returns session metadata and messages
	getRes := cmd.ExecuteJSON(context.Background(), "meta-test", "get_session", params(map[string]any{"id": "meta-test"}))
	if getRes.Error != nil {
		t.Fatalf("get_session error: %v", getRes.Error)
	}
	var getData struct {
		Session  map[string]any `json:"session"`
		Messages []any          `json:"messages"`
	}
	if err := json.Unmarshal(getRes.Data, &getData); err != nil {
		t.Fatalf("get_session unmarshal: %v", err)
	}

	// session_info returns only metadata
	infoRes := cmd.ExecuteJSON(context.Background(), "meta-test", "session_info", nil)
	if infoRes.Error != nil {
		t.Fatalf("session_info error: %v", infoRes.Error)
	}
	var infoData struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(infoRes.Data, &infoData); err != nil {
		t.Fatalf("session_info unmarshal: %v", err)
	}

	// Common fields must match between the two
	commonFields := []string{"id", "name", "short_id", "model", "message_count",
		"total_tokens", "total_cost", "estimated_context_tokens",
		"client_cwd", "created_at", "updated_at"}
	for _, field := range commonFields {
		gv, gok := getData.Session[field]
		iv, iok := infoData.Session[field]
		if !gok {
			t.Errorf("get_session missing field %q", field)
			continue
		}
		if !iok {
			t.Errorf("session_info missing field %q", field)
			continue
		}
		if gv != iv {
			t.Errorf("field %q mismatch: get_session=%v session_info=%v", field, gv, iv)
		}
	}

	// Verify specific values
	if name, _ := getData.Session["name"].(string); name != "Meta Test" {
		t.Errorf("expected name 'Meta Test', got %q", name)
	}
	if cwd, _ := getData.Session["client_cwd"].(string); cwd != "/home/test/project" {
		t.Errorf("expected client_cwd '/home/test/project', got %q", cwd)
	}
	if sid, _ := getData.Session["short_id"].(string); sid == "" {
		t.Error("expected non-empty short_id")
	}
	if ca, _ := getData.Session["created_at"].(string); ca == "" {
		t.Error("expected non-empty created_at")
	}
	if ua, _ := getData.Session["updated_at"].(string); ua == "" {
		t.Error("expected non-empty updated_at")
	}
}

func TestSessionMetadata_GetSessionIncludesMessages(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "msg-session")
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	session.Append(agent.Message{Role: agent.RoleAssistant, Content: "world"})
	sm.Save(context.Background(), "testns", session)

	getRes := cmd.ExecuteJSON(context.Background(), "msg-session", "get_session", params(map[string]any{"id": "msg-session"}))
	if getRes.Error != nil {
		t.Fatalf("get_session error: %v", getRes.Error)
	}
	var getData struct {
		Session  map[string]any `json:"session"`
		Messages []any          `json:"messages"`
	}
	if err := json.Unmarshal(getRes.Data, &getData); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(getData.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(getData.Messages))
	}
	if getData.Session["message_count"] != float64(2) {
		t.Fatalf("expected message_count=2, got %v", getData.Session["message_count"])
	}

	// session_info should NOT have messages
	infoRes := cmd.ExecuteJSON(context.Background(), "msg-session", "session_info", nil)
	var infoData struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(infoRes.Data, &infoData); err != nil {
		t.Fatalf("session_info unmarshal: %v", err)
	}
	if _, ok := infoData.Session["messages"]; ok {
		t.Error("session_info should not contain messages")
	}
}

func TestSessionMetadata_NewSessionHasClientCWD(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "cwd-session")
	session.SetClientCWD("/initial/cwd")
	sm.Save(context.Background(), "testns", session)

	createRes := cmd.ExecuteJSON(context.Background(), "cwd-session", "session_create", nil)
	if createRes.Error != nil {
		t.Fatalf("session_create error: %v", createRes.Error)
	}

	var createData struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(createRes.Data, &createData); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// New session inherits CWD from the previous session
	cwd, _ := createData.Session["client_cwd"].(string)
	if cwd == "" {
		t.Fatal("expected new session to have client_cwd (inherited from previous)")
	}
	if sid, _ := createData.Session["short_id"].(string); sid == "" {
		t.Fatal("expected non-empty short_id in session_create response")
	}
	if ca, _ := createData.Session["created_at"].(string); ca == "" {
		t.Fatal("expected non-empty created_at in session_create response")
	}
	if ua, _ := createData.Session["updated_at"].(string); ua == "" {
		t.Fatal("expected non-empty updated_at in session_create response")
	}
}

func TestSessionMetadata_SessionInfoIncludesClientCWD(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.CreateWithID(context.Background(), "testns", "info-cwd")
	session.SetClientCWD("/my/project")
	sm.Save(context.Background(), "testns", session)

	infoRes := cmd.ExecuteJSON(context.Background(), "info-cwd", "session_info", nil)
	if infoRes.Error != nil {
		t.Fatalf("session_info error: %v", infoRes.Error)
	}
	var infoData struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(infoRes.Data, &infoData); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cwd, ok := infoData.Session["client_cwd"]
	if !ok {
		t.Fatal("session_info must include client_cwd")
	}
	if cwd != "/my/project" {
		t.Fatalf("expected client_cwd '/my/project', got %v", cwd)
	}

	sid, ok := infoData.Session["short_id"]
	if !ok {
		t.Fatal("session_info must include short_id")
	}
	if sid == "" {
		t.Fatal("short_id must be non-empty")
	}
}

// --- Help command tests ---

func TestHelp_ReturnsCommands(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "help", nil)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Data == nil {
		t.Fatal("expected data in result")
	}
	var data struct {
		Commands []map[string]any `json:"commands"`
	}
	json.Unmarshal(res.Data, &data)
	if len(data.Commands) == 0 {
		t.Fatal("expected commands in help")
	}
}

// --- Unknown command test ---

func TestUnknownCommand_ReturnsError(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.ExecuteJSON(context.Background(), "sid", "unknown_cmd", nil)
	if !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected 'unknown command' reply, got: %+v", res)
	}
}

// --- Shortest unique prefix unit tests ---

func TestShortestUniquePrefix_SingleSession(t *testing.T) {
	sessions := []*agent.Session{
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "abcdef" })
			return s
		}(),
	}
	prefixes := shortestUniquePrefixes(sessions)
	if prefixes["abcdef"] != "a" {
		t.Fatalf("expected prefix 'a', got %q", prefixes["abcdef"])
	}
}

func TestShortestUniquePrefix_MultipleSessions(t *testing.T) {
	sessions := []*agent.Session{
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "abc123" })
			return s
		}(),
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "abd456" })
			return s
		}(),
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "abe789" })
			return s
		}(),
	}
	prefixes := shortestUniquePrefixes(sessions)
	if prefixes["abc123"] != "abc" {
		t.Fatalf("expected prefix 'abc' for abc123, got %q", prefixes["abc123"])
	}
	if prefixes["abd456"] != "abd" {
		t.Fatalf("expected prefix 'abd' for abd456, got %q", prefixes["abd456"])
	}
	if prefixes["abe789"] != "abe" {
		t.Fatalf("expected prefix 'abe' for abe789, got %q", prefixes["abe789"])
	}
}

func TestShortestUniquePrefix_DifferentLengths(t *testing.T) {
	sessions := []*agent.Session{
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "a-long-id" })
			return s
		}(),
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "another-id" })
			return s
		}(),
		func() *agent.Session {
			s := agent.NewSession("ns", "")
			s.Update(func(d *agent.SessionData) { d.ID = "b-short" })
			return s
		}(),
	}
	prefixes := shortestUniquePrefixes(sessions)
	if prefixes["a-long-id"] != "a-" {
		t.Fatalf("expected prefix 'a-' for a-long-id, got %q", prefixes["a-long-id"])
	}
	if prefixes["another-id"] != "an" {
		t.Fatalf("expected prefix 'an' for another-id, got %q", prefixes["another-id"])
	}
	if prefixes["b-short"] != "b" {
		t.Fatalf("expected prefix 'b' for b-short, got %q", prefixes["b-short"])
	}
}

// --- Session fork tests ---

func TestSessionFork_BlankChild(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)

	// Create a parent with some messages.
	parent, _ := sm.CreateWithID(context.Background(), "testns", "")
	parent.Append(agent.Message{ID: "m1", Role: agent.RoleUser, Content: "hello"})
	parent.Append(agent.Message{ID: "m2", Role: agent.RoleAssistant, Content: "hi"})

	// Fork without fork_point — blank child.
	res := cmd.ExecuteJSON(context.Background(), parent.SessionID(), "session_fork",
		params(map[string]any{"id": parent.SessionID()}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionSessionFork {
		t.Fatalf("expected ActionSessionFork, got %v", res.Action)
	}
	if res.Session == nil {
		t.Fatal("expected a child session")
	}

	childData := res.Session.Read()
	if childData.ParentID != parent.SessionID() {
		t.Fatalf("expected parent_id %q, got %q", parent.SessionID(), childData.ParentID)
	}
	if childData.ForkPoint != "" {
		t.Fatalf("expected empty fork_point, got %q", childData.ForkPoint)
	}
	if len(childData.Messages) != 0 {
		t.Fatalf("expected 0 messages in blank child, got %d", len(childData.Messages))
	}
}

func TestSessionFork_CopiesMessages(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)

	parent, _ := sm.CreateWithID(context.Background(), "testns", "")
	parent.Append(agent.Message{ID: "m1", Role: agent.RoleUser, Content: "first"})
	parent.Append(agent.Message{ID: "m2", Role: agent.RoleAssistant, Content: "second"})
	parent.Append(agent.Message{ID: "m3", Role: agent.RoleUser, Content: "third"})
	_ = sm.Save(context.Background(), "testns", parent)
	// Fork at m2.
	res := cmd.ExecuteJSON(context.Background(), parent.SessionID(), "session_fork",
		params(map[string]any{"id": parent.SessionID(), "fork_point": "m2"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	childData := res.Session.Read()
	if len(childData.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(childData.Messages))
	}
	if childData.Messages[0].ID != "m1" || childData.Messages[1].ID != "m2" {
		t.Fatalf("unexpected messages: %+v", childData.Messages)
	}
	if childData.ForkPoint != "m2" {
		t.Fatalf("expected fork_point 'm2', got %q", childData.ForkPoint)
	}
}

func TestSessionFork_BadForkPoint(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)

	parent, _ := sm.CreateWithID(context.Background(), "testns", "")
	parent.Append(agent.Message{ID: "m1", Role: agent.RoleUser, Content: "hello"})
	_ = sm.Save(context.Background(), "testns", parent)

	res := cmd.ExecuteJSON(context.Background(), parent.SessionID(), "session_fork",
		params(map[string]any{"id": parent.SessionID(), "fork_point": "nonexistent"}))

	if res.Error == nil && res.Reply == "" {
		t.Fatal("expected error for bad fork_point")
	}
}

func TestSessionFork_MissingParentID(t *testing.T) {
	_, cmd, _, _ := newCommandComponentsWithTools(t)

	res := cmd.ExecuteJSON(context.Background(), "any-session", "session_fork",
		params(map[string]any{}))

	if res.Error == nil && res.Reply == "" {
		t.Fatal("expected error for missing parent ID")
	}
}

func TestSessionFork_MetadataHasLineage(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)

	parent, _ := sm.CreateWithID(context.Background(), "testns", "")
	parent.Append(agent.Message{ID: "m1", Role: agent.RoleUser, Content: "hello"})
	_ = sm.Save(context.Background(), "testns", parent)

	res := cmd.ExecuteJSON(context.Background(), parent.SessionID(), "session_fork",
		params(map[string]any{"id": parent.SessionID(), "fork_point": "m1"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	meta := res.Session.Metadata()
	if meta["parent_id"] != parent.SessionID() {
		t.Fatalf("expected parent_id in metadata, got %v", meta["parent_id"])
	}
	if meta["fork_point"] != "m1" {
		t.Fatalf("expected fork_point in metadata, got %v", meta["fork_point"])
	}
}

func TestSessionFork_LastAssistantMessage(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)

	parent, _ := sm.CreateWithID(context.Background(), "testns", "")
	parent.Append(agent.Message{ID: "91c", Role: agent.RoleUser, Content: "what is 2+2?"})
	parent.Append(agent.Message{ID: "91d", Role: agent.RoleAssistant, Content: "4"})
	_ = sm.Save(context.Background(), "testns", parent)

	// Fork at the last assistant message.
	ctx := event.ContextWithNamespace(context.Background(), "testns")
	res := cmd.ExecuteJSON(ctx, parent.SessionID(), "session_fork",
		params(map[string]any{"id": parent.SessionID(), "fork_point": "91d"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Reply != "" && strings.HasPrefix(res.Reply, "fork failed") {
		t.Fatalf("fork failed: %s", res.Reply)
	}

	childData := res.Session.Read()
	if len(childData.Messages) != 2 {
		t.Fatalf("expected 2 messages in child, got %d; messages: %+v", len(childData.Messages), childData.Messages)
	}
	// The last message should be the fork point.
	lastMsg := childData.Messages[len(childData.Messages)-1]
	if lastMsg.ID != "91d" {
		t.Fatalf("expected last message ID '91d', got %q", lastMsg.ID)
	}
	if lastMsg.Content != "4" {
		t.Fatalf("expected content '4', got %q", lastMsg.Content)
	}

	// Now fetch the child via get_session and check the events include message IDs.
	getRes := cmd.ExecuteJSON(ctx, "", "get_session",
		params(map[string]any{"id": childData.ID}))
	if getRes.Error != nil {
		t.Fatalf("get_session error: %v", getRes.Error)
	}
	if getRes.Session == nil {
		t.Fatal("get_session returned nil session")
	}

	// The session_create doesn't include events replay. Check that the
	// child session has the right message_count.
	childMeta := getRes.Session.Metadata()
	if childMeta["message_count"].(int) != 2 {
		t.Fatalf("expected message_count 2, got %v", childMeta["message_count"])
	}

	// Also check session_create response: it should also include events.
	// Currently it does NOT — this is the bug.
	if res.Action != ActionSessionFork {
		t.Fatalf("expected ActionSessionFork, got %v", res.Action)
	}
	// ActionSessionCreate currently does NOT include events in the wire
	// response (see writeCommandResult). The client must call get_session
	// separately. This test verifies the data is correct; the protocol
	// gap (no events on session_create) is a separate issue.
}

// TestNonModifyingCommandsDontBumpUpdatedAt verifies that read-only
// commands (get_session, session_info, session_list) do not mutate
// a session's updated_at timestamp. This is a contract test — all
// commands that claim to be non-modifying must leave the session
// exactly as they found it.
func TestNonModifyingCommandsDontBumpUpdatedAt(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)

	// Create a fully-configured session with model and compact limit.
	session, _ := sm.CreateWithID(context.Background(), "testns", "readonly-test")
	session.SetModel("alpha")
	session.SetCompactSoftLimit(100000)
	session.SetClientCWD("/home/test")
	if err := sm.Save(context.Background(), "testns", session); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Wait a tick so any accidental bump would be visible.
	time.Sleep(time.Millisecond)

	initial := session.Read().UpdatedAt

	// ---- get_session ----
	res := cmd.ExecuteJSON(context.Background(), "readonly-test", "get_session",
		params(map[string]any{"id": "readonly-test"}))
	if res.Error != nil {
		t.Fatalf("get_session error: %v", res.Error)
	}
	s, err := sm.Get(context.Background(), "testns", "readonly-test")
	if err != nil {
		t.Fatalf("re-fetch after get_session: err=%v", err)
	}
	if !s.Read().UpdatedAt.Equal(initial) {
		t.Fatal("get_session should not change updated_at")
	}

	// ---- session_info ----
	_ = cmd.ExecuteJSON(context.Background(), "readonly-test", "session_info", nil)
	s2, err := sm.Get(context.Background(), "testns", "readonly-test")
	if err != nil {
		t.Fatalf("re-fetch after session_info: err=%v", err)
	}
	if !s2.Read().UpdatedAt.Equal(initial) {
		t.Fatal("session_info should not change updated_at")
	}

	// ---- session_list ----
	_ = cmd.ExecuteJSON(context.Background(), "readonly-test", "session_list", nil)
	s3, err := sm.Get(context.Background(), "testns", "readonly-test")
	if err != nil {
		t.Fatalf("re-fetch after session_list: err=%v", err)
	}
	if !s3.Read().UpdatedAt.Equal(initial) {
		t.Fatal("session_list should not change updated_at")
	}
}
