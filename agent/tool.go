package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

// SchemasForSession returns the tool schemas that should appear in the
// API "tools" section for the given session. These are tools that are
// both ALLOWED (not denied) and ENABLED.
//
// Tools that are allowed but disabled are NOT included here — they
// appear only in the system prompt list (via AllowedSchemas).
func (reg *ToolRegistry) SchemasForSession(session SessionToolAuth) []ToolSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	if session == nil {
		return nil
	}

	out := make([]ToolSchema, 0)
	for _, t := range reg.tools {
		if session.IsToolAllowed(t.Schema.Name) && session.IsToolEnabled(t.Schema.Name) {
			out = append(out, t.Schema)
		}
	}
	return out
}

// AllowedSchemas returns all tool schemas that are allowed (not denied)
// for the given session. This includes both enabled and disabled tools.
// Used to build the system prompt listing.
func (reg *ToolRegistry) AllowedSchemas(session SessionToolAuth) []ToolSchema {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	if session == nil {
		return nil
	}

	out := make([]ToolSchema, 0)
	for _, t := range reg.tools {
		if session.IsToolAllowed(t.Schema.Name) {
			out = append(out, t.Schema)
		}
	}
	return out
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
	IsToolAllowed(name string) bool
}

// ExecuteForSession runs the named tool, but first checks that the tool is
// allowed (not denied) for the given session. If denied, returns a typed
// error result without invoking the handler.
//
// In the new model (v2), tools that are allowed but disabled can still
// execute — this is "mutual activation": if the LLM manages to call a
// disabled tool (e.g. via provider-specific function calling bypass),
// we honour the call rather than reject it.
func (reg *ToolRegistry) ExecuteForSession(ctx context.Context, session toolSession, name, rawArgs string) event.ToolResult {
	if session != nil && !session.IsToolAllowed(name) {
		reg.log().Warn("execute: tool denied for session", "tool", name, "session", session.SessionID())
		return event.ErrorResult(fmt.Errorf("tool %q is denied for this session", name))
	}
	return reg.Execute(ctx, name, rawArgs)
}

// BuildToolListMessage returns a system message listing all allowed tools
// with their descriptions and Pythonic signatures. This is injected into
// the conversation context so the LLM knows what tools are available
// (and which ones are enabled vs disabled).
func BuildToolListMessage(tools *ToolRegistry, session SessionToolAuth) string {
	if tools == nil || session == nil {
		return ""
	}

	schemas := tools.AllowedSchemas(session)
	if len(schemas) == 0 {
		return ""
	}

	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].Name < schemas[j].Name
	})

	// Precompute signatures and find longest for alignment
	type item struct {
		sig  string
		desc string
	}
	items := make([]item, len(schemas))
	maxLen := 0
	for i, s := range schemas {
		sig := s.Signature()
		items[i] = item{sig: sig, desc: compactDescription(s.Description)}
		if len(sig) > maxLen {
			maxLen = len(sig)
		}
	}

	var b strings.Builder
	b.WriteString("You are allowed to use next tools:\n")
	for _, it := range items {
		b.WriteString(fmt.Sprintf("  %-*s - %s\n", maxLen, it.sig, it.desc))
	}
	b.WriteString("\nTo activate any tool or get detailed info, use show_tool call.")
	return b.String()
}

// appendToolListMessage injects a tool list system message into the
// message array. It is inserted after any existing system messages but
// before the conversation history, ensuring the LLM always sees the
// current tool availability status.
func appendToolListMessage(msgs []Message, tools *ToolRegistry, session SessionToolAuth) []Message {
	if tools == nil || session == nil {
		return msgs
	}
	msg := BuildToolListMessage(tools, session)
	if msg == "" {
		return msgs
	}
	toolMsg := Message{Role: RoleSystem, Content: msg}
	// Find the last system message position
	insertAt := 0
	for i, m := range msgs {
		if m.Role == RoleSystem {
			insertAt = i + 1
		} else {
			break
		}
	}
	enriched := make([]Message, 0, len(msgs)+1)
	enriched = append(enriched, msgs[:insertAt]...)
	enriched = append(enriched, toolMsg)
	enriched = append(enriched, msgs[insertAt:]...)
	return enriched
}

// compactDescription returns a compact version of a tool description by
// taking the first sentence and truncating if needed.
func compactDescription(desc string) string {
	// Take first sentence
	if idx := strings.Index(desc, "."); idx > 0 {
		desc = desc[:idx+1]
	}
	// Truncate at reasonable length
	if len(desc) > 100 {
		desc = desc[:97] + "..."
	}
	return desc
}

// Signature returns a Pythonic function signature for the tool schema.
// Examples:
//
//	read_file(path, offset=0, limit=200000)
//	random(min_value, max_value)
//	session_create()
//	edit_file(path, old, new, replace_all=False)
func (s ToolSchema) Signature() string {
	props, ok := s.Parameters["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return s.Name
	}

	// Build the required set — handle both []string and []any (JSON unmarshal).
	requiredSet := make(map[string]bool)
	if req, ok := s.Parameters["required"].([]string); ok {
		for _, name := range req {
			requiredSet[name] = true
		}
	} else if req, ok := s.Parameters["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				requiredSet[name] = true
			}
		}
	}

	var required, optional []string
	for name := range props {
		if requiredSet[name] {
			required = append(required, name)
		} else {
			optional = append(optional, name)
		}
	}
	sort.Strings(required)
	sort.Strings(optional)

	parts := make([]string, 0, len(required)+len(optional))
	for _, name := range required {
		parts = append(parts, name)
	}
	for _, name := range optional {
		parts = append(parts, formatOptionalParam(props, name))
	}

	if len(parts) == 0 {
		return s.Name + "()"
	}
	return s.Name + "(" + strings.Join(parts, ", ") + ")"
}

// formatOptionalParam formats an optional parameter with its default value.
// Priority:
//  1. Extract explicit default from description via (default X) or Default X. patterns
//  2. Boolean → False
//  3. Otherwise → None
func formatOptionalParam(props map[string]any, name string) string {
	prop, ok := props[name].(map[string]any)
	if !ok {
		return name + "=None"
	}

	desc, _ := prop["description"].(string)
	if defaultVal := extractDefaultFromDescription(desc); defaultVal != "" {
		return name + "=" + defaultVal
	}

	typ, _ := prop["type"].(string)
	if typ == "boolean" {
		return name + "=False"
	}
	return name + "=None"
}

// extractDefaultFromDescription searches for common default-value patterns
// in a parameter description string. Returns "" if no default is found.
func extractDefaultFromDescription(desc string) string {
	if desc == "" {
		return ""
	}

	// Pattern 1: "(default X)" — e.g. "(default 200_000)" or "(default 64KB)"
	const parenPrefix = "(default "
	if idx := strings.Index(desc, parenPrefix); idx >= 0 {
		rest := desc[idx+len(parenPrefix):]
		end := strings.IndexAny(rest, ").")
		if end < 0 {
			end = len(rest)
		}
		val := strings.TrimSpace(rest[:end])
		return cleanDefaultValue(val)
	}

	// Pattern 2: "Default X." — e.g. "Default 30." or "Default 50"
	const defaultPrefix = "Default "
	if idx := strings.Index(desc, defaultPrefix); idx >= 0 {
		rest := desc[idx+len(defaultPrefix):]
		end := strings.IndexAny(rest, " .")
		if end < 0 {
			end = len(rest)
		}
		val := strings.TrimSpace(rest[:end])
		return cleanDefaultValue(val)
	}

	return ""
}

// cleanDefaultValue normalises a raw default value string extracted from
// a description. Handles Go numeric underscores and KB/MB suffixes.
func cleanDefaultValue(val string) string {
	// Remove Go numeric underscores
	val = strings.ReplaceAll(val, "_", "")

	// Handle KB suffix (e.g. "64KB" → 65536)
	if strings.HasSuffix(val, "KB") {
		numStr := strings.TrimSuffix(val, "KB")
		if num, err := fmt.Sscanf(numStr, "%d", new(int)); err == nil && num == 1 {
			var n int
			fmt.Sscanf(numStr, "%d", &n)
			return fmt.Sprintf("%d", n*1024)
		}
	}

	// Handle MB suffix
	if strings.HasSuffix(val, "MB") {
		numStr := strings.TrimSuffix(val, "MB")
		if num, err := fmt.Sscanf(numStr, "%d", new(int)); err == nil && num == 1 {
			var n int
			fmt.Sscanf(numStr, "%d", &n)
			return fmt.Sprintf("%d", n*1024*1024)
		}
	}

	return val
}
