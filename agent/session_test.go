package agent

import (
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
	s.Append(Message{Role: RoleUser, Content: "hello"})
	s.Append(Message{Role: RoleAssistant, Content: "hi"})

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
	s.Append(Message{Role: RoleUser, Content: "hi"})
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
	s.SetClientCWD("") // clear default CWD for simplicity
	var wg sync.WaitGroup
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Append(Message{Role: RoleUser, Content: "x"})
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
	s.SetSessionName("My Chat")
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
	s.EnableTool("read_file")
	if !s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled after EnableTool")
	}
	s.DisableTool("read_file")
	if s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled after DisableTool")
	}
}

func TestEnabledTools_EnableByTag(t *testing.T) {
	s := NewSession("testns", "sys")
	s.DisableTool("read_file")
	s.EnableTool("write_file")
	s.EnableTool("list_dir")
	s.DisableTool("write_file")

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
	s.DisableTool("read_file")
	s.EnableTool("search")
	s.DisableTool("shell")

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
	err := s.DisableTool("show_tool")
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
