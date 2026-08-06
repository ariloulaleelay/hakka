package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/ariloulaleelay/hakka/agent"
)

const (
	searchMaxBytesDefault = 100_000
	searchMaxLineBytes    = 12_000
)

type searchArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxLines   int    `json:"max_lines"`
	MaxBytes   int    `json:"max_bytes"`
	IgnoreCase bool   `json:"ignore_case"`
}

// Search runs ripgrep and returns grep-like plaintext. Both the complete
// result and every individual matching line are bounded before being returned.
func Search() agent.Tool {
	description := "Search files recursively for a regex using ripgrep. Returns grep-style plaintext. Large output is saved to a tempfile."
	return NewTool("search", description).
		StringParam("pattern", "", true).
		StringParam("path", "Directory or file to search; defaults to current dir.", false).
		IntParam("max_lines", "Maximum matching lines to return (default 200).", false).
		IntParam("max_bytes", "Maximum returned output bytes (default 100000).", false).
		BoolParam("ignore_case", "", false).
		Tags("filesystem", "read", "developer", "all").
		ExecSnippet(execSnippetPatternWithPath()).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchArgs
			if err := unmarshalToolArgsStrict(raw, "search", description, &args, []paramInfo{
				{Name: "pattern", Type: "string", Required: true},
				{Name: "path", Type: "string", Description: "Directory or file to search; defaults to current dir."},
				{Name: "max_lines", Type: "integer", Description: "Maximum matching lines to return (default 200)."},
				{Name: "max_bytes", Type: "integer", Description: "Maximum returned output bytes (default 100000)."},
				{Name: "multiline", Type: "boolean"},
				{Name: "ignore_case", Type: "boolean"},
			}); err != nil {
				return "", err
			}
			if args.MaxLines <= 0 {
				args.MaxLines = 200
			}
			if args.MaxBytes <= 0 {
				args.MaxBytes = searchMaxBytesDefault
			}
			path := args.Path
			if path == "" {
				path = "."
			}
			argv := []string{"--line-number", "--column", "--no-heading", "--color", "never"}
			if args.IgnoreCase {
				argv = append(argv, "-i")
			}
			argv = append(argv, "--", args.Pattern, resolvePath(ctx, path))
			cmd := exec.CommandContext(ctx, "rg", argv...)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return "", err
			}
			if err := cmd.Start(); err != nil {
				return "", err
			}

			file, err := os.CreateTemp("", "hakka-search-*-output.log")
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return "", fmt.Errorf("search: create tempfile: %w", err)
			}
			filePath := file.Name()
			cleanup := true
			defer func() {
				_ = file.Close()
				if cleanup {
					_ = os.Remove(filePath)
				}
			}()

			reader := bufio.NewReader(stdout)
			var preview strings.Builder
			lineCount := 0
			truncated := false
			for {
				rawLine, readErr := reader.ReadString('\n')
				if len(rawLine) > 0 {
					if _, err := file.WriteString(rawLine); err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return "", fmt.Errorf("search: write tempfile: %w", err)
					}
					lineCount++
					line := strings.TrimSuffix(rawLine, "\n")
					rendered := truncateSearchLine(line, searchMaxLineBytes)
					if lineCount > args.MaxLines {
						truncated = true
					} else if !truncated {
						// Reserve space for the footer once truncation is needed.
						if preview.Len()+len(rendered)+1 > args.MaxBytes {
							truncated = true
						} else {
							preview.WriteString(rendered)
							preview.WriteByte('\n')
						}
					}
				}
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return "", readErr
					}
					break
				}
			}
			if err := file.Close(); err != nil {
				return "", fmt.Errorf("search: close tempfile: %w", err)
			}
			file = nil
			waitErr := cmd.Wait()
			if waitErr != nil {
				var exitErr *exec.ExitError
				if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
					return "", nil
				}
				return "", waitErr
			}
			if lineCount == 0 {
				return "", nil
			}
			if !truncated {
				cleanup = false
				return preview.String(), nil
			}

			footer := fmt.Sprintf("[TRUNCATED: search output exceeded limits]\n[Full results: %s]\n", filePath)
			available := args.MaxBytes - len(footer)
			if available < 0 {
				available = 0
			}
			text := preview.String()
			if len(text) > available {
				text = text[:available]
			}
			text = trimUTF8(text)
			cleanup = false
			return text + footer, nil
		}).Build()
}

func truncateSearchLine(line string, maxBytes int) string {
	if len(line) <= maxBytes {
		return line
	}
	marker := fmt.Sprintf(" [TRUNCATED: %d bytes omitted from line]", len(line)-maxBytes)
	if len(marker) >= maxBytes {
		return trimUTF8(line[:maxBytes])
	}
	return trimUTF8(line[:maxBytes-len(marker)]) + marker
}

func trimUTF8(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
