package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	
	"github.com/ariloulaleelay/hakka/agent"
)

// fakeAdapter is a minimal adapter for command tests.
type fakeAdapter struct{}

func (f *fakeAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "ok"}}, nil
}

func (f *fakeAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
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
	if _, err := conv.BindSessionModel(context.Background(), "sid", "beta"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	sess, _ := sm.GetOrCreate(context.Background(), "testns", "sid")
	if conv.SessionModel(sess) != "beta" {
		t.Fatalf("not persisted: %q", conv.SessionModel(sess))
	}
}

// --- Session list tests ---

func TestSessionList_ReturnsSessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
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
	sm.GetOrCreate(context.Background(), "testns", "empty-session")
	fullSession, _ := sm.GetOrCreate(context.Background(), "testns", "full-session")
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
	sm.GetOrCreate(context.Background(), "testns", "active-empty")

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

func TestSessionCreate_ReturnsNewSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	sm.GetOrCreate(context.Background(), "testns", "session1")

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
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "target-session")
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
	sm.GetOrCreate(context.Background(), "testns", "session1")

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
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "target-session")
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
	sm.GetOrCreate(context.Background(), "testns", "xyz-one")
	sm.GetOrCreate(context.Background(), "testns", "xyz-two")

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
	sm.GetOrCreate(context.Background(), "testns", "session1")
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
	sm.GetOrCreate(context.Background(), "testns", "session1")
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
	sm.GetOrCreate(context.Background(), "testns", "session1")

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
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "abc")
	s2, _ := sm.GetOrCreate(context.Background(), "testns", "abcdef")
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

// --- Session info tests ---

func TestSessionInfo_ShowsDetails(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.SetSessionName("My Session")

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
	sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.ExecuteJSON(context.Background(), "session1", "session_rename", params(map[string]any{"name": "My Chat"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Session == nil || res.Session.SessionName() != "My Chat" {
		t.Fatalf("expected session.Name = %q, got %q", "My Chat", res.Session.SessionName())
	}

	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
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
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
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
	sm.GetOrCreate(context.Background(), "testns", "session1")

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
	sm.GetOrCreate(context.Background(), "testns", "sid")

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
	conv, cmd, _ := newCommandComponents(t)
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
	sm.GetOrCreate(context.Background(), "testns", "s1")

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
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "read_file"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled")
	}
}

func TestToolDeny_DisablesTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("write_file")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_deny", params(map[string]any{"name": "write_file"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if session.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to be disabled")
	}
}

func TestToolAllow_ByTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "#filesystem"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !session.IsToolEnabled("read_file") || !session.IsToolEnabled("write_file") {
		t.Fatal("expected filesystem tools to be enabled via tag")
	}
}

func TestToolDeny_ByTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("read_file")
	session.EnableTool("shell")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_deny", params(map[string]any{"name": "#filesystem"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled via tag")
	}
	if !session.IsToolEnabled("shell") {
		t.Fatal("expected shell to remain enabled (different tag)")
	}
}

func TestToolAllow_UnknownTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "nonexistent"}))

	if res.Error == nil && !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected error about unknown tool, got: %+v", res)
	}
}

func TestToolAllow_NonexistentTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "#bogus"}))

	if res.Error == nil && !strings.Contains(res.Reply, "no tools") {
		t.Fatalf("expected 'no tools' message, got: %+v", res)
	}
}

func TestToolList_ShowsEnabledStatus(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("http_get")

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
	sm.GetOrCreate(context.Background(), "testns", "s1")
	cmd.ExecuteJSON(context.Background(), "s1", "tool_allow", params(map[string]any{"name": "read_file"}))

	session2, _ := sm.GetOrCreate(context.Background(), "testns", "s2")
	if session2.IsToolEnabled("read_file") {
		t.Fatal("expected new sessions to have no tools enabled by default")
	}

	session1, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
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

	sm.GetOrCreate(context.Background(), "testns", "tg-session")

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
	sm.GetOrCreate(context.Background(), "testns", "s1")

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
	sm.GetOrCreate(context.Background(), "testns", "s1")

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
	oldSession, _ := sm.GetOrCreate(context.Background(), "testns", "old-session")
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
	sess, _ := sm.GetOrCreate(context.Background(), "testns", "sid")
	sess.SetClientCWD("/old/path")

	res := cmd.ExecuteJSON(context.Background(), "sid", "cwd_set", params(map[string]any{"cwd": "/new/path"}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	got, _ := sm.GetOrCreate(context.Background(), "testns", "sid")
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
	sm.GetOrCreate(context.Background(), "testns", "sid")

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
	sm.GetOrCreate(context.Background(), "testns", "sid")

	res := cmd.ExecuteJSON(context.Background(), "sid", "compact", params(map[string]any{"n": 50000}))

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	session, _ := sm.GetOrCreate(context.Background(), "testns", "sid")
	if session.GetCompactSoftLimit() != 50000 {
		t.Fatalf("expected compact limit 50000, got %d", session.GetCompactSoftLimit())
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
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("abcdef"); return s }(),
	}
	prefixes := shortestUniquePrefixes(sessions)
	if prefixes["abcdef"] != "a" {
		t.Fatalf("expected prefix 'a', got %q", prefixes["abcdef"])
	}
}

func TestShortestUniquePrefix_MultipleSessions(t *testing.T) {
	sessions := []*agent.Session{
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("abc123"); return s }(),
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("abd456"); return s }(),
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("abe789"); return s }(),
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
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("a-long-id"); return s }(),
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("another-id"); return s }(),
		func() *agent.Session { s := agent.NewSession("ns", ""); s.SetID("b-short"); return s }(),
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
