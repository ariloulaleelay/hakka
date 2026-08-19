package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestFileStore(t *testing.T, maxUpload int64) (*FileStore, *agent.SessionManager) {
	t.Helper()
	sm := agent.NewSessionManager(agent.NewMemoryStore(), "sys")
	fs := NewFileStore(FileStoreConfig{Sessions: sm, Namespace: "ws", MaxUploadBytes: maxUpload})
	return fs, sm
}

func newFileStoreMux(fs *FileStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /session/{session_id}/file/{path...}", fs.HandleDownload)
	mux.HandleFunc("POST /session/{session_id}/file/{path...}", fs.HandleUpload)
	mux.HandleFunc("DELETE /session/{session_id}/file/{path...}", fs.HandleDelete)
	return mux
}

// createTestSession creates a session whose working directory is a fresh
// temp dir — the sandbox root for web file paths.
func createTestSession(t *testing.T, sm *agent.SessionManager) (string, string) {
	t.Helper()
	s, err := sm.Create(context.Background(), "ws")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := s.SetClientCWD(context.Background(), cwd); err != nil {
		t.Fatal(err)
	}
	return s.SessionID(), cwd
}

func writeCwdFile(t *testing.T, cwd, rel, content string) string {
	t.Helper()
	abs := filepath.Join(cwd, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

// ---------------------------------------------------------------------------
// resolveSandboxPath — confinement to the session working directory
// ---------------------------------------------------------------------------

func TestFileStore_resolveSandboxPath_Good(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)
	cwd := t.TempDir()

	tests := []struct {
		rel  string
		want string
	}{
		{"a.txt", filepath.Join(cwd, "a.txt")},
		{"docs/report.pdf", filepath.Join(cwd, "docs", "report.pdf")},
		{"a//b.txt", filepath.Join(cwd, "a", "b.txt")},
		{"a/./b.txt", filepath.Join(cwd, "a", "b.txt")},
		{".hidden", filepath.Join(cwd, ".hidden")},
	}
	for _, tt := range tests {
		got, err := fs.resolveSandboxPath(cwd, tt.rel)
		if err != nil {
			t.Fatalf("resolveSandboxPath(%q): unexpected error: %v", tt.rel, err)
		}
		if got != tt.want {
			t.Fatalf("resolveSandboxPath(%q) = %q, want %q", tt.rel, got, tt.want)
		}
	}
}

func TestFileStore_resolveSandboxPath_Traversal(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)
	cwd := t.TempDir()

	bad := []string{
		"..",
		"../evil.txt",
		"a/../../evil.txt",
		"/abs/evil.txt",
		"",
		".",
		"a\\..\\..\\evil.txt", // backslash separators must not smuggle traversal
		"a/\x00b",
		"dir/", // trailing slash — no filename
	}
	for _, rel := range bad {
		if _, err := fs.resolveSandboxPath(cwd, rel); err == nil {
			t.Fatalf("resolveSandboxPath(%q): expected error, got nil", rel)
		}
	}
}

func TestFileStore_resolveSandboxPath_DepthCap(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)
	cwd := t.TempDir()

	deep := strings.Repeat("a/", 40) + "file.txt"
	if _, err := fs.resolveSandboxPath(cwd, deep); err == nil {
		t.Fatal("expected error for overly deep path")
	}
}

func TestFileStore_resolveSandboxPath_EmptyCWDFallsBackToServerCWD(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)

	got, err := fs.resolveSandboxPath("", "a.txt")
	if err != nil {
		t.Fatalf("resolveSandboxPath with empty cwd: %v", err)
	}
	serverCWD, _ := os.Getwd()
	want := filepath.Join(serverCWD, "a.txt")
	if got != want {
		t.Fatalf("resolveSandboxPath = %q, want %q", got, want)
	}
}

func TestFileStore_getSession_RejectsBadIDs(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	createTestSession(t, sm)

	bad := []string{"", "..", "a/b", "../s1", ".", "s1\x00x"}
	for _, sid := range bad {
		if _, ok := fs.getSession(context.Background(), sid); ok {
			t.Fatalf("getSession(%q): expected not ok", sid)
		}
	}
}

// ---------------------------------------------------------------------------
// Download
// ---------------------------------------------------------------------------

func TestFileStore_DownloadServesFile(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "hello.txt", "hello world\n")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/hello.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello world\n" {
		t.Fatalf("body = %q, want %q", got, "hello world\n")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain prefix", ct)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "inline") {
		t.Fatalf("Content-Disposition = %q, want inline", disp)
	}
}

func TestFileStore_DownloadNestedPath(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "docs/report.txt", "nested")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/docs/report.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "nested" {
		t.Fatalf("body = %q, want %q", got, "nested")
	}
}

func TestFileStore_DownloadAttachment(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "hello.txt", "hello")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/hello.txt?download=1", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "attachment") {
		t.Fatalf("Content-Disposition = %q, want attachment", disp)
	}
}

func TestFileStore_DownloadMissingFile(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, _ := createTestSession(t, sm)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/missing.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestFileStore_DownloadUnknownSession(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/doesnotexist/file/hello.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestFileStore_DownloadDirectory(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	dir := filepath.Join(cwd, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/docs", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for directory, got %d", rec.Code)
	}
}

func TestFileStore_DownloadRange(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "hello.txt", "hello world\n")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/hello.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected 206, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello" {
		t.Fatalf("range body = %q, want %q", got, "hello")
	}
}

// ---------------------------------------------------------------------------
// Upload
// ---------------------------------------------------------------------------

func TestFileStore_UploadSavesFile(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/"+sid+"/file/docs/report.txt", strings.NewReader("uploaded bytes"))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// File must exist inside the session CWD.
	abs := filepath.Join(cwd, "docs", "report.txt")
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("uploaded file not found at %s: %v", abs, err)
	}
	if string(data) != "uploaded bytes" {
		t.Fatalf("file content = %q, want %q", string(data), "uploaded bytes")
	}

	// JSON response with url, path, bytes.
	var resp struct {
		URL   string `json:"url"`
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body %q)", err, rec.Body.String())
	}
	if resp.URL != "/session/"+sid+"/file/docs/report.txt" {
		t.Fatalf("url = %q, want %q", resp.URL, "/session/"+sid+"/file/docs/report.txt")
	}
	if resp.Path != abs {
		t.Fatalf("path = %q, want %q", resp.Path, abs)
	}
	if resp.Bytes != int64(len("uploaded bytes")) {
		t.Fatalf("bytes = %d, want %d", resp.Bytes, len("uploaded bytes"))
	}
}

func TestFileStore_UploadOverwrites(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "a.txt", "old")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/"+sid+"/file/a.txt", strings.NewReader("new"))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(cwd, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("file content = %q, want %q", string(data), "new")
	}
}

func TestFileStore_UploadUnknownSession(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/doesnotexist/file/a.txt", strings.NewReader("data"))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestFileStore_UploadTooLarge(t *testing.T) {
	fs, sm := newTestFileStore(t, 4) // 4-byte cap
	sid, cwd := createTestSession(t, sm)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/"+sid+"/file/big.bin", strings.NewReader("12345"))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	// No leftover partial file.
	if _, err := os.Stat(filepath.Join(cwd, "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected partial file to be removed, err=%v", err)
	}
}

func TestFileStore_UploadTraversalNotWritten(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	outside := filepath.Join(filepath.Dir(cwd), "evil.txt")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	// The mux may sanitize and redirect (301/307); either way the handler
	// must never write outside the working directory.
	req := httptest.NewRequest(http.MethodPost, "/session/"+sid+"/file/../../evil.txt", strings.NewReader("data"))
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected traversal upload to fail, got 200")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal upload wrote outside the working directory: err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestFileStore_DeleteRemovesFile(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	abs := writeCwdFile(t, cwd, "gone.txt", "bye")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/session/"+sid+"/file/gone.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, err=%v", err)
	}

	// Second delete → 404.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodDelete, "/session/"+sid+"/file/gone.txt", nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", rec2.Code)
	}
}

func TestFileStore_DeleteUnknownSession(t *testing.T) {
	fs, _ := newTestFileStore(t, 0)

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/session/doesnotexist/file/a.txt", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestFileStore_DeleteDirectory(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	dir := filepath.Join(cwd, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/session/"+sid+"/file/docs", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for directory delete, got %d", rec.Code)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory should not be removed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WebFilesProvider — agent system prompt integration
// ---------------------------------------------------------------------------

func TestWebFilesProvider_SystemMessages(t *testing.T) {
	cwd := t.TempDir()
	s := agent.NewSession("ws", "sys")
	if err := s.SetClientCWD(context.Background(), cwd); err != nil {
		t.Fatal(err)
	}

	msgs := (WebFilesProvider{}).SystemMessages(s)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Role != agent.RoleSystem {
		t.Fatalf("expected system role, got %v", m.Role)
	}
	if !strings.Contains(m.Content, cwd) {
		t.Fatalf("message must mention the session CWD %q: %q", cwd, m.Content)
	}
	if !strings.Contains(m.Content, "/session/"+s.SessionID()+"/file/") {
		t.Fatalf("message must contain the web link pattern: %q", m.Content)
	}
}

// ---------------------------------------------------------------------------
// Gateway integration — routes registered only when a store is configured
// ---------------------------------------------------------------------------

func newTestGateway(t *testing.T) (*Gateway, *agent.SessionManager) {
	t.Helper()
	sm := agent.NewSessionManager(agent.NewMemoryStore(), "sys")
	tools := agent.NewToolRegistry()
	conv := agent.NewConversation(sm, nil, tools, "ws", agent.DefaultEngineConfig())
	cmd := commands.New(sm, conv, "sys", "ws")
	gw := New("", conv, cmd)
	return gw, sm
}

func TestGateway_FileRoutesWhenStoreConfigured(t *testing.T) {
	gw, sm := newTestGateway(t)
	gw.SetFileStore(FileStoreConfig{})

	sid, _ := createTestSession(t, sm)

	mux := gw.buildMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/"+sid+"/file/a.txt", strings.NewReader("data"))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload via gateway mux: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/a.txt", nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("download via gateway mux: expected 200, got %d", rec2.Code)
	}
	if got := rec2.Body.String(); got != "data" {
		t.Fatalf("downloaded body = %q, want %q", got, "data")
	}
}

func TestGateway_FileRoutesAbsentWithoutStore(t *testing.T) {
	gw, sm := newTestGateway(t)
	sid, _ := createTestSession(t, sm)

	mux := gw.buildMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/session/"+sid+"/file/a.txt", nil)
	mux.ServeHTTP(rec, req)

	// No file store → the request falls through to the SPA handler,
	// which has no such file → 404. A configured store with an existing
	// file would return 200 (see TestGateway_FileRoutesWhenStoreConfigured).
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 without file store, got 200")
	}
}

func TestFileStore_HandlersRejectUnknownMethod(t *testing.T) {
	fs, sm := newTestFileStore(t, 0)
	sid, cwd := createTestSession(t, sm)
	writeCwdFile(t, cwd, "a.txt", "data")

	mux := newFileStoreMux(fs)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/session/"+sid+"/file/a.txt", strings.NewReader("data"))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
