package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ToolCommands handles tool-related slash commands.
type ToolCommands struct {
	Sessions *agent.SessionManager
	Tools    *agent.ToolRegistry
	NS       string
}

func NewToolCommands(sm *agent.SessionManager, tools *agent.ToolRegistry, ns string) *ToolCommands {
	return &ToolCommands{Sessions: sm, Tools: tools, NS: ns}
}

// HandleJSON handles a structured JSON tool command.
func (tc *ToolCommands) HandleJSON(ctx context.Context, sessionID, cmd string, params json.RawMessage) CommandResult {
	switch cmd {
	case "tool_list":
		return tc.jsonToolList(ctx, sessionID)
	case "tool_allow":
		return tc.jsonToolAllow(ctx, sessionID, params)
	case "tool_deny":
		return tc.jsonToolDeny(ctx, sessionID, params)
	}
	return CommandResult{Handled: false}
}

func (tc *ToolCommands) jsonToolList(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_list", Error: err}
	}

	if tc.Tools == nil {
		return CommandResult{Handled: true, Cmd: "tool_list", Reply: "no tool registry configured", Session: session}
	}

	allTools := tc.Tools.Schemas()
	sorted := make([]agent.ToolSchema, len(allTools))
	copy(sorted, allTools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	toolsList := make([]map[string]any, 0, len(sorted))
	for _, ts := range sorted {
		tool, _ := tc.Tools.Get(ts.Name)
		toolsList = append(toolsList, map[string]any{
			"name":        ts.Name,
			"description": ts.Description,
			"enabled":     session.IsToolEnabled(ts.Name),
			"tags":        tool.Tags,
		})
	}

	data, _ := json.Marshal(map[string]any{"tools": toolsList})
	return CommandResult{Handled: true, Cmd: "tool_list", Data: data, Session: session}
}

func (tc *ToolCommands) jsonToolAllow(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		Name string `json:"name"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Name == "" {
		return CommandResult{Handled: true, Cmd: "tool_allow", Reply: "error: please specify a tool name or #tag"}
	}

	// Tag allow
	if strings.HasPrefix(p.Name, "#") {
		return tc.jsonAllowByTag(ctx, sessionID, strings.TrimPrefix(p.Name, "#"))
	}

	if tc.Tools != nil {
		if _, ok := tc.Tools.Get(p.Name); !ok {
			return CommandResult{Handled: true, Cmd: "tool_allow", Reply: fmt.Sprintf("unknown tool: %s", p.Name)}
		}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}
	session.AllowTool(p.Name)
	session.EnableTool(p.Name) // Also enable so it appears in API "tools" section
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}

	data, _ := json.Marshal(map[string]any{"allowed": []string{p.Name}})
	return CommandResult{Handled: true, Cmd: "tool_allow", Data: data, Session: session}
}

func (tc *ToolCommands) jsonToolDeny(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		Name string `json:"name"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Name == "" {
		return CommandResult{Handled: true, Cmd: "tool_deny", Reply: "error: please specify a tool name or #tag"}
	}

	if strings.HasPrefix(p.Name, "#") {
		return tc.jsonDenyByTag(ctx, sessionID, strings.TrimPrefix(p.Name, "#"))
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}
	if err := session.DenyTool(p.Name); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Reply: fmt.Sprintf("error: %v", err)}
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}

	data, _ := json.Marshal(map[string]any{"denied": []string{p.Name}})
	return CommandResult{Handled: true, Cmd: "tool_deny", Data: data, Session: session}
}

func (tc *ToolCommands) jsonAllowByTag(ctx context.Context, sessionID, tag string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if tc.Tools == nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Reply: "no tool registry configured"}
	}

	tagged := tc.collectTagged([]string{tag})
	if len(tagged) == 0 {
		return CommandResult{Handled: true, Cmd: "tool_allow", Reply: fmt.Sprintf("no tools with tag #%s", tag)}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}

	names := make([]string, 0, len(tagged))
	for _, ts := range tagged {
		session.AllowTool(ts.Name)
		session.EnableTool(ts.Name)
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}

	data, _ := json.Marshal(map[string]any{"allowed": names, "tag": tag})
	return CommandResult{Handled: true, Cmd: "tool_allow", Data: data, Session: session}
}

func (tc *ToolCommands) jsonDenyByTag(ctx context.Context, sessionID, tag string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if tc.Tools == nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Reply: "no tool registry configured"}
	}

	tagged := tc.collectTagged([]string{tag})
	if len(tagged) == 0 {
		return CommandResult{Handled: true, Cmd: "tool_deny", Reply: fmt.Sprintf("no tools with tag #%s", tag)}
	}

	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}

	names := make([]string, 0, len(tagged))
	for _, ts := range tagged {
		if err := session.DenyTool(ts.Name); err != nil {
			return CommandResult{Handled: true, Cmd: "tool_deny", Reply: fmt.Sprintf("error denying tool %s: %v", ts.Name, err)}
		}
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}

	data, _ := json.Marshal(map[string]any{"denied": names, "tag": tag})
	return CommandResult{Handled: true, Cmd: "tool_deny", Data: data, Session: session}
}

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

// --- Text-based handlers ---

func (tc *ToolCommands) Handle(ctx context.Context, sessionID string, parts []string) CommandResult {
	if len(parts) < 2 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "tool usage:\n  /tool list\n  /tool allow <name-or-#tag>...\n  /tool deny <name-or-#tag>..."}
	}

	sub := parts[1]
	switch sub {
	case "list":
		return tc.handleToolList(ctx, sessionID)
	case "allow":
		return tc.allowTool(ctx, sessionID, parts)
	case "deny":
		return tc.denyTool(ctx, sessionID, parts)
	default:
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("unknown tool subcommand %q", sub)}
	}
}

func (tc *ToolCommands) allowTool(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a tool name or #tag to allow"}
	}
	values := parts[2:]
	if isTagValue(values[0]) {
		return tc.allowToolsByTag(ctx, sessionID, values)
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
	session.AllowTool(name)
	session.EnableTool(name) // Also enable so it appears in API "tools" section
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "allowed: " + name, Session: session}
}

func (tc *ToolCommands) denyTool(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a tool name or #tag to deny"}
	}
	values := parts[2:]
	if isTagValue(values[0]) {
		return tc.denyToolsByTag(ctx, sessionID, values)
	}
	name := parts[2]
	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if err := session.DenyTool(name); err != nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("error: %v", err)}
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "denied: " + name, Session: session}
}

func isTagValue(v string) bool {
	return strings.HasPrefix(v, "#")
}

func stripTagPrefix(v string) string {
	return strings.TrimPrefix(v, "#")
}

func (tc *ToolCommands) allowToolsByTag(ctx context.Context, sessionID string, values []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(values) < 1 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify at least one tag to allow"}
	}
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
		session.AllowTool(ts.Name)
		session.EnableTool(ts.Name)
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	tagList := strings.Join(tags, ", ")
	return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("allowed by tags %q: %s", "#"+tagList, strings.Join(names, ", ")), Session: session}
}

func (tc *ToolCommands) denyToolsByTag(ctx context.Context, sessionID string, values []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(values) < 1 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify at least one tag to deny"}
	}
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
		if err := session.DenyTool(ts.Name); err != nil {
			return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("error denying %s: %v", ts.Name, err)}
		}
		names = append(names, ts.Name)
	}
	if err := tc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	tagList := strings.Join(tags, ", ")
	return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("denied by tags %q: %s", "#"+tagList, strings.Join(names, ", ")), Session: session}
}

func (tc *ToolCommands) handleToolList(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := tc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if tc.Tools == nil {
		data, _ := json.Marshal(map[string]any{"tools": []any{}})
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tool registry configured", Data: data, Session: session}
	}
	allTools := tc.Tools.Schemas()
	if len(allTools) == 0 {
		data, _ := json.Marshal(map[string]any{"tools": []any{}})
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no tools available", Data: data, Session: session}
	}
	sorted := make([]agent.ToolSchema, len(allTools))
	copy(sorted, allTools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Build structured data and text reply in one pass.
	toolsList := make([]map[string]any, 0, len(sorted))
	lines := make([]string, 0, len(sorted)+1)
	lines = append(lines, "available tools:")
	for _, ts := range sorted {
		tool, _ := tc.Tools.Get(ts.Name)
		allowed := session.IsToolAllowed(ts.Name)
		enabled := session.IsToolEnabled(ts.Name)
		toolsList = append(toolsList, map[string]any{
			"name":        ts.Name,
			"description": ts.Description,
			"allowed":     allowed,
			"enabled":     enabled,
			"tags":        tool.Tags,
		})
		status := "[denied] "
		if !allowed {
			status = "[denied] "
		} else if enabled {
			status = "[enabled] "
		} else {
			status = "[allowed] "
		}
		tagStr := ""
		if len(tool.Tags) > 0 {
			tagStr = "  (tags: #" + strings.Join(tool.Tags, ", #") + ")"
		}
		lines = append(lines, fmt.Sprintf("  %-25s %s%s", ts.Name, status, tagStr))
	}

	data, _ := json.Marshal(map[string]any{"tools": toolsList})
	return CommandResult{Handled: true, Cmd: "tool_list", Action: ActionReply, Reply: strings.Join(lines, "\n"), Data: data, Session: session}
}
