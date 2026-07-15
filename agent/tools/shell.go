package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// shellInlineLimit is the per-stream byte threshold below which captured
// output is returned inline in the tool result. Beyond it, the stream is
// only referenced by its tempfile path so the agent can read or grep it.
const shellInlineLimit = 5000

// Shell runs a shell command via `sh -c`. Stdout and stderr are always
// streamed into tempfiles under the OS temp directory ($TMPDIR / $TMP /
// /tmp depending on platform). Short outputs are inlined verbatim; large
// outputs are summarised with their file path so the agent can fetch only
// the parts it needs via read_file / search / shell.
//
// Default timeout is 30s; override with the `timeout_seconds` arg.
func Shell() agent.Tool {
	return NewTool("shell",
		"Execute a shell command via `sh -c`.\n"+
			"Stdout/stderr are written to tempfiles;\n"+
			"short output is returned inline, otherwise the file path is reported so the agent can read or grep it.\n"+
			"Project cwd applied by default").
		StringParam("cmd", "", true).
		StringParam("cwd", "Working directory (optional).", false).
		IntParam("timeout_seconds", "Default 30.", false).
		Tags("exec", "dangerous", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params struct {
				Cmd string `json:"cmd"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.Cmd == "" {
				return ""
			}
			return `"` + params.Cmd + `"`
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Cmd     string `json:"cmd"`
				Cwd     string `json:"cwd"`
				Timeout int    `json:"timeout_seconds"`
			}
			if err := unmarshalToolArgsStrict(raw, "shell",
				"Execute a shell command via `sh -c`.\n"+
					"Stdout/stderr are written to tempfiles;\n"+
					"short output is returned inline, otherwise the file path is reported so the agent can read or grep it.\n"+
					"Project cwd applied by default",
				&args, []paramInfo{
					{Name: "cmd", Type: "string", Description: "", Required: true},
					{Name: "cwd", Type: "string", Description: "Working directory (optional)."},
					{Name: "timeout_seconds", Type: "integer", Description: "Default 30."},
				}); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Cmd) == "" {
				return "", fmt.Errorf("shell: cmd is required")
			}
			if args.Timeout <= 0 {
				args.Timeout = 30
			}

			outFile, err := os.CreateTemp("", "hakka-shell-*-stdout.log")
			if err != nil {
				return "", fmt.Errorf("shell: create stdout tempfile: %w", err)
			}
			errFile, err := os.CreateTemp("", "hakka-shell-*-stderr.log")
			if err != nil {
				_ = outFile.Close()
				_ = os.Remove(outFile.Name())
				return "", fmt.Errorf("shell: create stderr tempfile: %w", err)
			}

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "sh", "-c", args.Cmd)
			// On Unix, set up process-group isolation so timeout kills
			// all child processes, not just the immediate sh.
			setProcessGroupKill(cmd)
			workDir := args.Cwd
			if workDir == "" {
				workDir = event.CWDFromContext(ctx)
			}
			if workDir != "" {
				cmd.Dir = workDir
			}
			cmd.Stdout = outFile
			cmd.Stderr = errFile

			runErr := cmd.Run()
			_ = outFile.Close()
			_ = errFile.Close()

			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errors.As(runErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}
			timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)

			result := buildShellResult(exitCode, timedOut, outFile.Name(), errFile.Name())
			b, _ := json.Marshal(result)
			return string(b), nil
		}).
		Build()
}

// buildShellResult builds a structured JSON result for the shell tool.
func buildShellResult(exitCode int, timedOut bool, stdoutPath, stderrPath string) map[string]any {
	result := map[string]any{
		"exit_code": exitCode,
		"timed_out": timedOut,
	}
	result["stdout"] = renderStreamResult(stdoutPath)
	result["stderr"] = renderStreamResult(stderrPath)
	return result
}

// renderStreamResult reads a tempfile and returns either the inline content
// or a reference to the file on disk for the agent to read/grep later.
//
// Design rationale: small outputs (≤ shellInlineLimit bytes) are inlined
// directly into the tool result to keep the conversation compact. Large
// outputs are intentionally left on disk — the tool result returns the
// file path so the agent can read, search, or grep only the relevant
// portions via read_file / search / shell. This is a context-bloat
// prevention mechanism: dumping hundreds of kilobytes of shell output
// into the conversation would burn token budget on irrelevant data.
// The agent (LLM) is expected to clean up the tempfiles after use via
// shell("rm ...") when it no longer needs them.
func renderStreamResult(path string) any {
	info, err := os.Stat(path)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	size := info.Size()
	if size == 0 {
		_ = os.Remove(path)
		return ""
	}
	if size <= shellInlineLimit {
		data, err := os.ReadFile(path)
		if err != nil {
			return map[string]any{"error": err.Error(), "path": path}
		}
		_ = os.Remove(path)
		return string(data)
	}
	return map[string]any{
		"truncated": true,
		"bytes":     int(size),
		"path":      path,
		"message":   "output too large, use read_file or search to inspect",
	}
}
