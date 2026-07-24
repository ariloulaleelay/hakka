package gateways

import (
	"context"
	"strings"
	"testing"
)

// TestTelegramGateway_HelpShowsSpacesNotUnderscores verifies that /help
// displays command names with spaces (e.g. "/session list") rather than
// underscores (e.g. "/session_list").
func TestTelegramGateway_HelpShowsSpacesNotUnderscores(t *testing.T) {
	th := newTelegramTestHelper(t)
	defer th.close()

	th.dispatch(444, "/help")

	msg, ok := th.lastSentMessage()
	if !ok {
		t.Fatal("expected a sent message")
	}

	// Should NOT have underscore-separated names like /session_list
	if strings.Contains(msg.Text, "/session_list") {
		t.Fatalf("help should not show underscore-separated command names, got: %q", msg.Text)
	}
	// Should have space-separated names like /session list
	if !strings.Contains(msg.Text, "/session list") {
		t.Fatalf("help should show space-separated command names like '/session list', got: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "/session create") {
		t.Fatalf("help should show '/session create', got: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "/model switch") {
		t.Fatalf("help should show '/model switch', got: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "/tool allow") {
		t.Fatalf("help should show '/tool allow', got: %q", msg.Text)
	}
}

// TestTelegramGateway_ParseSessionSwitch verifies that /session switch <id> works
// as an alias for /session get <id>.
func TestTelegramGateway_ParseSessionSwitch(t *testing.T) {
	// Test parseSlashCommand directly
	jc := parseSlashCommand("/session switch abc123")
	if jc == nil {
		t.Fatal("expected jsonCmd, got nil")
	}
	if jc.Cmd != "get_session" {
		t.Fatalf("expected cmd 'get_session', got %q", jc.Cmd)
	}
	if jc.Params == nil {
		t.Fatal("expected non-nil params")
	}

	// Also verify that /session switch with no ID returns nil
	jc2 := parseSlashCommand("/session switch")
	if jc2 != nil {
		t.Fatalf("expected nil for '/session switch' without ID, got %+v", jc2)
	}
}

// TestTelegramGateway_ToolsPreEnabledByDefault verifies that a fresh
// Telegram session has all safe tools (http_get, random, feedback,
// session tools) pre-enabled and no dangerous tools.
func TestTelegramGateway_ToolsPreEnabledByDefault(t *testing.T) {
	th := newTelegramTestHelper(t)
	defer th.close()

	// Dispatch a message so a session is created
	th.dispatch(12345, "hello")

	// Get the session that was created
	namespace := "tg:12345"
	sessions, err := th.conv.Sessions().List(context.Background(), namespace)
	if err != nil {
		t.Fatalf("List sessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("no sessions found")
	}
	session := sessions[0]

	// Tools that should be pre-enabled for Telegram
	expectedEnabled := []string{
		"show_tool",   // pre-enabled by default for all sessions
		"http_get",
		"random",
		"feedback",
		"session_list",
		"session_create",
		"get_session",
		"session_info",
		"session_rename",
		"session_delete",
		"session_autorename",
		"session_read",
		"session_search",
		"session_summarize",
		"session_ask_question",
	}

	for _, name := range expectedEnabled {
		if !session.IsToolEnabled(name) {
			t.Errorf("expected tool %q to be enabled by default in Telegram session, but it was not", name)
		}
	}

	// subagent_run should be denied (completely invisible)
	if session.IsToolDenied("subagent_run") {
		// This is OK
	} else {
		// It may not be in the registry at all, which is also OK
		// Just verify it's not enabled
		if session.IsToolEnabled("subagent_run") {
			t.Errorf("subagent_run should not be enabled in Telegram session")
		}
	}

	// read_file and shell should NOT be enabled (filesystem/shell tools)
	if session.IsToolEnabled("read_file") {
		t.Errorf("read_file should not be enabled in Telegram session")
	}
	if session.IsToolEnabled("shell") {
		t.Errorf("shell should not be enabled in Telegram session")
	}
}
