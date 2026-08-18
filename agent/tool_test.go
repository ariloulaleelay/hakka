package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestToolRegistryRegisterAndSchemas(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo", Description: "echo", Parameters: map[string]any{}},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	if _, ok := r.Get("echo"); !ok {
		t.Fatal("expected to find registered tool")
	}
	schemas := r.Schemas()
	if len(schemas) != 1 || schemas[0].Name != "echo" {
		t.Fatalf("unexpected schemas: %+v", schemas)
	}
}

func TestToolRegistryExecuteUnknown(t *testing.T) {
	r := NewToolRegistry()
	out := r.Execute(context.Background(), "nope", `{}`)
	if !out.IsError() {
		t.Fatalf("expected error result, got success: %+v", out)
	}
	if !strings.Contains(out.Err.Error(), "unknown tool") {
		t.Fatalf("expected unknown-tool error, got %q", out.Err)
	}
}

func TestToolRegistryExecuteHandlerError(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "boom"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", errors.New("kaboom")
		},
	})
	out := r.Execute(context.Background(), "boom", "")
	if !out.IsError() {
		t.Fatalf("expected error result, got success: %+v", out)
	}
	if !strings.Contains(out.Err.Error(), "kaboom") {
		t.Fatalf("expected error payload, got %q", out.Err)
	}
}

func TestToolRegistryExecuteSuccess(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "add"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				A, B float64
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]float64{"sum": args.A + args.B})
			return string(b), nil
		},
	})
	out := r.Execute(context.Background(), "add", `{"A":2,"B":3}`)
	if !strings.Contains(out.Output, `"sum":5`) {
		t.Fatalf("unexpected output: %q", out.Output)
	}
	if out.IsError() {
		t.Fatalf("expected success, got error: %v", out.Err)
	}
}

// TestToolResultDoesNotConfuseLiteralErrorPrefix is the red test that
// motivates the typed-result refactor. Previously, success / error was
// detected by scanning the result for the literal substring "Error: ".
// That makes a tool whose legitimate output happens to start with
// "Error: " (e.g. a read_file result whose first line is `Error: foo`)
// misclassify as a failure.
//
// With the typed event.ToolResult, success vs error is carried by the
// Err field — independent of the human-readable Output.
func TestToolResultDoesNotConfuseLiteralErrorPrefix(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "logreader"},
		// A handler that successfully returns a payload whose first line
		// happens to start with "Error: " — e.g. the contents of a log
		// file being read by read_file.
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Error: connection refused\n(this is a log line, not a tool failure)", nil
		},
	})

	res := r.Execute(context.Background(), "logreader", `{}`)

	if res.IsError() {
		t.Fatalf("tool returned no error but result was classified as error: %+v", res)
	}
	if res.Err != nil {
		t.Fatalf("expected nil Err on successful handler, got %v", res.Err)
	}
	if !strings.HasPrefix(res.Output, "Error: connection refused") {
		t.Fatalf("expected raw output preserved, got %q", res.Output)
	}
}

// TestToolResultHandlerErrorIsTyped verifies a handler that returns an
// error produces a ToolResult with a non-nil Err and a model-facing
// rendering that starts with "Error: ".
func TestToolResultHandlerErrorIsTyped(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "boom"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", errors.New("kaboom")
		},
	})

	res := r.Execute(context.Background(), "boom", `{}`)

	if !res.IsError() {
		t.Fatalf("expected error result, got success: %+v", res)
	}
	if res.Err == nil || res.Err.Error() != "kaboom" {
		t.Fatalf("expected Err 'kaboom', got %v", res.Err)
	}
	if got := res.ForLLM(); !strings.HasPrefix(got, "Error: kaboom") {
		t.Fatalf("expected ForLLM to start with 'Error: kaboom', got %q", got)
	}
}

func TestExecSnippet_NoSnippetReturnsEmpty(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "noop"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
		// no ExecSnippet set
	})
	snippet := r.ExecSnippet("noop", `{}`)
	if snippet != "" {
		t.Fatalf("expected empty snippet, got %q", snippet)
	}
}

func TestExecSnippet_UnknownToolReturnsEmpty(t *testing.T) {
	r := NewToolRegistry()
	snippet := r.ExecSnippet("nope", `{}`)
	if snippet != "" {
		t.Fatalf("expected empty snippet, got %q", snippet)
	}
}

func TestExecSnippet_CalledWithArgs(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
		ExecSnippet: func(args json.RawMessage) string {
			var v struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(args, &v)
			return fmt.Sprintf("msg=%q", v.Msg)
		},
	})
	snippet := r.ExecSnippet("echo", `{"msg":"hello world"}`)
	if snippet != `msg="hello world"` {
		t.Fatalf("expected snippet, got %q", snippet)
	}
}

// ---------------------------------------------------------------------------
// Tags and per-session tool control tests
//
// Tool authorisation model:
//
//   - Tools tagged "tool" (e.g. list_tools, enable_tool, show_tool) are
//     ALWAYS available to the LLM — they let it discover and enable other
//     tools at runtime. They bypass the session's EnabledTools check.
//
//   - All other tools follow the session's opt-in model: when a session
//     has NO explicit tool configuration (EnabledTools is nil/empty), NO
//     non-"tool" tools are returned. The user must explicitly enable tools
//     via /tool enable or /tool disable commands.
//
//   - Once at least one tool has been explicitly enabled or disabled
//     (EnabledTools becomes non-nil), the map is consulted: tools with
//     a true value are enabled, tools with a false value are disabled,
//     and tools not mentioned are disabled (opt-in model — you must
//     explicitly enable tools you want). "tool"-tagged tools are still
//     always included regardless.
// ---------------------------------------------------------------------------

func TestToolWithTags(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "reader"},
		Tags:   []string{"filesystem", "read"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "danger"},
		Tags:   []string{"dangerous", "exec"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "notool"},
		// no Tags set
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	allTags := r.AllTags()
	expected := []string{"dangerous", "exec", "filesystem", "read"}
	if len(allTags) != len(expected) {
		t.Fatalf("expected %d tags, got %d: %v", len(expected), len(allTags), allTags)
	}
	for _, tag := range expected {
		found := false
		for _, t := range allTags {
			if t == tag {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing tag %q in %v", tag, allTags)
		}
	}
}

func TestSchemasForSession_EmptyEnabledGetsNoTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	session := NewSession("testns", "sys")
	// New session pre-enables management tools (show_tool, allow_tool, deny_tool)
	// but the registry only has "echo" which is not pre-enabled.
	// Since echo is allowed but not enabled, SchemasForSession returns 0.

	schemas := r.SchemasForSession(session)
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas for session with no matching pre-enabled tools, got %d: %+v", len(schemas), schemas)
	}
}

func TestSchemasForSession_ToolTaggedToolsMustBeEnabled(t *testing.T) {
	r := NewToolRegistry()
	// A tool with the "tool" tag — no longer special, must be enabled
	r.Register(Tool{
		Schema: ToolSchema{Name: "list_tools"},
		Tags:   []string{"tool", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	// Management tools (show_tool, allow_tool, deny_tool) are pre-enabled in
	// the session, but "list_tools" is not a management tool.

	session := NewSession("testns", "sys")
	// list_tools is allowed but not enabled → not in API tools section
	schemas := r.SchemasForSession(session)
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas (list_tools must be explicitly enabled), got %d: %+v", len(schemas), schemas)
	}

	// When explicitly enabled, it should appear
	session.EnableTool(context.Background(), "list_tools")
	schemas = r.SchemasForSession(session)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema after enabling list_tools, got %d: %+v", len(schemas), schemas)
	}
	if schemas[0].Name != "list_tools" {
		t.Fatalf("expected schema 'list_tools', got %q", schemas[0].Name)
	}
}

func TestExecuteForSession_ToolTaggedFollowsAllowDeny(t *testing.T) {
	r := NewToolRegistry()
	// A tool tagged "tool" — follows the same allow/deny rules as any tool
	r.Register(Tool{
		Schema: ToolSchema{Name: "list_tools"},
		Tags:   []string{"tool", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "tool-list-result", nil
		},
	})
	// A normal tool
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("testns", "sys")
	// Both are allowed (not denied) but disabled.
	// Mutual activation: disabled + allowed tools can still execute.

	// list_tools should execute even though disabled (mutual activation)
	result := r.ExecuteForSession(context.Background(), session, "list_tools", `{}`)
	if result.IsError() {
		t.Fatalf("expected success for allowed-but-disabled tool (mutual activation), got error: %v", result.Err)
	}
	if result.Output != "tool-list-result" {
		t.Fatalf("expected 'tool-list-result', got %q", result.Output)
	}

	// Normal tool should also work via mutual activation
	result = r.ExecuteForSession(context.Background(), session, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("expected success for allowed-but-disabled tool (mutual activation), got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}

	// Denied tools should be rejected
	session.DenyTool(context.Background(), "list_tools")
	result = r.ExecuteForSession(context.Background(), session, "list_tools", `{}`)
	if !result.IsError() {
		t.Fatalf("expected error for denied tool, got success: %+v", result)
	}
	if !strings.Contains(result.Err.Error(), "denied") {
		t.Fatalf("expected 'denied' error, got %q", result.Err)
	}
}

func TestSchemasForSession_SomeEnabled(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	session := NewSession("testns", "sys")
	session.EnableTool(context.Background(), "alpha")
	session.DisableTool(context.Background(), "beta") // explicitly disable beta

	schemas := r.SchemasForSession(session)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d: %+v", len(schemas), schemas)
	}
	if schemas[0].Name != "alpha" {
		t.Fatalf("expected schema 'alpha', got %q", schemas[0].Name)
	}
}

func TestSchemasForSession_AllEnabled(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	session := NewSession("testns", "sys")
	session.EnableTool(context.Background(), "alpha")
	session.EnableTool(context.Background(), "beta")

	schemas := r.SchemasForSession(session)
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d: %+v", len(schemas), schemas)
	}
}

func TestSchemasByTags(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "read_file"},
		Tags:   []string{"filesystem", "read"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "write_file"},
		Tags:   []string{"filesystem", "write"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "http_get"},
		Tags:   []string{"network"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	// Filter by single tag
	schemas := r.SchemasByTags("network")
	if len(schemas) != 1 || schemas[0].Name != "http_get" {
		t.Fatalf("expected 1 schema (http_get), got %+v", schemas)
	}

	// Filter by multiple tags (OR logic)
	schemas = r.SchemasByTags("read", "write")
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas for read+write tags, got %d: %+v", len(schemas), schemas)
	}

	// No matching tags
	schemas = r.SchemasByTags("nonexistent")
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas for nonexistent tag, got %d", len(schemas))
	}
}

func TestSchemasForSession_RespectsDeny(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "read_file"},
		Tags:   []string{"filesystem", "read"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	r.Register(Tool{
		Schema: ToolSchema{Name: "shell"},
		Tags:   []string{"dangerous", "exec"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	session := NewSession("testns", "sys")
	session.EnableTool(context.Background(), "read_file")
	session.EnableTool(context.Background(), "shell")
	session.DenyTool(context.Background(), "shell") // denied → not in schemas

	schemas := r.SchemasForSession(session)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (read_file only, shell denied), got %d: %+v", len(schemas), schemas)
	}
	if schemas[0].Name != "read_file" {
		t.Fatalf("expected schema 'read_file', got %q", schemas[0].Name)
	}
}

func TestExecute_DeniedToolReturnsError(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("testns", "sys")
	session.DenyTool(context.Background(), "echo") // explicitly deny

	result := r.ExecuteForSession(context.Background(), session, "echo", `{}`)
	if !result.IsError() {
		t.Fatalf("expected error result for denied tool, got %+v", result)
	}
	if !strings.Contains(result.Err.Error(), "denied") {
		t.Fatalf("expected error about tool being denied, got %q", result.Err)
	}
}

func TestExecute_EnabledToolSucceeds(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("testns", "sys")
	session.EnableTool(context.Background(), "echo")

	result := r.ExecuteForSession(context.Background(), session, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}
}

func TestExecute_DisabledToolMutualActivation(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	session := NewSession("testns", "sys")
	// echo is NOT enabled (disabled by default), but is allowed.
	// Mutual activation: allowed-but-disabled tools can still execute.

	result := r.ExecuteForSession(context.Background(), session, "echo", `{}`)
	if result.IsError() {
		t.Fatalf("expected success for allowed-but-disabled tool (mutual activation), got error: %v", result.Err)
	}
	if result.Output != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Output)
	}
}

func TestTool_TagsDefaultEmpty(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema: ToolSchema{Name: "simple"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	tool, ok := r.Get("simple")
	if !ok {
		t.Fatal("expected to find tool")
	}
	if len(tool.Tags) != 0 {
		t.Fatalf("expected empty Tags by default, got %v", tool.Tags)
	}
}

func TestExecSnippet_GuidelineLength(t *testing.T) {
	r := NewToolRegistry()
	r.Register(Tool{
		Schema:  ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil },
		ExecSnippet: func(args json.RawMessage) string {
			return `msg="hello world"`
		},
	})
	snippet := r.ExecSnippet("echo", `{"msg":"hello"}`)
	// The snippet should be whatever the function returns — the 60-char
	// limit is a guideline for snippet authors, not enforced by the registry.
	if snippet != `msg="hello world"` {
		t.Fatalf("expected snippet, got %q", snippet)
	}
}
