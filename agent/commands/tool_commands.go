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

type ToolCommands struct {
	Sessions *agent.SessionManager
	Tools    *agent.ToolRegistry
	NS       string
}

func NewToolCommands(sm *agent.SessionManager, tools *agent.ToolRegistry, ns string, reg *CommandRegistry) *ToolCommands {
	tc := &ToolCommands{Sessions: sm, Tools: tools, NS: ns}

	reg.Register(Command{
		Name: "tool_list", Description: "List available tools with status", Display: "tool list",
		Handler: tc.jsonToolList,
	})
	reg.Register(Command{
		Name: "tool_allow", Description: "Allow (and enable) a tool or tag", Display: "tool allow",
		Params:  map[string]string{"name": "tool name or #tag"},
		Handler: tc.jsonToolAllow,
	})
	reg.Register(Command{
		Name: "tool_deny", Description: "Deny (hide) a tool or tag", Display: "tool deny",
		Params:  map[string]string{"name": "tool name or #tag"},
		Handler: tc.jsonToolDeny,
	})

	return tc
}

func (tc *ToolCommands) jsonToolList(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sessionForCommand(tc.Sessions, ctx, ns, sessionID)
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

	session, err := sessionForCommand(tc.Sessions, ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}
	if err := session.AllowAndEnableTool(ctx, p.Name); err != nil {
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

	session, err := sessionForCommand(tc.Sessions, ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}
	if err := session.DenyTool(ctx, p.Name); err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Reply: fmt.Sprintf("error: %v", err)}
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

	session, err := sessionForCommand(tc.Sessions, ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
	}

	names := make([]string, 0, len(tagged))
	for _, ts := range tagged {
		if err := session.AllowAndEnableTool(ctx, ts.Name); err != nil {
			return CommandResult{Handled: true, Cmd: "tool_allow", Error: err}
		}
		names = append(names, ts.Name)
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

	session, err := sessionForCommand(tc.Sessions, ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "tool_deny", Error: err}
	}

	names := make([]string, 0, len(tagged))
	for _, ts := range tagged {
		if err := session.DenyTool(ctx, ts.Name); err != nil {
			return CommandResult{Handled: true, Cmd: "tool_deny", Reply: fmt.Sprintf("error denying tool %s: %v", ts.Name, err)}
		}
		names = append(names, ts.Name)
	}

	data, _ := json.Marshal(map[string]any{"denied": names, "tag": tag})
	return CommandResult{Handled: true, Cmd: "tool_deny", Data: data, Session: session}
}

func (tc *ToolCommands) collectTagged(tags []string) []agent.ToolSchema {
	var allTagged []agent.ToolSchema
	for _, tag := range tags {
		allTagged = append(allTagged, tc.Tools.SchemasByTags(tag)...)
	}
	return allTagged
}
