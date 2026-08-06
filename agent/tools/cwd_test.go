package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// runWithCWD invokes a tool handler with a context that has a CWD set.
// Expects JSON result (used by shell, search).
func runWithCWD(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any, cwd string) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx := event.ContextWithCWD(context.Background(), cwd)
	res, err := h(ctx, raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("parse: %v (%s)", err, res)
	}
	return out
}

// runWithCWDPlain invokes a tool handler with CWD context and returns
// the plain-text result (used by read_file, write_file, edit_file, list_dir).
func runWithCWDPlain(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any, cwd string) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx := event.ContextWithCWD(context.Background(), cwd)
	res, err := h(ctx, raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	return res
}

// TestShellRespectsCWDFromContext verifies that when a CWD is set in the
// context, the shell tool runs the command in that directory, even without
// a cwd argument.
func TestShellRespectsCWDFromContext(t *testing.T) {
	// Create a temp directory with a marker file
	baseDir := t.TempDir()
	markerPath := filepath.Join(baseDir, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("hello from cwd"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Run shell command that checks the marker file exists (relative path)
	out := runWithCWD(t, Shell(nil).Handler, map[string]any{
		"cmd": "cat marker.txt",
	}, baseDir)

	if int(out["exit_code"].(float64)) != 0 {
		t.Fatalf("expected exit_code=0, got %v: %+v", out["exit_code"], out)
	}
	stdout, ok := out["stdout"].(string)
	if !ok {
		t.Fatalf("expected stdout to be a string, got %T: %+v", out["stdout"], out)
	}
	if !strings.Contains(stdout, "hello from cwd") {
		t.Fatalf("expected stdout to contain 'hello from cwd', got %q", stdout)
	}
}

// TestShellCWDFromContextOverridenByExplicitArg verifies that if both
// context CWD and explicit cwd arg are present, the explicit arg wins.
func TestShellCWDFromContextOverridenByExplicitArg(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := os.WriteFile(filepath.Join(dirA, "info.txt"), []byte("from A"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "info.txt"), []byte("from B"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Context says dirA, explicit arg says dirB — explicit should win
	raw, _ := json.Marshal(map[string]any{
		"cmd": "cat info.txt",
		"cwd": dirB,
	})
	ctx := event.ContextWithCWD(context.Background(), dirA)
	res, err := Shell(nil).Handler(ctx, raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("parse: %v (%s)", err, res)
	}
	if int(out["exit_code"].(float64)) != 0 {
		t.Fatalf("expected exit_code=0, got %v: %+v", out["exit_code"], out)
	}
	stdout, ok := out["stdout"].(string)
	if !ok {
		t.Fatalf("expected stdout to be a string, got %T: %+v", out["stdout"], out)
	}
	if !strings.Contains(stdout, "from B") {
		t.Fatalf("expected stdout to contain 'from B', got %q", stdout)
	}
}

// TestReadFileRespectsCWDFromContext verifies read_file resolves relative
// paths against the CWD from context.
func TestReadFileRespectsCWDFromContext(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := runWithCWDPlain(t, ReadFile().Handler, map[string]any{
		"path": "hello.txt", // relative path
	}, baseDir)

	if !strings.Contains(res, "world") {
		t.Fatalf("expected 'world' in result, got: %q", res)
	}
}

// TestReadFileWithAbsolutePathStillWorks verifies that absolute paths are
// not affected by the CWD context.
func TestReadFileWithAbsolutePathStillWorks(t *testing.T) {
	baseDir := t.TempDir()
	otherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "file.txt"), []byte("from base"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "file.txt"), []byte("from other"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Use absolute path to the other dir's file, context CWD points to baseDir
	absPath := filepath.Join(otherDir, "file.txt")
	res := runWithCWDPlain(t, ReadFile().Handler, map[string]any{
		"path": absPath,
	}, baseDir)

	if !strings.Contains(res, "from other") {
		t.Fatalf("expected 'from other' in result, got: %q", res)
	}
}

// TestWriteFileRespectsCWDFromContext verifies write_file resolves relative
// paths against the CWD from context.
func TestWriteFileRespectsCWDFromContext(t *testing.T) {
	baseDir := t.TempDir()

	res := runWithCWDPlain(t, WriteFile(nil).Handler, map[string]any{
		"path":    "nested/output.txt",
		"content": "data",
	}, baseDir)

	if !strings.Contains(res, "Written 4 bytes") {
		t.Fatalf("expected 'Written 4 bytes', got: %q", res)
	}

	// Check the file was created at baseDir/nested/output.txt
	content, err := os.ReadFile(filepath.Join(baseDir, "nested", "output.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "data" {
		t.Fatalf("content: %q", content)
	}
}

// TestEditFileRespectsCWDFromContext verifies edit_file resolves relative
// paths against the CWD from context.
func TestEditFileRespectsCWDFromContext(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "example.txt"), []byte("foo foo"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := runWithCWDPlain(t, EditFile(nil).Handler, map[string]any{
		"path": "example.txt", // relative path
		"old":  "foo",
		"new":  "bar",
	}, baseDir)

	if !strings.Contains(res, "Replaced 1 occurrence(s)") {
		t.Fatalf("expected 'Replaced 1 occurrence(s)', got: %q", res)
	}

	content, err := os.ReadFile(filepath.Join(baseDir, "example.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "bar foo" {
		t.Fatalf("content: %q", content)
	}
}

// TestListDirRespectsCWDFromContext verifies list_dir resolves relative
// paths against the CWD from context.
func TestListDirRespectsCWDFromContext(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := runWithCWDPlain(t, ListDir().Handler, map[string]any{
		"path": ".", // relative — should resolve to CWD
	}, baseDir)

	if !strings.Contains(res, "a.txt") {
		t.Fatalf("expected 'a.txt' in result, got: %q", res)
	}
}

// TestSearchRespectsCWDFromContext verifies search resolves relative
// paths against the CWD from context.
func TestSearchRespectsCWDFromContext(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}

	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "search.txt"), []byte("needle\nhaystack\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := runWithCWDPlain(t, Search().Handler, map[string]any{
		"pattern": "needle",
		"path":    ".", // relative — should resolve to CWD
	}, baseDir)
	if res == "" {
		t.Fatalf("expected at least 1 match")
	}
}

// TestToolWithoutCWDInContextStillWorks verifies that tools work fine
// even when no CWD is set in the context (backward compatibility).
func TestToolWithoutCWDInContextStillWorks(t *testing.T) {
	// Use a temp dir with an absolute path — no CWD context needed
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// runPlain() uses context.Background() with no CWD
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": filePath})
	if !strings.Contains(res, "hello") {
		t.Fatalf("expected 'hello' in result, got: %q", res)
	}
}

// TestShellWithoutCWDInContextDefaultsToServerCWD verifies that when neither
// context CWD nor args CWD is set, the shell tool runs in the server's CWD
// (the default behavior of exec.Command - Dir is empty means process cwd).
func TestShellWithoutCWDInContextDefaultsToServerCWD(t *testing.T) {
	// Just run pwd and check we get something reasonable
	out := run(t, Shell(nil).Handler, map[string]any{"cmd": "pwd"})
	if int(out["exit_code"].(float64)) != 0 {
		t.Fatalf("expected exit_code=0, got %v", out["exit_code"])
	}
	stdout, ok := out["stdout"].(string)
	if !ok {
		t.Fatalf("expected stdout string, got %T", out["stdout"])
	}
	if !strings.Contains(stdout, "/") {
		t.Fatalf("expected pwd to contain '/', got %q", stdout)
	}
}

// TestAllToolsRespectCWD is a meta-test that verifies every registered
// tool that accepts path-like parameters correctly uses the CWD from
// context. This is a regression guard: if someone adds a new tool with
// file-path parameters but forgets to call resolvePath() or
// CWDFromContext(), this test will catch it.
//
// The test inspects the tool's JSON schema to determine whether it has
// path-related parameters. If it does, a basic CWD-respecting scenario
// is exercised.
func TestAllToolsRespectCWD(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg, nil)

	schemas := reg.Schemas()

	// Tools that are exempt from CWD testing because they don't deal
	// with filesystem paths at all.
	noPathTools := map[string]string{
		"http_get":        "URL-based, no filesystem paths",
		"vim_run_command": "Lua commands in Neovim, no filesystem paths",
	}

	// Parameter names that indicate a tool operates on filesystem paths.
	pathParamNames := []string{"path", "cwd"}

	for _, schema := range schemas {
		// Skip tools that are known not to deal with paths
		if _, exempt := noPathTools[schema.Name]; exempt {
			continue
		}

		t.Run(schema.Name, func(t *testing.T) {
			params, ok := schema.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("schema.Parameters.properties is not a map for tool %q", schema.Name)
			}

			// Check if the schema has any path-related parameter.
			hasPathParam := false
			for _, pname := range pathParamNames {
				if _, exists := params[pname]; exists {
					hasPathParam = true
					break
				}
			}

			if !hasPathParam {
				// No path-like parameters — nothing to test for CWD compliance.
				// But warn so we know if a new tool with paths is added but
				// pathParamNames doesn't cover its parameter name.
				t.Logf("no path parameter found in schema; parameters: %v", keysOf(params))
				return
			}

			// If the tool has path parameters, we need to verify that it
			// resolves relative paths against the CWD from context.
			tool, ok := reg.Get(schema.Name)
			if !ok {
				t.Fatalf("tool %q not found in registry", schema.Name)
			}

			// Create a temp dir with a known file
			baseDir := t.TempDir()

			// Different tools need different setup
			switch schema.Name {
			case "shell":
				// Shell: create a marker file and run a command that reads it
				markerPath := filepath.Join(baseDir, "cwd_test_marker.txt")
				if err := os.WriteFile(markerPath, []byte("cwd-works"), 0o644); err != nil {
					t.Fatalf("write marker: %v", err)
				}
				args := map[string]any{
					"cmd": "cat cwd_test_marker.txt",
				}
				out := runWithCWD(t, tool.Handler, args, baseDir)
				if int(out["exit_code"].(float64)) != 0 {
					t.Fatalf("shell exit_code=%v, expected 0; output: %+v", out["exit_code"], out)
				}
				stdout, ok := out["stdout"].(string)
				if !ok {
					t.Fatalf("expected stdout string, got %T: %+v", out["stdout"], out)
				}
				if !strings.Contains(stdout, "cwd-works") {
					t.Fatalf("expected stdout to contain 'cwd-works', got %q", stdout)
				}

			case "search":
				// Search: needs rg installed, create a file to search in
				if _, err := exec.LookPath("rg"); err != nil {
					t.Skip("rg not installed")
				}
				if err := os.WriteFile(filepath.Join(baseDir, "greet.txt"), []byte("hello-cwd\n"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				args := map[string]any{
					"pattern": "hello-cwd",
					"path":    ".", // relative — should resolve to CWD
				}
				res := runWithCWDPlain(t, tool.Handler, args, baseDir)
				if res == "" {
					t.Fatalf("expected at least 1 match")
				}

			default:
				// read_file, write_file, edit_file, list_dir: create a file
				// and use a relative path to access it.
				testFilePath := "cwd_test_file.txt"
				fullPath := filepath.Join(baseDir, testFilePath)
				if err := os.WriteFile(fullPath, []byte("cwd-content"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}

				switch schema.Name {
				case "read_file":
					args := map[string]any{"path": testFilePath}
					res := runWithCWDPlain(t, tool.Handler, args, baseDir)
					if !strings.Contains(res, "cwd-content") {
						t.Fatalf("expected 'cwd-content' in result, got: %q", res)
					}

				case "list_dir":
					args := map[string]any{"path": "."}
					res := runWithCWDPlain(t, tool.Handler, args, baseDir)
					if !strings.Contains(res, testFilePath) {
						t.Fatalf("expected %q in directory listing, got: %q", testFilePath, res)
					}

				case "write_file":
					newFile := "new_cwd_file.txt"
					args := map[string]any{
						"path":    newFile,
						"content": "written-via-cwd",
					}
					res := runWithCWDPlain(t, tool.Handler, args, baseDir)
					if !strings.Contains(res, "Written") {
						t.Fatalf("expected 'Written' in result, got: %q", res)
					}
					// Verify the file was created in the CWD directory
					if _, err := os.Stat(filepath.Join(baseDir, newFile)); err != nil {
						t.Fatalf("file should exist in CWD dir: %v", err)
					}

				case "edit_file":
					// Write initial content, then edit
					if err := os.WriteFile(fullPath, []byte("old-content"), 0o644); err != nil {
						t.Fatalf("write: %v", err)
					}
					args := map[string]any{
						"path": testFilePath,
						"old":  "old-content",
						"new":  "edited-content",
					}
					res := runWithCWDPlain(t, tool.Handler, args, baseDir)
					if !strings.Contains(res, "Replaced 1 occurrence(s)") {
						t.Fatalf("expected 'Replaced 1 occurrence(s)', got: %q", res)
					}

				default:
					t.Fatalf("unhandled tool %q with path parameters", schema.Name)
				}
			}
		})
	}
}

// keysOf returns the keys of a string-keyed map as a sorted slice.
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
