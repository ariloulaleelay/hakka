package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// =========================================================================
// NEW MODEL TESTS
//
// Tool status model (v2):
//   - Denied:  completely invisible to the LLM (not in system prompt, not
//              in API tools, not executable)
//   - Allowed: visible in system prompt list. May be:
//       - Enabled:  appears in API "tools" section (callable)
//       - Disabled: does NOT appear in API tools section (but mutual
//                   activation allows execution if called anyway)
//   - Locked:  once a tool has been called in a session, it can't be
//              denied or disabled.
// =========================================================================

// ---------------------------------------------------------------------------
// Session-level tests
// ---------------------------------------------------------------------------

func TestNewSession_ToolAllowedByDefault(t *testing.T) {
	s := NewSession("ns", "sys")
	if !s.IsToolAllowed("read_file") {
		t.Fatal("new session: all tools should be allowed by default")
	}
	if s.IsToolDenied("read_file") {
		t.Fatal("new session: no tools should be denied by default")
	}
}

func TestNewSession_ToolDisabledByDefault(t *testing.T) {
	s := NewSession("ns", "sys")
	if s.IsToolEnabled("read_file") {
		t.Fatal("new session: non-management tools should be disabled by default")
	}
}

func TestDenyTool(t *testing.T) {
	s := NewSession("ns", "sys")
	s.DenyTool(context.Background(), "read_file")
	if !s.IsToolDenied("read_file") {
		t.Fatal("expected read_file to be denied")
	}
	if s.IsToolAllowed("read_file") {
		t.Fatal("expected read_file to not be allowed")
	}
}

func TestAllowTool(t *testing.T) {
	s := NewSession("ns", "sys")
	s.DenyTool(context.Background(), "read_file")
	s.AllowTool(context.Background(), "read_file")
	if s.IsToolDenied("read_file") {
		t.Fatal("expected read_file to be allowed again")
	}
	if !s.IsToolAllowed("read_file") {
		t.Fatal("expected read_file to be allowed")
	}
}

func TestDenyTool_LockedToolFails(t *testing.T) {
	s := NewSession("ns", "sys")
	// Tool is locked when it appears in a tool call in conversation history
	s.AddMessages(context.Background(), []Message{Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c1", Name: "read_file", Arguments: `{"path":"x"}`},
	}}}, 0, 0)
	if !s.IsToolLocked("read_file") {
		t.Fatal("expected read_file to be locked")
	}
	err := s.DenyTool(context.Background(), "read_file")
	if err == nil {
		t.Fatal("expected error when denying a locked tool")
	}
	if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "used") {
		t.Fatalf("expected error about tool being locked/used, got: %v", err)
	}
}

func TestDisableTool_LockedToolFails(t *testing.T) {
	s := NewSession("ns", "sys")
	// Tool is locked when it appears in a tool call in conversation history
	s.AddMessages(context.Background(), []Message{Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c1", Name: "read_file", Arguments: `{"path":"x"}`},
	}}}, 0, 0)
	if !s.IsToolLocked("read_file") {
		t.Fatal("expected read_file to be locked")
	}
	err := s.DisableTool(context.Background(), "read_file")
	if err == nil {
		t.Fatal("expected error when disabling a locked tool")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("expected error about tool being locked, got: %v", err)
	}
}

func TestIsToolLocked_ByHistory(t *testing.T) {
	s := NewSession("ns", "sys")
	// A tool is locked when it appears in a tool call in the history
	s.AddMessages(context.Background(), []Message{Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c1", Name: "read_file", Arguments: `{"path":"x"}`},
	}}}, 0, 0)
	if !s.IsToolLocked("read_file") {
		t.Fatal("expected read_file to be locked after tool call in history")
	}
	if s.IsToolLocked("other_tool") {
		t.Fatal("expected other_tool to not be locked")
	}
}

func TestEnableTool(t *testing.T) {
	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "read_file")
	if !s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled")
	}
}

func TestDisableTool(t *testing.T) {
	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "read_file")
	err := s.DisableTool(context.Background(), "read_file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be disabled")
	}
}

func TestIsToolEnabled_RequiresAllowed(t *testing.T) {
	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "read_file")
	s.DenyTool(context.Background(), "read_file")
	// Denied takes precedence — even if "enabled", a denied tool is not enabled
	if s.IsToolEnabled("read_file") {
		t.Fatal("denied tool should not be enabled even if EnableTool was called")
	}
}

// ---------------------------------------------------------------------------
// SchemasForSession tests (returns only enabled tools for API "tools" section)
// ---------------------------------------------------------------------------

func TestSchemasForSession_OnlyEnabledTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "alpha")

	schemas := r.SchemasForSession(s)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (enabled tools only), got %d: %+v", len(schemas), schemas)
	}
	if schemas[0].Name != "alpha" {
		t.Fatalf("expected schema 'alpha', got %q", schemas[0].Name)
	}
}

func TestSchemasForSession_DeniedToolsExcluded(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "alpha")
	s.DenyTool(context.Background(), "alpha") // denied overrides enable

	schemas := r.SchemasForSession(s)
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas (denied tool excluded), got %d", len(schemas))
	}
}

func TestSchemasForSession_DisabledToolsNotInAPI(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	// alpha is not enabled → it's disabled (but still allowed)

	schemas := r.SchemasForSession(s)
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas for disabled tool, got %d", len(schemas))
	}
}

func TestSchemasForSession_ManagementToolsEnabledByDefault(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "show_tool"},
		Tags:    []string{"tool", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "read_file"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	// Only show_tool is pre-enabled

	schemas := r.SchemasForSession(s)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (show_tool only), got %d: %+v", len(schemas), schemaNames(schemas))
	}
	if schemas[0].Name != "show_tool" {
		t.Fatalf("expected schema 'show_tool', got %q", schemas[0].Name)
	}
}

// ---------------------------------------------------------------------------
// AllowedSchemas tests (returns all allowed tools for system prompt listing)
// ---------------------------------------------------------------------------

func TestAllowedSchemas_IncludesAllAllowed(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	// Both are allowed (not denied). alpha is enabled, beta is disabled.

	schemas := r.AllowedSchemas(s)
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas (all allowed), got %d: %+v", len(schemas), schemaNames(schemas))
	}
}

func TestAllowedSchemas_DeniedExcluded(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.DenyTool(context.Background(), "beta")

	schemas := r.AllowedSchemas(s)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (denied excluded), got %d: %+v", len(schemas), schemaNames(schemas))
	}
	if schemas[0].Name != "alpha" {
		t.Fatalf("expected schema 'alpha', got %q", schemas[0].Name)
	}
}

// ---------------------------------------------------------------------------
// ExecuteForSession tests
// ---------------------------------------------------------------------------

func TestExecuteForSession_EnabledToolSucceeds(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "hello", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "echo")

	result := r.ExecuteForSession(context.Background(), s, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}
}

func TestExecuteForSession_DisabledToolMutualActivation(t *testing.T) {
	// Mutual activation: a disabled but allowed tool can still be called
	// if the LLM somehow invokes it.
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "hello", nil },
	})

	s := NewSession("ns", "sys")
	// echo is NOT enabled (disabled by default), but IS allowed

	result := r.ExecuteForSession(context.Background(), s, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("mutual activation: disabled but allowed tool should execute, got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}
}

func TestExecuteForSession_DeniedToolRejected(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "hello", nil },
	})

	s := NewSession("ns", "sys")
	s.DenyTool(context.Background(), "echo")

	result := r.ExecuteForSession(context.Background(), s, "echo", `{}`)
	if !result.IsError() {
		t.Fatalf("expected error for denied tool, got success: %+v", result)
	}
	if !strings.Contains(result.Err.Error(), "denied") {
		t.Fatalf("expected 'denied' error, got: %v", result.Err)
	}
}

func TestExecuteForSession_LockedToolCanRun(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "hello", nil },
	})

	s := NewSession("ns", "sys")
	// Lock by adding a tool call to history
	s.AddMessages(context.Background(), []Message{Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c1", Name: "echo", Arguments: `{}`},
	}}}, 0, 0)

	result := r.ExecuteForSession(context.Background(), s, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("locked tool should still execute, got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}
}

// ---------------------------------------------------------------------------
// Context tool list generation
// ---------------------------------------------------------------------------

func TestBuildToolListMessage_IncludesAllowedTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "read_file", Description: "Read a file."},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "write_file", Description: "Write a file."},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "read_file")
	// read_file is enabled, write_file is disabled (but allowed)

	msg := BuildToolListMessage(r, s)
	if msg == "" {
		t.Fatal("expected non-empty tool list message")
	}
	if !strings.Contains(msg, "read_file") {
		t.Fatalf("expected read_file in tool list, got: %q", msg)
	}
	if !strings.Contains(msg, "write_file") {
		t.Fatalf("expected write_file in tool list, got: %q", msg)
	}
	if !strings.Contains(msg, "Read a file") {
		t.Fatalf("expected description in tool list, got: %q", msg)
	}
	if !strings.Contains(msg, "show_tool") {
		t.Fatalf("expected show_tool hint in tool list, got: %q", msg)
	}
}

func TestBuildToolListMessage_DeniedExcluded(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "read_file", Description: "Read a file."},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema:  ToolSchema{Name: "secret_tool", Description: "Secret."},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool(context.Background(), "read_file")
	s.DenyTool(context.Background(), "secret_tool")

	msg := BuildToolListMessage(r, s)
	if strings.Contains(msg, "secret_tool") {
		t.Fatalf("denied tool should not appear in tool list, got: %q", msg)
	}
	if !strings.Contains(msg, "read_file") {
		t.Fatalf("expected read_file in tool list, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func schemaNames(schemas []ToolSchema) []string {
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	return names
}
