package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func TestNewSession(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	if s.SessionID() == "" {
		t.Fatal("expected non-empty ID")
	}
	if s.Read().Namespace != "testns" {
		t.Fatalf("expected namespace 'testns', got %q", s.Read().Namespace)
	}
	if s.Read().SystemPrompt != "you are helpful" {
		t.Fatalf("unexpected system prompt: %q", s.Read().SystemPrompt)
	}
	if len(s.Messages()) != 0 {
		t.Fatal("new session must have no messages")
	}
	if s.Read().CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be set")
	}
}

func TestSessionAppendAndHistory(t *testing.T) {
	s := NewSession("testns", "sys")
	s.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "hello"}, Message{Role: RoleAssistant, Content: "hi"}}, 0, 0)

	if sp := s.SystemPrompt(); sp != "sys" {
		t.Fatalf("expected system prompt 'sys', got %q", sp)
	}
	msgs := s.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != RoleUser || msgs[1].Role != RoleAssistant {
		t.Fatalf("unexpected ordering: %+v", msgs)
	}
}

func TestSessionHistoryWithoutSystem(t *testing.T) {
	s := NewSession("testns", "")
	if err := s.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "hi"}}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if sp := s.SystemPrompt(); sp != "" {
		t.Fatalf("expected empty system prompt, got %q", sp)
	}
	msgs := s.Messages()
	if len(msgs) != 1 || msgs[0].Role != RoleUser {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

func TestSessionConcurrentAppend(t *testing.T) {
	s := NewSession("testns", "")
	s.SetClientCWD(context.Background(), "") // clear default CWD for simplicity
	var wg sync.WaitGroup
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.AddMessages(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, 0, 0); err != nil {
				t.Errorf("AddMessages: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(s.Messages()); got != N {
		t.Fatalf("expected %d messages, got %d", N, got)
	}
}

func TestSessionName_DefaultIsEmpty(t *testing.T) {
	s := NewSession("testns", "sys")
	if s.SessionName() != "" {
		t.Fatalf("expected empty Name by default, got %q", s.SessionName())
	}
}

func TestSessionName_DisplayNameReturnsIDWhenEmpty(t *testing.T) {
	s := NewSession("testns", "sys")
	id := s.SessionID()
	if s.DisplayName() != id {
		t.Fatalf("expected DisplayName() to return ID %q when Name is empty, got %q", id, s.DisplayName())
	}
}

func TestSessionName_DisplayNameReturnsNameWhenSet(t *testing.T) {
	s := NewSession("testns", "sys")
	s.SetSessionName(context.Background(), "My Chat")
	if s.DisplayName() != "My Chat" {
		t.Fatalf("expected DisplayName() to return %q, got %q", "My Chat", s.DisplayName())
	}
}

// ---------------------------------------------------------------------------
// EnabledTools tests
// ---------------------------------------------------------------------------

func TestEnabledTools_NewSessionAllToolsDisabled(t *testing.T) {
	s := NewSession("testns", "sys")
	if s.IsToolEnabled("any") {
		t.Fatal("expected NO tools to be enabled by default in new session (opt-in model)")
	}
}

func TestEnabledTools_DisablingToolWorks(t *testing.T) {
	s := NewSession("testns", "sys")
	s.EnableTool(context.Background(), "read_file")
	if !s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled after EnableTool")
	}
	s.DisableTool(context.Background(), "read_file")
	if s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled after DisableTool")
	}
}

func TestEnabledTools_EnableByTag(t *testing.T) {
	s := NewSession("testns", "sys")
	s.DisableTool(context.Background(), "read_file")
	s.EnableTool(context.Background(), "write_file")
	s.EnableTool(context.Background(), "list_dir")
	s.DisableTool(context.Background(), "write_file")

	if s.IsToolEnabled("read_file") {
		t.Fatal("read_file should be disabled")
	}
	if s.IsToolEnabled("write_file") {
		t.Fatal("write_file should be disabled")
	}
	if !s.IsToolEnabled("list_dir") {
		t.Fatal("list_dir should be enabled")
	}
}

func TestEnabledTools_JSONRoundTrip(t *testing.T) {
	s := NewSession("testns", "sys")
	s.DisableTool(context.Background(), "read_file")
	s.EnableTool(context.Background(), "search")
	s.DisableTool(context.Background(), "shell")

	// JSON round-trip through SessionData (used by stores).
	data := s.Read()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SessionData
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.EnabledTools == nil {
		t.Fatal("expected non-nil EnabledTools after JSON round-trip")
	}
	if decoded.EnabledTools["read_file"] {
		t.Fatal("expected read_file to be disabled after JSON round-trip")
	}
	if !decoded.EnabledTools["search"] {
		t.Fatal("expected search to survive JSON round-trip")
	}
	if decoded.EnabledTools["shell"] {
		t.Fatal("expected shell to remain disabled after JSON round-trip")
	}
}

func TestEnabledTools_NewSessionHasPreEnabled(t *testing.T) {
	s := NewSession("testns", "sys")
	// New sessions pre-enable management tools (show_tool, allow_tool, deny_tool)
	if s.Read().EnabledTools == nil {
		t.Fatal("expected EnabledTools to be non-nil in new session (management tools pre-enabled)")
	}
	if !s.IsToolEnabled("show_tool") {
		t.Fatal("expected show_tool to be pre-enabled")
	}
	// Non-management tools should not be enabled
	if s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to not be enabled by default")
	}
}

func TestEnabledTools_DisableTool(t *testing.T) {
	s := NewSession("testns", "sys")
	err := s.DisableTool(context.Background(), "show_tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.IsToolEnabled("show_tool") {
		t.Fatal("expected show_tool to be disabled")
	}
	if len(s.Read().EnabledTools) == 0 {
		t.Fatal("expected EnabledTools to have entries after DisableTool call")
	}
}

// ---------------------------------------------------------------------------
// ForkData tests
// ---------------------------------------------------------------------------

func TestForkData_EmptyForkPointReturnsBlankChild(t *testing.T) {
	sd := NewSessionData("ns", "prompt")
	sd.Model = "deepseek"
	sd.ClientCWD = "/home/user"
	sd.ID = "parent-1"

	// Add some messages — they should NOT be copied when forkPoint is "".
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hello"},
		Message{ID: "m2", Role: RoleAssistant, Content: "hi"},
	)

	child, err := sd.ForkData("")
	if err != nil {
		t.Fatal(err)
	}

	if len(child.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(child.Messages))
	}
	if child.ParentID != "parent-1" {
		t.Fatalf("expected parent_id 'parent-1', got %q", child.ParentID)
	}
	if child.ForkPoint != "" {
		t.Fatalf("expected empty fork_point, got %q", child.ForkPoint)
	}
	if child.Model != "deepseek" {
		t.Fatalf("expected model 'deepseek', got %q", child.Model)
	}
	if child.ClientCWD != "/home/user" {
		t.Fatalf("expected CWD '/home/user', got %q", child.ClientCWD)
	}
}

func TestForkData_CopiesUpToForkPointInclusive(t *testing.T) {
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "first"},
		Message{ID: "m2", Role: RoleAssistant, Content: "second"},
		Message{ID: "m3", Role: RoleUser, Content: "third"},
		Message{ID: "m4", Role: RoleAssistant, Content: "fourth"},
	)

	// Fork at m2 — child gets m1, m2.
	child, err := sd.ForkData("m2")
	if err != nil {
		t.Fatal(err)
	}

	if len(child.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(child.Messages))
	}
	if child.Messages[0].ID != "m1" {
		t.Fatalf("expected first message m1, got %s", child.Messages[0].ID)
	}
	if child.Messages[1].ID != "m2" {
		t.Fatalf("expected second message m2, got %s", child.Messages[1].ID)
	}
	if child.ParentID != "parent-1" {
		t.Fatalf("expected parent_id 'parent-1', got %q", child.ParentID)
	}
	if child.ForkPoint != "m2" {
		t.Fatalf("expected fork_point 'm2', got %q", child.ForkPoint)
	}
}

func TestForkData_NotFoundReturnsError(t *testing.T) {
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hello"},
	)

	_, err := sd.ForkData("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent fork_point")
	}
}

func TestForkData_AdvancesPastToolCalls(t *testing.T) {
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "read file"},
		Message{ID: "m2", Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{
			{ID: "tc1", Name: "read_file", Arguments: `{}`},
			{ID: "tc2", Name: "search", Arguments: `{}`},
		}},
		Message{ID: "m3", Role: RoleTool, Content: "file content", ToolCallID: "tc1"},
		Message{ID: "m4", Role: RoleTool, Content: "search result", ToolCallID: "tc2"},
		Message{ID: "m5", Role: RoleAssistant, Content: "done"},
	)

	// Fork at m2 — should advance to include m3 and m4 (tool results).
	child, err := sd.ForkData("m2")
	if err != nil {
		t.Fatal(err)
	}

	if len(child.Messages) != 4 {
		t.Fatalf("expected 4 messages (m1-m4), got %d", len(child.Messages))
	}
	if child.Messages[0].ID != "m1" {
		t.Fatalf("expected m1, got %s", child.Messages[0].ID)
	}
	if child.Messages[3].ID != "m4" {
		t.Fatalf("expected m4, got %s", child.Messages[3].ID)
	}
	// fork_point in metadata is still the originally requested one.
	if child.ForkPoint != "m2" {
		t.Fatalf("expected fork_point 'm2', got %q", child.ForkPoint)
	}
}

func TestForkData_PartialToolResults(t *testing.T) {
	// Only one tool result present — advance past it, stop at next non-tool.
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "do stuff"},
		Message{ID: "m2", Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{
			{ID: "tc1", Name: "shell", Arguments: `{}`},
		}},
		Message{ID: "m3", Role: RoleTool, Content: "output", ToolCallID: "tc1"},
		Message{ID: "m4", Role: RoleAssistant, Content: "done"},
		Message{ID: "m5", Role: RoleUser, Content: "more"},
	)

	child, err := sd.ForkData("m2")
	if err != nil {
		t.Fatal(err)
	}

	// Should get m1, m2, m3 — m4 is a new assistant turn, stops there.
	if len(child.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(child.Messages))
	}
	if child.Messages[2].ID != "m3" {
		t.Fatalf("expected m3, got %s", child.Messages[2].ID)
	}
}

func TestForkData_NoToolCallsAtForkPoint(t *testing.T) {
	// Fork at a plain assistant message — no advancement needed.
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hi"},
		Message{ID: "m2", Role: RoleAssistant, Content: "hello back"},
		Message{ID: "m3", Role: RoleUser, Content: "more"},
	)

	child, err := sd.ForkData("m2")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(child.Messages))
	}
}

func TestForkData_InheritsSettings(t *testing.T) {
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Model = "gemini"
	sd.ClientCWD = "/tmp"
	sd.CompactSoftLimit = 100
	sd.EnabledTools = map[string]bool{"tool_a": true, "tool_b": false}
	sd.BlockedTools = map[string]bool{"tool_c": true}
	sd.ActiveSkills = []string{"skill1", "skill2"}

	child, err := sd.ForkData("")
	if err != nil {
		t.Fatal(err)
	}

	if child.Model != "gemini" {
		t.Fatalf("expected model 'gemini', got %q", child.Model)
	}
	if child.ClientCWD != "/tmp" {
		t.Fatalf("expected CWD '/tmp', got %q", child.ClientCWD)
	}
	if child.CompactSoftLimit != 100 {
		t.Fatalf("expected compact limit 100, got %d", child.CompactSoftLimit)
	}
	if !child.EnabledTools["tool_a"] || child.EnabledTools["tool_b"] {
		t.Fatal("enabled tools not inherited correctly")
	}
	if !child.BlockedTools["tool_c"] {
		t.Fatal("blocked tools not inherited")
	}
	if len(child.ActiveSkills) != 2 {
		t.Fatalf("expected 2 active skills, got %d", len(child.ActiveSkills))
	}
	if child.ActiveSkills[0] != "skill1" || child.ActiveSkills[1] != "skill2" {
		t.Fatalf("active skills not inherited correctly: %v", child.ActiveSkills)
	}
}

func TestForkData_StripsUnresolvedToolCallsFromLastMessage(t *testing.T) {
	// Forking at the parent's last message (e.g. an in-flight subagent_run
	// call whose result has not been appended yet) must not leak orphaned
	// tool_calls into the child. Resolved earlier rounds survive.
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hello"},
		Message{ID: "m2", Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_1", Name: "shell", Arguments: `{"cmd":"echo hi"}`},
		}},
		Message{ID: "m3", Role: RoleTool, Content: "hi", ToolCallID: "call_1", Name: "shell"},
		Message{ID: "m4", Role: RoleAssistant, Content: "done"},
		Message{ID: "m5", Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_2", Name: "subagent_run", Arguments: `{"task":"x"}`},
			{ID: "call_3", Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	)

	child, err := sd.ForkData("m5")
	if err != nil {
		t.Fatal(err)
	}

	// m5 becomes empty (no content, all calls unresolved) and is dropped:
	// child = m1, m2, m3, m4.
	if len(child.Messages) != 4 {
		t.Fatalf("expected 4 messages after stripping unresolved calls, got %d: %+v", len(child.Messages), child.Messages)
	}
	if child.Messages[3].ID != "m4" {
		t.Fatalf("expected last child message m4, got %s", child.Messages[3].ID)
	}
	// The resolved round (m2 calls + m3 result) survives.
	if len(child.Messages[1].ToolCalls) != 1 || child.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected preserved resolved tool call call_1, got: %+v", child.Messages[1].ToolCalls)
	}
	// Lineage metadata is still the requested fork point.
	if child.ForkPoint != "m5" {
		t.Fatalf("expected fork_point 'm5', got %q", child.ForkPoint)
	}
}

func TestForkData_PreservesResolvedToolCalls(t *testing.T) {
	// When all tool calls are resolved, no messages should be removed.
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hello"},
		Message{ID: "m2", Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call_1", Name: "shell", Arguments: `{"cmd":"echo hi"}`},
		}},
		Message{ID: "m3", Role: RoleTool, Content: "hi", ToolCallID: "call_1", Name: "shell"},
		Message{ID: "m4", Role: RoleAssistant, Content: "done"},
	)

	child, err := sd.ForkData("m4")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Messages) != 4 {
		t.Fatalf("expected 4 messages preserved, got %d: %+v", len(child.Messages), child.Messages)
	}
}

func TestForkData_PreservesTextContentWhenStripping(t *testing.T) {
	// If the last assistant message has both text content and unresolved
	// tool calls, the text should survive (only the calls are stripped).
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.Messages = append(sd.Messages,
		Message{ID: "m1", Role: RoleUser, Content: "hello"},
		Message{ID: "m2", Role: RoleAssistant, Content: "I'm thinking...", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "subagent_run", Arguments: `{"task":"x"}`},
		}},
	)

	child, err := sd.ForkData("m2")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Messages) != 2 {
		t.Fatalf("expected 2 messages (text content should survive), got %d: %+v", len(child.Messages), child.Messages)
	}
	if child.Messages[1].Content != "I'm thinking..." {
		t.Fatalf("expected text content 'I'm thinking...', got: %q", child.Messages[1].Content)
	}
	if len(child.Messages[1].ToolCalls) != 0 {
		t.Fatalf("expected tool calls to be stripped, got: %+v", child.Messages[1].ToolCalls)
	}
}

func TestNewSessionData_UsesShortID(t *testing.T) {
	sd := NewSessionData("ns", "prompt")

	// Session ID must be non-empty.
	if sd.ID == "" {
		t.Fatal("session ID must not be empty")
	}

	// Session ID must NOT be a UUID (no dashes, shorter than 20 chars).
	if len(sd.ID) > 20 {
		t.Fatalf("session ID too long for a short ID: %q (len=%d)", sd.ID, len(sd.ID))
	}

	// UUIDs have dashes; short IDs don't.
	if len(sd.ID) >= 36 || containsDashHelper(sd.ID) {
		t.Fatalf("session ID looks like a UUID, want a short base62 ID: %q", sd.ID)
	}

	// All characters must be valid base62 (0-9A-Za-z).
	for _, c := range sd.ID {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			t.Fatalf("session ID %q contains invalid char %q (not base62)", sd.ID, c)
		}
	}

	// Generate a second session; IDs must be different.
	sd2 := NewSessionData("ns", "prompt2")
	if sd.ID == sd2.ID {
		t.Fatalf("two sessions got the same ID: %q", sd.ID)
	}
}

func containsDashHelper(s string) bool {
	for _, c := range s {
		if c == '-' {
			return true
		}
	}
	return false
}
