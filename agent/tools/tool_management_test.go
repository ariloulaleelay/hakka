package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
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

// ---------------------------------------------------------------------------
// list_tools tests
// ---------------------------------------------------------------------------

func TestListTools_All(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := ListTools(r, sm)

	res := runPlainCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{})
	if !strings.Contains(res, "read_file") {
		t.Fatalf("expected read_file in list, got: %q", res)
	}
	if !strings.Contains(res, "write_file") {
		t.Fatalf("expected write_file in list, got: %q", res)
	}
	if !strings.Contains(res, "shell") {
		t.Fatalf("expected shell in list, got: %q", res)
	}
	// Description snippets should appear
	if !strings.Contains(res, "Read a UTF-8") {
		t.Fatalf("expected description snippet, got: %q", res)
	}
	// Status should NOT appear
	if strings.Contains(res, "[enabled]") || strings.Contains(res, "[disabled]") {
		t.Fatalf("should not include status markers, got: %q", res)
	}
	// Tags should NOT appear
	if strings.Contains(res, "#all") || strings.Contains(res, "#filesystem") {
		t.Fatalf("should not include tags, got: %q", res)
	}
}

func TestListTools_NoTools(t *testing.T) {
	r := agent.NewToolRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := ListTools(r, sm)

	res := runPlainCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{})
	if !strings.Contains(res, "no tools") && !strings.Contains(res, "No tools") {
		t.Fatalf("expected empty message, got: %q", res)
	}
}

func TestListTools_FilterByTag(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := ListTools(r, sm)

	res := runPlainCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"tag": "dangerous"})
	if !strings.Contains(res, "shell") {
		t.Fatalf("expected shell in filtered list, got: %q", res)
	}
	if strings.Contains(res, "read_file") {
		t.Fatalf("should not include read_file, got: %q", res)
	}
	if strings.Contains(res, "write_file") {
		t.Fatalf("should not include write_file, got: %q", res)
	}
}

func TestListTools_FilterByTagNoMatch(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := ListTools(r, sm)

	res := runPlainCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{"tag": "nonexistent"})
	// Should say no tools with that tag
	if !strings.Contains(res, "No tools") {
		t.Fatalf("expected 'No tools' message, got: %q", res)
	}
}

// ---------------------------------------------------------------------------
// enable_tool tests
// ---------------------------------------------------------------------------

func TestEnableTool(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := EnableTool(r, sm)

	s, err := sm.GetOrCreate(context.Background(), "ns", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = sm.Save(context.Background(), "ns", s)

	res := runPlainCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
		"name":       "read_file",
	})
	if !strings.Contains(res, "Enabled tool") && !strings.Contains(res, "read_file") {
		t.Fatalf("expected enable confirmation, got: %q", res)
	}

	// Verify it's actually enabled
	if !s.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to be enabled")
	}
}

func TestEnableTool_Unknown(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := EnableTool(r, sm)

	s, _ := sm.GetOrCreate(context.Background(), "ns", "")
	_ = sm.Save(context.Background(), "ns", s)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
		"name":       "nonexistent_tool",
	})
	if !strings.Contains(errMsg, "unknown") {
		t.Fatalf("expected 'unknown tool' error, got: %q", errMsg)
	}
}

func TestEnableTool_MissingName(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")
	tool := EnableTool(r, sm)

	s, _ := sm.GetOrCreate(context.Background(), "ns", "")
	_ = sm.Save(context.Background(), "ns", s)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"session_id": s.SessionID(),
	})
	if !strings.Contains(errMsg, "name") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing name, got: %q", errMsg)
	}
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
	sm := agent.NewSessionManager(newTestStore(), "sys")

	tools := []agent.Tool{
		ListTools(r, sm),
		EnableTool(r, sm),
		ShowTool(r),
	}

	for _, tool := range tools {
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
}

func TestToolManagementTools_NoEmptyNames(t *testing.T) {
	r := newTestRegistry()
	sm := agent.NewSessionManager(newTestStore(), "sys")

	tools := []agent.Tool{
		ListTools(r, sm),
		EnableTool(r, sm),
		ShowTool(r),
	}

	for _, tool := range tools {
		if tool.Schema.Name == "" {
			t.Error("tool has empty name")
		}
		if tool.Handler == nil {
			t.Errorf("tool %q has nil handler", tool.Schema.Name)
		}
	}
}
