package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// ToolCommands handles /tool slash commands.
// ---------------------------------------------------------------------------

// ToolCommands handles tool-related slash commands.
// Namespace is resolved from context at each operation, so the same
// handler can serve multiple isolated namespaces concurrently.
type ToolCommands struct {
	Sessions *agent.SessionManager
	Tools    *agent.ToolRegistry // gateway's tool registry; used by /tool commands
	NS       string              // fallback namespace (used when context has none)
}

// NewToolCommands builds a ToolCommands handler.
func NewToolCommands(sm *agent.SessionManager, tools *agent.ToolRegistry, ns string) *ToolCommands {
	return &ToolCommands{Sessions: sm, Tools: tools, NS: ns}
}

// Handle dispatches to the appropriate sub-handler.
// Parts[0] is "/tool", Parts[1] is the subcommand.
func (tc *ToolCommands) Handle(ctx context.Context, sessionID string, parts []string) CommandResult {
	if len(parts) < 2 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "tool usage:\n  /tool list\n  /tool enable <name-or-#tag>...\n  /tool disable <name-or-#tag>..."}
	}

	sub := parts[1]
	switch sub {
	case "list":
		return tc.handleToolList(ctx, sessionID)
	case "enable":
		return tc.enableTool(ctx, sessionID, parts)
	case "disable":
		return tc.disableTool(ctx, sessionID, parts)
	default:
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("unknown tool subcommand %q", sub)}
	}
}

func (tc *ToolCommands) enableTool(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a tool name or #tag to enable"}
	}

	// Check if any value starts with '#' — if so, use tag logic.
	values := parts[2:]
	if isTagValue(values[0]) {
		return tc.enableToolsByTag(ctx, sessionID, values)
	}

	name := parts[2]

	if tc.Tools != nil {
		if _, ok := tc.Tools.Get(name); !ok {
			return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("unknown tool: %s", name)}
		}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	session.EnableTool(name)
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "enabled: " + name, Session: session}
}

func (tc *ToolCommands) disableTool(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a tool name or #tag to disable"}
	}

	// Check if any value starts with '#' — if so, use tag logic.
	values := parts[2:]
	if isTagValue(values[0]) {
		return tc.disableToolsByTag(ctx, sessionID, values)
	}

	name := parts[2]

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	session.DisableTool(name)
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "disabled: " + name, Session: session}
}

// isTagValue checks if a value starts with '#' (hashtag), indicating a tag reference.
func isTagValue(v string) bool {
	return strings.HasPrefix(v, "#")
}

// stripTagPrefix removes the leading '#' from a tag value.
func stripTagPrefix(v string) string {
	return strings.TrimPrefix(v, "#")
}

func (tc *ToolCommands) enableToolsByTag(ctx context.Context, sessionID string, values []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(values) < 1 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify at least one tag to enable (e.g. /tool enable #filesystem #dangerous)"}
	}

	// Strip '#' prefix from tag values.
	tags := make([]string, len(values))
	for i, v := range values {
		tags[i] = stripTagPrefix(v)
	}

	if tc.Tools == nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tool registry configured"}
	}

	allTagged := tc.collectTagged(tags)
	if len(allTagged) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("no tools with tags %q", tags)}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	names := make([]string, 0, len(allTagged))
	for _, ts := range allTagged {
		session.EnableTool(ts.Name)
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	tagList := strings.Join(tags, ", ")
	return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("enabled by tags %q: %s", "#"+tagList, strings.Join(names, ", ")), Session: session}
}

func (tc *ToolCommands) disableToolsByTag(ctx context.Context, sessionID string, values []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(values) < 1 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify at least one tag to disable (e.g. /tool disable #filesystem #dangerous)"}
	}

	// Strip '#' prefix from tag values.
	tags := make([]string, len(values))
	for i, v := range values {
		tags[i] = stripTagPrefix(v)
	}

	if tc.Tools == nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tool registry configured"}
	}

	allTagged := tc.collectTagged(tags)
	if len(allTagged) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("no tools with tags %q", tags)}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	names := make([]string, 0, len(allTagged))
	for _, ts := range allTagged {
		session.DisableTool(ts.Name)
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	tagList := strings.Join(tags, ", ")
	return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("disabled by tags %q: %s", "#"+tagList, strings.Join(names, ", ")), Session: session}
}

// collectTagged returns schemas for tools matching ANY of the given tags, deduped.
func (tc *ToolCommands) collectTagged(tags []string) []agent.ToolSchema {
	var allTagged []agent.ToolSchema
	seen := make(map[string]bool)
	for _, tag := range tags {
		tagged := tc.Tools.SchemasByTags(tag)
		for _, ts := range tagged {
			if !seen[ts.Name] {
				seen[ts.Name] = true
				allTagged = append(allTagged, ts)
			}
		}
	}
	return allTagged
}

func (tc *ToolCommands) handleToolList(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	if tc.Tools == nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tool registry configured", Session: session}
	}

	allTools := tc.Tools.Schemas()
	if len(allTools) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tools available", Session: session}
	}

	sorted := make([]agent.ToolSchema, len(allTools))
	copy(sorted, allTools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	lines := make([]string, 0, len(sorted)+1)
	lines = append(lines, "available tools:")

	for _, ts := range sorted {
		tool, _ := tc.Tools.Get(ts.Name)
		status := "[disabled]"
		if session.IsToolEnabled(ts.Name) {
			status = "[enabled] "
		}
		tagStr := ""
		if len(tool.Tags) > 0 {
			tagStr = "  (tags: #" + strings.Join(tool.Tags, ", #") + ")"
		}
		lines = append(lines, fmt.Sprintf("  %-25s %s%s", ts.Name, status, tagStr))
	}

	return CommandResult{Handled: true, Action: ActionReply, Reply: strings.Join(lines, "\n"), Session: session}
}
