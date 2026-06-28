package agent

import (
	"os"
	"strings"
	"testing"
)

func TestSessionClientCWD(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	// Default should be server's CWD
	serverCWD, _ := os.Getwd()
	if s.Read().ClientCWD != serverCWD {
		t.Fatalf("expected default ClientCWD=%q, got %q", serverCWD, s.Read().ClientCWD)
	}

	// Can be overridden
	s.SetClientCWD("/home/user/project")
	if s.Read().ClientCWD != "/home/user/project" {
		t.Fatalf("expected /home/user/project, got %q", s.Read().ClientCWD)
	}
}

func TestHistoryDoesNotIncludeCWD(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	s.SetClientCWD("/home/user/project")
	s.Append(Message{Role: RoleUser, Content: "hello"})

	h := s.History()
	// Should be exactly 2: system prompt and user message (no CWD injection)
	if len(h) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d: %+v", len(h), h)
	}
	if h[0].Role != RoleSystem || h[1].Role != RoleUser {
		t.Fatalf("unexpected roles: %+v", h)
	}
	// No message should contain the CWD path
	for _, msg := range h {
		if strings.Contains(msg.Content, "/home/user/project") {
			t.Fatalf("History() should not contain CWD, got: %+v", h)
		}
	}
}

func TestHistoryDoesNotIncludeCWDWhenEmpty(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	s.SetClientCWD("") // explicitly clear it
	s.Append(Message{Role: RoleUser, Content: "hello"})

	h := s.History()
	// Should be exactly 2: system prompt and user message
	if len(h) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(h), h)
	}
}

func TestCWDMessageReturnsMessageWhenSet(t *testing.T) {
	s := NewSession("testns", "sys prompt")
	s.SetClientCWD("/workspace")

	msg := s.CWDMessage()
	if msg == nil {
		t.Fatal("expected non-nil CWDMessage")
	}
	if msg.Role != RoleSystem {
		t.Fatalf("expected system role, got %v", msg.Role)
	}
	if !strings.Contains(msg.Content, "/workspace") {
		t.Fatalf("CWD message should contain path, got: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "operate relative") {
		t.Fatalf("CWD message should mention relative paths, got: %q", msg.Content)
	}
}

func TestCWDMessageReturnsNilWhenEmpty(t *testing.T) {
	s := NewSession("testns", "sys prompt")
	s.SetClientCWD("") // explicitly clear

	msg := s.CWDMessage()
	if msg != nil {
		t.Fatalf("expected nil CWDMessage when ClientCWD is empty, got %+v", msg)
	}
}

func TestBuildContextIncludesCWD(t *testing.T) {
	s := NewSession("testns", "sys prompt")
	s.SetClientCWD("/workspace")
	s.Append(Message{Role: RoleUser, Content: "hello"})

	ctx := BuildContext(s)
	if len(ctx) != 3 {
		t.Fatalf("expected 3 messages (system + CWD + user), got %d: %+v", len(ctx), ctx)
	}
	if ctx[0].Role != RoleSystem || ctx[0].Content != "sys prompt" {
		t.Fatalf("first message should be system prompt, got: %+v", ctx[0])
	}
	if ctx[1].Role != RoleSystem || !strings.Contains(ctx[1].Content, "/workspace") {
		t.Fatalf("second message should be CWD info, got: %+v", ctx[1])
	}
	if ctx[2].Role != RoleUser || ctx[2].Content != "hello" {
		t.Fatalf("third message should be user, got: %+v", ctx[2])
	}
}

func TestBuildContextWithoutCWD(t *testing.T) {
	s := NewSession("testns", "sys prompt")
	s.SetClientCWD("") // explicitly clear
	s.Append(Message{Role: RoleUser, Content: "hello"})

	ctx := BuildContext(s)
	// Should be exactly 2: system prompt and user message
	if len(ctx) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d: %+v", len(ctx), ctx)
	}
}

func TestBuildContextWithoutSystemPrompt(t *testing.T) {
	s := NewSession("testns", "")
	s.SetClientCWD("/project")
	s.Append(Message{Role: RoleUser, Content: "hi"})

	ctx := BuildContext(s)
	if len(ctx) != 2 {
		t.Fatalf("expected 2 messages (CWD + user), got %d: %+v", len(ctx), ctx)
	}
	if ctx[0].Role != RoleSystem || !strings.Contains(ctx[0].Content, "/project") {
		t.Fatalf("first message should be CWD info, got: %+v", ctx[0])
	}
	if ctx[1].Role != RoleUser || ctx[1].Content != "hi" {
		t.Fatalf("second message should be user, got: %+v", ctx[1])
	}
}

func TestNewSessionDoesNotSetSuppressCWD(t *testing.T) {
	s := NewSession("testns", "helpful assistant")
	// SuppressCWD was removed; just verify ClientCWD is set to server's cwd
	if s.Read().ClientCWD == "" {
		t.Fatal("expected ClientCWD to be set to server's CWD")
	}
}
