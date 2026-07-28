package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// resolvePath performs the following transformations in order:
//  1. Tilde expansion...
//  2. If the path is absolute, returned as-is.
//  3. If relative and CWD is set, joined with CWD.
//  4. Otherwise returned unchanged (relative to server CWD).
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
