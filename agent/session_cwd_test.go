package agent

import (
	"context"
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
	s.SetClientCWD(context.Background(), "/home/user/project")
	if s.Read().ClientCWD != "/home/user/project" {
		t.Fatalf("expected /home/user/project, got %q", s.Read().ClientCWD)
	}
}

func TestMessagesDoesNotIncludeCWD(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	s.SetClientCWD(context.Background(), "/home/user/project")
	if err := s.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	if sp := s.SystemPrompt(); sp != "you are helpful" {
		t.Fatalf("expected system prompt 'you are helpful', got %q", sp)
	}
	msgs := s.Messages()
	// Should be exactly 1: user message (CWD is not injected into Messages)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleUser {
		t.Fatalf("unexpected roles: %+v", msgs)
	}
	// No message should contain the CWD path
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "/home/user/project") {
			t.Fatalf("Messages() should not contain CWD, got: %+v", msgs)
		}
	}
}

func TestMessagesDoesNotIncludeCWDWhenEmpty(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	s.SetClientCWD(context.Background(), "") // explicitly clear it
	if err := s.AddMessages(context.Background(), []Message{Message{Role: RoleUser, Content: "hello"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	if sp := s.SystemPrompt(); sp != "you are helpful" {
		t.Fatalf("expected system prompt 'you are helpful', got %q", sp)
	}
	msgs := s.Messages()
	// Should be exactly 1: user message
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
}

func TestCWDMessageReturnsMessageWhenSet(t *testing.T) {
	s := NewSession("testns", "sys prompt")
	s.SetClientCWD(context.Background(), "/workspace")

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
	s.SetClientCWD(context.Background(), "") // explicitly clear

	msg := s.CWDMessage()
	if msg != nil {
		t.Fatalf("expected nil CWDMessage when ClientCWD is empty, got %+v", msg)
	}
}

func TestNewSessionDoesNotSetSuppressCWD(t *testing.T) {
	s := NewSession("testns", "helpful assistant")
	// SuppressCWD was removed; just verify ClientCWD is set to server's cwd
	if s.Read().ClientCWD == "" {
		t.Fatal("expected ClientCWD to be set to server's CWD")
	}
}

func TestSessionGetCWD(t *testing.T) {
	s := NewSession("testns", "you are helpful")
	// Default should be server's CWD
	serverCWD, _ := os.Getwd()
	if cwd := s.GetCWD(); cwd != serverCWD {
		t.Fatalf("expected GetCWD=%q, got %q", serverCWD, cwd)
	}

	s.SetClientCWD(context.Background(), "/home/user/project")
	if cwd := s.GetCWD(); cwd != "/home/user/project" {
		t.Fatalf("expected GetCWD=/home/user/project, got %q", cwd)
	}

	s.SetClientCWD(context.Background(), "")
	if cwd := s.GetCWD(); cwd != "" {
		t.Fatalf("expected GetCWD= empty, got %q", cwd)
	}
}
