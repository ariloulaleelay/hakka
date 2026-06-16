package tools

import (
	"context"
	"path/filepath"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// resolvePath resolves a file path against the client CWD from the context.
// If the path is absolute, it is returned as-is. If it is relative and a CWD
// is set in the context, the path is joined with the CWD. Otherwise, the
// path is returned unchanged (will be relative to the server's process CWD).
func resolvePath(ctx context.Context, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	cwd := event.CWDFromContext(ctx)
	if cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}
