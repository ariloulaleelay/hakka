package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// newTestRegistry builds a ToolRegistry with a few sample tools for testing
// tool management tools.
func newTestRegistry() *agent.ToolRegistry {
	r := agent.NewToolRegistry()
	r.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "read_file",
			Description: "Read a UTF-8 text file from disk and return its contents.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "Absolute or relative path"},
					"max_bytes": map[string]any{"type": "integer", "description": "Optional max bytes"},
				},
				"required": []any{"path"},
			},
		},
		Tags:    []string{"filesystem", "read", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "write_file",
			Description: "Create or overwrite a file with the given content.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Absolute or relative path"},
					"content": map[string]any{"type": "string", "description": "File content"},
				},
				"required": []any{"path", "content"},
			},
		},
		Tags:    []string{"filesystem", "write", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "shell",
			Description: "Execute a shell command via `sh -c`.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{"type": "string", "description": "Command to execute"},
				},
				"required": []any{"cmd"},
			},
		},
		Tags:    []string{"dangerous", "exec", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	return r
}

// ctxWithSessionNS returns a context with namespace, session ID, and session view set.
func ctxWithSessionNS(ns, sessionID string, sess *agent.Session) context.Context {
	ctx := context.Background()
	ctx = event.ContextWithNamespace(ctx, ns)
	ctx = event.ContextWithSessionID(ctx, sessionID)
	ctx = event.ContextWithSessionView(ctx, sess)
	return ctx
}

// ---------------------------------------------------------------------------
// show_tool tests
// ---------------------------------------------------------------------------

func TestShowTool(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	res := runPlainCtx(t, context.Background(), tool.Handler, map[string]any{"name": "read_file"})
	if !strings.Contains(res, "read_file") {
		t.Fatalf("expected tool name in output, got: %q", res)
	}
	if !strings.Contains(res, "Read a UTF-8") {
		t.Fatalf("expected description in output, got: %q", res)
	}
	if !strings.Contains(res, "path") {
		t.Fatalf("expected parameter 'path' in output, got: %q", res)
	}
	if !strings.Contains(res, "max_bytes") {
		t.Fatalf("expected parameter 'max_bytes' in output, got: %q", res)
	}
	// Should indicate required params
	if !strings.Contains(res, "required") {
		t.Fatalf("expected 'required' indicator in output, got: %q", res)
	}
}

func TestShowTool_Unknown(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	errMsg := runErrCtx(t, context.Background(), tool.Handler, map[string]any{"name": "nonexistent_tool"})
	if !strings.Contains(errMsg, "unknown") {
		t.Fatalf("expected 'unknown tool' error, got: %q", errMsg)
	}
}

func TestShowTool_MissingName(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	errMsg := runErrCtx(t, context.Background(), tool.Handler, map[string]any{})
	if !strings.Contains(errMsg, "name") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing name, got: %q", errMsg)
	}
}

func TestShowTool_ParameterDetails(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	res := runPlainCtx(t, context.Background(), tool.Handler, map[string]any{"name": "write_file"})
	if !strings.Contains(res, "path") {
		t.Fatalf("expected path param, got: %q", res)
	}
	if !strings.Contains(res, "content") {
		t.Fatalf("expected content param, got: %q", res)
	}
	// Both path and content are required for write_file
	if !strings.Contains(res, "required") {
		t.Fatalf("expected 'required' indicator, got: %q", res)
	}
}

// ---------------------------------------------------------------------------
// Schema and tags validation tests
// ---------------------------------------------------------------------------

func TestToolManagementTools_HaveCorrectTags(t *testing.T) {
	r := newTestRegistry()

	tool := ShowTool(r)

	name := tool.Schema.Name
	hasTool := false
	hasAll := false
	for _, tag := range tool.Tags {
		if tag == "tool" {
			hasTool = true
		}
		if tag == "all" {
			hasAll = true
		}
	}
	if !hasTool {
		t.Errorf("tool %q missing tag %q; tags: %v", name, "tool", tool.Tags)
	}
	if !hasAll {
		t.Errorf("tool %q missing tag %q; tags: %v", name, "all", tool.Tags)
	}
}

func TestToolManagementTools_NoEmptyNames(t *testing.T) {
	r := newTestRegistry()

	tool := ShowTool(r)

	if tool.Schema.Name == "" {
		t.Error("tool has empty name")
	}
	if tool.Handler == nil {
		t.Errorf("tool %q has nil handler", tool.Schema.Name)
	}
}

func TestListToolsNotRegistered(t *testing.T) {
	r := agent.NewToolRegistry()
	RegisterToolManagementTools(r)

	if _, ok := r.Get("list_tools"); ok {
		t.Fatal("list_tools should NOT be registered anymore")
	}

	// allow_tool and deny_tool should NOT be registered — they are
	// human-only slash commands, not LLM-callable tools.
	if _, ok := r.Get("allow_tool"); ok {
		t.Fatal("allow_tool should NOT be registered as an LLM tool")
	}
	if _, ok := r.Get("deny_tool"); ok {
		t.Fatal("deny_tool should NOT be registered as an LLM tool")
	}
	// Only show_tool should be registered
	if _, ok := r.Get("show_tool"); !ok {
		t.Fatal("show_tool should be registered")
	}
}
