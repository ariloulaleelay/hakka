package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// defaultMaxUploadBytes caps a single upload (64 MiB).
const defaultMaxUploadBytes = 64 << 20

// FileStoreConfig configures the per-session web file store.
type FileStoreConfig struct {
	// MaxUploadBytes caps a single upload; 0 = defaultMaxUploadBytes.
	MaxUploadBytes int64
	// Sessions validates that a session exists before any file operation
	// and provides the session's working directory (the sandbox root).
	// Defaults to the gateway conversation's session manager.
	Sessions *agent.SessionManager
	// Namespace used for session lookups. Defaults to the gateway namespace.
	Namespace string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// FileStore serves the per-session web file endpoints:
//
//	GET    /session/{session_id}/file/{path...}  — download
//	POST   /session/{session_id}/file/{path...}  — upload (raw body)
//	DELETE /session/{session_id}/file/{path...}  — delete
//
// Web file paths are relative to the session's working directory (CWD) —
// the same root the agent's tools operate in. All client-controlled
// reads and writes are confined to that directory: traversal attempts
// ("..", leading "/", backslash tricks) are rejected, so the rest of the
// filesystem is unreachable through these handlers. (Symlinks planted
// inside the CWD by the agent itself are followed; the sandbox trusts
// the agent, not the client.)
type FileStore struct {
	maxUploadBytes int64
	sessions       *agent.SessionManager
	namespace      string
	logger         *slog.Logger
}

// NewFileStore builds a FileStore from config.
func NewFileStore(cfg FileStoreConfig) *FileStore {
	maxBytes := cfg.MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &FileStore{
		maxUploadBytes: maxBytes,
		sessions:       cfg.Sessions,
		namespace:      cfg.Namespace,
		logger:         logger,
	}
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxPathDepth caps the number of path segments a request may address.
const maxPathDepth = 32

// getSession resolves the request's session ID to a live session.
// Returns (nil, false) when the ID is malformed or the session does not
// exist (or no session manager is configured).
func (fs *FileStore) getSession(ctx context.Context, sid string) (*agent.Session, bool) {
	if fs.sessions == nil || !sessionIDRe.MatchString(sid) {
		return nil, false
	}
	s, err := fs.sessions.Get(ctx, fs.namespace, sid)
	if err != nil {
		return nil, false
	}
	return s, true
}

// resolveSandboxPath validates the relative path and returns the absolute
// filesystem path inside the session's working directory. Any traversal
// attempt ("..", leading "/", backslash tricks, NUL bytes, trailing
// slash, excessive depth) is rejected.
func (fs *FileStore) resolveSandboxPath(cwd, rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("invalid path")
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("invalid path") // absolute paths escape the sandbox
	}
	if rel == "" || rel == "." || strings.HasSuffix(rel, "/") {
		return "", fmt.Errorf("invalid path")
	}
	clean := path.Clean(rel)
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid path")
	}
	segs := strings.Split(clean, "/")
	if len(segs) > maxPathDepth {
		return "", fmt.Errorf("invalid path")
	}
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path")
		}
	}

	base := cwd
	if base == "" {
		base, _ = os.Getwd()
	}
	if !filepath.IsAbs(base) {
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	abs := filepath.Join(base, filepath.FromSlash(clean))
	// Belt and braces: never resolve outside the working directory.
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path")
	}
	return abs, nil
}

// webFileURL builds the client-facing web link for a relative path,
// escaping each segment independently.
func webFileURL(sid, rel string) string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	clean := path.Clean("/" + rel)
	segs := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	esc := make([]string, len(segs))
	for i, s := range segs {
		esc[i] = url.PathEscape(s)
	}
	return "/session/" + sid + "/file/" + strings.Join(esc, "/")
}

func contentTypeFor(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// HandleDownload serves a file from the session's working directory.
// Inline by default (images render in chat); ?download=1 forces
// attachment. Range requests are supported via http.ServeContent.
func (fs *FileStore) HandleDownload(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	rel := r.PathValue("path")
	session, ok := fs.getSession(r.Context(), sid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := fs.resolveSandboxPath(session.GetCWD(), rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", contentTypeFor(abs))
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filepath.Base(abs)}))
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

// HandleUpload saves the raw request body to the session's working
// directory. Parent directories are created automatically; overwriting
// is allowed. Oversized uploads are rejected with 413 and the partial
// file removed.
func (fs *FileStore) HandleUpload(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	rel := r.PathValue("path")
	session, ok := fs.getSession(r.Context(), sid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := fs.resolveSandboxPath(session.GetCWD(), rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, fs.maxUploadBytes)

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		fs.logger.Error("web upload: mkdir failed", "path", filepath.Dir(abs), "err", err)
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fs.logger.Error("web upload: create failed", "path", abs, "err", err)
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	n, copyErr := io.Copy(f, r.Body)
	if closeErr := f.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(abs) // drop the partial file
		var mbe *http.MaxBytesError
		if errors.As(copyErr, &mbe) {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		fs.logger.Error("web upload: write failed", "path", abs, "err", copyErr)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	fs.logger.Info("web upload", "session", sid, "path", abs, "bytes", n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url":   webFileURL(sid, rel),
		"path":  abs,
		"bytes": n,
	})
}

// HandleDelete removes a file from the session's working directory.
// Directories are rejected; unknown files return 404.
func (fs *FileStore) HandleDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	rel := r.PathValue("path")
	session, ok := fs.getSession(r.Context(), sid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := fs.resolveSandboxPath(session.GetCWD(), rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		fs.logger.Error("web delete: stat failed", "path", abs, "err", err)
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "only files can be deleted", http.StatusBadRequest)
		return
	}
	if err := os.Remove(abs); err != nil {
		fs.logger.Error("web delete: remove failed", "path", abs, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	fs.logger.Info("web delete", "session", sid, "path", abs)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url":  webFileURL(sid, rel),
		"path": abs,
	})
}

// WebFilesProvider implements agent.SystemMessageProvider, telling the
// LLM how the web file sharing maps onto its working directory and how
// the user sees the files.
type WebFilesProvider struct{}

func (WebFilesProvider) SystemMessages(session agent.SessionView) []agent.Message {
	sid := session.SessionID()
	cwd := session.GetCWD()
	return []agent.Message{{
		Role: agent.RoleSystem,
		Content: fmt.Sprintf(
			"## Web file sharing\n\n"+
				"Web file paths are relative to your working directory (%s): the web path /session/%s/file/<rel> maps to %s/<rel>.\n\n"+
				"The user can upload files to you through the web UI; uploads are saved into your working directory. Read them with read_file.\n\n"+
				"To give files to the user (images, documents, generated results), write them into your working directory with write_file and reference them in your reply as /session/%s/file/<rel>. The user sees and opens these links in the web UI.",
			cwd, sid, cwd, sid,
		),
	}}
}
