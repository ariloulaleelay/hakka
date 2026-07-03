package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// resolvePath resolves a file path against the client CWD from the context.
// It performs the following transformations in order:
//
//  1. Tilde expansion: if the path starts with "~" (either bare "~" or
//     "~/..."), the tilde is replaced with the current user's home directory.
//     Paths starting with "~username" are NOT expanded (left as-is).
//  2. If the resulting path is absolute, it is returned as-is.
//  3. If it is relative and a CWD is set in the context, it is joined with the CWD.
//  4. Otherwise, the path is returned unchanged (will be relative to the
//     server's process CWD).
func resolvePath(ctx context.Context, path string) string {
	// Step 1: expand ~ (tilde) to the user's home directory.
	if strings.HasPrefix(path, "~") {
		if len(path) == 1 || path[1] == '/' {
			home, err := os.UserHomeDir()
			if err == nil {
				if len(path) == 1 {
					return home
				}
				return filepath.Join(home, path[2:])
			}
			// If we can't determine the home directory, fall through
			// and let the OS handle it (will likely produce an error).
		}
		// ~username paths: leave as-is, they're not our feature.
	}

	if filepath.IsAbs(path) {
		return path
	}
	cwd := event.CWDFromContext(ctx)
	if cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}
