package agent

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestNewSession(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	if s.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if s.Namespace != "testns" {
		t.Fatalf("expected namespace 'testns', got %q", s.Namespace)
	}
	if s.SystemPrompt != "you are helpful" {
		t.Fatalf("unexpected system prompt: %q", s.SystemPrompt)
	}
	if len(s.Messages) != 0 {
		t.Fatal("new session must have no messages")
	}
	if s.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be set")
	}
}

func TestSessionAppendAndHistory(t *testing.T) {
	s := NewSession("testns", "sys")
	s.Append(Message{Role: RoleUser, Content: "hello"})
	s.Append(Message{Role: RoleAssistant, Content: "hi"})

	h := s.History()
	if len(h) != 3 {
		t.Fatalf("expected 3 messages incl. system, got %d", len(h))
	}
	if h[0].Role != RoleSystem || h[0].Content != "sys" {
		t.Fatalf("first message must be system: %+v", h[0])
	}
	if h[1].Role != RoleUser || h[2].Role != RoleAssistant {
		t.Fatalf("unexpected ordering: %+v", h)
	}
}

func TestSessionHistoryWithoutSystem(t *testing.T) {
	s := NewSession("testns", "")
	s.Append(Message{Role: RoleUser, Content: "hi"})
	h := s.History()
	if len(h) != 1 || h[0].Role != RoleUser {
		t.Fatalf("unexpected history: %+v", h)
	}
}

func TestSessionConcurrentAppend(t *testing.T) {
	s := NewSession("testns", "")
	s.ClientCWD = "" // clear default CWD for simplicity
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
	if got := len(s.History()); got != N {
		t.Fatalf("expected %d messages, got %d", N, got)
	}
}

func TestSessionName_DefaultIsEmpty(t *testing.T) {
	s := NewSession("testns", "sys")
	if s.Name != "" {
		t.Fatalf("expected empty Name by default, got %q", s.Name)
	}
}

func TestSessionName_DisplayNameReturnsIDWhenEmpty(t *testing.T) {
	s := NewSession("testns", "sys")
	id := s.ID
	if s.DisplayName() != id {
		t.Fatalf("expected DisplayName() to return ID %q when Name is empty, got %q", id, s.DisplayName())
	}
}

func TestSessionName_DisplayNameReturnsNameWhenSet(t *testing.T) {
	s := NewSession("testns", "sys")
	s.Name = "My Chat"
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

func TestEnabledTools_DisableTool(t *testing.T) {
	s := NewSession("testns", "sys")
	s.DisableTool("read_file")
	s.EnableTool("write_file")
	if s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled after DisableTool")
	}
	if !s.IsToolEnabled("write_file") {
		t.Fatal("expected write_file to still be enabled")
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

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored Session
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if restored.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled after JSON round-trip")
	}
	if !restored.IsToolEnabled("search") {
		t.Fatal("expected search to survive JSON round-trip")
	}
	if restored.IsToolEnabled("shell") {
		t.Fatal("expected shell to remain disabled after JSON round-trip")
	}
}

func TestEnabledTools_NewSessionHasNilMap(t *testing.T) {
	s := NewSession("testns", "sys")
	if s.EnabledTools != nil {
		t.Fatal("expected EnabledTools to be nil in new session (saves space)")
	}
}

func TestEnabledTools_FirstDisableCreatesMap(t *testing.T) {
	s := NewSession("testns", "sys")
	s.DisableTool("read_file")
	if s.EnabledTools == nil {
		t.Fatal("expected EnabledTools to be non-nil after first DisableTool")
	}
	if len(s.EnabledTools) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(s.EnabledTools))
	}
}
