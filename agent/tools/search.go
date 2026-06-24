package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

type searchArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxLines   int    `json:"max_lines"`
	IgnoreCase bool   `json:"ignore_case"`
}

// Search runs ripgrep (rg) over a directory tree. Falls back to an error if
// rg is not installed.
func Search() agent.Tool {
	return NewTool("search", "Search files recursively for a regex using ripgrep. Returns matching lines with file:line:col prefixes.").
		StringParam("pattern", "", true).
		StringParam("path", "Directory or file to search; defaults to current dir.", false).
		IntParam("max_lines", "Maximum matching lines to return (default 200).", false).
		BoolParam("ignore_case", "", false).
		Tags("filesystem", "read", "developer", "all").
		ExecSnippet(execSnippetPatternWithPath()).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchArgs
			if err := unmarshalToolArgs(raw, "search", &args); err != nil {
				return "", err
			}
			if args.MaxLines <= 0 {
				args.MaxLines = 200
			}
			path := args.Path
			if path == "" {
				path = "."
			}
			resolved := resolvePath(ctx, path)
			argv := []string{"--line-number", "--column", "--no-heading", "--color", "never"}
			if args.IgnoreCase {
				argv = append(argv, "-i")
			}
			argv = append(argv, "--", args.Pattern, resolved)
			cmd := exec.CommandContext(ctx, "rg", argv...)
			out, err := cmd.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
					// rg exits 1 when no matches
					return `{"matches": [], "truncated": false}`, nil
				}
				return "", err
			}
			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			truncated := false
			if len(lines) > args.MaxLines {
				lines = lines[:args.MaxLines]
				truncated = true
			}
			res, err := json.Marshal(map[string]any{
				"matches":   lines,
				"truncated": truncated,
			})
			if err != nil {
				return "", err
			}
			return string(res), nil
		}).
		Build()
}
