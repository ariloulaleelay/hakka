package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// Tool argument types.
type (
	pathArgs struct {
		Path string `json:"path"`
	}
	readFileArgs struct {
		Path     string `json:"path"`
		MaxBytes int    `json:"max_bytes"`
	}
	writeFileArgs struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	editFileArgs struct {
		Path       string `json:"path"`
		Old        string `json:"old"`
		New        string `json:"new"`
		ReplaceAll bool   `json:"replace_all"`
	}
)

// unmarshalToolArgs unmarshals raw JSON into the provided args pointer,
// prefixing any error with the tool name.
func unmarshalToolArgs(raw json.RawMessage, toolName string, args any) error {
	if err := json.Unmarshal(raw, args); err != nil {
		return fmt.Errorf("%s: %w", toolName, err)
	}
	return nil
}

// ReadFile reads a UTF-8 file and returns its content (optionally truncated).
func ReadFile() agent.Tool {
	return NewTool("read_file", "Read a UTF-8 text file from disk and return its contents.").
		StringParam("path", "Absolute or relative path", true).
		IntParam("max_bytes", "Optional max bytes to return (default 200_000)", false).
		Tags("filesystem", "read", "developer", "all").
		ExecSnippetField("path").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args readFileArgs
			if err := unmarshalToolArgs(raw, "read_file", &args); err != nil {
				return "", err
			}
			if args.MaxBytes <= 0 {
				args.MaxBytes = 200_000
			}
			resolved := resolvePath(ctx, args.Path)
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			truncated := len(data) > args.MaxBytes
			if truncated {
				data = data[:args.MaxBytes]
			}
			var b strings.Builder
			if truncated {
				fileInfo, _ := os.Stat(resolved)
				omitted := 0
				if fileInfo != nil {
					omitted = int(fileInfo.Size()) - args.MaxBytes
				}
				b.WriteString(fmt.Sprintf("%s (omitted %d bytes)\n---\n", resolved, omitted))
			} else {
				b.WriteString(fmt.Sprintf("%s\n---\n", resolved))
			}
			b.Write(data)
			b.WriteByte('\n')
			if truncated {
				b.WriteString(fmt.Sprintf("[TRUNCATED: %d bytes omitted]", len(data)))
			}
			return b.String(), nil
		}).
		Build()
}

// ListDir returns directory entries as JSON.
func ListDir() agent.Tool {
	return NewTool("list_dir", "List the immediate entries of a directory.").
		StringParam("path", "", true).
		Tags("filesystem", "read", "developer").
		ExecSnippetField("path").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args pathArgs
			if err := unmarshalToolArgs(raw, "list_dir", &args); err != nil {
				return "", err
			}
			resolved := resolvePath(ctx, args.Path)
			dirEntries, err := os.ReadDir(resolved)
			if err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			sort.Slice(dirEntries, func(i, j int) bool {
				return dirEntries[i].Name() < dirEntries[j].Name()
			})
			var b strings.Builder
			b.WriteString(fmt.Sprintf("Entries in %s:\n", resolved))
			for _, dirEntry := range dirEntries {
				name := dirEntry.Name()
				if dirEntry.IsDir() {
					b.WriteString(fmt.Sprintf("  %s/\t(dir)\n", name))
				} else {
					info, _ := dirEntry.Info()
					if info != nil {
						b.WriteString(fmt.Sprintf("  %s\t%d bytes\n", name, info.Size()))
					} else {
						b.WriteString(fmt.Sprintf("  %s\n", name))
					}
				}
			}
			return b.String(), nil
		}).
		Build()
}

// WriteFile writes content to a file (creates dirs as needed).
func WriteFile() agent.Tool {
	return NewTool("write_file", "Create or overwrite a file with the given content. Creates parent dirs.").
		StringParam("path", "", true).
		StringParam("content", "", true).
		Tags("filesystem", "write", "developer", "all").
		ExecSnippetField("path").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args writeFileArgs
			if err := unmarshalToolArgs(raw, "write_file", &args); err != nil {
				return "", err
			}
			resolved := resolvePath(ctx, args.Path)
			if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := os.WriteFile(resolved, []byte(args.Content), 0o644); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			byteCount := len(args.Content)
			if byteCount == 0 {
				return fmt.Sprintf("Written 0 bytes to %s (empty file)", resolved), nil
			}
			return fmt.Sprintf("Written %d bytes to %s", byteCount, resolved), nil
		}).
		Build()
}

// EditFile applies a single literal-string replacement to a file.
func EditFile() agent.Tool {
	return NewTool("edit_file", "Replace the first occurrence of `old` with `new` in the given file. Set replace_all=true to replace every occurrence.").
		StringParam("path", "", true).
		StringParam("old", "", true).
		StringParam("new", "", true).
		BoolParam("replace_all", "", false).
		Tags("filesystem", "write", "developer").
		ExecSnippet(func(args json.RawMessage) string {
			var params editFileArgs
			if err := json.Unmarshal(args, &params); err != nil {
				return ""
			}
			return `"` + params.Path + `" old="` + params.Old + `"`
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args editFileArgs
			if err := unmarshalToolArgs(raw, "edit_file", &args); err != nil {
				return "", err
			}
			resolved := resolvePath(ctx, args.Path)
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			src := string(data)
			replaceCount := strings.Count(src, args.Old)
			if replaceCount == 0 {
				return "", fmt.Errorf("pattern not found in %s", args.Path)
			}
			var out string
			if args.ReplaceAll {
				out = strings.ReplaceAll(src, args.Old, args.New)
			} else {
				out = strings.Replace(src, args.Old, args.New, 1)
				replaceCount = 1
			}
			if err := os.WriteFile(resolved, []byte(out), 0o644); err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			if args.ReplaceAll {
				return fmt.Sprintf("Replaced %d occurrence(s) in %s (replace_all)", replaceCount, resolved), nil
			}
			return fmt.Sprintf("Replaced %d occurrence(s) in %s", replaceCount, resolved), nil
		}).
		Build()
}
