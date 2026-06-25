package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestHandleCommandNotACommand(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "", "hello")
	if res.Handled {
		t.Fatal("expected non-command input to be passed through")
	}
}

func TestHandleCommandUnknownCommand(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/typo")
	if !res.Handled {
		t.Fatal("expected unknown command starting with / to be handled (not passed to LLM)")
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "unknown command") {
		t.Fatalf("expected error about unknown command, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "/help") {
		t.Fatalf("expected reply to mention /help, got: %q", res.Reply)
	}
}

func TestHandleCommandUnknownCommandMultipleWords(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/helo world")
	if !res.Handled {
		t.Fatal("expected unknown command starting with / to be handled")
	}
	if !strings.Contains(res.Reply, "/helo") {
		t.Fatalf("expected reply to mention the unknown command '/helo', got: %q", res.Reply)
	}
}

func TestHandleCommandJustSlash(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/")
	if !res.Handled {
		t.Fatal("expected lone slash to be handled")
	}
	if !strings.Contains(res.Reply, "unknown command") {
		t.Fatalf("expected unknown command reply, got: %q", res.Reply)
	}
}

func TestHandleCommandWithBotUsernameSuffix(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	// Commands with @bot_username suffix should be handled like their bare equivalents.
	res := cmd.Execute(context.Background(), "sid", "/help@my_bot")
	if !res.Handled {
		t.Fatal("expected /help@my_bot to be handled as /help")
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "available commands:") {
		t.Fatalf("expected help menu for /help@my_bot, got: %q", res.Reply)
	}
}

func TestHandleCommandModelWithBotUsernameSuffix(t *testing.T) {
	conv, cmd, _ := newCommandComponents(t)
	// /model@bot_username beta should work like /model beta
	res := cmd.Execute(context.Background(), "sid", "/model@some_bot beta")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "beta") {
		t.Fatalf("reply: %q", res.Reply)
	}
	if conv.SessionModel(res.Session) != "beta" {
		t.Fatalf("session model not set: %q", conv.SessionModel(res.Session))
	}
}

func TestHandleCommandUnknownWithBotUsernameSuffix(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	// Unknown command with @bot_username should still be handled (not passed to LLM)
	res := cmd.Execute(context.Background(), "sid", "/typo@my_bot")
	if !res.Handled {
		t.Fatal("expected unknown command with @suffix to be handled (not passed to LLM)")
	}
	if !strings.Contains(res.Reply, "unknown command") {
		t.Fatalf("expected error about unknown command, got: %q", res.Reply)
	}
}

func TestHandleCommandModelSet(t *testing.T) {
	conv, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/model beta")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "beta") {
		t.Fatalf("reply: %q", res.Reply)
	}
	if conv.SessionModel(res.Session) != "beta" {
		t.Fatalf("session model not set: %q", conv.SessionModel(res.Session))
	}
}

func TestHandleCommandModelUnknown(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/model nope")
	if res.Error == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestHandleCommandModelShow(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/model")
	if res.Error != nil {
		t.Fatalf("handle: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "alpha") {
		t.Fatalf("reply: %q", res.Reply)
	}
}

func TestHandleCommandModelsList(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/models")
	if res.Error != nil {
		t.Fatalf("handle: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "alpha") || !strings.Contains(res.Reply, "beta") {
		t.Fatalf("reply: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "* alpha") {
		t.Fatalf("default not marked: %q", res.Reply)
	}
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

func TestHandleCommandHelp(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/help")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "available commands:") {
		t.Fatalf("expected help menu, got: %q", res.Reply)
	}
}

func TestHandleCommandModelSubcommands(t *testing.T) {
	conv, cmd, _ := newCommandComponents(t)

	res := cmd.Execute(context.Background(), "sid", "/model show")
	if !strings.Contains(res.Reply, "current model: alpha") {
		t.Fatalf("expected alpha, got: %q", res.Reply)
	}

	res = cmd.Execute(context.Background(), "sid", "/model switch beta")
	if !strings.Contains(res.Reply, "model set to: beta") {
		t.Fatalf("expected switch confirmation, got: %q", res.Reply)
	}
	if conv.SessionModel(res.Session) != "beta" {
		t.Fatalf("expected beta model, got: %q", conv.SessionModel(res.Session))
	}

	res = cmd.Execute(context.Background(), "sid", "/model list")
	if !strings.Contains(res.Reply, "* beta") {
		t.Fatalf("expected active beta, got: %q", res.Reply)
	}
}

func TestSessionList_MarksCurrentWithAsterisk(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "*") {
		t.Fatalf("expected current session1 to be marked with '*', got reply: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "session1") {
		t.Fatalf("expected session1 to appear in list, got: %q", res.Reply)
	}
}

func TestSessionCreate_ReturnsNewSessionWithID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session create")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "created and switched to session:") {
		t.Fatalf("expected confirmation of session creation, got reply: %q", res.Reply)
	}
	if res.Session == nil {
		t.Fatal("expected new session in result, got nil")
	}
	if res.Session.ID == "" {
		t.Fatal("expected non-empty session ID in new session")
	}
}

func TestSessionSwitch_ChangesActiveSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")
	createRes := cmd.Execute(context.Background(), "session1", "/session create")
	newID := createRes.Session.ID

	res := cmd.Execute(context.Background(), newID, "/session switch session1")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "switched to session: session1") {
		t.Fatalf("expected switch confirmation to 'session1', got reply: %q", res.Reply)
	}
	if res.Session == nil || res.Session.ID != "session1" {
		t.Fatalf("expected switched session to be session1, got session ID: %q", res.Session.ID)
	}
}

func TestSessionDelete_RemovesTargetFromList(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")
	createRes := cmd.Execute(context.Background(), "session1", "/session create")
	newID := createRes.Session.ID

	delRes := cmd.Execute(context.Background(), "session1", "/session delete "+newID)

	if delRes.Error != nil {
		t.Fatalf("unexpected error: %v", delRes.Error)
	}
	if !strings.Contains(delRes.Reply, "deleted") {
		t.Fatalf("expected delete confirmation, got reply: %q", delRes.Reply)
	}

	listRes := cmd.Execute(context.Background(), "session1", "/session list")
	if strings.Contains(listRes.Reply, newID) {
		t.Fatalf("deleted session %q should be absent from list, got: %q", newID, listRes.Reply)
	}
}

func TestSessionDeleteCurrent_ClearsSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session delete session1")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionClearSession {
		t.Fatalf("expected ActionClearSession when deleting active session, got: %v", res.Action)
	}
	if !strings.Contains(res.Reply, "deleted") {
		t.Fatalf("expected delete confirmation, got reply: %q", res.Reply)
	}
}

func TestSessionRename_SetsName(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", `/session rename "My Chat"`)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "renamed") {
		t.Fatalf("expected rename confirmation, got: %q", res.Reply)
	}
	if res.Session == nil || res.Session.Name != "My Chat" {
		t.Fatalf("expected session.Name = %q, got %q", "My Chat", res.Session.Name)
	}

	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	if session.Name != "My Chat" {
		t.Fatalf("expected persisted Name = %q, got %q", "My Chat", session.Name)
	}
}

func TestSessionRename_MissingName(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)

	res := cmd.Execute(context.Background(), "session1", "/session rename")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "specify") {
		t.Fatalf("expected error about missing name, got: %q", res.Reply)
	}
}

func TestSessionInfo_ShowsName(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.Name = "My Session"

	res := cmd.Execute(context.Background(), "session1", "/session info")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, session.ID) {
		t.Fatalf("expected reply to contain session ID, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "My Session") {
		t.Fatalf("expected reply to contain session name 'My Session', got: %q", res.Reply)
	}
}

func TestSessionList_ShowsNameAndID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.Name = "Alpha Chat"

	res := cmd.Execute(context.Background(), "session1", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "Alpha Chat") {
		t.Fatalf("expected list to contain session name 'Alpha Chat', got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "<session1>") {
		t.Fatalf("expected list to contain session ID '<session1>', got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "[s]") {
		t.Fatalf("expected list to contain short prefix '[s]', got: %q", res.Reply)
	}
}

func TestSessionList_ShowsIDWhenUnnamed(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "unnamed-session")

	res := cmd.Execute(context.Background(), "unnamed-session", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "unnamed-session") {
		t.Fatalf("expected list to contain session ID 'unnamed-session', got: %q", res.Reply)
	}
}

func TestSessionList_ShowsCreationDate(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.Name = "Test Session"

	res := cmd.Execute(context.Background(), "session1", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	// Check that the reply contains the creation date in YYYY-MM-DD format
	expectedDate := session.CreatedAt.Format("2006-01-02")
	if !strings.Contains(res.Reply, expectedDate) {
		t.Fatalf("expected list to contain creation date %q, got: %q", expectedDate, res.Reply)
	}
	// Check that the reply contains the creation time in HH:MM format
	expectedTime := session.CreatedAt.Format("15:04")
	if !strings.Contains(res.Reply, expectedTime) {
		t.Fatalf("expected list to contain creation time %q, got: %q", expectedTime, res.Reply)
	}
}

func TestSessionList_OrderedByCreationTimeOldestFirst(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)

	now := time.Now().Truncate(time.Second)

	// Create sessions with IDs that would NOT sort alphabetically by time,
	// so we can prove ordering is by CreatedAt, not by ID.
	// Alphabetical order: "session-b", "session-c", "session-a"
	// But we want CreatedAt order: oldest first (session-c, session-a, session-b)

	// oldest: created 2 hours ago
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "session-c")
	s1.CreatedAt = now.Add(-2 * time.Hour)
	s1.Name = "Oldest Session"
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "old"})

	// middle: created 1 hour ago
	s2, _ := sm.GetOrCreate(context.Background(), "testns", "session-a")
	s2.CreatedAt = now.Add(-1 * time.Hour)
	s2.Name = "Middle Session"
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "middle"})

	// newest: created now
	s3, _ := sm.GetOrCreate(context.Background(), "testns", "session-b")
	s3.CreatedAt = now
	s3.Name = "Newest Session"
	s3.Append(agent.Message{Role: agent.RoleUser, Content: "new"})

	// Save sessions with modified CreatedAt
	sm.Store.Put(context.Background(), "testns", s1)
	sm.Store.Put(context.Background(), "testns", s2)
	sm.Store.Put(context.Background(), "testns", s3)

	res := cmd.Execute(context.Background(), "session-b", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	lines := strings.Split(strings.TrimSpace(res.Reply), "\n")
	// First line is "available sessions:"
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d: %q", len(lines), res.Reply)
	}

	// Check order by CreatedAt: Oldest first (session-c), Middle (session-a), Newest (session-b)
	if !strings.Contains(lines[1], "Oldest Session") {
		t.Fatalf("expected first session to be 'Oldest Session' (session-c, oldest), got: %q", lines[1])
	}
	if !strings.Contains(lines[2], "Middle Session") {
		t.Fatalf("expected second session to be 'Middle Session' (session-a, middle), got: %q", lines[2])
	}
	if !strings.Contains(lines[3], "Newest Session") {
		t.Fatalf("expected third session to be 'Newest Session' (session-b, newest), got: %q", lines[3])
	}
	if !strings.Contains(lines[3], "*") {
		t.Fatalf("expected current session to be marked with '*', got: %q", lines[3])
	}
}

// ---------------------------------------------------------------------------
// Session list filtering — empty sessions should be hidden (except active)
// ---------------------------------------------------------------------------

func TestSessionList_HidesEmptySessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)

	// Create an empty session (like what :HakkaChat does)
	emptySession, _ := sm.GetOrCreate(context.Background(), "testns", "empty-session")
	// Ensure it's truly empty — no messages
	if len(emptySession.Messages) != 0 {
		t.Fatal("expected empty session to have no messages")
	}

	// Create a session with messages (real conversation)
	fullSession, _ := sm.GetOrCreate(context.Background(), "testns", "full-session")
	fullSession.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	fullSession.Append(agent.Message{Role: agent.RoleAssistant, Content: "hi back"})
	sm.Save(context.Background(), "testns", fullSession)

	// List from the perspective of the full session
	res := cmd.Execute(context.Background(), "full-session", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	// The empty session should NOT appear
	if strings.Contains(res.Reply, "empty-session") {
		t.Fatalf("expected empty session to be HIDDEN from list, got: %q", res.Reply)
	}
	// The full session SHOULD appear
	if !strings.Contains(res.Reply, "full-session") {
		t.Fatalf("expected full session to be VISIBLE in list, got: %q", res.Reply)
	}
}

func TestSessionList_ShowsActiveEmptySession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)

	// Create an empty session — it's the active one
	activeSession, _ := sm.GetOrCreate(context.Background(), "testns", "active-empty")
	if len(activeSession.Messages) != 0 {
		t.Fatal("expected session to have no messages")
	}

	res := cmd.Execute(context.Background(), "active-empty", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	// The active (empty) session SHOULD appear with the asterisk mark
	if !strings.Contains(res.Reply, "active-empty") {
		t.Fatalf("expected active empty session to be VISIBLE in list, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "*") {
		t.Fatalf("expected active session to be marked with '*', got: %q", res.Reply)
	}
}

func TestSessionList_HidesMultipleEmptySessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)

	// Create several empty sessions (simulating multiple :HakkaChat opens)
	for _, id := range []string{"empty-1", "empty-2", "empty-3"} {
		s, _ := sm.GetOrCreate(context.Background(), "testns", id)
		if len(s.Messages) != 0 {
			t.Fatalf("expected session %q to be empty", id)
		}
	}

	// Create one non-empty session
	realSession, _ := sm.GetOrCreate(context.Background(), "testns", "real-session")
	realSession.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", realSession)

	res := cmd.Execute(context.Background(), "real-session", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	// Empty sessions should NOT appear
	for _, id := range []string{"empty-1", "empty-2", "empty-3"} {
		if strings.Contains(res.Reply, id) {
			t.Fatalf("expected empty session %q to be HIDDEN from list, got: %q", id, res.Reply)
		}
	}
	// The real session SHOULD appear
	if !strings.Contains(res.Reply, "real-session") {
		t.Fatalf("expected non-empty session to be VISIBLE in list, got: %q", res.Reply)
	}
}

// ---------------------------------------------------------------------------
// Short ID tests — shortest unique prefix display and resolution
// ---------------------------------------------------------------------------

func TestShortestUniquePrefix_SingleSession(t *testing.T) {
	sessions := []*agent.Session{
		{ID: "abcdef"},
	}
	prefixes := shortestUniquePrefixes(sessions)
	if prefixes["abcdef"] != "a" {
		t.Fatalf("expected prefix 'a', got %q", prefixes["abcdef"])
	}
}

func TestShortestUniquePrefix_MultipleSessions(t *testing.T) {
	sessions := []*agent.Session{
		{ID: "abc123"},
		{ID: "abd456"},
		{ID: "abe789"},
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
		{ID: "a-long-id"},
		{ID: "another-id"},
		{ID: "b-short"},
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

func TestSessionList_ShowsShortPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "[s]") {
		t.Fatalf("expected short prefix '[s]' in list, got: %q", res.Reply)
	}
}

func TestSessionList_ShortPrefixesUniqueAmongAll(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	// Short prefixes should be unique even considering hidden empty sessions.
	_, _ = sm.GetOrCreate(context.Background(), "testns", "aaa-empty")
	s2, _ := sm.GetOrCreate(context.Background(), "testns", "aab-active")
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s2)

	res := cmd.Execute(context.Background(), "aab-active", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	// s2 should have a unique prefix that accounts for s1 (hidden empty)
	// "aab-" should uniquely match s2 (since "aaa-" matches s1)
	if !strings.Contains(res.Reply, "[aab") {
		t.Fatalf("expected short prefix for aab-active to start with 'aab', got: %q", res.Reply)
	}
}

func TestSessionDelete_WithShortID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")
	createRes := cmd.Execute(context.Background(), "session1", "/session create")
	newID := createRes.Session.ID
	// Give the new session a message so it's visible
	createRes.Session.Append(agent.Message{Role: agent.RoleUser, Content: "hi"})
	sm.Save(context.Background(), "testns", createRes.Session)

	// Delete using short prefix of the new session's UUID
	prefix := newID[:3]
	delRes := cmd.Execute(context.Background(), "session1", "/session delete "+prefix)

	if delRes.Error != nil {
		t.Fatalf("unexpected error: %v", delRes.Error)
	}
	if !strings.Contains(delRes.Reply, "deleted") {
		t.Fatalf("expected delete confirmation with short id, got: %q", delRes.Reply)
	}
}

func TestSessionDelete_WithAmbiguousPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "abc-one")
	_, _ = sm.GetOrCreate(context.Background(), "testns", "abc-two")

	res := cmd.Execute(context.Background(), "abc-one", "/session delete abc")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "ambiguous") {
		t.Fatalf("expected 'ambiguous' error, got: %q", res.Reply)
	}
}

func TestSessionDelete_WithNonexistentPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session delete nonexistent")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "no session matching") {
		t.Fatalf("expected 'no session matching' error, got: %q", res.Reply)
	}
}

func TestSessionSwitch_WithShortID(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	// Create two sessions
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "switch-target")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	sm.Save(context.Background(), "testns", s1)
	createRes := cmd.Execute(context.Background(), "switch-target", "/session create")
	newID := createRes.Session.ID
	createRes.Session.Append(agent.Message{Role: agent.RoleUser, Content: "other"})
	sm.Save(context.Background(), "testns", createRes.Session)

	// Switch back using short prefix
	prefix := "switch-target"[:3] // "swi"
	res := cmd.Execute(context.Background(), newID, "/session switch "+prefix)

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "switched to session: switch-target") {
		t.Fatalf("expected switch to 'switch-target', got: %q", res.Reply)
	}
	if res.Session == nil || res.Session.ID != "switch-target" {
		t.Fatalf("expected session 'switch-target', got: %v", res.Session)
	}
}

func TestSessionSwitch_WithAmbiguousPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "xyz-one")
	_, _ = sm.GetOrCreate(context.Background(), "testns", "xyz-two")

	res := cmd.Execute(context.Background(), "xyz-one", "/session switch xyz")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "ambiguous") {
		t.Fatalf("expected 'ambiguous' error, got: %q", res.Reply)
	}
}

func TestSessionList_ShortPrefixesWithEmptyHiddenSessions(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	// Create hidden empty sessions and one visible session
	// The visible session's short prefix should account for all sessions
	_, _ = sm.GetOrCreate(context.Background(), "testns", "hidden-empty-a")
	_, _ = sm.GetOrCreate(context.Background(), "testns", "hidden-empty-b")
	visible, _ := sm.GetOrCreate(context.Background(), "testns", "visible-chat")
	visible.Append(agent.Message{Role: agent.RoleUser, Content: "test"})
	sm.Save(context.Background(), "testns", visible)

	res := cmd.Execute(context.Background(), "visible-chat", "/session list")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	// The visible session should have a unique prefix considering hidden ones
	// "v" should be unique (no other session starts with 'v')
	if !strings.Contains(res.Reply, "[v]") {
		t.Fatalf("expected visible session short prefix '[v]' in list, got: %q", res.Reply)
	}
}

func TestSessionDelete_ExactMatchPreferredOverPrefix(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	// Create sessions where one ID is a prefix of another
	s1, _ := sm.GetOrCreate(context.Background(), "testns", "abc")
	s2, _ := sm.GetOrCreate(context.Background(), "testns", "abcdef")
	s1.Append(agent.Message{Role: agent.RoleUser, Content: "msg"})
	s2.Append(agent.Message{Role: agent.RoleUser, Content: "msg"})
	sm.Save(context.Background(), "testns", s1)
	sm.Save(context.Background(), "testns", s2)

	// Delete "abc" — exact match should take precedence over prefix match
	res := cmd.Execute(context.Background(), "abcdef", "/session delete abc")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "abc deleted") {
		t.Fatalf("expected deletion of exact match 'abc', got: %q", res.Reply)
	}

	// Verify abc is gone but abcdef still exists
	_, ok, _ := sm.Store.Get(context.Background(), "testns", "abcdef")
	if !ok {
		t.Fatal("expected abcdef to still exist")
	}
	_, ok, _ = sm.Store.Get(context.Background(), "testns", "abc")
	if ok {
		t.Fatal("expected abc to be deleted")
	}
}

func TestSessionAutoRename_NamesSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	session.Append(agent.Message{Role: agent.RoleAssistant, Content: "hi back"})
	session.Append(agent.Message{Role: agent.RoleUser, Content: "how are you?"})
	sm.Save(context.Background(), "testns", session)

	res := cmd.Execute(context.Background(), "session1", "/session autorename")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "renamed") && !strings.Contains(res.Reply, "failed") {
		t.Fatalf("expected rename reply, got: %q", res.Reply)
	}
}

func TestSessionAutoRename_ReplacesExistingName(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "session1")
	session.Name = "Old Name"
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	session.Append(agent.Message{Role: agent.RoleAssistant, Content: "hi back"})
	session.Append(agent.Message{Role: agent.RoleUser, Content: "how are you?"})
	sm.Save(context.Background(), "testns", session)

	res := cmd.Execute(context.Background(), "session1", "/session autorename")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "renamed") && !strings.Contains(res.Reply, "failed") {
		t.Fatalf("expected rename reply, got: %q", res.Reply)
	}
}

func TestSessionAutoRename_WorksEvenWithNoMessages(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session autorename")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "session renamed") {
		t.Fatalf("expected rename reply, got: %q", res.Reply)
	}
	if res.Session == nil || res.Session.Name == "" {
		t.Fatal("expected session to have a name after autorename")
	}
}

func TestSessionAutoRename_HelpIncludesAutorename(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/session")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "autorename") {
		t.Fatalf("expected /session usage to mention autorename, got: %q", res.Reply)
	}
}

func TestHelp_IncludesRename(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/help")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "rename") {
		t.Fatalf("expected /help to mention rename, got: %q", res.Reply)
	}
}

// ---------------------------------------------------------------------------
// /tool command tests — per-session tool control
// ---------------------------------------------------------------------------

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

func TestToolCommand_List(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool list")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	t.Logf("tool list:\n%s", res.Reply)

	if !strings.Contains(res.Reply, "read_file") {
		t.Fatalf("expected read_file in list, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "disabled") {
		t.Fatalf("expected all tools to show as disabled by default, got: %q", res.Reply)
	}
}

func TestToolCommand_Enable(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool enable read_file")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "read_file") {
		t.Fatalf("expected confirmation, got: %q", res.Reply)
	}
	if !session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled on the session")
	}
}

func TestToolCommand_Disable(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("write_file")

	res := cmd.Execute(context.Background(), "s1", "/tool disable write_file")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "write_file") {
		t.Fatalf("expected confirmation, got: %q", res.Reply)
	}
	if session.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to be disabled on the session")
	}
}

func TestToolCommand_EnableTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool enable #filesystem")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "read_file") || !strings.Contains(res.Reply, "write_file") {
		t.Fatalf("expected both filesystem tools enabled, got: %q", res.Reply)
	}
	if !session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled via tag")
	}
	if !session.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to be enabled via tag")
	}
}

func TestToolCommand_DisableTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("read_file")
	session.EnableTool("write_file")
	session.EnableTool("shell")

	res := cmd.Execute(context.Background(), "s1", "/tool disable #filesystem")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "read_file") || !strings.Contains(res.Reply, "write_file") {
		t.Fatalf("expected both filesystem tools disabled, got: %q", res.Reply)
	}
	if session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled via tag")
	}
	if session.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to be disabled via tag")
	}
	if !session.IsToolEnabled("shell") {
		t.Fatal("expected shell to remain enabled (different tag)")
	}
}

func TestToolCommand_EnableUnknownTool(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool enable nonexistent")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected error about unknown tool, got: %q", res.Reply)
	}
}

func TestToolCommand_ListShowsEnabledWithMarker(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("http_get")

	res := cmd.Execute(context.Background(), "s1", "/tool list")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "[enabled]") || !strings.Contains(res.Reply, "http_get") {
		t.Fatalf("expected http_get to show as enabled, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "[disabled]") {
		t.Fatalf("expected other tools to show as disabled by default, got: %q", res.Reply)
	}
}

func TestToolCommand_ListWithTags(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool list")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "#filesystem") || !strings.Contains(res.Reply, "#dangerous") {
		t.Fatalf("expected tags with # prefix in listing, got: %q", res.Reply)
	}
}

func TestToolCommand_PersistsAcrossSessions(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	cmd.Execute(context.Background(), "s1", "/tool enable read_file")

	session2, _ := sm.GetOrCreate(context.Background(), "testns", "s2")
	if session2.IsToolEnabled("read_file") {
		t.Fatal("expected new sessions to have no tools enabled by default (opt-in model)")
	}

	session1, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	if !session1.IsToolEnabled("read_file") {
		t.Fatal("expected session1 to retain its tool settings")
	}
}

func TestToolCommand_EnableNonexistentTag(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool enable #bogus")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "no tools") {
		t.Fatalf("expected 'no tools' message, got: %q", res.Reply)
	}
}

func TestToolCommand_EnableMultipleTags(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/tool enable #network #dangerous")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "http_get") {
		t.Fatalf("expected http_get enabled via 'network' tag, got: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "shell") {
		t.Fatalf("expected shell enabled via 'dangerous' tag, got: %q", res.Reply)
	}
	if !session.IsToolEnabled("http_get") {
		t.Fatal("expected http_get to be enabled")
	}
	if !session.IsToolEnabled("shell") {
		t.Fatal("expected shell to be enabled")
	}
	if session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to NOT be enabled (only network and dangerous tags were enabled)")
	}
}

func TestToolCommand_DisableMultipleTags(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "s1")
	session.EnableTool("http_get")
	session.EnableTool("shell")
	session.EnableTool("read_file")

	res := cmd.Execute(context.Background(), "s1", "/tool disable #network #dangerous")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if session.IsToolEnabled("http_get") {
		t.Fatal("expected http_get to be disabled")
	}
	if session.IsToolEnabled("shell") {
		t.Fatal("expected shell to be disabled")
	}
	if !session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to remain enabled (different tags)")
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

	_, _ = sm.GetOrCreate(context.Background(), "testns", "tg-session")

	res := cmd.Execute(context.Background(), "tg-session", "/tool enable shell")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "unknown") {
		t.Fatalf("expected error about unknown tool, got: %q", res.Reply)
	}

	res = cmd.Execute(context.Background(), "tg-session", "/tool enable http_get")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "http_get") {
		t.Fatalf("expected http_get enabled, got: %q", res.Reply)
	}

	res = cmd.Execute(context.Background(), "tg-session", "/tool list")
	if strings.Contains(res.Reply, "read_file") || strings.Contains(res.Reply, "shell") {
		t.Fatalf("expected only http_get in restricted list, got: %q", res.Reply)
	}
}

// ---------------------------------------------------------------------------
// /start command tests
// ---------------------------------------------------------------------------

func TestStartCommand_CreatesNewSession(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	// Create an initial session so we have something to start from
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/start")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if res.Action != ActionSessionCreate {
		t.Fatalf("expected ActionSessionCreate, got %v", res.Action)
	}
	if res.Session == nil {
		t.Fatal("expected a new session in result, got nil")
	}
	if res.Session.ID == "s1" {
		t.Fatal("expected a different session ID, not the old one")
	}
	if !strings.Contains(res.Reply, "started fresh session") {
		t.Fatalf("expected startup message, got: %q", res.Reply)
	}
}

func TestStartCommand_EnablesAllTools(t *testing.T) {
	_, cmd, sm, tools := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/start")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}

	session := res.Session
	// All registered tools should be enabled
	for _, schema := range tools.Schemas() {
		if !session.IsToolEnabled(schema.Name) {
			t.Fatalf("expected tool %q to be enabled after /start, but it's disabled", schema.Name)
		}
	}
}

func TestStartCommand_WithNoToolRegistry_StillWorks(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	// newCommandComponents does NOT call SetTools, so Tools is nil
	_, _ = sm.GetOrCreate(context.Background(), "testns", "s1")

	res := cmd.Execute(context.Background(), "s1", "/start")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if res.Session == nil {
		t.Fatal("expected a new session")
	}
}

func TestStartCommand_HelpIncludesStart(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/help")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "/start") {
		t.Fatalf("expected /help to mention /start, got: %q", res.Reply)
	}
}

func TestHelp_IncludesStart(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/help")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	if !strings.Contains(res.Reply, "/start") {
		t.Fatalf("expected /start in help, got: %q", res.Reply)
	}
}

// TestStartCommand_PreservesProvidedCWD verifies that /start preserves the
// client CWD from the previous session instead of resetting to server CWD.
func TestStartCommand_PreservesProvidedCWD(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	// Create old session WITH a client CWD (simulating what the gateway does)
	oldSession, _ := sm.GetOrCreate(context.Background(), "testns", "old-session")
	oldSession.ClientCWD = "/client/project"
	sm.Save(context.Background(), "testns", oldSession)

	res := cmd.Execute(context.Background(), "old-session", "/start")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	session := res.Session
	if session == nil {
		t.Fatal("expected a session from /start")
	}
	if session.ClientCWD != "/client/project" {
		t.Fatalf("BUG: /start should preserve the client CWD from the old session.\n"+
			"  expected ClientCWD = %q\n"+
			"  got               %q",
			"/client/project", session.ClientCWD)
	}
}

// TestSessionCreateCommand_PreservesProvidedCWD verifies that /session create
// preserves the client CWD from the previous session.
func TestSessionCreateCommand_PreservesProvidedCWD(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	oldSession, _ := sm.GetOrCreate(context.Background(), "testns", "old-session")
	oldSession.ClientCWD = "/workspace"
	sm.Save(context.Background(), "testns", oldSession)

	res := cmd.Execute(context.Background(), "old-session", "/session create")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	session := res.Session
	if session == nil {
		t.Fatal("expected a session from /session create")
	}
	if session.ClientCWD != "/workspace" {
		t.Fatalf("BUG: /session create should preserve the client CWD from the old session.\n"+
			"  expected ClientCWD = %q\n"+
			"  got               %q",
			"/workspace", session.ClientCWD)
	}
}

// TestStartCommand_WithoutCWD_UsesServerCWD verifies that /start without
// a client CWD still defaults to the server's working directory (backward
// compatibility).
func TestStartCommand_WithoutCWD_UsesServerCWD(t *testing.T) {
	_, cmd, sm, _ := newCommandComponentsWithTools(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "old-session")

	res := cmd.Execute(context.Background(), "old-session", "/start")
	if !res.Handled || res.Error != nil {
		t.Fatalf("handle: %v %v", res.Handled, res.Error)
	}
	session := res.Session
	if session == nil {
		t.Fatal("expected a session from /start")
	}
	// When no CWD is provided, the session should still have the server CWD
	// (set by NewSession). We just verify it's not empty.
	if session.ClientCWD == "" {
		t.Fatal("expected ClientCWD to be set to server's CWD when no cwd is provided")
	}
}


// ---------------------------------------------------------------------------
// /session delete this — delete current session with "this" keyword
// ---------------------------------------------------------------------------

func TestSessionDeleteThis_DeletesCurrentSession(t *testing.T) {
	_, cmd, sm := newCommandComponents(t)
	_, _ = sm.GetOrCreate(context.Background(), "testns", "session1")

	res := cmd.Execute(context.Background(), "session1", "/session delete this")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Action != ActionClearSession {
		t.Fatalf("expected ActionClearSession when deleting current session with 'this', got: %v", res.Action)
	}
	if !strings.Contains(res.Reply, "deleted") {
		t.Fatalf("expected delete confirmation, got reply: %q", res.Reply)
	}

	// Verify the session is actually removed from the store
	_, ok, _ := sm.Store.Get(context.Background(), "testns", "session1")
	if ok {
		t.Fatal("expected session to be deleted from store")
	}
}

func TestSessionDeleteThis_WithNoSession(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)

	res := cmd.Execute(context.Background(), "", "/session delete this")

	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Reply, "error") && !strings.Contains(res.Reply, "no session") {
		t.Fatalf("should handle delete this with no current session gracefully")
	}
}

// ---------------------------------------------------------------------------
// /continue command
// ---------------------------------------------------------------------------

func TestHandleContinue(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/continue")
	if !res.Handled {
		t.Fatal("expected /continue to be handled")
	}
	if res.Action != ActionContinue {
		t.Fatalf("expected ActionContinue, got %v", res.Action)
	}
	if res.Reply != "" {
		t.Fatalf("expected empty reply for /continue, got %q", res.Reply)
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
}

func TestHandleContinueWithExtraArgs(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	// Extra args after /continue should be ignored — the command still triggers a continue.
	res := cmd.Execute(context.Background(), "sid", "/continue with extra args")
	if !res.Handled {
		t.Fatal("expected /continue to be handled even with extra args")
	}
	if res.Action != ActionContinue {
		t.Fatalf("expected ActionContinue, got %v", res.Action)
	}
}

func TestHandleContinueWithBotUsernameSuffix(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/continue@my_bot")
	if !res.Handled {
		t.Fatal("expected /continue@my_bot to be handled")
	}
	if res.Action != ActionContinue {
		t.Fatalf("expected ActionContinue, got %v", res.Action)
	}
}

func TestHelpIncludesContinue(t *testing.T) {
	_, cmd, _ := newCommandComponents(t)
	res := cmd.Execute(context.Background(), "sid", "/help")
	if !res.Handled {
		t.Fatal("expected /help to be handled")
	}
	if !strings.Contains(res.Reply, "/continue") {
		t.Fatalf("expected /help to mention /continue, got: %q", res.Reply)
	}
}
