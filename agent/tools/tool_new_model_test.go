package tools

import (
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// =========================================================================
// show_tool tests
// =========================================================================

func TestShowTool_AutoEnables(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	session := agent.NewSession("ns", "sys")
	ctx := ctxWithSessionNS("ns", session.SessionID(), session)

	res := runPlainCtx(t, ctx, tool.Handler, map[string]any{"name": "read_file"})
	if !strings.Contains(res, "read_file") {
		t.Fatalf("expected tool name in output, got: %q", res)
	}
	if !strings.Contains(res, "Read a UTF-8") {
		t.Fatalf("expected description in output, got: %q", res)
	}
	if !strings.Contains(res, "path") {
		t.Fatalf("expected parameter 'path' in output, got: %q", res)
	}

	// Verify the tool was auto-enabled
	if !session.IsToolEnabled("read_file") {
		t.Fatal("show_tool should auto-enable the tool")
	}
}

func TestShowTool_AlreadyEnabledDoesNotDuplicate(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	session := agent.NewSession("ns", "sys")
	session.EnableTool("read_file")
	ctx := ctxWithSessionNS("ns", session.SessionID(), session)

	_ = runPlainCtx(t, ctx, tool.Handler, map[string]any{"name": "read_file"})

	// Should still be enabled (no error from double-enable)
	if !session.IsToolEnabled("read_file") {
		t.Fatal("expected read_file to remain enabled")
	}
}

// ---------------------------------------------------------------------------
// Registration tests
// ---------------------------------------------------------------------------

func TestManagementToolsRegistered(t *testing.T) {
	r := agent.NewToolRegistry()
	RegisterToolManagementTools(r)

	if _, ok := r.Get("show_tool"); !ok {
		t.Fatal("show_tool should be registered")
	}

	// allow_tool and deny_tool should NOT be registered — they are
	// human-only slash commands, not LLM-callable tools.
	if _, ok := r.Get("allow_tool"); ok {
		t.Fatal("allow_tool should NOT be registered as an LLM tool")
	}
	if _, ok := r.Get("deny_tool"); ok {
		t.Fatal("deny_tool should NOT be registered as an LLM tool")
	}
}

func TestShowTool_HasCorrectTags(t *testing.T) {
	r := newTestRegistry()
	tool := ShowTool(r)

	if tool.Schema.Name == "" {
		t.Error("tool has empty name")
	}
	if tool.Handler == nil {
		t.Error("show_tool has nil handler")
	}

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
		t.Errorf("show_tool missing tag %q; tags: %v", "tool", tool.Tags)
	}
	if !hasAll {
		t.Errorf("show_tool missing tag %q; tags: %v", "all", tool.Tags)
	}
}
