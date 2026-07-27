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
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
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

// paramInfo describes a tool parameter for error formatting.
type paramInfo struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// formatToolUsage builds a show_tool-like formatted usage string for a tool.
// This is reused by both unmarshalToolArgsStrict (error messages) and ShowTool.
func formatToolUsage(name, description string, params []paramInfo) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tool: %s\n", name))
	b.WriteString(fmt.Sprintf("Description: %s\n", description))

	if len(params) > 0 {
		b.WriteString("\nParameters:\n")
		// Find longest name for alignment
		maxNameLen := 0
		for _, p := range params {
			if len(p.Name) > maxNameLen {
				maxNameLen = len(p.Name)
			}
		}

		for _, p := range params {
			optional := "optional"
			if p.Required {
				optional = "required"
			}
			line := fmt.Sprintf("  %-*s  (%s, %s)", maxNameLen, p.Name, p.Type, optional)
			if p.Description != "" {
				line += " — " + p.Description
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// unmarshalToolArgsStrict unmarshals raw JSON into args, but first validates
// that every key in the JSON matches one of the tool's declared parameters.
// If an unknown key is found, it returns a descriptive error that includes
// the full tool usage info (like show_tool output).
func unmarshalToolArgsStrict(raw json.RawMessage, toolName, description string, args any, params []paramInfo) error {
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("%s: %w", toolName, err)
	}

	allowed := make(map[string]bool, len(params))
	for _, p := range params {
		allowed[p.Name] = true
	}

	var unknown []string
	for key := range rawMap {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}

	if len(unknown) > 0 {
		plural := "parameter"
		if len(unknown) > 1 {
			plural = "parameters"
		}
		usage := formatToolUsage(toolName, description, params)
		return fmt.Errorf("%s: unknown %s: %s\n\n%s",
			toolName, plural, strings.Join(unknown, ", "), usage)
	}

	return unmarshalToolArgs(raw, toolName, args)
}

// ReadFile reads a UTF-8 file and returns its content (optionally truncated).
// Supports offset+limit for windowed reading of large files using line numbers.
func ReadFile() agent.Tool {
	return NewTool("read_file", "Read a UTF-8 text file from disk and return its contents. "+
		"For large files, use offset+limit to read specific line ranges/windows. "+
		"If truncated, use a larger limit or offset to continue reading.").
		StringParam("path", "Absolute or relative path", true).
		IntParam("offset", "Line number to start reading from (0-based, default 0). Use with limit to read a window.", false).
		IntParam("limit", "Max lines to read (default 200). Use with offset to read a window.", false).
		Tags("filesystem", "read", "developer", "all").
		ExecSnippetField("path").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args readFileArgs
			if err := unmarshalToolArgsStrict(raw, "read_file",
				"Read a UTF-8 text file from disk and return its contents. "+
					"For large files, use offset+limit to read specific line ranges/windows. "+
					"If truncated, use a larger limit or offset to continue reading.",
				&args, []paramInfo{
					{Name: "path", Type: "string", Description: "Absolute or relative path", Required: true},
					{Name: "offset", Type: "integer", Description: "Line number to start reading from (0-based, default 0). Use with limit to read a window."},
					{Name: "limit", Type: "integer", Description: "Max lines to read (default 200). Use with offset to read a window."},
					{Name: "max_bytes", Type: "integer", Description: "(internal) Max output bytes before byte-level truncation (default 200000)."},
				}); err != nil {
				return "", err
			}
			limit := args.Limit
			if limit <= 0 {
				limit = 200
			}
			offset := args.Offset
			if offset < 0 {
				offset = 0
			}
			maxBytes := args.MaxBytes
			if maxBytes <= 0 {
				maxBytes = 200_000
			}

			resolved := resolvePath(ctx, args.Path)
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}

			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)

			// Clamp offset
			if offset > totalLines {
				offset = totalLines
			}
			readEnd := offset + limit
			if readEnd > totalLines {
				readEnd = totalLines
			}
			lineTruncated := readEnd < totalLines

			// Edge case: offset beyond file — return error
			if offset >= totalLines {
				return "", fmt.Errorf("read_file: offset %d beyond end (file has %d lines)", args.Offset, totalLines)
			}

			selected := lines[offset:readEnd]
			content := strings.Join(selected, "\n")

			// Byte-level safety cap: if the selected content is huge
			// (e.g. a single giant JSON line), truncate at byte level.
			byteTruncated := false
			if len(content) > maxBytes {
				content = content[:maxBytes]
				byteTruncated = true
			}

			var b strings.Builder
			omitted := totalLines - readEnd
			b.WriteString(content)
			if len(content) > 0 && content[len(content)-1] != '\n' {
				b.WriteByte('\n')
			}
			if byteTruncated {
				b.WriteString(fmt.Sprintf("[TRUNCATED: output exceeded %d bytes — use shell with head/tail/sed to extract specific portions]",
					maxBytes))
			} else if lineTruncated {
				b.WriteString(fmt.Sprintf("[TRUNCATED: %d lines omitted — use read_file with offset=%d&limit=%d to continue]",
					omitted, readEnd, limit))
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
			if err := unmarshalToolArgsStrict(raw, "list_dir",
				"List the immediate entries of a directory.",
				&args, []paramInfo{
					{Name: "path", Type: "string", Description: "", Required: true},
				}); err != nil {
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
			if err := unmarshalToolArgsStrict(raw, "write_file",
				"Create or overwrite a file with the given content. Creates parent dirs.",
				&args, []paramInfo{
					{Name: "path", Type: "string", Description: "", Required: true},
					{Name: "content", Type: "string", Description: "", Required: true},
				}); err != nil {
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
				return fmt.Sprintf("Written 0 bytes (empty file)"), nil
			}
			return fmt.Sprintf("Written %d bytes", byteCount), nil
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
		Tags("filesystem", "write", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params editFileArgs
			if err := json.Unmarshal(args, &params); err != nil {
				return ""
			}
			return `"` + params.Path + `" old="` + params.Old + `"`
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args editFileArgs
			if err := unmarshalToolArgsStrict(raw, "edit_file",
				"Replace the first occurrence of `old` with `new` in the given file. Set replace_all=true to replace every occurrence.",
				&args, []paramInfo{
					{Name: "path", Type: "string", Description: "", Required: true},
					{Name: "old", Type: "string", Description: "", Required: true},
					{Name: "new", Type: "string", Description: "", Required: true},
					{Name: "replace_all", Type: "boolean", Description: ""},
				}); err != nil {
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
				return fmt.Sprintf("Replaced %d occurrence(s) (replace_all)", replaceCount), nil
			}
			return fmt.Sprintf("Replaced %d occurrence(s)", replaceCount), nil
		}).
		Build()
}



