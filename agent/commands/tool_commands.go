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

// ToolCommands handles tool-related commands.
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
	session.EnableTool(p.Name)
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
