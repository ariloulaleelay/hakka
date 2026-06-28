package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ToolSchema is a JSON-schema-style description forwarded to the LLM.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolHandler executes a tool. args is raw JSON as produced by the model.
// The returned string is sent back to the model as the tool result; if it
// is not already JSON, the engine will still pass it through verbatim.
type ToolHandler func(ctx context.Context, args json.RawMessage) (string, error)

// ExecSnippetFunc extracts a short human-readable summary of the arguments
// that will be shown to the user before the tool executes. The returned
// string should be ≤ 60 characters. Return "" to show only the tool name.
type ExecSnippetFunc func(args json.RawMessage) string

type Tool struct {
	Schema      ToolSchema
	Handler     ToolHandler
	Timeout     time.Duration
	ExecSnippet ExecSnippetFunc // optional; produces user-facing execution snippet
	Tags        []string        // groups/tags for filtering, e.g. ["filesystem", "read"]
}

// ToolRegistry is a goroutine-safe collection of tools.
type ToolRegistry struct {
	mu     sync.RWMutex
	tools  map[string]Tool
	Logger *slog.Logger // optional; defaults to slog.Default()
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (reg *ToolRegistry) Register(t Tool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.tools[t.Schema.Name] = t
}

func (reg *ToolRegistry) Get(name string) (Tool, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	t, ok := reg.tools[name]
	return t, ok
}

func (reg *ToolRegistry) Schemas() []ToolSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]ToolSchema, 0, len(reg.tools))
	for _, t := range reg.tools {
		out = append(out, t.Schema)
	}
	return out
}

// SchemasForSession returns the tool schemas available for the given
// session.
//
// Tools tagged "tool" (e.g. list_tools, enable_tool, show_tool) are
// available by default — they let the LLM discover and enable other tools
// at runtime. However, if the user explicitly disables such a tool via
// session.DisableTool(), it is excluded.
//
// All other tools follow the session's opt-in model: only tools
// explicitly enabled via EnableTool() are included.
//
// Depends only on SessionToolAuth — the narrowest interface needed.
func (reg *ToolRegistry) SchemasForSession(session SessionToolAuth) []ToolSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	if session == nil {
		return nil
	}

	out := make([]ToolSchema, 0)
	for _, t := range reg.tools {
		if reg.isAlwaysEnabled(t) {
			// Include "tool"-tagged tools by default, unless explicitly disabled.
			if session.IsToolConfigured(t.Schema.Name) && !session.IsToolEnabled(t.Schema.Name) {
				continue // explicitly disabled
			}
			out = append(out, t.Schema)
		} else if session.IsToolEnabled(t.Schema.Name) {
			out = append(out, t.Schema)
		}
	}
	return out
}

// isAlwaysEnabled checks whether a tool is always available by default
// (no explicit enable needed). Tools with the "tool" tag (meta-tools for
// tool management) are available by default, but can still be explicitly
// disabled.
func (reg *ToolRegistry) isAlwaysEnabled(t Tool) bool {
	for _, tag := range t.Tags {
		if tag == "tool" {
			return true
		}
	}
	return false
}

// SchemasByTags returns schemas for tools that match ANY of the given tags.
// Does NOT apply session-level filtering.
func (reg *ToolRegistry) SchemasByTags(tags ...string) []ToolSchema {
	if len(tags) == 0 {
		return reg.Schemas()
	}

	reg.mu.RLock()
	defer reg.mu.RUnlock()

	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}

	out := make([]ToolSchema, 0)
	for _, t := range reg.tools {
		for _, tag := range t.Tags {
			if tagSet[tag] {
				out = append(out, t.Schema)
				break
			}
		}
	}
	return out
}

// AllTags returns all unique tags across all registered tools, sorted.
func (reg *ToolRegistry) AllTags() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	tagSet := make(map[string]bool)
	for _, t := range reg.tools {
		for _, tag := range t.Tags {
			tagSet[tag] = true
		}
	}

	out := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// ExecSnippet returns a short user-facing summary for the named tool's
// arguments, or "" if the tool has no snippet function.
func (reg *ToolRegistry) ExecSnippet(name, rawArgs string) string {
	t, ok := reg.Get(name)
	if !ok || t.ExecSnippet == nil {
		return ""
	}
	return t.ExecSnippet(json.RawMessage(rawArgs))
}

// log returns the registry's logger, falling back to slog.Default().
func (reg *ToolRegistry) log() *slog.Logger {
	if reg.Logger != nil {
		return reg.Logger
	}
	return slog.Default()
}

// Execute runs the named tool and returns a typed event.ToolResult.
// On success, Result.Output carries the model-facing payload. On a
// recoverable handler error, Result.Err is set and Result.ForLLM()
// renders the canonical "Error: <message>" line that gets appended to
// the conversation history. Tools never wrap errors in JSON; the
// textual rendering happens at one boundary (ForLLM).
func (reg *ToolRegistry) Execute(ctx context.Context, name, rawArgs string) event.ToolResult {
	t, ok := reg.Get(name)
	if !ok {
		reg.log().Warn("execute: unknown tool", "tool", name)
		return event.ErrorResult(fmt.Errorf("unknown tool: %s", name))
	}
	if rawArgs == "" {
		rawArgs = "{}"
	}

	reg.log().Debug("execute: starting tool", "tool", name, "args", rawArgs)

	runCtx := ctx
	if t.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
		reg.log().Debug("execute: tool has timeout", "tool", name, "timeout", t.Timeout)
	}

	res, err := t.Handler(runCtx, json.RawMessage(rawArgs))
	if err != nil {
		reg.log().Error("execute: tool failed", "tool", name, "error", err)
		return event.ErrorResult(err)
	}

	reg.log().Debug("execute: tool succeeded", "tool", name)
	return event.SuccessResult(res)
}

// toolSession is the narrow interface that ExecuteForSession depends on.
// Defined locally (consumer-site interface) to minimise coupling.
type toolSession interface {
	SessionID() string
	IsToolEnabled(name string) bool
	IsToolConfigured(name string) bool
}

// ExecuteForSession runs the named tool, but first checks that the tool is
// enabled for the given session. If disabled, returns a typed error
// result without invoking the handler. This provides defense-in-depth
// even if the LLM somehow calls a disabled tool (e.g. from context window).
//
// Tools tagged "tool" (e.g. list_tools, enable_tool, show_tool) are
// executable by default, unless the user has explicitly disabled them.
func (reg *ToolRegistry) ExecuteForSession(ctx context.Context, session toolSession, name, rawArgs string) event.ToolResult {
	if session != nil && !session.IsToolEnabled(name) {
		// Before rejecting, check if this is a "tool"-tagged tool that is
		// simply not configured yet (available by default).
		if t, ok := reg.Get(name); ok && reg.isAlwaysEnabled(t) {
			if !session.IsToolConfigured(name) {
				// Not configured → available by default.
				return reg.Execute(ctx, name, rawArgs)
			}
			// Configured but disabled → reject.
			reg.log().Warn("execute: tool explicitly disabled for session", "tool", name, "session", session.SessionID())
			return event.ErrorResult(errors.New("tool '" + name + "' is disabled for this session"))
		}
		reg.log().Warn("execute: tool disabled for session", "tool", name, "session", session.SessionID())
		return event.ErrorResult(errors.New("tool '" + name + "' is disabled for this session"))
	}
	return reg.Execute(ctx, name, rawArgs)
}
