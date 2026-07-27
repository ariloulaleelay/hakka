package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// run invokes a tool handler and unmarshals the JSON result.
func run(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("parse: %v (%s)", err, res)
	}
	return out
}

// runPlain invokes a tool handler and returns the plain-text result string.
func runPlain(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	return res
}

// runErr invokes a tool handler expecting an error result.
// Returns the error string (either from Go error or from an "Error:" prefix).
func runErr(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(context.Background(), raw)
	if err != nil {
		return err.Error()
	}
	// Fallback: error encoded as "Error: ..." prefix in the result string.
	if strings.HasPrefix(res, "Error: ") {
		return strings.TrimPrefix(res, "Error: ")
	}
	t.Fatalf("expected error result, got: %q", res)
	return ""
}

func TestReadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hello.txt")
	_ = os.WriteFile(p, []byte("hello world"), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p})
	want := "hello world\n"
	if res != want {
		t.Fatalf("expected %q, got: %q", want, res)
	}
}

func TestReadFileTruncated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	// 100 lines, each "x"
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "limit": 10})
	// Should show truncated footer with omitted lines, then content, then TRUNCATED marker
	if !strings.Contains(res, "[TRUNCATED:") {
		t.Fatalf("expected truncation marker in result, got: %q", res)
	}
	if !strings.Contains(res, strings.Repeat("x\n", 9)+"x") {
		t.Fatalf("expected first 10 lines in result, got: %q", res)
	}
	// Footer says [TRUNCATED: N lines omitted] where N is omitted (90)
	if !strings.Contains(res, "90 lines omitted") {
		t.Fatalf("expected footer to say '90 lines omitted' (the omitted count), got: %q", res)
	}
	// Truncation message should mention offset/limit
	if !strings.Contains(res, "offset") || !strings.Contains(res, "limit") {
		t.Fatalf("truncation message should mention offset/limit, got: %q", res)
	}
}

func TestReadFileError(t *testing.T) {
	errMsg := runErr(t, ReadFile().Handler, map[string]any{"path": "/no/such/file/here"})
	if !strings.Contains(errMsg, "no such file") {
		t.Fatalf("expected error about missing file, got: %q", errMsg)
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	res := runPlain(t, ListDir().Handler, map[string]any{"path": dir})
	if !strings.Contains(res, "a.txt") {
		t.Fatalf("expected a.txt in listing, got: %q", res)
	}
	if !strings.Contains(res, "sub/") {
		t.Fatalf("expected sub/ in listing, got: %q", res)
	}
	if !strings.Contains(res, "(dir)") {
		t.Fatalf("expected (dir) marker for sub, got: %q", res)
	}
	if !strings.Contains(res, "bytes") {
		t.Fatalf("expected size info in listing, got: %q", res)
	}
	// a.txt should appear before sub/ (sorted)
	aIdx := strings.Index(res, "a.txt")
	subIdx := strings.Index(res, "sub/")
	if aIdx < 0 || subIdx < 0 || aIdx > subIdx {
		t.Fatalf("expected a.txt before sub/, got: %q", res)
	}
}

func TestWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "file.txt")
	res := runPlain(t, WriteFile().Handler, map[string]any{"path": p, "content": "data"})
	if !strings.Contains(res, "Written 4 bytes") {
		t.Fatalf("expected 'Written 4 bytes', got: %q", res)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "data" {
		t.Fatalf("expected written content %q, got: %q", "data", string(b))
	}
}

func TestEditFileFirstOccurrence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(p, []byte("foo foo"), 0o644)
	res := runPlain(t, EditFile().Handler, map[string]any{"path": p, "old": "foo", "new": "bar"})
	if !strings.Contains(res, "Replaced 1 occurrence(s)") {
		t.Fatalf("expected 'Replaced 1 occurrence(s)', got: %q", res)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "bar foo" {
		t.Fatalf("expected file content %q after editing, got: %q", "bar foo", string(b))
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(p, []byte("foo foo foo"), 0o644)
	res := runPlain(t, EditFile().Handler, map[string]any{"path": p, "old": "foo", "new": "bar", "replace_all": true})
	if !strings.Contains(res, "Replaced 3 occurrence(s)") {
		t.Fatalf("expected 'Replaced 3 occurrence(s)', got: %q", res)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "bar bar bar" {
		t.Fatalf("expected file content %q after replace-all, got: %q", "bar bar bar", string(b))
	}
}

func TestEditFileNotFound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(p, []byte("abc"), 0o644)
	errMsg := runErr(t, EditFile().Handler, map[string]any{"path": p, "old": "xyz", "new": "q"})
	if !strings.Contains(errMsg, "pattern not found") {
		t.Fatalf("expected pattern not found error, got: %q", errMsg)
	}
}

func TestShellInlinesShortOutput(t *testing.T) {
	out := run(t, Shell().Handler, map[string]any{"cmd": "echo hello"})
	if int(out["exit_code"].(float64)) != 0 {
		t.Fatalf("exit_code: %v", out["exit_code"])
	}
	stdout := out["stdout"].(string)
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("stdout not inlined: %q", stdout)
	}
	stderr := out["stderr"]
	if stderr != "" {
		t.Fatalf("stderr should be empty, got: %q", stderr)
	}
}

func TestShellNonZero(t *testing.T) {
	out := run(t, Shell().Handler, map[string]any{"cmd": "exit 3"})
	if int(out["exit_code"].(float64)) != 3 {
		t.Fatalf("exit_code: %v", out["exit_code"])
	}
}

func TestShellLargeOutputGoesToFile(t *testing.T) {
	// Produce ~10000 bytes of stdout — well above the inline threshold.
	out := run(t, Shell().Handler, map[string]any{
		"cmd": "yes hakka | head -c 10000",
	})
	if int(out["exit_code"].(float64)) != 0 {
		t.Fatalf("exit_code: %v", out["exit_code"])
	}
	// Expect a tempfile reference instead of inlined content.
	stdout, ok := out["stdout"].(map[string]any)
	if !ok {
		t.Fatalf("stdout should be an object for large output, got: %T %+v", out["stdout"], out["stdout"])
	}
	if stdout["truncated"] != true {
		t.Fatalf("expected truncated=true, got: %v", stdout["truncated"])
	}
	path, _ := stdout["path"].(string)
	if path == "" {
		t.Fatalf("expected path in stdout object, got: %+v", stdout)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("tempfile missing: %v", err)
	}
	if info.Size() < 10000 {
		t.Fatalf("tempfile too small: %d", info.Size())
	}
	_ = os.Remove(path)
}

func TestShellEmptyCmd(t *testing.T) {
	errMsg := runErr(t, Shell().Handler, map[string]any{"cmd": "   "})
	if !strings.Contains(errMsg, "cmd is required") {
		t.Fatalf("expected cmd is required error, got: %q", errMsg)
	}
}

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Probe", "yes")
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()
	res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL})
	if !strings.Contains(res, "Status: 200") {
		t.Fatalf("expected Status: 200 in output, got: %q", res)
	}
	if !strings.Contains(res, "pong") {
		t.Fatalf("expected body %q in output, got: %q", "pong", res)
	}
}

func TestSearchSkippedWhenNoRG(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\n"), 0o644)
	out := run(t, Search().Handler, map[string]any{"pattern": "alpha", "path": dir})
	matches := out["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("no matches: %+v", out)
	}
}

// --- ExecSnippet tests ---

func snippetFor(t *testing.T, tool agent.Tool, args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	if tool.ExecSnippet == nil {
		return ""
	}
	return tool.ExecSnippet(raw)
}

func TestExecSnippet_ReadFile(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		wantCont  string // substring expected in snippet
		wantEmpty bool
	}{
		{name: "with path", args: map[string]any{"path": "src/main.go"}, wantCont: `"src/main.go"`},
		{name: "empty args", args: map[string]any{}, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := snippetFor(t, ReadFile(), tt.args)
			if tt.wantEmpty {
				if s != "" {
					t.Fatalf("expected empty snippet, got %q", s)
				}
				return
			}
			if !strings.Contains(s, tt.wantCont) {
				t.Fatalf("expected %q in snippet, got %q", tt.wantCont, s)
			}
		})
	}
}

func TestExecSnippet_ListDir(t *testing.T) {
	s := snippetFor(t, ListDir(), map[string]any{"path": "."})
	if !strings.Contains(s, `"."`) {
		t.Fatalf("expected path in snippet, got %q", s)
	}
}

func TestExecSnippet_WriteFileShowsPathOnly(t *testing.T) {
	s := snippetFor(t, WriteFile(), map[string]any{"path": "/tmp/foo.txt", "content": "secret data"})
	if !strings.Contains(s, `"/tmp/foo.txt"`) {
		t.Fatalf("expected path in snippet, got %q", s)
	}
	if strings.Contains(s, "secret") {
		t.Fatalf("snippet should not contain content, got %q", s)
	}
}

func TestExecSnippet_EditFile(t *testing.T) {
	s := snippetFor(t, EditFile(), map[string]any{"path": "main.go", "old": "foo", "new": "bar"})
	if !strings.Contains(s, `"main.go"`) {
		t.Fatalf("expected path in snippet, got %q", s)
	}
	if !strings.Contains(s, `old="foo"`) {
		t.Fatalf("expected old in snippet, got %q", s)
	}
}

func TestEditFileExecSnippet_TruncatesLongOld(t *testing.T) {
	longOld := strings.Repeat("x", 100)
	s := snippetFor(t, EditFile(), map[string]any{"path": "main.go", "old": longOld})
	// Server no longer truncates — full old value is sent to the client
	if !strings.Contains(s, longOld) {
		t.Fatalf("expected full old value in snippet (client truncates), got %q", s)
	}
}

func TestShellExecSnippet(t *testing.T) {
	s := snippetFor(t, Shell(), map[string]any{"cmd": "echo hello"})
	if !strings.Contains(s, `echo hello`) {
		t.Fatalf("expected cmd in snippet, got %q", s)
	}
}

func TestShellExecSnippet_TruncatesLongCmd(t *testing.T) {
	longCmd := "echo " + strings.Repeat("x", 100)
	s := snippetFor(t, Shell(), map[string]any{"cmd": longCmd})
	// Server no longer truncates — full cmd is sent to the client
	if !strings.Contains(s, longCmd) {
		t.Fatalf("expected full cmd in snippet (client truncates), got %q", s)
	}
}

func TestShellExecSnippet_EmptyCmd(t *testing.T) {
	s := snippetFor(t, Shell(), map[string]any{})
	if s != "" {
		t.Fatalf("expected empty snippet for empty cmd, got %q", s)
	}
}

func TestSearchExecSnippet(t *testing.T) {
	s := snippetFor(t, Search(), map[string]any{"pattern": "func main", "path": "."})
	if !strings.Contains(s, `pattern="func main"`) {
		t.Fatalf("expected pattern in snippet, got %q", s)
	}
}

func TestSearchExecSnippet_WithPath(t *testing.T) {
	s := snippetFor(t, Search(), map[string]any{"pattern": "TODO", "path": "src/"})
	if !strings.Contains(s, `"src/"`) {
		t.Fatalf("expected path in snippet, got %q", s)
	}
}

func TestSearchExecSnippet_TruncatesLongPattern(t *testing.T) {
	longPattern := strings.Repeat("x", 100)
	s := snippetFor(t, Search(), map[string]any{"pattern": longPattern})
	// Server no longer truncates — full pattern is sent to the client
	if !strings.Contains(s, longPattern) {
		t.Fatalf("expected full pattern in snippet (client truncates), got %q", s)
	}
}

func TestRandomTool(t *testing.T) {
	// Test with positive range
	res := runPlain(t, Random().Handler, map[string]any{"min_value": 10, "max_value": 20})
	var val int
	if _, err := fmt.Sscanf(res, "%d", &val); err != nil {
		t.Fatalf("expected integer result, got: %q", res)
	}
	if val < 10 || val > 20 {
		t.Fatalf("expected value between 10 and 20, got: %d", val)
	}

	// Test with negative range
	res2 := runPlain(t, Random().Handler, map[string]any{"min_value": -5, "max_value": 5})
	if _, err := fmt.Sscanf(res2, "%d", &val); err != nil {
		t.Fatalf("expected integer result, got: %q", res2)
	}
	if val < -5 || val > 5 {
		t.Fatalf("expected value between -5 and 5, got: %d", val)
	}

	// Test with single value
	res3 := runPlain(t, Random().Handler, map[string]any{"min_value": 42, "max_value": 42})
	if res3 != "42" {
		t.Fatalf("expected 42 for range [42,42], got: %q", res3)
	}

	// Test error: min > max
	errMsg := runErr(t, Random().Handler, map[string]any{"min_value": 10, "max_value": 5})
	if !strings.Contains(errMsg, "min_value must be <= max_value") {
		t.Fatalf("expected error about invalid range, got: %q", errMsg)
	}
}

func TestRandomToolExecSnippet(t *testing.T) {
	s := snippetFor(t, Random(), map[string]any{"min_value": 1, "max_value": 100})
	if !strings.Contains(s, "1") || !strings.Contains(s, "100") {
		t.Fatalf("expected snippet to contain range values, got: %q", s)
	}
}

func TestHTTPGetExecSnippet(t *testing.T) {
	s := snippetFor(t, HTTPGet(), map[string]any{"url": "https://example.com"})
	if !strings.Contains(s, `url="https://example.com"`) {
		t.Fatalf("expected url in snippet, got %q", s)
	}
}

func TestHTTPGetExecSnippet_Empty(t *testing.T) {
	s := snippetFor(t, HTTPGet(), map[string]any{})
	if s != "" {
		t.Fatalf("expected empty snippet for empty args, got %q", s)
	}
}

func TestHTTPGetConvertHTMLToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Hello</h1><p>World</p>"))
	}))
	defer srv.Close()
	res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL})
	if !strings.Contains(res, "Status: 200") {
		t.Fatalf("expected Status: 200 in output, got: %q", res)
	}
	// The HTML should be converted to markdown: <h1> → #, <p> → plain text
	if !strings.Contains(res, "Hello") {
		t.Fatalf("expected markdown-converted body containing 'Hello', got: %q", res)
	}
	if !strings.Contains(res, "World") {
		t.Fatalf("expected markdown-converted body containing 'World', got: %q", res)
	}
	// Should NOT contain raw HTML tags
	if strings.Contains(res, "<h1>") || strings.Contains(res, "<p>") {
		t.Fatalf("body should not contain raw HTML tags, got: %q", res)
	}
}

func TestHTTPGetTruncated(t *testing.T) {
	body := strings.Repeat("hello", 1000) // 5000 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL, "max_bytes": 100})
	if !strings.Contains(res, "Status: 200") {
		t.Fatalf("expected Status: 200 in output, got: %q", res)
	}
	if !strings.Contains(res, "[TRUNCATED:") {
		t.Fatalf("expected truncation marker in result, got: %q", res)
	}
	// Body is 5000 bytes, max_bytes=100, so omitted = 4900
	if !strings.Contains(res, "[TRUNCATED: 4900 bytes omitted]") {
		t.Fatalf("expected footer to say '4900 bytes omitted' (the omitted count), got: %q", res)
	}
}

func TestHTTPGetNonHTMLNotConverted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg": "hello"}`))
	}))
	defer srv.Close()
	res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL})
	if !strings.Contains(res, "Status: 200") {
		t.Fatalf("expected Status: 200 in output, got: %q", res)
	}
	// Non-HTML content should pass through unchanged
	if !strings.Contains(res, `{"msg": "hello"}`) {
		t.Fatalf("expected unchanged JSON body, got: %q", res)
	}
}

func TestHTTPGetNoContentTypeStillProcessed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Type header — should still detect HTML from content
		_, _ = w.Write([]byte("<h1>Title</h1>"))
	}))
	defer srv.Close()
	res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL})
	if !strings.Contains(res, "Status: 200") {
		t.Fatalf("expected Status: 200 in output, got: %q", res)
	}
	// Body should be markdown-converted (no raw <h1>)
	if strings.Contains(res, "<h1>") {
		t.Fatalf("expected markdown body when no Content-Type set, got: %q", res)
	}
}

func TestExecSnippet_AllToolsDontTruncate(t *testing.T) {
	tools := []agent.Tool{
		ReadFile(),
		ListDir(),
		WriteFile(),
		EditFile(),
		Shell(),
		Search(),
		HTTPGet(),
	}
	for _, tool := range tools {
		if tool.ExecSnippet == nil {
			continue
		}
		// Use a long path to verify server no longer truncates
		longPath := strings.Repeat("a/b/c/", 20)
		raw, _ := json.Marshal(map[string]any{"path": longPath, "pattern": longPath, "cmd": longPath, "url": "https://" + longPath, "old": longPath})
		s := tool.ExecSnippet(raw)
		// Server should not truncate — the full value should be present
		if !strings.Contains(s, longPath) && !strings.Contains(s, "https://") && !strings.Contains(s, longPath) {
			t.Errorf("%s snippet was truncated server-side — should be full: %q", tool.Schema.Name, s)
		}
	}
}

func TestRegisterAllDoesNotIncludeMetaTools(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterAll(reg)
	schemas := reg.Schemas()

	for _, s := range schemas {
		if s.Name == "context_compactify" {
			t.Fatal("RegisterAll must NOT include context_compactify — meta tools are registered via RegisterMeta to prevent schema duplication")
		}
	}
}

// TestBuildAskQuestionMessages_ExcludesCompactifyMessages verifies that
// context_compactify messages never leak into the session_ask_question LLM call.
func TestBuildAskQuestionMessages_ExcludesCompactifyMessages(t *testing.T) {
	session := agent.NewSession("testns", "You are helpful.")
	session.Append(agent.Message{Role: agent.RoleUser, Content: "hello"})
	// Add a compactify call/result pair — must be stripped.
	session.Append(agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
		{ID: "cc", Name: "context_compactify", Arguments: `{"range_start":0,"range_end":0}`},
	}})
	session.Append(agent.Message{Role: agent.RoleTool, Content: "Noted.", ToolCallID: "cc", Name: "context_compactify"})
	session.Append(agent.Message{Role: agent.RoleAssistant, Content: "hi there"})

	msgs := buildAskQuestionMessages(session, "what was said?")

	// Compactify messages should be skipped entirely.
	for _, m := range msgs {
		if m.Name == "context_compactify" {
			t.Fatalf("compactify message leaked into ask-question context: %+v", m)
		}
	}

	// The last message should be the question.
	last := msgs[len(msgs)-1]
	if last.Role != agent.RoleUser || last.Content != "what was said?" {
		t.Fatalf("expected last message to be user question, got: %+v", last)
	}
}

// TestHTTPGetConcurrentSameHost verifies that multiple concurrent http_get
// calls to the same host all succeed without timeouts or empty bodies.
// A shared http.Transport with the default DialContext causes HTTP/2
// multiplexing serialisation under concurrent requests.
func TestHTTPGetConcurrentSameHost(t *testing.T) {
	body := strings.Repeat("ABCDEFGHIJ", 10000) // 100KB body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// Run 10 concurrent truncated requests — should all succeed.
	const concurrency = 10
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := runPlain(t, HTTPGet().Handler, map[string]any{"url": srv.URL, "max_bytes": 100})
			if !strings.Contains(res, "Status: 200") {
				errs <- fmt.Errorf("expected Status: 200, got: %q", res)
				return
			}
			if !strings.Contains(res, "[TRUNCATED:") {
				errs <- fmt.Errorf("expected truncated output, got: %q", res)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// --- read_file: offset & limit support (line-based) ---

func TestReadFileWithOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.txt")
	// 3 lines: "line0", "line1", "line2"
	_ = os.WriteFile(p, []byte("line0\nline1\nline2"), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "offset": 1})
	// Reading from line 1: "line1\nline2"
	want := "line1\nline2\n"
	if res != want {
		t.Fatalf("expected %q, got: %q", want, res)
	}
}

func TestReadFileWithOffsetAndLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.txt")
	// 3 lines: "line0", "line1", "line2"
	_ = os.WriteFile(p, []byte("line0\nline1\nline2"), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "offset": 1, "limit": 1})
	// Lines 1..1 (1 line): "line1", but 1 line omitted (line2)
	want := "line1\n[TRUNCATED: 1 lines omitted — use read_file with offset=2&limit=1 to continue]"
	if res != want {
		t.Fatalf("expected %q, got: %q", want, res)
	}
}

func TestReadFileWithLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.txt")
	// 3 lines
	_ = os.WriteFile(p, []byte("a\nb\nc"), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "limit": 2})
	want := "a\nb\n[TRUNCATED: 1 lines omitted — use read_file with offset=2&limit=2 to continue]"
	if res != want {
		t.Fatalf("expected %q, got: %q", want, res)
	}
}

func TestReadFileOffsetBeyondEnd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.txt")
	_ = os.WriteFile(p, []byte("hello world"), 0o644)
	errMsg := runErr(t, ReadFile().Handler, map[string]any{"path": p, "offset": 100})
	// Offset beyond total lines: should return error
	if !strings.Contains(errMsg, "beyond end") {
		t.Fatalf("expected 'beyond end' error, got: %q", errMsg)
	}
}

func TestReadFileTruncationMessageSuggestsOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	// 100 lines
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "limit": 10})
	if !strings.Contains(res, "[TRUNCATED: 90 lines omitted") {
		t.Fatalf("expected truncation marker, got: %q", res)
	}
	// The truncation message should mention offset/limit
	if !strings.Contains(res, "offset") || !strings.Contains(res, "limit") {
		t.Fatalf("truncation message should mention offset and limit usage, got: %q", res)
	}
}

func TestReadFileOffsetWithTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	// 10 lines: "line0" through "line9"
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i)
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "offset": 5, "limit": 3})
	// Lines 5..7 (3 lines): "line5", "line6", "line7"
	if !strings.Contains(res, "line5") || !strings.Contains(res, "line7") {
		t.Fatalf("expected lines 5-7 in result, got: %q", res)
	}
	// Should say omitted: 10 lines total, offset=5, limit=3 => read 3, omitted = 10-5-3 = 2
	if !strings.Contains(res, "[TRUNCATED: 2 lines omitted") {
		t.Fatalf("expected truncation marker mentioning 2 omitted, got: %q", res)
	}
}

// --- Strict parameter validation ---

func TestReadFileStrictUnknownParam(t *testing.T) {
	errMsg := runErr(t, ReadFile().Handler, map[string]any{"path": "/tmp/x.txt", "offset_begin": 0})
	if !strings.Contains(errMsg, "unknown parameter") {
		t.Fatalf("expected 'unknown parameter' error, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "offset_begin") {
		t.Fatalf("error should mention the unknown param name, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "Tool: read_file") {
		t.Fatalf("error should include the tool usage (like show_tool output), got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "Parameters:") {
		t.Fatalf("error should include parameter list (like show_tool output), got: %q", errMsg)
	}
}

func TestReadFileStrictUnknownParamStartLine(t *testing.T) {
	errMsg := runErr(t, ReadFile().Handler, map[string]any{"path": "/tmp/x.txt", "from_line": 10})
	if !strings.Contains(errMsg, "unknown parameter") {
		t.Fatalf("expected 'unknown parameter' error, got: %q", errMsg)
	}
}

func TestReadFileValidParamsPass(t *testing.T) {
	// offset + limit should work fine
	p := filepath.Join(t.TempDir(), "ok.txt")
	_ = os.WriteFile(p, []byte("hello"), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "offset": 0, "limit": 5})
	want := "hello\n"
	if res != want {
		t.Fatalf("expected %q, got: %q", want, res)
	}
}

func TestReadFileByteTruncation(t *testing.T) {
	// max_bytes is an internal safety cap — test that it byte-truncates huge lines
	p := filepath.Join(t.TempDir(), "huge.txt")
	// Single huge line
	_ = os.WriteFile(p, []byte(strings.Repeat("x", 500)), 0o644)
	res := runPlain(t, ReadFile().Handler, map[string]any{"path": p, "max_bytes": 10})
	if !strings.Contains(res, "[TRUNCATED: output exceeded 10 bytes") {
		t.Fatalf("expected byte truncation marker, got: %q", res)
	}
}

func TestEditFileStrictUnknownParam(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(p, []byte("foo"), 0o644)
	errMsg := runErr(t, EditFile().Handler, map[string]any{"path": p, "old_string": "foo", "new_string": "bar"})
	if !strings.Contains(errMsg, "unknown parameter") {
		t.Fatalf("expected 'unknown parameter' error, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "old_string") {
		t.Fatalf("error should mention old_string, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "edit_file") {
		t.Fatalf("error should mention edit_file, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "Tool: edit_file") {
		t.Fatalf("error should include tool usage (like show_tool), got: %q", errMsg)
	}
}

func TestShellStrictUnknownParam(t *testing.T) {
	errMsg := runErr(t, Shell().Handler, map[string]any{"cmd": "echo hi", "description": "say hi"})
	if !strings.Contains(errMsg, "unknown parameter") {
		t.Fatalf("expected 'unknown parameter' error, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "description") {
		t.Fatalf("error should mention description param, got: %q", errMsg)
	}
}

// --- Required param validation ---

func TestReadFileMissingPath(t *testing.T) {
	errMsg := runErr(t, ReadFile().Handler, map[string]any{})
	if !strings.Contains(errMsg, "no such file") && !strings.Contains(errMsg, "path is required") {
		t.Fatalf("expected error about missing path, got: %q", errMsg)
	}
}

func TestRegisterMetaIncludesCompactify(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterMeta(reg)

	tool, ok := reg.Get("context_compactify")
	if !ok {
		t.Fatal("RegisterMeta must include context_compactify so Execute() can find it")
	}
	if tool.Schema.Name != "context_compactify" {
		t.Fatalf("expected schema name 'context_compactify', got %q", tool.Schema.Name)
	}
}
