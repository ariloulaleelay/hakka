package tools

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

// ToolBuilder provides a fluent, declarative API for constructing agent.Tool
// values. It replaces the repetitive inline map[string]any schema definitions
// with chainable methods, collapsing tool constructors from ~60 lines to ~15.
//
// Usage:
//
//	NewTool("read_file", "Read a UTF-8 text file...").
//	    StringParam("path", "Absolute or relative path", true).
//	    IntParam("offset", "Line number to start reading from (0-based, default 0).", false).
//	    Tags("filesystem", "read", "developer", "all").
//	    ExecSnippetField("path").
//	    Handler(func(ctx context.Context, raw json.RawMessage) (string, error) { ... }).
//	    Build()
type ToolBuilder struct {
	name        string
	description string
	params      []paramDef
	required    []string
	tags        []string
	timeout     time.Duration
	execSnippet agent.ExecSnippetFunc
	handler     agent.ToolHandler
}

type paramDef struct {
	Name        string
	Type        string // "string", "integer", "boolean", "object"
	Description string
}

func NewTool(name, description string) *ToolBuilder {
	return &ToolBuilder{name: name, description: description}
}

func (b *ToolBuilder) StringParam(name, desc string, required bool) *ToolBuilder {
	b.params = append(b.params, paramDef{Name: name, Type: "string", Description: desc})
	if required {
		b.required = append(b.required, name)
	}
	return b
}

func (b *ToolBuilder) IntParam(name, desc string, required bool) *ToolBuilder {
	b.params = append(b.params, paramDef{Name: name, Type: "integer", Description: desc})
	if required {
		b.required = append(b.required, name)
	}
	return b
}

func (b *ToolBuilder) BoolParam(name, desc string, required bool) *ToolBuilder {
	b.params = append(b.params, paramDef{Name: name, Type: "boolean", Description: desc})
	if required {
		b.required = append(b.required, name)
	}
	return b
}

func (b *ToolBuilder) ObjectParam(name, desc string, required bool) *ToolBuilder {
	b.params = append(b.params, paramDef{Name: name, Type: "object", Description: desc})
	if required {
		b.required = append(b.required, name)
	}
	return b
}

func (b *ToolBuilder) Tags(tags ...string) *ToolBuilder {
	b.tags = append(b.tags, tags...)
	return b
}

func (b *ToolBuilder) Timeout(d time.Duration) *ToolBuilder {
	b.timeout = d
	return b
}

func (b *ToolBuilder) ExecSnippet(fn agent.ExecSnippetFunc) *ToolBuilder {
	b.execSnippet = fn
	return b
}

// ExecSnippetField is a convenience shortcut for the common pattern of
// extracting a single field from args and displaying it as a snippet.
// For example, ExecSnippetField("path") produces snippets like `"foo.txt"`.
func (b *ToolBuilder) ExecSnippetField(field string) *ToolBuilder {
	b.execSnippet = func(args json.RawMessage) string {
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return ""
		}
		v, ok := m[field]
		if !ok {
			return ""
		}
		s, ok := v.(string)
		if !ok || s == "" {
			return ""
		}
		return `"` + s + `"`
	}
	return b
}

func (b *ToolBuilder) Handler(fn agent.ToolHandler) *ToolBuilder {
	b.handler = fn
	return b
}

func (b *ToolBuilder) Build() agent.Tool {
	properties := make(map[string]any, len(b.params))
	for _, p := range b.params {
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		properties[p.Name] = prop
	}

	params := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(b.required) > 0 {
		params["required"] = b.required
	}

	t := agent.Tool{
		Schema: agent.ToolSchema{
			Name:        b.name,
			Description: b.description,
			Parameters:  params,
		},
		Tags:        b.tags,
		Timeout:     b.timeout,
		ExecSnippet: b.execSnippet,
		Handler:     b.handler,
	}
	return t
}

// execSnippetOneLine returns the first non-empty string field value as-is
// (no quoting), suitable for commands or expressions.
func execSnippetOneLine(field string) agent.ExecSnippetFunc {
	return func(args json.RawMessage) string {
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return ""
		}
		v, ok := m[field]
		if !ok {
			return ""
		}
		s, ok := v.(string)
		if !ok || s == "" {
			return ""
		}
		return s
	}
}

// execSnippetPatternWithPath returns an ExecSnippet that shows a
// search-like pattern with optional path.
func execSnippetPatternWithPath() agent.ExecSnippetFunc {
	return func(args json.RawMessage) string {
		var params struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return ""
		}
		snippet := `pattern="` + params.Pattern + `"`
		if params.Path != "" && params.Path != "." {
			snippet += ` path="` + params.Path + `"`
		}
		return snippet
	}
}

// execSnippetRange returns an ExecSnippet that shows min..max.
func execSnippetRange() agent.ExecSnippetFunc {
	return func(args json.RawMessage) string {
		var params struct {
			Min int `json:"min_value"`
			Max int `json:"max_value"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return ""
		}
		return fmt.Sprintf("%d..%d", params.Min, params.Max)
	}
}
