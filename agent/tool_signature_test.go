package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// =========================================================================
// ToolSchema.Signature — Pythonic function signatures for the system prompt
// =========================================================================

func TestToolSchema_Signature_NoParams(t *testing.T) {
	s := ToolSchema{
		Name: "hello",
	}
	if got := s.Signature(); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestToolSchema_Signature_OnlyRequired(t *testing.T) {
	s := ToolSchema{
		Name: "read_file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "file path"},
			},
			"required": []string{"path"},
		},
	}
	if got := s.Signature(); got != "read_file(path)" {
		t.Fatalf("expected 'read_file(path)', got %q", got)
	}
}

func TestToolSchema_Signature_RequiredAndOptional(t *testing.T) {
	s := ToolSchema{
		Name: "read_file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "file path"},
				"limit": map[string]any{"type": "integer", "description": "Optional max lines (default 200)"},
			},
			"required": []string{"path"},
		},
	}
	if got := s.Signature(); got != "read_file(path, limit=200)" {
		t.Fatalf("expected 'read_file(path, limit=200)', got %q", got)
	}
}

func TestToolSchema_Signature_MultipleRequired(t *testing.T) {
	s := ToolSchema{
		Name: "random",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"min_value": map[string]any{"type": "integer", "description": "Minimum"},
				"max_value": map[string]any{"type": "integer", "description": "Maximum"},
			},
			"required": []string{"min_value", "max_value"},
		},
	}
	got := s.Signature()
	if !strings.Contains(got, "random(") {
		t.Fatalf("expected 'random(...)', got %q", got)
	}
	if !strings.Contains(got, "min_value") || !strings.Contains(got, "max_value") {
		t.Fatalf("expected both params in signature, got %q", got)
	}
}

func TestToolSchema_Signature_BoolDefaultFalse(t *testing.T) {
	s := ToolSchema{
		Name: "edit_file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"old":        map[string]any{"type": "string"},
				"new":        map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required": []string{"path", "old", "new"},
		},
	}
	got := s.Signature()
	if !strings.Contains(got, "replace_all=False") {
		t.Fatalf("expected replace_all=False, got %q", got)
	}
}

func TestToolSchema_Signature_OptionalObjectNone(t *testing.T) {
	s := ToolSchema{
		Name: "http_get",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":      map[string]any{"type": "string"},
				"headers":  map[string]any{"type": "object", "description": "Optional headers"},
				"max_bytes": map[string]any{"type": "integer", "description": "Max bytes (default 65536)"},
			},
			"required": []string{"url"},
		},
	}
	got := s.Signature()
	if !strings.Contains(got, "url") {
		t.Fatalf("expected url param, got %q", got)
	}
	// headers has no default extracted → should be headers=None (non-bool, non-defaulted)
	if !strings.Contains(got, "headers=None") {
		t.Fatalf("expected headers=None (optional object without default), got %q", got)
	}
}

func TestToolSchema_Signature_ExtractDefaultKB(t *testing.T) {
	s := ToolSchema{
		Name: "http_get",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":       map[string]any{"type": "string"},
				"max_bytes": map[string]any{"type": "integer", "description": "Max body bytes (default 64KB)"},
			},
			"required": []string{"url"},
		},
	}
	// 64KB → 65536
	if got := s.Signature(); !strings.Contains(got, "max_bytes=65536") {
		t.Fatalf("expected max_bytes=65536 (64KB converted), got %q", got)
	}
}

func TestBuildToolListMessage_SignatureFormat(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{
			Name:        "read_file",
			Description: "Read a file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "file path"},
					"limit": map[string]any{"type": "integer", "description": "Optional max lines (default 200)"},
				},
				"required": []string{"path"},
			},
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool("read_file")

	msg := BuildToolListMessage(r, s)
	if !strings.Contains(msg, "read_file(path, limit=200)") {
		t.Fatalf("expected Pythonic signature in tool list, got: %q", msg)
	}
}

func TestBuildToolListMessage_ToolWithoutParams(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{
			Name:        "session_create",
			Description: "Create a new session.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool("session_create")

	msg := BuildToolListMessage(r, s)
	if !strings.Contains(msg, "session_create") {
		t.Fatalf("expected tool name in list, got: %q", msg)
	}
}

func TestBuildToolListMessage_MultipleToolsWithSignatures(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{
			Name:        "read_file",
			Description: "Read a file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})
	r.Register(Tool{
		Schema: ToolSchema{
			Name:        "list_dir",
			Description: "List directory entries.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
	})

	s := NewSession("ns", "sys")
	s.EnableTool("read_file")
	s.EnableTool("list_dir")

	msg := BuildToolListMessage(r, s)
	if !strings.Contains(msg, "read_file(path)") {
		t.Fatalf("expected signature for read_file, got: %q", msg)
	}
	if !strings.Contains(msg, "list_dir(path)") {
		t.Fatalf("expected signature for list_dir, got: %q", msg)
	}
}
