package tools

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

func TestResolvePathTildeExpansion(t *testing.T) {
	// Get the current user's home directory.
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	home := u.HomeDir

	tests := []struct {
		name  string
		path  string
		cwd   string
		want  func() string // computed expected path
	}{
		{
			name: "tilde alone resolves to home",
			path: "~",
			cwd:  "/tmp",
			want: func() string { return home },
		},
		{
			name: "tilde slash resolves to home",
			path: "~/",
			cwd:  "/tmp",
			want: func() string { return home },
		},
		{
			name: "tilde path resolves under home",
			path: "~/foo/bar.txt",
			cwd:  "/tmp",
			want: func() string { return filepath.Join(home, "foo/bar.txt") },
		},
		{
			name: "tilde path with CWD context ignored (absolute after expansion)",
			path: "~/some/dir",
			cwd:  "/some/other/dir",
			want: func() string { return filepath.Join(home, "some/dir") },
		},
		{
			name: "relative path still resolved against CWD",
			path: "relative/path.txt",
			cwd:  "/my/project",
			want: func() string { return "/my/project/relative/path.txt" },
		},
		{
			name: "absolute path unchanged",
			path: "/etc/hosts",
			cwd:  "/tmp",
			want: func() string { return "/etc/hosts" },
		},
		{
			name: "tildeuser not expanded (not our feature, but should not break)",
			path: "~otheruser/file",
			cwd:  "/tmp",
			want: func() string { return filepath.Join("/tmp", "~otheruser/file") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := event.ContextWithCWD(context.Background(), tt.cwd)
			got := resolvePath(ctx, tt.path)
			want := tt.want()
			if got != want {
				t.Errorf("resolvePath(ctx, %q) = %q; want %q", tt.path, got, want)
			}
		})
	}
}

// TestResolvePathTildeReadFileIntegration tests the full read_file tool with
// tilde paths to ensure the integration works end-to-end.
func TestResolvePathTildeReadFileIntegration(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	home := u.HomeDir

	// Create a temporary file in the home directory-ish location.
	// Actually create it in a temp dir and symlink? No — just test that
	// the path gets properly resolved by actually writing a file under home.
	tmpFile := filepath.Join(home, ".hakka_test_tilde_file")
	if err := os.WriteFile(tmpFile, []byte("tilde test ok"), 0o644); err != nil {
		t.Fatalf("write temp file in home: %v", err)
	}
	defer os.Remove(tmpFile)

	res := runWithCWDPlain(t, ReadFile().Handler, map[string]any{
		"path": "~/.hakka_test_tilde_file",
	}, "/tmp/whatever")

	if !strings.Contains(res, "tilde test ok") {
		t.Fatalf("expected to read file via tilde path, got: %q", res)
	}
}

// TestResolvePathTildeWriteFileIntegration tests write_file with tilde paths.
func TestResolvePathTildeWriteFileIntegration(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	home := u.HomeDir

	tmpFile := filepath.Join(home, ".hakka_test_tilde_write")
	// Clean up any leftovers from previous runs
	os.Remove(tmpFile)
	defer os.Remove(tmpFile)

	res := runWithCWDPlain(t, WriteFile(nil).Handler, map[string]any{
		"path":    "~/.hakka_test_tilde_write",
		"content": "written via tilde",
	}, "/tmp/whatever")

	if !strings.Contains(res, "Written") {
		t.Fatalf("expected success, got: %q", res)
	}

	// Verify the file was created at the right place
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "written via tilde" {
		t.Fatalf("expected content %q, got %q", "written via tilde", string(data))
	}
}
